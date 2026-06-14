package accountset

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/keylet"
)

// rippled updates sfAccountTxnID in the Transactor::apply() preamble
// (Transactor.cpp:568) BEFORE doApply(). On a tec result that whole preamble
// is rolled back by reset() (Transactor.cpp:1001 ctx_.discard()), which then
// re-applies ONLY sfBalance (fee) and sfSequence — never sfAccountTxnID. So a
// tec must leave AccountTxnID at its prior value; only a successful tx updates
// it.
//
// Regression for the mixed-soak fork (iter-5 seq-70, soak#1 seq-39): goXRPL's
// tec-recovery path updated AccountTxnID, so a tec tx from an asfAccountTxnID
// account produced metadata (and, when it raced, a transaction_hash) that
// diverged from rippled while account_hash matched — wedging consensus.
func TestAccountTxnID_NotUpdatedOnTec(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	ghost := jtx.NewAccount("ghost") // unfunded → alice's payment to it tecs
	env.FundAmount(alice, uint64(jtx.XRP(10000)))
	env.Close()

	readTxnID := func() [32]byte {
		t.Helper()
		data, err := env.LedgerEntry(keylet.Account(alice.ID))
		require.NoError(t, err)
		ar, err := state.ParseAccountRoot(data)
		require.NoError(t, err)
		return ar.AccountTxnID
	}

	// Enable asfAccountTxnID (present, value zero).
	r := env.Submit(AccountSet(alice).SetFlag(5).Build())
	jtx.RequireTxSuccess(t, r)
	env.Close()

	// A successful tx must update AccountTxnID (rippled preamble survives).
	r = env.Submit(AccountSet(alice).Build()) // noop success
	jtx.RequireTxSuccess(t, r)
	env.Close()
	afterSuccess := readTxnID()
	require.NotEqual(t, [32]byte{}, afterSuccess, "a successful tx must set AccountTxnID")

	// A tec tx must NOT change AccountTxnID (rippled reset() re-applies only
	// fee+seq). Payment of 1 XRP to an unfunded account is below reserve →
	// tecNO_DST_INSUF_XRP, applied (fee claimed) but unsuccessful.
	r = env.Submit(payment.Pay(alice, ghost, uint64(jtx.XRP(1))).Build())
	require.False(t, r.Success, "payment below reserve to an unfunded dest must tec, got %s", r.Code)
	require.Contains(t, r.Code, "tec", "expected a tec result, got %s", r.Code)
	env.Close()
	afterTec := readTxnID()
	require.Equal(t, afterSuccess, afterTec,
		"a tec tx must NOT update AccountTxnID — rippled reset() discards the preamble and re-applies only fee+seq")
}
