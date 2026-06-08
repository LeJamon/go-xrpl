package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readClosedLedgerHashesSLE decodes the rolling LedgerHashes SLE from
// the supplied closed ledger and returns (LastLedgerSequence, Hashes).
func readClosedLedgerHashesSLE(t *testing.T, l *ledger.Ledger) (uint32, []string) {
	t.Helper()
	raw, err := l.Read(keylet.LedgerHashes())
	if err != nil {
		t.Fatalf("read LedgerHashes SLE: %v", err)
	}
	if raw == nil {
		return 0, nil
	}
	decoded, err := binarycodec.DecodeBytes(raw)
	if err != nil {
		t.Fatalf("decode LedgerHashes SLE: %v", err)
	}
	var lastSeq uint32
	switch v := decoded["LastLedgerSequence"].(type) {
	case uint32:
		lastSeq = v
	case int:
		lastSeq = uint32(v)
	case int64:
		lastSeq = uint32(v)
	case uint64:
		lastSeq = uint32(v)
	}
	var hashes []string
	switch v := decoded["Hashes"].(type) {
	case []string:
		hashes = append([]string(nil), v...)
	case []any:
		for _, h := range v {
			hashes = append(hashes, h.(string))
		}
	}
	return lastSeq, hashes
}

// TestService_AcceptConsensusResult_LedgerHashesInvariants reproduces
// issue #470 against the consensus-driven build path. Drives
// AcceptConsensusResult with empty tx sets across many sequential
// ledgers and asserts the LedgerHashes invariants on every step:
//
//   - LastLedgerSequence == parent.seq
//   - len(Hashes)        == parent.seq (capped at 256)
//   - last hash entry    == parent.Hash()
//   - the ledger's own hash never appears in Hashes
//
// Issue #470 reports that goxrpl validators in a mixed
// rippled/goxrpl Kurtosis cluster fork at seq 17 because the
// LedgerHashes pseudo-object inside the locally-built ledger is
// computed with the wrong (LastLedgerSequence, Hashes) tuple. If the
// invariants below pass for a long enough run, the consensus build
// path is rippled-faithful.
func TestService_AcceptConsensusResult_LedgerHashesInvariants(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false // exercise the consensus close path
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const closes = 20
	closeTime := time.Unix(1700000000, 0)
	for i := range closes {
		parent := svc.GetClosedLedger()
		if parent == nil {
			t.Fatalf("closedLedger nil after %d closes", i)
		}
		closeTime = closeTime.Add(2 * time.Second)
		seq, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, closeTime, true)
		if err != nil {
			t.Fatalf("AcceptConsensusResult at iter %d: %v", i, err)
		}

		closed := svc.GetClosedLedger()
		if closed == nil {
			t.Fatalf("closedLedger nil after AcceptConsensusResult #%d", i)
		}
		if closed.Sequence() != seq {
			t.Fatalf("closedLedger seq %d != returned %d", closed.Sequence(), seq)
		}

		lastSeq, hashStrs := readClosedLedgerHashesSLE(t, closed)

		// LastLedgerSequence must equal the parent's seq, not self.
		if want := seq - 1; lastSeq != want {
			t.Errorf("ledger %d: LastLedgerSequence = %d, want %d", seq, lastSeq, want)
		}

		// Length must match the parent's seq (rolling cap 256).
		wantLen := min(int(seq-1), 256)
		if got := len(hashStrs); got != wantLen {
			t.Errorf("ledger %d: len(Hashes) = %d, want %d (entries: %v)", seq, got, wantLen, hashStrs)
		}

		// Self-inclusion check.
		selfHex := fmt.Sprintf("%064X", closed.Hash())
		for idx, h := range hashStrs {
			if h == selfHex {
				t.Errorf("ledger %d: LedgerHashes contains own hash %s at index %d — self-inclusion bug from issue #470", seq, h, idx)
			}
		}

		// Last entry must equal parent.Hash().
		if len(hashStrs) > 0 {
			wantLast := fmt.Sprintf("%064X", parent.Hash())
			if got := hashStrs[len(hashStrs)-1]; got != wantLast {
				t.Errorf("ledger %d: last Hashes entry = %s, want parent hash %s", seq, got, wantLast)
			}
		}
	}
}

// TestService_AcceptConsensusResult_RebuildSameSeq_NoSkipListLeak
// exercises the consensus chain-switch case: AcceptConsensusResult
// is called twice for the same seq (different parents), simulating
// the wrong-ledger detection path. The second close must produce a
// clean LedgerHashes object — the first attempt's appended
// parent-hash must NOT leak into the second's state.
//
// Issue #470's three ghost hashes are consistent with this leakage
// pattern: a stale state map repeatedly mutated across speculative
// rounds. If chain switches reset state properly, this test passes.
func TestService_AcceptConsensusResult_RebuildSameSeq_NoSkipListLeak(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Standalone = false
	svc, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	closeTime := time.Unix(1700000000, 0)
	// Drive a few clean consensus rounds first to build history.
	for i := range 5 {
		closeTime = closeTime.Add(2 * time.Second)
		parent := svc.GetClosedLedger()
		if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, closeTime, true); err != nil {
			t.Fatalf("warm-up close %d: %v", i, err)
		}
	}

	// Now rebuild the next seq twice — first close, then a chain
	// switch back to the same parent and re-close.
	parent := svc.GetClosedLedger()
	parentSeq := parent.Sequence()

	closeTime = closeTime.Add(2 * time.Second)
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, closeTime, true); err != nil {
		t.Fatalf("first build: %v", err)
	}

	// Chain switch: drive AcceptConsensusResult with the original
	// parent again. This resets s.closedLedger to parent and rebuilds
	// the open ledger.
	closeTime = closeTime.Add(2 * time.Second)
	if _, err := svc.AcceptConsensusResult(context.TODO(), parent, nil, closeTime, true); err != nil {
		t.Fatalf("second build (chain switch): %v", err)
	}

	rebuilt := svc.GetClosedLedger()
	if rebuilt.Sequence() != parentSeq+1 {
		t.Fatalf("after chain switch: expected closed seq %d, got %d", parentSeq+1, rebuilt.Sequence())
	}

	lastSeq, hashStrs := readClosedLedgerHashesSLE(t, rebuilt)
	if want := parentSeq; lastSeq != want {
		t.Errorf("rebuilt: LastLedgerSequence = %d, want %d (chain-switch leaked stale state)", lastSeq, want)
	}
	wantLen := min(int(parentSeq), 256)
	if got := len(hashStrs); got != wantLen {
		t.Errorf("rebuilt: len(Hashes) = %d, want %d (chain-switch leaked extra entries)", got, wantLen)
	}
	selfHex := fmt.Sprintf("%064X", rebuilt.Hash())
	for idx, h := range hashStrs {
		if h == selfHex {
			t.Errorf("rebuilt: self-inclusion of %s at index %d", h, idx)
		}
	}
}

// TestService_AcceptConsensusResult_SiblingForkSwitchesChain reproduces
// the actual root cause of issue #470. Two services drive consensus in
// parallel from the same genesis; service A is the "alt" chain (its
// local build of seq N+1 produces hash X), service B is the "canonical"
// chain (its build for seq N+1 produces a different hash Y because of
// a different close time / tx set). When service A is then asked to
// build seq N+2 with the canonical parent (hash Y) — simulating the
// post-sync state where A has adopted the canonical seq N+1 — the
// chain-switch must trigger even though both A's closed and the
// supplied parent share the same seq. Without the switch, A keeps
// snapshotting its own alt's state map and stamps the alt's
// LedgerHashes ancestors into seq N+2, producing a divergent chain
// forever.
func TestService_AcceptConsensusResult_SiblingForkSwitchesChain(t *testing.T) {
	mkSvc := func() *Service {
		cfg := DefaultConfig()
		cfg.Standalone = false
		svc, err := New(cfg)
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if err := svc.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		return svc
	}

	alt := mkSvc()
	canonical := mkSvc()

	// Drive both services through a few clean rounds with the same
	// close times so they stay byte-identical.
	closeTime := time.Unix(1700000000, 0)
	for i := range 5 {
		closeTime = closeTime.Add(2 * time.Second)
		for _, svc := range []*Service{alt, canonical} {
			if _, err := svc.AcceptConsensusResult(context.TODO(), svc.GetClosedLedger(), nil, closeTime, true); err != nil {
				t.Fatalf("warm-up close iter %d: %v", i, err)
			}
		}
	}

	altSync := alt.GetClosedLedger()
	canonSync := canonical.GetClosedLedger()
	if altSync.Hash() != canonSync.Hash() {
		t.Fatalf("warm-up failed to keep services in sync: alt=%x canonical=%x", altSync.Hash(), canonSync.Hash())
	}

	// Now diverge: build seq N+1 on each with DIFFERENT close times so
	// they produce sibling hashes at the same seq.
	altCloseTime := closeTime.Add(2 * time.Second)
	canonCloseTime := closeTime.Add(7 * time.Second)
	if _, err := alt.AcceptConsensusResult(context.TODO(), altSync, nil, altCloseTime, true); err != nil {
		t.Fatalf("alt fork build: %v", err)
	}
	if _, err := canonical.AcceptConsensusResult(context.TODO(), canonSync, nil, canonCloseTime, true); err != nil {
		t.Fatalf("canonical fork build: %v", err)
	}
	altTip := alt.GetClosedLedger()
	canonTip := canonical.GetClosedLedger()
	if altTip.Sequence() != canonTip.Sequence() {
		t.Fatalf("sibling fork seqs differ: alt=%d canonical=%d", altTip.Sequence(), canonTip.Sequence())
	}
	if altTip.Hash() == canonTip.Hash() {
		t.Fatalf("sibling fork hashes equal — failed to diverge")
	}

	// Simulate post-adoption: drive alt's next consensus close with
	// canonical's tip as the parent. Pre-fix this was a no-op for the
	// chain-switch check (seq matches), and alt's openLedger stayed
	// pinned to the alt-built state.
	nextCloseTime := altCloseTime.Add(2 * time.Second)
	if _, err := alt.AcceptConsensusResult(context.TODO(), canonTip, nil, nextCloseTime, true); err != nil {
		t.Fatalf("alt rebuild on canonical parent: %v", err)
	}

	rebuilt := alt.GetClosedLedger()
	if rebuilt.Sequence() != canonTip.Sequence()+1 {
		t.Fatalf("rebuilt seq = %d, want %d", rebuilt.Sequence(), canonTip.Sequence()+1)
	}

	_, hashStrs := readClosedLedgerHashesSLE(t, rebuilt)
	// The LedgerHashes inside the rebuilt seq must end in canonTip.Hash()
	// — the canonical parent's hash — NOT altTip.Hash().
	wantLast := fmt.Sprintf("%064X", canonTip.Hash())
	dontWant := fmt.Sprintf("%064X", altTip.Hash())
	if len(hashStrs) == 0 {
		t.Fatalf("rebuilt LedgerHashes is empty")
	}
	gotLast := hashStrs[len(hashStrs)-1]
	if gotLast != wantLast {
		t.Errorf("rebuilt last LedgerHashes entry = %s, want canonical parent %s (got %s — alt parent leak: %v)",
			gotLast, wantLast, gotLast, gotLast == dontWant)
	}
	for idx, h := range hashStrs {
		if h == dontWant {
			t.Errorf("rebuilt LedgerHashes[%d] contains alt-chain hash %s — chain switch leaked stale state (issue #470 root cause)", idx, h)
		}
	}
}
