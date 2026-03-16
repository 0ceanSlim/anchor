package taproot

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/vulpemventures/go-elements/network"
)

// known CMR from pool_creation (filled with test placeholder).
var testCMR = make([]byte, 32)

func init() {
	for i := range testCMR {
		testCMR[i] = byte(i + 1)
	}
}

func TestAddressDerivation(t *testing.T) {
	net := network.Testnet
	addr, err := Address(testCMR, &net)
	if err != nil {
		t.Fatalf("Address: %v", err)
	}
	// Testnet bech32m addresses start with "tex1p"
	if !strings.HasPrefix(addr, "tex1p") {
		t.Errorf("expected testnet bech32m address, got %s", addr)
	}
}

func TestAddressIsDeterministic(t *testing.T) {
	net := network.Testnet
	addr1, _ := Address(testCMR, &net)
	addr2, _ := Address(testCMR, &net)
	if addr1 != addr2 {
		t.Errorf("Address not deterministic: %s != %s", addr1, addr2)
	}
}

func TestControlBlock(t *testing.T) {
	cb, err := ControlBlock(testCMR)
	if err != nil {
		t.Fatalf("ControlBlock: %v", err)
	}
	if len(cb) == 0 {
		t.Error("empty control block")
	}
}

func TestAddressDual(t *testing.T) {
	cmr1 := make([]byte, 32)
	cmr2 := make([]byte, 32)
	for i := range cmr1 {
		cmr1[i] = byte(i + 1)
		cmr2[i] = byte(i + 33)
	}
	net := network.Testnet
	addr, err := AddressDual(cmr1, cmr2, &net)
	if err != nil {
		t.Fatalf("AddressDual: %v", err)
	}
	if !strings.HasPrefix(addr, "tex1p") {
		t.Errorf("expected testnet bech32m address, got %s", addr)
	}
	// Dual address must differ from single-leaf addresses
	addr1, _ := Address(cmr1, &net)
	addr2, _ := Address(cmr2, &net)
	if addr == addr1 || addr == addr2 {
		t.Error("dual address should differ from single-leaf addresses")
	}
}

func TestControlBlockDual(t *testing.T) {
	cmr1 := make([]byte, 32)
	cmr2 := make([]byte, 32)
	for i := range cmr1 {
		cmr1[i] = byte(i + 1)
		cmr2[i] = byte(i + 33)
	}
	cb1, err := ControlBlockDual(cmr1, cmr2, cmr1)
	if err != nil {
		t.Fatalf("ControlBlockDual(cmr1): %v", err)
	}
	cb2, err := ControlBlockDual(cmr1, cmr2, cmr2)
	if err != nil {
		t.Fatalf("ControlBlockDual(cmr2): %v", err)
	}
	// Control blocks must differ (they encode different paths in the tree)
	if hex.EncodeToString(cb1) == hex.EncodeToString(cb2) {
		t.Error("control blocks for different leaves should differ")
	}
}

func TestControlBlockDualUnknownCMR(t *testing.T) {
	cmr1 := make([]byte, 32)
	cmr2 := make([]byte, 32)
	unknown := make([]byte, 32)
	for i := range cmr1 {
		cmr1[i] = 1
		cmr2[i] = 2
		unknown[i] = 3
	}
	_, err := ControlBlockDual(cmr1, cmr2, unknown)
	if err == nil {
		t.Error("expected error for unknown targetCMR")
	}
}
