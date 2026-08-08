package did_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/stretchr/testify/require"
)

func TestDIDSet_NoOpModify_NoGhostModifiedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// 1st DIDSet — creates the DID.
	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URIHex("4142").Build()))
	env.Close()

	// 2nd DIDSet — identical URI. This is the no-op modify.
	second := env.Submit(did.DIDSet(alice).URIHex("4142").Build())
	jtx.RequireTxSuccess(t, second)
	require.NotNil(t, second.Metadata, "2nd DIDSet has nil Metadata")

	require.Len(t, second.Metadata.AffectedNodes, 1)
	node := second.Metadata.AffectedNodes[0]
	require.Equal(t, "AccountRoot", node.LedgerEntryType)
	require.Equal(t, "ModifiedNode", node.NodeType)
}
