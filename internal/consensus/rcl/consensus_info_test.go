package rcl

import (
	"fmt"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// TestEngine_GetJSON_Defaults pins the always-present fields of consensus_info
// against rippled's Consensus::getJson (Consensus.h:962) on a freshly
// constructed engine.
func TestEngine_GetJSON_Defaults(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())

	j := engine.GetJSON(true)

	if _, ok := j["proposing"].(bool); !ok {
		t.Fatalf("proposing should be a bool, got %T", j["proposing"])
	}
	if got := j["proposers"].(int); got != 0 {
		t.Fatalf("proposers = %d, want 0", got)
	}
	// Default mode is not wrongLedger, so synched must be true.
	if synched, _ := j["synched"].(bool); !synched {
		t.Fatal("synched should be true")
	}
	// A freshly constructed engine is between rounds (PhaseAccepted).
	if got := j["phase"].(string); got != "accepted" {
		t.Fatalf("phase = %q, want accepted", got)
	}
	// mockAdaptor.validator defaults to true.
	if v, _ := j["validating"].(bool); !v {
		t.Fatal("validating should be true")
	}

	// full=true fields.
	if got := j["converge_percent"].(int); got != 0 {
		t.Fatalf("converge_percent = %d, want 0 outside establish phase", got)
	}
	for _, k := range []string{"close_resolution", "have_time_consensus", "previous_proposers", "previous_mseconds"} {
		if _, ok := j[k]; !ok {
			t.Fatalf("full view missing %q", k)
		}
	}
}

// TestEngine_GetJSON_NonFull verifies the compact view omits the full-only
// fields, matching rippled which only emits them when full is set.
func TestEngine_GetJSON_NonFull(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())

	j := engine.GetJSON(false)

	for _, k := range []string{"converge_percent", "close_resolution", "peer_positions", "previous_proposers"} {
		if _, ok := j[k]; ok {
			t.Fatalf("compact view should omit %q", k)
		}
	}
	// Always-present fields remain.
	if _, ok := j["phase"]; !ok {
		t.Fatal("phase must always be present")
	}
}

// TestEngine_GetJSON_LedgerSeq pins ledger_seq to prevLedger.seq()+1 while
// synched, mirroring rippled Consensus::getJson (Consensus.h:977-978).
func TestEngine_GetJSON_LedgerSeq(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{1}, seq: 100}

	j := engine.GetJSON(true)
	seq, ok := j["ledger_seq"].(uint32)
	if !ok {
		t.Fatalf("ledger_seq should be a uint32, got %T", j["ledger_seq"])
	}
	if seq != 101 {
		t.Fatalf("ledger_seq = %d, want 101", seq)
	}
}

// TestEngine_GetJSON_WrongLedger verifies synched is false and ledger_seq is
// omitted when the engine is on the wrong ledger, mirroring rippled
// Consensus::getJson (Consensus.h:974-982).
func TestEngine_GetJSON_WrongLedger(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())
	engine.modeAtomic.Store(int32(consensus.ModeWrongLedger))

	j := engine.GetJSON(true)
	if synched, _ := j["synched"].(bool); synched {
		t.Fatal("synched should be false on wrong ledger")
	}
	if _, ok := j["ledger_seq"]; ok {
		t.Fatal("ledger_seq should be omitted on wrong ledger")
	}
}

// TestEngine_GetJSON_CloseTimeIsString pins close_time to a JSON string,
// mirroring rippled ConsensusProposal::getJson which emits
// to_string(closeTime().time_since_epoch().count()) (ConsensusProposal.h:228).
func TestEngine_GetJSON_CloseTimeIsString(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())
	engine.state = &consensus.RoundState{
		OurPosition: &consensus.Proposal{
			PreviousLedger: consensus.LedgerID{2},
			TxSet:          consensus.TxSetID{3},
			Position:       0,
			CloseTime:      time.Now(),
		},
	}

	j := engine.GetJSON(true)
	op, ok := j["our_position"].(map[string]any)
	if !ok {
		t.Fatalf("our_position should be a map, got %T", j["our_position"])
	}
	if _, ok := op["close_time"].(string); !ok {
		t.Fatalf("close_time should be a string, got %T", op["close_time"])
	}
	// A non-bow-out position carries transaction_hash and propose_seq.
	if _, ok := op["transaction_hash"]; !ok {
		t.Fatal("our_position missing transaction_hash")
	}
	if _, ok := op["propose_seq"]; !ok {
		t.Fatal("our_position missing propose_seq")
	}
}

// TestEngine_GetJSON_RetainedRoundState verifies current_ms and converge_percent
// are reported from the retained round result outside the establish phase,
// mirroring rippled which gates current_ms on result_ (Consensus.h:994) and
// emits convergePercent_ unconditionally in full mode (Consensus.h:997). A
// freshly accepted round (PhaseAccepted) still has a result, so both must show.
func TestEngine_GetJSON_RetainedRoundState(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())
	// No round result yet: current_ms is omitted, converge_percent is 0.
	j := engine.GetJSON(true)
	if _, ok := j["current_ms"]; ok {
		t.Fatal("current_ms should be omitted with no round result")
	}

	// Simulate a completed round in the accepted phase (result retained).
	engine.ourTxSet = &mockTxSet{id: consensus.TxSetID{9}}
	engine.currentRoundTime = 2 * time.Second
	engine.lastConvergePercent = 100

	j = engine.GetJSON(true)
	if got, ok := j["current_ms"].(int64); !ok || got != 2000 {
		t.Fatalf("current_ms = %v (%T), want int64 2000", j["current_ms"], j["current_ms"])
	}
	if got := j["converge_percent"].(int); got != 100 {
		t.Fatalf("converge_percent = %d, want 100 retained outside establish", got)
	}
}

// TestEngine_GetJSON_ObserverPosition verifies a non-proposing node that has a
// round result still reports our_position, mirroring rippled which emits
// result_->position for every node with a result (Consensus.h:989-990) even
// when it never broadcast a Proposal.
func TestEngine_GetJSON_ObserverPosition(t *testing.T) {
	engine := NewEngine(newMockAdaptor(), DefaultConfig())
	engine.prevLedger = &mockLedger{id: consensus.LedgerID{5}, seq: 200}
	txSet := &mockTxSet{id: consensus.TxSetID{7}}
	engine.ourTxSet = txSet
	engine.state = &consensus.RoundState{
		CloseTimes: consensus.CloseTimes{Self: time.Now()},
	}

	j := engine.GetJSON(true)
	op, ok := j["our_position"].(map[string]any)
	if !ok {
		t.Fatalf("our_position should be present for an observer with a result, got %T", j["our_position"])
	}
	id := txSet.ID()
	wantHash := fmt.Sprintf("%X", id[:])
	if got := op["transaction_hash"].(string); got != wantHash {
		t.Fatalf("transaction_hash = %q, want %q", got, wantHash)
	}
	if got := op["propose_seq"].(uint32); got != 0 {
		t.Fatalf("propose_seq = %d, want 0 for an observer", got)
	}
}

// TestEngine_GetJSON_ValidatingRequiresFull verifies validating reflects the
// dynamic state (configured validator AND OpModeFull), mirroring rippled's
// adaptor_.validating() (RCLConsensus.cpp:937) rather than static config.
func TestEngine_GetJSON_ValidatingRequiresFull(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.opMode = consensus.OpModeConnected // configured validator, not synced
	engine := NewEngine(adaptor, DefaultConfig())

	if v, _ := engine.GetJSON(true)["validating"].(bool); v {
		t.Fatal("validating should be false when not OpModeFull")
	}

	adaptor.opMode = consensus.OpModeFull
	adaptor.validator = false // synced, but not a configured validator
	if v, _ := engine.GetJSON(true)["validating"].(bool); v {
		t.Fatal("validating should be false when not a configured validator")
	}
}
