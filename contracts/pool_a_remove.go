//go:build ignore

// Anchor AMM — Pool A Remove Variant
//
// Canonical split variant for the pool_a contract. Handles remove-liquidity
// operations only. Use pool_a_swap.go for swaps and add-liquidity.
//
// This file is the canonical source for build/pool_a_remove.shl.
// Transpile with: simgo -input contracts/pool_a_remove.go -output build/pool_a_remove.shl
// Or:             make transpile
//
// Asset IDs are placeholder zeros — overridden by build/pool_a_remove.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (remove-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Reserve UTXO
//            [3] User LP UTXO   [4] User L-BTC (fee)
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Reserve
//            [3] payout0   [4] payout1   [5+] change   [last] fee
//
// Security invariant: payout0 is proportional to LP tokens returned, bounded
// within ±1 satoshi (floor/ceiling enforced with Le128 checks).

package main

import "simplicity/jet"

// Asset0 and Asset1 are placeholder IDs overridden by .args at compile time.
const Asset0 = 0x0000000000000000000000000000000000000000000000000000000000000000
const Asset1 = 0x0000000000000000000000000000000000000000000000000000000000000001

// LpAssetId is a placeholder overridden by .args at compile time.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000002

// LpPremint is the total pre-minted LP token supply (Elements MAX_MONEY).
const LpPremint uint64 = 0

// PoolInputB is the input index of Pool UTXO B (asset1 side).
const PoolInputB uint32 = 1

// LpReserveInput is the input index of the LP Reserve UTXO.
const LpReserveInput uint32 = 2

// LpReturnInput is the input index of the user's LP token UTXO.
const LpReturnInput uint32 = 3

// PoolOutputA is the output index for the continuing Pool UTXO A.
const PoolOutputA uint32 = 0

// PoolOutputB is the output index for the continuing Pool UTXO B.
const PoolOutputB uint32 = 1

// LpReserveOutput is the output index for the continuing LP Reserve UTXO.
const LpReserveOutput uint32 = 2

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

	// Mode guard: assert remove mode (reserve0 must decrease).
	jet.Verify(jet.Lt64(newReserve0, reserve0))

	// Derive total supply from LP Reserve: total_supply = LP_PREMINT - lp_reserve
	lpReserve := jet.InputAmount(LpReserveInput)
	newLpReserve := jet.OutputAmount(LpReserveOutput)
	totalSupply := LpPremint - lpReserve

	// lpBurned = newLpReserve - lpReserve (reserve increases by tokens returned)
	lpBurned := newLpReserve - lpReserve

	// Verify the returned tokens carry the correct LP asset.
	lpAsset := jet.InputAsset(LpReturnInput)
	jet.Verify(lpAsset == LpAssetId)

	// payout0 = reserve0 - newReserve0
	payout0 := reserve0 - newReserve0

	// Floor:   payout0 * totalSupply <= lpBurned * reserve0
	payout0TimesSupply := jet.Multiply64(payout0, totalSupply)
	lpBurnedTimesReserve0 := jet.Multiply64(lpBurned, reserve0)
	jet.Verify(jet.Le128(payout0TimesSupply, lpBurnedTimesReserve0))

	// Ceiling: lpBurned * reserve0 <= (payout0+1) * totalSupply
	payout0Plus1 := payout0 + 1
	payout0Plus1TimesSupply := jet.Multiply64(payout0Plus1, totalSupply)
	jet.Verify(jet.Le128(lpBurnedTimesReserve0, payout0Plus1TimesSupply))

	// Suppress unused variable warnings.
	_ = newReserve1
}
