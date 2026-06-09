package adaptor

import (
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
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
// NodeID is the 20-byte calcNodeID(MasterKey) identifier matching
// rippled's NodeID type (rippled/include/xrpl/protocol/UintTypes.h:59).
// Wire frames carry the 33-byte SigningKey via sfSigningPubKey /
// TMProposeSet.nodepubkey; the consensus router resolves the signing
// key to its master via the manifest cache before populating NodeID
// on inbound Proposals / Validations, so all in-memory maps key on
// the master-derived identifier consistently with rippled.
type ValidatorIdentity struct {
	// MasterKey is the 33-byte compressed master public key declared in
	// the manifest. In seed-only mode it equals SigningKey.
	MasterKey [33]byte

	// SigningKey is the 33-byte compressed ephemeral public key used
	// to sign validations and proposals. In seed-only mode it equals
	// MasterKey.
	SigningKey [33]byte

	// NodeID is the validator's master-derived 20-byte identifier
	// (calcNodeID(MasterKey)). Distinct from SigningKey: rotating the
	// ephemeral signing key does not change NodeID, matching rippled's
	// long-term identity model.
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
	vi.NodeID = consensus.CalcNodeID(vi.MasterKey)
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
	vi.NodeID = consensus.CalcNodeID(vi.MasterKey)
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

// Verify dispatches on the pubkey-type prefix (0xED → ed25519, 0x02/0x03
// → secp256k1). The data parameter is a SHA-512Half digest (32 bytes).
// Mirrors rippled's PublicKey.cpp verify(uint256, ...): secp256k1 verifies
// the digest natively, and rippled's ed25519 wrapper signs/verifies the
// digest as a 32-byte message (no internal re-hash).
func Verify(pubKey []byte, data []byte, signature []byte) bool {
	if len(pubKey) != 33 {
		return false
	}
	switch pubKey[0] {
	case 0xED:
		if len(signature) != ed25519.SignatureSize {
			return false
		}
		return ed25519.Verify(ed25519.PublicKey(pubKey[1:]), data, signature)
	case 0x02, 0x03:
		algo := secp256k1.SECP256K1()
		var digest [32]byte
		copy(digest[:], data)
		return algo.ValidateDigest(digest, pubKey, signature)
	default:
		return false
	}
}

// SignProposal signs a consensus proposal.
// The signed data is SHA-512Half(HashPrefixProposal + serialized proposal fields).
// Matches rippled's Proposal signing format.
func (vi *ValidatorIdentity) SignProposal(proposal *consensus.Proposal) error {
	if vi == nil {
		return ErrNoValidatorKey
	}
	proposal.SigningPubKey = consensus.SigningPubKey(vi.SigningKey)
	proposal.NodeID = vi.NodeID
	data := buildProposalSigningData(proposal)
	sig, err := vi.Sign(data)
	if err != nil {
		return err
	}
	proposal.Signature = sig
	return nil
}

// VerifyProposal verifies a proposal's signature against its
// SigningPubKey. NodeID is the master-derived 20-byte identifier and
// is not a verification key — only the ephemeral SigningPubKey
// (sfSigningPubKey on the wire) is what the proposal was signed with.
func VerifyProposal(proposal *consensus.Proposal) error {
	data := buildProposalSigningData(proposal)
	if !Verify(proposal.SigningPubKey[:], data, proposal.Signature) {
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
	validation.SigningPubKey = consensus.SigningPubKey(vi.SigningKey)
	validation.NodeID = vi.NodeID
	data := buildValidationSigningData(validation)
	sig, err := vi.Sign(data)
	if err != nil {
		return err
	}
	validation.Signature = sig
	return nil
}

// VerifyValidation verifies a validation's signature against its
// SigningPubKey. NodeID is the master-derived 20-byte identifier and
// is not a verification key — only the ephemeral SigningPubKey
// (sfSigningPubKey on the wire) is what the validation was signed
// with.
func VerifyValidation(validation *consensus.Validation) error {
	data := buildValidationSigningData(validation)
	if !Verify(validation.SigningPubKey[:], data, validation.Signature) {
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
	closeTimeSec := timeToXrplEpoch(p.CloseTime)
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

	// Outbound: the signing preimage is the canonical wire serialization
	// with sfSignature omitted. Derive it from SerializeSTValidation — the
	// single STValidation serializer — so the preimage and the wire bytes
	// can never drift (the previous hand-rolled copy of every field was a
	// standing fork hazard). SerializeSTValidation emits sfSignature only
	// when v.Signature is non-empty and as a distinct field between
	// sfSigningPubKey and sfAmendments, so clearing it yields exactly the
	// non-signature preimage. Outbound validations carry Flags == 0 (only
	// the inbound parser sets Flags), so SerializeSTValidation synthesizes
	// the same vfFullyCanonicalSig|vfFullValidation pair this used to build.
	unsigned := *v
	unsigned.Signature = nil
	hash := common.Sha512Half(protocol.HashPrefixValidation[:], SerializeSTValidation(&unsigned))
	return hash[:]
}
