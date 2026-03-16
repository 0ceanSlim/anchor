// Package compiler wraps the simc binary to compile SimplicityHL contracts.
package compiler

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	anchorTaproot "github.com/0ceanslim/anchor/pkg/taproot"
	"github.com/vulpemventures/go-elements/network"
)

// SimcOutput is the JSON structure from simc --json
type SimcOutput struct {
	Program string `json:"program"`
	CMR     string `json:"cmr"`
}

// simcPath resolves the simc binary: SIMC_PATH env → ./bin/simc(.exe) → PATH
func simcPath() string {
	if p := os.Getenv("SIMC_PATH"); p != "" {
		return p
	}
	for _, name := range []string{filepath.Join("bin", "simc.exe"), filepath.Join("bin", "simc")} {
		if _, err := os.Stat(name); err == nil {
			if abs, err := filepath.Abs(name); err == nil {
				return abs
			}
		}
	}
	return "simc"
}

// Compile compiles a .shl file with simc and returns the program binary and CMR.
// If a companion <file>.args exists it is passed via --args.
// The CMR (Commitment Merkle Root) is the 32-byte Simplicity commitment hash
// used as the taproot leaf script content for address derivation.
func Compile(shlPath string) (binary []byte, cmr [32]byte, err error) {
	simc := simcPath()

	args := []string{shlPath}

	argsFile := strings.TrimSuffix(shlPath, filepath.Ext(shlPath)) + ".args"
	if _, err := os.Stat(argsFile); err == nil {
		args = append(args, "--args", argsFile)
	}

	args = append(args, "--json")

	cmd := exec.Command(simc, args...)
	out, runErr := cmd.Output()
	if runErr != nil {
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			return nil, [32]byte{}, fmt.Errorf("simc failed: %w\nstderr: %s", runErr, exitErr.Stderr)
		}
		return nil, [32]byte{}, fmt.Errorf("simc: %w", runErr)
	}

	binary, cmr, err = decodeOutput(out)
	if err != nil {
		return nil, [32]byte{}, fmt.Errorf("parse simc output: %w", err)
	}

	return binary, cmr, nil
}

// decodeOutput parses simc stdout: tries JSON first, then raw base64.
// Returns (binary, cmr, error). CMR comes from simc JSON; raw base64 mode
// falls back to SHA256(binary) which is only used for legacy compatibility.
func decodeOutput(out []byte) ([]byte, [32]byte, error) {
	trimmed := strings.TrimSpace(string(out))

	// Try JSON (preferred: includes true Simplicity CMR)
	var js SimcOutput
	if err := json.Unmarshal([]byte(trimmed), &js); err == nil && js.Program != "" {
		binary, err := base64.StdEncoding.DecodeString(js.Program)
		if err != nil {
			return nil, [32]byte{}, fmt.Errorf("decode program base64: %w", err)
		}
		var cmr [32]byte
		if js.CMR != "" {
			cmrBytes, err := hex.DecodeString(js.CMR)
			if err != nil || len(cmrBytes) != 32 {
				return nil, [32]byte{}, fmt.Errorf("decode cmr hex: %w", err)
			}
			copy(cmr[:], cmrBytes)
		} else {
			cmr = sha256.Sum256(binary) // fallback for old simc without CMR output
		}
		return binary, cmr, nil
	}

	// Try raw base64 (one line) — old simc format, no CMR available
	if b, err := base64.StdEncoding.DecodeString(trimmed); err == nil {
		return b, sha256.Sum256(b), nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil {
		return b, sha256.Sum256(b), nil
	}

	return nil, [32]byte{}, fmt.Errorf("unrecognised simc output format")
}

type argsEntry struct {
	Value string `json:"value"`
	Type  string `json:"type"`
}

// ArgsParam is one entry in a .args JSON file (value + type pair).
type ArgsParam = argsEntry

// PatchParams updates the specified parameter values in all .args files found
// in buildDir. Only keys that already exist in a given .args file are updated;
// extra keys in params are silently ignored for files that don't have them.
func PatchParams(buildDir string, params map[string]ArgsParam) error {
	entries, err := os.ReadDir(buildDir)
	if err != nil {
		return fmt.Errorf("read build dir %s: %w", buildDir, err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".args" {
			continue
		}
		argsPath := filepath.Join(buildDir, entry.Name())
		data, err := os.ReadFile(argsPath)
		if err != nil {
			return fmt.Errorf("read %s: %w", argsPath, err)
		}
		var args map[string]argsEntry
		if err := json.Unmarshal(data, &args); err != nil {
			return fmt.Errorf("parse %s: %w", argsPath, err)
		}
		changed := false
		for key, param := range params {
			if _, exists := args[key]; exists {
				args[key] = param
				changed = true
			}
		}
		if !changed {
			continue
		}
		out, err := json.MarshalIndent(args, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal %s: %w", argsPath, err)
		}
		if err := os.WriteFile(argsPath, out, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", argsPath, err)
		}
	}
	return nil
}

// PatchLPAssetID updates LP_ASSET_ID in the .args file that corresponds to shlPath.
// outPath is used only to derive the destination .args path (usually equal to shlPath).
func PatchLPAssetID(shlPath string, outPath string, assetID [32]byte) error {
	argsPath := strings.TrimSuffix(shlPath, filepath.Ext(shlPath)) + ".args"
	outArgsPath := strings.TrimSuffix(outPath, filepath.Ext(outPath)) + ".args"

	data, err := os.ReadFile(argsPath)
	if err != nil {
		return fmt.Errorf("read args file %s: %w", argsPath, err)
	}

	var args map[string]argsEntry
	if err := json.Unmarshal(data, &args); err != nil {
		return fmt.Errorf("parse args file %s: %w", argsPath, err)
	}

	entry, ok := args["LP_ASSET_ID"]
	if !ok {
		return nil // LP_ASSET_ID not used by this contract — skip
	}

	hexVal := fmt.Sprintf("0x%x", assetID)
	if len(hexVal) < 66 {
		hexVal = "0x" + strings.Repeat("0", 64-(len(hexVal)-2)) + hexVal[2:]
	}
	entry.Value = hexVal
	args["LP_ASSET_ID"] = entry

	out, err := json.MarshalIndent(args, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(outArgsPath, out, 0o644)
}

// CompileAll compiles all pool contracts from buildDir and returns a Config
// with addresses, CMRs, binaries, and control blocks populated.
// pool_creation uses a single-leaf taproot; pool_a/b and lp_reserve use dual-leaf
// taproot (swap+remove variants share one address).
func CompileAll(buildDir string, net *network.Network) (*pool.Config, error) {
	cfg := &pool.Config{}

	// pool_creation: single-leaf
	creationPath := filepath.Join(buildDir, "pool_creation.shl")
	bin, cmr, err := Compile(creationPath)
	if err != nil {
		return nil, fmt.Errorf("compile pool_creation: %w", err)
	}
	addr, err := anchorTaproot.Address(cmr[:], net)
	if err != nil {
		return nil, fmt.Errorf("address pool_creation: %w", err)
	}
	cfg.PoolCreation = pool.ContractInfo{
		Address:   addr,
		CMR:       hex.EncodeToString(cmr[:]),
		BinaryHex: hex.EncodeToString(bin),
	}

	// pool_a, pool_b, lp_reserve: dual-leaf (swap/add + remove variants)
	type pair struct {
		name       string
		swapFile   string
		removeFile string
		cfgAddr    *pool.ContractInfo
		cfgSwap    *pool.PoolVariant
		cfgRemove  *pool.PoolVariant
	}
	pairs := []pair{
		{"pool_a", "pool_a_swap.shl", "pool_a_remove.shl", &cfg.PoolA, &cfg.PoolASwap, &cfg.PoolARemove},
		{"pool_b", "pool_b_swap.shl", "pool_b_remove.shl", &cfg.PoolB, &cfg.PoolBSwap, &cfg.PoolBRemove},
		{"lp_reserve", "lp_reserve_add.shl", "lp_reserve_remove.shl", &cfg.LpReserve, &cfg.LpReserveAdd, &cfg.LpReserveRemove},
	}
	for _, p := range pairs {
		swapBin, swapCMR, err := Compile(filepath.Join(buildDir, p.swapFile))
		if err != nil {
			return nil, fmt.Errorf("compile %s_swap: %w", p.name, err)
		}
		removeBin, removeCMR, err := Compile(filepath.Join(buildDir, p.removeFile))
		if err != nil {
			return nil, fmt.Errorf("compile %s_remove: %w", p.name, err)
		}
		dualAddr, err := anchorTaproot.AddressDual(swapCMR[:], removeCMR[:], net)
		if err != nil {
			return nil, fmt.Errorf("dual address %s: %w", p.name, err)
		}
		swapCB, err := anchorTaproot.ControlBlockDual(swapCMR[:], removeCMR[:], swapCMR[:])
		if err != nil {
			return nil, fmt.Errorf("control block %s swap: %w", p.name, err)
		}
		removeCB, err := anchorTaproot.ControlBlockDual(swapCMR[:], removeCMR[:], removeCMR[:])
		if err != nil {
			return nil, fmt.Errorf("control block %s remove: %w", p.name, err)
		}
		*p.cfgAddr = pool.ContractInfo{Address: dualAddr}
		*p.cfgSwap = pool.PoolVariant{
			CMR:          hex.EncodeToString(swapCMR[:]),
			BinaryHex:    hex.EncodeToString(swapBin),
			ControlBlock: hex.EncodeToString(swapCB),
		}
		*p.cfgRemove = pool.PoolVariant{
			CMR:          hex.EncodeToString(removeCMR[:]),
			BinaryHex:    hex.EncodeToString(removeBin),
			ControlBlock: hex.EncodeToString(removeCB),
		}
	}
	return cfg, nil
}
