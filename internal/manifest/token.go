package manifest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ValidatorToken is the parsed payload of a rippled-format
// `[validator_token]` config block, as produced by `validator-keys-tool`.
//
// Wire format (rippled Manifest.cpp:263-308): the block is a list of
// whitespace-trimmed lines that concatenate into a single base64 string.
// Decoding the base64 yields a JSON object with two string fields:
//
//	{
//	    "manifest":              "<base64 STObject>",
//	    "validation_secret_key": "<hex 32-byte secp256k1 secret>"
//	}
//
// Both fields are mandatory. The manifest's embedded SigningPubKey must
// match secp256k1·G·validation_secret_key — that pairing check is the
// caller's responsibility (see ValidatorIdentity construction).
type ValidatorToken struct {
	// ManifestB64 is the manifest serialization, still base64-encoded.
	// Kept as the raw string so callers re-emitting the token don't
	// have to round-trip the bytes.
	ManifestB64 string

	// ValidationSecret is the 32-byte secp256k1 secret key used to sign
	// validations and proposals. Decoded from the JSON's hex field.
	ValidationSecret [32]byte
}

// LoadValidatorToken parses a `[validator_token]` config block. The block
// is the raw config-section text — leading/trailing whitespace and
// embedded newlines are tolerated and stripped before base64 decoding,
// matching rippled's loadValidatorToken.
func LoadValidatorToken(block string) (*ValidatorToken, error) {
	// Concatenate lines after trimming whitespace so a multi-line
	// pretty-printed block decodes identically to a single-line one.
	var sb strings.Builder
	for _, line := range strings.Split(block, "\n") {
		sb.WriteString(strings.TrimSpace(line))
	}
	concat := sb.String()
	if concat == "" {
		return nil, errors.New("validator_token: empty block")
	}

	decoded, err := base64.StdEncoding.DecodeString(concat)
	if err != nil {
		return nil, fmt.Errorf("validator_token: base64 decode: %w", err)
	}

	var raw struct {
		Manifest            string `json:"manifest"`
		ValidationSecretKey string `json:"validation_secret_key"`
	}
	if err := json.Unmarshal(decoded, &raw); err != nil {
		return nil, fmt.Errorf("validator_token: json decode: %w", err)
	}
	if raw.Manifest == "" {
		return nil, errors.New("validator_token: missing manifest")
	}
	if raw.ValidationSecretKey == "" {
		return nil, errors.New("validator_token: missing validation_secret_key")
	}

	keyBytes, err := hex.DecodeString(raw.ValidationSecretKey)
	if err != nil {
		return nil, fmt.Errorf("validator_token: validation_secret_key not hex: %w", err)
	}
	if len(keyBytes) != 32 {
		return nil, fmt.Errorf("validator_token: validation_secret_key wrong length %d", len(keyBytes))
	}

	tok := &ValidatorToken{ManifestB64: raw.Manifest}
	copy(tok.ValidationSecret[:], keyBytes)
	return tok, nil
}

// DecodeManifest base64-decodes the embedded manifest blob into wire
// bytes ready for Deserialize. Separated so callers that already have
// a ValidatorToken can reuse the same decode path.
func (t *ValidatorToken) DecodeManifest() ([]byte, error) {
	if t == nil || t.ManifestB64 == "" {
		return nil, errors.New("validator_token: nil or empty manifest")
	}
	b, err := base64.StdEncoding.DecodeString(t.ManifestB64)
	if err != nil {
		return nil, fmt.Errorf("validator_token: manifest base64: %w", err)
	}
	return b, nil
}
