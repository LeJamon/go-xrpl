package adaptor

import (
	"runtime/debug"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// recoverFrame is the router's panic boundary for peer-frame dispatch. The
// router parses peer-supplied frames (proposals, validations, tx sets, ledger
// data, get_ledger serves) on the Run goroutine and its worker pools; a panic
// reachable from a crafted frame — a manual byte parser, SHAMap assembly,
// fetch-pack handling — would otherwise take down the goroutine and the whole
// process, turning one malformed frame into a network-wide crash vector.
// Deferred at each dispatch entry point, it recovers the panic, logs it with
// the sender and message type, charges the peer for bad data, and drops the
// frame. Mirrors Overlay.handleInbound's recover and rippled's per-job
// try/catch on the JobQueue.
func (r *Router) recoverFrame(msg *peermanagement.InboundMessage, stage string) {
	r.finishFrameRecovery(msg, stage, recover(), nil)
}

func (r *Router) recoverTransactionFrame(
	msg *peermanagement.InboundMessage,
	jobPhase *bool,
	jobHash *[32]byte,
) {
	charge := func() bool {
		if jobPhase != nil && *jobPhase {
			if r.txSeen != nil && jobHash != nil {
				r.txSeen.markBad(*jobHash)
			}
			return msg.ChargePeer(resource.FeeInvalidData(), "panic-transaction")
		}
		return msg.SelectPeerCharge(resource.FeeInvalidData(), "panic-transaction")
	}
	r.finishFrameRecovery(msg, "transaction", recover(), charge)
}

func (r *Router) finishFrameRecovery(
	msg *peermanagement.InboundMessage,
	stage string,
	rec any,
	charge func() bool,
) {
	if rec == nil {
		return
	}
	r.logger.Error("panic recovered in router frame dispatch",
		"t", "consensus", "stage", stage,
		"peer", msg.PeerID, "msgType", msg.Type,
		"panic", rec, "stack", string(debug.Stack()))
	if charge != nil && charge() {
		return
	}
	r.gossip.IncPeerBadData(uint64(msg.PeerID), "panic-"+stage)
}
