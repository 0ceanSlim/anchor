package main

import (
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
)

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
