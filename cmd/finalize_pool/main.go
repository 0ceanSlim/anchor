// cmd/finalize_pool: sign and broadcast an unsigned pool creation tx.
// Usage: finalize_pool <unsigned-tx-hex> <lp-minted> [wallet-name]
// Reads pool.json for pool_creation binary_hex and CMR.
// Env: ANCHOR_RPC_URL, ANCHOR_RPC_USER, ANCHOR_RPC_PASS
package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/0ceanslim/anchor/pkg/pool"
	anchorTaproot "github.com/0ceanslim/anchor/pkg/taproot"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/vulpemventures/go-elements/transaction"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Fprintln(os.Stderr, "usage: finalize_pool <unsigned-tx-hex> <lp-minted> [wallet-name]")
		os.Exit(1)
	}
	// Accept either a raw hex string or a path to a file containing hex.
	unsignedHex := os.Args[1]
	if data, err := os.ReadFile(unsignedHex); err == nil {
		unsignedHex = strings.TrimSpace(string(data))
	}
	lpMinted, err := strconv.ParseUint(os.Args[2], 10, 64)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bad lp-minted: %v\n", err)
		os.Exit(1)
	}
	walletName := "anchor"
	if len(os.Args) >= 4 {
		walletName = os.Args[3]
	}

	cfg, err := pool.Load("pool.json")
	if err != nil {
		fmt.Fprintf(os.Stderr, "load pool.json: %v\n", err)
		os.Exit(1)
	}

	// Build Simplicity witness for vin[0]: [lpMinted_BE8, program, cmr, control_block]
	program, err := hex.DecodeString(cfg.PoolCreation.BinaryHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode binary: %v\n", err)
		os.Exit(1)
	}
	cmrBytes, err := hex.DecodeString(cfg.PoolCreation.CMR)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode cmr: %v\n", err)
		os.Exit(1)
	}
	cb, err := anchorTaproot.ControlBlock(cmrBytes)
	if err != nil {
		fmt.Fprintf(os.Stderr, "control block: %v\n", err)
		os.Exit(1)
	}
	lpBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(lpBytes, lpMinted)
	witness := [][]byte{lpBytes, program, cmrBytes, cb}

	rpcURL := os.Getenv("ANCHOR_RPC_URL")
	rpcUser := os.Getenv("ANCHOR_RPC_USER")
	rpcPass := os.Getenv("ANCHOR_RPC_PASS")
	base := rpc.New(rpcURL, rpcUser, rpcPass)
	client := base.WalletClient(walletName)

	// Sign wallet inputs (vin[1] and vin[2]).
	signed, complete, err := client.SignRawTransactionWithWallet(unsignedHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sign: %v\n", err)
		os.Exit(1)
	}
	if !complete {
		fmt.Fprintln(os.Stderr, "warning: signing incomplete — trying both wallets")
		// Try the other wallet.
		other := base.WalletClient("test")
		signed2, complete2, err2 := other.SignRawTransactionWithWallet(signed)
		if err2 == nil && complete2 {
			signed = signed2
			complete = true
		}
	}
	if !complete {
		fmt.Fprintln(os.Stderr, "warning: signing still incomplete — broadcasting anyway")
	}

	// Attach Simplicity witness to vin[0] using go-elements parser.
	parsedTx, err := transaction.NewTxFromHex(signed)
	if err != nil {
		fmt.Fprintf(os.Stderr, "parse signed tx: %v\n", err)
		os.Exit(1)
	}
	parsedTx.Inputs[0].Witness = witness
	finalHex, err := parsedTx.ToHex()
	if err != nil {
		fmt.Fprintf(os.Stderr, "serialize tx: %v\n", err)
		os.Exit(1)
	}

	txid, err := base.SendRawTransaction(finalHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "broadcast: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("txid: %s\n", txid)
}
