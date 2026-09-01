package addresscodec

import (
	"bytes"
)

var (
	// mainnetXAddressPrefix is the prefix for mainnet X-address encoding.
	mainnetXAddressPrefix = []byte{0x05, 0x44}
	// testnetXAddressPrefix is the prefix for testnet X-address encoding.
	testnetXAddressPrefix = []byte{0x04, 0x93}
)

// XAddressLength is the length of an X-address (35 bytes).
const XAddressLength = 35

// Network selects which XRPL network an X-address encodes for.
type Network uint8

const (
	// Mainnet is the production XRPL network.
	Mainnet Network = iota
	// Testnet is the XRPL test network.
	Testnet
)

// String returns the network's name.
func (n Network) String() string {
	if n == Testnet {
		return "testnet"
	}
	return "mainnet"
}

// IsValidXAddress returns true if the x-address is valid. Otherwise, it returns false.
func IsValidXAddress(xAddress string) bool {
	_, _, _, err := DecodeXAddress(xAddress)
	return err == nil
}

// EncodeXAddress returns the x-address encoding of accountID for the given
// network. A nil tag encodes an X-address without a destination tag; a non-nil
// tag encodes its value. It returns an error if accountID is not 20 bytes long.
func EncodeXAddress(accountID []byte, tag *uint32, network Network) (string, error) {
	if len(accountID) != AccountAddressLength {
		return "", ErrInvalidAccountID
	}

	xAddressBytes := make([]byte, 0, XAddressLength)

	if network == Testnet {
		xAddressBytes = append(xAddressBytes, testnetXAddressPrefix...)
	} else {
		xAddressBytes = append(xAddressBytes, mainnetXAddressPrefix...)
	}

	xAddressBytes = append(xAddressBytes, accountID...)

	var tagValue uint32
	if tag != nil {
		xAddressBytes = append(xAddressBytes, byte(1))
		tagValue = *tag
	} else {
		xAddressBytes = append(xAddressBytes, byte(0))
	}

	xAddressBytes = append(
		xAddressBytes,
		byte(tagValue&0xff),
		byte((tagValue>>8)&0xff),
		byte((tagValue>>16)&0xff),
		byte((tagValue>>24)&0xff),
		0,
		0,
		0,
		0,
	)

	cksum := checksum(xAddressBytes)
	xAddressBytes = append(xAddressBytes, cksum[:]...)

	return EncodeBase58(xAddressBytes), nil
}

// DecodeXAddress returns the accountID, tag, and network of the x-address.
// If the x-address is invalid, it returns an error.
func DecodeXAddress(xAddress string) (accountID []byte, tag uint32, network Network, err error) {
	accountID, tagPtr, network, err := DecodeXAddressWithTagPresence(xAddress)
	if err != nil {
		return nil, 0, Mainnet, err
	}
	if tagPtr != nil {
		tag = *tagPtr
	}
	return accountID, tag, network, nil
}

// DecodeXAddressWithTagPresence returns the accountID, optional tag, and
// network of the x-address. A nil tag means the X-address has no tag; a
// non-nil tag preserves an explicitly encoded zero value.
func DecodeXAddressWithTagPresence(xAddress string) (accountID []byte, tag *uint32, network Network, err error) {
	xAddressBytes, err := Base58CheckDecode(xAddress)
	if err != nil {
		return nil, nil, Mainnet, err
	}

	// Verify length (2 prefix + 20 accountID + 1 flag + 8 tag bytes = 31)
	if len(xAddressBytes) != 31 {
		return nil, nil, Mainnet, ErrInvalidXAddress
	}

	switch {
	case bytes.HasPrefix(xAddressBytes, mainnetXAddressPrefix):
		network = Mainnet
	case bytes.HasPrefix(xAddressBytes, testnetXAddressPrefix):
		network = Testnet
	default:
		return nil, nil, Mainnet, ErrInvalidXAddress
	}

	tagValue, hasTag, err := decodeTag(xAddressBytes)
	if err != nil {
		return nil, nil, Mainnet, err
	}
	if hasTag {
		tag = &tagValue
	}

	return xAddressBytes[2:22], tag, network, nil
}

// XAddressToClassicAddress converts the x-address to a classic address.
// It returns the classic address, tag and network.
// If the x-address is invalid, it returns an error.
func XAddressToClassicAddress(xAddress string) (classicAddress string, tag uint32, network Network, err error) {
	classicAddress, tagPtr, network, err := XAddressToClassicAddressWithTagPresence(xAddress)
	if err != nil {
		return "", 0, Mainnet, err
	}
	if tagPtr != nil {
		tag = *tagPtr
	}
	return classicAddress, tag, network, nil
}

// XAddressToClassicAddressWithTagPresence converts the x-address to a classic
// address while preserving whether its optional tag is present.
func XAddressToClassicAddressWithTagPresence(xAddress string) (classicAddress string, tag *uint32, network Network, err error) {
	accountID, tag, network, err := DecodeXAddressWithTagPresence(xAddress)
	if err != nil {
		return "", nil, Mainnet, err
	}

	classicAddress, err = EncodeAccountIDToClassicAddress(accountID)
	if err != nil {
		return "", nil, Mainnet, err
	}

	return classicAddress, tag, network, nil
}

// ClassicAddressToXAddress converts the classic address to an x-address for the
// given network. A nil tag omits the destination tag; a non-nil tag encodes its
// value. It returns an error if the classic address is invalid.
func ClassicAddressToXAddress(address string, tag *uint32, network Network) (string, error) {
	_, accountID, err := DecodeClassicAddressToAccountID(address)
	if err != nil {
		return "", err
	}

	return EncodeXAddress(accountID, tag, network)
}

// decodeTag returns the tag from the x-address.
// If the tag is invalid, it returns an error.
func decodeTag(xAddressBytes []byte) (uint32, bool, error) {
	flag := xAddressBytes[22]
	if flag >= 2 {
		// No support for 64-bit tags at this time
		return 0, false, ErrUnsupportedXAddress
	}
	reservedStart := 23
	if flag == 1 {
		reservedStart = 27
	}
	for i := reservedStart; i < 31; i++ {
		if xAddressBytes[i] != 0 {
			return 0, false, ErrInvalidTag
		}
	}
	if flag == 1 {
		// Little-endian to big-endian (4 bytes for full 32-bit tag support)
		tag := uint32(xAddressBytes[23]) +
			uint32(xAddressBytes[24])*0x100 +
			uint32(xAddressBytes[25])*0x10000 +
			uint32(xAddressBytes[26])*0x1000000
		return tag, true, nil
	}
	return 0, false, nil
}
