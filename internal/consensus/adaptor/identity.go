package adaptor

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/protocol"
)

var (
	ErrNoValidatorKey = errors.New("no validator key configured")
	ErrInvalidSeed    = errors.New("invalid validator seed")
)

// ValidatorIdentity holds the validator's signing keys.
// If nil or empty, the node operates as a non-validator (observer).
type ValidatorIdentity struct {
	// PublicKey is the compressed secp256k1 public key (33 bytes).
	PublicKey []byte
	// PrivateKey is the hex-encoded private key (for signing).
	PrivateKey string
	// NodeID is the consensus NodeID derived from the public key.
	NodeID consensus.NodeID
}

// NewValidatorIdentity creates a ValidatorIdentity from a seed string.
// The seed can be in base58 (sXXX...) format.
// Uses secp256k1 with validator=true derivation, matching rippled.
func NewValidatorIdentity(seed string) (*ValidatorIdentity, error) {
	if seed == "" {
		return nil, nil // not a validator
	}

	// Decode the seed from base58
	decodedSeed, _, err := addresscodec.DecodeSeed(seed)
	if err != nil {
		return nil, ErrInvalidSeed
	}

	// Derive validator keypair using secp256k1 (validator=true uses root generator directly)
	algo := secp256k1.SECP256K1()
	privKeyHex, pubKeyHex, err := algo.DeriveKeypair(decodedSeed, true)
	if err != nil {
		return nil, err
	}

	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return nil, err
	}

	var nodeID consensus.NodeID
	copy(nodeID[:], pubKeyBytes)

	return &ValidatorIdentity{
		PublicKey:  pubKeyBytes,
		PrivateKey: privKeyHex,
		NodeID:     nodeID,
	}, nil
}

// Sign signs a pre-computed digest with the validator's private key using secp256k1.
// The data parameter must be a SHA-512Half digest (32 bytes).
// Matches rippled's signDigest() which passes the hash directly to secp256k1.
func (vi *ValidatorIdentity) Sign(data []byte) ([]byte, error) {
	if vi == nil {
		return nil, ErrNoValidatorKey
	}
	algo := secp256k1.SECP256K1()
	var digest [32]byte
	copy(digest[:], data)
	return algo.SignDigest(digest, vi.PrivateKey)
}

// Verify verifies a signature against a public key.
// The data parameter must be a pre-computed SHA-512Half digest (32 bytes).
// Matches rippled's verifyDigest() which passes the hash directly to
// secp256k1_ecdsa_verify without re-hashing.
func Verify(pubKey []byte, data []byte, signature []byte) bool {
	algo := secp256k1.SECP256K1()
	var digest [32]byte
	copy(digest[:], data)
	return algo.ValidateDigest(digest, pubKey, signature)
}

// SignProposal signs a consensus proposal.
// The signed data is SHA-512Half(HashPrefixProposal + serialized proposal fields).
// Matches rippled's Proposal signing format.
func (vi *ValidatorIdentity) SignProposal(proposal *consensus.Proposal) error {
	if vi == nil {
		return ErrNoValidatorKey
	}
	data := buildProposalSigningData(proposal)
	sig, err := vi.Sign(data)
	if err != nil {
		return err
	}
	proposal.Signature = sig
	return nil
}

// VerifyProposal verifies a proposal's signature.
func VerifyProposal(proposal *consensus.Proposal) error {
	data := buildProposalSigningData(proposal)
	if !Verify(proposal.NodeID[:], data, proposal.Signature) {
		return errors.New("invalid proposal signature")
	}
	return nil
}

// SignValidation signs a consensus validation.
// The signed data is SHA-512Half(HashPrefixValidation + serialized validation fields).
// Matches rippled's STValidation signing format.
func (vi *ValidatorIdentity) SignValidation(validation *consensus.Validation) error {
	if vi == nil {
		return ErrNoValidatorKey
	}
	data := buildValidationSigningData(validation)
	sig, err := vi.Sign(data)
	if err != nil {
		return err
	}
	validation.Signature = sig
	return nil
}

// VerifyValidation verifies a validation's signature.
func VerifyValidation(validation *consensus.Validation) error {
	data := buildValidationSigningData(validation)
	if !Verify(validation.NodeID[:], data, validation.Signature) {
		return errors.New("invalid validation signature")
	}
	return nil
}

// buildProposalSigningData constructs the data to be signed for a proposal.
// Format: HashPrefixProposal + ProposeSeq(4) + CloseTime(4) + PreviousLedger(32) + TxSet(32)
func buildProposalSigningData(p *consensus.Proposal) []byte {
	var buf []byte
	buf = append(buf, protocol.HashPrefixProposal[:]...)

	// ProposeSeq (4 bytes, big-endian)
	buf = append(buf, byte(p.Position>>24), byte(p.Position>>16), byte(p.Position>>8), byte(p.Position))

	// CloseTime as XRPL epoch seconds (4 bytes, big-endian)
	closeTimeSec := uint32(p.CloseTime.Unix() - protocol.RippleEpochUnix)
	buf = append(buf, byte(closeTimeSec>>24), byte(closeTimeSec>>16), byte(closeTimeSec>>8), byte(closeTimeSec))

	// PreviousLedger (32 bytes)
	buf = append(buf, p.PreviousLedger[:]...)

	// TxSet (32 bytes)
	buf = append(buf, p.TxSet[:]...)

	hash := common.Sha512Half(buf)
	return hash[:]
}

// buildValidationSigningData constructs the signing digest for a validation.
//
// For inbound validations (SigningData populated by parseSTValidation), the
// exact non-signing bytes from the wire are used — including any optional
// fields the sender included that we don't model explicitly. That is what
// makes us compatible with rippled emitting fields we don't ourselves
// understand.
//
// For outbound validations (SigningData nil), we regenerate the preimage
// from struct fields. It MUST stay byte-identical to what
// SerializeSTValidation emits (minus sfSignature); otherwise a freshly-
// signed validation would fail verification when parsed back from the
// wire. When extending the wire format, update both functions together.
func buildValidationSigningData(v *consensus.Validation) []byte {
	if len(v.SigningData) > 0 {
		// Inbound: use the exact non-signing bytes from the wire.
		hash := common.Sha512Half(protocol.HashPrefixValidation[:], v.SigningData)
		return hash[:]
	}

	// Outbound: rebuild from struct fields in canonical field order,
	// matching SerializeSTValidation byte-for-byte.
	var buf []byte
	buf = append(buf, protocol.HashPrefixValidation[:]...)

	// sfFlags (type 2, field 2). Canonical-sig flag always set on
	// outbound — must match SerializeSTValidation.
	flags := uint32(vfFullyCanonicalSig)
	if v.Full {
		flags |= vfFullValidation
	}
	buf = appendFieldHeader(buf, typeUINT32, fieldFlags)
	buf = append(buf, byte(flags>>24), byte(flags>>16), byte(flags>>8), byte(flags))

	// sfLedgerSequence (type 2, field 6)
	buf = appendFieldHeader(buf, typeUINT32, fieldLedgerSequence)
	buf = append(buf, byte(v.LedgerSeq>>24), byte(v.LedgerSeq>>16), byte(v.LedgerSeq>>8), byte(v.LedgerSeq))

	// sfSigningTime (type 2, field 9)
	signTimeSec := uint32(v.SignTime.Unix() - protocol.RippleEpochUnix)
	buf = appendFieldHeader(buf, typeUINT32, fieldSigningTime)
	buf = append(buf, byte(signTimeSec>>24), byte(signTimeSec>>16), byte(signTimeSec>>8), byte(signTimeSec))

	// sfLoadFee (type 2, field 24) — optional
	if v.LoadFee != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldLoadFee)
		buf = append(buf, byte(v.LoadFee>>24), byte(v.LoadFee>>16), byte(v.LoadFee>>8), byte(v.LoadFee))
	}

	// sfReserveBase (type 2, field 31) — optional flag-ledger fee vote
	// (legacy pre-XRPFees form). Must stay in sync with
	// SerializeSTValidation emission order.
	if v.ReserveBase != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldReserveBase)
		buf = append(buf, byte(v.ReserveBase>>24), byte(v.ReserveBase>>16), byte(v.ReserveBase>>8), byte(v.ReserveBase))
	}

	// sfReserveIncrement (type 2, field 32) — optional flag-ledger fee vote.
	if v.ReserveIncrement != 0 {
		buf = appendFieldHeader(buf, typeUINT32, fieldReserveInc)
		buf = append(buf, byte(v.ReserveIncrement>>24), byte(v.ReserveIncrement>>16), byte(v.ReserveIncrement>>8), byte(v.ReserveIncrement))
	}

	// sfBaseFee (type 3, field 5) — optional flag-ledger fee vote
	// (legacy pre-XRPFees form).
	if v.BaseFee != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldBaseFee)
		for i := 7; i >= 0; i-- {
			buf = append(buf, byte(v.BaseFee>>(i*8)))
		}
	}

	// sfCookie (type 3, field 10) — optional
	if v.Cookie != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldCookie)
		for i := 7; i >= 0; i-- {
			buf = append(buf, byte(v.Cookie>>(i*8)))
		}
	}

	// sfServerVersion (type 3, field 11) — optional
	if v.ServerVersion != 0 {
		buf = appendFieldHeader(buf, typeUINT64, fieldServerVersion)
		for i := 7; i >= 0; i-- {
			buf = append(buf, byte(v.ServerVersion>>(i*8)))
		}
	}

	// --- HASH256 fields (type 5) — must precede AMOUNT (type 6) per
	// canonical ascending-type ordering. Order must stay byte-identical
	// to SerializeSTValidation — any drift silently breaks our own
	// self-verify round-trip.

	// sfLedgerHash (type 5, field 1)
	buf = appendFieldHeader(buf, typeHash256, fieldLedgerHash)
	buf = append(buf, v.LedgerID[:]...)

	// sfConsensusHash (type 5, field 23) — optional
	if v.ConsensusHash != ([32]byte{}) {
		buf = appendFieldHeader(buf, typeHash256, fieldConsensusHash)
		buf = append(buf, v.ConsensusHash[:]...)
	}

	// sfValidatedHash (type 5, field 25) — optional.
	if v.ValidatedHash != ([32]byte{}) {
		buf = appendFieldHeader(buf, typeHash256, fieldValidatedHash)
		buf = append(buf, v.ValidatedHash[:]...)
	}

	// --- AMOUNT fields (type 6) — post-featureXRPFees fee votes ---
	if v.BaseFeeDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldBaseFeeDrops)
		buf = appendXRPAmount(buf, v.BaseFeeDrops)
	}
	if v.ReserveBaseDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldReserveBaseDrops)
		buf = appendXRPAmount(buf, v.ReserveBaseDrops)
	}
	if v.ReserveIncrementDrops != 0 {
		buf = appendFieldHeader(buf, typeAmount, fieldReserveIncrementDrops)
		buf = appendXRPAmount(buf, v.ReserveIncrementDrops)
	}

	// sfSigningPubKey (type 7, field 3) — included in signing hash per XRPL spec.
	buf = appendFieldHeader(buf, typeBlob, fieldSigningPubKey)
	buf = appendVL(buf, v.NodeID[:])

	// sfAmendments — VECTOR256 (type 19) FIELD 3 per rippled
	// sfields.macro:306. The older value 19 confused type with field.
	// Must stay in sync with SerializeSTValidation which emits this
	// AFTER sfSigningPubKey; sfSignature comes last and is the only
	// field excluded from the signing preimage.
	if len(v.Amendments) > 0 {
		buf = appendFieldHeader(buf, typeVector256, fieldAmendments)
		blob := make([]byte, 0, 32*len(v.Amendments))
		for _, id := range v.Amendments {
			blob = append(blob, id[:]...)
		}
		buf = appendVL(buf, blob)
	}

	hash := common.Sha512Half(buf)
	return hash[:]
}
