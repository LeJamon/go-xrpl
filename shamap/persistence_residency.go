package shamap

import "sync"

// CleanResidencyRelease is a one-shot proof that a particular backed-map root
// may be detached after the caller's storage durability fence completes. It
// is intentionally separate from acquisition acknowledgement, which also
// updates scanner and full-below state.
type CleanResidencyRelease struct {
	sm       *SHAMap
	access   *familyAccess
	root     *innerNode
	rootHash [32]byte

	mu           sync.Mutex
	acknowledged bool
}

// Family returns the exact Family binding captured by the token. Callers can
// compare it with the service's canonical backend before acknowledging a
// durable completion.
func (token *CleanResidencyRelease) Family() Family {
	if token == nil || token.access == nil {
		return nil
	}
	return token.access.Family
}

// PrepareCleanResidencyRelease captures the current Family binding and root.
// The caller should invoke it before storing a pending root and acknowledge
// the token only after the service has completed its durable-generation and
// header fence. A nil result means the map has no bound Family or root.
func (sm *SHAMap) PrepareCleanResidencyRelease() *CleanResidencyRelease {
	if sm == nil {
		return nil
	}
	sm.tree.mu.RLock()
	defer sm.tree.mu.RUnlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	// Syncing nodes are owned by the acquisition budget and may still be
	// attached or replaced by the sync pipeline. Ordinary residency cleanup
	// must not detach that graph; the acquisition acknowledgement path owns it.
	if sm.tree.state == stateSyncing || !sm.backing.access.available() || sm.tree.root == nil || sm.tree.root.IsEmpty() {
		return nil
	}
	return &CleanResidencyRelease{
		sm:       sm,
		access:   sm.backing.access,
		root:     sm.tree.root,
		rootHash: sm.tree.root.Hash(),
	}
}

// AcknowledgeCleanResidencyRelease validates token identity and detaches the
// captured root's clean descendants. A changed Family, root, or root hash
// invalidates the token and leaves all pointers in place. The token is
// consumed on either outcome so a stale completion cannot be retried later.
func (sm *SHAMap) AcknowledgeCleanResidencyRelease(token *CleanResidencyRelease) bool {
	if sm == nil || token == nil || token.sm != sm {
		return false
	}
	token.mu.Lock()
	if token.acknowledged {
		token.mu.Unlock()
		return false
	}
	token.acknowledged = true
	token.mu.Unlock()

	sm.acquisition.walkMu.Lock()
	defer sm.acquisition.walkMu.Unlock()
	sm.tree.mu.Lock()
	defer sm.tree.mu.Unlock()
	sm.backing.mu.RLock()
	defer sm.backing.mu.RUnlock()
	access := sm.backing.access
	root := sm.tree.root
	valid := sm.tree.state != stateSyncing && access == token.access && root == token.root && access.available() &&
		root != nil && !root.IsDirty() && root.Hash() == token.rootHash
	if valid {
		root.ReleaseChildren()
	}
	return valid
}
