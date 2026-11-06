package service

import "time"

// ReplayAdmissionMapStats describes the detached verification work for one
// SHAMap. Counters cover the preparation walk only and are safe to expose to
// callers after the opaque admission token has been returned.
type ReplayAdmissionMapStats struct {
	Elapsed              time.Duration
	AdmissionWait        time.Duration
	TraversalElapsed     time.Duration
	NodesLoaded          uint64
	DurableReads         uint64
	EqualSubtreesSkipped uint64
	UsedVerifiedBase     bool
	Workers              int
	WorkspaceBytes       uint64
	// Coalesced reports that the caller joined an existing complete walk and
	// did not perform tree work itself.
	Coalesced bool
}

// ReplayAdmissionStats reports the bounded work performed before publication.
// ProofCacheHit identifies a generation-bound completeness certificate reused
// without another full state walk; ParentProofUsed identifies a sparse child
// walk that compared against the prepared parent base.
type ReplayAdmissionStats struct {
	Elapsed           time.Duration
	SnapshotElapsed   time.Duration
	State             ReplayAdmissionMapStats
	Transactions      ReplayAdmissionMapStats
	ProofCacheChecked bool
	ProofCacheHit     bool
	ProofCacheMiss    bool
	ParentProofUsed   bool
	GenerationPinned  bool
	GenerationChanged bool
}
