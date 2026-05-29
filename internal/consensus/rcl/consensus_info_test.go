package rcl

import (
	"testing"

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
