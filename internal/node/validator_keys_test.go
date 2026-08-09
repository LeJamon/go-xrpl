package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
)

func TestValidatorKeysBase58(t *testing.T) {
	keys := [][33]byte{{0x02, 0x01}, {0x03, 0x02}}
	got := validatorKeysBase58(keys)
	if len(got) != len(keys) {
		t.Fatalf("encoded keys = %d, want %d", len(got), len(keys))
	}
	for i, key := range keys {
		want, err := addresscodec.EncodeNodePublicKey(key[:])
		if err != nil {
			t.Fatal(err)
		}
		if got[i] != want {
			t.Fatalf("encoded key %d = %q, want %q", i, got[i], want)
		}
		if single, ok := validatorKeyBase58(key); !ok || single != want {
			t.Fatalf("single encoded key %d = %q, %t; want %q, true", i, single, ok, want)
		}
	}
}

func TestValidatorKeysBase58PreservesEmptyList(t *testing.T) {
	if got := validatorKeysBase58(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty encoded keys = %#v, want non-nil empty list", got)
	}
}
