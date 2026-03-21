// Package esplora provides a minimal typed client for the Esplora HTTP API
// (Elements/Liquid variant). Only the endpoints needed by Anchor are implemented.
package esplora

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is an Esplora HTTP API client.
type Client struct {
	baseURL string
	http    *http.Client
}

// New creates an Esplora client for the given base URL (e.g. "http://10.1.10.5:5500").
func New(baseURL string) *Client {
	return &Client{
		baseURL: baseURL,
		http: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// get performs an HTTP GET and decodes the JSON response into result.
func (c *Client) get(path string, result any) error {
	url := c.baseURL + path
	resp, err := c.http.Get(url)
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("GET %s: status %d: %s", path, resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("GET %s: read body: %w", path, err)
	}

	return json.Unmarshal(body, result)
}

// ── Types ─────────────────────────────────────────────────────────────────────

// TxStatus holds confirmation info for a transaction or UTXO.
type TxStatus struct {
	Confirmed   bool   `json:"confirmed"`
	BlockHeight int    `json:"block_height"`
	BlockHash   string `json:"block_hash"`
	BlockTime   int64  `json:"block_time"`
}

// UTXO is an unspent output from the address UTXO endpoint.
type UTXO struct {
	TxID   string   `json:"txid"`
	Vout   uint32   `json:"vout"`
	Value  uint64   `json:"value"`
	Asset  string   `json:"asset"`
	Status TxStatus `json:"status"`
}

// Tx is a transaction from the Esplora API.
type Tx struct {
	TxID     string   `json:"txid"`
	Version  int      `json:"version"`
	Locktime int      `json:"locktime"`
	Size     int      `json:"size"`
	Weight   int      `json:"weight"`
	Fee      uint64   `json:"fee"`
	Vin      []TxVin  `json:"vin"`
	Vout     []TxVout `json:"vout"`
	Status   TxStatus `json:"status"`
}

// TxVin is a transaction input.
type TxVin struct {
	TxID      string    `json:"txid"`
	Vout      uint32    `json:"vout"`
	Prevout   *TxVout   `json:"prevout"`
	Issuance  *Issuance `json:"issuance"`
	IsCoinbase bool     `json:"is_coinbase"`
	Sequence  uint32    `json:"sequence"`
}

// TxVout is a transaction output.
type TxVout struct {
	ScriptPubKey     string `json:"scriptpubkey"`
	ScriptPubKeyAsm  string `json:"scriptpubkey_asm"`
	ScriptPubKeyType string `json:"scriptpubkey_type"`
	ScriptPubKeyAddr string `json:"scriptpubkey_address"`
	Value            uint64 `json:"value"`
	Asset            string `json:"asset"`
}

// Issuance holds Elements asset issuance data attached to a transaction input.
type Issuance struct {
	AssetID        string `json:"asset_id"`
	IsReissuance   bool   `json:"is_reissuance"`
	ContractHash   string `json:"contract_hash"`
	AssetEntropy   string `json:"asset_entropy"`
	AssetAmount    uint64 `json:"assetamount"`
	TokenAmount    uint64 `json:"tokenamount"`
}

// Block is a block summary from the blocks endpoint.
type Block struct {
	ID                string `json:"id"`
	Height            int    `json:"height"`
	Version           int    `json:"version"`
	Timestamp         int64  `json:"timestamp"`
	MedianTime        int64  `json:"mediantime"`
	TxCount           int    `json:"tx_count"`
	Size              int    `json:"size"`
	Weight            int    `json:"weight"`
	PreviousBlockHash string `json:"previousblockhash"`
}

// ── Endpoints ─────────────────────────────────────────────────────────────────

// GetTx fetches a transaction by txid.
func (c *Client) GetTx(txid string) (*Tx, error) {
	var tx Tx
	if err := c.get("/api/tx/"+txid, &tx); err != nil {
		return nil, err
	}
	return &tx, nil
}

// GetAddressUTXOs returns unspent outputs for an address.
func (c *Client) GetAddressUTXOs(addr string) ([]UTXO, error) {
	var utxos []UTXO
	if err := c.get("/api/address/"+addr+"/utxo", &utxos); err != nil {
		return nil, err
	}
	return utxos, nil
}

// GetAddressTxs returns up to 25 confirmed transactions for an address,
// plus up to 50 unconfirmed. For paging confirmed txs, pass the last txid
// seen as afterTxID; pass "" for the first page.
func (c *Client) GetAddressTxs(addr string, afterTxID string) ([]Tx, error) {
	path := "/api/address/" + addr + "/txs"
	if afterTxID != "" {
		path += "/chain/" + afterTxID
	}
	var txs []Tx
	if err := c.get(path, &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// GetAddressMempoolTxs returns unconfirmed transactions for an address (max 50).
func (c *Client) GetAddressMempoolTxs(addr string) ([]Tx, error) {
	var txs []Tx
	if err := c.get("/api/address/"+addr+"/txs/mempool", &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// GetBlocks returns 10 block summaries starting at the given height (descending).
// Pass -1 to start from the tip.
func (c *Client) GetBlocks(startHeight int) ([]Block, error) {
	path := "/api/blocks"
	if startHeight >= 0 {
		path = fmt.Sprintf("/api/blocks/%d", startHeight)
	}
	var blocks []Block
	if err := c.get(path, &blocks); err != nil {
		return nil, err
	}
	return blocks, nil
}

// GetBlockTxs returns up to 25 transactions from a block, starting at startIndex.
// startIndex must be a multiple of 25.
func (c *Client) GetBlockTxs(blockHash string, startIndex int) ([]Tx, error) {
	path := fmt.Sprintf("/api/block/%s/txs/%d", blockHash, startIndex)
	var txs []Tx
	if err := c.get(path, &txs); err != nil {
		return nil, err
	}
	return txs, nil
}

// Ping checks connectivity by fetching the chain tip height.
// Returns the current block height or an error.
func (c *Client) Ping() (int, error) {
	var height int
	if err := c.get("/api/blocks/tip/height", &height); err != nil {
		return 0, err
	}
	return height, nil
}
