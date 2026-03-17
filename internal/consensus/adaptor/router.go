package adaptor

import (
	"context"
	"log/slog"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// Router reads inbound messages from the P2P overlay and dispatches
// them to the consensus engine and adaptor.
type Router struct {
	engine  consensus.Engine
	adaptor *Adaptor
	inbox   <-chan *peermanagement.InboundMessage
	logger  *slog.Logger
}

// NewRouter creates a new Router.
func NewRouter(engine consensus.Engine, adaptor *Adaptor, inbox <-chan *peermanagement.InboundMessage) *Router {
	return &Router{
		engine:  engine,
		adaptor: adaptor,
		inbox:   inbox,
		logger:  slog.Default().With("component", "consensus-router"),
	}
}

// Run reads messages from the overlay and dispatches them.
// It blocks until the context is cancelled.
func (r *Router) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-r.inbox:
			if !ok {
				return
			}
			r.handleMessage(msg)
		}
	}
}

func (r *Router) handleMessage(msg *peermanagement.InboundMessage) {
	msgType := message.MessageType(msg.Type)

	switch msgType {
	case message.TypeProposeLedger:
		r.handleProposal(msg)
	case message.TypeValidation:
		r.handleValidation(msg)
	case message.TypeTransaction:
		r.handleTransaction(msg)
	case message.TypeHaveSet:
		r.handleHaveSet(msg)
	case message.TypeStatusChange:
		r.handleStatusChange(msg)
	default:
		// Not a consensus message — ignore
	}
}

func (r *Router) handleProposal(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeProposeLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode proposal", "error", err, "peer", msg.PeerID)
		return
	}
	proposeSet, ok := decoded.(*message.ProposeSet)
	if !ok {
		return
	}

	proposal := ProposalFromMessage(proposeSet)
	if err := r.engine.OnProposal(proposal); err != nil {
		r.logger.Debug("engine rejected proposal", "error", err, "peer", msg.PeerID)
	}
}

func (r *Router) handleValidation(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeValidation, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode validation", "error", err, "peer", msg.PeerID)
		return
	}
	val, ok := decoded.(*message.Validation)
	if !ok {
		return
	}

	validation := ValidationFromMessage(val)
	if err := r.engine.OnValidation(validation); err != nil {
		r.logger.Debug("engine rejected validation", "error", err, "peer", msg.PeerID)
	}
}

func (r *Router) handleTransaction(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeTransaction, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode transaction", "error", err, "peer", msg.PeerID)
		return
	}
	txMsg, ok := decoded.(*message.Transaction)
	if !ok {
		return
	}

	blob := TransactionFromMessage(txMsg)
	if len(blob) == 0 {
		return
	}

	// Add to the adaptor's pending transaction pool
	r.adaptor.AddPendingTx(blob)
}

func (r *Router) handleHaveSet(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeHaveSet, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode have_set", "error", err, "peer", msg.PeerID)
		return
	}
	hts, ok := decoded.(*message.HaveTransactionSet)
	if !ok {
		return
	}

	txSetID, status := HaveSetFromMessage(hts)

	switch status {
	case message.TxSetStatusHave:
		// Peer has a tx set we might need — if the engine is waiting for it,
		// we could request the full set. For now, just log.
		r.logger.Debug("peer has txset", "txset", txSetID, "peer", msg.PeerID)
	case message.TxSetStatusNeed:
		// Peer needs a tx set we might have — check cache and respond.
		if ts, ok := r.adaptor.txSetCache.Get(txSetID); ok {
			// We have it — notify the engine with the tx set data
			if err := r.engine.OnTxSet(ts.ID(), ts.Txs()); err != nil {
				r.logger.Debug("engine rejected txset", "error", err)
			}
		}
	}
}

func (r *Router) handleStatusChange(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeStatusChange, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode status_change", "error", err, "peer", msg.PeerID)
		return
	}
	sc, ok := decoded.(*message.StatusChange)
	if !ok {
		return
	}

	// Use peer status changes to inform operating mode transitions.
	// For example, if a peer reports validating status, we know we're
	// connected to an active network.
	r.logger.Debug("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
	)
}
