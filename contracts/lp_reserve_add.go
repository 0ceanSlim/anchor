//go:build ignore

// Anchor AMM — LP Reserve Add Variant
//
// Handles add-liquidity operations on the LP Reserve UTXO. Use
// lp_reserve_remove.go for remove-liquidity.
//
// This file is the canonical source for build/lp_reserve_add.shl.
// Transpile with: simgo -input contracts/lp_reserve_add.go -output build/lp_reserve_add.shl
// Or:             make transpile
//
// The LP Reserve UTXO holds LP tokens that have not yet been issued to LPs.
// total_supply (circulating LP tokens) is derived as LP_PREMINT - InputAmount(2).
//
// Asset IDs are placeholder zeros — overridden by build/lp_reserve_add.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (add-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Reserve UTXO
//            [3] User Asset0   [4] User Asset1   [5] User L-BTC (fee)
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Reserve
//            [3] LP tokens → User   [4+] change   [last] fee
//
// Invariants enforced:
//   deposit1 == floor(deposit0 * reserve1 / reserve0)  (proportional deposit, floor rounding)
//   floor(deposit0 * totalSupply / reserve0) <= lpMinted <= ceil(...)
//   lpMinted > 0
//   OutputAsset(2) == LP_ASSET_ID

package main

import "simplicity/jet"

// LpAssetId is a placeholder overridden by .args at compile time.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000002

// LpPremint is the total pre-minted LP token supply (Elements MAX_MONEY).
// Set via .args at compile time but constant across all pools.
const LpPremint uint64 = 0

// PoolAInput is the input (and output) index of Pool UTXO A.
const PoolAInput uint32 = 0

// PoolBInput is the input (and output) index of Pool UTXO B.
const PoolBInput uint32 = 1

// LpReserveOutput is the output index for the continuing LP Reserve UTXO.
const LpReserveOutput uint32 = 2

func main() {
	// Read current and proposed LP reserve amounts.
	lpReserve := jet.CurrentAmount()
	newLpReserve := jet.OutputAmount(LpReserveOutput)

	// Self-covenant: LP Reserve UTXO must be re-locked by this same script.
	newScript := jet.OutputScriptHash(LpReserveOutput)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newScript == myScript)

	// Verify output carries LP tokens.
	outAsset := jet.OutputAsset(LpReserveOutput)
	jet.Verify(outAsset == LpAssetId)

	// Mode guard: assert add mode (LP reserve must decrease — tokens going out).
	jet.Verify(jet.Lt64(newLpReserve, lpReserve))

	// lpMinted = lpReserve - newLpReserve (reserve decreases by tokens minted)
	lpMinted := lpReserve - newLpReserve

	// Derive total supply from the reserve.
	totalSupply := LpPremint - lpReserve

	// Read current pool reserves.
	reserve0 := jet.InputAmount(PoolAInput)
	reserve1 := jet.InputAmount(PoolBInput)

	// Read new pool reserves to derive deposit amounts.
	newReserve0 := jet.OutputAmount(PoolAInput)
	newReserve1 := jet.OutputAmount(PoolBInput)

	// deposit0 = newReserve0 - reserve0
	deposit0 := newReserve0 - reserve0

	// deposit1 = newReserve1 - reserve1
	deposit1 := newReserve1 - reserve1

	// Proportional deposit: deposit1 == floor(deposit0 * reserve1 / reserve0)
	// Equivalently: deposit1*reserve0 <= deposit0*reserve1 < (deposit1+1)*reserve0
	deposit0TimesReserve1 := jet.Multiply64(deposit0, reserve1)
	deposit1TimesReserve0 := jet.Multiply64(deposit1, reserve0)
	jet.Verify(jet.Le128(deposit1TimesReserve0, deposit0TimesReserve1))
	deposit1Plus1 := deposit1 + 1
	deposit1Plus1TimesReserve0 := jet.Multiply64(deposit1Plus1, reserve0)
	jet.Verify(jet.Lt128(deposit0TimesReserve1, deposit1Plus1TimesReserve0))

	// LP mint floor:    lpMinted * reserve0 <= deposit0 * totalSupply
	lpMintedTimesReserve0 := jet.Multiply64(lpMinted, reserve0)
	deposit0TimesSupply := jet.Multiply64(deposit0, totalSupply)
	jet.Verify(jet.Le128(lpMintedTimesReserve0, deposit0TimesSupply))

	// LP mint ceiling:  deposit0 * totalSupply <= (lpMinted+1) * reserve0
	lpMintedPlus1 := lpMinted + 1
	lpMintedPlus1TimesReserve0 := jet.Multiply64(lpMintedPlus1, reserve0)
	jet.Verify(jet.Le128(deposit0TimesSupply, lpMintedPlus1TimesReserve0))
}
