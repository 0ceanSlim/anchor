package compiler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
)

func TestDecodeOutputJSON(t *testing.T) {
	// Build a known JSON payload with program + CMR.
	programBytes := []byte("hello simplicity")
	cmrBytes := sha256.Sum256([]byte("test cmr"))

	js := SimcOutput{
		Program: base64.StdEncoding.EncodeToString(programBytes),
		CMR:     hex.EncodeToString(cmrBytes[:]),
	}
	data, err := json.Marshal(js)
	if err != nil {
		t.Fatal(err)
	}

	binary, cmr, err := decodeOutput(data)
	if err != nil {
		t.Fatalf("decodeOutput: %v", err)
	}
	if string(binary) != string(programBytes) {
		t.Errorf("binary mismatch: got %x", binary)
	}
	if cmr != cmrBytes {
		t.Errorf("cmr mismatch: got %x, want %x", cmr, cmrBytes)
	}
}

func TestDecodeOutputRawBase64(t *testing.T) {
	programBytes := []byte("raw program bytes")
	b64 := base64.StdEncoding.EncodeToString(programBytes)

	binary, cmr, err := decodeOutput([]byte(b64))
	if err != nil {
		t.Fatalf("decodeOutput: %v", err)
	}
	if string(binary) != string(programBytes) {
		t.Errorf("binary mismatch: got %x", binary)
	}
	wantCMR := sha256.Sum256(programBytes)
	if cmr != wantCMR {
		t.Errorf("cmr mismatch: got %x, want %x (SHA256 fallback)", cmr, wantCMR)
	}
}

func TestDecodeOutputInvalid(t *testing.T) {
	_, _, err := decodeOutput([]byte("not valid base64 or json!!!"))
	if err == nil {
		t.Fatal("expected error for invalid input")
	}
}
