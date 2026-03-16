package tx

import (
	"encoding/hex"
	"fmt"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/vulpemventures/go-elements/address"
	"github.com/vulpemventures/go-elements/elementsutil"
	"github.com/vulpemventures/go-elements/transaction"
)

// UserInput holds a single wallet UTXO to spend.
type UserInput struct {
	TxID   string
	Vout   uint32
	Amount uint64 // satoshis
}

// AddLiquidityParams holds inputs for an add-liquidity transaction.
type AddLiquidityParams struct {
	State    *pool.State
	Deposit0 uint64 // satoshis of Asset0 to add
	Deposit1 uint64 // satoshis of Asset1 to add
	// Pool output addresses (from pool.json)
	PoolAAddr     string
	PoolBAddr     string
	LpReserveAddr string
	// User inputs: one or more UTXOs per asset. Change is returned if total > needed.
	Asset0Inputs []UserInput
	Asset1Inputs []UserInput
	LBTCInputs   []UserInput
	ChangeAddr   string // address to receive any surplus from user inputs
	UserLPAddr   string // address to receive LP tokens
	// Asset IDs
	LPAssetID string
	Asset0    string
	Asset1    string
	LBTCAsset string
	Fee       uint64
	// Pool swap/add-variant binaries, CMRs, and control blocks
	PoolABinaryHex         string
	PoolBBinaryHex         string
	LpReserveBinaryHex     string
	PoolACMRHex            string
	PoolBCMRHex            string
	LpReserveCMRHex        string
	PoolAControlBlock      string
	PoolBControlBlock      string
	LpReserveControlBlock  string
}

// AddLiquidityResult holds the completed add-liquidity transaction.
type AddLiquidityResult struct {
	TxHex            string
	LPMinted         uint64
	PoolAWitness     [][]byte // attach to input[0] after wallet signing
	PoolBWitness     [][]byte // attach to input[1] after wallet signing
	LpReserveWitness [][]byte // attach to input[2] after wallet signing
}

// BuildAddLiquidity builds an add-liquidity transaction.
//
// Input layout:
//
//	[0] pool_a   [1] pool_b   [2] lp_reserve
//	[3] user_asset0   [4] user_asset1   [5] user_lbtc (fee)
//
// Output layout:
//
//	[0] new_pool_a   [1] new_pool_b   [2] new_lp_reserve
//	[3] LP tokens → UserLPAddr
//	[4..] change outputs   [last] fee
//
// Pool witnesses are returned in AddLiquidityResult and must be attached to inputs[0,1,2]
// AFTER signing with the wallet.
func BuildAddLiquidity(params *AddLiquidityParams) (*AddLiquidityResult, error) {
	st := params.State
	totalSupply := st.TotalSupply()

	lpMinted := pool.LPMintedForDeposit(
		params.Deposit0, params.Deposit1,
		st.Reserve0, st.Reserve1, totalSupply,
	)
	if lpMinted == 0 {
		return nil, fmt.Errorf("lp_minted is 0 — deposits too small relative to reserves")
	}

	newReserve0 := st.Reserve0 + params.Deposit0
	newReserve1 := st.Reserve1 + params.Deposit1
	newLpReserve := st.LPReserve - lpMinted

	// Sum user input amounts.
	var totalAsset0, totalAsset1, totalLBTC uint64
	for _, inp := range params.Asset0Inputs {
		totalAsset0 += inp.Amount
	}
	for _, inp := range params.Asset1Inputs {
		totalAsset1 += inp.Amount
	}
	for _, inp := range params.LBTCInputs {
		totalLBTC += inp.Amount
	}

	tx := transaction.NewTx(2)

	addPoolInputs(tx, st)

	// User inputs: [3+] asset0, asset1, lbtc (variable count)
	for _, inp := range params.Asset0Inputs {
		addInput(tx, inp.TxID, inp.Vout)
	}
	for _, inp := range params.Asset1Inputs {
		addInput(tx, inp.TxID, inp.Vout)
	}
	for _, inp := range params.LBTCInputs {
		addInput(tx, inp.TxID, inp.Vout)
	}

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

	// Output[2]: new lp_reserve (reserve - lpMinted LP tokens)
	out2, err := buildOutput(params.LpReserveAddr, params.LPAssetID, newLpReserve)
	if err != nil {
		return nil, fmt.Errorf("lp_reserve out: %w", err)
	}
	tx.AddOutput(out2)

	// Output[3]: LP tokens → user
	lpScript, err := address.ToOutputScript(params.UserLPAddr)
	if err != nil {
		return nil, fmt.Errorf("LP recipient address: %w", err)
	}
	lpAssetBytes, err := elementsutil.AssetHashToBytes(params.LPAssetID)
	if err != nil {
		return nil, fmt.Errorf("LP asset bytes: %w", err)
	}
	lpValueBytes, err := elementsutil.ValueToBytes(lpMinted)
	if err != nil {
		return nil, fmt.Errorf("LP value bytes: %w", err)
	}
	tx.AddOutput(transaction.NewTxOutput(lpAssetBytes, lpValueBytes, lpScript))

	// Change outputs for surplus user input amounts.
	if params.ChangeAddr != "" {
		if totalAsset0 > params.Deposit0 {
			chg, err := buildOutput(params.ChangeAddr, params.Asset0, totalAsset0-params.Deposit0)
			if err != nil {
				return nil, fmt.Errorf("asset0 change: %w", err)
			}
			tx.AddOutput(chg)
		}
		if totalAsset1 > params.Deposit1 {
			chg, err := buildOutput(params.ChangeAddr, params.Asset1, totalAsset1-params.Deposit1)
			if err != nil {
				return nil, fmt.Errorf("asset1 change: %w", err)
			}
			tx.AddOutput(chg)
		}
		if totalLBTC > params.Fee {
			chg, err := buildOutput(params.ChangeAddr, params.LBTCAsset, totalLBTC-params.Fee)
			if err != nil {
				return nil, fmt.Errorf("lbtc change: %w", err)
			}
			tx.AddOutput(chg)
		}
	}

	// Output[last]: fee
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
	return &AddLiquidityResult{
		TxHex:            hex.EncodeToString(txBytes),
		LPMinted:         lpMinted,
		PoolAWitness:     poolAWit,
		PoolBWitness:     poolBWit,
		LpReserveWitness: lpWit,
	}, nil
}

// addPoolInputs adds pool_a, pool_b, lp_reserve inputs from state (indices 0, 1, 2).
func addPoolInputs(tx *transaction.Transaction, st *pool.State) {
	addInput(tx, st.PoolATxID, st.PoolAVout)
	addInput(tx, st.PoolBTxID, st.PoolBVout)
	addInput(tx, st.LpReserveTxID, st.LpReserveVout)
}

func addInput(tx *transaction.Transaction, txid string, vout uint32) {
	txidBytes, _ := elementsutil.TxIDToBytes(txid)
	tx.AddInput(transaction.NewTxInput(txidBytes, vout))
}
