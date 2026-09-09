package openledger_test

import (
	"encoding/hex"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

func buildSignedBlobOL(t *testing.T, env *jtx.TestEnv, txn tx.Transaction, signer *jtx.Account) []byte {
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
func closedParent(t *testing.T, env *jtx.TestEnv) *ledger.Ledger {
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
	env := jtx.NewTestEnv(t)
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
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
		Rules:            amendment.AllSupportedRules(),
	}

	changed, result := ol.Submit(pt, cfg, nil)
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
	if !ledgerTxExists(t, post, pt.Hash) {
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
	if ledgerTxExists(t, pre, pt.Hash) {
		t.Errorf("old Current() pointer was mutated — snapshot isolation broken")
	}
}

// TestOpenLedger_Modify_ReturnsFalse_DoesNotPublish verifies the publish gate
// (OpenLedger.cpp:63-66): a Modify callback returning false must not swap.
func TestOpenLedger_Modify_ReturnsFalse_DoesNotPublish(t *testing.T) {
	env := jtx.NewTestEnv(t)
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
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	const N = 50
	senders := make([]*jtx.Account, N)
	for i := range N {
		senders[i] = jtx.NewAccount("sender" + itoa(i))
	}
	dest := jtx.NewAccount("dest")
	all := append([]*jtx.Account{dest}, senders...)
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
	for i := range N {
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
				Rules:            amendment.AllSupportedRules(),
			},
		}
	}

	var wg sync.WaitGroup
	var successCount atomic.Int32
	stop := make(chan struct{})

	// Start readers BEFORE writers and wait for each to register itself,
	// otherwise on a slow scheduler all writers complete before any reader
	// is scheduled — historically the source of "racy test setup" flakes.
	// The real assertions this test cares about are (a) Current() never
	// returns nil under concurrent Submit, and (b) the final tx count
	// matches the successful Submits; an "at least one observation" check
	// adds no correctness signal and was the spurious failure mode.
	readersReady := make(chan struct{}, N)
	for range N {
		wg.Add(1)
		go func() {
			defer wg.Done()
			readersReady <- struct{}{}
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
			}
		}()
	}
	for range N {
		<-readersReady
	}

	for i := range N {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			changed, result := ol.Submit(prepped[idx].pt, prepped[idx].cfg, nil)
			if changed && result == openledger.ResultSuccess {
				successCount.Add(1)
			}
		}(i)
	}

	done := make(chan struct{})
	go func() {
		// Writers self-terminate; once successCount stops climbing we close.
		var last int32 = -1
		for {
			cur := successCount.Load()
			if cur == int32(N) || (cur == last && cur > 0) {
				close(done)
				return
			}
			last = cur
			// Yield without sleep; this loop only runs in the test path.
			for j := range 1000 {
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

// TestOpenLedger_Accept_ReplaysCurrentTxs verifies that Accept replays
// the prior current view's transactions onto the new working view.
// Mirrors OpenLedger::accept (OpenLedger.cpp:96-112).
func TestOpenLedger_Accept_ReplaysCurrentTxs(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
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
		Rules:            amendment.AllSupportedRules(),
	}

	// Submit two independent txs to current.
	pay1 := payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)).Build()
	blob1 := buildSignedBlobOL(t, env, pay1, alice)
	pt1, err := openledger.ParsePendingTx(blob1)
	if err != nil {
		t.Fatalf("ParsePendingTx pay1: %v", err)
	}
	if changed, result := ol.Submit(pt1, cfg, nil); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit pay1: changed=%v result=%v", changed, result)
	}

	pay2 := payment.Pay(bob, carol, 2_000_000).Sequence(env.Seq(bob)).Build()
	blob2 := buildSignedBlobOL(t, env, pay2, bob)
	pt2, err := openledger.ParsePendingTx(blob2)
	if err != nil {
		t.Fatalf("ParsePendingTx pay2: %v", err)
	}
	if changed, result := ol.Submit(pt2, cfg, nil); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit pay2: changed=%v result=%v", changed, result)
	}

	// New closed ledger sharing state with parent (no tx in its tx map).
	newClosed := parent
	var retries []openledger.PendingTx

	if err := ol.Accept(newClosed, nil, false, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}
	cur := ol.Current()
	if !ledgerTxExists(t, cur, pt1.Hash) {
		t.Errorf("post-Accept Current() missing pay1")
	}
	if !ledgerTxExists(t, cur, pt2.Hash) {
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
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
		Rules:            amendment.AllSupportedRules(),
	}

	// Submit txA to current open view.
	pay := payment.Pay(alice, bob, 1_000_000).Sequence(env.Seq(alice)).Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}
	if changed, result := ol.Submit(pt, cfg, nil); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit: changed=%v result=%v", changed, result)
	}

	newClosed := parent
	var retries []openledger.PendingTx

	// Pass the same pt in `locals` — current replay will commit it to
	// the working view, then the locals replay must see it via TxExists
	// and skip (no double-apply).
	if err := ol.Accept(newClosed, []openledger.PendingTx{pt}, false, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}

	// Working view should contain exactly one entry for txA (the one
	// committed during current-replay) — locals replay must skip the
	// duplicate.
	cur := ol.Current()
	if !ledgerTxExists(t, cur, pt.Hash) {
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
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
		Rules:            amendment.AllSupportedRules(),
	}

	pay := payment.Pay(alice, bob, 3_000_000).Sequence(env.Seq(alice)).Build()
	blob := buildSignedBlobOL(t, env, pay, alice)
	ptL, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	newClosed := parent
	var retries []openledger.PendingTx

	if err := ol.Accept(newClosed, []openledger.PendingTx{ptL}, false, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0", len(retries))
	}
	if !ledgerTxExists(t, ol.Current(), ptL.Hash) {
		t.Errorf("local tx missing from new Current()")
	}
}

func TestOpenLedger_AcceptRejectsCorruptReplayLeaf(t *testing.T) {
	for _, test := range []struct {
		name       string
		mutateKey  bool
		mutateBlob bool
		reorder    bool
	}{
		{name: "mismatched key", mutateKey: true},
		{name: "trailing data", mutateBlob: true},
		{name: "noncanonical field order", reorder: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)
			alice := jtx.NewAccount("alice")
			bob := jtx.NewAccount("bob")
			env.Fund(alice, bob)
			parent := closedParent(t, env)

			ol, err := openledger.New(parent, openledger.Config{})
			if err != nil {
				t.Fatalf("openledger.New: %v", err)
			}
			pay := payment.Pay(alice, bob, 3_000_000).Sequence(env.Seq(alice)).Build()
			blob := buildSignedBlobOL(t, env, pay, alice)
			pending, err := openledger.ParsePendingTx(blob)
			if err != nil {
				t.Fatalf("ParsePendingTx: %v", err)
			}
			key := pending.Hash
			if test.reorder {
				if len(blob) < 8 || blob[3] != 0x24 {
					t.Fatalf("unexpected payment field layout: %x", blob[:min(len(blob), 8)])
				}
				noncanonical := make([]byte, 0, len(blob))
				noncanonical = append(noncanonical, blob[3:8]...)
				noncanonical = append(noncanonical, blob[:3]...)
				noncanonical = append(noncanonical, blob[8:]...)
				blob = noncanonical
				pending, err = openledger.ParsePendingTx(blob)
				if err != nil {
					t.Fatalf("noncanonical transaction must remain parseable: %v", err)
				}
				key = pending.Hash
			}
			if test.mutateKey {
				key[0] ^= 0xff
			}
			if test.mutateBlob {
				blob = append(append([]byte(nil), blob...), 0)
			}
			if err := ol.Current().AddTransaction(key, blob); err != nil {
				t.Fatalf("AddTransaction: %v", err)
			}

			before := ol.Current()
			sentinel := openledger.PendingTx{Hash: [32]byte{1}}
			retries := []openledger.PendingTx{sentinel}
			cfg := openledger.ApplyConfig{
				BaseFee:          10,
				ReserveBase:      200_000_000,
				ReserveIncrement: 50_000_000,
				Rules:            amendment.AllSupportedRules(),
			}
			err = ol.Accept(parent, nil, false, &retries, cfg, nil, nil, nil)
			if err == nil {
				t.Fatal("Accept succeeded with a corrupt replay leaf")
			}
			if ol.Current() != before {
				t.Fatal("Accept published a view after replay failure")
			}
			if len(retries) != 1 || retries[0].Hash != sentinel.Hash {
				t.Fatalf("Accept mutated retries on failure: %+v", retries)
			}
		})
	}
}

func TestOpenLedger_AcceptRejectsMalformedLocalBeforeModifier(t *testing.T) {
	env := jtx.NewTestEnv(t)
	parent := closedParent(t, env)
	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}
	queue, err := txq.New(txq.DefaultConfig())
	if err != nil {
		t.Fatalf("txq.New: %v", err)
	}
	before := ol.Current()
	modifierCalled := false
	retries := []openledger.PendingTx{{Hash: [32]byte{1}}}
	err = ol.Accept(
		parent,
		[]openledger.PendingTx{{Hash: [32]byte{2}, Blob: []byte{0xff}}},
		false,
		&retries,
		openledger.ApplyConfig{Rules: amendment.AllSupportedRules()},
		queue,
		func(*ledger.Ledger) { modifierCalled = true },
		nil,
	)
	if err == nil {
		t.Fatal("Accept succeeded with a malformed local transaction")
	}
	if modifierCalled {
		t.Fatal("Accept invoked the modifier before validating locals")
	}
	if ol.Current() != before {
		t.Fatal("Accept published a view after local validation failure")
	}
	if len(retries) != 1 || retries[0].Hash != ([32]byte{1}) {
		t.Fatalf("Accept mutated retries on failure: %+v", retries)
	}
}

// TestOpenLedger_Submit_TecCommits verifies that Submit treats a tec
// engine result as Success+commit (rippled OpenLedger::apply_one,
// OpenLedger.cpp:170-189). The classic scenario: send less than
// ReserveBase to a brand-new account → tecNO_DST_INSUF_XRP. Pre-Mode
// fix, this returned ResultRetry and was silently dropped.
func TestOpenLedger_Submit_TecCommits(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	master := jtx.MasterAccount()
	newAcct := jtx.NewAccount("tec-target-submit")
	parent := closedParent(t, env)

	ol, err := openledger.New(parent, openledger.Config{})
	if err != nil {
		t.Fatalf("openledger.New: %v", err)
	}

	// 100 XRP < 200 XRP reserve → tecNO_DST_INSUF_XRP.
	pay := payment.Pay(master, newAcct, 100_000_000).
		Sequence(env.Seq(master)).
		Build()
	blob := buildSignedBlobOL(t, env, pay, master)
	pt, err := openledger.ParsePendingTx(blob)
	if err != nil {
		t.Fatalf("ParsePendingTx: %v", err)
	}

	cfg := openledger.ApplyConfig{
		BaseFee:          10,
		ReserveBase:      200_000_000,
		ReserveIncrement: 50_000_000,
		LedgerSequence:   ol.Current().Sequence(),
		NetworkID:        0,
		Rules:            amendment.AllSupportedRules(),
	}

	changed, result := ol.Submit(pt, cfg, nil)
	if !changed {
		t.Fatalf("Submit changed=false, want true; result=%v", result)
	}
	if result != openledger.ResultSuccess {
		t.Fatalf("Submit result=%v, want ResultSuccess (tec is Success in OpenLedger semantics)", result)
	}
	if !ledgerTxExists(t, ol.Current(), pt.Hash) {
		t.Errorf("Current() missing tec-committed tx after Submit")
	}
}

// TestOpenLedger_Accept_RetriesFirst_ReplaysHeldTx verifies that with
// retriesFirst=true, a held tx in *retries is replayed against the new
// working view. Mirrors OpenLedger.cpp:85-90.
func TestOpenLedger_Accept_RetriesFirst_ReplaysHeldTx(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
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
		Rules:            amendment.AllSupportedRules(),
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

	newClosed := parent
	retries := []openledger.PendingTx{held}

	if err := ol.Accept(newClosed, nil, true, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if !ledgerTxExists(t, ol.Current(), held.Hash) {
		t.Errorf("held retry tx missing from new Current()")
	}
	if len(retries) != 0 {
		t.Errorf("retries: got %d, want 0 (tx should have applied)", len(retries))
	}
}

func TestOpenLedger_Accept_SeededRetrySettlesAfterCurrentReplay(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("seed-current-alice")
	bob := jtx.NewAccount("seed-current-bob")
	carol := jtx.NewAccount("seed-current-carol")
	env.Fund(alice, carol)
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
		Rules:            amendment.AllSupportedRules(),
	}

	current := payment.Pay(alice, bob, 300_000_000).
		Sequence(env.Seq(alice)).
		Build()
	currentBlob := buildSignedBlobOL(t, env, current, alice)
	currentPending, err := openledger.ParsePendingTx(currentBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx current: %v", err)
	}
	if changed, result := ol.Submit(currentPending, cfg, nil); !changed || result != openledger.ResultSuccess {
		t.Fatalf("Submit current: changed=%v result=%v", changed, result)
	}

	// Bob is created by current, and rippled assigns a newly-created account
	// the successor ledger sequence as its first sequence.
	seeded := payment.Pay(bob, carol, 5_000_000).
		Sequence(ol.Current().Sequence()).
		Build()
	seededBlob := buildSignedBlobOL(t, env, seeded, bob)
	seededPending, err := openledger.ParsePendingTx(seededBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx seeded retry: %v", err)
	}

	retries := []openledger.PendingTx{seededPending}
	if err := ol.Accept(parent, nil, false, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("retries = %d, want 0 after seeded retry settles", len(retries))
	}
	cur := ol.Current()
	if !ledgerTxExists(t, cur, currentPending.Hash) {
		t.Errorf("current replay transaction missing from new Current()")
	}
	if !ledgerTxExists(t, cur, seededPending.Hash) {
		t.Errorf("seeded retry missing after current replay enabled its account")
	}
}

func TestOpenLedger_Accept_SeededRetryVerifiesSignature(t *testing.T) {
	env := jtx.NewTestEnv(t)
	env.SetVerifySignatures(true)

	alice := jtx.NewAccount("seed-signature-alice")
	bob := jtx.NewAccount("seed-signature-bob")
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
		Rules:            amendment.AllSupportedRules(),
	}

	paymentTx := payment.Pay(alice, bob, 1_000_000).
		Sequence(env.Seq(alice)).
		Build()
	blob := buildSignedBlobOL(t, env, paymentTx, alice)
	corrupted, err := tx.ParseFromBinary(blob)
	if err != nil {
		t.Fatalf("ParseFromBinary signed payment: %v", err)
	}
	if corrupted.GetCommon() == nil || corrupted.GetCommon().TxnSignature == "" {
		t.Fatal("signed payment has no transaction signature to corrupt")
	}
	signature := []byte(corrupted.GetCommon().TxnSignature)
	for i := range signature {
		signature[i] = '0'
	}
	corrupted.GetCommon().TxnSignature = string(signature)
	fields, err := corrupted.Flatten()
	if err != nil {
		t.Fatalf("Flatten corrupted payment: %v", err)
	}
	corruptedHex, err := binarycodec.Encode(fields)
	if err != nil {
		t.Fatalf("Encode corrupted payment: %v", err)
	}
	corruptedBlob, err := hex.DecodeString(corruptedHex)
	if err != nil {
		t.Fatalf("Decode corrupted payment: %v", err)
	}
	held, err := openledger.ParsePendingTx(corruptedBlob)
	if err != nil {
		t.Fatalf("ParsePendingTx corrupted payment: %v", err)
	}

	retries := []openledger.PendingTx{held}
	if err := ol.Accept(parent, nil, true, &retries, cfg, nil, nil, nil); err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if len(retries) != 0 {
		t.Fatalf("retries = %d, want 0 after invalid-signature rejection", len(retries))
	}
	if ledgerTxExists(t, ol.Current(), held.Hash) {
		t.Fatal("seeded retry with invalid signature committed to the open ledger")
	}
}

func TestOpenLedger_Accept_RetriesFirstOrdersCompetingTransactions(t *testing.T) {
	for _, retriesFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("retriesFirst=%t", retriesFirst), func(t *testing.T) {
			env := jtx.NewTestEnv(t)
			env.SetVerifySignatures(true)

			alice := jtx.NewAccount("competing-alice")
			bob := jtx.NewAccount("competing-bob")
			carol := jtx.NewAccount("competing-carol")
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
				Rules:            amendment.AllSupportedRules(),
			}

			sequence := env.Seq(alice)
			current := payment.Pay(alice, bob, 1_000_000).
				Sequence(sequence).
				Build()
			currentBlob := buildSignedBlobOL(t, env, current, alice)
			currentPending, err := openledger.ParsePendingTx(currentBlob)
			if err != nil {
				t.Fatalf("ParsePendingTx current: %v", err)
			}
			if changed, result := ol.Submit(currentPending, cfg, nil); !changed || result != openledger.ResultSuccess {
				t.Fatalf("Submit current: changed=%v result=%v", changed, result)
			}

			disputed := payment.Pay(alice, carol, 1_000_000).
				Sequence(sequence).
				Build()
			disputedBlob := buildSignedBlobOL(t, env, disputed, alice)
			disputedPending, err := openledger.ParsePendingTx(disputedBlob)
			if err != nil {
				t.Fatalf("ParsePendingTx disputed: %v", err)
			}

			retries := []openledger.PendingTx{disputedPending}
			if err := ol.Accept(parent, nil, retriesFirst, &retries, cfg, nil, nil, nil); err != nil {
				t.Fatalf("Accept: %v", err)
			}
			if len(retries) != 0 {
				t.Fatalf("retries = %d, want 0 after stale competing tx is rejected", len(retries))
			}

			cur := ol.Current()
			if retriesFirst {
				if !ledgerTxExists(t, cur, disputedPending.Hash) {
					t.Errorf("retriesFirst=true: disputed retry missing from new Current()")
				}
				if ledgerTxExists(t, cur, currentPending.Hash) {
					t.Errorf("retriesFirst=true: prior current tx won despite retry-first ordering")
				}
			} else {
				if ledgerTxExists(t, cur, disputedPending.Hash) {
					t.Errorf("retriesFirst=false: disputed retry won despite current replay ordering")
				}
				if !ledgerTxExists(t, cur, currentPending.Hash) {
					t.Errorf("retriesFirst=false: prior current tx missing from new Current()")
				}
			}
		})
	}
}
