package state

import addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"

// EncodeAccountID encodes a 20-byte account ID to a classic address string
func EncodeAccountID(accountID [20]byte) (string, error) {
	return addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
}

// DecodeAccountID decodes a classic address string to a 20-byte account ID
func DecodeAccountID(address string) ([20]byte, error) {
	var result [20]byte
	_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return result, err
	}
	copy(result[:], accountID)
	return result, nil
}

// decodeAccountID is the unexported version of DecodeAccountID
func decodeAccountID(address string) ([20]byte, error) {
	return DecodeAccountID(address)
}
