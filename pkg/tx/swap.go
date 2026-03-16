package tx

import (
	"encoding/hex"
	"fmt"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/vulpemventures/go-elements/elementsutil"
	"github.com/vulpemventures/go-elements/transaction"
)

// SwapParams holds inputs for a swap transaction.
type SwapParams struct {
	// Pool state
	State *pool.State
	// Which direction: true = Asset0→Asset1, false = Asset1→Asset0
	SwapAsset0In bool
	// Amount in (satoshis)
	AmountIn uint64
	// Minimum acceptable output (slippage guard, checked off-chain only)
	MinAmountOut uint64
	// User's input UTXO (provides AmountIn of the input asset)
	UserTxID  string
	UserVout  uint32
	UserAsset string
	// Where to send output asset
	UserOutputAddr string
	// Pool output addresses (from pool.json)
	PoolAAddr string
	PoolBAddr string
	// Asset IDs
	Asset0    string
	Asset1    string
	LBTCAsset string
	Fee       uint64
	// Fee parameters — must match what was compiled into the contracts.
	// Read from pool.json (Config.FeeNum / Config.FeeDen).
	FeeNum uint64
	FeeDen uint64
	// Pool swap-variant binaries, CMRs, and control blocks
	PoolABinaryHex    string
	PoolBBinaryHex    string
	PoolACMRHex       string
	PoolBCMRHex       string
	PoolAControlBlock string
	PoolBControlBlock string
}

// SwapResult holds the completed swap transaction outputs.
type SwapResult struct {
	TxHex        string
	AmountOut    uint64
	PoolAWitness [][]byte // attach to input[0] after wallet signing
	PoolBWitness [][]byte // attach to input[1] after wallet signing
}

// BuildSwap builds a swap transaction.
// Input layout: [pool_a, pool_b, user_input]
// Output layout: [new_pool_a, new_pool_b, user_output, fee]
//
// NOTE: Pool witnesses (PoolAWitness, PoolBWitness) are returned separately
// and must be attached to inputs[0,1] AFTER signing with the wallet, because
// signrawtransactionwithwallet cannot process transactions with pre-existing
// witness data.
func BuildSwap(params *SwapParams) (*SwapResult, error) {
	st := params.State

	var reserveIn, reserveOut uint64
	if params.SwapAsset0In {
		reserveIn, reserveOut = st.Reserve0, st.Reserve1
	} else {
		reserveIn, reserveOut = st.Reserve1, st.Reserve0
	}

	amountOut := pool.SwapOutput(params.AmountIn, reserveIn, reserveOut, params.FeeNum, params.FeeDen)
	if amountOut < params.MinAmountOut {
		return nil, fmt.Errorf("output %d < minimum %d (slippage exceeded)", amountOut, params.MinAmountOut)
	}

	var newReserve0, newReserve1 uint64
	if params.SwapAsset0In {
		newReserve0 = st.Reserve0 + params.AmountIn
		newReserve1 = st.Reserve1 - amountOut
	} else {
		newReserve0 = st.Reserve0 - amountOut
		newReserve1 = st.Reserve1 + params.AmountIn
	}

	tx := transaction.NewTx(2)

	// Input[0]: pool_a
	poolATxid, _ := elementsutil.TxIDToBytes(st.PoolATxID)
	tx.AddInput(transaction.NewTxInput(poolATxid, st.PoolAVout))

	// Input[1]: pool_b
	poolBTxid, _ := elementsutil.TxIDToBytes(st.PoolBTxID)
	tx.AddInput(transaction.NewTxInput(poolBTxid, st.PoolBVout))

	// Input[2]: user's asset input
	userTxid, _ := elementsutil.TxIDToBytes(params.UserTxID)
	tx.AddInput(transaction.NewTxInput(userTxid, params.UserVout))

	// Output[0]: new pool_a
	poolAOut, err := buildOutput(params.PoolAAddr, params.Asset0, newReserve0)
	if err != nil {
		return nil, fmt.Errorf("pool_a out: %w", err)
	}
	tx.AddOutput(poolAOut)

	// Output[1]: new pool_b
	poolBOut, err := buildOutput(params.PoolBAddr, params.Asset1, newReserve1)
	if err != nil {
		return nil, fmt.Errorf("pool_b out: %w", err)
	}
	tx.AddOutput(poolBOut)

	// Output[2]: user receives output asset
	outAsset := params.Asset1
	if !params.SwapAsset0In {
		outAsset = params.Asset0
	}
	userOut, err := buildOutput(params.UserOutputAddr, outAsset, amountOut)
	if err != nil {
		return nil, fmt.Errorf("user out: %w", err)
	}
	tx.AddOutput(userOut)

	// Output[3]: fee
	feeOut, err := buildFeeOutput(params.LBTCAsset, params.Fee)
	if err != nil {
		return nil, fmt.Errorf("fee out: %w", err)
	}
	tx.AddOutput(feeOut)

	// Build witnesses but do NOT attach to the transaction before serializing.
	// signrawtransactionwithwallet fails on transactions with pre-existing witness data.
	// The caller must: sign (input[2]), then attach these witnesses to inputs[0,1].
	poolAWit, err := noWitnessWithCB(params.PoolABinaryHex, params.PoolACMRHex, params.PoolAControlBlock)
	if err != nil {
		return nil, fmt.Errorf("pool_a witness: %w", err)
	}
	poolBWit, err := noWitnessWithCB(params.PoolBBinaryHex, params.PoolBCMRHex, params.PoolBControlBlock)
	if err != nil {
		return nil, fmt.Errorf("pool_b witness: %w", err)
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		return nil, err
	}
	return &SwapResult{
		TxHex:        hex.EncodeToString(txBytes),
		AmountOut:    amountOut,
		PoolAWitness: poolAWit,
		PoolBWitness: poolBWit,
	}, nil
}

