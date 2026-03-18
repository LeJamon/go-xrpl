package adaptor

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
)

// peerLedgerState tracks the latest ledger info reported by a peer.
type peerLedgerState struct {
	LedgerSeq  uint32
	LedgerHash [32]byte
}

// Router reads inbound messages from the P2P overlay and dispatches
// them to the consensus engine and adaptor.
type Router struct {
	engine  consensus.Engine
	adaptor *Adaptor
	inbox   <-chan *peermanagement.InboundMessage
	logger  *slog.Logger

	// Peer ledger tracking for catch-up detection
	peersMu    sync.RWMutex
	peerStates map[peermanagement.PeerID]*peerLedgerState
}

// NewRouter creates a new Router.
func NewRouter(engine consensus.Engine, adaptor *Adaptor, inbox <-chan *peermanagement.InboundMessage) *Router {
	return &Router{
		engine:     engine,
		adaptor:    adaptor,
		inbox:      inbox,
		logger:     slog.Default().With("component", "consensus-router"),
		peerStates: make(map[peermanagement.PeerID]*peerLedgerState),
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
	case message.TypeGetLedger:
		r.handleGetLedger(msg)
	case message.TypeLedgerData:
		r.handleLedgerData(msg)
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

func (r *Router) handleGetLedger(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeGetLedger, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode get_ledger", "error", err, "peer", msg.PeerID)
		return
	}
	req, ok := decoded.(*message.GetLedger)
	if !ok {
		return
	}

	r.logger.Debug("peer requests ledger",
		"peer", msg.PeerID,
		"itype", req.InfoType,
		"seq", req.LedgerSeq,
		"hash_len", len(req.LedgerHash),
	)

	// Only handle base (header) requests for now
	if req.InfoType != message.LedgerInfoBase {
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Find the requested ledger
	var l *ledger.Ledger
	if len(req.LedgerHash) == 32 {
		var hash [32]byte
		copy(hash[:], req.LedgerHash)
		l, err = svc.GetLedgerByHash(hash)
	} else if req.LedgerSeq > 0 {
		l, err = svc.GetLedgerBySequence(req.LedgerSeq)
	} else {
		l = svc.GetClosedLedger()
	}
	if err != nil || l == nil {
		return
	}

	hash := l.Hash()
	resp := &message.LedgerData{
		LedgerHash: hash[:],
		LedgerSeq:  l.Sequence(),
		InfoType:   message.LedgerInfoBase,
		Nodes: []message.LedgerNode{
			{NodeData: l.SerializeHeader()},
		},
		RequestCookie: uint32(req.RequestCookie),
	}

	frame, err := encodeFrame(message.TypeLedgerData, resp)
	if err != nil {
		r.logger.Warn("failed to encode ledger_data response", "error", err)
		return
	}

	if err := r.adaptor.SendToPeer(uint64(msg.PeerID), frame); err != nil {
		r.logger.Debug("failed to send ledger_data to peer", "error", err, "peer", msg.PeerID)
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

	r.logger.Info("peer status change",
		"peer", msg.PeerID,
		"status", sc.NewStatus,
		"event", sc.NewEvent,
		"ledger_seq", sc.LedgerSeq,
		"needs_sync", r.adaptor.NeedsInitialSync(),
	)

	// Track peer's reported ledger state
	if sc.LedgerSeq > 0 {
		var peerHash [32]byte
		if len(sc.LedgerHash) == 32 {
			copy(peerHash[:], sc.LedgerHash)
		}
		var parentHash [32]byte
		if len(sc.LedgerHashPrevious) == 32 {
			copy(parentHash[:], sc.LedgerHashPrevious)
		}

		r.peersMu.Lock()
		r.peerStates[msg.PeerID] = &peerLedgerState{
			LedgerSeq:  sc.LedgerSeq,
			LedgerHash: peerHash,
		}
		r.peersMu.Unlock()

		// During initial sync, adopt the peer's ledger from StatusChange
		if r.adaptor.NeedsInitialSync() && sc.LedgerSeq > 1 {
			r.tryAdoptFromStatusChange(sc.LedgerSeq, peerHash, parentHash)
		}

		// TODO: To properly track rippled's chain, goXRPL needs to participate
		// in consensus (match close times from proposals) rather than running
		// solo consensus rounds. For now, the initial adoption gets us past
		// the seq 2 barrier.

		// Check if we're behind and need to catch up
		r.checkBehind(sc.LedgerSeq, peerHash)
	}
}

// tryAdoptFromStatusChange builds a ledger header from StatusChange data and adopts it.
// This is used during initial sync when we can't fetch the full ledger from peers
// (rippled may not serve old ledgers). We construct a minimal header from the
// peer's reported state and use our genesis state map.
func (r *Router) tryAdoptFromStatusChange(seq uint32, hash, parentHash [32]byte) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		r.logger.Warn("tryAdopt: no ledger service")
		return
	}

	closedLedger := svc.GetClosedLedger()
	if closedLedger == nil {
		r.logger.Warn("tryAdopt: no closed ledger")
		return
	}

	r.logger.Info("attempting ledger adoption",
		"peer_seq", seq,
		"our_seq", closedLedger.Sequence(),
	)

	// Build a synthetic header from the StatusChange data
	h := &header.LedgerHeader{
		LedgerIndex: seq,
		Hash:        hash,
		ParentHash:  parentHash,
		// Reuse genesis state hash — valid for empty ledger sequences
		AccountHash: closedLedger.Header().AccountHash,
		// TxHash stays zero — no transactions
		Drops:               100_000_000_000_000_000, // total XRP supply
		CloseTimeResolution: 10,
	}

	if err := svc.AdoptLedgerHeader(h); err != nil {
		r.logger.Info("failed to adopt from status change", "error", err, "seq", seq)
		return
	}

	// Transition to Full mode
	r.adaptor.SetOperatingMode(consensus.OpModeFull)

	r.logger.Info("adopted ledger from peer status change",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
}

// checkBehind compares the peer's ledger seq to ours and requests missing ledgers.
func (r *Router) checkBehind(peerSeq uint32, peerHash [32]byte) {
	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	ourSeq := svc.GetClosedLedgerIndex()
	if peerSeq <= ourSeq+1 {
		return // not behind, or only 1 ahead (normal during consensus)
	}

	r.logger.Info("behind network",
		"our_seq", ourSeq,
		"peer_seq", peerSeq,
		"gap", peerSeq-ourSeq,
	)

	// Request the peer's closed ledger by hash and sequence
	if err := r.adaptor.RequestLedgerByHashAndSeq(peerHash, peerSeq); err != nil {
		r.logger.Debug("failed to request ledger", "seq", peerSeq, "error", err)
	}
}

func (r *Router) handleLedgerData(msg *peermanagement.InboundMessage) {
	decoded, err := message.Decode(message.TypeLedgerData, msg.Payload)
	if err != nil {
		r.logger.Warn("failed to decode ledger_data", "error", err, "peer", msg.PeerID)
		return
	}
	ld, ok := decoded.(*message.LedgerData)
	if !ok {
		return
	}

	r.logger.Debug("received ledger data",
		"peer", msg.PeerID,
		"seq", ld.LedgerSeq,
		"nodes", len(ld.Nodes),
		"itype", ld.InfoType,
	)

	// During initial sync, try to adopt the ledger header from peers
	if ld.InfoType == message.LedgerInfoBase && len(ld.Nodes) > 0 && r.adaptor.NeedsInitialSync() {
		headerData := ld.Nodes[0].NodeData
		if err := r.adaptor.AdoptLedgerFromHeader(headerData); err != nil {
			r.logger.Debug("failed to adopt ledger header", "error", err, "peer", msg.PeerID)
		} else {
			r.logger.Info("adopted ledger from peer",
				"seq", ld.LedgerSeq,
				"peer", msg.PeerID,
			)
			return
		}
	}

	// Pass the ledger data to the consensus engine
	if len(ld.LedgerHash) == 32 {
		var ledgerID consensus.LedgerID
		copy(ledgerID[:], ld.LedgerHash)

		var payload []byte
		for _, node := range ld.Nodes {
			payload = append(payload, node.NodeData...)
		}

		if err := r.engine.OnLedger(ledgerID, payload); err != nil {
			r.logger.Debug("engine rejected ledger data", "error", err, "peer", msg.PeerID)
		}
	}
}
