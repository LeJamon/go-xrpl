package crypto_test

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/crypto"
)

// SecureErase zeroes a byte slice in place — used to scrub key material from
// memory once it is no longer needed.
func ExampleSecureErase() {
	secret := []byte{0x01, 0x02, 0x03, 0x04}
	crypto.SecureErase(secret)
	fmt.Println(secret)
	// Output: [0 0 0 0]
}
