package adaptor

import (
	"context"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

type ledgerServeJob struct {
	ctx    context.Context
	cancel context.CancelFunc
	peerID peermanagement.PeerID
	mu     sync.Mutex
	msg    *peermanagement.InboundMessage
}

// Taking a message transfers its ownership to either the worker or cancellation.
func (j *ledgerServeJob) takeMessage() *peermanagement.InboundMessage {
	j.mu.Lock()
	defer j.mu.Unlock()
	msg := j.msg
	j.msg = nil
	return msg
}

func (j *ledgerServeJob) discard() {
	j.cancel()
	if msg := j.takeMessage(); msg != nil {
		_ = msg.Close()
	}
}

func (r *Router) runLedgerServeJob(job *ledgerServeJob) {
	defer r.finishLedgerServeJob(job)
	msg := job.takeMessage()
	if msg == nil {
		return
	}
	defer func() { _ = msg.Close() }()
	if job.ctx.Err() != nil || (r.peerSessions != nil && !r.peerSessions.IsPeerConnected(job.peerID)) {
		return
	}
	r.handleGetLedger(job.ctx, msg)
}

func (r *Router) finishLedgerServeJob(job *ledgerServeJob) {
	r.lifecycleMu.Lock()
	jobs := r.servePeers[job.peerID]
	delete(jobs, job)
	if len(jobs) == 0 {
		delete(r.servePeers, job.peerID)
	}
	r.lifecycleMu.Unlock()
	job.discard()
}

func (r *Router) cancelLedgerServePeer(peerID peermanagement.PeerID) {
	// Overlay peer IDs identify one connection and are never reused.
	r.lifecycleMu.Lock()
	jobs := r.servePeers[peerID]
	delete(r.servePeers, peerID)
	r.lifecycleMu.Unlock()
	for job := range jobs {
		job.discard()
	}
}

func (r *Router) drainLedgerServeJobs(jobs <-chan *ledgerServeJob) {
	for {
		select {
		case job := <-jobs:
			r.finishLedgerServeJob(job)
		default:
			return
		}
	}
}
