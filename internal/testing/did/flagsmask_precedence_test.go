package did_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// TestDID_FlagMaskBeatsBadFee pins the DIDSet/DIDDelete FlagsMasker adoption: the
// invalid-flags rejection runs at preflight0, ahead of the fee check, so a stray
// flag plus a malformed (negative) fee surfaces temINVALID_FLAG, not temBAD_FEE.
func TestDID_FlagMaskBeatsBadFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	const stray = uint32(0x08000000)
	cases := []struct {
		name string
		txn  tx.Transaction
	}{
		{"DIDSet", did.DIDSet(alice).URI("AB").Flags(stray).Build()},
		{"DIDDelete", did.DIDDelete(alice).Flags(stray).Build()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.txn.GetCommon().Fee = "-10"
			require.Equal(t, "temINVALID_FLAG", env.Submit(c.txn).Code)
		})
	}
}
