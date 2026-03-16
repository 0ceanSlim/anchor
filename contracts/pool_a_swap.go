//go:build ignore

// Anchor AMM — Pool A Swap/Add Variant
//
// Canonical split variant for the pool_a contract. Handles swaps and
// add-liquidity operations. Use pool_a_remove.go for remove-liquidity.
//
// This file is the canonical source for build/pool_a_swap.shl.
// Transpile with: simgo -input contracts/pool_a_swap.go -output build/pool_a_swap.shl
// Or:             make transpile
//
// Asset IDs are placeholder zeros — overridden by build/pool_a_swap.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (swap or add-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3+] User UTXOs
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3+] User outputs
//
// Security invariant: k_new >= k_old (constant product must not decrease).
// No mode guard is needed — the k check is direction-independent. Liquidity
// cannot be extracted without satisfying the k invariant.

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

// PoolInputB is the input index of Pool UTXO B (asset1 side).
const PoolInputB uint32 = 1

// PoolOutputA is the output index for the continuing Pool UTXO A.
const PoolOutputA uint32 = 0

// PoolOutputB is the output index for the continuing Pool UTXO B.
const PoolOutputB uint32 = 1

func main() {
	// Read current reserves.
	reserve0 := jet.CurrentAmount()
	reserve1 := jet.InputAmount(PoolInputB)

	// Read proposed new reserves from transaction outputs.
	newReserve0 := jet.OutputAmount(PoolOutputA)
	newReserve1 := jet.OutputAmount(PoolOutputB)

	// Verify correct assets are present in the new pool outputs.
	asset0Out := jet.OutputAsset(PoolOutputA)
	asset1Out := jet.OutputAsset(PoolOutputB)
	jet.Verify(asset0Out == Asset0)
	jet.Verify(asset1Out == Asset1)

	// Self-covenant: Pool UTXO A must be re-locked by this same script.
	newScriptA := jet.OutputScriptHash(PoolOutputA)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newScriptA == myScript)

	// Fee-adjusted invariant: (r0*(D-N) + newR0*N) * newR1 >= r0*D * r1
	// where N=FeeNum, D=FeeDen, D-N=FeeDiff. Enforces the 0.3% swap fee.
	jet.Verify(jet.FeeAdjustedLe128(reserve0, newReserve0, FeeNum, FeeDiff, FeeDen, newReserve1, reserve1))
}
