package types

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// AssetString reads a JSON string with the NUL termination used by RPC asset fields.
func AssetString(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	value, _, _ = strings.Cut(value, "\x00")
	return value, true
}

// ParseIssuer accepts a classic address, a 160-bit hexadecimal account ID, or "0".
func ParseIssuer(value string) ([20]byte, bool) {
	var issuer [20]byte
	if value == "0" {
		return issuer, true
	}
	if len(value) == hex.EncodedLen(len(issuer)) {
		if _, err := hex.Decode(issuer[:], []byte(value)); err == nil {
			return issuer, true
		}
	}
	_, account, err := addresscodec.DecodeClassicAddressToAccountID(value)
	if err != nil {
		return [20]byte{}, false
	}
	copy(issuer[:], account)
	return issuer, true
}
