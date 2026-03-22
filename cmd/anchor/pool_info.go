package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdPoolInfo() *cobra.Command {
	var poolFile, rpcURL, rpcUser, rpcPass, esploraURL, netName string
	var poolID, buildDir string
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "pool-info",
		Short: "Query live pool reserves from chain",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)

			// --pool-id: look up via Esplora without needing a saved config.
			if poolID != "" {
				esploraURL = resolveEsplora(esploraURL)
				netName = resolveNetwork(netName)
				net, err := parseNetwork(netName)
				if err != nil {
					return err
				}
				p, err := lookupPoolByID(esploraURL, poolID, buildDir, net)
				if err != nil {
					return err
				}
				feeStr := fmt.Sprintf("%.2f%%", float64(p.feeDen-p.feeNum)/float64(p.feeDen)*100)
				if jsonOut {
					result := map[string]any{
						"pool_id":  p.lpAsset,
						"asset0":   p.asset0,
						"asset1":   p.asset1,
						"fee_rate": feeStr,
						"reserve0": p.reserve0,
						"reserve1": p.reserve1,
						"pool_a":   p.poolAAddr,
						"pool_b":   p.poolBAddr,
						"closed":   p.closed,
					}
					if p.reserve0 > 0 && p.reserve1 > 0 {
						result["price"] = float64(p.reserve1) / float64(p.reserve0)
					}
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(result)
				}
				status := ""
				if p.closed {
					status = " [closed]"
				}
				fmt.Printf("Pool ID:     %s\n", p.lpAsset)
				fmt.Printf("Asset0:      %s\n", p.asset0)
				fmt.Printf("Asset1:      %s\n", p.asset1)
				fmt.Printf("Fee:         %s\n", feeStr)
				fmt.Printf("Reserve0:    %d sats\n", p.reserve0)
				fmt.Printf("Reserve1:    %d sats\n", p.reserve1)
				fmt.Printf("Pool A:      %s%s\n", p.poolAAddr, status)
				fmt.Printf("Pool B:      %s\n", p.poolBAddr)
				if p.reserve0 > 0 && p.reserve1 > 0 {
					ratio := float64(p.reserve1) / float64(p.reserve0)
					fmt.Printf("Price:       1 asset0 = %.6f asset1\n", ratio)
				}
				return nil
			}

			resolved, err := resolvePoolFile(cmd, poolFile)
			if err != nil {
				return err
			}
			if resolved == "" {
				return fmt.Errorf("no pool config found — use --pool-id <asset>, 'anchor find-pools', or specify --pool")
			}
			cfg, err := pool.Load(resolved)
			if err != nil {
				return err
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)
			state, err := pool.Query(cfg, client)
			if err != nil {
				return err
			}
			totalSupply := state.TotalSupply()
			if jsonOut {
				result := map[string]any{
					"reserve0":      state.Reserve0,
					"reserve1":      state.Reserve1,
					"total_supply":  totalSupply,
					"lp_reserve":    state.LPReserve,
					"pool_a_utxo":   fmt.Sprintf("%s:%d", state.PoolATxID, state.PoolAVout),
					"pool_b_utxo":   fmt.Sprintf("%s:%d", state.PoolBTxID, state.PoolBVout),
					"lp_reserve_utxo": fmt.Sprintf("%s:%d", state.LpReserveTxID, state.LpReserveVout),
				}
				if cfg.LPAssetID != "" {
					result["pool_id"] = cfg.LPAssetID
				}
				if cfg.Asset0 != "" {
					result["asset0"] = cfg.Asset0
				}
				if cfg.Asset1 != "" {
					result["asset1"] = cfg.Asset1
				}
				if cfg.FeeNum > 0 && cfg.FeeDen > 0 {
					result["fee_rate"] = fmt.Sprintf("%.2f%%", float64(cfg.FeeDen-cfg.FeeNum)/float64(cfg.FeeDen)*100)
				}
				if state.Reserve0 > 0 && state.Reserve1 > 0 {
					result["price"] = float64(state.Reserve1) / float64(state.Reserve0)
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}
			if cfg.LPAssetID != "" {
				fmt.Printf("Pool ID:            %s\n", cfg.LPAssetID)
			}
			if cfg.Asset0 != "" {
				fmt.Printf("Asset0:             %s\n", cfg.Asset0)
			}
			if cfg.Asset1 != "" {
				fmt.Printf("Asset1:             %s\n", cfg.Asset1)
			}
			if cfg.FeeNum > 0 && cfg.FeeDen > 0 {
				feeStr := fmt.Sprintf("%.2f%%", float64(cfg.FeeDen-cfg.FeeNum)/float64(cfg.FeeDen)*100)
				fmt.Printf("Fee:                %s\n", feeStr)
			}
			fmt.Printf("Reserve0 (Asset0):  %d sats\n", state.Reserve0)
			fmt.Printf("Reserve1 (Asset1):  %d sats\n", state.Reserve1)
			fmt.Printf("Total Supply (LP):  %d\n", totalSupply)
			fmt.Printf("LP Reserve:         %d\n", state.LPReserve)
			if state.Reserve0 > 0 && state.Reserve1 > 0 {
				ratio := float64(state.Reserve1) / float64(state.Reserve0)
				fmt.Printf("Price:              1 asset0 = %.6f asset1\n", ratio)
			}
			fmt.Printf("Pool A UTXO:        %s:%d\n", state.PoolATxID, state.PoolAVout)
			fmt.Printf("Pool B UTXO:        %s:%d\n", state.PoolBTxID, state.PoolBVout)
			fmt.Printf("LP Reserve UTXO:    %s:%d\n", state.LpReserveTxID, state.LpReserveVout)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
	cmd.Flags().StringVar(&poolID, "pool-id", "", "Look up pool by LP asset / pool ID (via Esplora)")
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&esploraURL, "esplora-url", "", "Esplora API URL (env: ANCHOR_ESPLORA_URL)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl and .args files")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	return cmd
}
