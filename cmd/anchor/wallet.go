package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/spf13/cobra"
)

func cmdWallet() *cobra.Command {
	var rpcURL, rpcUser, rpcPass, walletName, netName, lbtcAsset string
	var jsonOut bool

	cmd := &cobra.Command{
		Use:   "wallet",
		Short: "Wallet utilities: balance, UTXOs, address, send",
	}

	// Shared persistent flags for all wallet subcommands.
	cmd.PersistentFlags().StringVar(&rpcURL, "rpc-url", "", "Elements RPC URL (env: ANCHOR_RPC_URL)")
	cmd.PersistentFlags().StringVar(&rpcUser, "rpc-user", "", "RPC username (env: ANCHOR_RPC_USER)")
	cmd.PersistentFlags().StringVar(&rpcPass, "rpc-pass", "", "RPC password (env: ANCHOR_RPC_PASS)")
	cmd.PersistentFlags().StringVar(&walletName, "wallet", "anchor", "Wallet name")
	cmd.PersistentFlags().StringVar(&netName, "network", "", "Network: liquid, testnet, regtest (env: ANCHOR_NETWORK)")
	cmd.PersistentFlags().StringVar(&lbtcAsset, "lbtc-asset", "", "L-BTC asset ID (default: network native)")
	cmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "Output in JSON format")

	// Helper to create wallet client.
	walletClient := func() (*rpc.Client, string, error) {
		rpcURL, rpcUser, rpcPass = resolveRPC(rpcURL, rpcUser, rpcPass)
		netName = resolveNetwork(netName)
		if lbtcAsset == "" {
			net, err := parseNetwork(netName)
			if err != nil {
				return nil, "", err
			}
			lbtcAsset = net.AssetID
		}
		client := rpc.New(rpcURL, rpcUser, rpcPass)
		wc, err := client.LoadOrCreateWallet(walletName)
		if err != nil {
			return nil, "", fmt.Errorf("wallet: %w", err)
		}
		return wc, lbtcAsset, nil
	}

	// ── getbalance ──────────────────────────────────────────────────
	var balAsset string
	balCmd := &cobra.Command{
		Use:   "getbalance",
		Short: "Show wallet asset balances",
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, lbtc, err := walletClient()
			if err != nil {
				return err
			}

			if balAsset != "" {
				utxos, err := wc.ListUnspentByAsset(balAsset)
				if err != nil {
					return fmt.Errorf("list unspent: %w", err)
				}
				var total uint64
				for _, u := range utxos {
					total += satoshis(u.Amount)
				}
				if jsonOut {
					enc := json.NewEncoder(os.Stdout)
					enc.SetIndent("", "  ")
					return enc.Encode(map[string]any{
						"asset":   balAsset,
						"balance": total,
					})
				}
				fmt.Printf("%d sats\n", total)
				return nil
			}

			// All assets.
			balances, err := walletExplicitAssets(wc)
			if err != nil {
				return fmt.Errorf("scan assets: %w", err)
			}
			if jsonOut {
				var out []map[string]any
				for id, bal := range balances {
					out = append(out, map[string]any{
						"asset":   id,
						"balance": bal,
					})
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			// Sort: L-BTC first, then by balance descending.
			type entry struct {
				id  string
				bal uint64
			}
			var sorted []entry
			for id, bal := range balances {
				sorted = append(sorted, entry{id, bal})
			}
			sort.Slice(sorted, func(i, j int) bool {
				li := strings.EqualFold(sorted[i].id, lbtc)
				lj := strings.EqualFold(sorted[j].id, lbtc)
				if li != lj {
					return li
				}
				return sorted[i].bal > sorted[j].bal
			})
			fmt.Printf("%-66s  %s\n", "ASSET", "BALANCE (sats)")
			fmt.Println(strings.Repeat("-", 84))
			for _, e := range sorted {
				label := e.id
				if strings.EqualFold(e.id, lbtc) {
					label += "  (L-BTC)"
				}
				fmt.Printf("%-66s  %d\n", label, e.bal)
			}
			return nil
		},
	}
	balCmd.Flags().StringVar(&balAsset, "asset", "", "Filter to a specific asset ID")

	// ── listunspent ─────────────────────────────────────────────────
	var luAsset string
	luCmd := &cobra.Command{
		Use:   "listunspent",
		Short: "List unspent outputs (explicit only)",
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, _, err := walletClient()
			if err != nil {
				return err
			}

			var utxos []rpc.WalletUTXO
			if luAsset != "" {
				utxos, err = wc.ListUnspentByAsset(luAsset)
			} else {
				all, listErr := wc.ListUnspentAll()
				if listErr != nil {
					return fmt.Errorf("listunspent: %w", listErr)
				}
				for _, u := range all {
					if u.IsExplicit() {
						utxos = append(utxos, u)
					}
				}
				err = nil
			}
			if err != nil {
				return fmt.Errorf("list unspent: %w", err)
			}

			if jsonOut {
				var out []map[string]any
				for _, u := range utxos {
					out = append(out, map[string]any{
						"txid":   u.TxID,
						"vout":   u.Vout,
						"asset":  u.Asset,
						"amount": satoshis(u.Amount),
					})
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("%-67s  %-18s  %s\n", "TXID:VOUT", "ASSET", "AMOUNT (sats)")
			fmt.Println(strings.Repeat("-", 110))
			for _, u := range utxos {
				assetShort := u.Asset
				if len(assetShort) > 16 {
					assetShort = assetShort[:8] + "..." + assetShort[len(assetShort)-5:]
				}
				fmt.Printf("%s:%-4d  %-18s  %d\n", u.TxID, u.Vout, assetShort, satoshis(u.Amount))
			}
			return nil
		},
	}
	luCmd.Flags().StringVar(&luAsset, "asset", "", "Filter to a specific asset ID")

	// ── getnewaddress ───────────────────────────────────────────────
	addrCmd := &cobra.Command{
		Use:   "getnewaddress",
		Short: "Generate a new unconfidential receiving address",
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, _, err := walletClient()
			if err != nil {
				return err
			}
			addr, err := wc.GetNewAddress()
			if err != nil {
				return fmt.Errorf("getnewaddress: %w", err)
			}
			unconf, err := wc.GetUnconfidentialAddress(addr)
			if err != nil {
				unconf = addr
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{
					"address": unconf,
				})
			}
			fmt.Println(unconf)
			return nil
		},
	}

	// ── sendtoaddress ───────────────────────────────────────────────
	var sendAsset string
	var sendYes bool
	sendCmd := &cobra.Command{
		Use:   "sendtoaddress <address> <amount>",
		Short: "Send funds to an address",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			wc, lbtc, err := walletClient()
			if err != nil {
				return err
			}
			address := args[0]
			var amount uint64
			if _, err := fmt.Sscanf(args[1], "%d", &amount); err != nil {
				return fmt.Errorf("invalid amount %q — expected integer satoshis", args[1])
			}
			asset := sendAsset
			if asset == "" {
				asset = lbtc
			}

			if !sendYes && isTerminal() {
				assetLabel := asset
				if len(assetLabel) > 16 {
					assetLabel = assetLabel[:8] + "..." + assetLabel[len(assetLabel)-5:]
				}
				fmt.Fprintf(os.Stderr, "Send %d sats of %s to %s\n", amount, assetLabel, address)
				answer := promptString("Confirm? [y/n]: ")
				if strings.ToLower(answer) != "y" {
					fmt.Fprintln(os.Stderr, "Aborted.")
					return nil
				}
			}

			txid, err := wc.SendToAddress(address, amount, sendAsset)
			if err != nil {
				return fmt.Errorf("sendtoaddress: %w", err)
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]string{
					"txid": txid,
				})
			}
			fmt.Printf("Txid: %s\n", txid)
			return nil
		},
	}
	sendCmd.Flags().StringVar(&sendAsset, "asset", "", "Asset ID to send (default: L-BTC)")
	sendCmd.Flags().BoolVar(&sendYes, "yes", false, "Skip confirmation prompt")

	cmd.AddCommand(balCmd, luCmd, addrCmd, sendCmd)
	return cmd
}
