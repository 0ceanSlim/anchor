package rpc

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// buildAnchorScript constructs a valid ANCHR OP_RETURN script hex for testing.
func buildAnchorScript(asset0, asset1 [32]byte, feeNum, feeDen uint16) string {
	var payload [73]byte
	copy(payload[0:5], "ANCHR")
	copy(payload[5:37], asset0[:])
	copy(payload[37:69], asset1[:])
	binary.BigEndian.PutUint16(payload[69:71], feeNum)
	binary.BigEndian.PutUint16(payload[71:73], feeDen)
	return "6a49" + hex.EncodeToString(payload[:])
}

func TestParseAnchorOutput(t *testing.T) {
	var asset0, asset1 [32]byte
	for i := range asset0 {
		asset0[i] = byte(i + 1)
	}
	for i := range asset1 {
		asset1[i] = byte(i + 0x20)
	}

	script := buildAnchorScript(asset0, asset1, 3, 1000)
	rec, ok := parseAnchorOutput(script, "abc123", 100)
	if !ok {
		t.Fatal("expected successful parse")
	}
	if rec.TxID != "abc123" {
		t.Errorf("txid: got %s", rec.TxID)
	}
	if rec.Height != 100 {
		t.Errorf("height: got %d", rec.Height)
	}
	if rec.FeeNum != 3 {
		t.Errorf("feeNum: got %d", rec.FeeNum)
	}
	if rec.FeeDen != 1000 {
		t.Errorf("feeDen: got %d", rec.FeeDen)
	}
	wantAsset0 := hex.EncodeToString(reverseSlice(asset0[:]))
	if rec.Asset0 != wantAsset0 {
		t.Errorf("asset0: got %s, want %s", rec.Asset0, wantAsset0)
	}
}

func TestParseAnchorOutputTooShort(t *testing.T) {
	_, ok := parseAnchorOutput("6a49414e434852aabb", "tx1", 1)
	if ok {
		t.Fatal("expected false for short script")
	}
}

func TestParseAnchorOutputWrongPrefix(t *testing.T) {
	script := "6a49" + "deadbeef" + hex.EncodeToString(make([]byte, 73))
	_, ok := parseAnchorOutput(script, "tx2", 2)
	if ok {
		t.Fatal("expected false for non-ANCHR prefix")
	}
}
