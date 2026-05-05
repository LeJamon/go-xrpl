package manifest

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

// rippledFixtureBlock is the multi-line `[validator_token]` payload from
// rippled's Manifest_test.cpp:520-538. Reproduced verbatim (with the
// embedded whitespace + tabs) so we round-trip the same input rippled
// uses to assert loadValidatorToken behavior.
const rippledFixtureBlock = "    eyJ2YWxpZGF0aW9uX3NlY3JldF9rZXkiOiI5ZWQ0NWY4NjYyNDFjYzE4YTI3NDdiNT\n" +
	" \tQzODdjMDYyNTkwNzk3MmY0ZTcxOTAyMzFmYWE5Mzc0NTdmYTlkYWY2IiwibWFuaWZl     \n" +
	"\tc3QiOiJKQUFBQUFGeEllMUZ0d21pbXZHdEgyaUNjTUpxQzlnVkZLaWxHZncxL3ZDeE\n" +
	"\t hYWExwbGMyR25NaEFrRTFhZ3FYeEJ3RHdEYklENk9NU1l1TTBGREFscEFnTms4U0tG\t  \t\n" +
	"bjdNTzJmZGtjd1JRSWhBT25ndTlzQUtxWFlvdUorbDJWMFcrc0FPa1ZCK1pSUzZQU2\n" +
	"hsSkFmVXNYZkFpQnNWSkdlc2FhZE9KYy9hQVpva1MxdnltR21WcmxIUEtXWDNZeXd1\n" +
	"NmluOEhBU1FLUHVnQkQ2N2tNYVJGR3ZtcEFUSGxHS0pkdkRGbFdQWXk1QXFEZWRGdj\n" +
	"VUSmEydzBpMjFlcTNNWXl3TFZKWm5GT3I3QzBrdzJBaVR6U0NqSXpkaXRROD0ifQ==\n"

const rippledFixtureExpectedManifest = "JAAAAAFxIe1FtwmimvGtH2iCcMJqC9gVFKilGfw1/" +
	"vCxHXXLplc2GnMhAkE1agqXxBwDwDbID6OMSYuM0FDAlpAgNk8SKFn7MO2fdkcwRQIhAOngu9sA" +
	"KqXYouJ+l2V0W+sAOkVB+ZRS6PShlJAfUsXfAiBsVJGesaadOJc/aAZokS1vymGmVrlHPKWX3Y" +
	"ywu6in8HASQKPugBD67kMaRFGvmpATHlGKJdvDFlWPYy5AqDedFv5TJa2w0i21eq3MYywLVJZn" +
	"FOr7C0kw2AiTzSCjIzditQ8="

const rippledFixtureExpectedSecretHex = "9ed45f866241cc18a2747b54387c0625907972f4e7190231faa937457fa9daf6"

func TestLoadValidatorToken_RippledFixture(t *testing.T) {
	tok, err := LoadValidatorToken(rippledFixtureBlock)
	if err != nil {
		t.Fatalf("LoadValidatorToken: %v", err)
	}
	if tok.ManifestB64 != rippledFixtureExpectedManifest {
		t.Errorf("ManifestB64 mismatch:\n got: %s\nwant: %s", tok.ManifestB64, rippledFixtureExpectedManifest)
	}
	gotHex := hex.EncodeToString(tok.ValidationSecret[:])
	if gotHex != rippledFixtureExpectedSecretHex {
		t.Errorf("ValidationSecret mismatch:\n got: %s\nwant: %s", gotHex, rippledFixtureExpectedSecretHex)
	}
}

func TestLoadValidatorToken_DecodeManifest(t *testing.T) {
	tok, err := LoadValidatorToken(rippledFixtureBlock)
	if err != nil {
		t.Fatalf("LoadValidatorToken: %v", err)
	}
	wire, err := tok.DecodeManifest()
	if err != nil {
		t.Fatalf("DecodeManifest: %v", err)
	}
	// The manifest is a non-empty STObject. Pass it through Deserialize
	// to confirm the embedded wire bytes are well-formed: catches base64
	// truncation / corruption of the JSON manifest field.
	m, err := Deserialize(wire)
	if err != nil {
		t.Fatalf("Deserialize embedded manifest: %v", err)
	}
	if m.Sequence == 0 {
		t.Errorf("expected non-zero manifest sequence, got 0")
	}
}

func TestLoadValidatorToken_BadInputs(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"only_whitespace", "   \n\t\n"},
		{"not_base64", "bad token"},
		{"valid_base64_not_json", "Zm9vYmFy"}, // "foobar"
		{"json_missing_manifest", base64Encode(`{"validation_secret_key":"00"}`)},
		{"json_missing_secret", base64Encode(`{"manifest":"abc"}`)},
		{"secret_not_hex", base64Encode(`{"manifest":"abc","validation_secret_key":"NOTHEX"}`)},
		{"secret_wrong_length", base64Encode(`{"manifest":"abc","validation_secret_key":"00"}`)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := LoadValidatorToken(tt.in); err == nil {
				t.Errorf("expected error for %s", tt.name)
			}
		})
	}
}

// TestLoadValidatorToken_RoundTrip builds a token blob the way
// validator-keys-tool would (pretty-printed JSON, base64-wrapped) and
// confirms LoadValidatorToken recovers the same fields.
func TestLoadValidatorToken_RoundTrip(t *testing.T) {
	manifest := "JAAAAAFx" // arbitrary placeholder, parser doesn't decode it
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(0x10 + i)
	}
	jsonBlob, err := json.Marshal(map[string]string{
		"manifest":              manifest,
		"validation_secret_key": hex.EncodeToString(secret),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	encoded := base64.StdEncoding.EncodeToString(jsonBlob)
	// Wrap at 60-char lines with a leading indent — mirrors the
	// formatting validator-keys-tool emits for human-readable configs.
	var multi strings.Builder
	for i := 0; i < len(encoded); i += 60 {
		end := i + 60
		if end > len(encoded) {
			end = len(encoded)
		}
		multi.WriteString("   ")
		multi.WriteString(encoded[i:end])
		multi.WriteString("\n")
	}

	tok, err := LoadValidatorToken(multi.String())
	if err != nil {
		t.Fatalf("LoadValidatorToken: %v", err)
	}
	if tok.ManifestB64 != manifest {
		t.Errorf("ManifestB64: got %q want %q", tok.ManifestB64, manifest)
	}
	if !bytesEqual(tok.ValidationSecret[:], secret) {
		t.Errorf("ValidationSecret: got %x want %x", tok.ValidationSecret[:], secret)
	}
}

func base64Encode(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
