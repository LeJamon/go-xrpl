// Package manifest implements validator manifest parsing, verification, and
// caching — the equivalent of rippled's ValidatorManifests service.
//
// A manifest binds a validator's long-term master key to a rotatable
// ephemeral signing key. Peers gossip manifests so every node on the
// network can translate an ephemeral signing key used in a validation or
// proposal back to its master key for UNL / quorum decisions. Without
// this translation a validator that rotates its ephemeral key appears as
// a new untrusted node and breaks mainnet quorum arithmetic.
//
// Wire format (rippled Manifest.cpp:53-164):
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

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/definitions"
	"github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/protocol"
)

// RevokedSequence marks a manifest as a master-key revocation.
// Rippled: Manifest::revoked returns sequence == numeric_limits::max.
const RevokedSequence uint32 = math.MaxUint32

// Manifest is a parsed, syntactically-valid validator manifest.
// Signature verification is separate — callers invoke Verify before
// trusting the struct's key bindings.
type Manifest struct {
	// MasterKey is the 33-byte master public key (ed25519 0xED prefix or
	// secp256k1 0x02/0x03 prefix).
	MasterKey [33]byte

	// SigningKey is the 33-byte ephemeral signing key. Zero when Revoked.
	SigningKey [33]byte

	// Sequence is the manifest's monotonic counter. RevokedSequence
	// (MaxUint32) indicates master-key revocation; in that case
	// SigningKey is zero and both Signature and MasterSignature are
	// present only on the master side.
	Sequence uint32

	// Domain is the optional TOML domain string.
	Domain string

	// Serialized is the original wire bytes — kept so the cache can
	// relay the exact payload peers gossiped and the RPC can return it.
	Serialized []byte
}

// Revoked reports whether the manifest revokes its master key.
func (m *Manifest) Revoked() bool {
	return m.Sequence == RevokedSequence
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

	decoded, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return nil, fmt.Errorf("manifest: decode STObject: %w", err)
	}

	// Version default is 0; anything else is an unsupported manifest
	// format per rippled Manifest.cpp:92-93.
	if raw, ok := decoded["Version"]; ok {
		v, ok := toUint32(raw)
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
	seq, ok := toUint32(seqRaw)
	if !ok {
		return nil, errors.New("manifest: Sequence is not numeric")
	}

	if _, err := requireHexField(decoded, "MasterSignature"); err != nil {
		return nil, err
	}

	m := &Manifest{
		MasterKey:  master,
		Sequence:   seq,
		Serialized: append([]byte(nil), data...),
	}

	if dom, ok := decoded["Domain"]; ok {
		if s, ok := dom.(string); ok {
			// Domain is VL-encoded as bytes; the codec returns a hex
			// string. Decode it back to the raw UTF-8 text.
			b, err := hex.DecodeString(s)
			if err != nil {
				return nil, fmt.Errorf("manifest: Domain not hex: %w", err)
			}
			m.Domain = string(b)
		}
	}

	hasSigningKey := hasField(decoded, "SigningPubKey")
	hasSignature := hasField(decoded, "Signature")

	if m.Revoked() {
		if hasSigningKey || hasSignature {
			return nil, errors.New("manifest: revoked manifest must not carry ephemeral fields")
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
	m.SigningKey = signing
	return m, nil
}

// Verify checks both the master signature and (for non-revoked
// manifests) the ephemeral-key signature against the canonical signing
// preimage: HashPrefix("MAN\0") || STObject(manifest without Signature
// and MasterSignature). Mirrors Manifest::verify at Manifest.cpp:195-214.
func (m *Manifest) Verify() error {
	preimage, err := signingPreimage(m.Serialized)
	if err != nil {
		return fmt.Errorf("manifest: build signing preimage: %w", err)
	}
	// Re-decode to extract signature fields — small cost, keeps the
	// Manifest struct free of intermediate parser state.
	decoded, err := binarycodec.Decode(hex.EncodeToString(m.Serialized))
	if err != nil {
		return fmt.Errorf("manifest: re-decode for verify: %w", err)
	}
	masterSigHex, _ := decoded["MasterSignature"].(string)
	if masterSigHex == "" {
		return errors.New("manifest: MasterSignature missing on verify")
	}
	if !verifySignature(m.MasterKey, preimage, masterSigHex) {
		return errors.New("manifest: master signature invalid")
	}
	if !m.Revoked() {
		sigHex, _ := decoded["Signature"].(string)
		if sigHex == "" {
			return errors.New("manifest: Signature missing on verify")
		}
		if !verifySignature(m.SigningKey, preimage, sigHex) {
			return errors.New("manifest: ephemeral signature invalid")
		}
	}
	return nil
}

// signingPreimage returns HashPrefix("MAN\0") || STObject(manifest
// without signing fields). Mirrors rippled's ripple::verify pattern
// (Sign.cpp:47-62) where ss.add32(prefix) precedes
// st.addWithoutSigningFields(ss).
func signingPreimage(serialized []byte) ([]byte, error) {
	decoded, err := binarycodec.Decode(hex.EncodeToString(serialized))
	if err != nil {
		return nil, err
	}
	for k := range decoded {
		fi, _ := definitions.Get().GetFieldInstanceByFieldName(k)
		if fi != nil && !fi.IsSigningField {
			delete(decoded, k)
		}
	}
	encoded, err := binarycodec.Encode(decoded)
	if err != nil {
		return nil, err
	}
	body, err := hex.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	prefix := protocol.HashPrefixManifest
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix[:]...)
	out = append(out, body...)
	return out, nil
}

// verifySignature dispatches to the key-type-specific verifier. The raw
// message bytes are passed as a Go string (the crypto packages treat
// string as an opaque byte sequence); signature is hex-encoded.
func verifySignature(pubKey [33]byte, message []byte, sigHex string) bool {
	pubHex := hex.EncodeToString(pubKey[:])
	switch crypto.PublicKeyType(pubKey[:]) {
	case crypto.KeyTypeEd25519:
		return ed25519.ED25519().Validate(string(message), pubHex, sigHex)
	case crypto.KeyTypeSecp256k1:
		// Manifest signatures are not required to be fully canonical
		// (rippled verifies without the fully-canonical gate at
		// Sign.cpp:47-62 → PublicKey::verify → secp256k1_ecdsa_verify
		// with no low-S check). ValidateWithCanonicality(false) matches.
		return secp256k1.SECP256K1().ValidateWithCanonicality(string(message), pubHex, sigHex, false)
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
	v, ok := m[name]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		// Non-string fields (numeric) are always "present" if the key
		// is in the map.
		return true
	}
	return s != ""
}

// toUint32 accepts the several numeric shapes the JSON map may contain
// for a UInt32 field (float64 from json.Unmarshal, int / int64 from
// some codec paths, uint32/uint64 direct).
func toUint32(v any) (uint32, bool) {
	switch t := v.(type) {
	case uint32:
		return t, true
	case uint64:
		return uint32(t), true
	case int:
		if t < 0 {
			return 0, false
		}
		return uint32(t), true
	case int64:
		if t < 0 {
			return 0, false
		}
		return uint32(t), true
	case float64:
		if t < 0 {
			return 0, false
		}
		return uint32(t), true
	default:
		return 0, false
	}
}
