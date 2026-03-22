package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
)

func cmdRemoveLiquidity() *cobra.Command {
	var (
		poolFile, rpcURL, rpcUser, rpcPass                string
		lpAmount, fee                                     uint64
		lpUTXO, lbtcUTXO, userAddr0, userAddr1, changeAddr string
		asset0, asset1, lbtcAsset, lpAssetID, netName     string
		walletName                                        string
		broadcast                                         bool
		poolID, esploraURL, buildDir                      string
		jsonOut                                           bool
	)
	cmd := &cobra.Command{
		Use:   "remove-liquidity",
		Short: "Burn LP tokens and withdraw proportional reserves",
		RunE: func(cmd *cobra.Command, args []string) error {
			rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
			netName = resolveNetwork(netName)

			if poolID != "" {
				resolved, err := resolvePoolID(poolID, esploraURL, buildDir, netName)
				if err != nil {
					return err
				}
				poolFile = resolved
			}

			resolved, err := resolvePoolFile(cmd, poolFile)
			if err != nil {
				return err
			}
			if resolved == "" {
				return fmt.Errorf("no pool config found — use 'anchor find-pools --save' to discover one, or specify --pool")
			}
			cfg, err := pool.Load(resolved)
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
				rate := estimateFeeRate(client)
				fee = computeFee(1400, rate) // ~1400 vbytes for remove-liquidity tx
				fmt.Fprintf(os.Stderr, "Estimated fee: %d sats (%.1f sat/vB)\n", fee, rate)
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

			// Track full UTXO balance before any reduction — needed for LP change output.
			lpUTXOAmount := lpAmount

			// In wizard mode (no --lp-amount flag), prompt for how much to remove.
			if !cmd.Flags().Changed("lp-amount") && isTerminal() {
				totalSupplyPreview := state.TotalSupply()
				p0All, p1All := pool.RemovePayouts(lpAmount, state.Reserve0, state.Reserve1, totalSupplyPreview)
				fmt.Fprintf(os.Stderr, "\nYou have %d LP tokens.\n", lpAmount)
				fmt.Fprintf(os.Stderr, "  100%% → %d asset0 + %d asset1\n", p0All, p1All)
				fmt.Fprintf(os.Stderr, "\nEnter amount of LP tokens to redeem, or a percentage (e.g. 50%%).\n")
				for {
					input := promptString(fmt.Sprintf("LP to remove [default: %d = 100%%]: ", lpAmount))
					if input == "" {
						break // keep full amount
					}
					if strings.HasSuffix(input, "%") {
						pctStr := strings.TrimSuffix(input, "%")
						pctStr = strings.TrimSpace(pctStr)
						pct, err := strconv.ParseFloat(pctStr, 64)
						if err != nil || pct <= 0 || pct > 100 {
							fmt.Fprintf(os.Stderr, "Enter a percentage between 1%% and 100%%.\n")
							continue
						}
						lpAmount = uint64(float64(lpAmount) * pct / 100)
						if lpAmount == 0 {
							fmt.Fprintf(os.Stderr, "Amount too small — rounds to 0 tokens.\n")
							continue
						}
						break
					}
					n, err := strconv.ParseUint(input, 10, 64)
					if err != nil || n == 0 || n > lpAmount {
						fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", lpAmount)
						continue
					}
					lpAmount = n
					break
				}
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

			// In interactive mode without --broadcast, prompt to confirm and broadcast.
			if !broadcast && isTerminal() {
				answer := promptString("\nConfirm and broadcast? [y/n]: ")
				if strings.ToLower(answer) == "y" {
					broadcast = true
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

			if !broadcast {
				if jsonOut {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]any{
						"tx_hex":    result.TxHex,
						"lp_burned": lpAmount,
						"payout0":   result.Payout0,
						"payout1":   result.Payout1,
						"fee":       fee,
					})
				}
				fmt.Printf("\nPayout0:     %d sat\n", result.Payout0)
				fmt.Printf("Payout1:     %d sat\n", result.Payout1)
				fmt.Printf("Tx (hex): %s\n", result.TxHex)
				fmt.Fprintln(os.Stderr, "(use --broadcast to sign and send)")
				return nil
			}

			if !jsonOut {
				fmt.Printf("\nPayout0:     %d sat\n", result.Payout0)
				fmt.Printf("Payout1:     %d sat\n", result.Payout1)
			}

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
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{
					"txid":      txid,
					"lp_burned": lpAmount,
					"payout0":   result.Payout0,
					"payout1":   result.Payout1,
					"fee":       fee,
				})
			}
			fmt.Printf("Txid: %s\n", txid)
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output in JSON format")
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
	cmd.Flags().StringVar(&poolID, "pool-id", "", "Resolve pool by LP asset / pool ID (via Esplora)")
	cmd.Flags().StringVar(&esploraURL, "esplora-url", "", "Esplora API URL (env: ANCHOR_ESPLORA_URL)")
	cmd.Flags().StringVar(&buildDir, "build-dir", "./build", "Directory containing .shl and .args files")
	return cmd
}
