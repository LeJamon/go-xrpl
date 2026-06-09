// Binary-meta regression test for MPTokenIssuanceSet.
//
// rippled only writes sfFlags when it actually changes (MPTokenIssuanceSet.cpp:
// 181-182), so locking an already-locked issuance leaves the SLE unchanged and
// emits NO MPTokenIssuance node. goXRPL re-serialized the issuance on every Set,
// and its serializer dropped sfPreviousTxnID/sfPreviousTxnLgrSeq — so the
// re-serialized bytes differed from the original and a no-op emitted a spurious
// ("ghost") MPTokenIssuance ModifiedNode. The fix round-trips the threading
// fields (mirroring the DirectoryNode/SignerList fixes). Asserts against the
// decoded binary meta blob.
package mpt_test

import (
	"encoding/hex"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpt "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	mpttx "github.com/LeJamon/go-xrpl/internal/tx/mpt"
	"github.com/stretchr/testify/require"
)

func modifiedTypesFromBlob(t *testing.T, meta *tx.Metadata) map[string]map[string]any {
	t.Helper()
	blob, err := tx.SerializeMetadata(meta)
	require.NoError(t, err)
	decoded, err := binarycodec.Decode(hex.EncodeToString(blob))
	require.NoError(t, err)
	out := map[string]map[string]any{}
	nodes, _ := decoded["AffectedNodes"].([]any)
	for _, raw := range nodes {
		m, _ := raw.(map[string]any)
		inner, ok := m["ModifiedNode"].(map[string]any)
		if !ok {
			continue
		}
		let, _ := inner["LedgerEntryType"].(string)
		out[let] = inner
	}
	return out
}

func TestMPTokenIssuanceSet_Meta_NoOpLockEmitsNoGhost(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	m := mpt.NewMPTTester(t, env, issuer)
	m.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanLock})
	env.Close()

	// First lock — a real change. The issuance is modified and (being a threaded
	// type modified in place) carries a node-level PreviousTxnID.
	lock := mpttx.NewMPTokenIssuanceSet(issuer.Address, m.IssuanceID())
	lock.Fee = "10"
	lock.SetFlags(mpt.TfMPTLock)
	res := env.Submit(lock)
	jtx.RequireTxSuccess(t, res)
	mods := modifiedTypesFromBlob(t, res.Metadata)
	issuanceMod := mods["MPTokenIssuance"]
	require.NotNil(t, issuanceMod, "first lock must modify the MPTokenIssuance")
	_, hasPrevTxn := issuanceMod["PreviousTxnID"]
	require.True(t, hasPrevTxn, "in-place issuance modify must carry a node-level PreviousTxnID")
	env.Close()

	// Second lock — a no-op (already locked). rippled leaves the issuance
	// untouched, so no MPTokenIssuance node appears (only the fee AccountRoot).
	noop := mpttx.NewMPTokenIssuanceSet(issuer.Address, m.IssuanceID())
	noop.Fee = "10"
	noop.SetFlags(mpt.TfMPTLock)
	res2 := env.Submit(noop)
	jtx.RequireTxSuccess(t, res2)
	mods2 := modifiedTypesFromBlob(t, res2.Metadata)
	require.NotContains(t, mods2, "MPTokenIssuance",
		"a no-op lock must not emit a ghost MPTokenIssuance ModifiedNode")
	require.Contains(t, mods2, "AccountRoot", "the fee-paying AccountRoot is still modified")
}

func TestMPTokenIssuanceSet_Meta_HolderNoOpLockEmitsNoGhost(t *testing.T) {
	env := jtx.NewTestEnv(t)
	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")
	env.Fund(holder)
	m := mpt.NewMPTTester(t, env, issuer)
	m.Create(mpt.CreateOpts{Flags: mpt.TfMPTCanLock | mpt.TfMPTRequireAuth})
	m.Authorize(mpt.AuthorizeOpts{Account: holder})
	m.Authorize(mpt.AuthorizeOpts{Holder: holder})
	env.Close()

	lock := mpttx.NewMPTokenIssuanceSet(issuer.Address, m.IssuanceID())
	lock.Fee = "10"
	lock.Holder = holder.Address
	lock.SetFlags(mpt.TfMPTLock)
	jtx.RequireTxSuccess(t, env.Submit(lock))
	env.Close()

	// Re-lock the holder token (no-op): rippled emits no MPToken node.
	noop := mpttx.NewMPTokenIssuanceSet(issuer.Address, m.IssuanceID())
	noop.Fee = "10"
	noop.Holder = holder.Address
	noop.SetFlags(mpt.TfMPTLock)
	res := env.Submit(noop)
	jtx.RequireTxSuccess(t, res)
	mods := modifiedTypesFromBlob(t, res.Metadata)
	require.NotContains(t, mods, "MPToken",
		"a no-op holder lock must not emit a ghost MPToken ModifiedNode")
}
