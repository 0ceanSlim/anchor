//go:build ignore

// Anchor AMM — Pool Contract A (asset0 side)
//
// REFERENCE ONLY — This combined contract uses a CASE node (mode-detection match)
// and cannot be deployed on Liquid (SIMPLICITY_ERR_ANTIDOS). See pool_a_swap.go
// and pool_a_remove.go for the deployable split variants.
//
// This contract locks the pool UTXO that holds asset0. It supports three
// transaction modes detected automatically from reserve changes:
//
//   Add Liquidity  — both reserves increase; LP Supply UTXO at input 2 is spent
//   Swap           — one reserve increases; k-invariant enforced
//   Remove Liquidity — reserve0 decreases; proportional payout verified against
//                      LP tokens burned at input 3
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
// Pool Contract B runs concurrently and enforces the symmetric checks.
//
// Usage:
//   simgo -input contracts/pool_a.go -output build/pool_a.shl

package main

import "simplicity/jet"

// Asset0 is the canonical "smaller" asset ID for this pool (lexicographic order).
const Asset0 = 0x25b251070e29ca19043cf33ccd7324e2ddab03ecc4ae0b5e77c4fc0e5cf6c95a

// Asset1 is the canonical "larger" asset ID for this pool.
const Asset1 = 0xce091c998b83c78bb71a632313ba3760f1763d9cfcffae02258ffa9865a37bd2

// LpAssetId is the Liquid asset ID of the LP token for this pool.
// Computed deterministically from the pool creation input outpoint before signing.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000001

// PoolInputB is the transaction input index for Pool UTXO B (asset1 side).
const PoolInputB uint32 = 1

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
	reserve0 := jet.CurrentAmount()
	reserve1 := jet.InputAmount(PoolInputB)

	// --- Read proposed new reserves from transaction outputs ---
	newReserve0 := jet.OutputAmount(PoolOutputA)
	newReserve1 := jet.OutputAmount(PoolOutputB)

	// --- Verify correct assets in new pool outputs ---
	asset0Out := jet.OutputAsset(PoolOutputA)
	asset1Out := jet.OutputAsset(PoolOutputB)
	jet.Verify(asset0Out == Asset0)
	jet.Verify(asset1Out == Asset1)

	// --- Self-covenant: new Pool UTXO A locked by same script as this one ---
	newScriptA := jet.OutputScriptHash(PoolOutputA)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newScriptA == myScript)

	// --- Mode detection ---
	// Remove mode: reserve0 is decreasing (payout sent to user).
	// Swap/add mode: reserve0 is non-decreasing (k-invariant enforced).
	isRemoveMode := jet.Lt64(newReserve0, reserve0)

	if isRemoveMode {
		// --- Remove Liquidity ---
		// Verify LP tokens burned proportionally to payout.
		totalSupply := jet.InputAmount(LpSupplyInput)
		lpBurned := jet.InputAmount(LpBurnInput)
		lpAsset := jet.InputAsset(LpBurnInput)
		jet.Verify(lpAsset == LpAssetId)

		// payout0 = reserve0 - newReserve0
		payout0 := reserve0 - newReserve0

		// floor:   payout0 * totalSupply <= lpBurned * reserve0
		payout0TimesSupply := jet.Multiply64(payout0, totalSupply)
		lpBurnedTimesReserve0 := jet.Multiply64(lpBurned, reserve0)
		jet.Verify(jet.Le128(payout0TimesSupply, lpBurnedTimesReserve0))

		// ceiling: lpBurned * reserve0 <= (payout0+1) * totalSupply
		payout0Plus1 := payout0 + 1
		payout0Plus1TimesSupply := jet.Multiply64(payout0Plus1, totalSupply)
		jet.Verify(jet.Le128(lpBurnedTimesReserve0, payout0Plus1TimesSupply))
	} else {
		// --- Swap or Add Liquidity ---
		// Constant-product invariant: k = reserve0 * reserve1 must not decrease.
		kOld := jet.Multiply64(reserve0, reserve1)
		kNew := jet.Multiply64(newReserve0, newReserve1)
		jet.Verify(jet.Le128(kOld, kNew))
	}
}
