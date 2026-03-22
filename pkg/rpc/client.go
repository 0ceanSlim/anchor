// Package rpc provides a minimal Elements JSON-RPC client.
package rpc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client is an Elements node JSON-RPC client.
type Client struct {
	url  string
	user string
	pass string
	http *http.Client
}

// New creates an RPC client for the given endpoint.
func New(url, user, pass string) *Client {
	return &Client{url: url, user: user, pass: pass, http: &http.Client{}}
}

type rpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  []any  `json:"params"`
	ID      int    `json:"id"`
}

type rpcResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *rpcError       `json:"error"`
	ID     int             `json:"id"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (c *Client) call(method string, params []any, result any) error {
	reqBody, err := json.Marshal(rpcRequest{
		JSONRPC: "1.1",
		Method:  method,
		Params:  params,
		ID:      1,
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.url, bytes.NewReader(reqBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth(c.user, c.pass)

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("rpc %s: %w", method, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var rpc rpcResponse
	if err := json.Unmarshal(body, &rpc); err != nil {
		return fmt.Errorf("rpc %s: invalid response: %w", method, err)
	}
	if rpc.Error != nil {
		return fmt.Errorf("rpc %s: code %d: %s", method, rpc.Error.Code, rpc.Error.Message)
	}

	if result != nil {
		return json.Unmarshal(rpc.Result, result)
	}
	return nil
}

// UTXO holds unspent output details.
type UTXO struct {
	TxID          string  `json:"txid"`
	Vout          uint32  `json:"vout"`
	Asset         string  `json:"asset"`
	Amount        float64 `json:"amount"`
	ScriptPubKey  string  `json:"scriptPubKey"`
	Confirmations int     `json:"confirmations"`
}

// GetTxOut returns UTXO details for (txid, vout). Returns nil if spent/not found.
func (c *Client) GetTxOut(txid string, vout uint32) (*UTXO, error) {
	var raw json.RawMessage
	if err := c.call("gettxout", []any{txid, vout, true}, &raw); err != nil {
		return nil, err
	}
	if string(raw) == "null" {
		return nil, nil
	}

	// Elements gettxout returns value as a flat float64 and asset as a flat string.
	var out struct {
		Asset        string  `json:"asset"`
		Value        float64 `json:"value"`
		ScriptPubKey struct {
			Hex string `json:"hex"`
		} `json:"scriptPubKey"`
		Confirmations int `json:"confirmations"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("gettxout parse: %w", err)
	}
	return &UTXO{
		TxID:          txid,
		Vout:          vout,
		Asset:         out.Asset,
		Amount:        out.Value,
		ScriptPubKey:  out.ScriptPubKey.Hex,
		Confirmations: out.Confirmations,
	}, nil
}

// ScanResult holds a UTXO from scantxoutset.
type ScanResult struct {
	TxID   string  `json:"txid"`
	Vout   uint32  `json:"vout"`
	Asset  string  `json:"asset"`
	Amount float64 `json:"amount"`
	Height int     `json:"height"`
}

// ScanAddress uses scantxoutset to find UTXOs at a given address.
func (c *Client) ScanAddress(addr string) ([]ScanResult, error) {
	var result struct {
		Unspents []ScanResult `json:"unspents"`
		Success  bool         `json:"success"`
	}
	desc := fmt.Sprintf("addr(%s)", addr)
	if err := c.call("scantxoutset", []any{"start", []any{desc}}, &result); err != nil {
		return nil, err
	}
	return result.Unspents, nil
}

// SendRawTransaction broadcasts a raw hex transaction and returns the txid.
func (c *Client) SendRawTransaction(hexTx string) (string, error) {
	var txid string
	if err := c.call("sendrawtransaction", []any{hexTx}, &txid); err != nil {
		return "", err
	}
	return txid, nil
}

// GetNetworkInfo returns the node network name for connectivity checks.
func (c *Client) GetNetworkInfo() (string, error) {
	var info struct {
		Chain string `json:"chain"`
	}
	if err := c.call("getblockchaininfo", []any{}, &info); err != nil {
		return "", err
	}
	return info.Chain, nil
}

// GetRawTransaction fetches a raw transaction as hex.
func (c *Client) GetRawTransaction(txid string) (string, error) {
	var hexTx string
	if err := c.call("getrawtransaction", []any{txid, false}, &hexTx); err != nil {
		return "", err
	}
	return hexTx, nil
}

// WalletUTXO holds an unspent output from the wallet's listunspent.
type WalletUTXO struct {
	TxID          string  `json:"txid"`
	Vout          uint32  `json:"vout"`
	Asset         string  `json:"asset"`
	Amount        float64 `json:"amount"`
	AmountBlinder string  `json:"amountblinder"`
}

// IsExplicit returns true if this UTXO has unblinded (explicit) value and asset.
// Confidential UTXOs cannot be used as inputs in manually-built transactions
// because Elements cannot verify balance with mixed confidential/explicit values.
func (u *WalletUTXO) IsExplicit() bool {
	return u.AmountBlinder == "" || u.AmountBlinder == "0000000000000000000000000000000000000000000000000000000000000000"
}

// GetWalletUTXO calls listunspent and returns the entry matching (txid, vout).
// Returns nil if not found. The wallet decrypts confidential amounts automatically.
func (c *Client) GetWalletUTXO(txid string, vout uint32) (*WalletUTXO, error) {
	var utxos []WalletUTXO
	if err := c.call("listunspent", []any{0, 9999999}, &utxos); err != nil {
		return nil, fmt.Errorf("listunspent: %w", err)
	}
	for _, u := range utxos {
		if u.TxID == txid && u.Vout == vout {
			return &u, nil
		}
	}
	return nil, nil
}

// GetOutputFromTx fetches a specific output's amount and asset from the raw transaction.
// Use as a fallback when GetTxOut returns nil (e.g. for confidential/Taproot outputs).
func (c *Client) GetOutputFromTx(txid string, vout uint32) (amount float64, asset string, err error) {
	var decoded struct {
		Vout []struct {
			Value float64 `json:"value"`
			Asset string  `json:"asset"`
			N     uint32  `json:"n"`
		} `json:"vout"`
	}
	if err := c.call("getrawtransaction", []any{txid, true}, &decoded); err != nil {
		return 0, "", fmt.Errorf("getrawtransaction %s: %w", txid, err)
	}
	for _, o := range decoded.Vout {
		if o.N == vout {
			return o.Value, o.Asset, nil
		}
	}
	return 0, "", fmt.Errorf("output %s:%d not found in transaction", txid, vout)
}

// SignRawTransactionWithWallet signs a raw transaction hex using the node's wallet.
// Returns the signed hex and whether all inputs are complete.
func (c *Client) SignRawTransactionWithWallet(hexTx string) (string, bool, error) {
	var result struct {
		Hex      string `json:"hex"`
		Complete bool   `json:"complete"`
	}
	if err := c.call("signrawtransactionwithwallet", []any{hexTx}, &result); err != nil {
		return "", false, err
	}
	return result.Hex, result.Complete, nil
}

// ── Wallet management ─────────────────────────────────────────────────────────

// WalletClient returns a Client that routes calls to /wallet/<name>.
// Wallet-specific RPCs (listunspent, sendtoaddress, etc.) must use this client
// when more than one wallet is loaded.
func (c *Client) WalletClient(name string) *Client {
	base := strings.TrimRight(c.url, "/")
	// Strip any existing /wallet/... path before appending
	if idx := strings.Index(base, "/wallet/"); idx >= 0 {
		base = base[:idx]
	}
	return &Client{url: base + "/wallet/" + name, user: c.user, pass: c.pass, http: c.http}
}

// LoadWallet loads a wallet by name. Returns an error if not found.
func (c *Client) LoadWallet(name string) error {
	var result struct {
		Name string `json:"name"`
	}
	return c.call("loadwallet", []any{name}, &result)
}

// CreateWallet creates a new wallet with the given name.
func (c *Client) CreateWallet(name string) error {
	var result struct {
		Name string `json:"name"`
	}
	return c.call("createwallet", []any{name}, &result)
}

// LoadOrCreateWallet ensures the named wallet is loaded, creating it if needed.
// Returns a wallet-scoped Client for subsequent wallet RPC calls.
func (c *Client) LoadOrCreateWallet(name string) (*Client, error) {
	err := c.LoadWallet(name)
	if err != nil {
		msg := err.Error()
		// Already loaded is fine; any other error means it doesn't exist — create it.
		if !strings.Contains(msg, "already loaded") && !strings.Contains(msg, "is already open") {
			if cerr := c.CreateWallet(name); cerr != nil {
				return nil, fmt.Errorf("load wallet %q: %w; create: %v", name, err, cerr)
			}
		}
	}
	return c.WalletClient(name), nil
}

// GetNewAddress returns a new bech32 receiving address from the wallet.
func (c *Client) GetNewAddress() (string, error) {
	var addr string
	if err := c.call("getnewaddress", []any{}, &addr); err != nil {
		return "", err
	}
	return addr, nil
}

// SendToAddress sends satoshis of assetID to addr and returns the txid.
// Pass assetID="" to send the network native asset (L-BTC).
func (c *Client) SendToAddress(addr string, satoshis uint64, assetID string) (string, error) {
	amount := float64(satoshis) / 1e8
	var txid string
	var err error
	if assetID == "" {
		err = c.call("sendtoaddress", []any{addr, amount}, &txid)
	} else {
		// Elements sendtoaddress: fill optional positional params with null up to
		// assetlabel (position 10). null means "use node default" for each param.
		// Signature: addr, amount, comment, comment_to, subtractfeefromamount,
		// replaceable, conf_target, estimate_mode, avoid_reuse, assetlabel
		err = c.call("sendtoaddress", []any{addr, amount, nil, nil, nil, nil, nil, nil, nil, assetID}, &txid)
	}
	return txid, err
}

// WalletTxDetail is one output entry in a gettransaction result.
type WalletTxDetail struct {
	Address  string  `json:"address"`
	Category string  `json:"category"`
	Amount   float64 `json:"amount"`
	Asset    string  `json:"asset"`
	Vout     uint32  `json:"vout"`
}

// WalletTx is the result of gettransaction.
type WalletTx struct {
	TxID          string           `json:"txid"`
	Confirmations int              `json:"confirmations"`
	Details       []WalletTxDetail `json:"details"`
}

// GetTransaction returns wallet transaction details for txid.
func (c *Client) GetTransaction(txid string) (*WalletTx, error) {
	var tx WalletTx
	if err := c.call("gettransaction", []any{txid}, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// GetBlockCount returns the current best block height.
func (c *Client) GetBlockCount() (int, error) {
	var count int
	if err := c.call("getblockcount", []any{}, &count); err != nil {
		return 0, err
	}
	return count, nil
}

// ListUnspentAll returns all wallet UTXOs with 0 or more confirmations.
func (c *Client) ListUnspentAll() ([]WalletUTXO, error) {
	var utxos []WalletUTXO
	if err := c.call("listunspent", []any{0, 9999999}, &utxos); err != nil {
		return nil, err
	}
	return utxos, nil
}

// ListUnspentByAsset returns explicit (unblinded) wallet UTXOs for a specific asset ID.
// Confidential UTXOs are excluded because they cannot be used as inputs in
// manually-built transactions with explicit outputs.
func (c *Client) ListUnspentByAsset(assetID string) ([]WalletUTXO, error) {
	all, err := c.ListUnspentAll()
	if err != nil {
		return nil, err
	}
	var out []WalletUTXO
	for _, u := range all {
		if strings.EqualFold(u.Asset, assetID) && u.IsExplicit() {
			out = append(out, u)
		}
	}
	return out, nil
}

// GetUnconfidentialAddress returns the non-confidential (unblinded) form of a
// Liquid address. Funds sent to a non-confidential address produce explicit
// (unblinded) UTXOs, which can be spent in transactions with explicit outputs
// without Pedersen-commitment balance issues.
// Returns addr unchanged if the node does not report an unconfidential form.
func (c *Client) GetUnconfidentialAddress(addr string) (string, error) {
	var info struct {
		Unconfidential string `json:"unconfidential"`
	}
	if err := c.call("getaddressinfo", []any{addr}, &info); err != nil {
		return "", err
	}
	if info.Unconfidential != "" {
		return info.Unconfidential, nil
	}
	return addr, nil
}

// SendMany sends satoshis to multiple addresses in a single wallet transaction.
// All recipients use the same assetID; pass "" for the network's native asset.
// Returns the txid of the broadcast transaction.
func (c *Client) SendMany(outputs map[string]uint64, assetID string) (string, error) {
	amounts := make(map[string]float64, len(outputs))
	for addr, sats := range outputs {
		amounts[addr] = float64(sats) / 1e8
	}
	var txid string
	var err error
	if assetID == "" {
		// Native asset: minimal params — no assetlabel needed.
		err = c.call("sendmany", []any{"", amounts}, &txid)
	} else {
		// Non-native asset: assetlabels (pos 8) must be a JSON object {addr: assetID}.
		// subtractfeefromamount (pos 4) must be an array, not null.
		assetLabels := make(map[string]string, len(outputs))
		for addr := range outputs {
			assetLabels[addr] = assetID
		}
		err = c.call("sendmany", []any{"", amounts, 1, "", []string{}, nil, nil, nil, assetLabels}, &txid)
	}
	return txid, err
}

// GetMempoolMinFee returns the minimum fee rate (sat/vb) that will get
// accepted into the mempool. Returns 0.1 as fallback (Elements minimum).
func (c *Client) GetMempoolMinFee() (float64, error) {
	var result struct {
		MempoolMinFee float64 `json:"mempoolminfee"`
	}
	if err := c.call("getmempoolinfo", []any{}, &result); err != nil {
		return 0.1, err
	}
	satPerVb := result.MempoolMinFee * 100_000 // BTC/kB → sat/vB
	if satPerVb < 0.1 {
		satPerVb = 0.1
	}
	return satPerVb, nil
}

// EstimateSmartFee returns an estimated fee rate in sat/vb for the given
// confirmation target. Returns 1 sat/vb as a fallback if the node cannot
// estimate (e.g. insufficient data on regtest).
func (c *Client) EstimateSmartFee(confTarget int) (uint64, error) {
	var result struct {
		FeeRate float64 `json:"feerate"`
	}
	if err := c.call("estimatesmartfee", []any{confTarget}, &result); err != nil {
		return 0, err
	}
	if result.FeeRate <= 0 {
		return 1, nil
	}
	// BTC/kB → sat/vb: feerate * 1e8 / 1000
	satPerVb := uint64(result.FeeRate * 1e5)
	if satPerVb < 1 {
		satPerVb = 1
	}
	return satPerVb, nil
}

// GetBlockHash returns the block hash for the given height.
func (c *Client) GetBlockHash(height int) (string, error) {
	var hash string
	if err := c.call("getblockhash", []any{height}, &hash); err != nil {
		return "", err
	}
	return hash, nil
}

// BlockTx is a transaction entry in a verbose block.
type BlockTx struct {
	TxID string     `json:"txid"`
	Vout []BlockVout `json:"vout"`
}

// BlockVout is an output entry in a verbose block transaction.
type BlockVout struct {
	N            uint32 `json:"n"`
	ScriptPubKey struct {
		Hex string `json:"hex"`
	} `json:"scriptPubKey"`
}

// GetBlockTxs returns all transactions in the block with the given hash (verbosity=2).
func (c *Client) GetBlockTxs(hash string) ([]BlockTx, error) {
	var result struct {
		Tx []BlockTx `json:"tx"`
	}
	if err := c.call("getblock", []any{hash, 2}, &result); err != nil {
		return nil, err
	}
	return result.Tx, nil
}

// DecodedTx holds decoded raw transaction data.
type DecodedTx struct {
	TxID string `json:"txid"`
	Vin  []struct {
		TxID string `json:"txid"`
		Vout uint32 `json:"vout"`
	} `json:"vin"`
	Vout []struct {
		N     uint32  `json:"n"`
		Value float64 `json:"value"`
		Asset string  `json:"asset"`
		ScriptPubKey struct {
			Hex     string `json:"hex"`
			Address string `json:"address"`
		} `json:"scriptPubKey"`
	} `json:"vout"`
}

// DecodeRawTransaction fetches and decodes a transaction by txid.
func (c *Client) DecodeRawTransaction(txid string) (*DecodedTx, error) {
	var tx DecodedTx
	if err := c.call("getrawtransaction", []any{txid, true}, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// DecodedRawTx holds the decoded output of decoderawtransaction.
type DecodedRawTx struct {
	TxID string `json:"txid"`
	Vin  []struct {
		TxID      string `json:"txid"`
		Vout      uint32 `json:"vout"`
		Issuance  *struct {
			AssetBlindingNonce string  `json:"assetBlindingNonce"`
			AssetEntropy       string  `json:"assetEntropy"`
			IsReissuance       bool    `json:"isReissuance"`
			Asset              string  `json:"asset"`
			AssetAmount        float64 `json:"assetamount"`
			Token              string  `json:"token"`
			TokenAmount        float64 `json:"tokenamount"`
		} `json:"issuance"`
	} `json:"vin"`
	Vout []struct {
		N     uint32  `json:"n"`
		Value float64 `json:"value"`
		Asset string  `json:"asset"`
		ScriptPubKey struct {
			Hex string `json:"hex"`
		} `json:"scriptPubKey"`
	} `json:"vout"`
}

// DecodeRawTx decodes a raw transaction hex and returns the parsed structure.
func (c *Client) DecodeRawTx(hexTx string) (*DecodedRawTx, error) {
	var result DecodedRawTx
	if err := c.call("decoderawtransaction", []any{hexTx}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// TestMempoolAccept tests whether a raw tx would be accepted into the mempool.
// Returns the accept/reject result per transaction.
func (c *Client) TestMempoolAccept(hexTx string) (allowed bool, rejectReason string, err error) {
	var results []struct {
		TxID    string `json:"txid"`
		Allowed bool   `json:"allowed"`
		RejectReason string `json:"reject-reason"`
	}
	if err := c.call("testmempoolaccept", []any{[]string{hexTx}}, &results); err != nil {
		return false, "", err
	}
	if len(results) == 0 {
		return false, "no result", nil
	}
	return results[0].Allowed, results[0].RejectReason, nil
}

// ReissueResult holds the result of a reissueasset RPC call.
type ReissueResult struct {
	TxID string `json:"txid"`
	Vin  int    `json:"vin"`
}

// ReissueAsset uses the wallet to reissue (mint) more of an existing asset.
// The wallet must hold the reissuance token for the given asset.
// amount is in BTC units (e.g., 0.00003162 for 3162 sats).
func (c *Client) ReissueAsset(assetID string, amount float64) (*ReissueResult, error) {
	var result ReissueResult
	if err := c.call("reissueasset", []any{assetID, amount}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// WaitForConfirmations polls gettransaction every interval until txid has at
// least minConf confirmations, or until timeout is reached.
func (c *Client) WaitForConfirmations(txid string, minConf int, timeout, interval time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		tx, err := c.GetTransaction(txid)
		if err == nil && tx.Confirmations >= minConf {
			return nil
		}
		if time.Now().After(deadline) {
			if err != nil {
				return fmt.Errorf("tx %s: %w (timed out after %s)", txid, err, timeout)
			}
			return fmt.Errorf("tx %s: %d/%d confirmations after %s", txid, tx.Confirmations, minConf, timeout)
		}
		time.Sleep(interval)
	}
}
