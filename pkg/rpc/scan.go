package rpc

import (
	"encoding/binary"
	"encoding/hex"
	"strings"
)

// anchorScriptPrefix is the hex prefix of an ANCHR OP_RETURN script:
// OP_RETURN (6a) + PUSH73 (49) + "ANCHR" (414e434852)
const anchorScriptPrefix = "6a49414e434852"

// PoolCreationRecord holds parameters decoded from an ANCHR OP_RETURN output.
type PoolCreationRecord struct {
	TxID   string // creation transaction ID
	Asset0 string // display hex (reversed from internal)
	Asset1 string // display hex
	FeeNum uint16
	FeeDen uint16
	Height int
}

// ScanPoolCreations scans blocks from startBlock to the chain tip for ANCHR
// OP_RETURN outputs. If asset0 or asset1 is non-empty, only records matching
// both assets are returned (case-insensitive). Pass "" to return all pools.
//
// Note: this calls getblockhash + getblock for every block in the range.
// Use a narrow startBlock range on long chains to avoid slow scans.
func ScanPoolCreations(client *Client, asset0, asset1 string, startBlock int) ([]PoolCreationRecord, error) {
	tip, err := client.GetBlockCount()
	if err != nil {
		return nil, err
	}

	var records []PoolCreationRecord
	for h := startBlock; h <= tip; h++ {
		hash, err := client.GetBlockHash(h)
		if err != nil {
			return nil, err
		}
		txs, err := client.GetBlockTxs(hash)
		if err != nil {
			return nil, err
		}
		for _, tx := range txs {
			for _, vout := range tx.Vout {
				rec, ok := parseAnchorOutput(vout.ScriptPubKey.Hex, tx.TxID, h)
				if !ok {
					continue
				}
				if asset0 != "" && !strings.EqualFold(rec.Asset0, asset0) {
					continue
				}
				if asset1 != "" && !strings.EqualFold(rec.Asset1, asset1) {
					continue
				}
				records = append(records, rec)
			}
		}
	}
	return records, nil
}

// parseAnchorOutput attempts to decode an ANCHR OP_RETURN from a scriptPubKey hex.
// Returns (record, true) on success, (zero, false) if the script is not an ANCHR output.
func parseAnchorOutput(scriptHex, txid string, height int) (PoolCreationRecord, bool) {
	if !strings.HasPrefix(strings.ToLower(scriptHex), anchorScriptPrefix) {
		return PoolCreationRecord{}, false
	}
	// Script layout: "6a49" (2 bytes) + 73-byte payload (146 hex chars) = 150 hex chars
	if len(scriptHex) < 150 {
		return PoolCreationRecord{}, false
	}
	// Decode the 73-byte payload (skip leading "6a49")
	payload, err := hex.DecodeString(scriptHex[4 : 4+146]) // 4 = len("6a49"), 146 = 73*2
	if err != nil || len(payload) < 73 {
		return PoolCreationRecord{}, false
	}
	// payload[0:5]  = "ANCHR" (already confirmed via prefix)
	// payload[5:37] = asset0 internal byte order (32 bytes)
	// payload[37:69]= asset1 internal byte order (32 bytes)
	// payload[69:71]= feeNum uint16 big-endian
	// payload[71:73]= feeDen uint16 big-endian
	feeNum := binary.BigEndian.Uint16(payload[69:71])
	feeDen := binary.BigEndian.Uint16(payload[71:73])

	a0display := hex.EncodeToString(reverseSlice(payload[5:37]))
	a1display := hex.EncodeToString(reverseSlice(payload[37:69]))

	return PoolCreationRecord{
		TxID:   txid,
		Asset0: a0display,
		Asset1: a1display,
		FeeNum: feeNum,
		FeeDen: feeDen,
		Height: height,
	}, true
}

func reverseSlice(b []byte) []byte {
	r := make([]byte, len(b))
	for i, v := range b {
		r[len(b)-1-i] = v
	}
	return r
}
