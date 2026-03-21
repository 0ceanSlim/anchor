package main

import (
	"fmt"
	"strings"

	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdFindPools() *cobra.Command {
	var asset0, asset1, rpcURL, rpcUser, rpcPass, esploraURL, netName string
	var poolID string
	var startBlock, saveIndex int
	var save bool
	var saveFile, buildDir string
	cmd := &cobra.Command{
		Use:   "find-pools",
		Short: "Scan the chain for Anchor pools matching an asset pair or pool ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			esploraURL = resolveEsplora(esploraURL)
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}

			// ── Pool ID lookup (single pool) ────────────────────────────
			if poolID != "" {
				p, err := lookupPoolByID(esploraURL, poolID, buildDir, net)
				if err != nil {
					return err
				}

				// Display.
				feeStr := fmt.Sprintf("%.2f%%", float64(p.feeDen-p.feeNum)/float64(p.feeDen)*100)
				status := ""
				if p.closed {
					status = " [closed]"
				}
				fmt.Printf("Pool ID:   %s\n", p.lpAsset)
				fmt.Printf("Asset0:    %s\n", p.asset0)
				fmt.Printf("Asset1:    %s\n", p.asset1)
				fmt.Printf("Fee:       %s\n", feeStr)
				fmt.Printf("Reserve0:  %d sats\n", p.reserve0)
				fmt.Printf("Reserve1:  %d sats\n", p.reserve1)
				fmt.Printf("Pool A:    %s\n", p.poolAAddr)
				fmt.Printf("Pool B:    %s\n", p.poolBAddr)
				fmt.Printf("Height:    %d%s\n", p.height, status)

				// Prompt to save (or auto-save with --save/--index).
				shouldSave := save || saveIndex >= 0
				if !shouldSave && isTerminal() {
					answer := promptString("\nSave pool config? [y/n]: ")
					shouldSave = strings.ToLower(answer) == "y"
				}
				if shouldSave {
					if saveFile != "" {
						// Explicit --out: use savePoolFromDiscovered but override path.
						_, err := savePoolFromDiscovered(p, buildDir, net)
						return err
					}
					_, err := savePoolFromDiscovered(p, buildDir, net)
					return err
				}
				return nil
			}

			// ── Asset pair scan (multi-pool) ────────────────────────────
			nodeClient := rpc.New(rpcURL, rpcUser, rpcPass)

			entries, err := discoverPools(esploraURL, nodeClient, asset0, asset1, startBlock, buildDir, net)
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				fmt.Println("No compatible Anchor pools found.")
				return nil
			}

			// Display table.
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

			// Determine which pools to save.
			var selectedIndices []int
			if saveIndex >= 0 {
				if saveIndex >= len(entries) {
					return fmt.Errorf("index %d out of range (0..%d)", saveIndex, len(entries)-1)
				}
				selectedIndices = []int{saveIndex}
			} else if isTerminal() {
				allowEmpty := !save
				prompt := "\nSelect pool(s) to save (comma-separated indices, e.g. 0,2): "
				if allowEmpty {
					prompt = "\nSelect pool(s) to save (comma-separated, or Enter to skip): "
				}
				selectedIndices = promptMultiChoice(prompt, len(entries), allowEmpty)
			} else if save {
				selectedIndices = []int{0}
			}

			if len(selectedIndices) == 0 {
				return nil
			}

			// Compile and save each selected pool.
			for _, idx := range selectedIndices {
				selected := entries[idx]
				if _, err := savePoolFromDiscovered(&selected, buildDir, net); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&poolID, "pool-id", "", "Look up a specific pool by LP asset / pool ID")
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
