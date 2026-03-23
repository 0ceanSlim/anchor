package pool

import (
	"encoding/json"
	"testing"
)

func TestConfigRoundTrip(t *testing.T) {
	orig := Config{
		PoolCreation: ContractInfo{
			Address:   "tex1ptest",
			CMR:       "aabbccdd",
			BinaryHex: "deadbeef",
		},
		PoolA:    ContractInfo{Address: "tex1pa"},
		PoolASwap: PoolVariant{CMR: "aa", BinaryHex: "bb", ControlBlock: "cc"},
		Asset0:   "asset0hex",
		Asset1:   "asset1hex",
		FeeNum:   3,
		FeeDen:   1000,
	}

	data, err := json.Marshal(&orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var got Config
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.PoolCreation.Address != orig.PoolCreation.Address {
		t.Errorf("PoolCreation.Address: got %s, want %s", got.PoolCreation.Address, orig.PoolCreation.Address)
	}
	if got.PoolCreation.CMR != orig.PoolCreation.CMR {
		t.Errorf("PoolCreation.CMR: got %s, want %s", got.PoolCreation.CMR, orig.PoolCreation.CMR)
	}
	if got.Asset0 != orig.Asset0 {
		t.Errorf("Asset0: got %s, want %s", got.Asset0, orig.Asset0)
	}
	if got.FeeNum != orig.FeeNum {
		t.Errorf("FeeNum: got %d, want %d", got.FeeNum, orig.FeeNum)
	}
	if got.FeeDen != orig.FeeDen {
		t.Errorf("FeeDen: got %d, want %d", got.FeeDen, orig.FeeDen)
	}
}

func TestTotalSupply(t *testing.T) {
	tests := []struct {
		name      string
		lpReserve uint64
		want      uint64
	}{
		{"full reserve", LPPremint, 0},
		{"zero reserve", 0, LPPremint},
		{"partial", LPPremint - 1_000_000, 1_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := State{LPReserve: tt.lpReserve}
			got := s.TotalSupply()
			if got != tt.want {
				t.Errorf("TotalSupply: got %d, want %d", got, tt.want)
			}
		})
	}
}
