package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/spf13/cobra"
)

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
			resolved, err := resolvePoolFile(cmd, poolFile)
			if err != nil {
				return err
			}
			if resolved == "" {
				if isTerminal() {
					answer := promptString("No pool config found. Run pool discovery? [y/n]: ")
					if strings.ToLower(answer) == "y" {
						fmt.Fprintf(os.Stderr, "Run: anchor find-pools --save\n")
						fmt.Fprintf(os.Stderr, "Then re-run your swap command.\n")
					}
				}
				return fmt.Errorf("no pool config found — use 'anchor find-pools --save' to discover one, or specify --pool")
			}
			cfg, err := pool.Load(resolved)
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

			feeNum, feeDen := cfg.FeeNum, cfg.FeeDen
			if feeNum == 0 || feeDen == 0 {
				feeNum, feeDen = 1, 1
			}

			// Estimate fee if not explicitly set.
			if !cmd.Flags().Changed("fee") {
				if estimated, err := client.EstimateSmartFee(2); err == nil {
					fee = estimated * 1200 // ~1200 vbytes for swap tx
					fmt.Fprintf(os.Stderr, "Estimated fee: %d sats\n", fee)
				}
			}

			// ── Wizard mode: prompt for missing parameters ──────────────
			wizardMode := isTerminal() && !cmd.Flags().Changed("amount")

			walletClient, err := client.LoadOrCreateWallet(walletName)
			if err != nil {
				return fmt.Errorf("wallet: %w", err)
			}

			if wizardMode {
				// Show pool state.
				fmt.Fprintf(os.Stderr, "\nPool reserves:\n")
				fmt.Fprintf(os.Stderr, "  asset0: %d sats  (%s...)\n", state.Reserve0, asset0[:16])
				fmt.Fprintf(os.Stderr, "  asset1: %d sats  (%s...)\n", state.Reserve1, asset1[:16])
				feeRate := float64(feeDen-feeNum) / float64(feeDen) * 100
				fmt.Fprintf(os.Stderr, "  fee:    %.2f%%\n", feeRate)

				// Prompt for swap direction.
				if !cmd.Flags().Changed("in-asset") {
					choice := promptChoice("Which asset do you want to swap FROM?", []string{
						fmt.Sprintf("asset0 (%s...)", asset0[:16]),
						fmt.Sprintf("asset1 (%s...)", asset1[:16]),
					})
					if choice == 0 {
						inAsset = "asset0"
					} else {
						inAsset = "asset1"
					}
				}
			}

			swapAsset0In := strings.EqualFold(inAsset, "asset0") || strings.EqualFold(inAsset, asset0)

			inputAssetID := asset1
			outputAssetLabel := "asset0"
			if swapAsset0In {
				inputAssetID = asset0
				outputAssetLabel = "asset1"
			}

			if wizardMode {
				// Show wallet balance for the input asset.
				utxos, err := walletClient.ListUnspentByAsset(inputAssetID)
				if err != nil {
					return fmt.Errorf("list unspent: %w", err)
				}
				var totalBal uint64
				for _, u := range utxos {
					totalBal += satoshis(u.Amount)
				}
				if totalBal == 0 {
					return fmt.Errorf("no %s in wallet (asset %s)", inAsset, inputAssetID)
				}
				fmt.Fprintf(os.Stderr, "\nWallet balance (%s): %d sats\n", inAsset, totalBal)

				// Prompt for amount.
				maxSwap := totalBal
				if swapAsset0In && inputAssetID == lbtcAsset {
					// If swapping L-BTC, reserve some for the fee.
					if totalBal > fee {
						maxSwap = totalBal - fee
					} else {
						return fmt.Errorf("insufficient L-BTC: need at least %d sats for fee, have %d", fee, totalBal)
					}
				}
				for {
					amountIn = promptUint64(fmt.Sprintf("Amount to swap (1-%d sats): ", maxSwap), 0)
					if amountIn > 0 && amountIn <= maxSwap {
						break
					}
					fmt.Fprintf(os.Stderr, "Enter a value between 1 and %d.\n", maxSwap)
				}

				// Show quote.
				var reserveIn, reserveOut uint64
				if swapAsset0In {
					reserveIn, reserveOut = state.Reserve0, state.Reserve1
				} else {
					reserveIn, reserveOut = state.Reserve1, state.Reserve0
				}
				expectedOut := pool.SwapOutput(amountIn, reserveIn, reserveOut, feeNum, feeDen)
				priceImpact := float64(amountIn) / float64(reserveIn) * 100
				fmt.Fprintf(os.Stderr, "\nSwap quote:\n")
				fmt.Fprintf(os.Stderr, "  Input:        %d sats (%s)\n", amountIn, inAsset)
				fmt.Fprintf(os.Stderr, "  Output:       %d sats (%s)\n", expectedOut, outputAssetLabel)
				fmt.Fprintf(os.Stderr, "  Price impact: %.2f%%\n", priceImpact)
				fmt.Fprintf(os.Stderr, "  Network fee:  %d sats\n", fee)

				// Confirm and broadcast.
				answer := promptString("\nConfirm and broadcast? [y/n]: ")
				if strings.ToLower(answer) != "y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
				broadcast = true
			}

			// Auto-derive addresses from wallet if not provided.
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
			if userAddr == "" {
				userAddr, err = newAddr("swap output")
				if err != nil {
					return err
				}
				fmt.Fprintf(os.Stderr, "Output address (%s): %s\n", outputAssetLabel, userAddr)
			}
			var changeAddr string
			changeAddr, err = newAddr("change")
			if err != nil {
				return err
			}

			inputIsLBTC := inputAssetID == lbtcAsset

			// Auto-select user UTXO from wallet if not provided.
			var userInputAmount uint64
			if userUTXO == "" {
				utxos, err := walletClient.ListUnspentByAsset(inputAssetID)
				if err != nil {
					return fmt.Errorf("list unspent: %w", err)
				}
				needed := amountIn
				if inputIsLBTC {
					needed += fee // L-BTC UTXO must cover both swap amount and fee
				}
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
				userInputAmount = satoshis(best.Amount)
				fmt.Fprintf(os.Stderr, "Auto-selected UTXO: %s:%d (%d sats)\n", best.TxID, best.Vout, userInputAmount)
			} else {
				// Explicit --user-utxo: look up its value.
				tmpTxid, tmpVout, err := parseOutpoint(userUTXO)
				if err != nil {
					return err
				}
				info, err := client.GetTxOut(tmpTxid, tmpVout)
				if err != nil || info == nil {
					return fmt.Errorf("could not fetch user UTXO %s", userUTXO)
				}
				userInputAmount = satoshis(info.Amount)
			}

			// If input asset is not L-BTC, select a separate L-BTC UTXO for the fee.
			var feeTxID string
			var feeVout uint32
			var feeUTXOAmount uint64
			if !inputIsLBTC {
				utxos, err := walletClient.ListUnspentByAsset(lbtcAsset)
				if err != nil {
					return fmt.Errorf("list L-BTC unspent: %w", err)
				}
				var best *rpc.WalletUTXO
				for i := range utxos {
					u := &utxos[i]
					if satoshis(u.Amount) >= fee {
						if best == nil || u.Amount < best.Amount {
							best = u
						}
					}
				}
				if best == nil {
					return fmt.Errorf("no L-BTC UTXO with at least %d sats for fee", fee)
				}
				feeTxID = best.TxID
				feeVout = best.Vout
				feeUTXOAmount = satoshis(best.Amount)
				fmt.Fprintf(os.Stderr, "Auto-selected L-BTC fee UTXO: %s:%d (%d sats)\n", feeTxID, feeVout, feeUTXOAmount)
			}

			userTxid, userVout, err := parseOutpoint(userUTXO)
			if err != nil {
				return err
			}

			var reserveIn, reserveOut uint64
			if swapAsset0In {
				reserveIn, reserveOut = state.Reserve0, state.Reserve1
			} else {
				reserveIn, reserveOut = state.Reserve1, state.Reserve0
			}
			expectedOut := pool.SwapOutput(amountIn, reserveIn, reserveOut, feeNum, feeDen)
			if !wizardMode {
				fmt.Printf("Expected output: %d sats\n", expectedOut)
			}

			params := &tx.SwapParams{
				State:           state,
				SwapAsset0In:    swapAsset0In,
				AmountIn:        amountIn,
				MinAmountOut:    minOut,
				UserTxID:        userTxid,
				UserVout:        userVout,
				UserAsset:       inAsset,
				UserInputAmount: userInputAmount,
				UserOutputAddr:  userAddr,
				ChangeAddr:      changeAddr,
				PoolAAddr:       cfg.PoolA.Address,
				PoolBAddr:       cfg.PoolB.Address,
				Asset0:          asset0,
				Asset1:          asset1,
				LBTCAsset:       lbtcAsset,
				Fee:             fee,
				FeeNum:          feeNum,
				FeeDen:          feeDen,
				FeeTxID:         feeTxID,
				FeeVout:         feeVout,
				FeeAmount:       feeUTXOAmount,
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

			if !broadcast {
				fmt.Printf("Tx (hex): %s\n", result.TxHex)
				fmt.Fprintln(os.Stderr, "(use --broadcast to sign and send)")
				return nil
			}

			// Sign wallet input (input[2]) first, then attach Simplicity witnesses.
			signed, complete, err := walletClient.SignRawTransactionWithWallet(result.TxHex)
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
			txid, err := walletClient.SendRawTransaction(finalHex)
			if err != nil {
				return translateError(fmt.Errorf("broadcast: %w", err))
			}
			fmt.Printf("Txid: %s\n", txid)
			return nil
		},
	}
	cmd.Flags().StringVar(&poolFile, "pool", "pool.json", "Pool config file")
	cmd.Flags().StringVar(&inAsset, "in-asset", "asset0", "Input asset: 'asset0' or 'asset1'")
	cmd.Flags().Uint64Var(&amountIn, "amount", 0, "Amount to swap in satoshis")
	cmd.Flags().Uint64Var(&minOut, "min-out", 0, "Minimum acceptable output in satoshis")
	cmd.Flags().StringVar(&userUTXO, "user-utxo", "", "User's input UTXO as txid:vout (auto-selected from wallet if omitted)")
	cmd.Flags().StringVar(&userAddr, "user-addr", "", "User's output address for received asset (auto-derived from wallet if omitted)")
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
	return cmd
}
