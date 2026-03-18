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
