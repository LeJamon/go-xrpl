// Metadata-completeness regression tests for replacing an existing SignerList.
// Two divergences from rippled v2.6.2 were forking a mixed network:
//
//  (a) goXRPL emitted a spurious owner DirectoryNode ModifiedNode. rippled's
//      replaceSignerList removes then re-inserts the signer list on the same
//      page, so the directory nets to byte-identical and is dropped
//      (ApplyStateTable.cpp:156-157, *curNode == *origNode). goXRPL's
//      DirectoryNode parse/serialize dropped sfPreviousTxnID/sfPreviousTxnLgrSeq,
//      so the round-trip differed only in the threading fields, metadata
//      threading then bumped PreviousTxnID, and the node was emitted.
//
//  (b) goXRPL never wrote sfSignerListID. rippled hardcodes it to 0 on every
//      signer list (SetSignerList.cpp:428). Replacing a rippled-created list
//      (which carries SignerListID:0) then surfaced "SignerListID:0" in the
//      ModifiedNode PreviousFields, and diverged the SLE bytes.
package multisign_test

import (
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/stretchr/testify/require"
)

func TestSignerListReplace_Meta_NoDirectoryNode_AndSignerListID(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	b1 := jtx.NewAccount("b1")
	b2 := jtx.NewAccount("b2")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Fund(b1)
	env.Fund(b2)
	env.Close()

	// A second owned object keeps the owner directory multi-entry, so the
	// signer list is replaced in place (page modified, not erased/recreated).
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 2).Build()))
	env.Close()

	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: b1, Weight: 1}})
	env.Close()

	res := env.Submit(jtx.NewSignerListSetTx(alice, 2, []jtx.TestSigner{
		{Account: b1, Weight: 1},
		{Account: b2, Weight: 1},
	}))
	jtx.RequireTxSuccess(t, res)
	require.NotNil(t, res.Metadata)

	var dirNodes, signerListNodes int
	var slPrev, slFinal map[string]any
	for _, n := range res.Metadata.AffectedNodes {
		switch n.LedgerEntryType {
		case "DirectoryNode":
			dirNodes++
		case "SignerList":
			signerListNodes++
			slPrev = n.PreviousFields
			slFinal = n.FinalFields
		}
	}

	// (a) No owner DirectoryNode of any node type — the in-place replace nets
	// to no directory change, exactly like rippled.
	require.Equal(t, 0, dirNodes,
		"replacing a signer list in place must not emit any DirectoryNode (rippled drops the unchanged owner dir)")

	require.Equal(t, 1, signerListNodes, "exactly one SignerList ModifiedNode expected")

	// (b) SignerListID did not change (always 0), so it must NOT be in
	// PreviousFields, but it MUST be present (=0) in FinalFields, matching
	// rippled's always-serialized sfSignerListID.
	require.NotNil(t, slPrev)
	_, prevHasID := slPrev["SignerListID"]
	require.False(t, prevHasID, "SignerListID must not appear in PreviousFields (it never changed)")
	idVal, finalHasID := slFinal["SignerListID"]
	require.True(t, finalHasID, "SignerListID must be present in FinalFields (rippled always serializes sfSignerListID=0)")
	require.Equal(t, uint32(0), idVal, "SignerListID is hardcoded to 0")
}
