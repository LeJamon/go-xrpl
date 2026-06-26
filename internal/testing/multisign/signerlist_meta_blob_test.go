// Binary-meta regression tests for SignerListSet, decoding the produced meta
// blob (not just the in-memory struct). Three divergences from rippled v2.6.2:
//
//	(a) goXRPL serialized the SignerList SLE with a spurious sfAccount. rippled's
//	    ltSIGNER_LIST has no sfAccount (ledger_entries.macro:122-129), so it
//	    appeared in neither the SLE bytes nor the metadata FinalFields.
//	(b) goXRPL sorted SignerEntries by base58 address string; rippled sorts by
//	    the decoded 20-byte AccountID (SetSignerList.cpp:66, SignerEntries.h:67).
//	    Base58 order != AccountID byte order, so entries could be reordered.
//	(c) Replacing a signer list erases then re-inserts it (a fresh SLE). rippled's
//	    threadItem reads curNode's zero PreviousTxnID and emits no node-level
//	    PreviousTxnID; goXRPL wrongly emitted the original's value.
package multisign_test

import (
	"bytes"
	"encoding/hex"
	"testing"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/ticket"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/stretchr/testify/require"
)

func signerListModifiedFromBlob(t *testing.T, meta *tx.Metadata) map[string]any {
	t.Helper()
	blob, err := tx.SerializeMetadata(meta)
	require.NoError(t, err)
	decoded, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err)
	nodes, _ := decoded["AffectedNodes"].([]any)
	for _, raw := range nodes {
		m, _ := raw.(map[string]any)
		inner, ok := m["ModifiedNode"].(map[string]any)
		if !ok {
			continue
		}
		if inner["LedgerEntryType"] == "SignerList" {
			return inner
		}
	}
	return nil
}

func accountIDBytes(t *testing.T, addr string) []byte {
	t.Helper()
	_, id, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	require.NoError(t, err)
	return id
}

func TestSignerListReplace_MetaBlob_NoAccount_SortedEntries_NoPrevTxn(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	b1 := jtx.NewAccount("b1")
	b2 := jtx.NewAccount("b2")
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.Fund(b1)
	env.Fund(b2)
	env.Close()

	// A second owned object keeps the owner directory multi-entry so the signer
	// list is replaced in place (no owner DirectoryNode churn).
	jtx.RequireTxSuccess(t, env.Submit(ticket.TicketCreate(alice, 2).Build()))
	env.Close()
	env.SetSignerList(alice, 1, []jtx.TestSigner{{Account: b1, Weight: 1}})
	env.Close()

	// Submit the entries deliberately in reverse-of-sorted order so a correct
	// implementation must reorder them.
	res := env.Submit(jtx.NewSignerListSetTx(alice, 2, []jtx.TestSigner{
		{Account: b2, Weight: 1},
		{Account: b1, Weight: 1},
	}))
	jtx.RequireTxSuccess(t, res)

	inner := signerListModifiedFromBlob(t, res.Metadata)
	require.NotNil(t, inner, "SignerList ModifiedNode expected in meta blob")

	final, _ := inner["FinalFields"].(map[string]any)
	require.NotNil(t, final)

	// (a) No sfAccount anywhere on the SignerList node.
	_, finalHasAccount := final["Account"]
	require.False(t, finalHasAccount, "SignerList FinalFields must not contain Account")
	if prev, ok := inner["PreviousFields"].(map[string]any); ok {
		_, prevHasAccount := prev["Account"]
		require.False(t, prevHasAccount, "SignerList PreviousFields must not contain Account")
	}

	// (b) SignerEntries sorted by decoded AccountID, ascending.
	entries, _ := final["SignerEntries"].([]any)
	require.Len(t, entries, 2)
	var prevID []byte
	for i, e := range entries {
		wrap, _ := e.(map[string]any)
		se, _ := wrap["SignerEntry"].(map[string]any)
		acct, _ := se["Account"].(string)
		id := accountIDBytes(t, acct)
		if i > 0 {
			require.Negative(t, bytes.Compare(prevID, id),
				"SignerEntries must be sorted ascending by AccountID")
		}
		prevID = id
	}

	// (c) The replaced (erase+reinsert) SignerList carries no node-level
	// PreviousTxnID (rippled threadItem on a fresh curNode emits none).
	_, hasPrevTxnID := inner["PreviousTxnID"]
	require.False(t, hasPrevTxnID,
		"replaced SignerList must not carry a node-level PreviousTxnID (fresh re-inserted SLE)")
}
