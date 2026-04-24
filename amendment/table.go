// Copyright (c) 2024-2025. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package amendment

import (
	"sync"
)

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
	}
	return t
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
	return clone
}
