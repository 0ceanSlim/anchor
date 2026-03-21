package esplora

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
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
	Height int // block height (-1 if unconfirmed)
}

// ScanPoolCreations scans blocks from startHeight to the chain tip for ANCHR
// OP_RETURN outputs. Uses the Esplora block and block-tx endpoints which are
// indexed and much faster than RPC block-by-block scanning.
//
// If asset0 or asset1 is non-empty, only records matching both assets are
// returned (case-insensitive). Pass "" to return all pools.
func ScanPoolCreations(c *Client, asset0, asset1 string, startHeight int) ([]PoolCreationRecord, error) {
	tip, err := c.Ping()
	if err != nil {
		return nil, fmt.Errorf("get tip: %w", err)
	}

	var records []PoolCreationRecord

	// Walk blocks from startHeight to tip. GetBlocks returns 10 blocks
	// descending from the given height, so we walk forward in chunks.
	for h := startHeight; h <= tip; {
		// Fetch 10 blocks starting at min(h+9, tip) (API returns descending).
		fetchHeight := min(h+9, tip)
		blocks, err := c.GetBlocks(fetchHeight)
		if err != nil {
			return nil, fmt.Errorf("get blocks at %d: %w", fetchHeight, err)
		}
		if len(blocks) == 0 {
			break
		}

		// Blocks come descending — process in ascending order.
		for i := len(blocks) - 1; i >= 0; i-- {
			blk := blocks[i]
			if blk.Height < startHeight {
				continue
			}
			if blk.Height > tip {
				continue
			}
			// Skip blocks with only the coinbase tx.
			if blk.TxCount <= 1 {
				continue
			}

			// Fetch transactions in the block in pages of 25.
			for startIdx := 0; startIdx < blk.TxCount; startIdx += 25 {
				txs, err := c.GetBlockTxs(blk.ID, startIdx)
				if err != nil {
					return nil, fmt.Errorf("block %s txs at %d: %w", blk.ID, startIdx, err)
				}
				for _, tx := range txs {
					for _, vout := range tx.Vout {
						rec, ok := ParseAnchorOutput(vout.ScriptPubKey, tx.TxID, blk.Height)
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
		}

		h = fetchHeight + 1
	}

	return records, nil
}

// ParseAnchorOutput attempts to decode an ANCHR OP_RETURN from a scriptPubKey hex.
func ParseAnchorOutput(scriptHex, txid string, height int) (PoolCreationRecord, bool) {
	if !strings.HasPrefix(strings.ToLower(scriptHex), anchorScriptPrefix) {
		return PoolCreationRecord{}, false
	}
	// Script layout: "6a49" (2 bytes) + 73-byte payload (146 hex chars) = 150 hex chars
	if len(scriptHex) < 150 {
		return PoolCreationRecord{}, false
	}
	payload, err := hex.DecodeString(scriptHex[4 : 4+146])
	if err != nil || len(payload) < 73 {
		return PoolCreationRecord{}, false
	}
	// payload[0:5]  = "ANCHR"
	// payload[5:37] = asset0 internal byte order
	// payload[37:69]= asset1 internal byte order
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
