//go:build ignore

// Anchor AMM — LP Reserve Remove Variant
//
// Handles remove-liquidity operations on the LP Reserve UTXO. Use
// lp_reserve_add.go for add-liquidity.
//
// This file is the canonical source for build/lp_reserve_remove.shl.
// Transpile with: simgo -input contracts/lp_reserve_remove.go -output build/lp_reserve_remove.shl
// Or:             make transpile
//
// The LP Reserve UTXO holds LP tokens not yet issued to LPs.
// On remove-liquidity, LP tokens are returned from the user to the reserve.
//
// Asset IDs are placeholder zeros — overridden by build/lp_reserve_remove.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (remove-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Reserve UTXO
//            [3] User LP UTXO   [4] User L-BTC (fee)
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Reserve
//            [3] payout0   [4] payout1   [5+] change   [last] fee
//
// Invariants enforced:
//   new_lp_reserve >= lp_reserve  (reserve mode guard — tokens coming in)
//   OutputAsset(2) == LP_ASSET_ID
//   InputAsset(LP_RETURN_INPUT) == LP_ASSET_ID
//   lpReturned <= InputAmount(LP_RETURN_INPUT)

package main

import "simplicity/jet"

// LpAssetId is a placeholder overridden by .args at compile time.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000002

// LpReturnInput is the input index of the user's LP token UTXO.
const LpReturnInput uint32 = 3

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

	// Mode guard: assert remove mode (LP reserve must increase — tokens coming in).
	jet.Verify(jet.Lt64(lpReserve, newLpReserve))

	// lpReturned = newLpReserve - lpReserve (reserve increases by tokens returned)
	lpReturned := newLpReserve - lpReserve

	// Verify the returned tokens carry the correct LP asset.
	lpAsset := jet.InputAsset(LpReturnInput)
	jet.Verify(lpAsset == LpAssetId)

	// Verify the user's LP input has at least the declared return amount.
	// Any surplus LP tokens (lpAmount - lpReturned) are returned to the user
	// automatically by the Elements value balance check.
	lpAmount := jet.InputAmount(LpReturnInput)
	jet.Verify(jet.Le64(lpReturned, lpAmount))
}
