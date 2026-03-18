package main

import (
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
)

func cmdFindPools() *cobra.Command {
	var asset0, asset1, rpcURL, rpcUser, rpcPass, netName string
	var startBlock int
	cmd := &cobra.Command{
		Use:   "find-pools",
		Short: "Scan the chain for Anchor pools matching an asset pair",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			client := rpc.New(rpcURL, rpcUser, rpcPass)

			records, err := rpc.ScanPoolCreations(client, asset0, asset1, startBlock)
			if err != nil {
				return fmt.Errorf("scan: %w", err)
			}
			if len(records) == 0 {
				fmt.Println("No Anchor pools found.")
				return nil
			}

			type poolEntry struct {
				rec       rpc.PoolCreationRecord
				poolAAddr string
				poolBAddr string
				lpAsset   string
				reserve0  uint64
				reserve1  uint64
				depth     uint64
				closed    bool
			}

			var entries []poolEntry
			for _, rec := range records {
				// Decode the creation tx to get pool_a (output[0]) and pool_b (output[1]) addresses.
				decoded, err := client.DecodeRawTransaction(rec.TxID)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: decode tx %s: %v\n", rec.TxID, err)
					continue
				}
				if len(decoded.Vout) < 2 {
					continue
				}
				poolAAddr := decoded.Vout[0].ScriptPubKey.Address
				poolBAddr := decoded.Vout[1].ScriptPubKey.Address
				if poolAAddr == "" || poolBAddr == "" {
					continue
				}

				// Derive LP asset ID from creation tx input[0] outpoint.
				if len(decoded.Vin) == 0 {
					continue
				}
				lpID, err := tx.ComputeLPAssetID(decoded.Vin[0].TxID, decoded.Vin[0].Vout)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: LP asset for tx %s: %v\n", rec.TxID, err)
					continue
				}
				// LP asset ID: reverse internal bytes to display hex.
				lpBytes := lpID[:]
				for i, j := 0, len(lpBytes)-1; i < j; i, j = i+1, j-1 {
					lpBytes[i], lpBytes[j] = lpBytes[j], lpBytes[i]
				}
				lpAsset := hex.EncodeToString(lpBytes)

				// Scan pool addresses for live reserves.
				netName = resolveNetwork(netName)
				net, err := parseNetwork(netName)
				if err != nil {
					return err
				}
				lbtcAsset := net.AssetID

				utxosA, err := client.ScanAddress(poolAAddr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: scan pool_a %s: %v\n", poolAAddr, err)
					continue
				}
				utxosB, err := client.ScanAddress(poolBAddr)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: scan pool_b %s: %v\n", poolBAddr, err)
					continue
				}

				var reserve0, reserve1 uint64
				for _, u := range utxosA {
					if strings.EqualFold(u.Asset, rec.Asset0) {
						reserve0 += satoshis(u.Amount)
					}
				}
				for _, u := range utxosB {
					if strings.EqualFold(u.Asset, rec.Asset1) {
						reserve1 += satoshis(u.Amount)
					}
				}
				// Detect L-BTC-as-asset0 or asset1 pools using canonical asset ID.
				_ = lbtcAsset // used for future explicit L-BTC reserve handling

				entries = append(entries, poolEntry{
					rec:       rec,
					poolAAddr: poolAAddr,
					poolBAddr: poolBAddr,
					lpAsset:   lpAsset,
					reserve0:  reserve0,
					reserve1:  reserve1,
					depth:     reserve0 * reserve1,
					closed:    reserve0 == 0 && reserve1 == 0,
				})
			}

			// Sort by depth descending (deepest pool first).
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].depth > entries[j].depth
			})

			fmt.Printf("%-20s  %-6s  %-14s  %-14s  %s\n", "POOL (pool_a)", "FEE", "RESERVE0", "RESERVE1", "LP ASSET")
			fmt.Println(strings.Repeat("-", 100))
			for i, e := range entries {
				feeStr := fmt.Sprintf("%.4f%%", float64(e.rec.FeeNum)/float64(e.rec.FeeDen)*100)
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
				fmt.Printf("%-20s  %-6s  %-14d  %-14d  %s%s\n",
					addr, feeStr, e.reserve0, e.reserve1, e.lpAsset[:16]+"...", status)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID to filter by (case-insensitive)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID to filter by (case-insensitive)")
	cmd.Flags().IntVar(&startBlock, "start-block", 0, "Block height to start scanning from (default: 0 = genesis)")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	return cmd
}
