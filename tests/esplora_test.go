//go:build integration

package tests

import (
	"os"
	"testing"

	"github.com/0ceanslim/anchor/pkg/esplora"
)

func esploraClient(t *testing.T) *esplora.Client {
	t.Helper()
	url := os.Getenv("ANCHOR_ESPLORA_URL")
	if url == "" {
		t.Skip("ANCHOR_ESPLORA_URL not set")
	}
	return esplora.New(url)
}

func TestEsploraPing(t *testing.T) {
	c := esploraClient(t)
	height, err := c.Ping()
	if err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if height < 1 {
		t.Fatalf("unexpected height: %d", height)
	}
	t.Logf("chain tip: %d", height)
}

func TestEsploraGetAddressUTXOs(t *testing.T) {
	c := esploraClient(t)
	// pool_a address from pool.example.json
	utxos, err := c.GetAddressUTXOs("tex1pwxrntyf859lq3md29k7ul6gucr66qfas0lpwf7agwtpfr2a9rhnqx9kkms")
	if err != nil {
		t.Fatalf("GetAddressUTXOs: %v", err)
	}
	if len(utxos) == 0 {
		t.Fatal("expected at least one UTXO")
	}
	u := utxos[0]
	t.Logf("utxo: %s:%d value=%d asset=%s", u.TxID, u.Vout, u.Value, u.Asset)
	if u.Value == 0 {
		t.Error("expected non-zero value")
	}
	if u.Asset == "" {
		t.Error("expected non-empty asset")
	}
}

func TestEsploraGetTx(t *testing.T) {
	c := esploraClient(t)
	// A known pool tx from pool.example.json's pool_a
	tx, err := c.GetTx("e378d2d5ff45bfc7a407fbb59d03cec81cef5af9696b4ce92167c361651a913c")
	if err != nil {
		t.Fatalf("GetTx: %v", err)
	}
	if tx.TxID == "" {
		t.Fatal("expected non-empty txid")
	}
	if len(tx.Vin) == 0 {
		t.Fatal("expected inputs")
	}
	if len(tx.Vout) == 0 {
		t.Fatal("expected outputs")
	}
	t.Logf("tx %s: %d vin, %d vout, fee=%d", tx.TxID, len(tx.Vin), len(tx.Vout), tx.Fee)
	for i, o := range tx.Vout {
		t.Logf("  vout[%d]: value=%d asset=%s type=%s", i, o.Value, o.Asset[:16]+"...", o.ScriptPubKeyType)
	}
}

func TestEsploraGetAddressTxs(t *testing.T) {
	c := esploraClient(t)
	txs, err := c.GetAddressTxs("tex1pwxrntyf859lq3md29k7ul6gucr66qfas0lpwf7agwtpfr2a9rhnqx9kkms", "")
	if err != nil {
		t.Fatalf("GetAddressTxs: %v", err)
	}
	if len(txs) == 0 {
		t.Fatal("expected at least one tx")
	}
	t.Logf("got %d txs for pool_a address", len(txs))
}

func TestEsploraGetBlocks(t *testing.T) {
	c := esploraClient(t)
	blocks, err := c.GetBlocks(-1)
	if err != nil {
		t.Fatalf("GetBlocks: %v", err)
	}
	if len(blocks) == 0 {
		t.Fatal("expected blocks")
	}
	t.Logf("tip block: height=%d hash=%s txcount=%d", blocks[0].Height, blocks[0].ID[:16]+"...", blocks[0].TxCount)
}

func TestEsploraScanPoolCreations(t *testing.T) {
	c := esploraClient(t)
	// The pool.example.json pool was created around block 2354483.
	// Scan a narrow range to find it.
	records, err := esplora.ScanPoolCreations(c, "", "", 2354480)
	if err != nil {
		t.Fatalf("ScanPoolCreations: %v", err)
	}
	t.Logf("found %d pool creation(s)", len(records))
	for _, r := range records {
		t.Logf("  txid=%s asset0=%s...  asset1=%s...  fee=%d/%d  height=%d",
			r.TxID[:16], r.Asset0[:16], r.Asset1[:16], r.FeeNum, r.FeeDen, r.Height)
	}
	if len(records) == 0 {
		t.Fatal("expected at least one pool creation record")
	}
}

func TestEsploraScanPoolCreationsFiltered(t *testing.T) {
	c := esploraClient(t)
	asset0 := "144c654344aa716d6f3abcc1ca90e5641e4e2a7f633bc09fe3baf64585819a49"
	records, err := esplora.ScanPoolCreations(c, asset0, "", 2354480)
	if err != nil {
		t.Fatalf("ScanPoolCreations: %v", err)
	}
	t.Logf("found %d pool(s) with asset0 filter", len(records))
	for _, r := range records {
		if r.Asset0 != asset0 {
			t.Errorf("expected asset0 %s, got %s", asset0, r.Asset0)
		}
	}
}
