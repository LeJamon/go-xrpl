package list

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/goXRPLd/crypto"
	"github.com/LeJamon/goXRPLd/crypto/ed25519"
	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
)

// rippleEpochOffset is the gap between Unix epoch (1970) and the XRPL
// ripple epoch (2000-01-01 UTC). Validator-list expiration / effective
// fields are encoded as seconds-since-ripple-epoch. Mirrors rippled's
// TimeKeeper time_point arithmetic at TimeKeeper.h.
const rippleEpochOffset int64 = 946684800

// SupportedVersions enumerates the protocol versions this aggregator
// accepts. Version 1 is the original single-blob format; version 2 adds
// the blobs_v2 collection used for forward-dated lists. Anything else
// is treated as UnsupportedVersion.
var SupportedVersions = []uint32{1, 2}

// blobJSON is the schema published in vl.ripple.com-style envelopes and
// embedded in TMValidatorList / TMValidatorListCollection messages.
// Decoded from the base64-encoded blob bytes.
//
// Fields beyond these are tolerated and ignored — rippled's JSON parser
// is lenient and forward-compatibility relies on it.
type blobJSON struct {
	Sequence   uint32        `json:"sequence"`
	Expiration uint32        `json:"expiration"`
	Effective  uint32        `json:"effective,omitempty"`
	Validators []blobEntryJS `json:"validators"`
}

// blobEntryJS is a single validator entry inside a publisher blob. The
// validation_public_key is the validator's 33-byte compressed master
// pubkey in hex. The optional manifest carries the validator's current
// manifest STObject (base64-encoded) — peers without it would otherwise
// be unable to translate the ephemeral signing key back to the master.
type blobEntryJS struct {
	ValidationPublicKey string `json:"validation_public_key"`
	Manifest            string `json:"manifest,omitempty"`
}

// parseBlob decodes the base64-encoded blob, JSON-unmarshals the inner
// payload, and returns the parsed structure. Returns Malformed when the
// outer encoding is broken (not base64 / not JSON), Invalid when the
// structural invariants of the inner JSON fail (missing fields,
// expiration <= effective, validator pubkey isn't 33 bytes of a known
// key type).
func parseBlob(rawBlob []byte) (*blobJSON, Disposition, error) {
	decoded, err := decodeBase64Tolerant(rawBlob)
	if err != nil {
		return nil, Malformed, fmt.Errorf("blob base64 decode: %w", err)
	}
	var b blobJSON
	if err := json.Unmarshal(decoded, &b); err != nil {
		return nil, Malformed, fmt.Errorf("blob JSON unmarshal: %w", err)
	}
	if b.Sequence == 0 {
		return nil, Invalid, errors.New("blob missing or zero sequence")
	}
	if b.Expiration == 0 {
		return nil, Invalid, errors.New("blob missing or zero expiration")
	}
	if b.Expiration <= b.Effective {
		return nil, Invalid, fmt.Errorf("blob expiration %d <= effective %d", b.Expiration, b.Effective)
	}
	if len(b.Validators) == 0 {
		return nil, Invalid, errors.New("blob carries no validators")
	}
	for i, v := range b.Validators {
		raw, err := hex.DecodeString(v.ValidationPublicKey)
		if err != nil {
			return nil, Invalid, fmt.Errorf("validator[%d] pubkey not hex: %w", i, err)
		}
		if len(raw) != 33 {
			return nil, Invalid, fmt.Errorf("validator[%d] pubkey is %d bytes, want 33", i, len(raw))
		}
		if crypto.PublicKeyType(raw) == crypto.KeyTypeUnknown {
			return nil, Invalid, fmt.Errorf("validator[%d] pubkey has unknown key-type prefix", i)
		}
	}
	return &b, Accepted, nil
}

// verifyBlobSignature checks that `signature` (hex-encoded) is a valid
// signature by `signingKey` over the base64-decoded blob (the inner
// JSON payload). Mirrors rippled ValidatorList.cpp:1385-1388, which
// signs `base64_decode(blob)` — not the on-wire base64 bytes.
func verifyBlobSignature(signingKey [33]byte, blob, signatureHex []byte) error {
	decoded, err := decodeBase64Tolerant(blob)
	if err != nil {
		return fmt.Errorf("blob decode for signature verify: %w", err)
	}
	sigHexStr := string(signatureHex)
	if _, err := hex.DecodeString(sigHexStr); err != nil {
		return fmt.Errorf("signature not hex: %w", err)
	}
	pubHex := hex.EncodeToString(signingKey[:])
	switch crypto.PublicKeyType(signingKey[:]) {
	case crypto.KeyTypeEd25519:
		if !ed25519.ED25519().Validate(string(decoded), pubHex, sigHexStr) {
			return errors.New("ed25519 signature invalid")
		}
		return nil
	case crypto.KeyTypeSecp256k1:
		// Rippled verify() requires fully-canonical signatures
		// (PublicKey::verify default). Match by passing canonicality=true.
		if !secp256k1.SECP256K1().ValidateWithCanonicality(string(decoded), pubHex, sigHexStr, true) {
			return errors.New("secp256k1 signature invalid")
		}
		return nil
	default:
		return errors.New("unknown signing key type")
	}
}

// decodeBase64Tolerant decodes a base64-encoded payload that may be in
// either the standard or URL-safe variant, and may or may not include
// padding. Real publisher JSON envelopes consistently use standard
// padded base64, but rippled accepts unpadded too via boost::beast's
// detail::base64::decode (lenient). Match the same tolerance.
func decodeBase64Tolerant(payload []byte) ([]byte, error) {
	s := string(payload)
	if out, err := base64.StdEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	if out, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	if out, err := base64.URLEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	if out, err := base64.RawURLEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	return nil, errors.New("payload is not valid base64 in any variant")
}

// rippleSecondsToUnix converts a ripple-epoch second count (the form
// validator blobs use for expiration / effective) into a Unix epoch
// second count. Pure arithmetic; no time.Time round-trip so the result
// is deterministic across machines with different time zones.
func rippleSecondsToUnix(rippleSec uint32) int64 {
	return int64(rippleSec) + rippleEpochOffset
}
