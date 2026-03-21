package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/esplora"
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

			type poolEntry struct {
				txid          string
				asset0        string
				asset1        string
				feeNum        uint16
				feeDen        uint16
				height        int
				poolAAddr     string
				poolBAddr     string
				lpAsset       string
				reserve0      uint64
				reserve1      uint64
				depth         uint64
				closed        bool
				creationVinTx string // vin[0] txid from creation tx
				creationVinV  uint32 // vin[0] vout from creation tx
			}

			var entries []poolEntry

			if esploraURL != "" {
				ec := esplora.New(esploraURL)
				fmt.Fprintf(os.Stderr, "Scanning via Esplora (%s) from block %d...\n", esploraURL, startBlock)

				records, err := esplora.ScanPoolCreations(ec, asset0, asset1, startBlock)
				if err != nil {
					return fmt.Errorf("esplora scan: %w", err)
				}
				if len(records) == 0 {
					fmt.Println("No Anchor pools found.")
					return nil
				}

				for _, rec := range records {
					// Fetch creation tx to get pool addresses and LP asset ID.
					creationTx, err := ec.GetTx(rec.TxID)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: get tx %s: %v\n", rec.TxID, err)
						continue
					}
					if len(creationTx.Vout) < 3 || len(creationTx.Vin) == 0 {
						continue
					}

					poolAAddr := creationTx.Vout[0].ScriptPubKeyAddr
					poolBAddr := creationTx.Vout[1].ScriptPubKeyAddr
					if poolAAddr == "" || poolBAddr == "" {
						continue
					}

					// Derive LP asset ID from creation tx input[0] outpoint.
					lpID, err := tx.ComputeLPAssetID(creationTx.Vin[0].TxID, creationTx.Vin[0].Vout)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: LP asset for tx %s: %v\n", rec.TxID, err)
						continue
					}
					lpAsset := hex.EncodeToString(revBytes(lpID[:]))

					// Query live reserves via Esplora address UTXO endpoint.
					utxosA, err := ec.GetAddressUTXOs(poolAAddr)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: utxos pool_a %s: %v\n", poolAAddr, err)
						continue
					}
					utxosB, err := ec.GetAddressUTXOs(poolBAddr)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: utxos pool_b %s: %v\n", poolBAddr, err)
						continue
					}

					var reserve0, reserve1 uint64
					for _, u := range utxosA {
						if strings.EqualFold(u.Asset, rec.Asset0) {
							reserve0 += u.Value
						}
					}
					for _, u := range utxosB {
						if strings.EqualFold(u.Asset, rec.Asset1) {
							reserve1 += u.Value
						}
					}

					entries = append(entries, poolEntry{
						txid:          rec.TxID,
						asset0:        rec.Asset0,
						asset1:        rec.Asset1,
						feeNum:        rec.FeeNum,
						feeDen:        rec.FeeDen,
						height:        rec.Height,
						poolAAddr:     poolAAddr,
						poolBAddr:     poolBAddr,
						lpAsset:       lpAsset,
						reserve0:      reserve0,
						reserve1:      reserve1,
						depth:         reserve0 * reserve1,
						closed:        reserve0 == 0 && reserve1 == 0,
						creationVinTx: creationTx.Vin[0].TxID,
						creationVinV:  creationTx.Vin[0].Vout,
					})
				}
			} else {
				// Fallback: RPC block-by-block scan (slow on long chains).
				fmt.Fprintf(os.Stderr, "No Esplora URL set — falling back to RPC scan (slow).\n")
				fmt.Fprintf(os.Stderr, "Set ANCHOR_ESPLORA_URL or use --esplora-url for faster scanning.\n")
				client := rpc.New(rpcURL, rpcUser, rpcPass)

				records, err := rpc.ScanPoolCreations(client, asset0, asset1, startBlock)
				if err != nil {
					return fmt.Errorf("rpc scan: %w", err)
				}
				if len(records) == 0 {
					fmt.Println("No Anchor pools found.")
					return nil
				}

				netName = resolveNetwork(netName)
				for _, rec := range records {
					decoded, err := client.DecodeRawTransaction(rec.TxID)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: decode tx %s: %v\n", rec.TxID, err)
						continue
					}
					if len(decoded.Vout) < 2 || len(decoded.Vin) == 0 {
						continue
					}
					poolAAddr := decoded.Vout[0].ScriptPubKey.Address
					poolBAddr := decoded.Vout[1].ScriptPubKey.Address
					if poolAAddr == "" || poolBAddr == "" {
						continue
					}

					lpID, err := tx.ComputeLPAssetID(decoded.Vin[0].TxID, decoded.Vin[0].Vout)
					if err != nil {
						fmt.Fprintf(os.Stderr, "warn: LP asset for tx %s: %v\n", rec.TxID, err)
						continue
					}
					lpAsset := hex.EncodeToString(revBytes(lpID[:]))

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

					entries = append(entries, poolEntry{
						txid:          rec.TxID,
						asset0:        rec.Asset0,
						asset1:        rec.Asset1,
						feeNum:        rec.FeeNum,
						feeDen:        rec.FeeDen,
						height:        rec.Height,
						poolAAddr:     poolAAddr,
						poolBAddr:     poolBAddr,
						lpAsset:       lpAsset,
						reserve0:      reserve0,
						reserve1:      reserve1,
						depth:         reserve0 * reserve1,
						closed:        reserve0 == 0 && reserve1 == 0,
						creationVinTx: decoded.Vin[0].TxID,
						creationVinV:  decoded.Vin[0].Vout,
					})
				}
			}

			if len(entries) == 0 {
				fmt.Println("No Anchor pools found.")
				return nil
			}

			// Sort by depth descending (deepest pool first).
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].depth > entries[j].depth
			})

			// ── Verify pool compatibility with current contracts ─────────
			// When --save is used, compile contracts for each pool and check
			// if the resulting pool_a address matches on-chain. Pools created
			// with an older contract version will not match and are filtered out.
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			if save {
				fmt.Fprintf(os.Stderr, "Verifying pool compatibility with current contracts...\n")

				var compatible []poolEntry
				for _, e := range entries {
				lpAssetID, err := tx.ComputeLPAssetID(e.creationVinTx, e.creationVinV)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: LP asset for tx %s: %v\n", e.txid, err)
					continue
				}
				feeDiff := uint64(e.feeDen) - uint64(e.feeNum)
				patchMap := map[string]compiler.ArgsParam{
					"ASSET0":     {Value: normalizeHex(e.asset0), Type: "u256"},
					"ASSET1":     {Value: normalizeHex(e.asset1), Type: "u256"},
					"FEE_NUM":    {Value: fmt.Sprintf("%d", e.feeNum), Type: "u64"},
					"FEE_DEN":    {Value: fmt.Sprintf("%d", e.feeDen), Type: "u64"},
					"FEE_DIFF":   {Value: fmt.Sprintf("%d", feeDiff), Type: "u64"},
					"LP_PREMINT": {Value: fmt.Sprintf("%d", pool.LPPremint), Type: "u64"},
				}
				if err := compiler.PatchParams(buildDir, patchMap); err != nil {
					fmt.Fprintf(os.Stderr, "warn: patch params for tx %s: %v\n", e.txid, err)
					continue
				}
				for _, shlName := range []string{
					"pool_a_swap.shl", "pool_a_remove.shl",
					"pool_b_swap.shl", "pool_b_remove.shl",
					"lp_reserve_add.shl", "lp_reserve_remove.shl",
				} {
					shlPath := buildDir + "/" + shlName
					_ = compiler.PatchLPAssetID(shlPath, shlPath, lpAssetID)
				}
				cfg, err := compiler.CompileAll(buildDir, net)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: compile for tx %s: %v\n", e.txid, err)
					continue
				}
				if cfg.PoolA.Address != e.poolAAddr {
					fmt.Fprintf(os.Stderr, "Skipping pool %s — created with older contract version\n", e.txid[:16]+"...")
					continue
				}
				compatible = append(compatible, e)
			}
				entries = compatible

				if len(entries) == 0 {
					fmt.Println("No compatible Anchor pools found (all were created with older contracts).")
					return nil
				}
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

			if !save {
				return nil
			}

			// ── Save selected pool to pool.json ──────────────────────────
			idx := saveIndex
			if saveIndex < 0 {
				fmt.Fprintf(os.Stderr, "\nSelect pool index to save [0]: ")
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					text := strings.TrimSpace(scanner.Text())
					if text != "" {
						n, err := strconv.Atoi(text)
						if err != nil || n < 0 || n >= len(entries) {
							return fmt.Errorf("invalid index %q — must be 0..%d", text, len(entries)-1)
						}
						idx = n
					} else {
						idx = 0
					}
				}
			}
			if idx < 0 || idx >= len(entries) {
				return fmt.Errorf("index %d out of range (0..%d)", idx, len(entries)-1)
			}
			selected := entries[idx]

			// Recompile for the selected pool (may already be compiled from verification).
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

			fmt.Fprintf(os.Stderr, "Compiling contracts for selected pool...\n")
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
				outFile = poolJSONName(selected.asset0, selected.asset1, uint64(selected.feeNum), uint64(selected.feeDen))
			}

			if err := cfg.Save(outFile); err != nil {
				return fmt.Errorf("save pool config: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Saved pool config to %s\n", outFile)
			fmt.Fprintf(os.Stderr, "  pool_a: %s\n", cfg.PoolA.Address)
			fmt.Fprintf(os.Stderr, "  pool_b: %s\n", cfg.PoolB.Address)
			fmt.Fprintf(os.Stderr, "  lp_reserve: %s\n", cfg.LpReserve.Address)
			fmt.Fprintf(os.Stderr, "  lp_asset: %s\n", selected.lpAsset)
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
	cmd.Flags().BoolVar(&save, "save", false, "Recompile contracts and save pool.json for a selected pool")
	cmd.Flags().StringVar(&saveFile, "out", "", "Output filename (default: auto-named pool-<a0>-<a1>-<bps>bps.json)")
	cmd.Flags().IntVar(&saveIndex, "index", -1, "Pool index to save (default: prompt interactively)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl and .args files")
	return cmd
}
