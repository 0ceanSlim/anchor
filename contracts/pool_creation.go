//go:build ignore

// Anchor AMM — Pool Creation Contract
//
// This contract is used exactly once to create the pool. The creator's UTXO
// must be locked by this script. When spent, it:
//
//   1. Creates Pool UTXO A (deposit0 of Asset0) at output 0
//   2. Creates Pool UTXO B (deposit1 of Asset1) at output 1
//   3. Creates LP Reserve UTXO (LP_PREMINT - lpMinted LP tokens) at output 2
//   4. Issues LP_PREMINT LP tokens at input 0 (via Liquid issuance)
//   5. Sends lpMinted LP tokens to the creator at output 3
//
// The LP token amount is provided by the creator as a witness value and must
// equal floor(sqrt(deposit0 * deposit1)), verified on-chain.
//
// Transaction layout:
//
//   Inputs:  [0] Creator UTXO (locked by this contract, carries LP issuance)
//            [1] Asset0 deposit UTXO   [2] Asset1 deposit UTXO
//   Outputs: [0] Pool UTXO A (deposit0 of Asset0)
//            [1] Pool UTXO B (deposit1 of Asset1)
//            [2] LP Reserve UTXO (LP_PREMINT - lpMinted LP tokens)
//            [3] LP tokens to creator (lpMinted)
//            [4+] change   [last] fee
//
// Usage:
//   simgo -input contracts/pool_creation.go -output build/pool_creation.shl

package main

import "simplicity/jet"

// Asset0 and Asset1 are placeholder zeros — overridden by build/pool_creation.args
// at simc compile time. Do not edit these constants; run:
//   anchor create-pool --asset0 <hex> --asset1 <hex>
const Asset0 = 0x0000000000000000000000000000000000000000000000000000000000000000
const Asset1 = 0x0000000000000000000000000000000000000000000000000000000000000001

// LpPremint is the total pre-minted LP token supply (Elements MAX_MONEY).
const LpPremint uint64 = 0

func main() {
	// Read initial deposits from the pool outputs.
	deposit0 := jet.OutputAmount(0)
	deposit1 := jet.OutputAmount(1)

	// Verify correct assets in the new pool UTXOs.
	asset0Out := jet.OutputAsset(0)
	asset1Out := jet.OutputAsset(1)
	jet.Verify(asset0Out == Asset0)
	jet.Verify(asset1Out == Asset1)

	// lpMinted is the floor(sqrt(deposit0 * deposit1)), provided by the creator.
	var lpMinted uint64

	// Verify the LP issuance at input 0 equals LP_PREMINT (full pre-mint).
	issuedAmount := jet.IssuanceAssetAmount(0)
	jet.Verify(jet.Eq64(issuedAmount, LpPremint))

	// Verify LP Reserve UTXO gets the remainder (LP_PREMINT - lpMinted).
	lpReserveOut := jet.OutputAmount(2)
	lpReserveExpected := LpPremint - lpMinted
	jet.Verify(jet.Eq64(lpReserveOut, lpReserveExpected))

	// Verify sqrt correctness: lpMinted^2 <= deposit0 * deposit1
	kInitial := jet.Multiply64(deposit0, deposit1)
	lpSq := jet.Multiply64(lpMinted, lpMinted)
	jet.Verify(jet.Le128(lpSq, kInitial))

	// Verify (lpMinted+1)^2 > deposit0 * deposit1
	lpPlus1 := lpMinted + 1
	lpPlus1Sq := jet.Multiply64(lpPlus1, lpPlus1)
	jet.Verify(jet.Lt128(kInitial, lpPlus1Sq))
}
