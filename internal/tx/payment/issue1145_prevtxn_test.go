package payment

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// TestAdjustTrustLineBalance_DustRoundBack_NoStamp pins the issue #1145 fix at the
// writer level: when an offer/AMM-crossing IOU leg adjusts a trust-line balance by
// a dust amount that rounds back to the existing value, the re-serialized trust line
// must be byte-identical to its pre-tx state — which means the writer must NOT stamp
// PreviousTxnID/PreviousTxnLgrSeq.
//
// adjustTrustLineBalance used to stamp the threading pointers before serializing.
// On a round-back the balance is unchanged, so the stamp was the ONLY byte
// difference between Original and Current — defeating the
// bytes.Equal(Original, Current) skip the ApplyStateTable runs *before* threading
// (apply_state_table.go:374-376 / 511-513, mirroring rippled
// ApplyStateTable.cpp:156-157) → a ghost empty-diff ModifiedNode plus a bogus
// PreviousTxnID bump on the counterparty trust line (the mainnet replay fork at
// ledger 99272143). Threading is the ApplyStateTable's job; the writer leaves the
// pointers alone.
//
// A near-max balance (1e55, resolution 1e40) makes a 100-unit adjustment round back
// byte-identical, the same way mainnet's dust crossing did. Re-introducing the stamp
// breaks the byte-equality assertion, so this guard is non-vacuous by construction.
func TestAdjustTrustLineBalance_DustRoundBack_NoStamp(t *testing.T) {
	var a, b [20]byte
	copy(a[:], []byte("issue1145-acct-aaaaa"))
	copy(b[:], []byte("issue1145-issuer-bbb"))
	low, high := a, b
	if bytes.Compare(a[:], b[:]) > 0 {
		low, high = b, a
	}

	var seededTxn [32]byte
	copy(seededTxn[:], []byte("SEEDED-PREVIOUS-TXN-ID-DO-NOT-MOV"))
	const seededSeq = uint32(99272121)

	// Balance from the low account's perspective; issuer is the high account.
	// 1e55 (mantissa 1e15, exponent 40) — far above the 100-unit adjustment so the
	// recomputed balance rounds back to exactly the same mantissa/exponent.
	rs := &state.RippleState{
		Balance:           tx.NewIssuedAmount(1000000000000000, 40, "USD", state.EncodeAccountIDSafe(high)),
		LowLimit:          tx.NewIssuedAmountFromFloat64(0, "USD", state.EncodeAccountIDSafe(low)),
		HighLimit:         tx.NewIssuedAmountFromFloat64(0, "USD", state.EncodeAccountIDSafe(high)),
		PreviousTxnID:     seededTxn,
		PreviousTxnLgrSeq: seededSeq,
	}
	originalData, err := state.SerializeRippleState(rs)
	require.NoError(t, err)

	view := newPaymentMockLedgerView()
	tlKey := keylet.Line(low, high, "USD")
	view.data[tlKey.Key] = originalData

	sb := NewPaymentSandbox(view)
	var txContext [32]byte
	copy(txContext[:], []byte("THE-APPLYING-TX-HASH-WOULD-STAMP."))
	sb.SetTransactionContext(txContext, 99272143)
	require.NotEqual(t, seededTxn, txContext, "test setup: tx context must differ from the seeded PreviousTxnID")

	// Credit the low account by a dust amount → balance rounds back to 1e55.
	dust := tx.NewIssuedAmountFromFloat64(100, "USD", state.EncodeAccountIDSafe(high))
	require.NoError(t, adjustTrustLineBalance(sb, low, high, "USD", dust, true))

	resultData, err := sb.Read(tlKey)
	require.NoError(t, err)

	// The round-back left the trust line byte-identical: balance unchanged AND no
	// PreviousTxnID stamp, so the ApplyStateTable no-op skip fires (no ghost node).
	require.True(t, bytes.Equal(originalData, resultData),
		"adjustTrustLineBalance changed the serialized trust line after a round-back-identical dust "+
			"adjustment; re-stamping PreviousTxnID here reintroduces issue #1145's ghost ModifiedNode")

	got, err := state.ParseRippleState(resultData)
	require.NoError(t, err)
	require.Equal(t, seededTxn, got.PreviousTxnID, "writer stamped PreviousTxnID — must defer to ApplyStateTable")
	require.Equal(t, seededSeq, got.PreviousTxnLgrSeq, "writer stamped PreviousTxnLgrSeq — must defer to ApplyStateTable")
}

// TestXRPTransferInSandbox_NoStamp pins the sibling fix on the native leg: the
// XRP writer for offer/AMM-crossing legs adjusts AccountRoot balances but must NOT
// stamp PreviousTxnID/PreviousTxnLgrSeq either — threading is deferred to the
// ApplyStateTable (rippled's accountSendIOU native branch, View.cpp, likewise only
// adjusts sfBalance + view.update). Native XRP is exact-integer so it never rounds
// back, but a manual stamp would still make a net-zero touch ghost; leaving the
// pointers to the ApplyStateTable matches rippled and the IOU leg.
func TestXRPTransferInSandbox_NoStamp(t *testing.T) {
	var from, to [20]byte
	copy(from[:], []byte("issue1145-from-acct1"))
	copy(to[:], []byte("issue1145-to-acct222"))

	var fromTxn, toTxn [32]byte
	copy(fromTxn[:], []byte("FROM-SEEDED-PREVTXN-ID-UNCHANGED."))
	copy(toTxn[:], []byte("TO-SEEDED-PREVTXN-ID-UNCHANGED..."))
	const seededSeq = uint32(99272121)

	fromAcct := &state.AccountRoot{
		Account:           state.EncodeAccountIDSafe(from),
		Balance:           1_000_000_000,
		Sequence:          5,
		PreviousTxnID:     fromTxn,
		PreviousTxnLgrSeq: seededSeq,
	}
	toAcct := &state.AccountRoot{
		Account:           state.EncodeAccountIDSafe(to),
		Balance:           2_000_000_000,
		Sequence:          9,
		PreviousTxnID:     toTxn,
		PreviousTxnLgrSeq: seededSeq,
	}
	fromData, err := state.SerializeAccountRoot(fromAcct)
	require.NoError(t, err)
	toData, err := state.SerializeAccountRoot(toAcct)
	require.NoError(t, err)

	view := newPaymentMockLedgerView()
	view.data[keylet.Account(from).Key] = fromData
	view.data[keylet.Account(to).Key] = toData

	sb := NewPaymentSandbox(view)
	var txContext [32]byte
	copy(txContext[:], []byte("THE-APPLYING-TX-HASH-WOULD-STAMP."))
	sb.SetTransactionContext(txContext, 99272143)

	const drops = int64(500_000)
	require.NoError(t, xrpTransferInSandbox(sb, from, to, drops))

	fromOut, err := sb.Read(keylet.Account(from))
	require.NoError(t, err)
	toOut, err := sb.Read(keylet.Account(to))
	require.NoError(t, err)
	gotFrom, err := state.ParseAccountRoot(fromOut)
	require.NoError(t, err)
	gotTo, err := state.ParseAccountRoot(toOut)
	require.NoError(t, err)

	// Balances moved (XRP is exact), but the threading pointers are left to the
	// ApplyStateTable — the writer must not stamp them.
	require.Equal(t, uint64(1_000_000_000-drops), gotFrom.Balance, "debit not applied")
	require.Equal(t, uint64(2_000_000_000+drops), gotTo.Balance, "credit not applied")
	require.Equal(t, fromTxn, gotFrom.PreviousTxnID, "from AccountRoot PreviousTxnID was stamped by the writer")
	require.Equal(t, seededSeq, gotFrom.PreviousTxnLgrSeq, "from AccountRoot PreviousTxnLgrSeq was stamped by the writer")
	require.Equal(t, toTxn, gotTo.PreviousTxnID, "to AccountRoot PreviousTxnID was stamped by the writer")
	require.Equal(t, seededSeq, gotTo.PreviousTxnLgrSeq, "to AccountRoot PreviousTxnLgrSeq was stamped by the writer")
}
