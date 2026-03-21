package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/0ceanslim/anchor/pkg/esplora"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdCheck() *cobra.Command {
	var poolFile, rpcURL, rpcUser, rpcPass, esploraURL string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate environment: RPC connection, simc binary, and pool.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			esploraURL = resolveEsplora(esploraURL)

			ok := true
			check := func(label, value, hint string) {
				if value == "" {
					fmt.Printf("  MISSING  %s — %s\n", label, hint)
					ok = false
				} else {
					fmt.Printf("  OK       %s = %s\n", label, value)
				}
			}

			fmt.Println("Environment variables:")
			check("ANCHOR_RPC_URL", rpcURL, "set ANCHOR_RPC_URL or use --rpc-url")
			check("ANCHOR_RPC_USER", rpcUser, "set ANCHOR_RPC_USER or use --rpc-user")
			check("ANCHOR_RPC_PASS", rpcPass, "set ANCHOR_RPC_PASS or use --rpc-pass")
			check("ANCHOR_NETWORK", os.Getenv("ANCHOR_NETWORK"), "set ANCHOR_NETWORK (liquid/testnet/regtest) or use --network")

			fmt.Println("\nRPC connection:")
			if rpcURL != "" {
				client := rpc.New(rpcURL, rpcUser, rpcPass)
				if chain, err := client.GetNetworkInfo(); err != nil {
					fmt.Printf("  FAIL     %s — %v\n", rpcURL, err)
					ok = false
				} else {
					fmt.Printf("  OK       connected (chain: %s)\n", chain)
				}
			} else {
				fmt.Println("  SKIP     (no RPC URL)")
			}

			fmt.Println("\nEsplora:")
			if esploraURL != "" {
				ec := esplora.New(esploraURL)
				if height, err := ec.Ping(); err != nil {
					fmt.Printf("  WARN     %s — %v\n", esploraURL, err)
					fmt.Println("           (pool discovery will be unavailable; flag-mode with pool.json still works)")
				} else {
					fmt.Printf("  OK       %s (tip: %d)\n", esploraURL, height)
				}
			} else {
				fmt.Println("  SKIP     ANCHOR_ESPLORA_URL not set (pool discovery unavailable)")
			}

			fmt.Println("\nPool config:")
			resolved, resolveErr := resolvePoolFile(cmd, poolFile)
			if resolveErr != nil {
				fmt.Printf("  WARN     %v\n", resolveErr)
			}
			if resolved == "" {
				// Check pools/ directory status.
				poolsEntries, _ := filepath.Glob(filepath.Join("pools", "*.json"))
				if len(poolsEntries) == 0 {
					fmt.Println("  MISSING  no pool config found (pools/ empty or missing, no pool.json)")
					fmt.Println("           run 'anchor create-pool' or 'anchor find-pools --save'")
					ok = false
				}
			} else {
				fmt.Printf("  OK       %s\n", resolved)
				if cfg, loadErr := pool.Load(resolved); loadErr != nil {
					fmt.Printf("  INVALID  %s — %v\n", resolved, loadErr)
					ok = false
				} else {
					if cfg.Asset0 != "" {
						fmt.Printf("  OK       asset0 = %s\n", cfg.Asset0)
					} else {
						fmt.Println("  INFO     asset0 not set (run 'anchor create-pool' first)")
					}
					if cfg.LPAssetID != "" {
						fmt.Printf("  OK       lp_asset_id = %s\n", cfg.LPAssetID)
					}
				}
			}
			// Show pools/ directory summary.
			poolsEntries, _ := filepath.Glob(filepath.Join("pools", "*.json"))
			if len(poolsEntries) > 0 {
				fmt.Printf("  INFO     pools/ contains %d config(s)\n", len(poolsEntries))
			}

			if !ok {
				return fmt.Errorf("one or more checks failed")
			}
			fmt.Println("\nAll checks passed.")
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().StringVar(&esploraURL, "esplora-url", "", "Esplora API URL (env: ANCHOR_ESPLORA_URL)")
	return cmd
}
