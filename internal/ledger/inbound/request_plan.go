package inbound

import (
	"context"
	"errors"

	"github.com/LeJamon/go-xrpl/shamap"
)

// PlanMissingReplyRequestsContext plans a bounded set of reply-driven requests
// and reports whether the owner should continue discovery or wait for an
// external event. The returned requests remain leased until a matching reply,
// expiry, or failed-send release.
func (l *Ledger) PlanMissingReplyRequestsContext(ctx context.Context, peerIDs []uint64) (MissingRequestPlan, error) {
	requests, complete, err := l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodesReply, false, true)
	plan := l.missingRequestPlan(requests, complete, err, peerIDs)
	if errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget) {
		return plan, nil
	}
	return plan, err
}

// PlanMissingAddedRequestsContext is the typed counterpart to the blind
// request path used when new peers join an active acquisition.
func (l *Ledger) PlanMissingAddedRequestsContext(ctx context.Context, peerIDs []uint64) (MissingRequestPlan, error) {
	requests, complete, err := l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodes, true, true)
	plan := l.missingRequestPlan(requests, complete, err, peerIDs)
	if errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget) {
		return plan, nil
	}
	return plan, err
}

func (l *Ledger) missingRequestPlan(requests []MissingRequest, complete bool, err error, peerIDs []uint64) MissingRequestPlan {
	plan := MissingRequestPlan{Requests: requests, Outcome: MissingRequestContinue}
	if complete {
		plan.Outcome = MissingRequestComplete
		return plan
	}
	if errors.Is(err, shamap.ErrAcquisitionMemory) {
		plan.Outcome = MissingRequestWaitMemory
		return plan
	}
	if errors.Is(err, shamap.ErrTraversalBudget) {
		return plan
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state == StateComplete {
		plan.Outcome = MissingRequestComplete
		return plan
	}
	if l.lastScanStatus == shamap.MissingNodesMemory {
		plan.Outcome = MissingRequestWaitMemory
		return plan
	}
	if l.lastScanStatus == shamap.MissingNodesPersistence || l.lastScanStatus.String() == "persistence" {
		plan.Outcome = MissingRequestWaitPersistence
		return plan
	}
	if l.lastScanStatus == shamap.MissingNodesMore {
		freePeer := false
		for _, peerID := range peerIDs {
			if peerID == 0 {
				continue
			}
			if _, busy := l.requestPeers[peerID]; !busy {
				freePeer = true
				break
			}
		}
		if freePeer && len(l.requestPeers) < requestPeerLimit && l.requestAvailableLocked() > 0 {
			// A resumable scanner slice is incomplete even when this call
			// emitted requests. Keep the worker on its ready queue while a
			// distinct peer/window slot can still make progress.
			plan.Outcome = MissingRequestContinue
			return plan
		}
	}
	if l.lastScanStatus == shamap.MissingNodesWaiting || l.lastScanStatus == shamap.MissingNodesMore || len(l.requestPeers) >= requestPeerLimit || len(l.requestLeases) > 0 || len(requests) == 0 {
		plan.Outcome = MissingRequestWaitReply
	}
	return plan
}
