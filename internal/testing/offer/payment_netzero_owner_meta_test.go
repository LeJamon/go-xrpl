package offer

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestPayment_NetZeroOwnerCount_EmitsBareThreadedNode pins the issue #1180 fix:
// a payment that fully consumes an offer (owner count -1) while auto-creating a
// trust line for the same owner to receive the proceeds (owner count +1) leaves
// the owner's AccountRoot byte-identical. rippled emits that owner as a bare
// threaded ModifiedNode — old PreviousTxnID/PreviousTxnLgrSeq only, no
// FinalFields, no PreviousFields (mainnet ledger 99358634, tx index 75).
//
// goXRPL diverged because the owner-count helpers stamped
// PreviousTxnID/PreviousTxnLgrSeq on the AccountRoot mid-apply, so the
// bytes.Equal(Original, Current) no-op guards in the ApplyStateTable misfired
// and attached a spurious FinalFields block. Threading is the ApplyStateTable's
// job at metadata time; rippled's adjustOwnerCount (View.cpp) only touches
// OwnerCount.
func TestPayment_NetZeroOwnerCount_EmitsBareThreadedNode(t *testing.T) {
	env := jtx.NewTestEnv(t)

	gw := jtx.NewAccount("gateway")
	// "mm990" hashes to account key 000B614F… — low enough that the deleted
	// offer and created trust line keys deterministically sort after it (the
	// ordering the bare-node emission requires; asserted below).
	mm := jtx.NewAccount("mm990")
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")

	env.FundAmount(gw, uint64(jtx.XRP(100000)))
	env.FundAmount(mm, uint64(jtx.XRP(100000)))
	env.FundAmount(alice, uint64(jtx.XRP(100000)))
	env.FundAmount(bob, uint64(jtx.XRP(100000)))
	env.Close()

	// mm deliberately holds NO USD trust line: crossing its offer auto-creates
	// one to receive the USD proceeds (+1) while the fully-consumed offer is
	// deleted (-1) — a net-zero OwnerCount change with no other AccountRoot
	// field touched.
	env.Trust(mm, jtx.EUR(gw, 1_000_000))
	env.Trust(alice, jtx.USD(gw, 1_000_000))
	env.Trust(bob, jtx.EUR(gw, 1_000_000))
	env.Close()

	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, mm, jtx.EUR(gw, 1000)).Build()))
	jtx.RequireTxSuccess(t, env.Submit(payment.PayIssued(gw, alice, jtx.USD(gw, 1000)).Build()))
	env.Close()

	// mm gives EUR, receives USD.
	mmOfferSeq := env.Seq(mm)
	jtx.RequireTxSuccess(t, env.Submit(OfferCreate(mm, jtx.USD(gw, 100), jtx.EUR(gw, 100)).Build()))
	env.Close()

	mmAcctKL := keylet.Account(mm.ID)
	offerKey := keylet.Offer(mm.ID, mmOfferSeq).Key
	lineKey := keylet.Line(mm.ID, gw.ID, "USD").Key

	// The bare-vs-FinalFields outcome for a no-op owner follows rippled's
	// ascending-key apply order: the owner is emitted bare only when every
	// mutated child sorts after the owner's account key. Pin the shape that
	// diverged on mainnet.
	require.Positive(t, bytes.Compare(offerKey[:], mmAcctKL.Key[:]),
		"precondition: deleted offer key must sort after mm's account key")
	require.Positive(t, bytes.Compare(lineKey[:], mmAcctKL.Key[:]),
		"precondition: created trust line key must sort after mm's account key")

	beforeData, err := env.LedgerEntry(mmAcctKL)
	require.NoError(t, err)
	before, err := state.ParseAccountRoot(beforeData)
	require.NoError(t, err)

	// alice pays bob 100 EUR funded by 100 USD through the book, consuming
	// mm's offer exactly and entirely.
	pay := env.Submit(payment.PayIssued(alice, bob, jtx.EUR(gw, 100)).SendMax(jtx.USD(gw, 100)).Build())
	jtx.RequireTxSuccess(t, pay)

	// Sanity: the offer was fully consumed and deleted, the USD line was
	// created, and mm's AccountRoot is a true no-op (OwnerCount unchanged).
	require.Nil(t, GetOffer(env, mm, mmOfferSeq), "mm's offer should be fully consumed and deleted")
	jtx.RequireIOUBalance(t, env, mm, gw, "USD", 100)
	jtx.RequireIOUBalance(t, env, mm, gw, "EUR", 900)
	jtx.RequireIOUBalance(t, env, bob, gw, "EUR", 100)

	afterData, err := env.LedgerEntry(mmAcctKL)
	require.NoError(t, err)
	after, err := state.ParseAccountRoot(afterData)
	require.NoError(t, err)
	require.Equal(t, before.OwnerCount, after.OwnerCount, "premise: OwnerCount nets to zero")
	require.NotEqual(t, before.PreviousTxnID, after.PreviousTxnID,
		"mm's AccountRoot must still be re-threaded to the payment in ledger state")

	require.NotNil(t, pay.Metadata, "payment has nil Metadata")
	mmAcctIndex := strings.ToUpper(hex.EncodeToString(mmAcctKL.Key[:]))
	found := false
	for i := range pay.Metadata.AffectedNodes {
		n := &pay.Metadata.AffectedNodes[i]
		if !strings.EqualFold(n.LedgerIndex, mmAcctIndex) {
			continue
		}
		require.False(t, found, "mm's AccountRoot appears more than once in AffectedNodes")
		found = true
		require.Equal(t, "ModifiedNode", n.NodeType)
		require.Empty(t, n.FinalFields,
			"net-zero owner AccountRoot must be emitted bare (threading only), got spurious FinalFields")
		require.Empty(t, n.PreviousFields,
			"net-zero owner AccountRoot must not carry PreviousFields")
		require.Equal(t, strings.ToUpper(hex.EncodeToString(before.PreviousTxnID[:])), n.PreviousTxnID,
			"bare node must carry the pre-payment PreviousTxnID")
		require.Equal(t, before.PreviousTxnLgrSeq, n.PreviousTxnLgrSeq,
			"bare node must carry the pre-payment PreviousTxnLgrSeq")
	}
	require.True(t, found, "mm's AccountRoot must appear as a bare threaded ModifiedNode")
}
