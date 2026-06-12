package batch

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
)

// serializeBatch builds the digest signed by each BatchSigner:
//
//	HashPrefix::batch || outer flags || txid count || inner txids
//
// The result is the raw message slice; the signature scheme hashes it
// internally (SHA512-Half). Reference: rippled Batch.h serializeBatch.
func serializeBatch(flags uint32, txids [][32]byte) []byte {
	msg := make([]byte, 0, 4+4+4+len(txids)*32)
	msg = append(msg, protocol.HashPrefixBatch.Bytes()...)
	msg = binary.BigEndian.AppendUint32(msg, flags)
	msg = binary.BigEndian.AppendUint32(msg, uint32(len(txids)))
	for _, txid := range txids {
		msg = append(msg, txid[:]...)
	}
	return msg
}

// BatchSigningMessage returns the serializeBatch digest that each BatchSigner
// signs over: HashPrefix::batch || outer flags || txid count || inner txids.
// The returned bytes are the raw message; the signing scheme hashes them.
func (b *Batch) BatchSigningMessage() ([]byte, error) {
	txids, err := b.batchTransactionIDs()
	if err != nil {
		return nil, err
	}
	return serializeBatch(b.GetFlags(), txids), nil
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
		id, err := tx.ComputeTransactionHash(inner)
		if err != nil {
			return nil, ErrBatchInnerHashUncomputable
		}
		ids[i] = id
	}
	return ids, nil
}

// verifyBatchSignatures cryptographically verifies every BatchSigner signature
// over the serializeBatch digest. Each signer is single-signed (direct
// SigningPubKey + BatchTxnSignature) or multi-signed (nested Signers array,
// each over the digest suffixed with the signer's account ID). Any failure
// yields temBAD_SIGNATURE. Reference: rippled STTx::checkBatchSign,
// checkBatchSingleSign, checkBatchMultiSign.
func (b *Batch) verifyBatchSignatures() error {
	digest, err := b.BatchSigningMessage()
	if err != nil {
		return err
	}

	for i := range b.BatchSigners {
		signer := b.BatchSigners[i].BatchSigner
		if signer.SigningPubKey == "" {
			if err := verifyBatchMultiSign(digest, signer); err != nil {
				return err
			}
			continue
		}
		if err := verifyBatchSingleSign(digest, signer); err != nil {
			return err
		}
	}
	return nil
}

// verifyBatchSingleSign verifies a single-signed BatchSigner: the digest must
// validate against SigningPubKey and BatchTxnSignature. A signer that also
// carries nested Signers is signed two ways and rejected.
// Reference: rippled singleSignHelper.
func verifyBatchSingleSign(digest []byte, signer BatchSignerData) error {
	if len(signer.Signers) > 0 {
		return ErrBatchInvalidSignature
	}
	if !verifyBatchSig(digest, signer.SigningPubKey, signer.BatchTxnSignature) {
		return ErrBatchInvalidSignature
	}
	return nil
}

// verifyBatchMultiSign verifies a multi-signed BatchSigner: each nested signer
// signs the digest suffixed with its own account ID. Signers must be ordered by
// account ID, contain no duplicates, and none may be the batch-signer account.
// Reference: rippled multiSignHelper.
func verifyBatchMultiSign(digest []byte, signer BatchSignerData) error {
	if len(signer.Signers) == 0 {
		return ErrBatchInvalidSignature
	}

	batchSignerID, err := state.DecodeAccountID(signer.Account)
	if err != nil {
		return ErrBatchInvalidSignature
	}

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

		msg := make([]byte, 0, len(digest)+20)
		msg = append(msg, digest...)
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
		return ed25519.ED25519().Validate(msgStr, pubKeyHex, sigHex)
	case 0x02, 0x03:
		return secp256k1.SECP256K1().ValidateWithCanonicality(msgStr, pubKeyHex, sigHex, true)
	default:
		return false
	}
}

// validateBatchSigners mirrors the BatchSigners portion of rippled
// Batch::preflight (Batch.cpp:387-453): every BatchSigner account must be unique,
// not the outer account, and required by an inner transaction; after all signers
// are consumed the required set must be empty; finally every signature must
// verify. requiredSigners is the set of inner-tx accounts other than the outer
// account.
func (b *Batch) validateBatchSigners(requiredSigners map[string]struct{}) error {
	if len(b.BatchSigners) > MaxBatchTransactions {
		return ErrBatchTooManySigners
	}

	if len(b.BatchSigners) > 0 {
		seen := make(map[string]struct{}, len(b.BatchSigners))
		for i := range b.BatchSigners {
			acct := b.BatchSigners[i].BatchSigner.Account
			if acct == b.Account {
				return ErrBatchSignerIsOuter
			}
			if _, dup := seen[acct]; dup {
				return ErrBatchDuplicateSigner
			}
			seen[acct] = struct{}{}

			if _, required := requiredSigners[acct]; !required {
				return ErrBatchSignerNotRequired
			}
			delete(requiredSigners, acct)
		}

		if err := b.verifyBatchSignatures(); err != nil {
			return err
		}
	}

	if len(requiredSigners) != 0 {
		return ErrBatchMissingSigner
	}
	return nil
}
