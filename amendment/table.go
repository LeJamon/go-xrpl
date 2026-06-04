// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"maps"
	"sync"
)

// majorityTimeSeconds is the duration an amendment must hold majority before it
// is enabled (14 days). Mirrors rippled's DEFAULT_AMENDMENT_MAJORITY_TIME used
// to project firstUnsupportedExpected.
const majorityTimeSeconds uint32 = 14 * 24 * 60 * 60

// AmendmentTable tracks which amendments are enabled and manages voting.
// This is the central data structure for amendment management in the node.
type AmendmentTable struct {
	mu sync.RWMutex

	// enabled tracks which amendments are currently enabled in the ledger
	enabled map[[32]byte]bool

	// vetoed tracks amendments that are explicitly vetoed by the operator
	vetoed map[[32]byte]bool

	// upVoted tracks amendments explicitly voted for by the operator
	upVoted map[[32]byte]bool

	// unsupportedEnabled is set once an amendment this build does not support
	// becomes enabled. Cached counterpart of HasUnsupportedEnabled.
	// Mirrors rippled's AmendmentTableImpl::unsupportedEnabled_.
	unsupportedEnabled bool

	// blocked is sticky: once an unsupported amendment activates the node can no
	// longer validate new ledgers. Mirrors NetworkOPs::amendmentBlocked_.
	blocked bool

	// lastUpdateSeq is the sequence of the last validated ledger folded into the
	// table by DoValidatedLedger. Mirrors AmendmentTableImpl::lastUpdateSeq_.
	lastUpdateSeq uint32

	// firstUnsupportedExpected, when set, is the projected close time (XRPL epoch
	// seconds) at which the earliest unsupported amendment holding majority would
	// activate. nil when no unsupported amendment currently holds majority.
	// Mirrors AmendmentTableImpl::firstUnsupportedExpected_.
	firstUnsupportedExpected *uint32

	// lastVote caches the tallies from the most recent voting round for RPC
	// introspection (the `feature` command). nil until the first round.
	// Mirrors AmendmentTableImpl::lastVote_.
	lastVote *LastVote
}

// LastVote captures the per-amendment tallies from the most recent amendment
// voting round, for admin `feature` RPC introspection. Mirrors rippled's
// AmendmentSet (votes_, trustedValidations_, threshold_).
type LastVote struct {
	// TrustedValidations is the number of trusted validations counted.
	TrustedValidations int
	// Threshold is the yes-vote count an amendment needed to pass this round.
	Threshold int
	// Votes is the yes-vote count per amendment hash.
	Votes map[[32]byte]int
}

// NewAmendmentTable creates a new AmendmentTable with no enabled amendments.
func NewAmendmentTable() *AmendmentTable {
	return &AmendmentTable{
		enabled: make(map[[32]byte]bool),
		vetoed:  make(map[[32]byte]bool),
		upVoted: make(map[[32]byte]bool),
	}
}

// NewAmendmentTableWithEnabled creates a new AmendmentTable with the specified
// amendments already enabled. This is useful for loading from ledger state.
func NewAmendmentTableWithEnabled(enabledIDs [][32]byte) *AmendmentTable {
	t := NewAmendmentTable()
	for _, id := range enabledIDs {
		t.enabled[id] = true
		if !isSupported(id) {
			t.unsupportedEnabled = true
		}
	}
	return t
}

// isSupported reports whether the given amendment is recognised and supported
// by this build.
func isSupported(featureID [32]byte) bool {
	f := GetFeature(featureID)
	return f != nil && f.Supported == SupportedYes
}

// IsEnabled returns true if the amendment with the given ID is enabled.
func (t *AmendmentTable) IsEnabled(featureID [32]byte) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.enabled[featureID]
}

// IsSupported returns true if the amendment with the given ID is supported
// by this node's code.
func (t *AmendmentTable) IsSupported(featureID [32]byte) bool {
	f := GetFeature(featureID)
	if f == nil {
		return false
	}
	return f.Supported == SupportedYes
}

// Enable marks an amendment as enabled. This should be called when an
// amendment passes voting and becomes active in the ledger.
func (t *AmendmentTable) Enable(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.enabled[featureID] = true
	if !isSupported(featureID) {
		t.unsupportedEnabled = true
	}
}

// Disable marks an amendment as not enabled. This is primarily for testing.
func (t *AmendmentTable) Disable(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.enabled, featureID)
}

// EnableMultiple enables multiple amendments at once.
func (t *AmendmentTable) EnableMultiple(featureIDs [][32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, id := range featureIDs {
		t.enabled[id] = true
		if !isSupported(id) {
			t.unsupportedEnabled = true
		}
	}
}

// GetEnabled returns a slice of all enabled amendment IDs.
func (t *AmendmentTable) GetEnabled() [][32]byte {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([][32]byte, 0, len(t.enabled))
	for id := range t.enabled {
		result = append(result, id)
	}
	return result
}

// GetDesired returns the list of amendment IDs that this node wants to vote for.
// This includes:
// - Amendments with VoteDefaultYes that are not vetoed
// - Amendments explicitly upvoted by the operator
// It excludes:
// - Amendments that are already enabled
// - Amendments that are vetoed
// - Unsupported amendments
func (t *AmendmentTable) GetDesired() [][32]byte {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([][32]byte, 0)

	for _, f := range AllFeatures() {
		if t.enabled[f.ID] {
			continue
		}

		if f.Supported != SupportedYes {
			continue
		}

		if t.vetoed[f.ID] {
			continue
		}

		if f.Vote == VoteObsolete {
			continue
		}

		// Include if default yes or explicitly upvoted
		if f.Vote == VoteDefaultYes || t.upVoted[f.ID] {
			result = append(result, f.ID)
		}
	}

	return result
}

// Veto marks an amendment as vetoed, preventing this node from voting for it.
func (t *AmendmentTable) Veto(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.vetoed[featureID] = true
	delete(t.upVoted, featureID)
}

// Unveto removes the veto on an amendment.
func (t *AmendmentTable) Unveto(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.vetoed, featureID)
}

// UpVote explicitly votes for an amendment.
func (t *AmendmentTable) UpVote(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.upVoted[featureID] = true
	delete(t.vetoed, featureID)
}

// DownVote removes explicit vote for an amendment (returns to default behavior).
func (t *AmendmentTable) DownVote(featureID [32]byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.upVoted, featureID)
}

// IsVetoed returns true if the amendment is vetoed.
func (t *AmendmentTable) IsVetoed(featureID [32]byte) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.vetoed[featureID]
}

// IsUpVoted returns true if the amendment is explicitly upvoted.
func (t *AmendmentTable) IsUpVoted(featureID [32]byte) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.upVoted[featureID]
}

// HasUnsupportedEnabled returns true if any unsupported amendment is enabled.
// This indicates the node is running old software and may not be able to
// properly validate new ledgers.
func (t *AmendmentTable) HasUnsupportedEnabled() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()

	for id := range t.enabled {
		f := GetFeature(id)
		if f == nil || f.Supported != SupportedYes {
			return true
		}
	}
	return false
}

// GetUnsupportedEnabled returns a slice of enabled amendment IDs that are
// not supported by this node.
func (t *AmendmentTable) GetUnsupportedEnabled() [][32]byte {
	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make([][32]byte, 0)
	for id := range t.enabled {
		f := GetFeature(id)
		if f == nil || f.Supported != SupportedYes {
			result = append(result, id)
		}
	}
	return result
}

// IsBlocked reports whether the node is amendment-blocked: an amendment this
// build does not support has activated, so the node can no longer validate new
// ledgers. Sticky once set. Mirrors NetworkOPs::isAmendmentBlocked.
func (t *AmendmentTable) IsBlocked() bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.blocked
}

// SetBlocked marks the node amendment-blocked. Mirrors
// NetworkOPs::setAmendmentBlocked.
func (t *AmendmentTable) SetBlocked() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.blocked = true
}

// FirstUnsupportedExpected returns the projected activation time (XRPL epoch
// seconds) of the earliest unsupported amendment currently holding majority,
// or (0, false) when none. Mirrors AmendmentTableImpl::firstUnsupportedExpected.
func (t *AmendmentTable) FirstUnsupportedExpected() (uint32, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.firstUnsupportedExpected == nil {
		return 0, false
	}
	return *t.firstUnsupportedExpected, true
}

// SetLastVote stores the tallies from the most recent voting round. The Votes
// map is copied defensively. Mirrors stashing AmendmentSet into lastVote_.
func (t *AmendmentTable) SetLastVote(v *LastVote) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if v == nil {
		t.lastVote = nil
		return
	}
	cp := &LastVote{
		TrustedValidations: v.TrustedValidations,
		Threshold:          v.Threshold,
		Votes:              make(map[[32]byte]int, len(v.Votes)),
	}
	maps.Copy(cp.Votes, v.Votes)
	t.lastVote = cp
}

// LastVote returns the most recent voting-round tallies, or nil if no round has
// been recorded. The returned value is a defensive copy.
func (t *AmendmentTable) LastVote() *LastVote {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.lastVote == nil {
		return nil
	}
	cp := &LastVote{
		TrustedValidations: t.lastVote.TrustedValidations,
		Threshold:          t.lastVote.Threshold,
		Votes:              make(map[[32]byte]int, len(t.lastVote.Votes)),
	}
	maps.Copy(cp.Votes, t.lastVote.Votes)
	return cp
}

// NeedValidatedLedger reports whether DoValidatedLedger should run for the
// ledger at seq. Amendment state can only change at flag ledgers (every 256),
// so it returns true only when seq and the last folded-in ledger fall in
// different 256-ledger windows. Mirrors AmendmentTableImpl::needValidatedLedger.
//
// The (seq-1) underflow at the initial lastUpdateSeq==0 is intentional and
// matches rippled bit-for-bit: (0-1)/256 wraps to a sentinel window so the first
// validated ledger always triggers a sync. Do not "fix" it.
func (t *AmendmentTable) NeedValidatedLedger(seq uint32) bool {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return ((seq - 1) / 256) != ((t.lastUpdateSeq - 1) / 256)
}

// DoValidatedLedger re-syncs the in-memory table from a validated flag ledger:
// it enables every amendment in `enabled` and recomputes
// firstUnsupportedExpected from `majorities` (amendment hash → majority close
// time in XRPL epoch seconds). Blocking is engaged once an unsupported
// amendment is enabled. Mirrors AmendmentTableImpl::doValidatedLedger.
func (t *AmendmentTable) DoValidatedLedger(seq uint32, enabled map[[32]byte]bool, majorities map[[32]byte]uint32) {
	// enable() locks internally; run it before taking the lock, as rippled does.
	for id := range enabled {
		t.Enable(id)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	t.lastUpdateSeq = seq

	var earliest uint32
	haveEarliest := false
	for hash, closeTime := range majorities {
		if t.enabled[hash] {
			continue
		}
		if isSupported(hash) {
			continue
		}
		if !haveEarliest || closeTime < earliest {
			earliest = closeTime
			haveEarliest = true
		}
	}
	if haveEarliest {
		projected := earliest + majorityTimeSeconds
		t.firstUnsupportedExpected = &projected
	} else {
		t.firstUnsupportedExpected = nil
	}

	if t.unsupportedEnabled {
		t.blocked = true
	}
}

// EnabledCount returns the number of enabled amendments.
func (t *AmendmentTable) EnabledCount() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.enabled)
}

// Clone creates a copy of the AmendmentTable.
func (t *AmendmentTable) Clone() *AmendmentTable {
	t.mu.RLock()
	defer t.mu.RUnlock()

	clone := NewAmendmentTable()
	for id := range t.enabled {
		clone.enabled[id] = true
	}
	for id := range t.vetoed {
		clone.vetoed[id] = true
	}
	for id := range t.upVoted {
		clone.upVoted[id] = true
	}
	clone.unsupportedEnabled = t.unsupportedEnabled
	clone.blocked = t.blocked
	clone.lastUpdateSeq = t.lastUpdateSeq
	if t.firstUnsupportedExpected != nil {
		v := *t.firstUnsupportedExpected
		clone.firstUnsupportedExpected = &v
	}
	if t.lastVote != nil {
		cp := &LastVote{
			TrustedValidations: t.lastVote.TrustedValidations,
			Threshold:          t.lastVote.Threshold,
			Votes:              make(map[[32]byte]int, len(t.lastVote.Votes)),
		}
		maps.Copy(cp.Votes, t.lastVote.Votes)
		clone.lastVote = cp
	}
	return clone
}
