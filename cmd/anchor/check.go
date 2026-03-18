package main

import (
	"fmt"
	"os"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdCheck() *cobra.Command {
	var poolFile, rpcURL, rpcUser, rpcPass string
	cmd := &cobra.Command{
		Use:   "check",
		Short: "Validate environment: RPC connection, simc binary, and pool.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)

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

			fmt.Println("\npool.json:")
			if _, err := os.Stat(poolFile); os.IsNotExist(err) {
				fmt.Printf("  MISSING  %s — run 'anchor compile' first\n", poolFile)
				ok = false
			} else {
				fmt.Printf("  OK       %s\n", poolFile)
				if cfg, err := pool.Load(poolFile); err != nil {
					fmt.Printf("  INVALID  %s — %v\n", poolFile, err)
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
	return cmd
}
