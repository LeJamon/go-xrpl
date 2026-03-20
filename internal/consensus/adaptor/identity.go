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
	closeTimeSec := uint32(p.CloseTime.Unix() - xrplEpochOffset)
	buf = append(buf, byte(closeTimeSec>>24), byte(closeTimeSec>>16), byte(closeTimeSec>>8), byte(closeTimeSec))

	// PreviousLedger (32 bytes)
	buf = append(buf, p.PreviousLedger[:]...)

	// TxSet (32 bytes)
	buf = append(buf, p.TxSet[:]...)

	hash := common.Sha512Half(buf)
	return hash[:]
}

// buildValidationSigningData constructs the data to be signed for a validation.
// Format: HashPrefixValidation + Flags(4) + LedgerSeq(4) + CloseTime(4) + SigningTime(4) + LedgerID(32)
func buildValidationSigningData(v *consensus.Validation) []byte {
	var buf []byte
	buf = append(buf, protocol.HashPrefixValidation[:]...)

	// Flags (4 bytes) - Full=1
	flags := uint32(0)
	if v.Full {
		flags |= 0x00000001
	}
	buf = append(buf, byte(flags>>24), byte(flags>>16), byte(flags>>8), byte(flags))

	// LedgerSeq (4 bytes, big-endian)
	buf = append(buf, byte(v.LedgerSeq>>24), byte(v.LedgerSeq>>16), byte(v.LedgerSeq>>8), byte(v.LedgerSeq))

	// SignTime as XRPL epoch seconds (4 bytes, big-endian)
	signTimeSec := uint32(v.SignTime.Unix() - xrplEpochOffset)
	buf = append(buf, byte(signTimeSec>>24), byte(signTimeSec>>16), byte(signTimeSec>>8), byte(signTimeSec))

	// LedgerID (32 bytes)
	buf = append(buf, v.LedgerID[:]...)

	hash := common.Sha512Half(buf)
	return hash[:]
}

// xrplEpochOffset is the difference between Unix epoch and XRPL epoch (2000-01-01 00:00:00 UTC).
const xrplEpochOffset int64 = 946684800
