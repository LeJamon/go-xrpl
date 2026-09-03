package adaptor

import (
	"bytes"
	"crypto/ed25519"
	"errors"
	"fmt"
	"sync"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	xrplcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	errNoValidatorKey           = errors.New("no validator key configured")
	errInvalidSeed              = errors.New("invalid validator seed")
	errSigningKeyMismatch       = errors.New("signing private key does not match signing public key")
	errTokenManifestKeyMismatch = errors.New("validator_token: signing key in manifest does not match validation_secret_key")
	errTokenAndSeed             = errors.New("validator_token and validation_seed are mutually exclusive")
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
// NodeID is the 20-byte calcNodeID(MasterKey) identifier. Wire frames
// carry the 33-byte SigningKey via sfSigningPubKey /
// TMProposeSet.nodepubkey; the consensus router resolves the signing
// key to its master via the manifest cache before populating NodeID
// on inbound Proposals / Validations, so all in-memory maps key on
// the master-derived identifier consistently.
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
	// ephemeral signing key does not change NodeID, preserving the
	// long-term identity.
	NodeID consensus.NodeID

	// Manifest is the parsed local manifest when configured via
	// validator_token. Nil in seed-only mode. Drives TMManifests
	// emission.
	Manifest *manifest.Manifest

	// SerializedMfst is the wire bytes of the local manifest, kept so
	// emission can broadcast the exact payload peers expect without
	// re-encoding through the codec.
	SerializedMfst []byte

	signingSecret *signingSecret
}

type signingSecret struct {
	mu         sync.RWMutex
	privateKey []byte
	closed     bool
}

// NewValidatorIdentity creates a seed-only identity. The seed is the
// base58 [validation_seed] string. Returns nil if seed is empty (the
// observer / non-validator case).
//
// Master and signing keys are identical in this mode, the fallback when
// [validator_token] is absent. The caller owns the returned identity and
// must call [ValidatorIdentity.Close] when it is no longer needed.
func NewValidatorIdentity(seed string) (*ValidatorIdentity, error) {
	if seed == "" {
		return nil, nil
	}

	decodedSeed, _, err := addresscodec.DecodeSeed(seed)
	if err != nil {
		return nil, errInvalidSeed
	}
	defer xrplcrypto.SecureErase(decodedSeed)

	algo := secp256k1.Algorithm{}
	privKeyBytes, pubKeyBytes, err := algo.DeriveKeypairBytes(decodedSeed, true)
	if err != nil {
		return nil, err
	}
	defer xrplcrypto.SecureErase(privKeyBytes)
	if len(pubKeyBytes) != 33 {
		return nil, fmt.Errorf("derived pubkey: unexpected length %d", len(pubKeyBytes))
	}

	vi := &ValidatorIdentity{}
	copy(vi.MasterKey[:], pubKeyBytes)
	copy(vi.SigningKey[:], pubKeyBytes)
	if err := vi.replaceSigningPrivateKey(privKeyBytes); err != nil {
		return nil, err
	}
	vi.NodeID = consensus.CalcNodeID(vi.MasterKey)
	return vi, nil
}

// newValidatorIdentityFromToken creates a master/ephemeral split
// identity from a `[validator_token]` config block. The block is the
// raw multi-line section text (whitespace tolerated).
//
//  1. Parse the token into manifest + 32-byte secret.
//  2. Decode and parse the embedded manifest (structural invariants
//     only; signatures are not verified here — the ManifestCache
//     verifies on apply).
//  3. Derive the public key from the secret and confirm it matches the
//     manifest's SigningPubKey — protects against a swapped or corrupt
//     token blob where the secret no longer signs the declared
//     ephemeral key.
//  4. Store master, signing, signing-priv, and the wire-format manifest
//     for broadcast.
func newValidatorIdentityFromToken(block string) (*ValidatorIdentity, error) {
	if block == "" {
		return nil, errNoValidatorKey
	}
	tok, err := manifest.LoadValidatorToken(block)
	if err != nil {
		return nil, err
	}
	defer xrplcrypto.SecureErase(tok.ValidationSecret[:])
	wire, err := tok.DecodeManifest()
	if err != nil {
		return nil, err
	}
	m, err := manifest.Deserialize(wire)
	if err != nil {
		return nil, fmt.Errorf("validator_token: deserialize manifest: %w", err)
	}

	pub, err := secp256k1.Algorithm{}.DerivePublicKeyFromSecret(tok.ValidationSecret[:])
	if err != nil {
		return nil, fmt.Errorf("validator_token: derive pubkey: %w", err)
	}
	var derived [33]byte
	copy(derived[:], pub)
	if derived != m.SigningKey() {
		return nil, errTokenManifestKeyMismatch
	}

	vi := &ValidatorIdentity{
		MasterKey:      m.MasterKey(),
		SigningKey:     m.SigningKey(),
		Manifest:       m,
		SerializedMfst: m.Serialized(),
	}
	if err := vi.replaceSigningPrivateKey(tok.ValidationSecret[:]); err != nil {
		return nil, err
	}
	vi.NodeID = consensus.CalcNodeID(vi.MasterKey)
	return vi, nil
}

// newValidatorIdentityFromConfig dispatches to the seed or token
// constructor based on which field the operator configured. Returns nil
// when neither is set (observer mode): an empty validator config means a
// non-validating node.
//
// Both configured at once is a fatal misconfiguration; the returned
// error lets cmd/goxrpl surface it before the consensus engine starts.
func newValidatorIdentityFromConfig(seed, token string) (*ValidatorIdentity, error) {
	if seed != "" && token != "" {
		return nil, errTokenAndSeed
	}
	if token != "" {
		return newValidatorIdentityFromToken(token)
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

func (vi *ValidatorIdentity) replaceSigningPrivateKey(privateKey []byte) error {
	publicKey, err := (secp256k1.Algorithm{}).DerivePublicKeyFromSecret(privateKey)
	if err != nil {
		return err
	}
	if !bytes.Equal(publicKey, vi.SigningKey[:]) {
		return errSigningKeyMismatch
	}
	privateKeyCopy := append([]byte(nil), privateKey...)
	if vi.signingSecret == nil {
		vi.signingSecret = &signingSecret{}
	}

	vi.signingSecret.mu.Lock()
	defer vi.signingSecret.mu.Unlock()
	if vi.signingSecret.closed {
		xrplcrypto.SecureErase(privateKeyCopy)
		return errNoValidatorKey
	}
	xrplcrypto.SecureErase(vi.signingSecret.privateKey)
	vi.signingSecret.privateKey = privateKeyCopy
	return nil
}

// Close erases the identity-owned signing key. Memory erasure is best-effort
// in Go: encoded seeds and tokens are immutable strings, and copies held by the
// runtime, dependencies, registers, or swap may remain.
func (vi *ValidatorIdentity) Close() error {
	if vi == nil {
		return nil
	}

	if vi.signingSecret == nil {
		return nil
	}
	vi.signingSecret.mu.Lock()
	defer vi.signingSecret.mu.Unlock()
	xrplcrypto.SecureErase(vi.signingSecret.privateKey)
	vi.signingSecret.privateKey = nil
	vi.signingSecret.closed = true
	return nil
}

// Sign signs a pre-computed digest with the ephemeral signing key using
// secp256k1. The data parameter must be a SHA-512Half digest (32 bytes),
// passed directly to secp256k1.
func (vi *ValidatorIdentity) Sign(data []byte) ([]byte, error) {
	if vi == nil {
		return nil, errNoValidatorKey
	}
	if vi.signingSecret == nil {
		return nil, errNoValidatorKey
	}
	vi.signingSecret.mu.RLock()
	defer vi.signingSecret.mu.RUnlock()
	if len(vi.signingSecret.privateKey) == 0 {
		return nil, errNoValidatorKey
	}
	return secp256k1.SignDigestBytes(data, vi.signingSecret.privateKey)
}

// verify dispatches on the pubkey-type prefix (0xED → ed25519, 0x02/0x03
// → secp256k1). The data parameter is a SHA-512Half digest (32 bytes):
// secp256k1 verifies the digest natively, and ed25519 verifies the
// digest as a 32-byte message (no internal re-hash).
func verify(pubKey []byte, data []byte, signature []byte) bool {
	return verifyWithCanonicality(pubKey, data, signature, false)
}

func verifyWithCanonicality(pubKey []byte, data []byte, signature []byte, mustBeFullyCanonical bool) bool {
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
		algo := secp256k1.Algorithm{}
		var digest [32]byte
		copy(digest[:], data)
		return algo.ValidateDigestWithCanonicality(digest, pubKey, signature, mustBeFullyCanonical)
	default:
		return false
	}
}

// SignProposal signs a consensus proposal. The signed data is
// SHA-512Half(HashPrefixProposal + serialized proposal fields).
func (vi *ValidatorIdentity) SignProposal(proposal *consensus.Proposal) error {
	if vi == nil {
		return errNoValidatorKey
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

// verifyProposal verifies a proposal's signature against its
// SigningPubKey. NodeID is the master-derived 20-byte identifier and
// is not a verification key — only the ephemeral SigningPubKey
// (sfSigningPubKey on the wire) is what the proposal was signed with.
func verifyProposal(proposal *consensus.Proposal) error {
	data := buildProposalSigningData(proposal)
	if !verify(proposal.SigningPubKey[:], data, proposal.Signature) {
		return errors.New("invalid proposal signature")
	}
	return nil
}

// SignValidation signs a consensus validation. The signed data is
// SHA-512Half(HashPrefixValidation + serialized validation fields).
func (vi *ValidatorIdentity) SignValidation(validation *consensus.Validation) error {
	if vi == nil {
		return errNoValidatorKey
	}
	validation.SigningPubKey = consensus.SigningPubKey(vi.SigningKey)
	validation.NodeID = vi.NodeID
	validation.Flags |= vfFullyCanonicalSig
	if validation.Full {
		validation.Flags |= vfFullValidation
	}
	data := buildValidationSigningData(validation)
	sig, err := vi.Sign(data)
	if err != nil {
		return err
	}
	validation.Signature = sig
	return nil
}

// verifyValidation verifies a validation's signature against its
// SigningPubKey. NodeID is the master-derived 20-byte identifier and
// is not a verification key — only the ephemeral SigningPubKey
// (sfSigningPubKey on the wire) is what the validation was signed
// with.
func verifyValidation(validation *consensus.Validation) error {
	data := buildValidationSigningData(validation)
	mustBeFullyCanonical := validation.Flags&vfFullyCanonicalSig != 0
	if !verifyWithCanonicality(validation.SigningPubKey[:], data, validation.Signature, mustBeFullyCanonical) {
		return errors.New("invalid validation signature")
	}
	return nil
}

// buildProposalSigningData constructs the data to be signed for a proposal.
// Format: HashPrefixProposal + ProposeSeq(4) + CloseTime(4) + PreviousLedger(32) + TxSet(32)
func buildProposalSigningData(p *consensus.Proposal) []byte {
	buf := make([]byte, 0, len(protocol.HashPrefixProposal())+4+4+len(p.PreviousLedger)+len(p.TxSet))
	buf = append(buf, protocol.HashPrefixProposal().Bytes()...)

	// ProposeSeq (4 bytes, big-endian)
	buf = append(buf, byte(p.Position>>24), byte(p.Position>>16), byte(p.Position>>8), byte(p.Position))

	// CloseTime as XRPL epoch seconds (4 bytes, big-endian)
	closeTimeSec := timeToXrplEpoch(p.CloseTime)
	buf = append(buf, byte(closeTimeSec>>24), byte(closeTimeSec>>16), byte(closeTimeSec>>8), byte(closeTimeSec))

	// PreviousLedger (32 bytes)
	buf = append(buf, p.PreviousLedger[:]...)

	// TxSet (32 bytes)
	buf = append(buf, p.TxSet[:]...)

	hash := sha512half.Sum(buf)
	return hash[:]
}

// buildValidationSigningData constructs the signing digest for a validation.
//
// For inbound validations (SigningData populated by parseSTValidation), the
// exact non-signing bytes from the wire are used — including any optional
// fields the sender included that we don't model explicitly. That keeps us
// compatible with senders emitting fields we don't ourselves understand.
//
// For outbound validations (SigningData nil), we regenerate the preimage
// from struct fields. It MUST stay byte-identical to what
// serializeSTValidation emits (minus sfSignature); otherwise a freshly-
// signed validation would fail verification when parsed back from the
// wire. When extending the wire format, update both functions together.
func buildValidationSigningData(v *consensus.Validation) []byte {
	if len(v.SigningData) > 0 {
		// Inbound: use the exact non-signing bytes from the wire.
		hash := sha512half.Sum(protocol.HashPrefixValidation().Bytes(), v.SigningData)
		return hash[:]
	}

	// Outbound: the signing preimage is the canonical wire serialization
	// with sfSignature omitted. Derive it from serializeSTValidation — the
	// single STValidation serializer — so the preimage and the wire bytes
	// can never drift (the previous hand-rolled copy of every field was a
	// standing fork hazard). serializeSTValidation emits sfSignature only
	// when v.Signature is non-empty and as a distinct field between
	// sfSigningPubKey and sfAmendments, so clearing it yields exactly the
	// non-signature preimage. SignValidation stamps outbound flags before
	// this function is called.
	unsigned := *v
	unsigned.Signature = nil
	hash := sha512half.Sum(protocol.HashPrefixValidation().Bytes(), serializeSTValidation(&unsigned))
	return hash[:]
}
