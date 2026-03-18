package main

import (
	"fmt"
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
)

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
