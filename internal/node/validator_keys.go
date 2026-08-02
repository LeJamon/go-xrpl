package node

import "github.com/LeJamon/go-xrpl/codec/addresscodec"

func validatorKeyBase58(key [33]byte) (string, bool) {
	encoded, err := addresscodec.EncodeNodePublicKey(key[:])
	return encoded, err == nil
}

func validatorKeysBase58(keys [][33]byte) []string {
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := validatorKeyBase58(key); ok {
			encoded = append(encoded, value)
		}
	}
	return encoded
}
