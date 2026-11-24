package types

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
)

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
