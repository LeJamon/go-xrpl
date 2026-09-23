package adaptor

import (
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

const txSetLearnQueueDepth = 256

type txSetLearnJob struct {
	peer uint64
	id   [32]byte
	blob []byte
}

// Learning/relaying a transaction is best effort; delivering the acquired
// SHAMap to consensus is not. A full learning queue must never backpressure
// acquisition dispatch or execute a slow submission inline. The full set
// retains its transaction blobs independently of these detached jobs.
func (r *Router) submitTxSetLearnJob(job txSetLearnJob) {
	r.lifecycleMu.RLock()
	if r.lifecycleState == routerLifecycleInitial {
		r.lifecycleMu.RUnlock()
		// Match direct-dispatch tests' existing synchronous contract.
		r.handleTxSetLearnJob(job)
		return
	}
	defer r.lifecycleMu.RUnlock()
	if r.lifecycleState != routerLifecycleRunning || r.txSetLearnJobs == nil {
		r.droppedTxJobs.Add(1)
		return
	}
	select {
	case r.txSetLearnJobs <- job:
	default:
		r.droppedTxJobs.Add(1)
		r.logger.Debug("tx-set learning dropped: worker pool saturated",
			"t", "consensus", "event", "txset-learn-shed", "peer", job.peer)
	}
}

func (r *Router) handleTxSetLearnJob(job txSetLearnJob) {
	defer r.recoverFrame(&peermanagement.InboundMessage{
		PeerID: peermanagement.PeerID(job.peer), Type: message.TypeLedgerData,
	}, "txset-learning")
	exists, err := r.adaptor.HasTx(consensus.TxID(job.id))
	if err != nil || exists {
		return
	}
	// Preserve the acquired-leaf admission path: ordinary gossip suppression
	// must not swallow a novel acquired transaction merely seen from a peer.
	if outcome, err := r.adaptor.SubmitPendingTx(job.blob, false); err == nil && outcome.Class == openledger.ResultSuccess {
		r.relayTransaction(
			r.transactionRelaySkip(job.id, peermanagement.PeerID(job.peer)),
			job.blob,
			outcome.Queued,
		)
	}
}
