package localtxs_test

import (
	"bytes"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/localtxs"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

const holdLedgers uint32 = 5

func buildSignedBlob(t *testing.T, env *jtx.TestEnv, txn tx.Transaction, signer *jtx.Account) []byte {
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
func pendingFromPay(t *testing.T, env *jtx.TestEnv, alice, bob *jtx.Account, seq uint32) (openledger.PendingTx, *ledger.Ledger) {
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
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
// ledger N is dropped when the sweep view's seq exceeds the retention window.
func TestLocalTxs_Sweep_ExpiresOldEntries(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	// Advance the LCL enough that we can anchor strictly before
	// view.Sequence() - holdLedgers.
	for range holdLedgers + 3 {
		env.Close()
	}

	ptx, view := pendingFromPay(t, env, alice, bob, env.Seq(alice))
	if view.Sequence() <= holdLedgers+2 {
		t.Fatalf("test setup: view seq %d too small to expire", view.Sequence())
	}
	anchor := view.Sequence() - holdLedgers - 2

	pool := localtxs.New()
	pool.PushBack(anchor, ptx)

	if err := pool.Sweep(view); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after expiring Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_Sweep_KeepsFreshEntries verifies that an entry pushed at
// the current ledger survives a Sweep against that same ledger.
func TestLocalTxs_Sweep_KeepsFreshEntries(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	// Build a payment with a *future* sequence so the seq-advance check
	// doesn't drop it (alice's AccountRoot.Sequence is unchanged by the
	// env.Close inside pendingFromPay).
	futureSeq := env.Seq(alice) + 10
	ptx, view := pendingFromPay(t, env, alice, bob, futureSeq)

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	if err := pool.Sweep(view); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := pool.Size(); got != 1 {
		t.Errorf("Size after fresh Sweep = %d, want 1", got)
	}
}

// TestLocalTxs_Sweep_DropsBySeqAdvance verifies that when the view's
// AccountRoot.Sequence has advanced past the tx's sequence, the entry
// is dropped (mirrors LocalTxs.cpp:163-164 tefPAST_SEQ branch).
func TestLocalTxs_Sweep_DropsBySeqAdvance(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	txSeq := env.Seq(alice)
	ptx, view := pendingFromPay(t, env, alice, bob, txSeq)

	// Bump alice's AccountRoot.Sequence to txSeq + 1 in the view.
	bumpAccountSequence(t, view, alice, txSeq+1)

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	if err := pool.Sweep(view); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after seq-advance Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_Sweep_DropsAlreadyValidatedTx verifies that an entry
// already present in the view's tx map is dropped (LocalTxs.cpp:150-151).
func TestLocalTxs_Sweep_DropsAlreadyValidatedTx(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	// Use a future seq so the seq-advance check won't fire — we want to
	// isolate the txExists branch.
	futureSeq := env.Seq(alice) + 10
	ptx, view := pendingFromPay(t, env, alice, bob, futureSeq)

	if err := view.AddTransaction(ptx.Hash, ptx.Blob); err != nil {
		t.Fatalf("AddTransaction: %v", err)
	}

	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)

	if err := pool.Sweep(view); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := pool.Size(); got != 0 {
		t.Errorf("Size after txExists Sweep = %d, want 0", got)
	}
}

// TestLocalTxs_GetTxSet_CanonicalOrder verifies the (account, sequence,
// hash) ordering with zero salt (LocalTxs.cpp:126).
func TestLocalTxs_GetTxSet_CanonicalOrder(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
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

func TestLocalTxs_GetReturnsOwnedTransaction(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	ptx, _ := pendingFromPay(t, env, alice, bob, env.Seq(alice)+10)

	pool := localtxs.New()
	pool.PushBack(1, ptx)
	got, ok := pool.Get(ptx.Hash)
	if !ok {
		t.Fatal("Get returned ok=false for a held transaction")
	}
	if !bytes.Equal(got.Blob, ptx.Blob) {
		t.Fatal("Get returned a different transaction blob")
	}
	got.Blob[0] ^= 0xff
	again, ok := pool.Get(ptx.Hash)
	if !ok || !bytes.Equal(again.Blob, ptx.Blob) {
		t.Fatal("Get did not return an owned blob copy")
	}
}

// TestLocalTxs_GetTxSet_SortsBySequenceWithinAccount verifies that two
// pending txs from the same account are returned in ascending sequence
// order regardless of push order.
func TestLocalTxs_GetTxSet_SortsBySequenceWithinAccount(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)

	seq := env.Seq(alice)
	tx1 := buildPendingTx(t, env, alice, bob, seq+10)
	tx2 := buildPendingTx(t, env, alice, bob, seq+11)
	tx3 := buildPendingTx(t, env, alice, bob, seq+12)

	pool := localtxs.New()
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

func TestLocalTxs_DuplicateAdmissionDoesNotShortenExpiry(t *testing.T) {
	env := jtx.NewTestEnv(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	env.Fund(alice, bob)
	for range holdLedgers + 6 {
		env.Close()
	}

	ptx, view := pendingFromPay(t, env, alice, bob, env.Seq(alice)+100)
	pool := localtxs.New()
	pool.PushBack(view.Sequence(), ptx)
	pool.PushBack(view.Sequence()-holdLedgers-1, ptx)

	if err := pool.Sweep(view); err != nil {
		t.Fatalf("Sweep: %v", err)
	}
	if got := pool.Size(); got != 1 {
		t.Fatalf("Size after older duplicate and Sweep = %d, want 1", got)
	}
}

func TestLocalTxs_OwnsInputAndSnapshotBlobs(t *testing.T) {
	ptx := openledger.PendingTx{
		Blob: []byte{1, 2, 3, 4},
		Hash: [32]byte{1},
	}
	pool := localtxs.New()
	pool.PushBack(1, ptx)
	ptx.Blob[0] = 9

	first := pool.GetTxSet()
	if got := first[0].Blob; !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("stored blob = %v, want owned input copy", got)
	}
	first[0].Blob[1] = 9
	second := pool.GetTxSet()
	if got := second[0].Blob; !bytes.Equal(got, []byte{1, 2, 3, 4}) {
		t.Fatalf("stored blob after snapshot mutation = %v", got)
	}
}

func TestLocalTxs_SweepRejectsNilView(t *testing.T) {
	if err := localtxs.New().Sweep(nil); err == nil {
		t.Fatal("Sweep(nil) succeeded")
	}
	var view *ledger.Ledger
	if err := localtxs.New().Sweep(view); err == nil {
		t.Fatal("Sweep((*ledger.Ledger)(nil)) succeeded")
	}
}

func TestLocalTxs_SweepReturnsStateReadErrorWithoutMutation(t *testing.T) {
	pool := localtxs.New()
	pool.PushBack(10, openledger.PendingTx{
		Blob:     []byte{1},
		Hash:     [32]byte{1},
		Account:  [20]byte{2},
		Sequence: 1,
	})
	wantErr := errors.New("state read failed")
	err := pool.Sweep(errorSweepView{err: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Sweep error = %v, want %v", err, wantErr)
	}
	if got := pool.Size(); got != 1 {
		t.Fatalf("Size after failed Sweep = %d, want 1", got)
	}
}

func TestLocalTxs_SweepReturnsMembershipErrorWithoutMutation(t *testing.T) {
	pool := localtxs.New()
	pool.PushBack(10, openledger.PendingTx{
		Blob:     []byte{1},
		Hash:     [32]byte{1},
		Account:  [20]byte{2},
		Sequence: 1,
	})
	wantErr := errors.New("transaction membership failed")
	err := pool.Sweep(errorSweepView{txErr: wantErr})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Sweep error = %v, want %v", err, wantErr)
	}
	if got := pool.Size(); got != 1 {
		t.Fatalf("Size after failed Sweep = %d, want 1", got)
	}
}

type errorSweepView struct {
	err   error
	txErr error
}

func (v errorSweepView) Sequence() uint32 {
	return 10
}

func (v errorSweepView) TxExists([32]byte) (bool, error) {
	return false, v.txErr
}

func (v errorSweepView) Read(keylet.Keylet) ([]byte, error) {
	return nil, v.err
}

func (v errorSweepView) Exists(keylet.Keylet) (bool, error) {
	return false, v.err
}

// buildPendingTx constructs a signed payment from `from` to `to` at the
// given sequence and returns the parsed PendingTx.
func buildPendingTx(t *testing.T, env *jtx.TestEnv, from, to *jtx.Account, seq uint32) openledger.PendingTx {
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
func bumpAccountSequence(t *testing.T, view *ledger.Ledger, acc *jtx.Account, target uint32) {
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
