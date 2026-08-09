// Package manifest implements validator manifest parsing, verification, caching,
// and persistence.
//
// A manifest binds a validator's long-term master key to a rotatable
// ephemeral signing key. Peers gossip manifests so every node on the
// network can translate an ephemeral signing key used in a validation or
// proposal back to its master key for UNL / quorum decisions. Without
// this translation a validator that rotates its ephemeral key appears as
// a new untrusted node and breaks mainnet quorum arithmetic.
//
// Wire format:
//
//	STObject with fields
//	  PublicKey        (required) — master public key
//	  MasterSignature  (required) — signature by the master key
//	  Sequence         (required) — strictly monotonic; MaxUint32 = revoked
//	  Version          (default 0)
//	  Domain           (optional)
//	  SigningPubKey    (optional; absent iff revoked) — ephemeral public key
//	  Signature        (optional; absent iff revoked) — signature by the ephemeral key
//
// Both signatures sign the same preimage: HashPrefix("MAN\0") prepended
// to the canonical STObject serialization with Signature and
// MasterSignature removed (the xrpl "isSigningField" filter). The
// ed25519 path verifies the raw preimage; the secp256k1 path SHA-512Half
// hashes the preimage first. Both already match that convention in
// crypto/ed25519 and crypto/secp256k1.
package manifest

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/protocol"
)

// RevokedSequence marks a manifest as a master-key revocation.
const RevokedSequence uint32 = math.MaxUint32

const maxSerializedSize = 1024

// Manifest is a parsed, syntactically-valid validator manifest.
// Signature verification is separate — callers invoke Verify before
// trusting the struct's key bindings.
type Manifest struct {
	masterKey       [33]byte
	signingKey      [33]byte
	sequence        uint32
	domain          string
	serialized      []byte
	masterSignature string
	signature       string
	signingPreimage []byte
}

// MasterKey returns the manifest's long-term public key.
func (m *Manifest) MasterKey() [33]byte {
	if m == nil {
		return [33]byte{}
	}
	return m.masterKey
}

// SigningKey returns the current ephemeral public key, or zero for a revocation.
func (m *Manifest) SigningKey() [33]byte {
	if m == nil {
		return [33]byte{}
	}
	return m.signingKey
}

func (m *Manifest) Sequence() uint32 {
	if m == nil {
		return 0
	}
	return m.sequence
}

// Domain returns the optional validator domain.
func (m *Manifest) Domain() string {
	if m == nil {
		return ""
	}
	return m.domain
}

// Serialized returns an owned copy of the original wire bytes.
func (m *Manifest) Serialized() []byte {
	if m == nil {
		return nil
	}
	return append([]byte(nil), m.serialized...)
}

// Revoked reports whether the manifest revokes its master key.
func (m *Manifest) Revoked() bool {
	return m != nil && m.sequence == RevokedSequence
}

func cloneManifest(m *Manifest) *Manifest {
	if m == nil {
		return nil
	}
	cloned := *m
	cloned.serialized = append([]byte(nil), m.serialized...)
	cloned.signingPreimage = append([]byte(nil), m.signingPreimage...)
	return &cloned
}

// Signatures returns the wire signatures captured during parsing.
func (m *Manifest) Signatures() (masterSigHex, signatureHex string) {
	if m == nil {
		return "", ""
	}
	return m.masterSignature, m.signature
}

// Deserialize parses a wire-format manifest. Returns a non-nil error if
// the bytes aren't a well-formed STObject, a required field is missing,
// or the field relationship invariants (revoked ⇒ no ephemeral fields;
// non-revoked ⇒ ephemeral fields present; signing key != master key;
// key-type prefix byte valid) are violated.
//
// Signatures are NOT verified here — call Verify after parsing.
func Deserialize(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, errors.New("manifest: empty payload")
	}
	if len(data) > maxSerializedSize {
		return nil, fmt.Errorf("manifest: payload exceeds %d bytes", maxSerializedSize)
	}

	decoded, err := binarycodec.DecodeBytes(data)
	if err != nil {
		return nil, fmt.Errorf("manifest: decode STObject: %w", err)
	}
	for field := range decoded {
		switch field {
		case "PublicKey", "MasterSignature", "Sequence", "Version", "Domain", "SigningPubKey", "Signature":
		default:
			return nil, fmt.Errorf("manifest: unexpected field %s", field)
		}
	}

	// Version defaults to 0; any other value is unsupported.
	if raw, ok := decoded["Version"]; ok {
		v, ok := raw.(int)
		if !ok {
			return nil, errors.New("manifest: Version is not numeric")
		}
		if v != 0 {
			return nil, fmt.Errorf("manifest: unsupported Version %d", v)
		}
	}

	masterHex, err := requireHexField(decoded, "PublicKey")
	if err != nil {
		return nil, err
	}
	master, err := decodeKey(masterHex)
	if err != nil {
		return nil, fmt.Errorf("manifest: PublicKey: %w", err)
	}

	seqRaw, ok := decoded["Sequence"]
	if !ok {
		return nil, errors.New("manifest: missing required Sequence")
	}
	seq, ok := seqRaw.(uint32)
	if !ok {
		return nil, errors.New("manifest: Sequence is not numeric")
	}

	masterSignature, err := requireHexField(decoded, "MasterSignature")
	if err != nil {
		return nil, err
	}

	m := &Manifest{
		masterKey:       master,
		sequence:        seq,
		serialized:      append([]byte(nil), data...),
		masterSignature: masterSignature,
	}

	if dom, ok := decoded["Domain"]; ok {
		s, ok := dom.(string)
		if !ok {
			return nil, errors.New("manifest: Domain is not a variable-length field")
		}
		// Domain is VL-encoded as bytes; the codec returns a hex
		// string. Decode it back to the raw UTF-8 text.
		b, err := hex.DecodeString(s)
		if err != nil {
			return nil, fmt.Errorf("manifest: Domain not hex: %w", err)
		}
		m.domain = string(b)
		if !protocol.IsProperlyFormedTomlDomain(m.domain) {
			return nil, errors.New("manifest: Domain is not a properly formed TOML domain")
		}
	}

	hasSigningKey := hasField(decoded, "SigningPubKey")
	hasSignature := hasField(decoded, "Signature")

	if m.Revoked() {
		if hasSigningKey || hasSignature {
			return nil, errors.New("manifest: revoked manifest must not carry ephemeral fields")
		}
		m.signingPreimage, err = signingPreimageFromDecoded(decoded)
		if err != nil {
			return nil, fmt.Errorf("manifest: build signing preimage: %w", err)
		}
		return m, nil
	}

	if !hasSigningKey || !hasSignature {
		return nil, errors.New("manifest: non-revoked manifest requires SigningPubKey and Signature")
	}

	signingHex, _ := requireHexField(decoded, "SigningPubKey")
	signing, err := decodeKey(signingHex)
	if err != nil {
		return nil, fmt.Errorf("manifest: SigningPubKey: %w", err)
	}
	if signing == master {
		return nil, errors.New("manifest: signing key equals master key")
	}
	signature, err := requireHexField(decoded, "Signature")
	if err != nil {
		return nil, err
	}
	m.signingKey = signing
	m.signature = signature
	m.signingPreimage, err = signingPreimageFromDecoded(decoded)
	if err != nil {
		return nil, fmt.Errorf("manifest: build signing preimage: %w", err)
	}
	return m, nil
}

// Verify checks both the master signature and (for non-revoked
// manifests) the ephemeral-key signature against the canonical signing
// preimage: HashPrefix("MAN\0") || STObject(manifest without Signature
// and MasterSignature).
func (m *Manifest) Verify() error {
	if m == nil {
		return errors.New("manifest: nil manifest")
	}
	if m.masterSignature == "" {
		return errors.New("manifest: MasterSignature missing on verify")
	}
	if !m.Revoked() && m.signature == "" {
		return errors.New("manifest: Signature missing on verify")
	}
	if !VerifyKeyTypeSignature(m.masterKey, m.signingPreimage, m.masterSignature) {
		return errors.New("manifest: master signature invalid")
	}
	if !m.Revoked() {
		if !VerifyKeyTypeSignature(m.signingKey, m.signingPreimage, m.signature) {
			return errors.New("manifest: ephemeral signature invalid")
		}
	}
	return nil
}

// signingPreimageFromDecoded returns HashPrefix("MAN\0") || STObject
// (manifest without signing fields). The caller owns `decoded`; this
// function mutates it (deletes non-signing fields), so callers that
// still need the original map must pass a clone.
func signingPreimageFromDecoded(decoded map[string]any) ([]byte, error) {
	for k := range decoded {
		fi, _ := definitions.Get().FieldInstanceByName(k)
		if fi != nil && !fi.IsSigningField {
			delete(decoded, k)
		}
	}
	body, err := binarycodec.EncodeBytes(decoded)
	if err != nil {
		return nil, err
	}
	prefix := protocol.HashPrefixManifest()
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out, nil
}

// VerifyKeyTypeSignature verifies sigHex (hex-encoded) over message using
// pubKey, dispatching on the 33-byte key's type prefix (ed25519 vs
// secp256k1). The raw message bytes are passed as a Go string (the crypto
// packages treat string as an opaque byte sequence). secp256k1 requires a
// fully-canonical (low-S) signature. Returns false for an unrecognized key
// type or malformed signature.
func VerifyKeyTypeSignature(pubKey [33]byte, message []byte, sigHex string) bool {
	pubHex := hex.EncodeToString(pubKey[:])
	switch crypto.PublicKeyType(pubKey[:]) {
	case crypto.KeyTypeEd25519:
		return ed25519.Algorithm{}.Validate(string(message), pubHex, sigHex)
	case crypto.KeyTypeSecp256k1:
		return secp256k1.Algorithm{}.Validate(string(message), pubHex, sigHex)
	default:
		return false
	}
}

func decodeKey(hexStr string) ([33]byte, error) {
	var out [33]byte
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return out, err
	}
	if len(b) != 33 {
		return out, fmt.Errorf("expected 33 bytes, got %d", len(b))
	}
	if crypto.PublicKeyType(b) == crypto.KeyTypeUnknown {
		return out, errors.New("unknown key type prefix")
	}
	copy(out[:], b)
	return out, nil
}

func requireHexField(m map[string]any, name string) (string, error) {
	raw, ok := m[name]
	if !ok {
		return "", fmt.Errorf("manifest: missing required %s", name)
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("manifest: %s is not a string", name)
	}
	return s, nil
}

func hasField(m map[string]any, name string) bool {
	_, ok := m[name]
	return ok
}
