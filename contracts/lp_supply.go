//go:build ignore

// Anchor AMM — LP Supply Contract
//
// REFERENCE ONLY — This combined contract uses a CASE node (mode-detection match)
// and cannot be deployed on Liquid (SIMPLICITY_ERR_ANTIDOS). See lp_supply_add.go
// and lp_supply_remove.go for the deployable split variants.
//
// This contract locks the LP Supply UTXO, which carries total_supply satoshis
// of L-BTC. One satoshi is locked per LP token outstanding, so total_supply
// always equals the total number of LP tokens in circulation.
//
// The contract is spent in every add-liquidity and remove-liquidity transaction.
// It is NOT spent in swaps (LP supply does not change on a swap).
//
// Transaction layouts:
//
//   Add Liquidity:
//     Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3+] User UTXOs
//     Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3+] User outputs
//
//     Add mode: newTotalSupply > totalSupply
//     Verifies: deposit0/reserve0 == deposit1/reserve1  (proportional deposit)
//               lpMinted ≈ deposit0 * totalSupply / reserve0  (floor/ceiling)
//
//   Remove Liquidity:
//     Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3] User LP UTXO
//     Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3] payout0  [4] payout1
//
//     Remove mode: newTotalSupply < totalSupply
//     Verifies: LP tokens at input 3 equal lpBurned = totalSupply - newTotalSupply
//               (payout proportionality enforced by pool_a and pool_b)
//
// Usage:
//   simgo -input contracts/lp_supply.go -output build/lp_supply.shl

package main

import "simplicity/jet"

// Asset0 is the canonical "smaller" asset ID for this pool.
const Asset0 = 0x25b251070e29ca19043cf33ccd7324e2ddab03ecc4ae0b5e77c4fc0e5cf6c95a

// Asset1 is the canonical "larger" asset ID for this pool.
const Asset1 = 0xce091c998b83c78bb71a632313ba3760f1763d9cfcffae02258ffa9865a37bd2

// LpAssetId is the Liquid asset ID of the LP token for this pool.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000001

// PoolAInput is the input index of Pool UTXO A.
const PoolAInput uint32 = 0

// PoolBInput is the input index of Pool UTXO B.
const PoolBInput uint32 = 1

// LpSupplyOutput is the output index for the continuing LP Supply UTXO.
const LpSupplyOutput uint32 = 2

// LpBurnInput is the input index of the user's LP token UTXO (remove mode only).
const LpBurnInput uint32 = 3

func main() {
	// --- Read current and proposed LP supply ---
	totalSupply := jet.CurrentAmount()
	newTotalSupply := jet.OutputAmount(LpSupplyOutput)

	// --- Self-covenant: LP Supply UTXO re-locked by this same script ---
	newLpScript := jet.OutputScriptHash(LpSupplyOutput)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newLpScript == myScript)

	// --- Mode detection ---
	// Add mode: LP supply is increasing (tokens being minted).
	isAddMode := jet.Lt64(totalSupply, newTotalSupply)

	if isAddMode {
		// --- Add Liquidity ---
		// Read current pool reserves.
		reserve0 := jet.InputAmount(PoolAInput)
		reserve1 := jet.InputAmount(PoolBInput)

		// Read new reserves to derive deposit amounts.
		newReserve0 := jet.OutputAmount(PoolAInput)
		newReserve1 := jet.OutputAmount(PoolBInput)

		// deposit0 = newReserve0 - reserve0
		deposit0 := newReserve0 - reserve0

		// deposit1 = newReserve1 - reserve1
		deposit1 := newReserve1 - reserve1

		// lpMinted = newTotalSupply - totalSupply
		lpMinted := newTotalSupply - totalSupply

		// Proportional deposit: deposit0 * reserve1 == deposit1 * reserve0
		deposit0TimesReserve1 := jet.Multiply64(deposit0, reserve1)
		deposit1TimesReserve0 := jet.Multiply64(deposit1, reserve0)
		jet.Verify(jet.Eq128(deposit0TimesReserve1, deposit1TimesReserve0))

		// LP mint floor:    lpMinted * reserve0 <= deposit0 * totalSupply
		lpMintedTimesReserve0 := jet.Multiply64(lpMinted, reserve0)
		deposit0TimesSupply := jet.Multiply64(deposit0, totalSupply)
		jet.Verify(jet.Le128(lpMintedTimesReserve0, deposit0TimesSupply))

		// LP mint ceiling:  deposit0 * totalSupply <= (lpMinted+1) * reserve0
		lpMintedPlus1 := lpMinted + 1
		lpMintedPlus1TimesReserve0 := jet.Multiply64(lpMintedPlus1, reserve0)
		jet.Verify(jet.Le128(deposit0TimesSupply, lpMintedPlus1TimesReserve0))
	} else {
		// --- Remove Liquidity ---
		// lpBurned = totalSupply - newTotalSupply
		lpBurned := totalSupply - newTotalSupply

		// Verify LP tokens at input 3 carry the LP asset and correct amount.
		lpAsset := jet.InputAsset(LpBurnInput)
		jet.Verify(lpAsset == LpAssetId)
		lpAmount := jet.InputAmount(LpBurnInput)
		jet.Verify(jet.Eq64(lpAmount, lpBurned))
	}
}
