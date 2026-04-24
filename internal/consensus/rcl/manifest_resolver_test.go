package rcl

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
)

// TestConsensus_EphemeralSigningKey_TranslatedForQuorum — acceptance
// test from issue #265. A validation signed by an ephemeral key whose
// manifest maps to a UNL master key must count toward quorum as if it
// came from the master.
func TestConsensus_EphemeralSigningKey_TranslatedForQuorum(t *testing.T) {
	masterKey := consensus.NodeID{0xEE, 0x01}
	ephemeralKey := consensus.NodeID{0xEE, 0x02}

	vt := NewValidationTracker(1, 5*time.Minute)
	vt.SetTrusted([]consensus.NodeID{masterKey})

	// Wire the resolver: ephemeral → master. Anything else
	// returns its input unchanged (so a non-rotated validator still
	// works).
	vt.SetManifestResolver(func(n consensus.NodeID) consensus.NodeID {
		if n == ephemeralKey {
			return masterKey
		}
		return n
	})

	ledger := consensus.LedgerID{0x42}
	var fired bool
	vt.SetFullyValidatedCallback(func(id consensus.LedgerID, _ uint32) {
		if id == ledger {
			fired = true
		}
	})

	v := &consensus.Validation{
		LedgerID:  ledger,
		LedgerSeq: 100,
		NodeID:    ephemeralKey, // signed with the ephemeral key
		SignTime:  time.Now(),
		Full:      true,
	}

	if !vt.Add(v) {
		t.Fatal("Add: validation rejected")
	}
	if !fired {
		t.Fatal("fully-validated callback did not fire — ephemeral key was not translated to master for quorum")
	}
	if count := vt.GetTrustedValidationCount(ledger); count != 1 {
		t.Fatalf("GetTrustedValidationCount: got %d want 1", count)
	}
	if !vt.IsFullyValidated(ledger) {
		t.Fatal("IsFullyValidated returned false")
	}
	if got := vt.ProposersValidated(ledger); got != 1 {
		t.Fatalf("ProposersValidated: got %d want 1", got)
	}
}

// TestConsensus_NoResolver_EphemeralNotTrusted — baseline: without a
// manifest resolver, a validation signed by an ephemeral key whose
// master is in the UNL must NOT count toward quorum. This guards
// against a regression where the resolver is accidentally reversed or
// applied in the wrong direction.
func TestConsensus_NoResolver_EphemeralNotTrusted(t *testing.T) {
	masterKey := consensus.NodeID{0xEE, 0x10}
	ephemeralKey := consensus.NodeID{0xEE, 0x11}

	vt := NewValidationTracker(1, 5*time.Minute)
	vt.SetTrusted([]consensus.NodeID{masterKey})
	// No SetManifestResolver call — default identity.

	v := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x43},
		LedgerSeq: 101,
		NodeID:    ephemeralKey,
		SignTime:  time.Now(),
		Full:      true,
	}
	if !vt.Add(v) {
		t.Fatal("Add should still accept the validation (untrusted ones are tracked)")
	}
	if count := vt.GetTrustedValidationCount(consensus.LedgerID{0x43}); count != 0 {
		t.Fatalf("untrusted validation counted as trusted: got %d", count)
	}
}
