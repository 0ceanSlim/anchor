//go:build ignore

// Anchor AMM — Pool Contract B (asset1 side)
//
// REFERENCE ONLY — This combined contract uses a CASE node (mode-detection match)
// and cannot be deployed on Liquid (SIMPLICITY_ERR_ANTIDOS). See pool_b_swap.go
// and pool_b_remove.go for the deployable split variants.
//
// Mirror of pool_a.go. This contract locks the pool UTXO that holds asset1.
// It runs concurrently with Pool Contract A in every swap, add-liquidity, or
// remove-liquidity transaction, enforcing the same invariant from the asset1
// perspective.
//
// Transaction layouts:
//
//   Swap / Add Liquidity:
//     Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3+] User UTXOs
//     Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3+] User outputs
//
//   Remove Liquidity:
//     Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3] User LP UTXO
//     Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3] payout0  [4] payout1
//
// Usage:
//   simgo -input contracts/pool_b.go -output build/pool_b.shl

package main

import "simplicity/jet"

// Asset0 and Asset1 must match the values in pool_a.go exactly.
const Asset0 = 0x25b251070e29ca19043cf33ccd7324e2ddab03ecc4ae0b5e77c4fc0e5cf6c95a
const Asset1 = 0xce091c998b83c78bb71a632313ba3760f1763d9cfcffae02258ffa9865a37bd2

// LpAssetId must match the value in pool_a.go exactly.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000001

// PoolInputA is the transaction input index for Pool UTXO A (asset0 side).
const PoolInputA uint32 = 0

// LpSupplyInput is the input index of the LP Supply UTXO.
const LpSupplyInput uint32 = 2

// LpBurnInput is the input index of the user's LP token UTXO (remove mode only).
const LpBurnInput uint32 = 3

// PoolOutputA is the transaction output index for the new Pool UTXO A.
const PoolOutputA uint32 = 0

// PoolOutputB is the transaction output index for the new Pool UTXO B.
const PoolOutputB uint32 = 1

func main() {
	// --- Read current reserves ---
	reserve1 := jet.CurrentAmount()
	reserve0 := jet.InputAmount(PoolInputA)

	// --- Read proposed new reserves from transaction outputs ---
	newReserve0 := jet.OutputAmount(PoolOutputA)
	newReserve1 := jet.OutputAmount(PoolOutputB)

	// --- Verify correct assets in new pool outputs ---
	asset0Out := jet.OutputAsset(PoolOutputA)
	asset1Out := jet.OutputAsset(PoolOutputB)
	jet.Verify(asset0Out == Asset0)
	jet.Verify(asset1Out == Asset1)

	// --- Self-covenant: new Pool UTXO B locked by same script as this one ---
	newScriptB := jet.OutputScriptHash(PoolOutputB)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newScriptB == myScript)

	// --- Mode detection ---
	// Remove mode: reserve1 is decreasing (payout sent to user).
	isRemoveMode := jet.Lt64(newReserve1, reserve1)

	if isRemoveMode {
		// --- Remove Liquidity ---
		totalSupply := jet.InputAmount(LpSupplyInput)
		lpBurned := jet.InputAmount(LpBurnInput)
		lpAsset := jet.InputAsset(LpBurnInput)
		jet.Verify(lpAsset == LpAssetId)

		// payout1 = reserve1 - newReserve1
		payout1 := reserve1 - newReserve1

		// floor:   payout1 * totalSupply <= lpBurned * reserve1
		payout1TimesSupply := jet.Multiply64(payout1, totalSupply)
		lpBurnedTimesReserve1 := jet.Multiply64(lpBurned, reserve1)
		jet.Verify(jet.Le128(payout1TimesSupply, lpBurnedTimesReserve1))

		// ceiling: lpBurned * reserve1 <= (payout1+1) * totalSupply
		payout1Plus1 := payout1 + 1
		payout1Plus1TimesSupply := jet.Multiply64(payout1Plus1, totalSupply)
		jet.Verify(jet.Le128(lpBurnedTimesReserve1, payout1Plus1TimesSupply))
	} else {
		// --- Swap or Add Liquidity ---
		kOld := jet.Multiply64(reserve0, reserve1)
		kNew := jet.Multiply64(newReserve0, newReserve1)
		jet.Verify(jet.Le128(kOld, kNew))
	}
}
