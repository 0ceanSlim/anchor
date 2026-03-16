//go:build ignore

// Anchor AMM — Pool B Swap/Add Variant
//
// Canonical split variant for the pool_b contract. Handles swaps and
// add-liquidity operations. Use pool_b_remove.go for remove-liquidity.
//
// This file is the canonical source for build/pool_b_swap.shl.
// Transpile with: simgo -input contracts/pool_b_swap.go -output build/pool_b_swap.shl
// Or:             make transpile
//
// Asset IDs are placeholder zeros — overridden by build/pool_b_swap.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (swap or add-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3+] User UTXOs
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3+] User outputs
//
// Mirror of pool_a_swap.go from the asset1 perspective.
// Pool B holds asset1; it reads reserve0 from Pool UTXO A as a sibling input.

package main

import "simplicity/jet"

// Asset0 and Asset1 are placeholder IDs overridden by .args at compile time.
const Asset0 = 0x0000000000000000000000000000000000000000000000000000000000000000
const Asset1 = 0x0000000000000000000000000000000000000000000000000000000000000001

// Fee parameters — placeholders overridden by .args at compile time.
// FeeNum/FeeDen represent the fee fraction kept by the pool (e.g. 997/1000 = 0.3% fee).
// FeeDiff = FeeDen - FeeNum (the fee amount).
const FeeNum  uint64 = 1
const FeeDen  uint64 = 1
const FeeDiff uint64 = 0

// PoolInputA is the input index of Pool UTXO A (asset0 side).
const PoolInputA uint32 = 0

// PoolOutputA is the output index for the continuing Pool UTXO A.
const PoolOutputA uint32 = 0

// PoolOutputB is the output index for the continuing Pool UTXO B.
const PoolOutputB uint32 = 1

func main() {
	// Read current reserves (pool B holds asset1).
	reserve1 := jet.CurrentAmount()
	reserve0 := jet.InputAmount(PoolInputA)

	// Read proposed new reserves from transaction outputs.
	newReserve0 := jet.OutputAmount(PoolOutputA)
	newReserve1 := jet.OutputAmount(PoolOutputB)

	// Verify correct assets are present in the new pool outputs.
	asset0Out := jet.OutputAsset(PoolOutputA)
	asset1Out := jet.OutputAsset(PoolOutputB)
	jet.Verify(asset0Out == Asset0)
	jet.Verify(asset1Out == Asset1)

	// Self-covenant: Pool UTXO B must be re-locked by this same script.
	newScriptB := jet.OutputScriptHash(PoolOutputB)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newScriptB == myScript)

	// Fee-adjusted invariant (pool_b perspective): (r1*(D-N) + newR1*N) * newR0 >= r1*D * r0
	// where N=FeeNum, D=FeeDen, D-N=FeeDiff. Enforces the 0.3% swap fee.
	jet.Verify(jet.FeeAdjustedLe128(reserve1, newReserve1, FeeNum, FeeDiff, FeeDen, newReserve0, reserve0))
}
