package main

import (
	"encoding/json"
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
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate environment: RPC connection, simc binary, and pool.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			esploraURL = resolveEsplora(esploraURL)

			// Collect results for JSON mode.
			type checkResult struct {
				OK    bool   `json:"ok"`
				Chain string `json:"chain,omitempty"`
				Error string `json:"error,omitempty"`
			}
			jRPC := checkResult{}
			jEsplora := checkResult{}
			poolsFound := 0

			ok := true
			check := func(label, value, hint string) {
				if value == "" {
					if !jsonOut {
						fmt.Printf("  MISSING  %s — %s\n", label, hint)
					}
					ok = false
				} else if !jsonOut {
					fmt.Printf("  OK       %s = %s\n", label, value)
				}
			}

			if !jsonOut {
				fmt.Println("Environment variables:")
			}
			check("ANCHOR_RPC_URL", rpcURL, "set ANCHOR_RPC_URL or use --rpc-url")
			check("ANCHOR_RPC_USER", rpcUser, "set ANCHOR_RPC_USER or use --rpc-user")
			check("ANCHOR_RPC_PASS", rpcPass, "set ANCHOR_RPC_PASS or use --rpc-pass")
			check("ANCHOR_NETWORK", os.Getenv("ANCHOR_NETWORK"), "set ANCHOR_NETWORK (liquid/testnet/regtest) or use --network")

			if !jsonOut {
				fmt.Println("\nRPC connection:")
			}
			if rpcURL != "" {
				client := rpc.New(rpcURL, rpcUser, rpcPass)
				if chain, err := client.GetNetworkInfo(); err != nil {
					if !jsonOut {
						fmt.Printf("  FAIL     %s — %v\n", rpcURL, err)
					}
					jRPC = checkResult{OK: false, Error: err.Error()}
					ok = false
				} else {
					if !jsonOut {
						fmt.Printf("  OK       connected (chain: %s)\n", chain)
					}
					jRPC = checkResult{OK: true, Chain: chain}
				}
			} else {
				if !jsonOut {
					fmt.Println("  SKIP     (no RPC URL)")
				}
			}

			if !jsonOut {
				fmt.Println("\nEsplora:")
			}
			if esploraURL != "" {
				ec := esplora.New(esploraURL)
				if _, err := ec.Ping(); err != nil {
					if !jsonOut {
						fmt.Printf("  WARN     %s — %v\n", esploraURL, err)
						fmt.Println("           (pool discovery will be unavailable; flag-mode with pool.json still works)")
					}
					jEsplora = checkResult{OK: false, Error: err.Error()}
				} else {
					if !jsonOut {
						fmt.Printf("  OK       %s\n", esploraURL)
					}
					jEsplora = checkResult{OK: true}
				}
			} else {
				if !jsonOut {
					fmt.Println("  SKIP     ANCHOR_ESPLORA_URL not set (pool discovery unavailable)")
				}
			}

			if !jsonOut {
				fmt.Println("\nPool config:")
			}
			resolved, resolveErr := resolvePoolFile(cmd, poolFile)
			if resolveErr != nil && !jsonOut {
				fmt.Printf("  WARN     %v\n", resolveErr)
			}
			if resolved == "" {
				poolsEntries, _ := filepath.Glob(filepath.Join("pools", "*.json"))
				poolsFound = len(poolsEntries)
				if len(poolsEntries) == 0 {
					if !jsonOut {
						fmt.Println("  MISSING  no pool config found (pools/ empty or missing, no pool.json)")
						fmt.Println("           run 'anchor create-pool' or 'anchor find-pools --save'")
					}
					ok = false
				}
			} else {
				if !jsonOut {
					fmt.Printf("  OK       %s\n", resolved)
				}
				if cfg, loadErr := pool.Load(resolved); loadErr != nil {
					if !jsonOut {
						fmt.Printf("  INVALID  %s — %v\n", resolved, loadErr)
					}
					ok = false
				} else if !jsonOut {
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
			if poolsFound == 0 {
				poolsFound = len(poolsEntries)
			}
			if len(poolsEntries) > 0 && !jsonOut {
				fmt.Printf("  INFO     pools/ contains %d config(s)\n", len(poolsEntries))
			}

			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"rpc":         jRPC,
					"esplora":     jEsplora,
					"pools_found": poolsFound,
				})
			}

			if !ok {
				return fmt.Errorf("one or more checks failed")
			}
			fmt.Println("\nAll checks passed.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().StringVar(&esploraURL, "esplora-url", "", "Esplora API URL (env: ANCHOR_ESPLORA_URL)")
	return cmd
}
