//go:build ignore

// Anchor AMM — LP Supply Add Variant
//
// Canonical split variant for the lp_supply contract. Handles add-liquidity
// operations only. Use lp_supply_remove.go for remove-liquidity.
//
// This file is the canonical source for build/lp_supply_add.shl.
// Transpile with: simgo -input contracts/lp_supply_add.go -output build/lp_supply_add.shl
// Or:             make transpile
//
// The LP Supply UTXO carries one satoshi of L-BTC per LP token outstanding.
// total_supply always equals the number of LP tokens in circulation.
//
// Asset IDs are placeholder zeros — overridden by build/lp_supply_add.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (add-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3+] User UTXOs
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3+] User outputs
//
// Invariants enforced:
//   deposit1 == floor(deposit0 * reserve1 / reserve0)  (proportional deposit, floor rounding)
//   i.e. deposit1*reserve0 <= deposit0*reserve1 < (deposit1+1)*reserve0
//   floor(deposit0 * totalSupply / reserve0) <= lpMinted <= ceil(...)

package main

import "simplicity/jet"

// PoolAInput is the input (and output) index of Pool UTXO A.
const PoolAInput uint32 = 0

// PoolBInput is the input (and output) index of Pool UTXO B.
const PoolBInput uint32 = 1

// LpSupplyOutput is the output index for the continuing LP Supply UTXO.
const LpSupplyOutput uint32 = 2

func main() {
	// Read current and proposed LP supply.
	totalSupply := jet.CurrentAmount()
	newTotalSupply := jet.OutputAmount(LpSupplyOutput)

	// Self-covenant: LP Supply UTXO must be re-locked by this same script.
	newLpScript := jet.OutputScriptHash(LpSupplyOutput)
	myScript := jet.CurrentScriptHash()
	jet.Verify(newLpScript == myScript)

	// Mode guard: assert add mode (LP supply must increase).
	jet.Verify(jet.Lt64(totalSupply, newTotalSupply))

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

	// lpMinted = newTotalSupply - totalSupply
	lpMinted := newTotalSupply - totalSupply

	// Proportional deposit: deposit1 == floor(deposit0 * reserve1 / reserve0)
	// Equivalently: deposit1*reserve0 <= deposit0*reserve1 < (deposit1+1)*reserve0
	// This allows any deposit0 amount; deposit1 must be the integer floor of the
	// exact proportion. Avoids the coprimality trap of strict equality (where
	// gcd(reserve0,reserve1)=1 would require enormous minimum deposits).
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
