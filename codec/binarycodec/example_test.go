package binarycodec_test

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
)

// Encode serializes a transaction's JSON representation into the canonical XRPL
// binary format (returned as an uppercase hex string). Fields are emitted in the
// protocol's deterministic order regardless of map iteration order.
func ExampleEncode() {
	hex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "Payment",
		"Sequence":        uint32(1),
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(hex)
	// Output: 1200002400000001
}

// Decode reverses Encode, parsing canonical binary hex back into a field map.
func ExampleDecode() {
	fields, err := binarycodec.Decode("1200002400000001")
	if err != nil {
		panic(err)
	}
	fmt.Println(fields["TransactionType"])
	fmt.Println(fields["Sequence"])
	// Output:
	// Payment
	// 1
}
