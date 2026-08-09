package batch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"sort"

	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// serializeBatch builds the common portion of the data signed by each BatchSigner:
//
//	HashPrefix::batch || outer account || sequence-or-ticket || outer flags ||
//	inner count || ordered inner txids
//
// The result is the raw message slice; the signature scheme hashes it
// internally (SHA512-Half). Reference: rippled Batch.h serializeBatch.
func serializeBatch(outerAccount [20]byte, seqValue, flags uint32, txids [][32]byte) []byte {
	msg := make([]byte, 0, 4+20+4+4+4+len(txids)*32)
	msg = append(msg, protocol.HashPrefixBatch().Bytes()...)
	msg = append(msg, outerAccount[:]...)
	msg = binary.BigEndian.AppendUint32(msg, seqValue)
	msg = binary.BigEndian.AppendUint32(msg, flags)
	msg = binary.BigEndian.AppendUint32(msg, uint32(len(txids)))
	for _, txid := range txids {
		msg = append(msg, txid[:]...)
	}
	return msg
}

// BatchSigningMessage returns the common Batch signing preimage. A single-signed
// BatchSigner appends its AccountID. A multi-signed BatchSigner appends the
// BatchSigner AccountID followed by the nested signer AccountID.
func (b *Batch) BatchSigningMessage() ([]byte, error) {
	if len(b.RawTransactions) > MaxBatchTransactions {
		return nil, ErrBatchTooManyTxns
	}
	outerAccount, err := state.DecodeAccountID(b.Account)
	if err != nil {
		return nil, ErrBatchInvalidSignature
	}
	txids, err := b.batchTransactionIDs()
	if err != nil {
		return nil, err
	}
	return serializeBatch(outerAccount, b.outerSeqValue(), b.GetFlags(), txids), nil
}

func (b *Batch) outerSeqValue() uint32 {
	if b.Sequence != nil && *b.Sequence != 0 {
		return *b.Sequence
	}
	if b.TicketSequence != nil {
		return *b.TicketSequence
	}
	return 0
}

// batchTransactionIDs returns the transaction IDs of the inner transactions in
// order, mirroring rippled STTx::getBatchTransactionIDs (each inner hashed with
// HashPrefix::transactionID).
func (b *Batch) batchTransactionIDs() ([][32]byte, error) {
	ids := make([][32]byte, len(b.RawTransactions))
	for i, rt := range b.RawTransactions {
		inner := rt.RawTransaction.InnerTx
		if inner == nil {
			return nil, ErrBatchNilInnerTx
		}
		matches, err := tx.CurrentFieldsMatchRaw(inner)
		if err != nil || !matches {
			return nil, ErrBatchInnerHashUncomputable
		}
		id, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			return nil, ErrBatchInnerHashUncomputable
		}
		ids[i] = id
	}
	return ids, nil
}

// VerifyBatchSignatures cryptographically verifies every BatchSigner signature
// over the serializeBatch digest. Each signer is single-signed (direct
// SigningPubKey + BatchTxnSignature) or multi-signed (nested Signers array,
// each over the digest suffixed with the signer's account ID). Any failure
// yields an invalid-signature error. The engine calls this from its signature-
// verification stage so it is skipped under SkipSignatureVerification; exact
// signer coverage is checked afterward in PreflightSigValidated.
// Reference: rippled STTx::checkBatchSign, checkBatchSingleSign, checkBatchMultiSign.
func (b *Batch) VerifyBatchSignatures() error {
	if len(b.BatchSigners) > MaxBatchSigners {
		return errors.New("BatchSigners array exceeds max entries.")
	}
	if err := b.validateBatchSignerBounds(); err != nil {
		return err
	}
	message, err := b.BatchSigningMessage()
	if err != nil {
		return err
	}

	for i := range b.BatchSigners {
		signer := b.BatchSigners[i].BatchSigner
		if signer.SigningPubKey == "" {
			if err := verifyBatchMultiSign(message, signer); err != nil {
				return err
			}
			continue
		}
		if err := verifyBatchSingleSign(message, signer); err != nil {
			return err
		}
	}
	return nil
}

// verifyBatchSingleSign verifies a single-signed BatchSigner: the digest must
// validate against SigningPubKey and BatchTxnSignature. A signer that also
// carries nested Signers is signed two ways and rejected.
// Reference: rippled singleSignHelper.
func verifyBatchSingleSign(message []byte, signer BatchSignerData) error {
	if signer.hasSigners() {
		return ErrBatchInvalidSignature
	}
	signerID, err := state.DecodeAccountID(signer.Account)
	if err != nil {
		return ErrBatchInvalidSignature
	}
	signingData := make([]byte, 0, len(message)+len(signerID))
	signingData = append(signingData, message...)
	signingData = append(signingData, signerID[:]...)
	if !verifyBatchSig(signingData, signer.SigningPubKey, signer.BatchTxnSignature) {
		return ErrBatchInvalidSignature
	}
	return nil
}

// verifyBatchMultiSign verifies a multi-signed BatchSigner: each nested signer
// signs the digest suffixed with its own account ID. Signers must be ordered by
// account ID, contain no duplicates, and none may be the batch-signer account.
// Reference: rippled multiSignHelper.
func verifyBatchMultiSign(message []byte, signer BatchSignerData) error {
	if len(signer.Signers) == 0 {
		return ErrBatchInvalidSignature
	}
	// A multi-signed BatchSigner carries its signatures in the nested Signers
	// array; a direct BatchTxnSignature alongside it would mean the entry is
	// signed two ways, which is rejected.
	if signer.hasTxnSignature() {
		return ErrBatchInvalidSignature
	}

	batchSignerID, err := state.DecodeAccountID(signer.Account)
	if err != nil {
		return ErrBatchInvalidSignature
	}

	dataStart := make([]byte, 0, len(message)+20)
	dataStart = append(dataStart, message...)
	dataStart = append(dataStart, batchSignerID[:]...)

	var lastID [20]byte
	first := true
	for _, sw := range signer.Signers {
		nested := sw.Signer
		nestedID, decErr := state.DecodeAccountID(nested.Account)
		if decErr != nil {
			return ErrBatchInvalidSignature
		}
		if nestedID == batchSignerID {
			return ErrBatchInvalidSignature
		}
		// Nested signers must be strictly increasing by account ID — this rejects
		// both unsorted and duplicate signers.
		if !first && bytes.Compare(lastID[:], nestedID[:]) >= 0 {
			return ErrBatchInvalidSignature
		}
		lastID = nestedID
		first = false

		msg := make([]byte, 0, len(dataStart)+20)
		msg = append(msg, dataStart...)
		msg = append(msg, nestedID[:]...)
		if !verifyBatchSig(msg, nested.SigningPubKey, nested.TxnSignature) {
			return ErrBatchInvalidSignature
		}
	}
	return nil
}

// verifyBatchSig validates a signature over msg using the given public key,
// dispatching on the key-type byte. RequireFullyCanonicalSig::yes is always in
// effect for batch signatures. Reference: rippled verify() with fullyCanonical.
func verifyBatchSig(msg []byte, pubKeyHex, sigHex string) bool {
	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil || len(pubKeyBytes) == 0 {
		return false
	}
	msgStr := string(msg)
	switch pubKeyBytes[0] {
	case 0xED:
		return ed25519.Algorithm{}.Validate(msgStr, pubKeyHex, sigHex)
	case 0x02, 0x03:
		return secp256k1.Algorithm{}.ValidateWithCanonicality(msgStr, pubKeyHex, sigHex, true)
	default:
		return false
	}
}

// validateBatchSigners mirrors the BatchSigners checks of rippled
// Batch::preflightSigValidated: every BatchSigner account must be unique,
// not the outer account, and required by an inner transaction; after all signers
// are consumed the required set must be empty. requiredSigners is the set of
// inner-tx accounts other than the outer account. The cryptographic verification
// of each signature lives in VerifyBatchSignatures, which the engine runs first.
func (b *Batch) validateBatchSigners(requiredSigners map[string]struct{}) error {
	if len(b.BatchSigners) > MaxBatchSigners {
		return ErrBatchTooManySigners
	}

	if len(b.BatchSigners) > 0 {
		outerID, err := state.DecodeAccountID(b.Account)
		if err != nil {
			return ErrBatchSignerIsOuter
		}
		requiredIDs := make([][20]byte, 0, len(requiredSigners))
		for account := range requiredSigners {
			id, err := state.DecodeAccountID(account)
			if err != nil {
				return ErrBatchMissingSigner
			}
			requiredIDs = append(requiredIDs, id)
		}
		sort.Slice(requiredIDs, func(i, j int) bool {
			return bytes.Compare(requiredIDs[i][:], requiredIDs[j][:]) < 0
		})

		signerIDs := make([][20]byte, len(b.BatchSigners))
		var lastID [20]byte
		for i := range b.BatchSigners {
			acct := b.BatchSigners[i].BatchSigner.Account
			accountID, err := state.DecodeAccountID(acct)
			if err != nil {
				return ErrBatchSignerNotRequired
			}
			if accountID == outerID {
				return ErrBatchSignerIsOuter
			}
			if i > 0 {
				switch bytes.Compare(lastID[:], accountID[:]) {
				case 0:
					return ErrBatchDuplicateSigner
				case 1:
					return ErrBatchUnsortedSigner
				}
			}
			lastID = accountID
			signerIDs[i] = accountID
		}
		for i, accountID := range signerIDs {
			if i >= len(requiredIDs) || accountID != requiredIDs[i] {
				return ErrBatchSignerNotRequired
			}
		}

		// Structural "signed two ways" check, mirroring checkBatchSign's presence
		// rules: a single-signed BatchSigner (SigningPubKey present) must not also
		// carry a nested Signers array, and a multi-signed one (no SigningPubKey)
		// must carry Signers and no direct BatchTxnSignature. This runs
		// unconditionally; the cryptographic verification lives in
		// VerifyBatchSignatures (the engine signature stage).
		// Reference: rippled singleSignHelper / multiSignHelper.
		for i := range b.BatchSigners {
			signer := b.BatchSigners[i].BatchSigner
			if signer.SigningPubKey == "" {
				if len(signer.Signers) == 0 || signer.hasTxnSignature() {
					return ErrBatchInvalidSignature
				}
			} else if signer.hasSigners() {
				return ErrBatchInvalidSignature
			}
		}
	}

	if len(b.BatchSigners) != len(requiredSigners) {
		return ErrBatchMissingSigner
	}
	return nil
}
