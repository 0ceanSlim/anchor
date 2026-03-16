// Package tx builds Elements transactions for Anchor AMM operations.
package tx

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"

	anchorTaproot "github.com/0ceanslim/anchor/pkg/taproot"
)

// simplicityWitness builds the input witness for a Simplicity script-path spend.
//
// The Elements Simplicity taproot witness format is 4 items:
//
//	[witness_bitstream, program_binary, cmr_32_bytes, control_block]
//
// After the node pops cmr and control_block, the remaining stack has 2 items:
// [witness_bitstream, program_binary], which the Simplicity verifier uses.
//
// witnessVals: Simplicity witness values encoded as byte slices (big-endian).
// programHex: hex-encoded Simplicity binary.
// cmrHex: hex-encoded 32-byte Commitment Merkle Root (taproot leaf content).
func simplicityWitness(programHex, cmrHex string, witnessVals ...[]byte) ([][]byte, error) {
	program, err := hex.DecodeString(programHex)
	if err != nil {
		return nil, fmt.Errorf("decode program: %w", err)
	}
	cmrBytes, err := hex.DecodeString(cmrHex)
	if err != nil {
		return nil, fmt.Errorf("decode cmr: %w", err)
	}
	cb, err := anchorTaproot.ControlBlock(cmrBytes)
	if err != nil {
		return nil, fmt.Errorf("control block: %w", err)
	}

	// Witness stack: [val1, val2, ..., program, cmr, control_block]
	stack := make([][]byte, 0, len(witnessVals)+3)
	stack = append(stack, witnessVals...)
	stack = append(stack, program, cmrBytes, cb)
	return stack, nil
}

// u64BE encodes a uint64 as 8 bytes big-endian (Simplicity u64 encoding).
func u64BE(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// noWitness builds a Simplicity witness with no witness values (single-leaf taproot).
// An empty bitstream item is prepended even when there are no witness values,
// because the Elements node always expects the full 4-item stack format:
// [witness_bitstream, program_binary, cmr_32_bytes, control_block].
func noWitness(programHex, cmrHex string) ([][]byte, error) {
	return simplicityWitness(programHex, cmrHex, []byte{})
}

// noWitnessWithCB builds a Simplicity witness using a pre-computed control block.
// Use this for dual-leaf taproot spends (swap/add/remove pool scripts).
// Stack format: [empty_bitstream, program_binary, cmr_32_bytes, control_block]
func noWitnessWithCB(programHex, cmrHex, controlBlockHex string) ([][]byte, error) {
	program, err := hex.DecodeString(programHex)
	if err != nil {
		return nil, fmt.Errorf("decode program: %w", err)
	}
	cmrBytes, err := hex.DecodeString(cmrHex)
	if err != nil {
		return nil, fmt.Errorf("decode cmr: %w", err)
	}
	cb, err := hex.DecodeString(controlBlockHex)
	if err != nil {
		return nil, fmt.Errorf("decode control block: %w", err)
	}
	// Empty bitstream required even for programs with no witness values.
	return [][]byte{[]byte{}, program, cmrBytes, cb}, nil
}

// lpMintedWitness builds the witness for pool_creation (LP_MINTED: u64).
func lpMintedWitness(programHex, cmrHex string, lpMinted uint64) ([][]byte, error) {
	return simplicityWitness(programHex, cmrHex, u64BE(lpMinted))
}
