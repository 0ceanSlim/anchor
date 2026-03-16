//go:build integration

package tests

import (
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	"github.com/0ceanslim/anchor/pkg/rpc"
	"github.com/0ceanslim/anchor/pkg/tx"
	"github.com/vulpemventures/go-elements/network"
	"github.com/vulpemventures/go-elements/transaction"
)

// normalizeHexAsset converts a display-format asset ID (from RPC) to the
// byte-reversed form required by Simplicity .args files.
func normalizeHexAsset(h string) string {
	h = strings.TrimSpace(h)
	h = strings.TrimPrefix(strings.TrimPrefix(h, "0x"), "0X")
	if len(h) != 64 {
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

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// Liquid testnet asset IDs (fixed — do not discover dynamically).
	// tLBTC is the network's native asset; TEST is the faucet test asset.
	// These must match what the faucet at liquidtestnet.com distributes.
	testnetTLBTCAssetID = "25b251070e29ca19043cf33ccd7324e2ddab03ecc4ae0b5e77c4fc0e5cf6c95a"
	testnetTESTAssetID  = "38fca2d939696061a8f76d4e6b5eecd54e3b4221c846f24a6b279e79952850a5"

	// Amounts used for pool creation.
	// L-BTC faucet sends 100_000 sats; test asset faucet sends 5_000 sats.
	// Keep deposits well within faucet amounts so the same UTXOs can be reused.
	deposit0Sats   = uint64(10_000) // sats of tLBTC to deposit as asset0
	deposit1Sats   = uint64(4_000)  // sats of TEST to deposit as asset1
	creationFee    = uint64(500)    // tx fee for create-pool transaction
	faucetPollWait = 15 * time.Second
	faucetTimeout  = 10 * time.Minute
	blockTimeout   = 10 * time.Minute
	blockPollWait  = 15 * time.Second

	swapAmountIn = uint64(1_000) // sats of Asset0 to swap in
	swapFee      = uint64(500)   // L-BTC network fee for the swap tx
	addFee       = uint64(500)   // L-BTC network fee for add-liquidity tx
	removeFee    = uint64(500)   // L-BTC network fee for remove-liquidity tx
)

// ── Helpers ───────────────────────────────────────────────────────────────────

func rpcClient(t *testing.T) *rpc.Client {
	t.Helper()
	url := os.Getenv("ANCHOR_RPC_URL")
	user := os.Getenv("ANCHOR_RPC_USER")
	pass := os.Getenv("ANCHOR_RPC_PASS")
	if url == "" {
		url = "http://localhost:18884"
	}
	return rpc.New(url, user, pass)
}

func poolJSONPath(t *testing.T) string {
	t.Helper()
	if p := os.Getenv("ANCHOR_POOL_JSON"); p != "" {
		return p
	}
	// go test runs with cwd = package dir (tests/), so walk up to project root
	abs, err := filepath.Abs(filepath.Join("..", "pool.json"))
	if err != nil {
		t.Fatalf("resolve pool.json path: %v", err)
	}
	return abs
}

func buildDir(t *testing.T) string {
	t.Helper()
	if d := os.Getenv("ANCHOR_BUILD_DIR"); d != "" {
		return d
	}
	// Default: ../build relative to tests/
	abs, err := filepath.Abs(filepath.Join("..", "build"))
	if err != nil {
		t.Fatalf("resolve build dir: %v", err)
	}
	return abs
}

// projectRoot returns the absolute path to the project root (parent of tests/).
func projectRoot(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolve project root: %v", err)
	}
	return abs
}

// faucetTxRE extracts the txid from the faucet HTML success response.
// The page contains: "Sent N sats to address X with transaction <txid>."
var faucetTxRE = regexp.MustCompile(`with transaction ([0-9a-f]{64})`)

// callFaucet hits the Liquid testnet faucet and returns the txid.
// Returns ("", false) if the faucet is rate-limited (no txid in response).
// Fatals on network errors or HTTP 4xx/5xx.
func callFaucet(t *testing.T, addr, action string) (string, bool) {
	t.Helper()
	url := fmt.Sprintf("https://liquidtestnet.com/faucet?address=%s&action=%s", addr, action)
	t.Logf("Calling faucet (%s): %s", action, url)
	resp, err := http.Get(url) //nolint:gosec
	if err != nil {
		t.Fatalf("faucet %s: %v", action, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)

	if resp.StatusCode >= 400 {
		t.Fatalf("faucet %s returned HTTP %d", action, resp.StatusCode)
	}

	m := faucetTxRE.FindStringSubmatch(bodyStr)
	if len(m) < 2 {
		// Show a snippet so we can see what the rate-limit message says
		snippet := bodyStr
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		t.Logf("Faucet (%s): no txid in response (rate-limited?). Snippet:\n%s", action, snippet)
		return "", false
	}
	txid := m[1]
	t.Logf("Faucet (%s) txid: %s", action, txid)
	return txid, true
}

// ensureFaucetUTXO checks if the wallet already has a UTXO of assetID with at
// least minSats. If not, calls the faucet and waits for the tx to confirm.
// Returns the UTXO to use for the deposit.
func ensureFaucetUTXO(t *testing.T, client *rpc.Client, addr, action, assetID string, minSats uint64) rpc.WalletUTXO {
	t.Helper()

	// Check if we already have a suitable EXPLICIT UTXO (avoids faucet rate limits on reruns).
	// Must be explicit (unblinded) — confidential UTXOs from wallet change cannot be spent
	// alongside explicit outputs without triggering "bad-txns-in-ne-out".
	utxos, err := client.ListUnspentByAsset(assetID)
	if err == nil {
		for _, u := range utxos {
			if satoshis(u.Amount) < minSats {
				continue
			}
			// Only reuse if the UTXO is explicit — gettxout returns Amount>0 for explicit,
			// and null/Amount=0 for confidential (blinded) outputs.
			if info, err := client.GetTxOut(u.TxID, u.Vout); err == nil && info != nil && info.Amount > 0 {
				t.Logf("Reusing existing explicit %s UTXO: %s:%d (%.8f)", action, u.TxID, u.Vout, u.Amount)
				return u
			}
		}
	}

	// No suitable UTXO — hit the faucet
	txid, ok := callFaucet(t, addr, action)
	if !ok {
		t.Fatalf("faucet (%s) rate-limited and no existing UTXO with >= %d sats available", action, minSats)
	}
	return utxoFromTx(t, client, txid, faucetTimeout, faucetPollWait)
}

// utxoFromTx waits for txid to confirm then returns the first receive UTXO in the tx.
// Uses gettransaction details; does not match by address since the wallet returns
// unblinded addresses while GetNewAddress returns confidential addresses.
func utxoFromTx(t *testing.T, client *rpc.Client, txid string, timeout, interval time.Duration) rpc.WalletUTXO {
	t.Helper()
	t.Logf("Waiting for txid %s to confirm...", txid)
	if err := client.WaitForConfirmations(txid, 1, timeout, interval); err != nil {
		t.Fatalf("wait confirm %s: %v", txid, err)
	}
	tx, err := client.GetTransaction(txid)
	if err != nil {
		t.Fatalf("gettransaction %s: %v", txid, err)
	}
	for _, d := range tx.Details {
		if d.Category == "receive" {
			t.Logf("Confirmed: %s:%d  asset=%s  amount=%.8f", txid, d.Vout, d.Asset, d.Amount)
			return rpc.WalletUTXO{TxID: txid, Vout: d.Vout, Asset: d.Asset, Amount: d.Amount}
		}
	}
	t.Fatalf("txid %s confirmed but no receive output found in wallet details", txid)
	panic("unreachable")
}

// attachWitness sets the Simplicity witness on input[idx] of a signed tx.
func attachWitness(txHex string, inputIdx int, witness [][]byte) (string, error) {
	parsed, err := transaction.NewTxFromHex(txHex)
	if err != nil {
		return "", fmt.Errorf("parse tx: %w", err)
	}
	if inputIdx >= len(parsed.Inputs) {
		return "", fmt.Errorf("input index %d out of range", inputIdx)
	}
	parsed.Inputs[inputIdx].Witness = witness
	return parsed.ToHex()
}

func satoshis(btc float64) uint64 {
	v := btc * 1e8
	if v < 0 {
		return 0
	}
	return uint64(v + 0.5)
}

func gcd64(a, b uint64) uint64 {
	for b != 0 {
		a, b = b, a%b
	}
	return a
}

// walletAssetBalance returns the total explicit balance (in satoshis) of assetID
// held in the wallet. Confidential UTXOs are excluded (GetTxOut returns Amount==0).
func walletAssetBalance(t *testing.T, wc, nc *rpc.Client, assetID string) uint64 {
	t.Helper()
	utxos, err := wc.ListUnspentByAsset(assetID)
	if err != nil {
		return 0
	}
	var total uint64
	for _, u := range utxos {
		if info, err := nc.GetTxOut(u.TxID, u.Vout); err == nil && info != nil && info.Amount > 0 {
			total += satoshis(u.Amount)
		}
	}
	return total
}

// doSwapForAsset1 swaps amountIn sats of Asset0 (L-BTC) for Asset1 and returns the new pool state.
// Used by TestFullLifecycle/AddLiquidity to acquire enough Asset1 for a proportional deposit.
func doSwapForAsset1(t *testing.T, cfg *pool.Config, walletClient, nodeClient *rpc.Client, amountIn uint64) *pool.State {
	t.Helper()
	net := network.Testnet
	lbtcAsset := net.AssetID

	state, err := pool.Query(cfg, nodeClient)
	if err != nil {
		t.Fatalf("doSwap: query: %v", err)
	}

	walletAddr, err := walletClient.GetNewAddress()
	if err != nil {
		t.Fatalf("doSwap: getnewaddress: %v", err)
	}
	unconfAddr, err := walletClient.GetUnconfidentialAddress(walletAddr)
	if err != nil {
		unconfAddr = walletAddr
	}

	userSats := amountIn + swapFee
	fundTxid, err := walletClient.SendMany(map[string]uint64{unconfAddr: userSats}, "")
	if err != nil {
		t.Fatalf("doSwap: sendmany: %v", err)
	}
	if err := walletClient.WaitForConfirmations(fundTxid, 1, blockTimeout, blockPollWait); err != nil {
		t.Fatalf("doSwap: fund confirm: %v", err)
	}

	var userVout uint32
	var found bool
	for v := uint32(0); v <= 10 && !found; v++ {
		info, err := nodeClient.GetTxOut(fundTxid, v)
		if err != nil || info == nil || info.Amount == 0 {
			continue
		}
		if satoshis(info.Amount) == userSats && strings.EqualFold(info.Asset, lbtcAsset) {
			userVout = v
			found = true
		}
	}
	if !found {
		t.Fatalf("doSwap: user UTXO not found in %s", fundTxid)
	}

	swapResult, err := tx.BuildSwap(&tx.SwapParams{
		State: state, SwapAsset0In: true, AmountIn: amountIn, MinAmountOut: 0,
		UserTxID: fundTxid, UserVout: userVout, UserAsset: lbtcAsset, UserOutputAddr: unconfAddr,
		PoolAAddr: cfg.PoolA.Address, PoolBAddr: cfg.PoolB.Address,
		Asset0: cfg.Asset0, Asset1: cfg.Asset1, LBTCAsset: lbtcAsset,
		Fee: swapFee, FeeNum: cfg.FeeNum, FeeDen: cfg.FeeDen,
		PoolABinaryHex:    cfg.PoolASwap.BinaryHex,
		PoolBBinaryHex:    cfg.PoolBSwap.BinaryHex,
		PoolACMRHex:       cfg.PoolASwap.CMR,
		PoolBCMRHex:       cfg.PoolBSwap.CMR,
		PoolAControlBlock: cfg.PoolASwap.ControlBlock,
		PoolBControlBlock: cfg.PoolBSwap.ControlBlock,
	})
	if err != nil {
		t.Fatalf("doSwap: BuildSwap: %v", err)
	}

	signed, _, err := walletClient.SignRawTransactionWithWallet(swapResult.TxHex)
	if err != nil {
		t.Fatalf("doSwap: sign: %v", err)
	}
	withA, err := attachWitness(signed, 0, swapResult.PoolAWitness)
	if err != nil {
		t.Fatalf("doSwap: attach pool_a: %v", err)
	}
	finalHex, err := attachWitness(withA, 1, swapResult.PoolBWitness)
	if err != nil {
		t.Fatalf("doSwap: attach pool_b: %v", err)
	}
	swapTxid, err := walletClient.SendRawTransaction(finalHex)
	if err != nil {
		t.Fatalf("doSwap: broadcast: %v", err)
	}
	if err := walletClient.WaitForConfirmations(swapTxid, 1, blockTimeout, blockPollWait); err != nil {
		t.Fatalf("doSwap: confirm: %v", err)
	}
	t.Logf("Pre-swap %d L-BTC → Asset1 confirmed (%s)", amountIn, swapTxid)

	newState, err := pool.Query(cfg, nodeClient)
	if err != nil {
		t.Fatalf("doSwap: re-query: %v", err)
	}
	return newState
}

// compilePool patches .args with the given asset IDs and fee params, then
// compiles all contracts. The pool_creation address depends on the asset IDs
// (via CMR derivation), so assets must be known before calling this.
func compilePool(t *testing.T, net *network.Network, asset0, asset1 string) *pool.Config {
	t.Helper()

	// Resolve simc binary path.
	if os.Getenv("SIMC_PATH") == "" {
		root := projectRoot(t)
		for _, name := range []string{"simc.exe", "simc"} {
			candidate := filepath.Join(root, "bin", name)
			if _, err := os.Stat(candidate); err == nil {
				t.Setenv("SIMC_PATH", candidate)
				t.Logf("Using simc at %s", candidate)
				break
			}
		}
	}

	// Patch .args with real asset IDs and fee params before compiling.
	// Without this, contracts compile with placeholder zero asset IDs and the
	// pool_creation address won't match the contract the node would accept.
	patchMap := map[string]compiler.ArgsParam{
		"ASSET0":   {Value: normalizeHexAsset(asset0), Type: "u256"},
		"ASSET1":   {Value: normalizeHexAsset(asset1), Type: "u256"},
		"FEE_NUM":  {Value: "997", Type: "u64"},
		"FEE_DEN":  {Value: "1000", Type: "u64"},
		"FEE_DIFF": {Value: "3", Type: "u64"},
	}
	if err := compiler.PatchParams(buildDir(t), patchMap); err != nil {
		t.Fatalf("PatchParams: %v", err)
	}

	t.Log("Compiling contracts with simc...")
	cfg, err := compiler.CompileAll(buildDir(t), net)
	if err != nil {
		if strings.Contains(err.Error(), "simc") || strings.Contains(err.Error(), "executable file not found") {
			t.Skipf("simc not available, skipping: %v", err)
		}
		t.Fatalf("CompileAll: %v", err)
	}
	cfg.FeeNum = 997
	cfg.FeeDen = 1000
	if err := cfg.Save(poolJSONPath(t)); err != nil {
		t.Fatalf("save pool.json: %v", err)
	}
	t.Logf("pool.json written to %s", poolJSONPath(t))
	t.Logf("pool_creation address: %s", cfg.PoolCreation.Address)
	t.Logf("pool_a address:        %s", cfg.PoolA.Address)
	return cfg
}

// min returns the smaller of two ints (for slice bounds).
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// ── Full lifecycle test ───────────────────────────────────────────────────────

// TestFullLifecycle runs a complete create-pool → swap → add-liquidity →
// remove-liquidity sequence on Liquid testnet, always creating a fresh pool.
//
// Each step is a subtest; the test aborts early if any step fails.
// After remove-liquidity, LP tokens are verified to be truly gone (OP_RETURN burn).
//
// Run with:
//
//	go test -v -tags=integration -timeout=60m ./tests/ -run TestFullLifecycle
func TestFullLifecycle(t *testing.T) {
	net := network.Testnet
	lbtcAsset := net.AssetID

	nodeClient := rpcClient(t)
	walletClient, err := nodeClient.LoadOrCreateWallet("test")
	if err != nil {
		t.Fatalf("wallet: %v", err)
	}

	walletAddr, err := walletClient.GetNewAddress()
	if err != nil {
		t.Fatalf("getnewaddress: %v", err)
	}
	unconfAddr, err := walletClient.GetUnconfidentialAddress(walletAddr)
	if err != nil {
		t.Logf("GetUnconfidentialAddress failed (%v), falling back", err)
		unconfAddr = walletAddr
	}
	t.Logf("Wallet address (unconfidential): %s", unconfAddr)

	// Pre-compute funding amounts.
	lpMintedCreate := pool.IntSqrt(deposit0Sats * deposit1Sats)
	creationNeeded := lpMintedCreate + creationFee
	minLBTC := creationNeeded + deposit0Sats + 5000 // buffer for swap + add-liquidity fees
	minTEST := deposit1Sats

	// Ensure faucet UTXOs before any subtest.
	t.Logf("Ensuring tLBTC (need ≥%d sats explicit)...", minLBTC)
	ensureFaucetUTXO(t, walletClient, unconfAddr, "lbtc", lbtcAsset, minLBTC)
	t.Logf("Ensuring TEST asset (need ≥%d sats)...", minTEST)
	a1UTXO := ensureFaucetUTXO(t, walletClient, unconfAddr, "test", testnetTESTAssetID, minTEST)

	// State shared across subtests.
	var cfg *pool.Config
	var state *pool.State
	var lpMintedAdd uint64

	// ── Step 1: CreatePool ────────────────────────────────────────────────────

	t.Run("CreatePool", func(t *testing.T) {
		cfg = compilePool(t, &net, lbtcAsset, testnetTESTAssetID)

		creationAddr := cfg.PoolCreation.Address
		t.Logf("sendmany: pool_creation=%s (%d sats), deposit0=%s (%d sats)",
			creationAddr, creationNeeded, unconfAddr, deposit0Sats)
		fundingTxid, err := walletClient.SendMany(map[string]uint64{
			creationAddr: creationNeeded,
			unconfAddr:   deposit0Sats,
		}, "")
		if err != nil {
			t.Fatalf("sendmany: %v", err)
		}
		t.Logf("sendmany txid: %s", fundingTxid)

		if err := walletClient.WaitForConfirmations(fundingTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("funding confirmation: %v", err)
		}

		// Locate the two explicit UTXOs by amount.
		var creationVout, a0Vout uint32
		var foundCreation, foundA0 bool
		for v := uint32(0); v <= 10 && (!foundCreation || !foundA0); v++ {
			info, err := nodeClient.GetTxOut(fundingTxid, v)
			if err != nil || info == nil || info.ScriptPubKey == "" {
				continue
			}
			sats := satoshis(info.Amount)
			switch {
			case !foundCreation && sats == creationNeeded && strings.EqualFold(info.Asset, lbtcAsset):
				creationVout = v
				foundCreation = true
				t.Logf("pool_creation UTXO: %s:%d (%d sats)", fundingTxid, v, sats)
			case !foundA0 && sats == deposit0Sats && strings.EqualFold(info.Asset, lbtcAsset):
				a0Vout = v
				foundA0 = true
				t.Logf("deposit0 UTXO:      %s:%d (%d sats)", fundingTxid, v, sats)
			}
		}
		if !foundCreation {
			t.Fatalf("pool_creation UTXO (%d sats) not found in %s", creationNeeded, fundingTxid)
		}
		if !foundA0 {
			t.Fatalf("deposit0 UTXO (%d sats) not found in %s", deposit0Sats, fundingTxid)
		}

		params := &tx.CreatePoolParams{
			CreationTxID:   fundingTxid,
			CreationVout:   creationVout,
			CreationAmount: creationNeeded,
			Asset0TxID:     fundingTxid,
			Asset0Vout:     a0Vout,
			Asset0Amount:   deposit0Sats,
			Asset1TxID:     a1UTXO.TxID,
			Asset1Vout:     a1UTXO.Vout,
			Asset1Amount:   satoshis(a1UTXO.Amount),
			BuildDir:       buildDir(t),
			Deposit0:       deposit0Sats,
			Deposit1:       deposit1Sats,
			Asset0:         lbtcAsset,
			Asset1:         testnetTESTAssetID,
			LBTCAsset:      lbtcAsset,
			Fee:            creationFee,
			ChangeAddr:     unconfAddr,
			Network:        &net,
		}

		result, err := tx.BuildCreatePool(params, cfg)
		if err != nil {
			t.Fatalf("BuildCreatePool: %v", err)
		}
		t.Logf("LP Asset ID: %s  LP Minted: %d", result.LPAssetID, result.LPMinted)
		for i, item := range result.SimplicityWitness {
			t.Logf("  witness[%d]: %d bytes", i, len(item))
		}

		signed, complete, err := walletClient.SignRawTransactionWithWallet(result.TxHex)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if !complete {
			t.Log("Warning: signing incomplete")
		}

		finalHex, err := attachWitness(signed, 0, result.SimplicityWitness)
		if err != nil {
			t.Fatalf("attach witness: %v", err)
		}

		createTxid, err := walletClient.SendRawTransaction(finalHex)
		if err != nil {
			t.Fatalf("broadcast create-pool: %v", err)
		}
		t.Logf("create-pool txid: %s", createTxid)

		cfg = result.PoolConfig

		if err := cfg.Save(poolJSONPath(t)); err != nil {
			t.Logf("Warning: save pool.json: %v", err)
		} else {
			t.Logf("pool.json saved (pool_a: %s)", cfg.PoolA.Address)
		}

		if err := walletClient.WaitForConfirmations(createTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("create-pool confirmation: %v", err)
		}
		t.Log("create-pool confirmed!")

		state, err = pool.Query(cfg, nodeClient)
		if err != nil {
			t.Fatalf("query pool: %v", err)
		}
		t.Logf("Pool: reserve0=%d  reserve1=%d  supply=%d", state.Reserve0, state.Reserve1, state.TotalSupply())

		if state.Reserve0 != deposit0Sats {
			t.Errorf("reserve0 = %d, want %d", state.Reserve0, deposit0Sats)
		}
		if state.Reserve1 != deposit1Sats {
			t.Errorf("reserve1 = %d, want %d", state.Reserve1, deposit1Sats)
		}
		if state.TotalSupply() != result.LPMinted {
			t.Errorf("total_supply = %d, want %d", state.TotalSupply(), result.LPMinted)
		}

		walletLP := walletAssetBalance(t, walletClient, nodeClient, cfg.LPAssetID)
		t.Logf("Wallet LP balance: %d (expected %d)", walletLP, lpMintedCreate)
		if walletLP != lpMintedCreate {
			t.Errorf("wallet LP = %d, want %d", walletLP, lpMintedCreate)
		}
	})
	if t.Failed() {
		return
	}

	// ── Step 2: Swap (Asset0 → Asset1) ───────────────────────────────────────

	t.Run("Swap", func(t *testing.T) {
		// state is set from CreatePool subtest.
		t.Logf("Pool before swap: reserve0=%d  reserve1=%d", state.Reserve0, state.Reserve1)

		expectedOut := pool.SwapOutput(swapAmountIn, state.Reserve0, state.Reserve1, cfg.FeeNum, cfg.FeeDen)
		if expectedOut == 0 {
			t.Fatal("swap would produce 0 output — deposits too small or amountIn too small")
		}
		t.Logf("Swap %d L-BTC → expected %d Asset1", swapAmountIn, expectedOut)

		userSats := swapAmountIn + swapFee
		fundTxid, err := walletClient.SendMany(map[string]uint64{unconfAddr: userSats}, "")
		if err != nil {
			t.Fatalf("sendmany user UTXO: %v", err)
		}
		if err := walletClient.WaitForConfirmations(fundTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("user fund confirm: %v", err)
		}

		var userVout uint32
		var found bool
		for v := uint32(0); v <= 10 && !found; v++ {
			info, err := nodeClient.GetTxOut(fundTxid, v)
			if err != nil || info == nil || info.Amount == 0 {
				continue
			}
			if satoshis(info.Amount) == userSats && strings.EqualFold(info.Asset, lbtcAsset) {
				userVout = v
				found = true
				t.Logf("User UTXO: %s:%d (%d sats)", fundTxid, v, userSats)
			}
		}
		if !found {
			t.Fatalf("user UTXO (%d sats) not found in %s", userSats, fundTxid)
		}

		swapResult, err := tx.BuildSwap(&tx.SwapParams{
			State:             state,
			SwapAsset0In:      true,
			AmountIn:          swapAmountIn,
			MinAmountOut:      0,
			UserTxID:          fundTxid,
			UserVout:          userVout,
			UserAsset:         lbtcAsset,
			UserOutputAddr:    unconfAddr,
			PoolAAddr:         cfg.PoolA.Address,
			PoolBAddr:         cfg.PoolB.Address,
			Asset0:            cfg.Asset0,
			Asset1:            cfg.Asset1,
			LBTCAsset:         lbtcAsset,
			Fee:               swapFee,
			FeeNum:            cfg.FeeNum,
			FeeDen:            cfg.FeeDen,
			PoolABinaryHex:    cfg.PoolASwap.BinaryHex,
			PoolBBinaryHex:    cfg.PoolBSwap.BinaryHex,
			PoolACMRHex:       cfg.PoolASwap.CMR,
			PoolBCMRHex:       cfg.PoolBSwap.CMR,
			PoolAControlBlock: cfg.PoolASwap.ControlBlock,
			PoolBControlBlock: cfg.PoolBSwap.ControlBlock,
		})
		if err != nil {
			t.Fatalf("BuildSwap: %v", err)
		}

		signed, _, err := walletClient.SignRawTransactionWithWallet(swapResult.TxHex)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		withA, err := attachWitness(signed, 0, swapResult.PoolAWitness)
		if err != nil {
			t.Fatalf("attach pool_a: %v", err)
		}
		finalHex, err := attachWitness(withA, 1, swapResult.PoolBWitness)
		if err != nil {
			t.Fatalf("attach pool_b: %v", err)
		}

		swapTxid, err := walletClient.SendRawTransaction(finalHex)
		if err != nil {
			t.Fatalf("broadcast swap: %v", err)
		}
		t.Logf("swap txid: %s", swapTxid)

		if err := walletClient.WaitForConfirmations(swapTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("swap confirmation: %v", err)
		}
		t.Log("Swap confirmed!")

		prevReserve0 := state.Reserve0
		prevReserve1 := state.Reserve1

		newState, err := pool.Query(cfg, nodeClient)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		state = newState
		t.Logf("Pool after swap: reserve0=%d  reserve1=%d", state.Reserve0, state.Reserve1)

		if state.Reserve0 != prevReserve0+swapAmountIn {
			t.Errorf("reserve0 = %d, want %d", state.Reserve0, prevReserve0+swapAmountIn)
		}
		if state.Reserve1 != prevReserve1-expectedOut {
			t.Errorf("reserve1 = %d, want %d (amountOut=%d)", state.Reserve1, prevReserve1-expectedOut, expectedOut)
		}
	})
	if t.Failed() {
		return
	}

	// ── Step 3: AddLiquidity (with LP reissuance via wallet RPC) ──────────────

	t.Run("AddLiquidity", func(t *testing.T) {
		// state is set from Swap subtest.
		t.Logf("Pool before add: reserve0=%d  reserve1=%d  supply=%d", state.Reserve0, state.Reserve1, state.TotalSupply())

		// Compute minimum proportional deposit unit.
		g := gcd64(state.Reserve0, state.Reserve1)
		minD0 := state.Reserve0 / g
		minD1 := state.Reserve1 / g

		// Check wallet Asset1 balance; swap L-BTC for more if needed.
		walletAsset1 := walletAssetBalance(t, walletClient, nodeClient, cfg.Asset1)
		if walletAsset1 < minD1 {
			need := minD1 - walletAsset1
			if need >= state.Reserve1 {
				t.Fatalf("cannot acquire %d Asset1 via swap — would drain reserve1 (%d)", need, state.Reserve1)
			}
			swapIn := need*state.Reserve0/(state.Reserve1-need) + 1
			t.Logf("Wallet has %d Asset1, need %d — pre-swapping %d L-BTC", walletAsset1, minD1, swapIn)
			state = doSwapForAsset1(t, cfg, walletClient, nodeClient, swapIn)
			g = gcd64(state.Reserve0, state.Reserve1)
			minD0 = state.Reserve0 / g
			minD1 = state.Reserve1 / g
		}

		// Scale up so all amounts exceed dust and lpMinted > 0.
		const minDepositSats = uint64(500)
		scale := uint64(1)
		for minD0*scale < minDepositSats || minD1*scale < minDepositSats {
			scale++
		}
		deposit0 := minD0 * scale
		deposit1 := minD1 * scale
		lpMinted := pool.LPMintedForDeposit(deposit0, deposit1, state.Reserve0, state.Reserve1, state.TotalSupply())
		for lpMinted == 0 {
			scale++
			deposit0 = minD0 * scale
			deposit1 = minD1 * scale
			lpMinted = pool.LPMintedForDeposit(deposit0, deposit1, state.Reserve0, state.Reserve1, state.TotalSupply())
		}
		lpMintedAdd = lpMinted
		t.Logf("Add-liquidity: deposit0=%d  deposit1=%d  lp_minted=%d", deposit0, deposit1, lpMinted)

		// Derive unconfidential addresses for user UTXOs and LP token receipt.
		getUnconf := func() string {
			a, err := walletClient.GetNewAddress()
			if err != nil {
				t.Fatalf("getnewaddress: %v", err)
			}
			u, err := walletClient.GetUnconfidentialAddress(a)
			if err != nil {
				return a
			}
			return u
		}
		unconf1 := getUnconf() // deposit0 (L-BTC)
		unconf2 := getUnconf() // fee (L-BTC)
		unconf3 := getUnconf() // deposit1 (Asset1)
		userLPAddr := getUnconf()

		// Fund L-BTC UTXOs (deposit0 + fee).
		lbtcFundTxid, err := walletClient.SendMany(map[string]uint64{
			unconf1: deposit0,
			unconf2: addFee,
		}, "")
		if err != nil {
			t.Fatalf("sendmany L-BTC: %v", err)
		}
		t.Logf("L-BTC funding txid: %s", lbtcFundTxid)

		// Fund Asset1 UTXO.
		assetFundTxid, err := walletClient.SendMany(map[string]uint64{unconf3: deposit1}, cfg.Asset1)
		if err != nil {
			t.Fatalf("sendmany Asset1: %v", err)
		}
		t.Logf("Asset1 funding txid: %s", assetFundTxid)

		// Wait for all funding txs to confirm.
		for _, txid := range []string{lbtcFundTxid, assetFundTxid} {
			if err := walletClient.WaitForConfirmations(txid, 1, blockTimeout, blockPollWait); err != nil {
				t.Fatalf("fund confirm %s: %v", txid[:8], err)
			}
		}

		findExplicit := func(txid string, wantSats uint64, wantAsset string) uint32 {
			t.Helper()
			for v := uint32(0); v <= 10; v++ {
				info, err := nodeClient.GetTxOut(txid, v)
				if err != nil || info == nil || info.Amount == 0 {
					continue
				}
				if satoshis(info.Amount) == wantSats && strings.EqualFold(info.Asset, wantAsset) {
					t.Logf("  found %s:%d  %d sats  asset=%s", txid, v, wantSats, wantAsset)
					return v
				}
			}
			t.Fatalf("explicit %d sat UTXO (asset=%s) not found in %s", wantSats, wantAsset, txid)
			return 0
		}

		a0Vout := findExplicit(lbtcFundTxid, deposit0, lbtcAsset)
		feeVout := findExplicit(lbtcFundTxid, addFee, lbtcAsset)
		a1Vout := findExplicit(assetFundTxid, deposit1, cfg.Asset1)

		addResult, err := tx.BuildAddLiquidity(&tx.AddLiquidityParams{
			State:                 state,
			Deposit0:              deposit0,
			Deposit1:              deposit1,
			PoolAAddr:             cfg.PoolA.Address,
			PoolBAddr:             cfg.PoolB.Address,
			LpReserveAddr:         cfg.LpReserve.Address,
			Asset0Inputs:          []tx.UserInput{{TxID: lbtcFundTxid, Vout: a0Vout, Amount: deposit0}},
			Asset1Inputs:          []tx.UserInput{{TxID: assetFundTxid, Vout: a1Vout, Amount: deposit1}},
			LBTCInputs:            []tx.UserInput{{TxID: lbtcFundTxid, Vout: feeVout, Amount: addFee}},
			ChangeAddr:            userLPAddr,
			UserLPAddr:            userLPAddr,
			LPAssetID:             cfg.LPAssetID,
			Asset0:                cfg.Asset0,
			Asset1:                cfg.Asset1,
			LBTCAsset:             lbtcAsset,
			Fee:                   addFee,
			PoolABinaryHex:        cfg.PoolASwap.BinaryHex,
			PoolBBinaryHex:        cfg.PoolBSwap.BinaryHex,
			LpReserveBinaryHex:    cfg.LpReserveAdd.BinaryHex,
			PoolACMRHex:           cfg.PoolASwap.CMR,
			PoolBCMRHex:           cfg.PoolBSwap.CMR,
			LpReserveCMRHex:       cfg.LpReserveAdd.CMR,
			PoolAControlBlock:     cfg.PoolASwap.ControlBlock,
			PoolBControlBlock:     cfg.PoolBSwap.ControlBlock,
			LpReserveControlBlock: cfg.LpReserveAdd.ControlBlock,
		})
		if err != nil {
			t.Fatalf("BuildAddLiquidity: %v", err)
		}
		t.Logf("Add-liquidity tx built (lp_minted=%d)", addResult.LPMinted)

		signed, complete, err := walletClient.SignRawTransactionWithWallet(addResult.TxHex)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if !complete {
			t.Log("Warning: signing incomplete")
		}

		withA, err := attachWitness(signed, 0, addResult.PoolAWitness)
		if err != nil {
			t.Fatalf("attach pool_a: %v", err)
		}
		withB, err := attachWitness(withA, 1, addResult.PoolBWitness)
		if err != nil {
			t.Fatalf("attach pool_b: %v", err)
		}
		finalHex, err := attachWitness(withB, 2, addResult.LpReserveWitness)
		if err != nil {
			t.Fatalf("attach lp_reserve: %v", err)
		}

		addTxid, err := walletClient.SendRawTransaction(finalHex)
		if err != nil {
			t.Fatalf("broadcast add-liquidity: %v", err)
		}
		t.Logf("add-liquidity txid: %s", addTxid)

		if err := walletClient.WaitForConfirmations(addTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("add-liquidity confirmation: %v", err)
		}
		t.Log("Add-liquidity confirmed!")

		prevReserve0 := state.Reserve0
		prevReserve1 := state.Reserve1
		prevSupply := state.TotalSupply()

		newState, err := pool.Query(cfg, nodeClient)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		state = newState
		t.Logf("Pool after add: reserve0=%d  reserve1=%d  supply=%d", state.Reserve0, state.Reserve1, state.TotalSupply())

		if state.Reserve0 != prevReserve0+deposit0 {
			t.Errorf("reserve0 = %d, want %d", state.Reserve0, prevReserve0+deposit0)
		}
		if state.Reserve1 != prevReserve1+deposit1 {
			t.Errorf("reserve1 = %d, want %d", state.Reserve1, prevReserve1+deposit1)
		}
		if state.TotalSupply() != prevSupply+addResult.LPMinted {
			t.Errorf("total_supply = %d, want %d", state.TotalSupply(), prevSupply+addResult.LPMinted)
		}

		walletLP := walletAssetBalance(t, walletClient, nodeClient, cfg.LPAssetID)
		t.Logf("Wallet LP balance: %d (expected ≥%d)", walletLP, lpMintedCreate+lpMintedAdd)
		if walletLP < lpMintedCreate+lpMintedAdd {
			t.Errorf("wallet LP = %d, want ≥%d", walletLP, lpMintedCreate+lpMintedAdd)
		}
	})
	if t.Failed() {
		return
	}

	// ── Step 4: RemoveLiquidity (burn LP tokens) ──────────────────────────────

	t.Run("RemoveLiquidity", func(t *testing.T) {
		// state is set from AddLiquidity subtest.
		t.Logf("Pool before remove: reserve0=%d  reserve1=%d  supply=%d", state.Reserve0, state.Reserve1, state.TotalSupply())

		// Find the largest explicit LP UTXO in the wallet.
		lpUTXOs, err := walletClient.ListUnspentByAsset(cfg.LPAssetID)
		if err != nil {
			t.Fatalf("ListUnspentByAsset(LP): %v", err)
		}
		var lpUTXO rpc.WalletUTXO
		for _, u := range lpUTXOs {
			if info, err := nodeClient.GetTxOut(u.TxID, u.Vout); err == nil && info != nil && info.Amount > 0 {
				if satoshis(u.Amount) > satoshis(lpUTXO.Amount) {
					lpUTXO = u
				}
			}
		}
		if lpUTXO.TxID == "" {
			t.Fatal("no explicit LP token UTXO in wallet")
		}
		lpUTXOAmount := satoshis(lpUTXO.Amount)
		t.Logf("LP UTXO: %s:%d  %d LP tokens", lpUTXO.TxID, lpUTXO.Vout, lpUTXOAmount)

		// Cap burn so pool_a and pool_b outputs stay above dust.
		totalSupply := state.TotalSupply()
		const dustMin = uint64(330)
		maxBurnA := (state.Reserve0 - dustMin) * totalSupply / state.Reserve0
		maxBurnB := (state.Reserve1 - dustMin) * totalSupply / state.Reserve1
		maxBurn := maxBurnA
		if maxBurnB < maxBurn {
			maxBurn = maxBurnB
		}
		if maxBurn == 0 {
			t.Fatalf("supply (%d) too small to remove liquidity", totalSupply)
		}
		lpBurned := lpUTXOAmount
		if lpBurned > maxBurn {
			lpBurned = maxBurn
		}

		payout0, payout1 := pool.RemovePayouts(lpBurned, state.Reserve0, state.Reserve1, totalSupply)
		t.Logf("Burning %d LP (of %d in UTXO) → payout0=%d  payout1=%d",
			lpBurned, lpUTXOAmount, payout0, payout1)

		getUnconf := func() string {
			a, err := walletClient.GetNewAddress()
			if err != nil {
				t.Fatalf("getnewaddress: %v", err)
			}
			u, err := walletClient.GetUnconfidentialAddress(a)
			if err != nil {
				return a
			}
			return u
		}
		payoutAddr := getUnconf()

		// Fund L-BTC UTXO for fee.
		feeAddr := getUnconf()
		feeFundTxid, err := walletClient.SendMany(map[string]uint64{feeAddr: removeFee}, "")
		if err != nil {
			t.Fatalf("sendmany fee L-BTC: %v", err)
		}
		t.Logf("Fee funding txid: %s", feeFundTxid)
		if err := walletClient.WaitForConfirmations(feeFundTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("fee fund confirm: %v", err)
		}

		// Find the explicit fee UTXO.
		var feeVout uint32
		for v := uint32(0); v <= 10; v++ {
			info, err := nodeClient.GetTxOut(feeFundTxid, v)
			if err != nil || info == nil || info.Amount == 0 {
				continue
			}
			if satoshis(info.Amount) == removeFee && strings.EqualFold(info.Asset, lbtcAsset) {
				feeVout = v
				break
			}
		}

		removeResult, err := tx.BuildRemoveLiquidity(&tx.RemoveLiquidityParams{
			State:                 state,
			LPBurned:              lpBurned,
			UserLPAmount:          lpUTXOAmount,
			PoolAAddr:             cfg.PoolA.Address,
			PoolBAddr:             cfg.PoolB.Address,
			LpReserveAddr:         cfg.LpReserve.Address,
			UserLPTxID:            lpUTXO.TxID,
			UserLPVout:            lpUTXO.Vout,
			UserLBTCTxID:          feeFundTxid,
			UserLBTCVout:          feeVout,
			UserLBTCAmount:        removeFee,
			UserAsset0Addr:        payoutAddr,
			UserAsset1Addr:        payoutAddr,
			ChangeAddr:            payoutAddr,
			Asset0:                cfg.Asset0,
			Asset1:                cfg.Asset1,
			LPAssetID:             cfg.LPAssetID,
			LBTCAsset:             lbtcAsset,
			Fee:                   removeFee,
			PoolABinaryHex:        cfg.PoolARemove.BinaryHex,
			PoolBBinaryHex:        cfg.PoolBRemove.BinaryHex,
			LpReserveBinaryHex:    cfg.LpReserveRemove.BinaryHex,
			PoolACMRHex:           cfg.PoolARemove.CMR,
			PoolBCMRHex:           cfg.PoolBRemove.CMR,
			LpReserveCMRHex:       cfg.LpReserveRemove.CMR,
			PoolAControlBlock:     cfg.PoolARemove.ControlBlock,
			PoolBControlBlock:     cfg.PoolBRemove.ControlBlock,
			LpReserveControlBlock: cfg.LpReserveRemove.ControlBlock,
		})
		if err != nil {
			t.Fatalf("BuildRemoveLiquidity: %v", err)
		}
		t.Logf("Remove tx built  payout0=%d  payout1=%d", removeResult.Payout0, removeResult.Payout1)

		signed, complete, err := walletClient.SignRawTransactionWithWallet(removeResult.TxHex)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if !complete {
			t.Log("Warning: signing incomplete")
		}
		withA, err := attachWitness(signed, 0, removeResult.PoolAWitness)
		if err != nil {
			t.Fatalf("attach pool_a: %v", err)
		}
		withB, err := attachWitness(withA, 1, removeResult.PoolBWitness)
		if err != nil {
			t.Fatalf("attach pool_b: %v", err)
		}
		finalHex, err := attachWitness(withB, 2, removeResult.LpReserveWitness)
		if err != nil {
			t.Fatalf("attach lp_reserve: %v", err)
		}

		walletLPBefore := walletAssetBalance(t, walletClient, nodeClient, cfg.LPAssetID)

		removeTxid, err := walletClient.SendRawTransaction(finalHex)
		if err != nil {
			t.Fatalf("broadcast remove-liquidity: %v", err)
		}
		t.Logf("remove-liquidity txid: %s", removeTxid)

		if err := walletClient.WaitForConfirmations(removeTxid, 1, blockTimeout, blockPollWait); err != nil {
			t.Fatalf("remove-liquidity confirmation: %v", err)
		}
		t.Log("Remove-liquidity confirmed!")

		prevReserve0 := state.Reserve0
		prevReserve1 := state.Reserve1
		prevSupply := state.TotalSupply()

		newState, err := pool.Query(cfg, nodeClient)
		if err != nil {
			t.Fatalf("query: %v", err)
		}
		state = newState
		t.Logf("Pool after remove: reserve0=%d  reserve1=%d  supply=%d", state.Reserve0, state.Reserve1, state.TotalSupply())

		if state.Reserve0 != prevReserve0-removeResult.Payout0 {
			t.Errorf("reserve0 = %d, want %d", state.Reserve0, prevReserve0-removeResult.Payout0)
		}
		if state.Reserve1 != prevReserve1-removeResult.Payout1 {
			t.Errorf("reserve1 = %d, want %d", state.Reserve1, prevReserve1-removeResult.Payout1)
		}
		if state.TotalSupply() != prevSupply-lpBurned {
			t.Errorf("total_supply = %d, want %d", state.TotalSupply(), prevSupply-lpBurned)
		}

		// Verify LP token balance decreased by exactly lpBurned.
		walletLPAfter := walletAssetBalance(t, walletClient, nodeClient, cfg.LPAssetID)
		t.Logf("Wallet LP before=%d  after=%d  burned=%d", walletLPBefore, walletLPAfter, lpBurned)
		wantLPAfter := walletLPBefore - lpBurned
		if walletLPAfter != wantLPAfter {
			t.Errorf("wallet LP after remove = %d, want %d", walletLPAfter, wantLPAfter)
		}
		if lpBurned == lpUTXOAmount && walletLPAfter == 0 {
			t.Log("All LP tokens returned to reserve")
		}
	})
}
