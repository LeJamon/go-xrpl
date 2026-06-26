package paychan

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

func findModified(t *testing.T, res jtx.TxResult, entryType string) (prev, final map[string]any) {
	t.Helper()
	require.NotNil(t, res.Metadata, "metadata must be present")
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == "ModifiedNode" && n.LedgerEntryType == entryType {
			return n.PreviousFields, n.FinalFields
		}
	}
	t.Fatalf("no ModifiedNode of type %s in meta", entryType)
	return nil, nil
}

func findCreatedNewFields(t *testing.T, res jtx.TxResult, entryType string) map[string]any {
	t.Helper()
	require.NotNil(t, res.Metadata, "metadata must be present")
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == "CreatedNode" && n.LedgerEntryType == entryType {
			return n.NewFields
		}
	}
	t.Fatalf("no CreatedNode of type %s in meta", entryType)
	return nil
}

// TestPayChanCreate_Meta_NewFields asserts that PaymentChannelCreate's
// CreatedNode NewFields match rippled: the channel is created with
// sfBalance == 0 (a default zero XRP STAmount). rippled's ApplyStateTable
// (ApplyStateTable.cpp:251 `!obj.isDefault()`) drops default-valued fields
// from NewFields, and STAmount::isDefault() is true for a zero XRP amount, so
// Balance must NOT appear. The non-default required fields (Amount, Account,
// Destination, PublicKey, SettleDelay) must appear.
func TestPayChanCreate_Meta_NewFields(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	res := env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, pk).Build())
	jtx.RequireTxSuccess(t, res)

	nf := findCreatedNewFields(t, res, "PayChannel")

	_, hasBalance := nf["Balance"]
	require.False(t, hasBalance, "NewFields must NOT contain default zero Balance")

	require.Equal(t, "1000000000", nf["Amount"], "NewFields.Amount must be 1000 XRP")
	require.Equal(t, alice.Address, nf["Account"], "NewFields.Account must be the source")
	require.Equal(t, bob.Address, nf["Destination"], "NewFields.Destination must be the destination")
	require.Equal(t, uint32(100), nf["SettleDelay"], "NewFields.SettleDelay must be present")
	gotPK, _ := nf["PublicKey"].(string)
	require.NotEmpty(t, gotPK, "NewFields.PublicKey must be present")
}

func hasNode(res jtx.TxResult, nodeType, entryType string) bool {
	if res.Metadata == nil {
		return false
	}
	for _, n := range res.Metadata.AffectedNodes {
		if n.NodeType == nodeType && n.LedgerEntryType == entryType {
			return true
		}
	}
	return false
}

// TestPayChanClaim_Meta_NoOpClaimLeavesChannelUntouched asserts that a
// PaymentChannelClaim which changes nothing on the channel (no Balance, no
// Amount, no flags — a fee-only claim) produces metadata with NO PayChannel
// node. rippled's PayChanClaim::doApply only calls view.update(slep) when the
// claim actually changes the channel; a no-op claim leaves the channel SLE
// untouched, so the only AffectedNode is the submitter's AccountRoot (the
// fee). goXRPL previously re-serialized and wrote the channel back
// unconditionally; because the hand-written PayChannel serializer dropped the
// threading fields, the round-trip bytes differed from the original, defeating
// the engine's no-op-modify drop — producing a ghost ModifiedNode and bumping
// the channel's PreviousTxnID (a tx_hash + account_hash fork vs rippled).
func TestPayChanClaim_Meta_NoOpClaimLeavesChannelUntouched(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	createSeq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, createSeq)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, pk).Build()))
	env.Close()

	chanIDHex := hex.EncodeToString(chanK.Key[:])

	// Owner submits a claim with no Balance, no Amount, no flags: a no-op on
	// the channel. Only the fee is charged.
	res := env.Submit(ChannelClaim(alice, chanIDHex).Build())
	jtx.RequireTxSuccess(t, res)

	require.False(t, hasNode(res, "ModifiedNode", "PayChannel"),
		"no-op claim must NOT emit a PayChannel ModifiedNode")
	require.False(t, hasNode(res, "CreatedNode", "PayChannel"),
		"no-op claim must NOT create a PayChannel node")
	require.False(t, hasNode(res, "DeletedNode", "PayChannel"),
		"no-op claim must NOT delete the PayChannel")
	require.True(t, hasNode(res, "ModifiedNode", "AccountRoot"),
		"fee-only claim must still modify the submitter AccountRoot (the fee)")
}

// TestPayChanFund_Meta_PreviousAmount asserts that PaymentChannelFund records
// the channel's prior Amount in the ModifiedNode PreviousFields, matching
// rippled (PayChan.cpp PayChanFund::doApply bumps sfAmount; ApplyStateTable
// emits the original value in sfPreviousFields). The bug was a generator
// shadowing defect in the typed PayChannel.EmitPreviousFields: the prev
// argument shadowed the receiver, so every field was compared against itself
// and PreviousFields came out empty.
func TestPayChanFund_Meta_PreviousAmount(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	createSeq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, createSeq)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, pk).Build()))
	env.Close()

	chanIDHex := hex.EncodeToString(chanK.Key[:])
	res := env.Submit(ChannelFund(alice, chanIDHex, xrp(1000)).Build())
	jtx.RequireTxSuccess(t, res)

	prev, final := findModified(t, res, "PayChannel")
	require.NotNil(t, prev, "PayChannel ModifiedNode must carry PreviousFields")
	require.Equal(t, "1000000000", prev["Amount"], "PreviousFields.Amount must be the pre-fund amount (1000 XRP)")
	require.Equal(t, "2000000000", final["Amount"], "FinalFields.Amount must be the post-fund amount (2000 XRP)")
}

// TestPayChanClaim_Meta_PreviousBalance asserts that PaymentChannelClaim records
// the channel's prior Balance in the ModifiedNode PreviousFields, matching
// rippled (PayChan.cpp PayChanClaim bumps sfBalance). Same generator
// shadowing root cause as the Fund case.
func TestPayChanClaim_Meta_PreviousBalance(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.FundAmount(bob, uint64(jtx.XRP(10000)))
	env.Close()

	pk := alice.PublicKeyHex()
	createSeq := env.Seq(alice)
	chanK := chanKeylet(alice, bob, createSeq)
	jtx.RequireTxSuccess(t, env.Submit(ChannelCreate(alice, bob, xrp(1000), 100, pk).Build()))
	env.Close()

	chanIDHex := hex.EncodeToString(chanK.Key[:])

	sig1 := signClaimAuth(alice, chanIDHex, uint64(xrp(100)))
	jtx.RequireTxSuccess(t, env.Submit(
		ChannelClaim(bob, chanIDHex).Balance(xrp(100)).Amount(xrp(100)).Signature(sig1).PublicKey(pk).Build()))
	env.Close()
	require.Equal(t, uint64(xrp(100)), chanBalance(env, chanK))

	sig2 := signClaimAuth(alice, chanIDHex, uint64(xrp(250)))
	res := env.Submit(
		ChannelClaim(bob, chanIDHex).Balance(xrp(250)).Amount(xrp(250)).Signature(sig2).PublicKey(pk).Build())
	jtx.RequireTxSuccess(t, res)

	prev, final := findModified(t, res, "PayChannel")
	require.NotNil(t, prev, "PayChannel ModifiedNode must carry PreviousFields")
	require.Equal(t, "100000000", prev["Balance"], "PreviousFields.Balance must be the pre-claim balance (100 XRP)")
	require.Equal(t, "250000000", final["Balance"], "FinalFields.Balance must be the post-claim balance (250 XRP)")
}
