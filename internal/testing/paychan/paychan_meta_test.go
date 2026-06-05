package paychan

import (
	"encoding/hex"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/stretchr/testify/require"
)

// findModified returns the first ModifiedNode of the given ledger entry type.
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

	// First claim raises Balance from 0 -> 100 XRP.
	sig1 := signClaimAuth(alice, chanIDHex, uint64(xrp(100)))
	jtx.RequireTxSuccess(t, env.Submit(
		ChannelClaim(bob, chanIDHex).Balance(xrp(100)).Amount(xrp(100)).Signature(sig1).PublicKey(pk).Build()))
	env.Close()
	require.Equal(t, uint64(xrp(100)), chanBalance(env, chanK))

	// Second claim raises Balance 100 -> 250 XRP; PreviousFields.Balance == 100 XRP.
	sig2 := signClaimAuth(alice, chanIDHex, uint64(xrp(250)))
	res := env.Submit(
		ChannelClaim(bob, chanIDHex).Balance(xrp(250)).Amount(xrp(250)).Signature(sig2).PublicKey(pk).Build())
	jtx.RequireTxSuccess(t, res)

	prev, final := findModified(t, res, "PayChannel")
	require.NotNil(t, prev, "PayChannel ModifiedNode must carry PreviousFields")
	require.Equal(t, "100000000", prev["Balance"], "PreviousFields.Balance must be the pre-claim balance (100 XRP)")
	require.Equal(t, "250000000", final["Balance"], "FinalFields.Balance must be the post-claim balance (250 XRP)")
}
