package payment

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestSandboxSuccessorIndexMatchesMergedView(t *testing.T) {
	rng := rand.New(rand.NewSource(21457985))
	base := newPaymentMockLedgerView()
	root := NewPaymentSandbox(base)
	sb := NewChildSandbox(root)
	check := func(v *PaymentSandbox) {
		entries := map[[32]byte][]byte{}
		require.NoError(t, v.ForEach(func(k [32]byte, data []byte) bool { entries[k] = data; return true }))
		for n := range 65 {
			query := [32]byte{byte(n)}
			var want [32]byte
			var value []byte
			found := false
			for k, data := range entries {
				if bytes.Compare(k[:], query[:]) > 0 && (!found || bytes.Compare(k[:], want[:]) < 0) {
					want, value, found = k, data, true
				}
			}
			got, data, ok, err := v.Succ(query)
			require.NoError(t, err)
			require.Equal(t, found, ok)
			require.Equal(t, want, got)
			require.Equal(t, value, data)
		}
	}
	for i := range 300 {
		k := keylet.Keylet{Key: [32]byte{byte(1 + rng.Intn(64))}}
		switch rng.Intn(3) {
		case 0:
			require.NoError(t, sb.Insert(k, []byte{byte(i)}))
		case 1:
			require.NoError(t, sb.Update(k, []byte{byte(i)}))
		case 2:
			require.NoError(t, sb.Erase(k))
		}
		check(sb)
		if i%17 == 0 {
			require.NoError(t, sb.Apply(root))
			sb = NewChildSandbox(root)
			check(root)
		}
		if i%101 == 0 {
			root.Reset()
			sb = NewChildSandbox(root)
			check(root)
		}
	}
}
