package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
)

func cmdFindPools() *cobra.Command {
	var asset0, asset1, rpcURL, rpcUser, rpcPass, esploraURL, netName string
	var startBlock, saveIndex int
	var save bool
	var saveFile, buildDir string
	cmd := &cobra.Command{
		Use:   "find-pools",
		Short: "Scan the chain for Anchor pools matching an asset pair",
		RunE: func(cmd *cobra.Command, args []string) error {
			esploraURL = resolveEsplora(esploraURL)
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}

			nodeClient := rpc.New(rpcURL, rpcUser, rpcPass)

			entries, err := discoverPools(esploraURL, nodeClient, asset0, asset1, startBlock, buildDir, net)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No compatible Anchor pools found.")
				return nil
			}

			// ── Display ──────────────────────────────────────────────────
			fmt.Printf("%-5s %-20s  %-6s  %-14s  %-14s  %s\n", "IDX", "POOL (pool_a)", "FEE", "RESERVE0", "RESERVE1", "LP ASSET")
			fmt.Println(strings.Repeat("-", 105))
			for i, e := range entries {
				feeStr := fmt.Sprintf("%.2f%%", float64(e.feeDen-e.feeNum)/float64(e.feeDen)*100)
				addr := e.poolAAddr
				if len(addr) > 20 {
					addr = addr[:8] + "..." + addr[len(addr)-8:]
				}
				status := ""
				if e.closed {
					status = " [closed]"
				} else if i == 0 {
					status = " (deepest)"
				}
				fmt.Printf("%-5d %-20s  %-6s  %-14d  %-14d  %s%s\n",
					i, addr, feeStr, e.reserve0, e.reserve1, e.lpAsset[:16]+"...", status)
			}

			// ── Save selected pools ─────────────────────────────────────
			// Determine which pools to save. --index N for non-interactive,
			// otherwise prompt with multi-selection.
			var selectedIndices []int
			if saveIndex >= 0 {
				// Non-interactive: explicit --index
				if saveIndex >= len(entries) {
					return fmt.Errorf("index %d out of range (0..%d)", saveIndex, len(entries)-1)
				}
				selectedIndices = []int{saveIndex}
			} else if isTerminal() {
				// Interactive: prompt for multi-selection.
				// If --save was passed, require at least one selection.
				// Otherwise allow skipping (empty = no save).
				allowEmpty := !save
				prompt := "\nSelect pool(s) to save (comma-separated indices, e.g. 0,2): "
				if allowEmpty {
					prompt = "\nSelect pool(s) to save (comma-separated, or Enter to skip): "
				}
				selectedIndices = promptMultiChoice(prompt, len(entries), allowEmpty)
			} else if save {
				// Non-interactive with --save but no --index: default to 0
				selectedIndices = []int{0}
			}

			if len(selectedIndices) == 0 {
				return nil
			}

			// ── Compile and save each selected pool ─────────────────────
			for _, idx := range selectedIndices {
				selected := entries[idx]

				lpAssetID, err := tx.ComputeLPAssetID(selected.creationVinTx, selected.creationVinV)
				if err != nil {
					return fmt.Errorf("compute LP asset ID: %w", err)
				}
				feeDiff := uint64(selected.feeDen) - uint64(selected.feeNum)
				patchMap := map[string]compiler.ArgsParam{
					"ASSET0":     {Value: normalizeHex(selected.asset0), Type: "u256"},
					"ASSET1":     {Value: normalizeHex(selected.asset1), Type: "u256"},
					"FEE_NUM":    {Value: fmt.Sprintf("%d", selected.feeNum), Type: "u64"},
					"FEE_DEN":    {Value: fmt.Sprintf("%d", selected.feeDen), Type: "u64"},
					"FEE_DIFF":   {Value: fmt.Sprintf("%d", feeDiff), Type: "u64"},
					"LP_PREMINT": {Value: fmt.Sprintf("%d", pool.LPPremint), Type: "u64"},
				}
				if err := compiler.PatchParams(buildDir, patchMap); err != nil {
					return fmt.Errorf("patch params: %w", err)
				}
				for _, shlName := range []string{
					"pool_a_swap.shl", "pool_a_remove.shl",
					"pool_b_swap.shl", "pool_b_remove.shl",
					"lp_reserve_add.shl", "lp_reserve_remove.shl",
				} {
					shlPath := buildDir + "/" + shlName
					if err := compiler.PatchLPAssetID(shlPath, shlPath, lpAssetID); err != nil {
						return fmt.Errorf("patch LP asset ID in %s: %w", shlName, err)
					}
				}

				fmt.Fprintf(os.Stderr, "Compiling contracts for pool %d...\n", idx)
				cfg, err := compiler.CompileAll(buildDir, net)
				if err != nil {
					return fmt.Errorf("compile: %w", err)
				}

				cfg.Asset0 = selected.asset0
				cfg.Asset1 = selected.asset1
				cfg.LPAssetID = selected.lpAsset
				cfg.FeeNum = uint64(selected.feeNum)
				cfg.FeeDen = uint64(selected.feeDen)

				outFile := saveFile
				if outFile == "" {
					if err := ensurePoolsDir(); err != nil {
						return fmt.Errorf("create pools/: %w", err)
					}
					outFile = poolsSavePath(selected.asset0, selected.asset1, uint64(selected.feeNum), uint64(selected.feeDen))
				}

				if err := cfg.Save(outFile); err != nil {
					return fmt.Errorf("save pool config: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Saved pool config to %s\n", outFile)
				fmt.Fprintf(os.Stderr, "  pool_a: %s\n", cfg.PoolA.Address)
				fmt.Fprintf(os.Stderr, "  pool_b: %s\n", cfg.PoolB.Address)
				fmt.Fprintf(os.Stderr, "  lp_reserve: %s\n", cfg.LpReserve.Address)
				fmt.Fprintf(os.Stderr, "  lp_asset: %s\n", selected.lpAsset)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID to filter by (case-insensitive)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID to filter by (case-insensitive)")
	cmd.Flags().IntVar(&startBlock, "start-block", 0, "Block height to start scanning from (default: 0 = genesis)")
	cmd.Flags().StringVar(&esploraURL, "esplora-url", "", "Esplora API URL (env: ANCHOR_ESPLORA_URL)")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.Flags().BoolVar(&save, "save", false, "Require at least one pool selection (deprecated — saving is always offered)")
	cmd.Flags().StringVar(&saveFile, "out", "", "Output filename (default: auto-named in pools/)")
	cmd.Flags().IntVar(&saveIndex, "index", -1, "Pool index to save (non-interactive)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl and .args files")
	return cmd
}
