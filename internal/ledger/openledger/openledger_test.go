package openledger_test

import (
	"encoding/hex"
	"sync"
	"sync/atomic"
	"testing"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
	testenv "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/payment"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

func buildSignedBlobOL(t *testing.T, env *testenv.TestEnv, txn tx.Transaction, signer *testenv.Account) []byte {
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

// closedParent closes the env once and returns the LCL, providing a clean
// closed ledger to anchor a freshly constructed OpenLedger against.
func closedParent(t *testing.T, env *testenv.TestEnv) *ledger.Ledger {
	t.Helper()
	env.Close()
	parent := env.LastClosedLedger()
	if parent == nil {
		t.Fatal("no LastClosedLedger after Close")
	}
	return parent
}

// TestOpenLedger_NewCurrent_SnapshotsClosed verifies that New() produces an
// OpenLedger whose Current() view is sequence = parent + 1 and has an empty
// tx map. Mirrors rippled OpenLedger ctor (OpenLedger.cpp:35-41) + create().
func TestOpenLedger_NewCurrent_SnapshotsClosed(t *testing.T) {
	env := testenv.NewTestEnv(t)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	cur := ol.Current()
	if cur == nil {
		t.Fatal("Current() returned nil")
	}
	if got, want := cur.Sequence(), parent.Sequence()+1; got != want {
		t.Errorf("Current().Sequence() = %d, want %d", got, want)
	}
	count := 0
	if err := cur.ForEachTransaction(func(_ [32]byte, _ []byte) bool {
		count++
		return true
	}); err != nil {
		t.Fatalf("ForEachTransaction: %v", err)
	}
	if count != 0 {
		t.Errorf("expected empty tx map, got %d entries", count)
	}
}

// TestOpenLedger_Submit_AppliesAndPublishes verifies that a successful Submit
// publishes a new Current(), keeps the tx in it, and changes state-map hash.
// Mirrors NetworkOPsImp::apply -> openLedger().modify (NetworkOPs.cpp:1507).
func TestOpenLedger_Submit_AppliesAndPublishes(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	pre := ol.Current()
	preStateHash, err := pre.StateMapHash()
	if err != nil {
		t.Fatalf("pre StateMapHash: %v", err)
	}

	pay := payment.Pay(alice, bob, 1_000_000).
		Sequence(env.Seq(alice)).
		Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   pre.Sequence(),
		NetworkID:        0,
	}

	changed, result := ol.Submit(pt, cfg)
	if !changed {
		t.Fatalf("Submit changed=false, want true; result=%v", result)
	}
	if result != openledger.ResultSuccess {
		t.Fatalf("Submit result=%v, want ResultSuccess", result)
	}

	post := ol.Current()
	if post == pre {
		t.Errorf("Current() pointer unchanged after successful Submit")
	}
	if !post.TxExists(pt.Hash) {
		t.Errorf("published view missing submitted tx")
	}
	postStateHash, err := post.StateMapHash()
	if err != nil {
		t.Fatalf("post StateMapHash: %v", err)
	}
	if postStateHash == preStateHash {
		t.Errorf("state map hash unchanged after successful Submit")
	}
	// The pre-Submit Current() must not have been mutated (snapshot
	// isolation — readers of the old pointer keep their view).
	if pre.TxExists(pt.Hash) {
		t.Errorf("old Current() pointer was mutated — snapshot isolation broken")
	}
}

// TestOpenLedger_Modify_ReturnsFalse_DoesNotPublish verifies the publish gate
// (OpenLedger.cpp:63-66): a Modify callback returning false must not swap.
func TestOpenLedger_Modify_ReturnsFalse_DoesNotPublish(t *testing.T) {
	env := testenv.NewTestEnv(t)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	pre := ol.Current()
	changed := ol.Modify(func(_ *ledger.Ledger) bool { return false })
	if changed {
		t.Errorf("Modify returned true for a no-op callback")
	}
	post := ol.Current()
	if post != pre {
		t.Errorf("Current() pointer swapped despite Modify returning false")
	}
}

// TestOpenLedger_ConcurrentSubmitReader spawns parallel Submit + Current
// goroutines. Validates: no panic, every Current() observation is non-nil,
// and the final txCount matches the number of successful Submits.
func TestOpenLedger_ConcurrentSubmitReader(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	const N = 50
	senders := make([]*testenv.Account, N)
	for i := 0; i < N; i++ {
		senders[i] = testenv.NewAccount("sender" + itoa(i))
	}
	dest := testenv.NewAccount("dest")
	// Fund each sender + dest. Fund accepts variadic.
	all := append([]*testenv.Account{dest}, senders...)
	env.Fund(all...)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	// Pre-build one pending tx per sender so the per-goroutine body is
	// strictly the Submit call (no signing inside the parallel section,
	// since SignWith / Seq mutate shared env state).
	type prepared struct {
		pt  openledger.PendingTx
		cfg openledger.ApplyConfig
	}
	prepped := make([]prepared, N)
	for i := 0; i < N; i++ {
		pay := payment.Pay(senders[i], dest, 1_000_000).
			Sequence(env.Seq(senders[i])).
			Build()
		blob := buildSignedBlobOL(t, env, pay, senders[i])
		pt, err := openledger.ParsePendingTx(blob)
		if err != nil {
			t.Fatalf("ParsePendingTx[%d]: %v", i, err)
		}
		prepped[i] = prepared{
			pt: pt,
			cfg: openledger.ApplyConfig{
				BaseFee:          10,
				ReserveBase:      200_000_000,
				ReserveIncrement: 50_000_000,
				LedgerSequence:   ol.Current().Sequence(),
				NetworkID:        0,
			},
		}
	}

	var wg sync.WaitGroup
	var successCount atomic.Int32
	var readerObservations atomic.Int32

	// Writers
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			changed, result := ol.Submit(prepped[idx].pt, prepped[idx].cfg)
			if changed && result == openledger.ResultSuccess {
				successCount.Add(1)
			}
		}(i)
	}

	// Readers
	stop := make(chan struct{})
	for r := 0; r < N; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				cur := ol.Current()
				if cur == nil {
					t.Errorf("Current() returned nil during concurrent reads")
					return
				}
				_ = cur.Sequence()
				readerObservations.Add(1)
			}
		}()
	}

	// Wait for writers, then signal readers to stop.
	writersDone := make(chan struct{})
	go func() {
		for i := 0; i < N; i++ {
			// no-op; the writers are joined below via the same wg.
		}
		close(writersDone)
	}()

	// Simple approach: wait for ALL goroutines but kick readers via stop.
	// Drain writers first by joining a separate WG would require split; we
	// instead spin briefly until successCount + parse-failures equals N's
	// upper bound, then stop readers.
	done := make(chan struct{})
	go func() {
		// Writers self-terminate; once successCount stops climbing we close.
		var last int32 = -1
		for {
			cur := successCount.Load()
			if cur == int32(N) || (cur == last && cur > 0) {
				// Either all succeeded or we've stabilized.
				close(done)
				return
			}
			last = cur
			// Yield without sleep; this loop only runs in the test path.
			for j := 0; j < 1000; j++ {
				_ = j
			}
		}
	}()

	<-done
	close(stop)
	wg.Wait()

	finalCur := ol.Current()
	if finalCur == nil {
		t.Fatal("final Current() is nil")
	}
	count := 0
	if err := finalCur.ForEachTransaction(func(_ [32]byte, _ []byte) bool {
		count++
		return true
	}); err != nil {
		t.Fatalf("final ForEachTransaction: %v", err)
	}
	if got := int(successCount.Load()); count != got {
		t.Errorf("final tx count = %d, but successful Submits = %d", count, got)
	}
	if readerObservations.Load() == 0 {
		t.Errorf("readers never observed a Current() — racy test setup")
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [12]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

// newClosedFrom returns a fresh closed-shaped Ledger derived from parent
// via MutableSnapshot, used as the "newLCL" argument to Accept. The
// state is identical to parent so any tx submitted to the prior open
// view is still applicable against this new closed ledger.
func newClosedFrom(t *testing.T, parent *ledger.Ledger) *ledger.Ledger {
	t.Helper()
	snap, err := parent.MutableSnapshot()
	if err != nil {
		t.Fatalf("MutableSnapshot: %v", err)
	}
	return snap
}

// TestOpenLedger_Accept_ReplaysCurrentTxs verifies that Accept replays
// the prior current view's transactions onto the new working view.
// Mirrors OpenLedger::accept (OpenLedger.cpp:96-112).
func TestOpenLedger_Accept_ReplaysCurrentTxs(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	carol := testenv.NewAccount("carol")
	env.Fund(alice, bob, carol)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   ol.Current().Sequence(),
		NetworkID:        0,
	}

	// Submit two independent txs to current.
	pay1 := payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)).Build()
	blob1 := buildSignedBlobOL(t, env, pay1, alice)
	pt1, err := openledger.ParsePendingTx(blob1)
	if err != nil {
		t.Fatalf("ParsePendingTx pay1: %v", err)
	}
	if changed, result := ol.Submit(pt1, cfg); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit pay1: changed=%v result=%v", changed, result)
	}

	pay2 := payment.Pay(bob, carol, 2_000_000).Sequence(env.Seq(bob)).Build()
	blob2 := buildSignedBlobOL(t, env, pay2, bob)
	pt2, err := openledger.ParsePendingTx(blob2)
	if err != nil {
		t.Fatalf("ParsePendingTx pay2: %v", err)
	}
	if changed, result := ol.Submit(pt2, cfg); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit pay2: changed=%v result=%v", changed, result)
	}

	// New closed ledger sharing state with parent (no tx in its tx map).
	newClosed := newClosedFrom(t, parent)
	var retries []openledger.PendingTx

	if err := ol.Accept(newClosed, nil, false, &retries, cfg); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}
	cur := ol.Current()
	if !cur.TxExists(pt1.Hash) {
		t.Errorf("post-Accept Current() missing pay1")
	}
	if !cur.TxExists(pt2.Hash) {
		t.Errorf("post-Accept Current() missing pay2")
	}
	if got, want := cur.Sequence(), newClosed.Sequence()+1; got != want {
		t.Errorf("Current().Sequence() = %d, want %d", got, want)
	}
}

// TestOpenLedger_Accept_NoDoubleApply verifies the Accept replay does
// not double-apply a tx that appears in both the prior current view
// AND the locals slice. The dedup happens via the working view's
// per-tx TxExists check inside ApplyTxs (apply.go:138 — the same
// mechanism that pre-filters parent-committed txs in rippled per
// OpenLedger.h:226-228).
//
// This is the goxrpl-side equivalent of rippled's `check` parameter:
// once txA is committed to the working view by the current-replay
// pass, the locals pass sees it and skips.
func TestOpenLedger_Accept_NoDoubleApply(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   ol.Current().Sequence(),
		NetworkID:        0,
	}

	// Submit txA to current open view.
	pay := payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)).Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	if changed, result := ol.Submit(pt, cfg); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit: changed=%v result=%v", changed, result)
	}

	newClosed := newClosedFrom(t, parent)
	var retries []openledger.PendingTx

	// Pass the same pt in `locals` — current replay will commit it to
	// the working view, then the locals replay must see it via TxExists
	// and skip (no double-apply).
	if err := ol.Accept(newClosed, []openledger.PendingTx{pt}, false, &retries, cfg); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}

	// Working view should contain exactly one entry for txA (the one
	// committed during current-replay) — locals replay must skip the
	// duplicate.
	cur := ol.Current()
	if !cur.TxExists(pt.Hash) {
		t.Errorf("working view missing txA after Accept")
	}
	count := 0
	_ = cur.ForEachTransaction(func(_ [32]byte, _ []byte) bool {
		count++
		return true
	})
	if count != 1 {
		t.Errorf("working view tx map: got %d entries, want 1 (no double-apply)", count)
	}
}

// TestOpenLedger_Accept_LocalsApplied verifies that locals passed to
// Accept are applied to the new working view (OpenLedger.cpp:117-118).
func TestOpenLedger_Accept_LocalsApplied(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   ol.Current().Sequence(),
		NetworkID:        0,
	}

	pay := payment.Pay(alice, bob, 3_000_000).Sequence(env.Seq(alice)).Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	ptL, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	newClosed := newClosedFrom(t, parent)
	var retries []openledger.PendingTx

	if err := ol.Accept(newClosed, []openledger.PendingTx{ptL}, false, &retries, cfg); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}
	if !ol.Current().TxExists(ptL.Hash) {
		t.Errorf("local tx missing from new Current()")
	}
}

// TestOpenLedger_Accept_RetriesFirst_ReplaysHeldTx verifies that with
// retriesFirst=true, a held tx in *retries is replayed against the new
// working view. Mirrors OpenLedger.cpp:85-90.
func TestOpenLedger_Accept_RetriesFirst_ReplaysHeldTx(t *testing.T) {
	env := testenv.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := testenv.NewAccount("alice")
	bob := testenv.NewAccount("bob")
	env.Fund(alice, bob)
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   ol.Current().Sequence(),
		NetworkID:        0,
	}

	// Build a held-retry tx — a vanilla payment that applies cleanly
	// against newLCL. Per the spec, the load-bearing assertion is "a tx
	// in the input retries slice ends up in the new view".
	pay := payment.Pay(alice, bob, 4_000_000).Sequence(env.Seq(alice)).Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	held, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	newClosed := newClosedFrom(t, parent)
	retries := []openledger.PendingTx{held}

	if err := ol.Accept(newClosed, nil, true, &retries, cfg); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !ol.Current().TxExists(held.Hash) {
		t.Errorf("held retry tx missing from new Current()")
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0 (tx should have applied)", len(retries))
	}
}
