package did_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	"github.com/stretchr/testify/require"
)

// TestDIDSet_NoOpModify_NoGhostModifiedNode is the end-to-end #1130 guard: a
// DIDSet that re-asserts the existing field values (a no-op modify) must emit no
// DID ModifiedNode, only the fee-paying AccountRoot — matching rippled, which
// skips ModifiedNode emission when *curNode == *origNode
// (ApplyStateTable.cpp:156-157). Before PR #1131 the DID serializer dropped
// PreviousTxnID, so the re-serialized entry differed from the stored one and the
// no-op surfaced as a ghost ModifiedNode.
func TestDIDSet_NoOpModify_NoGhostModifiedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Close()

	// 1st DIDSet — creates the DID.
	jtx.RequireTxSuccess(t, env.Submit(did.DIDSet(alice).URI("4142").Build()))
	env.Close()

	// 2nd DIDSet — identical URI. This is the no-op modify.
	second := env.Submit(did.DIDSet(alice).URI("4142").Build())
	jtx.RequireTxSuccess(t, second)
	require.NotNil(t, second.Metadata, "2nd DIDSet has nil Metadata")

	didMods := 0
	accountRootMods := 0
	for _, n := range second.Metadata.AffectedNodes {
		switch n.LedgerEntryType {
		case "DID":
			if n.NodeType == "ModifiedNode" {
				didMods++
			}
		case "AccountRoot":
			if n.NodeType == "ModifiedNode" {
				accountRootMods++
			}
		}
	}

	require.Equal(t, 0, didMods,
		"no-op DIDSet emitted %d ghost DID ModifiedNode(s); expected 0 (matches rippled ApplyStateTable.cpp:156-157)", didMods)
	require.Equal(t, 1, accountRootMods,
		"expected exactly 1 AccountRoot ModifiedNode (fee/Sequence), got %d", accountRootMods)
}
