package tx

import (
	"encoding/hex"
	"fmt"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/vulpemventures/go-elements/elementsutil"
	"github.com/vulpemventures/go-elements/transaction"
)

// RemoveLiquidityParams holds inputs for a remove-liquidity transaction.
type RemoveLiquidityParams struct {
	State        *pool.State
	LPBurned     uint64 // LP tokens to return to reserve
	UserLPAmount uint64 // full LP UTXO balance (input[3] total); change returned to user
	// Pool output addresses
	PoolAAddr     string
	PoolBAddr     string
	LpReserveAddr string
	// User's LP token UTXO (input[3] = LP_RETURN_INPUT in contracts)
	UserLPTxID string
	UserLPVout uint32
	// User's L-BTC UTXO for fee (input[4])
	UserLBTCTxID   string
	UserLBTCVout   uint32
	UserLBTCAmount uint64 // actual UTXO amount; change returned if > Fee
	// Where to send payouts
	UserAsset0Addr string // receives payout0 of Asset0
	UserAsset1Addr string // receives payout1 of Asset1
	ChangeAddr     string // receives LP change and L-BTC change
	// Asset IDs
	Asset0    string
	Asset1    string
	LPAssetID string
	LBTCAsset string
	Fee       uint64
	// Pool remove-variant binaries, CMRs, and control blocks
	PoolABinaryHex        string
	PoolBBinaryHex        string
	LpReserveBinaryHex    string
	PoolACMRHex           string
	PoolBCMRHex           string
	LpReserveCMRHex       string
	PoolAControlBlock     string
	PoolBControlBlock     string
	LpReserveControlBlock string
}

// RemoveLiquidityResult holds the completed remove-liquidity transaction.
type RemoveLiquidityResult struct {
	TxHex            string
	Payout0          uint64   // Asset0 paid to user
	Payout1          uint64   // Asset1 paid to user
	PoolAWitness     [][]byte // attach to input[0] after wallet signing
	PoolBWitness     [][]byte // attach to input[1] after wallet signing
	LpReserveWitness [][]byte // attach to input[2] after wallet signing
}

// BuildRemoveLiquidity builds a remove-liquidity transaction.
//
// Input layout:  [pool_a(0), pool_b(1), lp_reserve(2), user_lp(3), user_lbtc(4)]
// Output layout: [new_pool_a(0), new_pool_b(1), new_lp_reserve(2),
//
//	payout0(3), payout1(4),
//	lp_change→user(5, if any), lbtc_change→user(6, if any),
//	fee(last)]
//
// LP tokens are returned to the LP Reserve (not destroyed). The reserve increases
// by LPBurned. If UserLPAmount > LPBurned, the remainder is returned as LP change.
//
// The user must provide an L-BTC UTXO (input[4]) to cover the on-chain fee.
// Any surplus L-BTC is returned as change.
//
// Pool witnesses are returned separately and must be attached AFTER wallet signing.
func BuildRemoveLiquidity(params *RemoveLiquidityParams) (*RemoveLiquidityResult, error) {
	st := params.State
	totalSupply := st.TotalSupply()

	payout0, payout1 := pool.RemovePayouts(params.LPBurned, st.Reserve0, st.Reserve1, totalSupply)
	newReserve0 := st.Reserve0 - payout0
	newReserve1 := st.Reserve1 - payout1
	newLpReserve := st.LPReserve + params.LPBurned

	tx := transaction.NewTx(2)

	// Inputs: pool_a(0), pool_b(1), lp_reserve(2)
	addPoolInputs(tx, st)

	// Input[3]: user's LP token UTXO (LP_RETURN_INPUT) — wallet-signed
	lpTxidBytes, _ := elementsutil.TxIDToBytes(params.UserLPTxID)
	tx.AddInput(transaction.NewTxInput(lpTxidBytes, params.UserLPVout))

	// Input[4]: user's L-BTC UTXO for fee — wallet-signed
	addInput(tx, params.UserLBTCTxID, params.UserLBTCVout)

	// Output[0]: new pool_a
	out0, err := buildOutput(params.PoolAAddr, params.Asset0, newReserve0)
	if err != nil {
		return nil, fmt.Errorf("pool_a out: %w", err)
	}
	tx.AddOutput(out0)

	// Output[1]: new pool_b
	out1, err := buildOutput(params.PoolBAddr, params.Asset1, newReserve1)
	if err != nil {
		return nil, fmt.Errorf("pool_b out: %w", err)
	}
	tx.AddOutput(out1)

	// Output[2]: new lp_reserve (reserve + lpBurned LP tokens)
	out2, err := buildOutput(params.LpReserveAddr, params.LPAssetID, newLpReserve)
	if err != nil {
		return nil, fmt.Errorf("lp_reserve out: %w", err)
	}
	tx.AddOutput(out2)

	// Output[3]: payout0 (Asset0) to user
	if payout0 > 0 {
		out3, err := buildOutput(params.UserAsset0Addr, params.Asset0, payout0)
		if err != nil {
			return nil, fmt.Errorf("payout0 out: %w", err)
		}
		tx.AddOutput(out3)
	}

	// Output[4]: payout1 (Asset1) to user
	if payout1 > 0 {
		out4, err := buildOutput(params.UserAsset1Addr, params.Asset1, payout1)
		if err != nil {
			return nil, fmt.Errorf("payout1 out: %w", err)
		}
		tx.AddOutput(out4)
	}

	// LP token change back to user (if partial return)
	if lpChange := params.UserLPAmount - params.LPBurned; lpChange > 0 && params.ChangeAddr != "" {
		changeOut, err := buildOutput(params.ChangeAddr, params.LPAssetID, lpChange)
		if err != nil {
			return nil, fmt.Errorf("lp change out: %w", err)
		}
		tx.AddOutput(changeOut)
	}

	// L-BTC change back to user (if fee UTXO > fee)
	if lbtcChange := params.UserLBTCAmount - params.Fee; lbtcChange > 0 && params.ChangeAddr != "" {
		changeOut, err := buildOutput(params.ChangeAddr, params.LBTCAsset, lbtcChange)
		if err != nil {
			return nil, fmt.Errorf("lbtc change out: %w", err)
		}
		tx.AddOutput(changeOut)
	}

	// fee (L-BTC → empty script, must be last)
	feeOut, err := buildFeeOutput(params.LBTCAsset, params.Fee)
	if err != nil {
		return nil, fmt.Errorf("fee out: %w", err)
	}
	tx.AddOutput(feeOut)

	// Build pool witnesses but do NOT attach.
	poolAWit, err := noWitnessWithCB(params.PoolABinaryHex, params.PoolACMRHex, params.PoolAControlBlock)
	if err != nil {
		return nil, fmt.Errorf("pool_a witness: %w", err)
	}
	poolBWit, err := noWitnessWithCB(params.PoolBBinaryHex, params.PoolBCMRHex, params.PoolBControlBlock)
	if err != nil {
		return nil, fmt.Errorf("pool_b witness: %w", err)
	}
	lpWit, err := noWitnessWithCB(params.LpReserveBinaryHex, params.LpReserveCMRHex, params.LpReserveControlBlock)
	if err != nil {
		return nil, fmt.Errorf("lp_reserve witness: %w", err)
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		return nil, err
	}
	return &RemoveLiquidityResult{
		TxHex:            hex.EncodeToString(txBytes),
		Payout0:          payout0,
		Payout1:          payout1,
		PoolAWitness:     poolAWit,
		PoolBWitness:     poolBWit,
		LpReserveWitness: lpWit,
	}, nil
}
