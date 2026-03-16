package tx

import (
	"encoding/hex"
	"fmt"

	"github.com/0ceanslim/anchor/pkg/compiler"
	"github.com/0ceanslim/anchor/pkg/pool"
	anchorTaproot "github.com/0ceanslim/anchor/pkg/taproot"
	"github.com/vulpemventures/go-elements/address"
	"github.com/vulpemventures/go-elements/elementsutil"
	"github.com/vulpemventures/go-elements/network"
	"github.com/vulpemventures/go-elements/transaction"
)

// CreatePoolParams holds inputs for the create-pool transaction.
type CreatePoolParams struct {
	// pool_creation UTXO — provides LP issuance anchor + fee
	CreationTxID   string
	CreationVout   uint32
	CreationAmount uint64 // L-BTC sats in the creation UTXO

	// Deposit UTXOs — user wallet UTXOs providing the initial reserves
	Asset0TxID   string
	Asset0Vout   uint32
	Asset0Amount uint64 // total amount in this UTXO (deposit0 + any change)

	Asset1TxID   string
	Asset1Vout   uint32
	Asset1Amount uint64 // total amount in this UTXO (deposit1 + any change)

	BuildDir  string // path to directory containing .shl/.args files
	Deposit0  uint64 // satoshis of Asset0 to lock in pool_a
	Deposit1  uint64 // satoshis of Asset1 to lock in pool_b
	Asset0    string // hex asset ID (display format)
	Asset1    string
	LBTCAsset string // L-BTC asset ID
	Fee       uint64 // satoshis fee (paid from creation UTXO)
	FeeNum    uint64 // protocol fee numerator (for OP_RETURN announcement)
	FeeDen    uint64 // protocol fee denominator (for OP_RETURN announcement)
	Announce  bool   // include OP_RETURN pool announcement output

	ChangeAddr string // address to receive Asset0/Asset1/L-BTC change
	Network    *network.Network
}

// CreatePoolResult holds the completed create-pool outputs.
type CreatePoolResult struct {
	TxHex             string
	LPAssetID         string // 32-byte hex
	LPMinted          uint64
	PoolConfig        *pool.Config
	SimplicityWitness [][]byte // witness for input[0]; attach after wallet signing
}

// ComputeIssuanceEntropy computes the issuance entropy for a UTXO outpoint.
func ComputeIssuanceEntropy(txidHex string, vout uint32) ([]byte, error) {
	txidBytes, err := elementsutil.TxIDToBytes(txidHex)
	if err != nil {
		return nil, fmt.Errorf("decode txid: %w", err)
	}
	contractHash := make([]byte, 32)
	return transaction.ComputeEntropy(txidBytes, vout, contractHash)
}

// ComputeLPAssetID computes the LP asset ID from the creation UTXO outpoint.
// Elements deterministic issuance with 32-zero contract hash.
func ComputeLPAssetID(txidHex string, vout uint32) ([32]byte, error) {
	entropy, err := ComputeIssuanceEntropy(txidHex, vout)
	if err != nil {
		return [32]byte{}, fmt.Errorf("entropy: %w", err)
	}
	assetBytes, err := transaction.ComputeAsset(entropy)
	if err != nil {
		return [32]byte{}, fmt.Errorf("asset: %w", err)
	}
	var id [32]byte
	copy(id[:], assetBytes)
	return id, nil
}

// BuildCreatePool builds the create-pool transaction.
//
// Transaction structure:
//
//	Inputs:  [0] pool_creation UTXO (Simplicity spend, LP issuance attached)
//	         [1] Asset0 UTXO (wallet-signed)
//	         [2] Asset1 UTXO (wallet-signed)
//	Outputs: [0] pool_a              ← deposit0 of Asset0
//	         [1] pool_b              ← deposit1 of Asset1
//	         [2] lp_reserve          ← (LP_PREMINT - lp_minted) LP tokens
//	         [3] LP tokens           → change address (lp_minted, from issuance)
//	         [4] Asset0 change (if any)
//	         [5] Asset1 change (if any)
//	         [6] L-BTC change (if any)
//	         [7] OP_RETURN announcement (if Announce)
//	         [last] fee (explicit, empty script)
func BuildCreatePool(params *CreatePoolParams, cfg *pool.Config) (*CreatePoolResult, error) {
	if params.Asset0Amount < params.Deposit0 {
		return nil, fmt.Errorf("asset0 UTXO has %d sats but deposit0 requires %d", params.Asset0Amount, params.Deposit0)
	}
	if params.Asset1Amount < params.Deposit1 {
		return nil, fmt.Errorf("asset1 UTXO has %d sats but deposit1 requires %d", params.Asset1Amount, params.Deposit1)
	}

	lpAssetID, err := ComputeLPAssetID(params.CreationTxID, params.CreationVout)
	if err != nil {
		return nil, fmt.Errorf("LP asset ID: %w", err)
	}

	lpMinted := pool.IntSqrt(params.Deposit0 * params.Deposit1)
	if lpMinted == 0 {
		return nil, fmt.Errorf("lp_minted is 0 — deposits too small")
	}

	if params.CreationAmount < params.Fee {
		return nil, fmt.Errorf("creation UTXO has %d L-BTC sats but need at least %d for fee",
			params.CreationAmount, params.Fee)
	}

	updatedCfg, err := recompileWithLPAsset(params.BuildDir, lpAssetID, params.Network)
	if err != nil {
		return nil, fmt.Errorf("recompile: %w", err)
	}
	updatedCfg.PoolCreation = cfg.PoolCreation

	creationBinary, err := hex.DecodeString(cfg.PoolCreation.BinaryHex)
	if err != nil {
		return nil, fmt.Errorf("decode creation binary: %w", err)
	}

	tx := transaction.NewTx(2)

	// ── Inputs ────────────────────────────────────────────────────────────────

	// Input[0]: pool_creation UTXO — LP issuance attached here
	creationTxidBytes, err := elementsutil.TxIDToBytes(params.CreationTxID)
	if err != nil {
		return nil, err
	}
	creationInput := transaction.NewTxInput(creationTxidBytes, params.CreationVout)
	issuance, err := buildLPIssuance(creationTxidBytes, params.CreationVout)
	if err != nil {
		return nil, fmt.Errorf("LP issuance: %w", err)
	}
	creationInput.Issuance = &issuance.TxIssuance
	tx.AddInput(creationInput)

	// Input[1]: Asset0 deposit UTXO (wallet-signed, no witness set here)
	asset0TxidBytes, err := elementsutil.TxIDToBytes(params.Asset0TxID)
	if err != nil {
		return nil, fmt.Errorf("asset0 txid: %w", err)
	}
	tx.AddInput(transaction.NewTxInput(asset0TxidBytes, params.Asset0Vout))

	// Input[2]: Asset1 deposit UTXO (wallet-signed, no witness set here)
	asset1TxidBytes, err := elementsutil.TxIDToBytes(params.Asset1TxID)
	if err != nil {
		return nil, fmt.Errorf("asset1 txid: %w", err)
	}
	tx.AddInput(transaction.NewTxInput(asset1TxidBytes, params.Asset1Vout))

	// ── Outputs ───────────────────────────────────────────────────────────────

	// Output[0]: pool_a — deposit0 of Asset0
	out0, err := buildOutput(updatedCfg.PoolA.Address, params.Asset0, params.Deposit0)
	if err != nil {
		return nil, fmt.Errorf("pool_a output: %w", err)
	}
	tx.AddOutput(out0)

	// Output[1]: pool_b — deposit1 of Asset1
	out1, err := buildOutput(updatedCfg.PoolB.Address, params.Asset1, params.Deposit1)
	if err != nil {
		return nil, fmt.Errorf("pool_b output: %w", err)
	}
	tx.AddOutput(out1)

	// Output[2]: lp_reserve — (LP_PREMINT - lp_minted) LP tokens
	lpAssetHex := hex.EncodeToString(reverseBytes(lpAssetID[:]))
	lpReserveAmount := pool.LPPremint - lpMinted
	out2, err := buildOutput(updatedCfg.LpReserve.Address, lpAssetHex, lpReserveAmount)
	if err != nil {
		return nil, fmt.Errorf("lp_reserve output: %w", err)
	}
	tx.AddOutput(out2)

	// Output[3]: LP tokens → change address (lp_minted from issuance)
	lpAssetBytes, err := elementsutil.AssetHashToBytes(lpAssetHex)
	if err != nil {
		return nil, fmt.Errorf("LP asset bytes: %w", err)
	}
	lpValueBytes, err := elementsutil.ValueToBytes(lpMinted)
	if err != nil {
		return nil, fmt.Errorf("LP value bytes: %w", err)
	}
	changeScript, err := address.ToOutputScript(params.ChangeAddr)
	if err != nil {
		return nil, fmt.Errorf("change address: %w", err)
	}
	tx.AddOutput(transaction.NewTxOutput(lpAssetBytes, lpValueBytes, changeScript))

	// Output[4]: Asset0 change (if any)
	if change0 := params.Asset0Amount - params.Deposit0; change0 > 0 {
		chg, err := buildOutput(params.ChangeAddr, params.Asset0, change0)
		if err != nil {
			return nil, fmt.Errorf("asset0 change output: %w", err)
		}
		tx.AddOutput(chg)
	}

	// Output[5]: Asset1 change (if any)
	if change1 := params.Asset1Amount - params.Deposit1; change1 > 0 {
		chg, err := buildOutput(params.ChangeAddr, params.Asset1, change1)
		if err != nil {
			return nil, fmt.Errorf("asset1 change output: %w", err)
		}
		tx.AddOutput(chg)
	}

	// Output[6]: L-BTC change from creation UTXO (if any)
	if lbtcChange := params.CreationAmount - params.Fee; lbtcChange > 0 {
		chg, err := buildOutput(params.ChangeAddr, params.LBTCAsset, lbtcChange)
		if err != nil {
			return nil, fmt.Errorf("lbtc change output: %w", err)
		}
		tx.AddOutput(chg)
	}

	// Output[7]: OP_RETURN pool announcement (if Announce)
	if params.Announce {
		annOut, err := buildAnchorAnnouncement(params.Asset0, params.Asset1, params.FeeNum, params.FeeDen, params.LBTCAsset)
		if err != nil {
			return nil, fmt.Errorf("announcement output: %w", err)
		}
		tx.AddOutput(annOut)
	}

	// Output[last]: fee (explicit, empty script) — must be last
	feeOut, err := buildFeeOutput(params.LBTCAsset, params.Fee)
	if err != nil {
		return nil, fmt.Errorf("fee output: %w", err)
	}
	tx.AddOutput(feeOut)

	// Build Simplicity witness for input[0] but do NOT attach yet.
	witness, err := lpMintedWitness(hex.EncodeToString(creationBinary), cfg.PoolCreation.CMR, lpMinted)
	if err != nil {
		return nil, fmt.Errorf("witness: %w", err)
	}

	txBytes, err := tx.Serialize()
	if err != nil {
		return nil, fmt.Errorf("serialize: %w", err)
	}

	// Store asset IDs and fee params in the config for future commands
	updatedCfg.Asset0 = params.Asset0
	updatedCfg.Asset1 = params.Asset1
	updatedCfg.LPAssetID = lpAssetHex
	updatedCfg.FeeNum = cfg.FeeNum
	updatedCfg.FeeDen = cfg.FeeDen

	return &CreatePoolResult{
		TxHex:             hex.EncodeToString(txBytes),
		LPAssetID:         lpAssetHex,
		LPMinted:          lpMinted,
		PoolConfig:        updatedCfg,
		SimplicityWitness: witness,
	}, nil
}

// recompileWithLPAsset patches LP_ASSET_ID into all pool_a/b/lp_reserve variants,
// recompiles them, builds dual-leaf taproot addresses, and returns the updated config.
func recompileWithLPAsset(buildDir string, lpAssetID [32]byte, net *network.Network) (*pool.Config, error) {
	type pairEntry struct {
		name       string
		swapFile   string
		removeFile string
		cfgAddr    *pool.ContractInfo
		cfgSwap    *pool.PoolVariant
		cfgRemove  *pool.PoolVariant
	}
	cfg := &pool.Config{}
	pairs := []pairEntry{
		{
			"pool_a",
			buildDir + "/pool_a_swap.shl",
			buildDir + "/pool_a_remove.shl",
			&cfg.PoolA,
			&cfg.PoolASwap,
			&cfg.PoolARemove,
		},
		{
			"pool_b",
			buildDir + "/pool_b_swap.shl",
			buildDir + "/pool_b_remove.shl",
			&cfg.PoolB,
			&cfg.PoolBSwap,
			&cfg.PoolBRemove,
		},
		{
			"lp_reserve",
			buildDir + "/lp_reserve_add.shl",
			buildDir + "/lp_reserve_remove.shl",
			&cfg.LpReserve,
			&cfg.LpReserveAdd,
			&cfg.LpReserveRemove,
		},
	}
	for _, p := range pairs {
		// Patch LP_ASSET_ID into both variant .args files
		if err := compiler.PatchLPAssetID(p.swapFile, p.swapFile, lpAssetID); err != nil {
			return nil, fmt.Errorf("patch %s swap: %w", p.name, err)
		}
		if err := compiler.PatchLPAssetID(p.removeFile, p.removeFile, lpAssetID); err != nil {
			return nil, fmt.Errorf("patch %s remove: %w", p.name, err)
		}

		swapBinary, swapCMR, err := compiler.Compile(p.swapFile)
		if err != nil {
			return nil, fmt.Errorf("compile %s_swap: %w", p.name, err)
		}
		removeBinary, removeCMR, err := compiler.Compile(p.removeFile)
		if err != nil {
			return nil, fmt.Errorf("compile %s_remove: %w", p.name, err)
		}

		addr, err := anchorTaproot.AddressDual(swapCMR[:], removeCMR[:], net)
		if err != nil {
			return nil, fmt.Errorf("dual address %s: %w", p.name, err)
		}

		swapCB, err := anchorTaproot.ControlBlockDual(swapCMR[:], removeCMR[:], swapCMR[:])
		if err != nil {
			return nil, fmt.Errorf("control block %s swap: %w", p.name, err)
		}
		removeCB, err := anchorTaproot.ControlBlockDual(swapCMR[:], removeCMR[:], removeCMR[:])
		if err != nil {
			return nil, fmt.Errorf("control block %s remove: %w", p.name, err)
		}

		*p.cfgAddr = pool.ContractInfo{Address: addr}
		*p.cfgSwap = pool.PoolVariant{
			CMR:          hex.EncodeToString(swapCMR[:]),
			BinaryHex:    hex.EncodeToString(swapBinary),
			ControlBlock: hex.EncodeToString(swapCB),
		}
		*p.cfgRemove = pool.PoolVariant{
			CMR:          hex.EncodeToString(removeCMR[:]),
			BinaryHex:    hex.EncodeToString(removeBinary),
			ControlBlock: hex.EncodeToString(removeCB),
		}
	}
	return cfg, nil
}

func buildOutput(addr, assetIDHex string, value uint64) (*transaction.TxOutput, error) {
	script, err := address.ToOutputScript(addr)
	if err != nil {
		return nil, fmt.Errorf("addr %s: %w", addr, err)
	}
	assetBytes, err := elementsutil.AssetHashToBytes(assetIDHex)
	if err != nil {
		return nil, err
	}
	valueBytes, err := elementsutil.ValueToBytes(value)
	if err != nil {
		return nil, err
	}
	return transaction.NewTxOutput(assetBytes, valueBytes, script), nil
}

// buildAnchorAnnouncement creates the OP_RETURN pool discovery output.
//
// Payload format (73 bytes):
//
//	[5]  "ANCHR" magic
//	[32] asset0 internal byte order (reversed display hex)
//	[32] asset1 internal byte order
//	[2]  feeNum uint16 big-endian
//	[2]  feeDen uint16 big-endian
func buildAnchorAnnouncement(asset0Hex, asset1Hex string, feeNum, feeDen uint64, lbtcAsset string) (*transaction.TxOutput, error) {
	a0bytes, err := hex.DecodeString(asset0Hex)
	if err != nil {
		return nil, fmt.Errorf("decode asset0: %w", err)
	}
	a1bytes, err := hex.DecodeString(asset1Hex)
	if err != nil {
		return nil, fmt.Errorf("decode asset1: %w", err)
	}

	var payload [73]byte
	copy(payload[0:5], []byte("ANCHR"))
	copy(payload[5:37], reverseBytes(a0bytes))
	copy(payload[37:69], reverseBytes(a1bytes))
	payload[69] = byte(feeNum >> 8)
	payload[70] = byte(feeNum)
	payload[71] = byte(feeDen >> 8)
	payload[72] = byte(feeDen)

	// OP_RETURN (0x6a) + push 73 bytes (0x49) + payload
	script := make([]byte, 75)
	script[0] = 0x6a
	script[1] = 0x49
	copy(script[2:], payload[:])

	assetBytes, err := elementsutil.AssetHashToBytes(lbtcAsset)
	if err != nil {
		return nil, fmt.Errorf("asset bytes: %w", err)
	}
	valueBytes, err := elementsutil.ValueToBytes(0)
	if err != nil {
		return nil, fmt.Errorf("value bytes: %w", err)
	}
	return transaction.NewTxOutput(assetBytes, valueBytes, script), nil
}

// buildBurnOutput creates an OP_RETURN output that permanently destroys the given
// amount of assetIDHex. Elements treats IsUnspendable() outputs as having dust
// threshold 0, so any explicit amount is accepted.
func buildBurnOutput(assetIDHex string, amount uint64) (*transaction.TxOutput, error) {
	assetBytes, err := elementsutil.AssetHashToBytes(assetIDHex)
	if err != nil {
		return nil, err
	}
	valueBytes, err := elementsutil.ValueToBytes(amount)
	if err != nil {
		return nil, err
	}
	return transaction.NewTxOutput(assetBytes, valueBytes, []byte{0x6a}), nil // OP_RETURN
}

func buildFeeOutput(lbtcAsset string, fee uint64) (*transaction.TxOutput, error) {
	assetBytes, err := elementsutil.AssetHashToBytes(lbtcAsset)
	if err != nil {
		return nil, err
	}
	valueBytes, err := elementsutil.ValueToBytes(fee)
	if err != nil {
		return nil, err
	}
	return transaction.NewTxOutput(assetBytes, valueBytes, []byte{}), nil
}

func buildLPIssuance(txidBytes []byte, vout uint32) (*transaction.TxIssuanceExtended, error) {
	// Mint LP_PREMINT LP tokens, no reissuance token (tokenAmount=0).
	issuance, err := transaction.NewTxIssuance(pool.LPPremint, 0, 8, nil)
	if err != nil {
		return nil, err
	}
	// Elements wire format: for a NEW issuance, AssetEntropy must be the
	// contract hash (32 zero bytes), NOT the computed entropy.
	issuance.TxIssuance.AssetEntropy = make([]byte, 32)
	return issuance, nil
}

func reverseBytes(b []byte) []byte {
	r := make([]byte, len(b))
	for i, v := range b {
		r[len(b)-1-i] = v
	}
	return r
}
