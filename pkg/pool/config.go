// Package pool manages pool configuration and on-chain state queries.
package pool

import (
	"encoding/json"
	"os"
)

// LPPremint is the total pre-minted LP token supply per pool.
// Elements consensus sums ALL explicit output values (cross-asset) and
// rejects transactions where the total exceeds MAX_MONEY (2.1e15).
// We reserve 1e14 (~$10M headroom) for pool deposits, change, and fees
// in the create-pool transaction where the full premint appears.
const LPPremint uint64 = 2_000_000_000_000_000

// ContractInfo holds compiled contract data for one pool script.
type ContractInfo struct {
	Address   string `json:"address"`
	CMR       string `json:"cmr"`        // 32-byte Simplicity CMR hex (taproot leaf content)
	BinaryHex string `json:"binary_hex"` // hex-encoded Simplicity program
}

// PoolVariant holds the CMR, binary, and control block for one mode-specific
// pool script (e.g., swap-only or remove-only). The address is shared with
// the parent ContractInfo (dual-leaf taproot).
type PoolVariant struct {
	CMR          string `json:"cmr"`
	BinaryHex    string `json:"binary_hex"`
	ControlBlock string `json:"control_block"` // hex-encoded taproot control block
}

// Config holds all pool contract addresses, binaries, and asset IDs.
type Config struct {
	PoolCreation ContractInfo `json:"pool_creation"`

	// Pool A: dual-leaf taproot (swap + remove leaves share one address)
	PoolA       ContractInfo `json:"pool_a"`
	PoolASwap   PoolVariant  `json:"pool_a_swap"`
	PoolARemove PoolVariant  `json:"pool_a_remove"`

	// Pool B: dual-leaf taproot
	PoolB       ContractInfo `json:"pool_b"`
	PoolBSwap   PoolVariant  `json:"pool_b_swap"`
	PoolBRemove PoolVariant  `json:"pool_b_remove"`

	// LP Reserve: dual-leaf taproot (add + remove leaves share one address)
	LpReserve       ContractInfo `json:"lp_reserve"`
	LpReserveAdd    PoolVariant  `json:"lp_reserve_add"`
	LpReserveRemove PoolVariant  `json:"lp_reserve_remove"`

	// Asset IDs and fee parameters stored after compile/create-pool.
	// FeeNum/FeeDen are baked into the compiled contracts (.args at compile time)
	// and must be stored here so swap/add/remove operations use the correct formula.
	Asset0    string `json:"asset0,omitempty"`
	Asset1    string `json:"asset1,omitempty"`
	LPAssetID string `json:"lp_asset_id,omitempty"`
	FeeNum    uint64 `json:"fee_num,omitempty"`
	FeeDen    uint64 `json:"fee_den,omitempty"`
}

// Load reads pool.json from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	return &cfg, json.Unmarshal(data, &cfg)
}

// Save writes pool.json to path (pretty-printed).
func (c *Config) Save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
