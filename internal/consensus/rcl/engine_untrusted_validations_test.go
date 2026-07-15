package rcl

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

// startedEngine builds and starts an engine over adaptor, registering
// cleanup. Tests drive it via OnValidation directly.
func startedEngine(t *testing.T, adaptor *mockAdaptor) *Engine {
	t.Helper()
	engine := NewEngine(adaptor, DefaultConfig())
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { engine.Stop() })
	return engine
}

func (a *mockAdaptor) relayedValidations() []*consensus.Validation {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]*consensus.Validation(nil), a.validationsRelayed...)
}

// TestEngine_OnValidation_ListedUntrustedStoredAndPromoted pins the #1192
// contract end-to-end: a validation from a listed-but-untrusted signer is
// stored (byNode tip present) without counting toward quorum, and a
// subsequent trust change — delivered through the TrustChangeNotifier hook,
// not an accepted ledger — promotes the already-stored validation into the
// trusted count, mirroring rippled's trustChanged.
func TestEngine_OnValidation_ListedUntrustedStoredAndPromoted(t *testing.T) {
	adaptor := newMockAdaptor()
	nL := consensus.NodeID{0x11}
	adaptor.setListed([]consensus.NodeID{nL})
	engine := startedEngine(t, adaptor)

	now := adaptor.Now()
	ledgerA := consensus.LedgerID{0xA}
	v := &consensus.Validation{LedgerSeq: 200, LedgerID: ledgerA, NodeID: nL, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v, 3); err != nil {
		t.Fatalf("listed-untrusted validation rejected: %v", err)
	}

	// Stored but untrusted: tip tracked, quorum count zero, no relay under
	// the default (mock) trusted-only relay policy.
	if tip := engine.validationTracker.LatestValidation(nL); tip == nil || tip.LedgerID != ledgerA {
		t.Fatalf("listed-untrusted validation must be stored; got %+v", tip)
	}
	if got := engine.validationTracker.TrustedValidationCount(ledgerA); got != 0 {
		t.Errorf("untrusted validation must not count toward quorum: got %d, want 0", got)
	}
	if relayed := adaptor.relayedValidations(); len(relayed) != 0 {
		t.Errorf("untrusted validation must not relay under trusted-only policy: %d relayed", len(relayed))
	}

	// Promote: the validator enters the UNL and the adaptor fires the
	// trust-change hook the engine registered at Start.
	adaptor.setTrusted([]consensus.NodeID{nL})
	adaptor.notifyTrustChanged()

	if got := engine.validationTracker.TrustedValidationCount(ledgerA); got != 1 {
		t.Errorf("stored validation must count after trust promotion: got %d, want 1", got)
	}
}

// TestEngine_OnValidation_UnlistedUntrustedNotStored: without a listing, an
// untrusted signer gets no byNode entry — the unbounded-growth guard stays.
func TestEngine_OnValidation_UnlistedUntrustedNotStored(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.relayUntrusted = true // storage must not piggyback on the relay stance
	nU := consensus.NodeID{0x22}
	engine := startedEngine(t, adaptor)

	now := adaptor.Now()
	v := &consensus.Validation{LedgerSeq: 200, LedgerID: consensus.LedgerID{0xA}, NodeID: nU, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v, 3); err != nil {
		t.Fatalf("unlisted validation errored: %v", err)
	}
	if tip := engine.validationTracker.LatestValidation(nU); tip != nil {
		t.Errorf("unlisted-untrusted validation must not be stored; got %+v", tip)
	}
}

// TestEngine_OnValidation_RelayUntrustedPolicy pins the #1206 contract: with
// the [relay_validations]=all stance an untrusted (even unlisted) verified
// validation is forwarded — rippled's RELAY_UNTRUSTED_VALIDATIONS=1 default —
// while the trusted-only stance keeps the old drop.
func TestEngine_OnValidation_RelayUntrustedPolicy(t *testing.T) {
	for _, tc := range []struct {
		name           string
		relayUntrusted bool
		wantRelayed    int
	}{
		{"all relays untrusted", true, 1},
		{"trusted-only drops untrusted", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adaptor := newMockAdaptor()
			adaptor.relayUntrusted = tc.relayUntrusted
			engine := startedEngine(t, adaptor)

			now := adaptor.Now()
			v := &consensus.Validation{LedgerSeq: 200, LedgerID: consensus.LedgerID{0xA}, NodeID: consensus.NodeID{0x22}, SignTime: now, SeenTime: now, Full: true}
			if err := engine.OnValidation(v, 3); err != nil {
				t.Fatalf("OnValidation: %v", err)
			}
			if got := len(adaptor.relayedValidations()); got != tc.wantRelayed {
				t.Errorf("relayed = %d, want %d", got, tc.wantRelayed)
			}
		})
	}
}

// TestEngine_OnValidation_ListedUntrustedDoubleSign: same-seq double-signs
// from listed-but-untrusted validators are detected against their stored tip
// and surfaced with Trusted=false so the router logs at info, not error.
// Under the relay-all stance the conflicting validation still forwards.
func TestEngine_OnValidation_ListedUntrustedDoubleSign(t *testing.T) {
	adaptor := newMockAdaptor()
	adaptor.relayUntrusted = true
	nL := consensus.NodeID{0x11}
	adaptor.setListed([]consensus.NodeID{nL})
	engine := startedEngine(t, adaptor)

	now := adaptor.Now()
	v1 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xA}, NodeID: nL, SignTime: now, SeenTime: now, Full: true}
	if err := engine.OnValidation(v1, 7); err != nil {
		t.Fatalf("first validation rejected: %v", err)
	}

	v2 := &consensus.Validation{LedgerSeq: 100, LedgerID: consensus.LedgerID{0xB}, NodeID: nL, SignTime: now, SeenTime: now, Full: true}
	err := engine.OnValidation(v2, 7)
	var bv *consensus.ByzantineValidationError
	if !errors.As(err, &bv) {
		t.Fatalf("expected *consensus.ByzantineValidationError, got %v", err)
	}
	if bv.Trusted {
		t.Errorf("Trusted = true for an untrusted double-signer")
	}

	var relayedConflict bool
	for _, v := range adaptor.relayedValidations() {
		if v.LedgerID == (consensus.LedgerID{0xB}) {
			relayedConflict = true
		}
	}
	if !relayedConflict {
		t.Errorf("conflicting untrusted validation must still relay under the all stance")
	}
}
