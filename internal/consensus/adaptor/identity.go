package adaptor

import (
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/manifest"
	"github.com/LeJamon/goXRPLd/protocol"
)

var (
	ErrNoValidatorKey           = errors.New("no validator key configured")
	ErrInvalidSeed              = errors.New("invalid validator seed")
	ErrTokenManifestKeyMismatch = errors.New("validator_token: signing key in manifest does not match validation_secret_key")
	ErrTokenAndSeed             = errors.New("validator_token and validation_seed are mutually exclusive")
)

// ValidatorIdentity holds the validator's signing keys and, when
// configured via [validator_token], its master-signed manifest.
//
// Two configuration paths populate this struct:
//
//   - validator_token (preferred): MasterKey is the long-term identity
//     declared in the manifest; SigningKey is the rotatable ephemeral
//     key used to sign every consensus message; Manifest carries the
//     master-signed binding so peers can resolve SigningKey → MasterKey.
//
//   - validation_seed (legacy): MasterKey == SigningKey, derived
//     directly from the seed; Manifest is nil. Peers cannot rotate
//     keys without operator intervention on every peer in this mode.
//
// NodeID currently equals SigningKey for wire-format compatibility
// (sfSigningPubKey carries 33 bytes; consensus.NodeID is also 33 bytes
// and is used both as the wire signing pubkey and as the in-memory
// identifier). Rippled's true NodeID is calcNodeID(masterKey) — a
// 20-byte RIPEMD-160 — and the manifest cache resolver in startup.go
// already maps the signing-pubkey-shaped NodeID back to a master-shaped
// one via GetMasterKey. Migrating consensus.NodeID to the 20-byte form
// is tracked as a separate sub-issue per #371.
type ValidatorIdentity struct {
	// MasterKey is the 33-byte compressed master public key declared in
	// the manifest. In seed-only mode it equals SigningKey.
	MasterKey [33]byte

	// SigningKey is the 33-byte compressed ephemeral public key used
	// to sign validations and proposals. In seed-only mode it equals
	// MasterKey.
	SigningKey [33]byte

	// NodeID is the validator's wire-level identifier. Currently set
	// to SigningKey to keep the existing
	// {Validation,Proposal}.NodeID == sfSigningPubKey contract intact;
	// see the type-level comment for the deferred migration.
	NodeID consensus.NodeID

	// Manifest is the parsed local manifest when configured via
	// validator_token. Nil in seed-only mode. Used by #372 to drive
	// TMManifests emission.
	Manifest *manifest.Manifest

	// SerializedMfst is the wire bytes of the local manifest, kept so
	// emission (#372) can broadcast the exact payload peers expect
	// without re-encoding through the codec.
	SerializedMfst []byte

	// signingPriv is the hex-encoded signing private key (with or
	// without the leading "00" prefix; secp256k1.SignDigest accepts
	// both). Unexported so callers cannot accidentally leak the secret.
	signingPriv string
}

// NewValidatorIdentity creates a seed-only identity. The seed is the
// base58 [validation_seed] string. Returns nil if seed is empty (the
// observer / non-validator case).
//
// Master and signing keys are identical in this mode, matching rippled's
// ValidatorKeys.cpp:84-89 fallback when [validator_token] is absent.
func NewValidatorIdentity(seed string) (*ValidatorIdentity, error) {
	if seed == "" {
		return nil, nil
	}

	decodedSeed, _, err := addresscodec.DecodeSeed(seed)
	if err != nil {
		return nil, ErrInvalidSeed
	}

	algo := secp256k1.SECP256K1()
	privKeyHex, pubKeyHex, err := algo.DeriveKeypair(decodedSeed, true)
	if err != nil {
		return nil, err
	}

	pubKeyBytes, err := hex.DecodeString(pubKeyHex)
	if err != nil {
		return nil, err
	}
	if len(pubKeyBytes) != 33 {
		return nil, fmt.Errorf("derived pubkey: unexpected length %d", len(pubKeyBytes))
	}

	vi := &ValidatorIdentity{signingPriv: privKeyHex}
	copy(vi.MasterKey[:], pubKeyBytes)
	copy(vi.SigningKey[:], pubKeyBytes)
	copy(vi.NodeID[:], pubKeyBytes)
	return vi, nil
}

// NewValidatorIdentityFromToken creates a master/ephemeral split
// identity from a `[validator_token]` config block. The block is the
// raw multi-line section text (whitespace tolerated).
//
// Steps mirror rippled ValidatorKeys.cpp:42-71:
//  1. Parse the token into manifest + 32-byte secret.
//  2. Decode and parse the embedded manifest (structural invariants
//     only; signatures are not verified here, matching rippled — the
//     ManifestCache verifies on apply).
//  3. Derive the public key from the secret and confirm it matches the
//     manifest's SigningPubKey — protects against a swapped or corrupt
//     token blob where the secret no longer signs the declared
//     ephemeral key.
//  4. Store master, signing, signing-priv, and the wire-format manifest
//     so #372 can broadcast it.
func NewValidatorIdentityFromToken(block string) (*ValidatorIdentity, error) {
	if block == "" {
		return nil, ErrNoValidatorKey
	}
	tok, err := manifest.LoadValidatorToken(block)
	if err != nil {
		return nil, err
	}
	wire, err := tok.DecodeManifest()
	if err != nil {
		return nil, err
	}
	m, err := manifest.Deserialize(wire)
	if err != nil {
		return nil, fmt.Errorf("validator_token: deserialize manifest: %w", err)
	}

	pub, err := secp256k1.SECP256K1().DerivePublicKeyFromSecret(tok.ValidationSecret[:])
	if err != nil {
		return nil, fmt.Errorf("validator_token: derive pubkey: %w", err)
	}
	var derived [33]byte
	copy(derived[:], pub)
	if derived != m.SigningKey {
		return nil, ErrTokenManifestKeyMismatch
	}

	vi := &ValidatorIdentity{
		MasterKey:      m.MasterKey,
		SigningKey:     m.SigningKey,
		Manifest:       m,
		SerializedMfst: append([]byte(nil), m.Serialized...),
		signingPriv:    hex.EncodeToString(tok.ValidationSecret[:]),
	}
	copy(vi.NodeID[:], m.SigningKey[:])
	return vi, nil
}

// NewValidatorIdentityFromConfig dispatches to the seed or token
// constructor based on which field the operator configured. Returns nil
// when neither is set (observer mode), matching rippled which treats an
// empty validator config as a non-validating node.
//
// Both configured at once is a fatal misconfiguration (rippled
// ValidatorKeys.cpp:31-38 sets configInvalid_ in that case); the
// equivalent here is a returned error so cmd/xrpld can surface it
// before the consensus engine starts.
func NewValidatorIdentityFromConfig(seed, token string) (*ValidatorIdentity, error) {
	if seed != "" && token != "" {
		return nil, ErrTokenAndSeed
	}
	if token != "" {
		return NewValidatorIdentityFromToken(token)
	}
	return NewValidatorIdentity(seed)
}

// SigningPubKey returns the 33-byte compressed signing public key as a
// fresh slice. Convenience for callers wiring overlay options that
// expect a []byte (peermanagement.WithLocalValidatorPubKey).
func (vi *ValidatorIdentity) SigningPubKey() []byte {
	if vi == nil {
		return nil
	}
	return append([]byte(nil), vi.SigningKey[:]...)
}

// Sign signs a pre-computed digest with the ephemeral signing key using
// secp256k1. The data parameter must be a SHA-512Half digest (32 bytes).
// Matches rippled's signDigest() which passes the hash directly to
// secp256k1.
func (vi *ValidatorIdentity) Sign(data []byte) ([]byte, error) {
	if vi == nil {
		return nil, ErrNoValidatorKey
	}
	algo := secp256k1.SECP256K1()
	var digest [32]byte
	copy(digest[:], data)
	return algo.SignDigest(digest, vi.signingPriv)
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
