package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
	"github.com/vulpemventures/go-elements/network"
	"github.com/vulpemventures/go-elements/transaction"
)

var rootCmd = &cobra.Command{
	Use:   "anchor",
	Short: "Anchor: Liquid AMM tool for Simplicity contracts",
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(cmdCompile())
	rootCmd.AddCommand(cmdCreatePool())
	rootCmd.AddCommand(cmdPoolInfo())
	rootCmd.AddCommand(cmdSwap())
	rootCmd.AddCommand(cmdAddLiquidity())
	rootCmd.AddCommand(cmdRemoveLiquidity())
	rootCmd.AddCommand(cmdCheck())
	rootCmd.AddCommand(cmdFindPools())
}

// ── anchor compile ────────────────────────────────────────────────────────────

func cmdCompile() *cobra.Command {
	var buildDir, outFile, netName string
	var asset0Hex, asset1Hex string
	var feeNum, feeDen uint64
	cmd := &cobra.Command{
		Use:   "compile",
		Short: "Compile .shl contracts and write pool.json",
		RunE: func(cmd *cobra.Command, args []string) error {
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			// Patch .args files before compiling if asset/fee flags are set.
			params := map[string]compiler.ArgsParam{}
			if asset0Hex != "" {
				params["ASSET0"] = compiler.ArgsParam{Value: normalizeHex(asset0Hex), Type: "u256"}
			}
			if asset1Hex != "" {
				params["ASSET1"] = compiler.ArgsParam{Value: normalizeHex(asset1Hex), Type: "u256"}
			}
			if cmd.Flags().Changed("fee-num") || cmd.Flags().Changed("fee-den") {
				feeDiff := feeDen - feeNum
				params["FEE_NUM"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeNum), Type: "u64"}
				params["FEE_DEN"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeDen), Type: "u64"}
				params["FEE_DIFF"] = compiler.ArgsParam{Value: fmt.Sprintf("%d", feeDiff), Type: "u64"}
			}
			if len(params) > 0 {
				if err := compiler.PatchParams(buildDir, params); err != nil {
					return fmt.Errorf("patch params: %w", err)
				}
			}
			fmt.Fprintf(os.Stderr, "Compiling contracts from %s...\n", buildDir)
			cfg, err := compiler.CompileAll(buildDir, net)
			if err != nil {
				return err
			}
			cfg.FeeNum = feeNum
			cfg.FeeDen = feeDen
			fmt.Printf("pool_creation: %s\n", cfg.PoolCreation.Address)
			fmt.Printf("pool_a:        %s\n", cfg.PoolA.Address)
			fmt.Printf("pool_b:        %s\n", cfg.PoolB.Address)
			fmt.Printf("lp_reserve:    %s\n", cfg.LpReserve.Address)
			// Guard: if outFile already holds a deployed pool, prompt before overwriting.
			savePath := outFile
			if !cmd.Flags().Changed("out") {
				if existing, loadErr := pool.Load(outFile); loadErr == nil && existing.LPAssetID != "" {
					fmt.Fprintf(os.Stderr, "\nWARNING: %s already contains a deployed pool:\n", outFile)
					fmt.Fprintf(os.Stderr, "  asset0:      %s\n", existing.Asset0)
					fmt.Fprintf(os.Stderr, "  asset1:      %s\n", existing.Asset1)
					fmt.Fprintf(os.Stderr, "  fee:         %d/%d\n", existing.FeeNum, existing.FeeDen)
					fmt.Fprintf(os.Stderr, "  lp_asset_id: %s\n", existing.LPAssetID)
					autoName := poolJSONName(existing.Asset0, existing.Asset1, existing.FeeNum, existing.FeeDen)
					fmt.Fprintf(os.Stderr, "\n  [o] Overwrite %s\n", outFile)
					fmt.Fprintf(os.Stderr, "  [n] Save as new file: %s\n", autoName)
					fmt.Fprintf(os.Stderr, "  Or type a custom filename: ")
					choice := strings.TrimSpace(promptString(""))
					switch strings.ToLower(choice) {
					case "o":
						// overwrite — savePath stays as outFile
					case "n":
						savePath = autoName
					case "":
						fmt.Fprintf(os.Stderr, "Aborted.\n")
						return nil
					default:
						savePath = choice
					}
				}
			}
			if err := cfg.Save(savePath); err != nil {
				return fmt.Errorf("write %s: %w", savePath, err)
			}
			fmt.Fprintf(os.Stderr, "Wrote %s\n", savePath)
			return nil
		},
	}
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl files")
	cmd.Flags().StringVar(&outFile, "out", "pool.json", "Output pool.json path")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.Flags().StringVar(&asset0Hex, "asset0", "", "Asset0 ID (64-char hex) — patches .args before compiling")
	cmd.Flags().StringVar(&asset1Hex, "asset1", "", "Asset1 ID (64-char hex) — patches .args before compiling")
	cmd.Flags().Uint64Var(&feeNum, "fee-num", 997, "Fee numerator — patches .args before compiling")
	cmd.Flags().Uint64Var(&feeDen, "fee-den", 1000, "Fee denominator — patches .args before compiling")
	return cmd
}

// ── anchor create-pool ────────────────────────────────────────────────────────

// cmdCreatePool compiles contracts with the given asset IDs, funds the
// pool_creation address and deposit UTXOs from the wallet via sendmany, then
// builds, signs, and (optionally) broadcasts the create-pool transaction.
//
// If any of --asset0, --asset1, --deposit0, --deposit1 are omitted, an
// interactive wizard scans the wallet and prompts for the missing values.
//
// The pool_creation taproot address is only known after compilation (it
// depends on the asset IDs via CMR derivation), so there is no
// --creation-utxo flag — the command creates that UTXO itself.
func cmdCreatePool() *cobra.Command {
	var (
		poolFile, rpcURL, rpcUser, rpcPass string
		deposit0, deposit1, fee            uint64
		asset0, asset1, lbtcAsset, netName string
		broadcast                          bool
		buildDir, walletName               string
		feeNum, feeDen                     uint64
		noAnnounce, force                  bool
		startBlock                         int
	)
	cmd := &cobra.Command{
		Use:   "create-pool",
		Short: "Compile, fund, build, and broadcast a new AMM pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			if lbtcAsset == "" {
				lbtcAsset = net.AssetID
			}

			// Create RPC clients early — needed by wizard, duplicate check, and tx building.
			nodeClient := rpc.New(rpcURL, rpcUser, rpcPass)
			walletClient, err := nodeClient.LoadOrCreateWallet(walletName)
			if err != nil {
				return fmt.Errorf("wallet: %w", err)
			}

			const createPoolVbytes uint64 = 1600

			// ── Wizard: prompt for any missing required values ──────────────────
			wizardNeeded := asset0 == "" || asset1 == "" || deposit0 == 0 || deposit1 == 0
			if wizardNeeded {
				balances, err := walletExplicitAssets(walletClient)
				if err != nil {
					return fmt.Errorf("scan wallet assets: %w", err)
				}

				type assetEntry struct {
					id      string
					balance uint64
				}
				var assetList []assetEntry
				for id, bal := range balances {
					assetList = append(assetList, assetEntry{id, bal})
				}
				sort.Slice(assetList, func(i, j int) bool {
					if assetList[i].balance != assetList[j].balance {
						return assetList[i].balance > assetList[j].balance
					}
					return assetList[i].id < assetList[j].id
				})

				assetLabel := func(id string) string {
					if len(id) < 16 {
						return id
					}
					short := id[:8] + "..." + id[len(id)-5:]
					if strings.EqualFold(id, lbtcAsset) {
						return short + "  (L-BTC)"
					}
					return short
				}

				if asset0 == "" {
					if len(assetList) == 0 {
						return fmt.Errorf("no assets found in wallet — fund wallet first")
					}
					var opts []string
					for _, a := range assetList {
						opts = append(opts, fmt.Sprintf("%-32s  %d sats", assetLabel(a.id), a.balance))
					}
					idx := promptChoice("Select Asset0", opts)
					asset0 = assetList[idx].id
				}

				if asset1 == "" {
					var filteredList []assetEntry
					for _, a := range assetList {
						if !strings.EqualFold(a.id, asset0) {
							filteredList = append(filteredList, a)
						}
					}
					if len(filteredList) == 0 {
						return fmt.Errorf("only one asset type in wallet — fund a second asset before creating a pool")
					}
					var opts []string
					for _, a := range filteredList {
						opts = append(opts, fmt.Sprintf("%-32s  %d sats", assetLabel(a.id), a.balance))
					}
					idx := promptChoice("Select Asset1", opts)
					asset1 = filteredList[idx].id
				}

				// ── AMM fee tier ───────────────────────────────────────────────────────────
				// The fee tier is baked into the contract and determines the pool address.
				if !cmd.Flags().Changed("fee-num") && !cmd.Flags().Changed("fee-den") {
					pct := float64(feeDen-feeNum) / float64(feeDen) * 100
					fmt.Fprintf(os.Stderr, "AMM fee tier [default: %d/%d (%.2f%%)] — press Enter to keep or type num/den: ", feeNum, feeDen, pct)
					if raw := promptString(""); raw != "" {
						parts := strings.SplitN(raw, "/", 2)
						if len(parts) == 2 {
							if n, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64); err == nil {
								feeNum = n
							}
							if d, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64); err == nil {
								feeDen = d
							}
						}
					}
					pct = float64(feeDen-feeNum) / float64(feeDen) * 100
					fmt.Fprintf(os.Stderr, "Pool fee: %d/%d (%.2f%%)\n", feeNum, feeDen, pct)
				}

				// ── Early pool duplicate detection ──────────────────────────────────────
				// Compiles with the selected assets and fee params to derive the pool_a
				// address, then does a single ScanAddress RPC to check for live UTXOs.
				// This is O(1) RPCs and runs in < 1 second.
				if !force {
					fmt.Fprintf(os.Stderr, "Checking for existing pool...\n")
					earlyPatch := map[string]compiler.ArgsParam{
						"ASSET0":   {Value: normalizeHex(asset0), Type: "u256"},
						"ASSET1":   {Value: normalizeHex(asset1), Type: "u256"},
						"FEE_NUM":  {Value: fmt.Sprintf("%d", feeNum), Type: "u64"},
						"FEE_DEN":  {Value: fmt.Sprintf("%d", feeDen), Type: "u64"},
						"FEE_DIFF": {Value: fmt.Sprintf("%d", feeDen-feeNum), Type: "u64"},
					}
					if err := compiler.PatchParams(buildDir, earlyPatch); err != nil {
						return fmt.Errorf("patch params: %w", err)
					}
					earlyCfg, earlyErr := compiler.CompileAll(buildDir, net)
					if earlyErr != nil {
						return fmt.Errorf("compile: %w", earlyErr)
					}
					fmt.Fprintf(os.Stderr, "  Scanning: %s\n", earlyCfg.PoolA.Address)
				existUTXOs, scanErr := nodeClient.ScanAddress(earlyCfg.PoolA.Address)
					if scanErr != nil {
						fmt.Fprintf(os.Stderr, "warn: pool check failed: %v\n", scanErr)
					} else if len(existUTXOs) == 0 {
					// Secondary: pool.json may store the real on-chain address when contracts
					// changed since the pool was created (different CMR → different address).
					if savedCfg, loadErr := pool.Load(poolFile); loadErr == nil &&
						strings.EqualFold(savedCfg.Asset0, asset0) &&
						strings.EqualFold(savedCfg.Asset1, asset1) &&
						savedCfg.FeeNum == feeNum && savedCfg.FeeDen == feeDen &&
						savedCfg.PoolA.Address != "" &&
						savedCfg.PoolA.Address != earlyCfg.PoolA.Address {
						if altUTXOs, altErr := nodeClient.ScanAddress(savedCfg.PoolA.Address); altErr == nil && len(altUTXOs) > 0 {
							existUTXOs = altUTXOs
							earlyCfg = savedCfg
						}
					}
				}
				if len(existUTXOs) > 0 {
						var existReserve0, existReserve1 uint64
						for _, u := range existUTXOs {
							if strings.EqualFold(u.Asset, asset0) {
								existReserve0 += satoshis(u.Amount)
							}
						}
						poolBUTXOs, _ := nodeClient.ScanAddress(earlyCfg.PoolB.Address)
						for _, u := range poolBUTXOs {
							if strings.EqualFold(u.Asset, asset1) {
								existReserve1 += satoshis(u.Amount)
							}
						}
						fmt.Fprintf(os.Stderr, "\nA pool with these parameters already exists.\n")
						fmt.Fprintf(os.Stderr, "  pool_a:   %s\n", earlyCfg.PoolA.Address)
						fmt.Fprintf(os.Stderr, "  reserve0: %d sats\n", existReserve0)
						fmt.Fprintf(os.Stderr, "  reserve1: %d sats\n", existReserve1)
						if strings.ToLower(promptString("Add liquidity to this pool instead? [y/n]: ")) != "y" {
							fmt.Fprintf(os.Stderr, "\nTo create a new pool with these parameters anyway:\n")
							fmt.Fprintf(os.Stderr, "  anchor create-pool --force --asset0 %s --asset1 %s\n", asset0, asset1)
							return nil
						}

						// Get LP asset ID: try pool.json, then walk back from pool UTXO creating tx.
						lpAsset := ""
						if existPool, loadErr := pool.Load(poolFile); loadErr == nil {
							lpAsset = existPool.LPAssetID
						}
						if lpAsset == "" && len(existUTXOs) > 0 {
							fmt.Fprintf(os.Stderr, "Resolving LP asset ID from pool chain...\n")
							if resolved, resolveErr := resolveLPAsset(nodeClient, existUTXOs[0].TxID); resolveErr == nil {
								lpAsset = resolved
							} else {
								fmt.Fprintf(os.Stderr, "Walk-back failed: %v\n", resolveErr)
							}
						}
						earlyCfg.Asset0 = asset0
						earlyCfg.Asset1 = asset1
						earlyCfg.LPAssetID = lpAsset
						return runAddLiquidityWizard(earlyCfg, walletClient, nodeClient, lbtcAsset, balances, broadcast)
					}
				}

				// If no existing pool was found above, inform the user before continuing.
				fmt.Fprintf(os.Stderr, "No existing pool found for this asset pair and fee tier. Proceeding to create.\n")
				fmt.Fprintf(os.Stderr, "  (use --force to skip this check, or Ctrl+C to abort)\n")

				if deposit0 == 0 {
					bal := balances[asset0]
					fmt.Fprintf(os.Stderr, "Asset0 balance: %d sats (%s)\n", bal, assetLabel(asset0))
					for {
						v := promptUint64(fmt.Sprintf("Enter deposit0 amount (1-%d sats): ", bal), 0)
						if v > 0 && v <= bal {
							deposit0 = v
							break
						}
						fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", bal)
					}
				}

				if deposit1 == 0 {
					bal := balances[asset1]
					fmt.Fprintf(os.Stderr, "Asset1 balance: %d sats (%s)\n", bal, assetLabel(asset1))
					for {
						v := promptUint64(fmt.Sprintf("Enter deposit1 amount (1-%d sats): ", bal), 0)
						if v > 0 && v <= bal {
							deposit1 = v
							break
						}
						fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", bal)
					}
				}

				// ── Network fee rate ──────────────────────────────────────────────────────────
				if !cmd.Flags().Changed("fee") {
					feeRate := uint64(1)
					if estimated, err := nodeClient.EstimateSmartFee(2); err == nil && estimated > 0 {
						feeRate = estimated
					}
					feeRate = promptUint64(fmt.Sprintf("Network fee rate [default: %d sat/vbyte]: ", feeRate), feeRate)
					fee = feeRate * createPoolVbytes
					fmt.Fprintf(os.Stderr, "Total network fee: %d sats\n", fee)
				}
			}

			// Non-wizard fee estimation (all flags provided — no prompt).
			if !cmd.Flags().Changed("fee") && !wizardNeeded {
				feeRate := uint64(1)
				if estimated, err := nodeClient.EstimateSmartFee(2); err == nil && estimated > 0 {
					feeRate = estimated
				}
				fee = feeRate * createPoolVbytes
				fmt.Fprintf(os.Stderr, "Estimated fee: %d sats (%d sat/vbyte)\n", fee, feeRate)
			}

			// 1. Patch .args with asset IDs and fee params, then compile fresh.
			feeDiff := feeDen - feeNum
			patchMap := map[string]compiler.ArgsParam{
				"ASSET0":   {Value: normalizeHex(asset0), Type: "u256"},
				"ASSET1":   {Value: normalizeHex(asset1), Type: "u256"},
				"FEE_NUM":  {Value: fmt.Sprintf("%d", feeNum), Type: "u64"},
				"FEE_DEN":  {Value: fmt.Sprintf("%d", feeDen), Type: "u64"},
				"FEE_DIFF": {Value: fmt.Sprintf("%d", feeDiff), Type: "u64"},
			}
			if err := compiler.PatchParams(buildDir, patchMap); err != nil {
				return fmt.Errorf("patch params: %w", err)
			}
			fmt.Fprintf(os.Stderr, "Compiling contracts from %s...\n", buildDir)
			cfg, err := compiler.CompileAll(buildDir, net)
			if err != nil {
				return fmt.Errorf("compile: %w", err)
			}
			fmt.Fprintf(os.Stderr, "pool_creation: %s\n", cfg.PoolCreation.Address)
			fmt.Fprintf(os.Stderr, "pool_a:        %s\n", cfg.PoolA.Address)

			// 1b. Duplicate pool detection — check if pool_a address already has UTXOs.
			if !force {
				existingUTXOs, err := nodeClient.ScanAddress(cfg.PoolA.Address)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warn: duplicate check failed: %v\n", err)
				} else if len(existingUTXOs) > 0 {
					// Compute live reserves from the existing UTXOs.
					var existReserve0, existReserve1 uint64
					for _, u := range existingUTXOs {
						if strings.EqualFold(u.Asset, asset0) {
							existReserve0 += satoshis(u.Amount)
						}
					}
					poolBUTXOs, _ := nodeClient.ScanAddress(cfg.PoolB.Address)
					for _, u := range poolBUTXOs {
						if strings.EqualFold(u.Asset, asset1) {
							existReserve1 += satoshis(u.Amount)
						}
					}
					fmt.Fprintf(os.Stderr, "\nA pool with these parameters already exists.\n")
					fmt.Fprintf(os.Stderr, "  pool_a address: %s\n", cfg.PoolA.Address)
					fmt.Fprintf(os.Stderr, "  reserve0:       %d sats\n", existReserve0)
					fmt.Fprintf(os.Stderr, "  reserve1:       %d sats\n", existReserve1)
					fmt.Fprintf(os.Stderr, "\nTo add liquidity to this pool:\n")
					fmt.Fprintf(os.Stderr, "  anchor add-liquidity --deposit0 <sats> [--pool pool.json ...]\n")
					fmt.Fprintf(os.Stderr, "\nTo create a new pool with these same parameters anyway:\n")
					fmt.Fprintf(os.Stderr, "  anchor create-pool --force --asset0 %s --asset1 %s --deposit0 %d --deposit1 %d [flags]\n",
						asset0, asset1, deposit0, deposit1)
					return nil
				}
			}

			// 2. Get an unconfidential address for deposits and change.
			addr, err := walletClient.GetNewAddress()
			if err != nil {
				return fmt.Errorf("getnewaddress: %w", err)
			}
			unconfAddr, err := walletClient.GetUnconfidentialAddress(addr)
			if err != nil {
				unconfAddr = addr
			}

			// 3. Compute amounts.
			lpMinted := pool.IntSqrt(deposit0 * deposit1)
			creationNeeded := lpMinted + fee

			// ── Wizard: confirmation summary ───────────────────────────────────────
			if wizardNeeded {
				a0lbl := asset0
				if len(a0lbl) > 16 {
					a0lbl = asset0[:8] + "..." + asset0[len(asset0)-5:]
				}
				if strings.EqualFold(asset0, lbtcAsset) {
					a0lbl += " (L-BTC)"
				}
				a1lbl := asset1
				if len(a1lbl) > 16 {
					a1lbl = asset1[:8] + "..." + asset1[len(asset1)-5:]
				}
				if strings.EqualFold(asset1, lbtcAsset) {
					a1lbl += " (L-BTC)"
				}
				fmt.Fprintln(os.Stderr, "\n─────────────────────────────────────────")
				fmt.Fprintf(os.Stderr, "  Asset0:          %s\n", a0lbl)
				fmt.Fprintf(os.Stderr, "  Asset1:          %s\n", a1lbl)
				fmt.Fprintf(os.Stderr, "  Deposit0:        %d sats\n", deposit0)
				fmt.Fprintf(os.Stderr, "  Deposit1:        %d sats\n", deposit1)
				fmt.Fprintf(os.Stderr, "  Fee:             %d sats\n", fee)
				fmt.Fprintf(os.Stderr, "  LP tokens (est): %d\n", lpMinted)
				fmt.Fprintf(os.Stderr, "  pool_a:          %s\n", cfg.PoolA.Address)
				fmt.Fprintln(os.Stderr, "─────────────────────────────────────────")
				fmt.Fprintf(os.Stderr, "\nThis will send funds from your wallet and broadcast immediately.\n")
				if answer := promptString("Confirm and create pool? [y/n]: "); strings.ToLower(answer) != "y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
				// Wizard confirmation = consent to broadcast — always sign and send.
				broadcast = true
			} else {
				fmt.Fprintf(os.Stderr, "LP minted (estimated): %d\n", lpMinted)
				fmt.Fprintf(os.Stderr, "Creation UTXO needed:  %d sats L-BTC\n", creationNeeded)
			}

			// 4. Fund pool_creation + deposit0 in one L-BTC sendmany.
			fmt.Fprintf(os.Stderr, "Funding pool_creation (%d) + deposit0 (%d) L-BTC via sendmany...\n",
				creationNeeded, deposit0)
			lbtcTxid, err := walletClient.SendMany(map[string]uint64{
				cfg.PoolCreation.Address: creationNeeded,
				unconfAddr:               deposit0,
			}, "")
			if err != nil {
				return fmt.Errorf("fund L-BTC: %w", err)
			}
			fmt.Fprintf(os.Stderr, "L-BTC funding txid: %s\n", lbtcTxid)

			// 5. Fund deposit1 (Asset1) via a separate sendmany if it differs from L-BTC.
			a1Txid := lbtcTxid
			if !strings.EqualFold(asset1, lbtcAsset) {
				fmt.Fprintf(os.Stderr, "Funding deposit1 (%d sats Asset1) via sendmany...\n", deposit1)
				a1Txid, err = walletClient.SendMany(map[string]uint64{unconfAddr: deposit1}, asset1)
				if err != nil {
					return fmt.Errorf("fund Asset1: %w", err)
				}
				fmt.Fprintf(os.Stderr, "Asset1 funding txid: %s\n", a1Txid)
			}

			// 6. Wait for funding txs to confirm (Liquid testnet ~1 min blocks).
			const fundTimeout = 10 * time.Minute
			const fundInterval = 30 * time.Second
			fmt.Fprintf(os.Stderr, "Waiting for L-BTC funding to confirm...\n")
			if err := walletClient.WaitForConfirmations(lbtcTxid, 1, fundTimeout, fundInterval); err != nil {
				return fmt.Errorf("L-BTC funding confirm: %w", err)
			}
			if a1Txid != lbtcTxid {
				fmt.Fprintf(os.Stderr, "Waiting for Asset1 funding to confirm...\n")
				if err := walletClient.WaitForConfirmations(a1Txid, 1, fundTimeout, fundInterval); err != nil {
					return fmt.Errorf("Asset1 funding confirm: %w", err)
				}
			}

			// 7. Locate explicit UTXOs in the funding txs by scanning outputs.
			findVout := func(txid string, wantSats uint64, wantAsset string) (uint32, error) {
				for v := uint32(0); v <= 10; v++ {
					info, err := nodeClient.GetTxOut(txid, v)
					if err != nil || info == nil || info.Amount == 0 {
						continue
					}
					if satoshis(info.Amount) == wantSats && strings.EqualFold(info.Asset, wantAsset) {
						return v, nil
					}
				}
				return 0, fmt.Errorf("UTXO %d sats %s not found in tx %s", wantSats, wantAsset, txid)
			}
			creationVout, err := findVout(lbtcTxid, creationNeeded, lbtcAsset)
			if err != nil {
				return fmt.Errorf("creation UTXO: %w", err)
			}
			a0Vout, err := findVout(lbtcTxid, deposit0, lbtcAsset)
			if err != nil {
				return fmt.Errorf("deposit0 UTXO: %w", err)
			}
			a1Vout, err := findVout(a1Txid, deposit1, asset1)
			if err != nil {
				return fmt.Errorf("deposit1 UTXO: %w", err)
			}
			fmt.Fprintf(os.Stderr, "creation: %s:%d  deposit0: %s:%d  deposit1: %s:%d\n",
				lbtcTxid, creationVout, lbtcTxid, a0Vout, a1Txid, a1Vout)

			// 8. Build create-pool transaction.
			txParams := &tx.CreatePoolParams{
				CreationTxID:   lbtcTxid,
				CreationVout:   creationVout,
				CreationAmount: creationNeeded,
				Asset0TxID:     lbtcTxid,
				Asset0Vout:     a0Vout,
				Asset0Amount:   deposit0,
				Asset1TxID:     a1Txid,
				Asset1Vout:     a1Vout,
				Asset1Amount:   deposit1,
				BuildDir:       buildDir,
				Deposit0:       deposit0,
				Deposit1:       deposit1,
				Asset0:         asset0,
				Asset1:         asset1,
				LBTCAsset:      lbtcAsset,
				Fee:            fee,
				FeeNum:         feeNum,
				FeeDen:         feeDen,
				Announce:       !noAnnounce,
				ChangeAddr:     unconfAddr,
				Network:        net,
			}
			result, err := tx.BuildCreatePool(txParams, cfg)
			if err != nil {
				return fmt.Errorf("build tx: %w", err)
			}
			result.PoolConfig.FeeNum = feeNum
			result.PoolConfig.FeeDen = feeDen
			fmt.Printf("LP Asset ID:           %s\n", result.LPAssetID)
			fmt.Printf("LP Minted:             %d\n", result.LPMinted)
			fmt.Printf("LP Reserve:            %d\n", pool.LPPremint-result.LPMinted)

			if err := result.PoolConfig.Save(poolFile); err != nil {
				return fmt.Errorf("update %s: %w", poolFile, err)
			}
			fmt.Fprintf(os.Stderr, "Updated %s\n", poolFile)

			// 9. Sign, attach Simplicity witness, and broadcast.
			if broadcast {
				signed, complete, err := walletClient.SignRawTransactionWithWallet(result.TxHex)
				if err != nil {
					return fmt.Errorf("sign: %w", err)
				}
				if !complete {
					fmt.Fprintln(os.Stderr, "Warning: signing incomplete — some inputs may not be wallet-owned")
				}
				finalHex, err := attachWitness(signed, 0, result.SimplicityWitness)
				if err != nil {
					return fmt.Errorf("attach witness: %w", err)
				}
				txid, err := walletClient.SendRawTransaction(finalHex)
				if err != nil {
					return fmt.Errorf("broadcast: %w", err)
				}
				fmt.Printf("Txid: %s\n", txid)
			} else {
				fmt.Printf("Tx (hex): %s\n", result.TxHex)
				fmt.Fprintln(os.Stderr, "(use --broadcast to sign and send)")
			}
			return nil
		},
	}
	cmd.Flags().Uint64Var(&deposit0, "deposit0", 0, "Asset0 sats to lock in pool_a (prompted if omitted)")
	cmd.Flags().Uint64Var(&deposit1, "deposit1", 0, "Asset1 sats to lock in pool_b (prompted if omitted)")
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID hex (prompted if omitted)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID hex (prompted if omitted)")
	cmd.Flags().StringVar(&lbtcAsset, "lbtc-asset", "", "L-BTC asset ID (default: network native asset)")
	cmd.Flags().Uint64Var(&fee, "fee", 500, "Fee in satoshis (auto-estimated if not set)")
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Output pool.json path")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl and .args files")
	cmd.Flags().Uint64Var(&feeNum, "fee-num", 997, "Fee numerator (default 997 = 0.3% fee)")
	cmd.Flags().Uint64Var(&feeDen, "fee-den", 1000, "Fee denominator (default 1000)")
	cmd.Flags().StringVar(&walletName, "wallet", "anchor", "Wallet name to load/create for funding and signing")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().BoolVar(&broadcast, "broadcast", false, "Sign with wallet and broadcast")
	cmd.Flags().BoolVar(&noAnnounce, "no-announce", false, "Skip OP_RETURN pool discovery announcement")
	cmd.Flags().BoolVar(&force, "force", false, "Skip duplicate pool check and create anyway")
	cmd.Flags().IntVar(&startBlock, "start-block", -1, "Block height to start OP_RETURN scan when LP asset ID is unknown (-1 = skip scan, prompt user)")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	return cmd
}

// ── anchor pool-info ──────────────────────────────────────────────────────────

func cmdPoolInfo() *cobra.Command {
	var poolFile, rpcURL, rpcUser, rpcPass string
	cmd := &cobra.Command{
		Use:   "pool-info",
		Short: "Query live pool reserves from chain",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			cfg, err := pool.Load(poolFile)
			if err != nil {
				return err
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)
			state, err := pool.Query(cfg, client)
			if err != nil {
				return err
			}
			fmt.Printf("Reserve0 (Asset0): %d sat\n", state.Reserve0)
			fmt.Printf("Reserve1 (Asset1): %d sat\n", state.Reserve1)
			fmt.Printf("Total Supply (LP):  %d sat\n", state.TotalSupply())
			fmt.Printf("LP Reserve:         %d sat\n", state.LPReserve)
			fmt.Printf("Pool A UTXO:        %s:%d\n", state.PoolATxID, state.PoolAVout)
			fmt.Printf("Pool B UTXO:        %s:%d\n", state.PoolBTxID, state.PoolBVout)
			fmt.Printf("LP Reserve UTXO:    %s:%d\n", state.LpReserveTxID, state.LpReserveVout)
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	return cmd
}

// ── anchor swap ───────────────────────────────────────────────────────────────

func cmdSwap() *cobra.Command {
	var (
		poolFile, rpcURL, rpcUser, rpcPass   string
		inAsset, userUTXO, userAddr, netName string
		amountIn, minOut, fee                uint64
		lbtcAsset, asset0, asset1            string
		broadcast                            bool
		walletName                           string
	)
	cmd := &cobra.Command{
		Use:   "swap",
		Short: "Swap assets using the AMM pool",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			cfg, err := pool.Load(poolFile)
			if err != nil {
				return err
			}
			// Read asset IDs from pool.json if not provided via flags
			if asset0 == "" {
				asset0 = cfg.Asset0
			}
			if asset1 == "" {
				asset1 = cfg.Asset1
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)
			state, err := pool.Query(cfg, client)
			if err != nil {
				return fmt.Errorf("query pool: %w", err)
			}

			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			if lbtcAsset == "" {
				lbtcAsset = net.AssetID
			}

			// Estimate fee if not explicitly set.
			if !cmd.Flags().Changed("fee") {
				if estimated, err := client.EstimateSmartFee(2); err == nil {
					fee = estimated * 1200 // ~1200 vbytes for swap tx
					fmt.Fprintf(os.Stderr, "Estimated fee: %d sats\n", fee)
				}
			}

			swapAsset0In := strings.EqualFold(inAsset, "asset0") || strings.EqualFold(inAsset, asset0)

			// Auto-select user UTXO from wallet if not provided.
			if userUTXO == "" {
				inputAssetID := asset1
				if swapAsset0In {
					inputAssetID = asset0
				}
				walletClient, err := client.LoadOrCreateWallet(walletName)
				if err != nil {
					return fmt.Errorf("wallet for auto-select: %w", err)
				}
				utxos, err := walletClient.ListUnspentByAsset(inputAssetID)
				if err != nil {
					return fmt.Errorf("list unspent: %w", err)
				}
				needed := amountIn + fee
				var best *rpc.WalletUTXO
				for i := range utxos {
					u := &utxos[i]
					if satoshis(u.Amount) >= needed {
						if best == nil || u.Amount < best.Amount {
							best = u
						}
					}
				}
				if best == nil {
					return fmt.Errorf("no suitable UTXO found for asset %s (need %d sats) — fund wallet or use --user-utxo", inputAssetID, needed)
				}
				userUTXO = fmt.Sprintf("%s:%d", best.TxID, best.Vout)
				fmt.Fprintf(os.Stderr, "Auto-selected UTXO: %s:%d (%d sats)\n", best.TxID, best.Vout, satoshis(best.Amount))
			}

			userTxid, userVout, err := parseOutpoint(userUTXO)
			if err != nil {
				return err
			}

			feeNum, feeDen := cfg.FeeNum, cfg.FeeDen
			if feeNum == 0 || feeDen == 0 {
				// Legacy pool.json without fee params — assume zero fee.
				feeNum, feeDen = 1, 1
			}

			var reserveIn, reserveOut uint64
			if swapAsset0In {
				reserveIn, reserveOut = state.Reserve0, state.Reserve1
			} else {
				reserveIn, reserveOut = state.Reserve1, state.Reserve0
			}
			expectedOut := pool.SwapOutput(amountIn, reserveIn, reserveOut, feeNum, feeDen)
			fmt.Printf("Expected output: %d sats\n", expectedOut)

			params := &tx.SwapParams{
				State:             state,
				SwapAsset0In:      swapAsset0In,
				AmountIn:          amountIn,
				MinAmountOut:      minOut,
				UserTxID:          userTxid,
				UserVout:          userVout,
				UserAsset:         inAsset,
				PoolAAddr:         cfg.PoolA.Address,
				PoolBAddr:         cfg.PoolB.Address,
				Asset0:            asset0,
				Asset1:            asset1,
				LBTCAsset:         lbtcAsset,
				Fee:               fee,
				FeeNum:            feeNum,
				FeeDen:            feeDen,
				PoolABinaryHex:    cfg.PoolASwap.BinaryHex,
				PoolBBinaryHex:    cfg.PoolBSwap.BinaryHex,
				PoolACMRHex:       cfg.PoolASwap.CMR,
				PoolBCMRHex:       cfg.PoolBSwap.CMR,
				PoolAControlBlock: cfg.PoolASwap.ControlBlock,
				PoolBControlBlock: cfg.PoolBSwap.ControlBlock,
			}

			result, err := tx.BuildSwap(params)
			if err != nil {
				return err
			}

			if broadcast {
				// Sign wallet input (input[2]) first, then attach Simplicity witnesses.
				signed, complete, err := client.SignRawTransactionWithWallet(result.TxHex)
				if err != nil {
					return translateError(fmt.Errorf("sign: %w", err))
				}
				if !complete {
					fmt.Fprintln(os.Stderr, "Warning: signing incomplete — user input may not be wallet-owned")
				}
				withA, err := attachWitness(signed, 0, result.PoolAWitness)
				if err != nil {
					return fmt.Errorf("attach pool_a witness: %w", err)
				}
				finalHex, err := attachWitness(withA, 1, result.PoolBWitness)
				if err != nil {
					return fmt.Errorf("attach pool_b witness: %w", err)
				}
				txid, err := client.SendRawTransaction(finalHex)
				if err != nil {
					return translateError(fmt.Errorf("broadcast: %w", err))
				}
				fmt.Printf("Txid: %s\n", txid)
			} else {
				fmt.Printf("Tx (hex): %s\n", result.TxHex)
				fmt.Fprintln(os.Stderr, "(use --broadcast to sign and send)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&inAsset, "in-asset", "asset0", "Input asset: 'asset0' or 'asset1'")
	cmd.Flags().Uint64Var(&amountIn, "amount", 0, "Amount to swap in satoshis (required)")
	cmd.Flags().Uint64Var(&minOut, "min-out", 0, "Minimum acceptable output in satoshis")
	cmd.Flags().StringVar(&userUTXO, "user-utxo", "", "User's input UTXO as txid:vout (auto-selected from wallet if omitted)")
	cmd.Flags().StringVar(&userAddr, "user-addr", "", "User's output address for received asset (required)")
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&lbtcAsset, "lbtc-asset", "", "L-BTC asset ID")
	cmd.Flags().Uint64Var(&fee, "fee", 500, "Fee in satoshis (auto-estimated if not set)")
	cmd.Flags().StringVar(&walletName, "wallet", "anchor", "Wallet name for UTXO auto-selection")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().BoolVar(&broadcast, "broadcast", false, "Broadcast transaction via RPC")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	_ = cmd.MarkFlagRequired("amount")
	_ = cmd.MarkFlagRequired("user-addr")
	return cmd
}

// ── anchor add-liquidity ──────────────────────────────────────────────────────

func cmdAddLiquidity() *cobra.Command {
	var (
		poolFile, rpcURL, rpcUser, rpcPass, buildDir              string
		deposit0, deposit1, fee                                   uint64
		asset0UTXO, asset1UTXO, lbtcUTXO, userAddr               string
		asset0Amount, asset1Amount, lbtcAmount                    uint64
		asset0, asset1, lbtcAsset, lpAssetID, netName, changeAddr string
		broadcast                                                 bool
		walletName                                                string
	)
	cmd := &cobra.Command{
		Use:   "add-liquidity",
		Short: "Add liquidity to the pool and receive LP tokens",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			if lbtcAsset == "" {
				lbtcAsset = net.AssetID
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)

			// ── Wizard mode: no deposit0 flag → asset selection + pool discovery ─────
			if !cmd.Flags().Changed("deposit0") {
				wc, werr := client.LoadOrCreateWallet(walletName)
				if werr != nil {
					return fmt.Errorf("wallet: %w", werr)
				}
				balances, _ := walletExplicitAssets(wc)

				// Build asset list from wallet.
				type assetEntry struct{ id, label string; balance uint64 }
				wizLabel := func(id string) string {
					if len(id) < 16 { return id }
					short := id[:8] + "..." + id[len(id)-5:]
					if strings.EqualFold(id, lbtcAsset) { return short + "  (L-BTC)" }
					return short
				}
				var assetList []assetEntry
				for id, bal := range balances {
					assetList = append(assetList, assetEntry{id, wizLabel(id), bal})
				}
				if len(assetList) < 2 {
					return fmt.Errorf("need at least 2 asset types in wallet to add liquidity")
				}
				// Sort: L-BTC first, then alphabetical.
				sort.Slice(assetList, func(i, j int) bool {
					li := strings.EqualFold(assetList[i].id, lbtcAsset)
					lj := strings.EqualFold(assetList[j].id, lbtcAsset)
					if li != lj { return li }
					return assetList[i].label < assetList[j].label
				})

				// Select asset0.
				var opts0 []string
				for _, a := range assetList {
					opts0 = append(opts0, fmt.Sprintf("%-32s  %d sats", a.label, a.balance))
				}
				pick0 := promptChoice("Select Asset0", opts0)
				selA0 := assetList[pick0]

				// Select asset1 (exclude asset0).
				var filtered []assetEntry
				for _, a := range assetList {
					if a.id != selA0.id {
						filtered = append(filtered, a)
					}
				}
				var opts1 []string
				for _, a := range filtered {
					opts1 = append(opts1, fmt.Sprintf("%-32s  %d sats", a.label, a.balance))
				}
				pick1 := promptChoice("Select Asset1", opts1)
				selA1 := filtered[pick1]

				// AMM fee tier.
				wizFeeNum, wizFeeDen := uint64(997), uint64(1000)
				{
					pct := float64(wizFeeDen-wizFeeNum) / float64(wizFeeDen) * 100
					fmt.Fprintf(os.Stderr, "AMM fee tier [default: %d/%d (%.2f%%)] — press Enter to keep or type num/den: ", wizFeeNum, wizFeeDen, pct)
					if raw := promptString(""); raw != "" {
						parts := strings.SplitN(raw, "/", 2)
						if len(parts) == 2 {
							if n, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 64); err == nil { wizFeeNum = n }
							if d, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 64); err == nil { wizFeeDen = d }
						}
					}
					pct = float64(wizFeeDen-wizFeeNum) / float64(wizFeeDen) * 100
					fmt.Fprintf(os.Stderr, "Pool fee: %d/%d (%.2f%%)\n", wizFeeNum, wizFeeDen, pct)
				}

				// Compile and scan for existing pool.
				fmt.Fprintf(os.Stderr, "Searching for pool...\n")
				wizPatch := map[string]compiler.ArgsParam{
					"ASSET0":   {Value: normalizeHex(selA0.id), Type: "u256"},
					"ASSET1":   {Value: normalizeHex(selA1.id), Type: "u256"},
					"FEE_NUM":  {Value: fmt.Sprintf("%d", wizFeeNum), Type: "u64"},
					"FEE_DEN":  {Value: fmt.Sprintf("%d", wizFeeDen), Type: "u64"},
					"FEE_DIFF": {Value: fmt.Sprintf("%d", wizFeeDen-wizFeeNum), Type: "u64"},
				}
				if err := compiler.PatchParams(buildDir, wizPatch); err != nil {
					return fmt.Errorf("patch: %w", err)
				}
				wizCfg, wizErr := compiler.CompileAll(buildDir, net)
				if wizErr != nil {
					return fmt.Errorf("compile: %w", wizErr)
				}
				fmt.Fprintf(os.Stderr, "  Scanning: %s\n", wizCfg.PoolA.Address)
				poolUTXOs, _ := client.ScanAddress(wizCfg.PoolA.Address)

				// Secondary: try pool.json address if contracts changed.
				if len(poolUTXOs) == 0 {
					if saved, loadErr := pool.Load(poolFile); loadErr == nil &&
						strings.EqualFold(saved.Asset0, selA0.id) &&
						strings.EqualFold(saved.Asset1, selA1.id) &&
						saved.FeeNum == wizFeeNum && saved.FeeDen == wizFeeDen &&
						saved.PoolA.Address != "" && saved.PoolA.Address != wizCfg.PoolA.Address {
						if alt, altErr := client.ScanAddress(saved.PoolA.Address); altErr == nil && len(alt) > 0 {
							poolUTXOs = alt
							wizCfg = saved
						}
					}
				}

				if len(poolUTXOs) == 0 {
					fmt.Fprintf(os.Stderr, "No pool found for %s/%s at fee %d/%d.\n", selA0.label, selA1.label, wizFeeNum, wizFeeDen)
					fmt.Fprintf(os.Stderr, "Use anchor create-pool to create one.\n")
					return nil
				}

				// Get LP asset ID: try pool.json, then walk back from pool UTXO creating tx.
				// In every pool operation tx, pool_a is vin[0]. Walk back until we find
				// the creation tx (has ANCHR OP_RETURN), then compute LP asset from its vin[0].
				wizLPAsset := ""
				if saved, loadErr := pool.Load(poolFile); loadErr == nil {
					if strings.EqualFold(saved.Asset0, selA0.id) && strings.EqualFold(saved.Asset1, selA1.id) {
						wizLPAsset = saved.LPAssetID
					}
				}
				if wizLPAsset == "" && len(poolUTXOs) > 0 {
					fmt.Fprintf(os.Stderr, "Resolving LP asset ID from pool chain...\n")
					if resolved, resolveErr := resolveLPAsset(client, poolUTXOs[0].TxID); resolveErr == nil {
						wizLPAsset = resolved
					} else {
						fmt.Fprintf(os.Stderr, "Walk-back failed: %v\n", resolveErr)
					}
				}
				if wizLPAsset == "" {
					fmt.Fprintf(os.Stderr, "Could not resolve LP asset ID automatically.\n")
					wizLPAsset = strings.TrimSpace(promptString("Enter LP asset ID manually: "))
					if wizLPAsset == "" {
						return fmt.Errorf("LP asset ID required")
					}
				}

				wizCfg.Asset0 = selA0.id
				wizCfg.Asset1 = selA1.id
				wizCfg.LPAssetID = wizLPAsset
				wizCfg.FeeNum = wizFeeNum
				wizCfg.FeeDen = wizFeeDen
				return runAddLiquidityWizard(wizCfg, wc, client, lbtcAsset, balances, broadcast)
			}

			// ── Flag mode: load pool.json and proceed ────────────────────────────────
			cfg, err := pool.Load(poolFile)
			if err != nil {
				return err
			}
			// Read asset IDs from pool.json if not provided via flags
			if asset0 == "" {
				asset0 = cfg.Asset0
			}
			if asset1 == "" {
				asset1 = cfg.Asset1
			}
			if lpAssetID == "" {
				lpAssetID = cfg.LPAssetID
			}
			state, err := pool.Query(cfg, client)
			if err != nil {
				return fmt.Errorf("query pool: %w", err)
			}

			// Estimate fee if not explicitly set.
			if !cmd.Flags().Changed("fee") {
				if estimated, err := client.EstimateSmartFee(2); err == nil {
					fee = estimated * 1400 // ~1400 vbytes for add-liquidity tx
					fmt.Fprintf(os.Stderr, "Estimated fee: %d sats\n", fee)
				}
			}

			// Enforce proportional deposit: deposit1 must equal floor(deposit0 * reserve1 / reserve0).
			// The lp_supply_add contract checks: deposit1*reserve0 <= deposit0*reserve1 < (deposit1+1)*reserve0.
			// The CLI always computes deposit1 from deposit0 (user's deposit0 is authoritative).
			// deposit1 provided via flag is ignored when reserves are known — use deposit0 to control the deposit.
			if state.Reserve0 > 0 && state.Reserve1 > 0 {
				// floor(deposit0 * reserve1 / reserve0)
				correctDeposit1 := deposit0 * state.Reserve1 / state.Reserve0
				if correctDeposit1 == 0 {
					return fmt.Errorf("deposit0 (%d sats) is too small relative to reserves %d:%d — floor ratio yields 0 of asset1; increase deposit0",
						deposit0, state.Reserve0, state.Reserve1)
				}
				if correctDeposit1 != deposit1 {
					fmt.Fprintf(os.Stderr, "Adjusting deposit1 to floor proportion (reserves %d:%d):\n",
						state.Reserve0, state.Reserve1)
					fmt.Fprintf(os.Stderr, "  deposit1: %d → %d\n", deposit1, correctDeposit1)
					deposit1 = correctDeposit1
				}
			}

			// Compute LP minted (quote).
			totalSupply := state.TotalSupply()
			lpMinted := pool.LPMintedForDeposit(deposit0, deposit1, state.Reserve0, state.Reserve1, totalSupply)
			fmt.Fprintf(os.Stderr, "LP minted (estimated): %d\n", lpMinted)
			_ = lpMinted // used only for display; actual minting handled by BuildAddLiquidity

			// Auto-select UTXOs and get change/recipient addresses from wallet.
			walletClient, err := client.LoadOrCreateWallet(walletName)
			if err != nil {
				return fmt.Errorf("wallet: %w", err)
			}

			type sel struct {
				outpoint string
				amount   uint64
			}
			autoSelect := func(label, assetID string, needed uint64, exclude ...string) (sel, error) {
				utxos, err := walletClient.ListUnspentByAsset(assetID)
				if err != nil {
					return sel{}, fmt.Errorf("list unspent %s: %w", label, err)
				}
				var best *rpc.WalletUTXO
				for i := range utxos {
					u := &utxos[i]
					outpoint := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
					skip := false
					for _, ex := range exclude {
						if ex == outpoint {
							skip = true
							break
						}
					}
					if skip {
						continue
					}
					if satoshis(u.Amount) >= needed {
						if best == nil || u.Amount < best.Amount {
							best = u
						}
					}
				}
				if best == nil {
					extra := ""
					if label == "lbtc" && strings.EqualFold(assetID, asset0) {
						extra = "\n  asset0 is L-BTC — a second separate L-BTC UTXO is required for the fee; fund one or use --lbtc-utxo"
					}
					return sel{}, fmt.Errorf("no suitable %s UTXO (need %d sats) — use --%s-utxo%s", label, needed, label, extra)
				}
				chosen := fmt.Sprintf("%s:%d", best.TxID, best.Vout)
				fmt.Fprintf(os.Stderr, "Auto-selected %s UTXO: %s (%d sats)\n", label, chosen, satoshis(best.Amount))
				return sel{chosen, satoshis(best.Amount)}, nil
			}

			if asset0UTXO == "" {
				s, e := autoSelect("asset0", asset0, deposit0)
				if e != nil {
					return e
				}
				asset0UTXO, asset0Amount = s.outpoint, s.amount
			}
			if asset1UTXO == "" {
				s, e := autoSelect("asset1", asset1, deposit1, asset0UTXO)
				if e != nil {
					return e
				}
				asset1UTXO, asset1Amount = s.outpoint, s.amount
			}
			if lbtcUTXO == "" {
				s, e := autoSelect("lbtc", lbtcAsset, fee, asset0UTXO, asset1UTXO)
				if e != nil {
					return e
				}
				lbtcUTXO, lbtcAmount = s.outpoint, s.amount
			}

			// For explicitly-provided UTXOs, look up their amounts.
			lookupAmount := func(outpoint, assetID string) (uint64, error) {
				txid, vout, err := parseOutpoint(outpoint)
				if err != nil {
					return 0, err
				}
				info, err := client.GetTxOut(txid, vout)
				if err != nil || info == nil {
					return 0, fmt.Errorf("could not fetch UTXO %s", outpoint)
				}
				return satoshis(info.Amount), nil
			}
			if asset0Amount == 0 {
				asset0Amount, err = lookupAmount(asset0UTXO, asset0)
				if err != nil {
					return err
				}
			}
			if asset1Amount == 0 {
				asset1Amount, err = lookupAmount(asset1UTXO, asset1)
				if err != nil {
					return err
				}
			}
			if lbtcAmount == 0 {
				lbtcAmount, err = lookupAmount(lbtcUTXO, lbtcAsset)
				if err != nil {
					return err
				}
			}

			// Get change and LP recipient addresses from wallet (unconfidential — tx uses explicit outputs).
			unconfAddr := func(label string) (string, error) {
				a, err := walletClient.GetNewAddress()
				if err != nil {
					return "", fmt.Errorf("getnewaddress (%s): %w", label, err)
				}
				u, err := walletClient.GetUnconfidentialAddress(a)
				if err != nil {
					u = a
				}
				return u, nil
			}
			if changeAddr == "" {
				if changeAddr, err = unconfAddr("change"); err != nil {
					return err
				}
			}

			// Derive LP recipient address if not provided.
			if userAddr == "" {
				if userAddr, err = unconfAddr("LP recipient"); err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "LP tokens will be sent to: %s\n", userAddr)
			}

			// Convert single-UTXO flag values to UserInput slices.
			toInputs := func(outpoint string, amount uint64) []tx.UserInput {
				txid, vout, _ := parseOutpoint(outpoint)
				return []tx.UserInput{{TxID: txid, Vout: vout, Amount: amount}}
			}
			return execAddLiquidity(cfg, state, deposit0, deposit1,
				toInputs(asset0UTXO, asset0Amount),
				toInputs(asset1UTXO, asset1Amount),
				toInputs(lbtcUTXO, lbtcAmount),
				asset0, asset1, lbtcAsset, lpAssetID,
				changeAddr, userAddr, fee, walletClient, broadcast)
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().Uint64Var(&deposit0, "deposit0", 0, "Asset0 amount to deposit (required)")
	cmd.Flags().Uint64Var(&deposit1, "deposit1", 0, "Asset1 amount to deposit (required)")
	cmd.Flags().StringVar(&asset0UTXO, "asset0-utxo", "", "User's Asset0 UTXO as txid:vout (auto-selected if omitted)")
	cmd.Flags().StringVar(&asset1UTXO, "asset1-utxo", "", "User's Asset1 UTXO as txid:vout (auto-selected if omitted)")
	cmd.Flags().StringVar(&lbtcUTXO, "lbtc-utxo", "", "User's L-BTC UTXO for fee (auto-selected if omitted)")
	cmd.Flags().StringVar(&userAddr, "user-addr", "", "Address to receive LP tokens (auto-derived from wallet if omitted)")
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&lbtcAsset, "lbtc-asset", "", "L-BTC asset ID")
	cmd.Flags().StringVar(&lpAssetID, "lp-asset", "", "LP token asset ID (read from pool.json if omitted)")
	cmd.Flags().Uint64Var(&fee, "fee", 500, "Fee in satoshis (auto-estimated if not set)")
	cmd.Flags().StringVar(&walletName, "wallet", "anchor", "Wallet name for UTXO auto-selection")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().BoolVar(&broadcast, "broadcast", false, "Broadcast transaction via RPC")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl files (used in wizard mode)")
	// deposit0 and deposit1 are optional — wizard mode if omitted.
	return cmd
}

// ── anchor remove-liquidity ───────────────────────────────────────────────────

func cmdRemoveLiquidity() *cobra.Command {
	var (
		poolFile, rpcURL, rpcUser, rpcPass                string
		lpAmount, fee                                     uint64
		lpUTXO, lbtcUTXO, userAddr0, userAddr1, changeAddr string
		asset0, asset1, lbtcAsset, lpAssetID, netName     string
		walletName                                        string
		broadcast                                         bool
	)
	cmd := &cobra.Command{
		Use:   "remove-liquidity",
		Short: "Burn LP tokens and withdraw proportional reserves",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)
			cfg, err := pool.Load(poolFile)
			if err != nil {
				return err
			}
			if asset0 == "" {
				asset0 = cfg.Asset0
			}
			if asset1 == "" {
				asset1 = cfg.Asset1
			}
			if lpAssetID == "" {
				lpAssetID = cfg.LPAssetID
			}
			client := rpc.New(rpcURL, rpcUser, rpcPass)
			walletClient, err := client.LoadOrCreateWallet(walletName)
			if err != nil {
				return fmt.Errorf("wallet: %w", err)
			}
			state, err := pool.Query(cfg, client)
			if err != nil {
				return fmt.Errorf("query pool: %w", err)
			}
			net, err := parseNetwork(netName)
			if err != nil {
				return err
			}
			if lbtcAsset == "" {
				lbtcAsset = net.AssetID
			}

			// Estimate fee if not explicitly set.
			if !cmd.Flags().Changed("fee") {
				if estimated, err := client.EstimateSmartFee(2); err == nil && estimated > 0 {
					fee = estimated * 1400 // ~1400 vbytes for remove-liquidity tx
					fmt.Fprintf(os.Stderr, "Estimated fee: %d sats (%d sat/vB × ~1400 vB)\n", fee, estimated)
				}
			}

			// Auto-select LP UTXO from wallet if not provided.
			if lpUTXO == "" {
				if lpAssetID == "" {
					return fmt.Errorf("lp asset ID unknown — specify --lp-asset or ensure pool.json has lp_asset_id")
				}
				lpUTXOs, err := walletClient.ListUnspentByAsset(lpAssetID)
				if err != nil {
					return fmt.Errorf("scan LP UTXOs: %w", err)
				}
				if len(lpUTXOs) == 0 {
					return fmt.Errorf("no LP token UTXOs found in wallet for asset %s", lpAssetID)
				}
				if len(lpUTXOs) > 1 {
					fmt.Fprintf(os.Stderr, "Found %d LP UTXOs — using the largest. Consolidate to process all at once.\n", len(lpUTXOs))
				}
				// Pick the largest LP UTXO.
				best := lpUTXOs[0]
				for _, u := range lpUTXOs[1:] {
					if u.Amount > best.Amount {
						best = u
					}
				}
				lpUTXO = fmt.Sprintf("%s:%d", best.TxID, best.Vout)
				lpAmount = satoshis(best.Amount)
				fmt.Fprintf(os.Stderr, "Auto-selected LP UTXO: %s (%d tokens)\n", lpUTXO, lpAmount)
			}

			// If lp-amount not explicitly set, use the full UTXO balance.
			if !cmd.Flags().Changed("lp-amount") && lpAmount == 0 {
				lpTxidTmp, lpVoutTmp, err := parseOutpoint(lpUTXO)
				if err != nil {
					return fmt.Errorf("parse lp-utxo: %w", err)
				}
				info, err := client.GetTxOut(lpTxidTmp, lpVoutTmp)
				if err != nil || info == nil {
					return fmt.Errorf("could not fetch LP UTXO %s", lpUTXO)
				}
				lpAmount = satoshis(info.Amount)
				fmt.Fprintf(os.Stderr, "LP amount: %d tokens (full UTXO balance)\n", lpAmount)
			}

			// Auto-derive payout addresses from wallet (unconfidential — tx uses explicit outputs).
			newAddr := func(label string) (string, error) {
				a, err := walletClient.GetNewAddress()
				if err != nil {
					return "", fmt.Errorf("getnewaddress (%s): %w", label, err)
				}
				u, err := walletClient.GetUnconfidentialAddress(a)
				if err != nil {
					u = a
				}
				fmt.Fprintf(os.Stderr, "Payout address (%s): %s\n", label, u)
				return u, nil
			}
			if userAddr0 == "" {
				if userAddr0, err = newAddr("asset0"); err != nil {
					return err
				}
			}
			if userAddr1 == "" {
				if userAddr1, err = newAddr("asset1"); err != nil {
					return err
				}
			}
			// Track full UTXO balance before any capping — needed for LP asset value balance.
			lpUTXOAmount := lpAmount

			// Cap lpAmount to avoid dust pool outputs.
			// Elements rejects explicit taproot outputs below ~330 sats.
			// Each remaining pool UTXO (pool_a, pool_b) must stay >= dustMin.
			// maxBurnForX = floor((reserveX - dustMin) * totalSupply / reserveX)
			const dustMin = uint64(330)
			totalSupply := state.TotalSupply()
			if state.Reserve0 > dustMin && state.Reserve1 > dustMin && totalSupply > dustMin {
				maxBurnForA := (state.Reserve0 - dustMin) * totalSupply / state.Reserve0
				maxBurnForB := (state.Reserve1 - dustMin) * totalSupply / state.Reserve1
				maxBurn := maxBurnForA
				if maxBurnForB < maxBurn {
					maxBurn = maxBurnForB
				}
				if lpAmount > maxBurn {
					fmt.Fprintf(os.Stderr, "Capping LP burn at %d (pool UTXOs must stay ≥ %d sats); %d LP token(s) remain as minimum liquidity\n", maxBurn, dustMin, totalSupply-maxBurn)
					lpAmount = maxBurn
				}
			} else {
				return fmt.Errorf("pool reserves too small to withdraw safely (each pool UTXO must be > %d sats)", dustMin)
			}

			// Compute and display quote.
			p0, p1 := pool.RemovePayouts(lpAmount, state.Reserve0, state.Reserve1, totalSupply)
			fmt.Printf("\nRemove liquidity quote:\n")
			fmt.Printf("  LP tokens to burn: %d\n", lpAmount)
			fmt.Printf("  Asset0 payout:     %d sats\n", p0)
			fmt.Printf("  Asset1 payout:     %d sats\n", p1)
			fmt.Printf("  Fee:               %d sats\n", fee)

			if broadcast {
				fmt.Fprintf(os.Stderr, "\nProceed? [y/n]: ")
				var answer string
				fmt.Scanln(&answer)
				if strings.ToLower(strings.TrimSpace(answer)) != "y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}

			lpTxid, lpVout, _ := parseOutpoint(lpUTXO)

			// Auto-select L-BTC UTXO for fee if not provided.
			var lbtcAmount uint64
			if lbtcUTXO == "" {
				utxos, err := walletClient.ListUnspentByAsset(lbtcAsset)
				if err != nil {
					return fmt.Errorf("scan L-BTC UTXOs: %w", err)
				}
				// Pick the smallest L-BTC UTXO that covers the fee, excluding pool UTXOs.
				var best *struct {
					txid string
					vout uint32
					amt  uint64
				}
				for _, u := range utxos {
					amt := satoshis(u.Amount)
					if amt < fee {
						continue
					}
					outpoint := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
					if outpoint == lpUTXO {
						continue
					}
					if best == nil || amt < best.amt {
						best = &struct {
							txid string
							vout uint32
							amt  uint64
						}{u.TxID, u.Vout, amt}
					}
				}
				if best == nil {
					return fmt.Errorf("no L-BTC UTXO found with at least %d sats for fee", fee)
				}
				lbtcUTXO = fmt.Sprintf("%s:%d", best.txid, best.vout)
				lbtcAmount = best.amt
				fmt.Fprintf(os.Stderr, "Auto-selected L-BTC UTXO: %s (%d sats)\n", lbtcUTXO, lbtcAmount)
			} else {
				lbtcTxid, lbtcVout, err := parseOutpoint(lbtcUTXO)
				if err != nil {
					return fmt.Errorf("parse lbtc-utxo: %w", err)
				}
				info, err := client.GetTxOut(lbtcTxid, lbtcVout)
				if err != nil || info == nil {
					return fmt.Errorf("could not fetch L-BTC UTXO %s", lbtcUTXO)
				}
				lbtcAmount = satoshis(info.Amount)
			}
			lbtcTxid, lbtcVout, _ := parseOutpoint(lbtcUTXO)

			// Derive change address if not provided.
			if changeAddr == "" {
				a, err := walletClient.GetNewAddress()
				if err != nil {
					return fmt.Errorf("getnewaddress (change): %w", err)
				}
				u, err := walletClient.GetUnconfidentialAddress(a)
				if err != nil {
					u = a
				}
				changeAddr = u
			}

			params := &tx.RemoveLiquidityParams{
				State:                 state,
				LPBurned:              lpAmount,
				UserLPAmount:          lpUTXOAmount,
				PoolAAddr:             cfg.PoolA.Address,
				PoolBAddr:             cfg.PoolB.Address,
				LpReserveAddr:         cfg.LpReserve.Address,
				UserLPTxID:            lpTxid,
				UserLPVout:            lpVout,
				UserLBTCTxID:          lbtcTxid,
				UserLBTCVout:          lbtcVout,
				UserLBTCAmount:        lbtcAmount,
				UserAsset0Addr:        userAddr0,
				UserAsset1Addr:        userAddr1,
				ChangeAddr:            changeAddr,
				Asset0:                asset0,
				Asset1:                asset1,
				LPAssetID:             lpAssetID,
				LBTCAsset:             lbtcAsset,
				Fee:                   fee,
				PoolABinaryHex:        cfg.PoolARemove.BinaryHex,
				PoolBBinaryHex:        cfg.PoolBRemove.BinaryHex,
				LpReserveBinaryHex:    cfg.LpReserveRemove.BinaryHex,
				PoolACMRHex:           cfg.PoolARemove.CMR,
				PoolBCMRHex:           cfg.PoolBRemove.CMR,
				LpReserveCMRHex:       cfg.LpReserveRemove.CMR,
				PoolAControlBlock:     cfg.PoolARemove.ControlBlock,
				PoolBControlBlock:     cfg.PoolBRemove.ControlBlock,
				LpReserveControlBlock: cfg.LpReserveRemove.ControlBlock,
			}

			result, err := tx.BuildRemoveLiquidity(params)
			if err != nil {
				return err
			}
			fmt.Printf("\nPayout0:     %d sat\n", result.Payout0)
			fmt.Printf("Payout1:     %d sat\n", result.Payout1)
			fmt.Printf("Tx (hex): %s\n", result.TxHex)

			if broadcast {
				// Sign user inputs (3=LP, 4=L-BTC) with wallet first.
				signed, complete, err := walletClient.SignRawTransactionWithWallet(result.TxHex)
				if err != nil {
					return translateError(fmt.Errorf("sign: %w", err))
				}
				if !complete {
					fmt.Fprintln(os.Stderr, "Warning: signing incomplete — some inputs may not be wallet-owned")
				}
				// Attach Simplicity witnesses to pool inputs AFTER signing.
				finalHex := signed
				for idx, wit := range []struct {
					i int
					w [][]byte
				}{
					{0, result.PoolAWitness},
					{1, result.PoolBWitness},
					{2, result.LpReserveWitness},
				} {
					finalHex, err = attachWitness(finalHex, wit.i, wit.w)
					if err != nil {
						return fmt.Errorf("attach witness[%d]: %w", idx, err)
					}
				}
				txid, err := walletClient.SendRawTransaction(finalHex)
				if err != nil {
					return translateError(fmt.Errorf("broadcast: %w", err))
				}
				fmt.Printf("Txid: %s\n", txid)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().Uint64Var(&lpAmount, "lp-amount", 0, "LP token amount to burn (auto-set to full UTXO balance if omitted)")
	cmd.Flags().StringVar(&lpUTXO, "lp-utxo", "", "User's LP token UTXO as txid:vout (auto-selected from wallet if omitted)")
	cmd.Flags().StringVar(&lbtcUTXO, "lbtc-utxo", "", "User's L-BTC UTXO for fee (auto-selected from wallet if omitted)")
	cmd.Flags().StringVar(&userAddr0, "user-addr0", "", "Address to receive Asset0 payout (auto-derived from wallet if omitted)")
	cmd.Flags().StringVar(&userAddr1, "user-addr1", "", "Address to receive Asset1 payout (auto-derived from wallet if omitted)")
	cmd.Flags().StringVar(&changeAddr, "change-addr", "", "Address to receive LP and L-BTC change (auto-derived from wallet if omitted)")
	cmd.Flags().StringVar(&asset0, "asset0", "", "Asset0 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&asset1, "asset1", "", "Asset1 ID (read from pool.json if omitted)")
	cmd.Flags().StringVar(&lbtcAsset, "lbtc-asset", "", "L-BTC asset ID")
	cmd.Flags().StringVar(&lpAssetID, "lp-asset", "", "LP token asset ID (read from pool.json if omitted)")
	cmd.Flags().Uint64Var(&fee, "fee", 500, "Fee in satoshis (auto-estimated if not set)")
	cmd.Flags().StringVar(&walletName, "wallet", "anchor", "Wallet name for LP UTXO selection and signing")
	cmd.Flags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.Flags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.Flags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.Flags().BoolVar(&broadcast, "broadcast", false, "Broadcast transaction via RPC")
	cmd.Flags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	return cmd
}

// ── anchor check ──────────────────────────────────────────────────────────────

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

// ── anchor find-pools ─────────────────────────────────────────────────────────

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

// execAddLiquidity builds and (optionally) broadcasts an add-liquidity transaction.
// All deposit amounts, UTXOs, and addresses must already be resolved by the caller.
func execAddLiquidity(
	cfg *pool.Config,
	state *pool.State,
	deposit0, deposit1 uint64,
	asset0Inputs, asset1Inputs, lbtcInputs []tx.UserInput,
	asset0, asset1, lbtcAsset, lpAssetID string,
	changeAddr, userAddr string,
	fee uint64,
	walletClient *rpc.Client,
	broadcast bool,
) error {
	params := &tx.AddLiquidityParams{
		State:                 state,
		Deposit0:              deposit0,
		Deposit1:              deposit1,
		PoolAAddr:             cfg.PoolA.Address,
		PoolBAddr:             cfg.PoolB.Address,
		LpReserveAddr:         cfg.LpReserve.Address,
		Asset0Inputs:          asset0Inputs,
		Asset1Inputs:          asset1Inputs,
		LBTCInputs:            lbtcInputs,
		ChangeAddr:            changeAddr,
		UserLPAddr:            userAddr,
		LPAssetID:             lpAssetID,
		Asset0:                asset0,
		Asset1:                asset1,
		LBTCAsset:             lbtcAsset,
		Fee:                   fee,
		PoolABinaryHex:        cfg.PoolASwap.BinaryHex,
		PoolBBinaryHex:        cfg.PoolBSwap.BinaryHex,
		LpReserveBinaryHex:    cfg.LpReserveAdd.BinaryHex,
		PoolACMRHex:           cfg.PoolASwap.CMR,
		PoolBCMRHex:           cfg.PoolBSwap.CMR,
		LpReserveCMRHex:       cfg.LpReserveAdd.CMR,
		PoolAControlBlock:     cfg.PoolASwap.ControlBlock,
		PoolBControlBlock:     cfg.PoolBSwap.ControlBlock,
		LpReserveControlBlock: cfg.LpReserveAdd.ControlBlock,
	}

	result, err := tx.BuildAddLiquidity(params)
	if err != nil {
		return err
	}
	fmt.Printf("LP Minted: %d sat\n", result.LPMinted)

	if !broadcast {
		fmt.Printf("Tx (hex): %s\n", result.TxHex)
		fmt.Fprintln(os.Stderr, "(use --broadcast to sign and send)")
		return nil
	}

	signed, complete, err := walletClient.SignRawTransactionWithWallet(result.TxHex)
	if err != nil {
		return translateError(fmt.Errorf("sign: %w", err))
	}
	if !complete {
		fmt.Fprintln(os.Stderr, "Warning: signing incomplete")
	}
	finalHex := signed
	for idx, wit := range []struct {
		i int
		w [][]byte
	}{
		{0, result.PoolAWitness},
		{1, result.PoolBWitness},
		{2, result.LpReserveWitness},
	} {
		finalHex, err = attachWitness(finalHex, wit.i, wit.w)
		if err != nil {
			return fmt.Errorf("attach witness[%d]: %w", idx, err)
		}
	}
	txid, err := walletClient.SendRawTransaction(finalHex)
	if err != nil {
		return translateError(fmt.Errorf("broadcast: %w", err))
	}
	fmt.Printf("Txid: %s\n", txid)
	return nil
}

// runAddLiquidityWizard is the interactive add-liquidity flow (phase 1.2).
// Prompts for deposit0, auto-computes proportional deposit1, shows a quote,
// selects UTXOs, and calls execAddLiquidity. Called from cmdAddLiquidity and
// from the create-pool duplicate-detection redirect.
func runAddLiquidityWizard(
	cfg *pool.Config,
	walletClient *rpc.Client,
	nodeClient *rpc.Client,
	lbtcAsset string,
	balances map[string]uint64,
	broadcast bool,
) error {
	state, err := pool.Query(cfg, nodeClient)
	if err != nil {
		return fmt.Errorf("query pool: %w", err)
	}

	fmt.Fprintf(os.Stderr, "\nExisting pool reserves:\n")
	fmt.Fprintf(os.Stderr, "  reserve0: %d sats\n", state.Reserve0)
	fmt.Fprintf(os.Stderr, "  reserve1: %d sats\n", state.Reserve1)
	if state.Reserve0 > 0 && state.Reserve1 > 0 {
		ratio := float64(state.Reserve1) / float64(state.Reserve0)
		fmt.Fprintf(os.Stderr, "  price:    1 asset0 = %.6f asset1\n", ratio)
	}

	bal0 := balances[cfg.Asset0]
	fmt.Fprintf(os.Stderr, "Asset0 balance: %d sats\n", bal0)
	var deposit0 uint64
	for {
		deposit0 = promptUint64(fmt.Sprintf("Enter deposit0 amount (1-%d sats): ", bal0), 0)
		if deposit0 > 0 && deposit0 <= bal0 {
			break
		}
		fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", bal0)
	}

	var deposit1 uint64
	if state.Reserve0 > 0 && state.Reserve1 > 0 {
		bal1 := balances[cfg.Asset1]
		for {
			deposit1 = deposit0 * state.Reserve1 / state.Reserve0
			if deposit1 == 0 {
				return fmt.Errorf("deposit0 (%d sats) too small relative to reserves; increase deposit0", deposit0)
			}
			if deposit1 <= bal1 {
				break
			}
			fmt.Fprintf(os.Stderr, "Insufficient asset1: need %d sats, have %d sats.\n", deposit1, bal1)
			for {
				deposit0 = promptUint64(fmt.Sprintf("Lower deposit0 amount (1-%d sats): ", bal0), 0)
				if deposit0 > 0 && deposit0 <= bal0 {
					break
				}
				fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", bal0)
			}
		}
		fmt.Fprintf(os.Stderr, "Proportional deposit1: %d sats\n", deposit1)
	} else {
		bal1 := balances[cfg.Asset1]
		fmt.Fprintf(os.Stderr, "Asset1 balance: %d sats\n", bal1)
		for {
			deposit1 = promptUint64(fmt.Sprintf("Enter deposit1 amount (1-%d sats): ", bal1), 0)
			if deposit1 > 0 && deposit1 <= bal1 {
				break
			}
			fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", bal1)
		}
	}

	totalSupply := state.TotalSupply()
	lpMinted := pool.LPMintedForDeposit(deposit0, deposit1, state.Reserve0, state.Reserve1, totalSupply)
	fmt.Fprintf(os.Stderr, "LP tokens to receive: %d\n", lpMinted)

	const addLiqVbytes uint64 = 1400
	feeRate := uint64(1)
	if est, err := nodeClient.EstimateSmartFee(2); err == nil && est > 0 {
		feeRate = est
	}
	feeRate = promptUint64(fmt.Sprintf("Network fee rate [default: %d sat/vbyte]: ", feeRate), feeRate)
	fee := feeRate * addLiqVbytes
	fmt.Fprintf(os.Stderr, "Total network fee: %d sats\n", fee)

	fmt.Fprintln(os.Stderr, "\n-----------------------------------------")
	fmt.Fprintf(os.Stderr, "  Deposit0:    %d sats\n", deposit0)
	fmt.Fprintf(os.Stderr, "  Deposit1:    %d sats\n", deposit1)
	fmt.Fprintf(os.Stderr, "  LP minted:   %d\n", lpMinted)
	fmt.Fprintf(os.Stderr, "  Fee:         %d sats\n", fee)
	fmt.Fprintln(os.Stderr, "-----------------------------------------")
	fmt.Fprintf(os.Stderr, "\nThis will send funds from your wallet and broadcast immediately.\n")
	if strings.ToLower(promptString("Confirm and add liquidity? [y/n]: ")) != "y" {
		fmt.Fprintln(os.Stderr, "Aborted.")
		return nil
	}
	broadcast = true

	// selectInputs picks wallet UTXOs for the required amount, combining multiple
	// if no single UTXO is large enough. Returns outpoints sorted largest-first.
	excludeSet := make(map[string]bool)
	selectInputs := func(label, assetID string, needed uint64) ([]tx.UserInput, error) {
		utxos, err := walletClient.ListUnspentByAsset(assetID)
		if err != nil {
			return nil, fmt.Errorf("list unspent %s: %w", label, err)
		}
		// Filter excluded outpoints and sort by amount descending.
		var avail []rpc.WalletUTXO
		for _, u := range utxos {
			op := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
			if !excludeSet[op] {
				avail = append(avail, u)
			}
		}
		// Try single UTXO first (smallest that covers needed).
		var bestSingle *rpc.WalletUTXO
		for i := range avail {
			u := &avail[i]
			if satoshis(u.Amount) >= needed {
				if bestSingle == nil || u.Amount < bestSingle.Amount {
					bestSingle = u
				}
			}
		}
		if bestSingle != nil {
			op := fmt.Sprintf("%s:%d", bestSingle.TxID, bestSingle.Vout)
			excludeSet[op] = true
			fmt.Fprintf(os.Stderr, "Auto-selected %s UTXO: %s (%d sats)\n", label, op, satoshis(bestSingle.Amount))
			return []tx.UserInput{{TxID: bestSingle.TxID, Vout: bestSingle.Vout, Amount: satoshis(bestSingle.Amount)}}, nil
		}
		// No single UTXO covers it — combine largest UTXOs until we have enough.
		// Sort descending by amount.
		for i := 0; i < len(avail); i++ {
			for j := i + 1; j < len(avail); j++ {
				if avail[j].Amount > avail[i].Amount {
					avail[i], avail[j] = avail[j], avail[i]
				}
			}
		}
		var selected []tx.UserInput
		var total uint64
		for _, u := range avail {
			op := fmt.Sprintf("%s:%d", u.TxID, u.Vout)
			selected = append(selected, tx.UserInput{TxID: u.TxID, Vout: u.Vout, Amount: satoshis(u.Amount)})
			total += satoshis(u.Amount)
			fmt.Fprintf(os.Stderr, "Auto-selected %s UTXO: %s (%d sats)\n", label, op, satoshis(u.Amount))
			if total >= needed {
				break
			}
		}
		if total < needed {
			return nil, fmt.Errorf("insufficient %s UTXOs: need %d sats, have %d across %d UTXOs", label, needed, total, len(avail))
		}
		for _, inp := range selected {
			excludeSet[fmt.Sprintf("%s:%d", inp.TxID, inp.Vout)] = true
		}
		return selected, nil
	}

	a0inputs, err := selectInputs("asset0", cfg.Asset0, deposit0)
	if err != nil {
		return err
	}
	a1inputs, err := selectInputs("asset1", cfg.Asset1, deposit1)
	if err != nil {
		return err
	}
	lbInputs, err := selectInputs("lbtc", lbtcAsset, fee)
	if err != nil {
		return err
	}

	newAddr := func(label string) (string, error) {
		a, err := walletClient.GetNewAddress()
		if err != nil {
			return "", fmt.Errorf("getnewaddress (%s): %w", label, err)
		}
		u, err := walletClient.GetUnconfidentialAddress(a)
		if err != nil {
			u = a
		}
		return u, nil
	}
	changeAddr, err := newAddr("change")
	if err != nil {
		return err
	}
	userAddr, err := newAddr("LP recipient")
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "LP tokens will be sent to: %s\n", userAddr)

	return execAddLiquidity(cfg, state, deposit0, deposit1,
		a0inputs, a1inputs, lbInputs,
		cfg.Asset0, cfg.Asset1, lbtcAsset, cfg.LPAssetID,
		changeAddr, userAddr, fee, walletClient, broadcast)
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

// ── helpers ───────────────────────────────────────────────────────────────────

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

func resolveNetwork(name string) string {
	if name == "" {
		if v := os.Getenv("ANCHOR_NETWORK"); v != "" {
			return v
		}
		return "testnet"
	}
	return name
}

func satoshis(btc float64) uint64 {
	return uint64(math.Round(btc * 1e8))
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

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
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

// ── wizard helpers ────────────────────────────────────────────────────────────

// stdinReader is a shared buffered reader for interactive prompts.
var stdinReader = bufio.NewReader(os.Stdin)

// promptString prints prompt to stderr and returns a trimmed line from stdin.
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

func promptString(prompt string) string {
	fmt.Fprint(os.Stderr, prompt)
	line, _ := stdinReader.ReadString('\n')
	return strings.TrimSpace(line)
}

// promptUint64 prints prompt to stderr, parses a uint64, and returns defaultVal
// if the user enters a blank line.
func promptUint64(prompt string, defaultVal uint64) uint64 {
	for {
		fmt.Fprint(os.Stderr, prompt)
		line, _ := stdinReader.ReadString('\n')
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultVal
		}
		v, err := strconv.ParseUint(line, 10, 64)
		if err == nil {
			return v
		}
		fmt.Fprintln(os.Stderr, "Enter a whole number.")
	}
}

// promptChoice prints a numbered list to stderr and returns the 0-based index
// of the user's selection.
func promptChoice(prompt string, options []string) int {
	for i, opt := range options {
		fmt.Fprintf(os.Stderr, "  [%d]  %s\n", i+1, opt)
	}
	for {
		fmt.Fprintf(os.Stderr, "%s (1-%d): ", prompt, len(options))
		line, _ := stdinReader.ReadString('\n')
		n, err := strconv.Atoi(strings.TrimSpace(line))
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		fmt.Fprintf(os.Stderr, "Enter a number between 1 and %d.\n", len(options))
	}
}

// revBytes returns a reversed copy of b.
func revBytes(b []byte) []byte {
	c := make([]byte, len(b))
	for i, v := range b {
		c[len(b)-1-i] = v
	}
	return c
}

// resolveLPAsset walks back from poolATxID through the pool_a input chain until
// it finds the creation transaction (identified by an ANCHR OP_RETURN output).
// It then computes and returns the LP asset ID (display-format hex).
//
// Each pool operation transaction has pool_a as vin[0], so following vin[0]
// backward leads to the creation tx within O(operations) hops.
//
// Uses GetRawTransaction (non-verbose) + local go-elements parsing, so it
// works whether or not the node has txindex enabled.
func resolveLPAsset(nodeClient *rpc.Client, poolATxID string) (string, error) {
	const anchrMagic = "ANCHR" // 5-byte magic after OP_RETURN + push
	currentTxID := poolATxID
	for step := 0; step < 200; step++ {
		rawHex, err := nodeClient.GetRawTransaction(currentTxID)
		if err != nil {
			return "", fmt.Errorf("getrawtransaction %s (step %d): %w", currentTxID[:16], step, err)
		}
		parsedTx, err := transaction.NewTxFromHex(rawHex)
		if err != nil {
			return "", fmt.Errorf("parse tx %s: %w", currentTxID[:16], err)
		}

		// Primary: vin[0] has an LP issuance — this is the creation tx.
		// BuildCreatePool attaches the LP issuance to vin[0] (pool_creation UTXO).
		// Works for all pools regardless of whether ANCHR OP_RETURN was included.
		if len(parsedTx.Inputs) > 0 && parsedTx.Inputs[0].Issuance != nil {
			vin0 := parsedTx.Inputs[0]
			vin0TxID := hex.EncodeToString(revBytes(vin0.Hash))
			lpID, err := tx.ComputeLPAssetID(vin0TxID, vin0.Index)
			if err != nil {
				return "", fmt.Errorf("compute LP asset: %w", err)
			}
			return hex.EncodeToString(revBytes(lpID[:])), nil
		}

		// Secondary: ANCHR OP_RETURN output: 0x6a 0x49 "ANCHR" ...
		for _, out := range parsedTx.Outputs {
			s := out.Script
			if len(s) >= 7 && s[0] == 0x6a && s[1] == 0x49 && string(s[2:7]) == anchrMagic {
				if len(parsedTx.Inputs) == 0 {
					return "", fmt.Errorf("creation tx has no inputs")
				}
				vin0 := parsedTx.Inputs[0]
				vin0TxID := hex.EncodeToString(revBytes(vin0.Hash))
				lpID, err := tx.ComputeLPAssetID(vin0TxID, vin0.Index)
				if err != nil {
					return "", fmt.Errorf("compute LP asset: %w", err)
				}
				return hex.EncodeToString(revBytes(lpID[:])), nil
			}
		}

		// Not found yet — pool_a is always vin[0] in pool operation txs.
		if len(parsedTx.Inputs) == 0 {
			return "", fmt.Errorf("tx %s has no inputs after %d steps", currentTxID[:16], step)
		}
		currentTxID = hex.EncodeToString(revBytes(parsedTx.Inputs[0].Hash))
	}
	return "", fmt.Errorf("creation tx (LP issuance on vin[0]) not found after 200 hops")
}

// walletExplicitAssets returns a map of assetID → total sats of explicit (unblinded)
// UTXOs in the wallet. Confidential UTXOs are excluded because they cannot be
// used as inputs in manually-built transactions.
func walletExplicitAssets(walletClient *rpc.Client) (map[string]uint64, error) {
	utxos, err := walletClient.ListUnspentAll()
	if err != nil {
		return nil, err
	}
	totals := make(map[string]uint64)
	for _, u := range utxos {
		if u.Asset == "" || u.Amount == 0 || !u.IsExplicit() {
			continue
		}
		totals[u.Asset] += satoshis(u.Amount)
	}
	return totals, nil
}
