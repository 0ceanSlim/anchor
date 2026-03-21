package main

import (
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/spf13/cobra"
	"github.com/vulpemventures/go-elements/network"
	"github.com/vulpemventures/go-elements/transaction"
)

// resolvePoolFile determines which pool config file to use.
// If --pool was explicitly set, it returns that path directly.
// Otherwise it searches pools/*.json, falling back to the default poolFlag value.
func resolvePoolFile(cmd *cobra.Command, poolFlag string) (string, error) {
	// Explicit --pool always wins.
	if cmd.Flags().Changed("pool") {
		return poolFlag, nil
	}

	// Search pools/ directory.
	matches, _ := filepath.Glob(filepath.Join("pools", "*.json"))
	switch len(matches) {
	case 1:
		fmt.Fprintf(os.Stderr, "Using pool: %s\n", matches[0])
		return matches[0], nil
	case 0:
		// No pools/ files — fall back to default (pool.json) if it exists.
		if _, err := os.Stat(poolFlag); err == nil {
			return poolFlag, nil
		}
		// Nothing found at all.
		return "", nil
	default:
		// Multiple pools — prompt if interactive, error otherwise.
		if !isTerminal() {
			return "", fmt.Errorf("multiple pool configs found in pools/ — use --pool to select one")
		}
		fmt.Fprintf(os.Stderr, "Multiple pools found:\n")
		idx := promptChoice("Select pool", matches)
		fmt.Fprintf(os.Stderr, "Using pool: %s\n", matches[idx])
		return matches[idx], nil
	}
}

// ensurePoolsDir creates the pools/ directory if it doesn't exist.
func ensurePoolsDir() error {
	return os.MkdirAll("pools", 0o755)
}

// poolsSavePath returns the canonical save path inside pools/ for a given asset pair and fee.
func poolsSavePath(asset0, asset1 string, feeNum, feeDen uint64) string {
	return filepath.Join("pools", poolJSONName(asset0, asset1, feeNum, feeDen))
}

// findMatchingPoolConfig searches pools/*.json and pool.json for a config
// that matches the given asset pair and fee tier. Returns the loaded config
// and its file path, or nil if no match is found.
func findMatchingPoolConfig(asset0, asset1 string, feeNum, feeDen uint64) (*pool.Config, string) {
	// Collect candidate files: pools/*.json + pool.json.
	candidates, _ := filepath.Glob(filepath.Join("pools", "*.json"))
	if _, err := os.Stat("pool.json"); err == nil {
		candidates = append(candidates, "pool.json")
	}
	for _, path := range candidates {
		cfg, err := pool.Load(path)
		if err != nil {
			continue
		}
		if strings.EqualFold(cfg.Asset0, asset0) &&
			strings.EqualFold(cfg.Asset1, asset1) &&
			cfg.FeeNum == feeNum && cfg.FeeDen == feeDen &&
			cfg.PoolA.Address != "" {
			return cfg, path
		}
	}
	return nil, ""
}

// isTerminal returns true if stdin is connected to a terminal.
func isTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// translateError maps known RPC / Simplicity error strings to actionable messages.
func translateError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SIMPLICITY_ERR_ANTIDOS"):
		return fmt.Errorf("Simplicity anti-DOS limit exceeded — contract execution cost too high for this spend path\n  (original: %w)", err)
	case strings.Contains(msg, "txn-mempool-conflict"):
		return fmt.Errorf("pool UTXOs already spent in mempool — another operation is pending; wait for confirmation and retry\n  (original: %w)", err)
	case strings.Contains(msg, "insufficient fee"):
		return fmt.Errorf("transaction fee too low — increase with --fee\n  (original: %w)", err)
	case strings.Contains(msg, "dust"):
		return fmt.Errorf("output amount too small (dust) — increase deposit or output amounts\n  (original: %w)", err)
	case strings.Contains(msg, "mandatory-script-verify-flag-failed"):
		return fmt.Errorf("script validation failed — contract witness mismatch; check pool.json is up to date\n  (original: %w)", err)
	default:
		return err
	}
}

func resolveRPC(url, user, pass string) (string, string, string) {
	if url == "" {
		if v := os.Getenv("ANCHOR_RPC_URL"); v != "" {
			url = v
		} else {
			url = "http://localhost:18884"
		}
	}
	if user == "" {
		user = os.Getenv("ANCHOR_RPC_USER")
	}
	if pass == "" {
		pass = os.Getenv("ANCHOR_RPC_PASS")
	}
	return url, user, pass
}

func resolveEsplora(url string) string {
	if url == "" {
		if v := os.Getenv("ANCHOR_ESPLORA_URL"); v != "" {
			url = v
		}
	}
	return url
}

func resolveNetwork(name string) string {
	if name == "" {
		if v := os.Getenv("ANCHOR_NETWORK"); v != "" {
			return v
		}
		return "testnet"
	}
	return name
}

func parseNetwork(name string) (*network.Network, error) {
	switch strings.ToLower(name) {
	case "liquid", "mainnet":
		n := network.Liquid
		return &n, nil
	case "testnet":
		n := network.Testnet
		return &n, nil
	case "regtest":
		n := network.Regtest
		return &n, nil
	default:
		return nil, fmt.Errorf("unknown network %q (use: liquid, testnet, regtest)", name)
	}
}

func satoshis(btc float64) uint64 {
	return uint64(math.Round(btc * 1e8))
}

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// attachWitness parses a transaction hex, sets the witness on the given input index, and returns the new hex.
func attachWitness(txHex string, inputIdx int, witness [][]byte) (string, error) {
	parsedTx, err := transaction.NewTxFromHex(txHex)
	if err != nil {
		return "", fmt.Errorf("parse tx: %w", err)
	}
	if inputIdx >= len(parsedTx.Inputs) {
		return "", fmt.Errorf("input index %d out of range (tx has %d inputs)", inputIdx, len(parsedTx.Inputs))
	}
	parsedTx.Inputs[inputIdx].Witness = witness
	return parsedTx.ToHex()
}

// normalizeHex converts a display-format asset ID (from RPC / user input) to
// the byte-reversed form required by Simplicity .args files.
// Simplicity reads assets from the transaction wire format where bytes are
// reversed relative to the display (RPC) representation.
func normalizeHex(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
	if len(h) != 64 {
		// Not a 32-byte asset — return as-is with prefix
		return "0x" + h
	}
	b, err := hex.DecodeString(h)
	if err != nil {
		return "0x" + h
	}
	for i, j := 0, len(b)-1; i < j; i, j = i+1, j-1 {
		b[i], b[j] = b[j], b[i]
	}
	return "0x" + hex.EncodeToString(b)
}

func parseOutpoint(s string) (txid string, vout uint32, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid outpoint %q — expected txid:vout", s)
	}
	txid = parts[0]
	var v uint64
	if _, err := fmt.Sscanf(parts[1], "%d", &v); err != nil {
		return "", 0, fmt.Errorf("invalid vout in %q: %w", s, err)
	}
	return txid, uint32(v), nil
}

// poolJSONName generates an auto-named pool file like pool-<a0>-<a1>-<bps>bps.json.
func poolJSONName(asset0, asset1 string, feeNum, feeDen uint64) string {
	a0 := asset0
	if len(a0) > 8 {
		a0 = a0[:8]
	}
	a1 := asset1
	if len(a1) > 8 {
		a1 = a1[:8]
	}
	var bps uint64
	if feeDen > 0 {
		bps = (feeDen - feeNum) * 10000 / feeDen
	}
	return fmt.Sprintf("pool-%s-%s-%dbps.json", a0, a1, bps)
}

// revBytes returns a reversed copy of b.
func revBytes(b []byte) []byte {
	c := make([]byte, len(b))
	for i, v := range b {
		c[len(b)-1-i] = v
	}
	return c
}
