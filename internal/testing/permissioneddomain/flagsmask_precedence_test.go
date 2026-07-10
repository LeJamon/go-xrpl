package permissioneddomain_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/permissioneddomain"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

// someDomainID is any well-formed 256-bit ID; the flag mask fires at preflight0
// before the ledger-stage domain lookup, so it need not resolve.
const someDomainID = "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF"

// TestPermissionedDomain_FlagMaskBeatsBadFee pins the PermissionedDomainSet/Delete
// FlagsMasker adoption: the invalid-flags rejection runs at preflight0, ahead of
// the fee check, so a stray flag plus a malformed (negative) fee surfaces
// temINVALID_FLAG, not temBAD_FEE.
func TestPermissionedDomain_FlagMaskBeatsBadFee(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.Fund(alice)
	env.Close()

	const stray = uint32(0x08000000)
	cases := []struct {
		name string
		txn  tx.Transaction
	}{
		{"PermissionedDomainSet", permissioneddomain.DomainSet(alice).
			Credential(alice, "KYC").Flags(stray).Build()},
		{"PermissionedDomainDelete", permissioneddomain.DomainDelete(alice, someDomainID).Flags(stray).Build()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			c.txn.GetCommon().Fee = "-10"
			require.Equal(t, "temINVALID_FLAG", env.Submit(c.txn).Code)
		})
	}
}
