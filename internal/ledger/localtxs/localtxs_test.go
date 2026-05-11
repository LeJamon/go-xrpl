package localtxs_test

import (
	"bytes"
	"encoding/hex"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/localtxs"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	testenv "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func buildSignedBlob(t *testing.T, env *testenv.TestEnv, txn tx.Transaction, signer *testenv.Account) []byte {
	t.Helper()
	env.SignWith(txn, signer)
	txMap, err := txn.Flatten()
	if err != nil {
		t.Fatalf("Flatten: %v", err)
	}
	hexStr, err := binarycodec.Encode(txMap)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	blob, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return blob
}

// pendingFromPay funds alice, builds a 1-drop payment from alice→bob at
// the given sequence, signs it, and returns the parsed PendingTx and an
// open writable view on top of the LCL (so the test helper can mutate
// AccountRoot / inject tx entries).
func pendingFromPay(t *testing.T, env *testenv.TestEnv, alice, bob *testenv.Account, seq uint32) (openledger.PendingTx, *ledger.Ledger) {
	t.Helper()
	pay := payment.Pay(alice, bob, 1_000_000).Sequence(seq).Build()
	blob := buildSignedBlob(t, env, pay, alice)
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	env.Close()
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no LastClosedLedger after Close")
	}
	view, err := ledger.NewOpen(parent, time.Now())
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return ptx, view
}

// TestLocalTxs_PushBack_Dedup verifies that pushing the same tx hash twice
// only stores it once.
func TestLocalTxs_PushBack_Dedup(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	ptx, _ := pendingFromPay(t, env, alice, bob, env.Seq(alice))

	pool := localtxs.New()
	pool.PushBack(10, ptx)
	pool.PushBack(10, ptx)
	pool.PushBack(11, ptx)

	if got, want := pool.Size(), 1; got != want {
		t.Errorf("Size = %d, want %d (dedup by hash)", got, want)
	}
}

// TestLocalTxs_Sweep_ExpiresOldEntries verifies that an entry pushed at
// ledger N is dropped when the sweep view's seq exceeds N + HoldLedgers.
func TestLocalTxs_Sweep_ExpiresOldEntries(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	// Advance the LCL enough that we can anchor strictly before
	// view.Sequence() - HoldLedgers.
	for i := uint32(0); i < localtxs.HoldLedgers+3; i++ {
		env.Close()
	}

	ptx, view := pendingFromPay(t, env, alice, bob, env.Seq(alice))
	if view.Sequence() <= localtxs.HoldLedgers+2 {
		t.Fatalf("test setup: view seq %d too small to expire", view.Sequence())
	}
	anchor := view.Sequence() - localtxs.HoldLedgers - 2

	pool := localtxs.New()
	pool.PushBack(anchor, ptx)

	pool.Sweep(view)
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after expiring Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_Sweep_KeepsFreshEntries verifies that an entry pushed at
// the current ledger survives a Sweep against that same ledger.
func TestLocalTxs_Sweep_KeepsFreshEntries(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	// Build a payment with a *future* sequence so the seq-advance check
	// doesn't drop it (alice's AccountRoot.Sequence is unchanged by the
	// env.Close inside pendingFromPay).
	futureSeq := env.Seq(alice) + 10
	ptx, view := pendingFromPay(t, env, alice, bob, futureSeq)

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	pool.Sweep(view)
	if got := pool.Size(); got != 1 {
		t.Errorf("Size after fresh Sweep = %d, want 1", got)
	}
}

// TestLocalTxs_Sweep_DropsBySeqAdvance verifies that when the view's
// AccountRoot.Sequence has advanced past the tx's sequence, the entry
// is dropped (mirrors LocalTxs.cpp:163-164 tefPAST_SEQ branch).
func TestLocalTxs_Sweep_DropsBySeqAdvance(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	txSeq := env.Seq(alice)
	ptx, view := pendingFromPay(t, env, alice, bob, txSeq)

	// Bump alice's AccountRoot.Sequence to txSeq + 1 in the view.
	bumpAccountSequence(t, view, alice, txSeq+1)

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	pool.Sweep(view)
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after seq-advance Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_Sweep_DropsAlreadyValidatedTx verifies that an entry
// already present in the view's tx map is dropped (LocalTxs.cpp:150-151).
func TestLocalTxs_Sweep_DropsAlreadyValidatedTx(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	// Use a future seq so the seq-advance check won't fire — we want to
	// isolate the txExists branch.
	futureSeq := env.Seq(alice) + 10
	ptx, view := pendingFromPay(t, env, alice, bob, futureSeq)

	// Inject the tx into the view's tx map directly.
	if err := view.AddTransaction(ptx.Hash, ptx.Blob); err != nil {
		t.Fatalf("AddTransaction: %v", err)
	}

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	pool.Sweep(view)
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after txExists Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_GetTxSet_CanonicalOrder verifies the (account, sequence,
// hash) ordering with zero salt (LocalTxs.cpp:126).
func TestLocalTxs_GetTxSet_CanonicalOrder(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	carol := testenv.NewAccount("carol")
	env.Fund(alice, bob, carol)

	// Three pending txs from independent senders. We'll push them in a
	// scrambled order and verify GetTxSet sorts by account bytes.
	aliceSeq := env.Seq(alice)
	bobSeq := env.Seq(bob)
	carolSeq := env.Seq(carol)

	aliceTx := buildPendingTx(t, env, alice, bob, aliceSeq+100)
	bobTx := buildPendingTx(t, env, bob, carol, bobSeq+100)
	carolTx := buildPendingTx(t, env, carol, alice, carolSeq+100)

	pool := localtxs.New()
	// Scramble insertion order.
	pool.PushBack(1, carolTx)
	pool.PushBack(1, aliceTx)
	pool.PushBack(1, bobTx)

	got := pool.GetTxSet()
	if len(got) != 3 {
		t.Fatalf("GetTxSet len = %d, want 3", len(got))
	}
	// Compare adjacent pairs — each must be in account order, then
	// sequence, then hash (the latter two are tiebreakers, which we
	// don't synthetically force here — account-byte order is the
	// load-bearing one).
	for i := 0; i < len(got)-1; i++ {
		if bytes.Compare(got[i].Account[:], got[i+1].Account[:]) > 0 {
			t.Errorf("GetTxSet not in canonical order at index %d: %x > %x",
				i, got[i].Account[:4], got[i+1].Account[:4])
		}
	}
}

// TestLocalTxs_GetTxSet_SortsBySequenceWithinAccount verifies that two
// pending txs from the same account are returned in ascending sequence
// order regardless of push order.
func TestLocalTxs_GetTxSet_SortsBySequenceWithinAccount(t *testing.T) {
	env := testenv.NewTestEnv(t)
	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)

	seq := env.Seq(alice)
	tx1 := buildPendingTx(t, env, alice, bob, seq+10)
	tx2 := buildPendingTx(t, env, alice, bob, seq+11)
	tx3 := buildPendingTx(t, env, alice, bob, seq+12)

	pool := localtxs.New()
	// Insert in scrambled order.
	pool.PushBack(1, tx3)
	pool.PushBack(1, tx1)
	pool.PushBack(1, tx2)

	got := pool.GetTxSet()
	if len(got) != 3 {
		t.Fatalf("GetTxSet len = %d, want 3", len(got))
	}
	if got[0].Sequence != seq+10 || got[1].Sequence != seq+11 || got[2].Sequence != seq+12 {
		t.Errorf("GetTxSet sequences = [%d, %d, %d], want [%d, %d, %d]",
			got[0].Sequence, got[1].Sequence, got[2].Sequence,
			seq+10, seq+11, seq+12)
	}
}

// --- helpers --------------------------------------------------------------

// buildPendingTx constructs a signed payment from `from` to `to` at the
// given sequence and returns the parsed PendingTx.
func buildPendingTx(t *testing.T, env *testenv.TestEnv, from, to *testenv.Account, seq uint32) openledger.PendingTx {
	t.Helper()
	pay := payment.Pay(from, to, 1_000_000).Sequence(seq).Build()
	blob := buildSignedBlob(t, env, pay, from)
	ptx, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	return ptx
}

// bumpAccountSequence reads alice's AccountRoot from view, sets its
// Sequence to target, and writes it back. Used to simulate "the tx has
// been replaced or already applied in a sibling round".
func bumpAccountSequence(t *testing.T, view *ledger.Ledger, acc *testenv.Account, target uint32) {
	t.Helper()
	accID := acc.AccountID()
	k := keylet.Account(accID)

	data, err := view.Read(k)
	if err != nil {
		t.Fatalf("view.Read(account): %v", err)
	}
	ar, err := state.ParseAccountRoot(data)
	if err != nil || ar == nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	ar.Sequence = target
	encoded, err := state.SerializeAccountRoot(ar)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	if err := view.Update(k, encoded); err != nil {
		t.Fatalf("view.Update: %v", err)
	}
}
