// Package taproot builds Simplicity P2TR addresses for Elements/Liquid.
//
// Simplicity uses leaf version 0xBE (not 0xC4 which is standard Elements tapscript).
// The internal key is a NUMS point (key-path spend disabled).
package taproot

import (
	"encoding/hex"
	"fmt"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/txscript"
	"github.com/vulpemventures/go-elements/address"
	"github.com/vulpemventures/go-elements/network"
	"github.com/vulpemventures/go-elements/taproot"
)

// SimplicityLeafVersion is the Simplicity tapscript leaf version on Elements.
const SimplicityLeafVersion txscript.TapscriptLeafVersion = 0xBE

// numsXHex is the x-coordinate of the standard BIP341 NUMS internal key.
// lift_x(0x50929b74...) — provably has no known discrete log.
const numsXHex = "50929b74c1a04954b78b4b6035e97a5e078a5a0f28ec96d547bfee9ace803ac0"

// internalKey parses the NUMS internal key (even-y compressed form).
func internalKey() (*btcec.PublicKey, error) {
	xBytes, err := hex.DecodeString(numsXHex)
	if err != nil {
		return nil, fmt.Errorf("invalid NUMS key: %w", err)
	}
	// 0x02 prefix = even y-coordinate (lift_x convention)
	compressed := append([]byte{0x02}, xBytes...)
	return btcec.ParsePubKey(compressed)
}

// Address derives the P2TR bech32m address for a Simplicity contract.
// cmrBytes must be the 32-byte Commitment Merkle Root — this is what goes in
// the taproot leaf, NOT the full program binary.
func Address(cmrBytes []byte, net *network.Network) (string, error) {
	tweaked, err := tweakedKey(cmrBytes)
	if err != nil {
		return "", err
	}
	xOnly := tweaked.SerializeCompressed()[1:]
	return address.ToBech32(&address.Bech32{
		Prefix:  net.Bech32,
		Version: 1,
		Program: xOnly,
	})
}

// ControlBlock returns the serialised taproot control block for a Simplicity
// spend. cmrBytes must be the 32-byte CMR used as the taproot leaf script.
func ControlBlock(cmrBytes []byte) ([]byte, error) {
	inKey, err := internalKey()
	if err != nil {
		return nil, err
	}
	leaf := taproot.NewTapElementsLeaf(SimplicityLeafVersion, cmrBytes)
	tree := taproot.AssembleTaprootScriptTree(leaf)
	proof := tree.LeafMerkleProofs[0]
	cb := proof.ToControlBlock(inKey)
	return cb.ControlBlock.ToBytes()
}

// tweakedKey computes the taproot output key using the given leaf script bytes.
// For Simplicity, leafScript should be the 32-byte CMR.
func tweakedKey(leafScript []byte) (*btcec.PublicKey, error) {
	inKey, err := internalKey()
	if err != nil {
		return nil, err
	}
	leaf := taproot.NewTapElementsLeaf(SimplicityLeafVersion, leafScript)
	tree := taproot.AssembleTaprootScriptTree(leaf)
	rootHash := tree.RootNode.TapHash()
	return taproot.ComputeTaprootOutputKey(inKey, rootHash[:]), nil
}

// AddressDual derives a P2TR address for a 2-leaf Simplicity taproot tree.
// Both cmr1 and cmr2 are 32-byte Simplicity CMRs (e.g., swap and remove variants).
// The resulting UTXO can be spent using either program.
func AddressDual(cmr1, cmr2 []byte, net *network.Network) (string, error) {
	tweaked, err := tweakedKeyDual(cmr1, cmr2)
	if err != nil {
		return "", err
	}
	xOnly := tweaked.SerializeCompressed()[1:]
	return address.ToBech32(&address.Bech32{
		Prefix:  net.Bech32,
		Version: 1,
		Program: xOnly,
	})
}

// ControlBlockDual returns the control block for spending the leaf identified by
// targetCMR from a 2-leaf tree built from (cmr1, cmr2).
func ControlBlockDual(cmr1, cmr2, targetCMR []byte) ([]byte, error) {
	inKey, err := internalKey()
	if err != nil {
		return nil, err
	}
	leaf1 := taproot.NewTapElementsLeaf(SimplicityLeafVersion, cmr1)
	leaf2 := taproot.NewTapElementsLeaf(SimplicityLeafVersion, cmr2)
	tree := taproot.AssembleTaprootScriptTree(leaf1, leaf2)
	for i := range tree.LeafMerkleProofs {
		proof := &tree.LeafMerkleProofs[i]
		if string(proof.Script) == string(targetCMR) {
			cb := proof.ToControlBlock(inKey)
			return cb.ControlBlock.ToBytes()
		}
	}
	return nil, fmt.Errorf("targetCMR not found in dual-leaf tree")
}

// tweakedKeyDual computes the taproot output key for a 2-leaf tree.
func tweakedKeyDual(cmr1, cmr2 []byte) (*btcec.PublicKey, error) {
	inKey, err := internalKey()
	if err != nil {
		return nil, err
	}
	leaf1 := taproot.NewTapElementsLeaf(SimplicityLeafVersion, cmr1)
	leaf2 := taproot.NewTapElementsLeaf(SimplicityLeafVersion, cmr2)
	tree := taproot.AssembleTaprootScriptTree(leaf1, leaf2)
	rootHash := tree.RootNode.TapHash()
	return taproot.ComputeTaprootOutputKey(inKey, rootHash[:]), nil
}
