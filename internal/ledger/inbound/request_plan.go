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
	plan := l.missingRequestPlan(requests, complete, err)
	if errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget) {
		return plan, nil
	}
	return plan, err
}

// PlanMissingAddedRequestsContext is the typed counterpart to the blind
// request path used when new peers join an active acquisition.
func (l *Ledger) PlanMissingAddedRequestsContext(ctx context.Context, peerIDs []uint64) (MissingRequestPlan, error) {
	requests, complete, err := l.collectMissingPeerRequestsContext(ctx, peerIDs, reqNodes, true, true)
	plan := l.missingRequestPlan(requests, complete, err)
	if errors.Is(err, shamap.ErrAcquisitionMemory) || errors.Is(err, shamap.ErrTraversalBudget) {
		return plan, nil
	}
	return plan, err
}

func (l *Ledger) missingRequestPlan(requests []MissingRequest, complete bool, err error) MissingRequestPlan {
	plan := MissingRequestPlan{Requests: requests, Outcome: MissingRequestContinue}
	if complete {
		plan.Outcome = MissingRequestComplete
		return plan
	}
	if errors.Is(err, shamap.ErrAcquisitionMemory) {
		plan.Outcome = MissingRequestWaitPersistence
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
	if l.lastScanStatus == shamap.MissingNodesMore && len(requests) == 0 && l.requestAvailableLocked() > 0 {
		plan.Outcome = MissingRequestContinue
		return plan
	}
	if l.lastScanStatus == shamap.MissingNodesWaiting || len(l.requestPeers) >= requestPeerLimit || len(l.requestLeases) > 0 || len(requests) == 0 {
		plan.Outcome = MissingRequestWaitReply
	}
	return plan
}
