package tx

import (
	"encoding/binary"
	"encoding/hex"
	"math"
	"testing"
)

func TestU64BE(t *testing.T) {
	tests := []struct {
		name string
		val  uint64
		want uint64
	}{
		{"zero", 0, 0},
		{"one", 1, 1},
		{"max", math.MaxUint64, math.MaxUint64},
		{"mid", 1_000_000, 1_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := u64BE(tt.val)
			if len(b) != 8 {
				t.Fatalf("expected 8 bytes, got %d", len(b))
			}
			got := binary.BigEndian.Uint64(b)
			if got != tt.want {
				t.Errorf("got %d, want %d", got, tt.want)
			}
		})
	}
}

func TestReverseBytes(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"empty", []byte{}, []byte{}},
		{"single", []byte{0xab}, []byte{0xab}},
		{"even", []byte{0x01, 0x02, 0x03, 0x04}, []byte{0x04, 0x03, 0x02, 0x01}},
		{"odd", []byte{0x01, 0x02, 0x03}, []byte{0x03, 0x02, 0x01}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reverseBytes(tt.in)
			if len(got) != len(tt.want) {
				t.Fatalf("length mismatch: got %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("byte %d: got %02x, want %02x", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestComputeIssuanceEntropy(t *testing.T) {
	// Deterministic: same txid+vout always produces the same entropy.
	txid := "e378d2d5ff45bfc7a407fbb59d03cec81cef5af9696b4ce92167c361651a913c"
	entropy1, err := ComputeIssuanceEntropy(txid, 0)
	if err != nil {
		t.Fatalf("ComputeIssuanceEntropy: %v", err)
	}
	if len(entropy1) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(entropy1))
	}
	// Same inputs → same output
	entropy2, err := ComputeIssuanceEntropy(txid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(entropy1) != hex.EncodeToString(entropy2) {
		t.Error("entropy not deterministic")
	}
	// Different vout → different output
	entropy3, err := ComputeIssuanceEntropy(txid, 1)
	if err != nil {
		t.Fatal(err)
	}
	if hex.EncodeToString(entropy1) == hex.EncodeToString(entropy3) {
		t.Error("different vout should produce different entropy")
	}
}

func TestComputeLPAssetID(t *testing.T) {
	txid := "e378d2d5ff45bfc7a407fbb59d03cec81cef5af9696b4ce92167c361651a913c"
	id1, err := ComputeLPAssetID(txid, 0)
	if err != nil {
		t.Fatalf("ComputeLPAssetID: %v", err)
	}
	// Must be 32 bytes, non-zero
	allZero := true
	for _, b := range id1 {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Error("LP asset ID should not be all zeros")
	}
	// Deterministic
	id2, err := ComputeLPAssetID(txid, 0)
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Error("LP asset ID not deterministic")
	}
}

func TestSimplicityWitness(t *testing.T) {
	// Use minimal hex-encoded values
	programHex := "deadbeef"
	cmrHex := "0000000000000000000000000000000000000000000000000000000000000001"

	stack, err := simplicityWitness(programHex, cmrHex, []byte{0x42})
	if err != nil {
		t.Fatalf("simplicityWitness: %v", err)
	}
	// Stack: [witnessVal, program, cmr, controlBlock] = 4 elements
	if len(stack) != 4 {
		t.Fatalf("expected 4 stack items, got %d", len(stack))
	}
	// First element is the witness value
	if len(stack[0]) != 1 || stack[0][0] != 0x42 {
		t.Errorf("witness value: got %x, want 42", stack[0])
	}
	// Second element is the decoded program
	if hex.EncodeToString(stack[1]) != programHex {
		t.Errorf("program mismatch")
	}
	// Third element is the CMR bytes
	if hex.EncodeToString(stack[2]) != cmrHex {
		t.Errorf("cmr mismatch")
	}
}

func TestNoWitness(t *testing.T) {
	programHex := "deadbeef"
	cmrHex := "0000000000000000000000000000000000000000000000000000000000000001"

	stack, err := noWitness(programHex, cmrHex)
	if err != nil {
		t.Fatalf("noWitness: %v", err)
	}
	// Stack: [empty_bitstream, program, cmr, controlBlock] = 4 items
	if len(stack) != 4 {
		t.Fatalf("expected 4 stack items, got %d", len(stack))
	}
	// First element must be empty bitstream
	if len(stack[0]) != 0 {
		t.Errorf("expected empty bitstream, got %x", stack[0])
	}
}

func TestNoWitnessWithCB(t *testing.T) {
	programHex := "deadbeef"
	cmrHex := "0000000000000000000000000000000000000000000000000000000000000001"
	cbHex := "c0" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

	stack, err := noWitnessWithCB(programHex, cmrHex, cbHex)
	if err != nil {
		t.Fatalf("noWitnessWithCB: %v", err)
	}
	if len(stack) != 4 {
		t.Fatalf("expected 4 stack items, got %d", len(stack))
	}
	// First element: empty bitstream
	if len(stack[0]) != 0 {
		t.Errorf("expected empty bitstream, got %x", stack[0])
	}
	// Program matches
	if hex.EncodeToString(stack[1]) != programHex {
		t.Errorf("program mismatch")
	}
	// CMR matches
	if hex.EncodeToString(stack[2]) != cmrHex {
		t.Errorf("cmr mismatch")
	}
	// Control block matches
	if hex.EncodeToString(stack[3]) != cbHex {
		t.Errorf("control block mismatch")
	}
}

func TestLpMintedWitness(t *testing.T) {
	programHex := "deadbeef"
	cmrHex := "0000000000000000000000000000000000000000000000000000000000000001"
	lpMinted := uint64(1_000_000)

	stack, err := lpMintedWitness(programHex, cmrHex, lpMinted)
	if err != nil {
		t.Fatalf("lpMintedWitness: %v", err)
	}
	if len(stack) != 4 {
		t.Fatalf("expected 4 stack items, got %d", len(stack))
	}
	// First element is LP_MINTED as big-endian u64
	if len(stack[0]) != 8 {
		t.Fatalf("expected 8-byte LP amount, got %d bytes", len(stack[0]))
	}
	got := binary.BigEndian.Uint64(stack[0])
	if got != lpMinted {
		t.Errorf("LP minted: got %d, want %d", got, lpMinted)
	}
}
