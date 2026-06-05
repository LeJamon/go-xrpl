// Confirmation test that a plain MPTokenIssuanceCreate produces exactly the
// CreatedNode NewFields rippled does. Investigated as a candidate
// meta-completeness divergence; the goXRPL created SLE matches rippled
// (MPTokenIssuanceCreate.cpp:113-135): sfFlags (& ~tfUniversal),
// sfOutstandingAmount=0 and sfOwnerNode=0 are default and excluded from
// NewFields, leaving {Issuer, Sequence}.
package mpt_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/stretchr/testify/require"
)

func TestMPTokenIssuanceCreate_Meta_NewFields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	c := mpttx.NewMPTokenIssuanceCreate(alice.Address)
	c.Fee = "10"
	res := env.Submit(c)
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	var nf map[string]any
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == "MPTokenIssuance" {
			nf = n.NewFields
		}
	}
	require.NotNil(t, nf, "MPTokenIssuance CreatedNode expected")
	require.Equal(t, alice.Address, nf["Issuer"])
	require.Contains(t, nf, "Sequence")
	// Defaulted required fields are excluded from NewFields, matching rippled.
	require.NotContains(t, nf, "OutstandingAmount", "OutstandingAmount=0 is default; excluded from NewFields")
	require.NotContains(t, nf, "OwnerNode", "OwnerNode=0 (page 0) is default; excluded from NewFields")
	require.NotContains(t, nf, "Flags", "Flags=0 is default; excluded from NewFields")
}
