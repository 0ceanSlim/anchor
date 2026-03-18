package main

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/vulpemventures/go-elements/transaction"
)

// stdinReader is a shared buffered reader for interactive prompts.
var stdinReader = bufio.NewReader(os.Stdin)

// promptString prints prompt to stderr and returns a trimmed line from stdin.
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
