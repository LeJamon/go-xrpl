package addresscodec_test

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// A classic address round-trips to the same account ID it was derived from.
func ExampleEncodeAccountIDToClassicAddress() {
	var accountID [20]byte // 20-byte account ID (zero value for illustration)

	address, err := addresscodec.EncodeAccountIDToClassicAddress(accountID[:])
	if err != nil {
		panic(err)
	}

	_, decoded, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		panic(err)
	}

	fmt.Println("valid:", addresscodec.IsValidClassicAddress(address))
	fmt.Println("round-trips:", string(accountID[:]) == string(decoded))
	// Output:
	// valid: true
	// round-trips: true
}
