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

// validatorKeyValid reports whether raw is a 33-byte master pubkey with
// a recognized key-type prefix. Used by applyAcceptedLocked to silently
// skip malformed validator entries — mirrors rippled
// ValidatorList.cpp:1250-1273 which logs and skips bad entries without
// rejecting the surrounding list.
func validatorKeyValid(raw []byte) bool {
	if len(raw) != 33 {
		return false
	}
	return crypto.PublicKeyType(raw) != crypto.KeyTypeUnknown
}

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

// MaxSupportedBlobs caps how many per-blob entries a v2 collection may
// carry. Matches rippled ValidatorList.h:272
// `static constexpr std::size_t maxSupportedBlobs = 5;`. A peer that
// sends more than this is treated as Malformed before any signature
// verification work.
const MaxSupportedBlobs = 5

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
// payload, and returns the parsed structure. Every parse / structure
// failure returns Invalid — matching rippled ValidatorList.cpp:1390-1437
// which folds bad-base64, bad-JSON, missing-required-field, wrong-type,
// and validity-window violations all into ListDisposition::invalid.
//
// Required fields (rippled lines 1394-1397): `sequence` (int),
// `expiration` (int), `validators` (array). `effective` is optional but
// must be an integer when present. Absence is a hard reject — the
// silent-zero-coercion json.Unmarshal default would let a malformed
// publisher feed produce an empty trusted set without ever surfacing
// the error.
//
// Per-entry validator pubkey validation is intentionally deferred to
// applyAcceptedLocked: rippled at ValidatorList.cpp:1250-1273 logs and
// silently skips malformed entries rather than rejecting the surrounding
// blob, so a publisher mistake on one entry does not poison the whole
// list.
func parseBlob(rawBlob []byte) (*blobJSON, Disposition, error) {
	decoded, err := decodeBase64Tolerant(rawBlob)
	if err != nil {
		return nil, Invalid, fmt.Errorf("blob base64 decode: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return nil, Invalid, fmt.Errorf("blob JSON unmarshal: %w", err)
	}
	seqRaw, hasSeq := raw["sequence"]
	expRaw, hasExp := raw["expiration"]
	valsRaw, hasVals := raw["validators"]
	if !hasSeq || !hasExp || !hasVals {
		return nil, Invalid, fmt.Errorf("blob missing required field(s); have sequence=%t expiration=%t validators=%t", hasSeq, hasExp, hasVals)
	}
	b := blobJSON{}
	if err := json.Unmarshal(seqRaw, &b.Sequence); err != nil {
		return nil, Invalid, fmt.Errorf("blob sequence not uint32: %w", err)
	}
	if err := json.Unmarshal(expRaw, &b.Expiration); err != nil {
		return nil, Invalid, fmt.Errorf("blob expiration not uint32: %w", err)
	}
	if effRaw, ok := raw["effective"]; ok {
		if err := json.Unmarshal(effRaw, &b.Effective); err != nil {
			return nil, Invalid, fmt.Errorf("blob effective not uint32: %w", err)
		}
	}
	if err := json.Unmarshal(valsRaw, &b.Validators); err != nil {
		return nil, Invalid, fmt.Errorf("blob validators not array: %w", err)
	}
	if b.Expiration <= b.Effective {
		return nil, Invalid, fmt.Errorf("blob expiration %d <= effective %d", b.Expiration, b.Effective)
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

// decodeBase64Tolerant decodes a standard base64-encoded payload,
// padded or unpadded. Rippled's `base64_decode` uses the standard
// alphabet only (`base64.cpp:191-234`); URL-safe characters `-` and `_`
// are rejected. Accepting them in goXRPL would let a malicious peer
// craft a payload that this implementation accepts and rippled rejects
// (or vice versa), diverging hash-routing and relay decisions across
// implementations.
func decodeBase64Tolerant(payload []byte) ([]byte, error) {
	s := string(payload)
	if out, err := base64.StdEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	if out, err := base64.RawStdEncoding.DecodeString(s); err == nil {
		return out, nil
	}
	return nil, errors.New("payload is not valid standard base64")
}

// rippleSecondsToUnix converts a ripple-epoch second count (the form
// validator blobs use for expiration / effective) into a Unix epoch
// second count. Pure arithmetic; no time.Time round-trip so the result
// is deterministic across machines with different time zones.
func rippleSecondsToUnix(rippleSec uint32) int64 {
	return int64(rippleSec) + rippleEpochOffset
}
