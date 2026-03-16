//go:build ignore

// Anchor AMM — LP Supply Remove Variant
//
// Canonical split variant for the lp_supply contract. Handles remove-liquidity
// operations only. Use lp_supply_add.go for add-liquidity.
//
// This file is the canonical source for build/lp_supply_remove.shl.
// Transpile with: simgo -input contracts/lp_supply_remove.go -output build/lp_supply_remove.shl
// Or:             make transpile
//
// The LP Supply UTXO carries one satoshi of L-BTC per LP token outstanding.
// total_supply always equals the number of LP tokens in circulation.
//
// Asset IDs are placeholder zeros — overridden by build/lp_supply_remove.args at
// simc compile time. Do not edit the constants below; edit the .args file or
// run: anchor create-pool --asset0 <hex> --asset1 <hex>
//
// Transaction layout (remove-liquidity):
//   Inputs:  [0] Pool UTXO A   [1] Pool UTXO B   [2] LP Supply UTXO   [3] User LP UTXO
//   Outputs: [0] New Pool A    [1] New Pool B    [2] New LP Supply     [3] payout0  [4] payout1
//
// Invariants enforced:
//   new_total_supply <= total_supply  (remove mode guard)
//   lp_amount >= lp_burned            (user must hold at least the burned amount;
//                                      remainder flows back to user via Elements balance)

package main

import "simplicity/jet"

// LpAssetId is a placeholder overridden by .args at compile time.
const LpAssetId = 0x0000000000000000000000000000000000000000000000000000000000000002

// LpBurnInput is the input index of the user's LP token UTXO.
const LpBurnInput uint32 = 3

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

	// Mode guard: assert remove mode (LP supply must decrease or stay equal).
	jet.Verify(jet.Le64(newTotalSupply, totalSupply))

	// lpBurned = totalSupply - newTotalSupply
	lpBurned := totalSupply - newTotalSupply

	// Verify the burned tokens carry the correct LP asset.
	lpAsset := jet.InputAsset(LpBurnInput)
	jet.Verify(lpAsset == LpAssetId)

	// Verify the user's LP input is at least the declared burn amount.
	// Any surplus LP tokens (lpAmount - lpBurned) are returned to the user
	// automatically by the Elements value balance check.
	lpAmount := jet.InputAmount(LpBurnInput)
	jet.Verify(jet.Le64(lpBurned, lpAmount))
}
