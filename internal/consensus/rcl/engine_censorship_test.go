package rcl

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// TestEngine_CensorshipWarnsOnPersistentExclusion drives the engine's accept
// path and proves the detector actually fires. A tx we proposed and keep
// tracking, that stays out of the accepted set — here because dispute
// resolution forced us to vote NO on it as the network kept excluding it —
// must warn once its exclusion crosses the interval.
//
// This is the exact case a predicate that dropped disputed-NO txs would
// silence: every tx the predicate ever sees (tracked but not accepted) is one
// we voted NO on, so dropping them disables the detector entirely. The captured
// warn is the regression guard against re-introducing that drop.
func TestEngine_CensorshipWarnsOnPersistentExclusion(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.opMode = consensus.OpModeFull
	adaptor.validator = true

	engine := NewEngine(adaptor, DefaultConfig())
	engine.StartRound(consensus.RoundID{Seq: 101, ParentHash: consensus.LedgerID{1}}, true)
	driveToEstablish(t, engine, adaptor)

	// Our accepted position for this round holds no transactions, so the tx we
	// track below is necessarily tracked-but-not-accepted at check time.
	emptySet, err := adaptor.BuildTxSet(nil)
	if err != nil {
		t.Fatalf("BuildTxSet(nil): %v", err)
	}

	x := censorTxID(77)

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	engine.mu.Lock()
	curr := engine.prevLedger.Seq() + 1
	engine.state.HaveCorrectLCL = true
	engine.ourTxSet = emptySet
	// x was first proposed exactly one interval ago and never included; we've
	// since been forced to vote NO on it (why it isn't in our accepted set).
	engine.censorship.propose([]consensus.TxID{x}, curr-censorshipWarnInterval)
	engine.disputeTracker.CreateDispute(x, []byte{0xAA}, false)
	engine.acceptLedger(consensus.ResultSuccess)
	engine.mu.Unlock()

	out := buf.String()
	if !strings.Contains(out, "censorship-warn") {
		t.Fatalf("expected a censorship warning for a tx excluded for %d ledgers; got:\n%s",
			censorshipWarnInterval, out)
	}
	if !strings.Contains(out, "waited=15") {
		t.Fatalf("censorship warning should report waited=%d; got:\n%s", censorshipWarnInterval, out)
	}
}
