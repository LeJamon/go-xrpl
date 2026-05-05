package pseudo

import (
	"encoding/hex"
	"fmt"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// ZeroAccount is the base58-encoded all-zero AccountID used as the
// source on every XRPL pseudo-transaction (rippled AccountID()). The
// wire form serializes to a 20-byte zero blob.
const ZeroAccount = "rrrrrrrrrrrrrrrrrrrrrhoLvTp"

// applyPseudoTxDefaults stamps the rippled-default values for the
// REQUIRED common fields on a pseudo-tx. Mirrors rippled's
// STTx(TxType, …) constructor at STTx.cpp:113-128, which calls
// set(format->getSOTemplate()) and inserts default values for every
// REQUIRED common field (TxFormats.cpp:32-50): zero Fee, zero
// Sequence, empty SigningPubKey.
func applyPseudoTxDefaults(c *tx.Common) {
	zeroSeq := uint32(0)
	c.Fee = "0"
	c.Sequence = &zeroSeq
	c.SigningPubKey = ""
}

// EncodePseudoTx serializes a pseudo-tx to the canonical XRPL wire
// bytes used in the consensus tx set. It stamps the rippled-default
// common fields, flattens the tx, ensures the empty SigningPubKey is
// present in the encoded blob (matching rippled's STObject::add
// behaviour at STObject.cpp:881-921 — every field with a default
// value emitted by set(SOTemplate) is serialized, including the
// empty Blob for sfSigningPubKey), and returns the binary blob.
//
// Common.ToMap omits SigningPubKey when empty, but rippled's wire
// format always carries it as VL(0) for pseudo-tx because it is a
// REQUIRED common field (TxFormats.cpp:44 → soeREQUIRED →
// STObject.cpp:165 writes a defaultObject). We re-inject it after
// Flatten so the codec emits the trailing 0x73 0x00 bytes.
//
// sfFlags is soeOPTIONAL (TxFormats.cpp:34) and the pseudo-tx
// assemblers in rippled (FeeVoteImpl::doVoting at
// FeeVoteImpl.cpp:297-319; NegativeUNLVote::addTx at
// NegativeUNLVote.cpp:110-140) never set it. Common.ToMap honors
// this by omitting Flags when c.Flags is nil, so no special
// handling is needed here — applyPseudoTxDefaults does not touch
// Flags.
func EncodePseudoTx(stx tx.Transaction) ([]byte, error) {
	applyPseudoTxDefaults(stx.GetCommon())

	flat, err := stx.Flatten()
	if err != nil {
		return nil, fmt.Errorf("flatten pseudo-tx: %w", err)
	}
	if _, ok := flat["SigningPubKey"]; !ok {
		flat["SigningPubKey"] = ""
	}

	hexStr, err := binarycodec.Encode(flat)
	if err != nil {
		return nil, fmt.Errorf("encode pseudo-tx: %w", err)
	}
	return hex.DecodeString(hexStr)
}
