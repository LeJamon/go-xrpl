package keylet_test

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/keylet"
)

// Keylet derivation is deterministic: the same identifying fields always
// produce the same 256-bit ledger key.
func ExampleAccount() {
	var accountID [20]byte // 20-byte account ID (zero value for illustration)

	k1 := keylet.Account(accountID)
	k2 := keylet.Account(accountID)

	fmt.Println("key length:", len(k1.Key))
	fmt.Println("deterministic:", k1.Key == k2.Key)
	// Output:
	// key length: 32
	// deterministic: true
}
