package adaptor

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/shamap"
)

type ledgerServeNetwork interface {
	PeerSupportsNodeDepth(peerID uint64) bool
	SendToPeer(peerID uint64, frame []byte) error
	ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool
	PeerWithLedger(target [32]byte, seq uint32, exclude uint64) (uint64, bool)
	PeerLatency(peerID uint64) (time.Duration, bool)
	IncPeerBadData(peerID uint64, reason string)
}

func (r *Router) invalidFutureLedgerSequence(seq uint32) bool {
	svc := r.adaptor.LedgerService()
	if svc == nil || svc.GetValidatedLedgerAge() > 10*time.Second {
		return false
	}
	validated := svc.GetValidatedLedgerIndex()
	return seq > validated && seq-validated > 10
}

// validateGetLedgerMessage performs the request checks that rippled applies
// before placing TMGetLedger on its ledger-request job queue. Node ID contents
// are intentionally left to the worker so the dispatch path only does the
// bounded protobuf decode and count checks.
func (r *Router) validateGetLedgerMessage(msg *peermanagement.InboundMessage) string {
	decoded, err := message.Decode(message.TypeGetLedger, msg.Payload)
	if err != nil {
		return "get-ledger-decode"
	}
	req, ok := decoded.(*message.GetLedger)
	if !ok {
		return "get-ledger-decode"
	}
	if reason := r.validateGetLedgerRequest(req); reason != "" {
		return reason
	}
	msg.GetLedger = req
	return ""
}

func (r *Router) validateGetLedgerRequest(req *message.GetLedger) string {
	if req == nil {
		return "get-ledger-invalid-request"
	}
	if req.InfoType < message.LedgerInfoBase || req.InfoType > message.LedgerInfoTsCandidate {
		return "get-ledger-invalid-itype"
	}
	if req.InfoType == message.LedgerInfoTsCandidate && !req.HasLedgerHash() {
		return "get-ledger-missing-txset-hash"
	}
	if req.InfoType != message.LedgerInfoTsCandidate &&
		!req.HasLedgerHash() && !req.HasLedgerSeq() &&
		(!req.HasLType() || req.LType != message.LedgerTypeClosed) {
		return "get-ledger-invalid-request"
	}
	if req.HasLType() && (req.LType < message.LedgerTypeAccepted || req.LType > message.LedgerTypeClosed) {
		return "get-ledger-invalid-ltype"
	}
	if req.HasLedgerHash() && len(req.LedgerHash) != 32 {
		return "get-ledger-invalid-hash"
	}
	if req.HasLedgerSeq() && r.invalidFutureLedgerSequence(req.LedgerSeq) {
		return "get-ledger-invalid-sequence"
	}
	if req.InfoType != message.LedgerInfoBase {
		if len(req.NodeIDs) == 0 || len(req.NodeIDs) > txSetHardMaxReplyNodes {
			return "get-ledger-invalid-nodeids"
		}
	}
	if req.QueryType != nil && *req.QueryType != message.QueryTypeIndirect {
		return "get-ledger-bad-querytype"
	}
	if req.HasQueryDepth() && (req.InfoType == message.LedgerInfoBase || req.QueryDepth > maxQueryDepth) {
		return "get-ledger-bad-querydepth"
	}
	return ""
}

func (r *Router) chargeGetLedgerAdmission(msg *peermanagement.InboundMessage, reason string) {
	if !msg.SelectPeerCharge(resource.FeeInvalidData(), reason) {
		r.serve.IncPeerBadData(uint64(msg.PeerID), reason)
	}
}

func (r *Router) chargeGetLedgerWorker(msg *peermanagement.InboundMessage, fee resource.Charge, reason string) {
	if !msg.ChargePeer(fee, reason) {
		r.serve.IncPeerBadData(uint64(msg.PeerID), reason)
	}
}

func (r *Router) handleGetLedger(ctx context.Context, msg *peermanagement.InboundMessage) {
	defer r.recoverFrame(msg, "get_ledger")
	if ctx.Err() != nil {
		return
	}

	req := msg.GetLedger
	if req == nil {
		decoded, err := message.Decode(message.TypeGetLedger, msg.Payload)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			r.logger.Warn("failed to decode get_ledger", "error", err, "peer", msg.PeerID)
			r.chargeGetLedgerAdmission(msg, "get-ledger-decode")
			return
		}
		var ok bool
		req, ok = decoded.(*message.GetLedger)
		if !ok {
			r.chargeGetLedgerAdmission(msg, "get-ledger-decode")
			return
		}
		if reason := r.validateGetLedgerRequest(req); reason != "" {
			r.chargeGetLedgerAdmission(msg, reason)
			return
		}
	}

	tooManyNodeIDs := false
	if req.InfoType != message.LedgerInfoBase {
		limit := len(req.NodeIDs)
		if limit > txSetSoftMaxReplyNodes {
			limit = txSetSoftMaxReplyNodes
			tooManyNodeIDs = true
		}
		for _, rawID := range req.NodeIDs[:limit] {
			if ctx.Err() != nil {
				return
			}
			if _, _, valid := parseSHAMapNodeID(rawID); !valid {
				r.chargeGetLedgerWorker(msg, resource.FeeInvalidData(), "get-ledger-invalid-nodeid")
				return
			}
		}
		if tooManyNodeIDs {
			_ = msg.ChargePeer(resource.FeeModerateBurdenPeer(), "get-ledger-too-many-nodeids")
			req.NodeIDs = req.NodeIDs[:limit]
		}
	}
	if !req.HasRequestCookie() {
		_ = msg.ChargePeer(resource.FeeModerateBurdenPeer(), "get-ledger-request")
	}

	r.logger.Debug("peer requests ledger",
		"peer", msg.PeerID,
		"itype", req.InfoType,
		"seq", req.LedgerSeq,
		"hash_len", len(req.LedgerHash),
	)

	// For a liTS_CANDIDATE request, ledger_hash carries the tx-set ID and
	// the response is TMLedgerData{type=liTS_CANDIDATE, ...}.
	if req.InfoType == message.LedgerInfoTsCandidate {
		r.serveTxSet(ctx, msg.PeerID, req)
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Load-shed ledger-BODY requests under load. The liTS_CANDIDATE branch
	// above is exempt from this isLoadedLocal gate — a tx-set serve that
	// reaches the handler is answered — so this gate runs only for
	// liBASE / liAS_NODE / liTX_NODE. (Under extreme load the bounded serve
	// pool can still shed any get_ledger, tx-set included, before it reaches
	// here; the requesting peer retries elsewhere.)
	loadedLocal := false
	if ft := svc.FeeTrack(); ft != nil {
		loadedLocal = ft.IsLoadedLocal()
	}
	if r.serve.ShouldShedLedgerRequest(uint64(msg.PeerID), loadedLocal) {
		r.logger.Debug("get_ledger shed under load",
			"peer", msg.PeerID, "itype", req.InfoType)
		return
	}
	workspace := r.reserveLedgerServeWorkspace(ctx, req.InfoType == message.LedgerInfoBase)
	if workspace == nil {
		return
	}
	defer workspace.close()

	var (
		l   *ledger.Ledger
		err error
	)
	if req.HasLedgerHash() {
		var hash [32]byte
		copy(hash[:], req.LedgerHash)
		l, err = svc.GetLedgerByHashContext(ctx, hash)
	} else if req.HasLedgerSeq() {
		if req.LedgerSeq < svc.EarliestFetch() {
			return
		}
		l, err = svc.GetLedgerBySequenceContext(ctx, req.LedgerSeq)
	} else if req.HasLType() && req.LType == message.LedgerTypeClosed {
		l = svc.GetClosedLedger()
	}
	if ctx.Err() != nil {
		return
	}
	if err != nil || l == nil {
		// We don't have it — relay to a peer that advertises it, mirroring
		// rippled's getLedger relay. Falls through to a silent drop when the
		// request isn't relayable or no peer qualifies.
		r.maybeRelayGetLedger(ctx, msg.PeerID, req)
		return
	}
	if req.HasLedgerSeq() {
		if l.Sequence() != req.LedgerSeq {
			if !req.HasRequestCookie() {
				r.chargeGetLedgerWorker(msg, resource.FeeMalformedRequest(), "get-ledger-sequence-mismatch")
			}
			return
		}
	} else if l.Sequence() < svc.EarliestFetch() {
		return
	}

	// Refuse to serve a ledger online-delete has reclaimed: rippled can't
	// serve what its store deleted, so neither do we (the in-memory history
	// window may still hold it). The liTS_CANDIDATE tx-set path returned
	// above is exempt — consensus liveness depends on it always serving.
	if r.belowFloor(l.Sequence()) {
		floor := uint32(0)
		if r.catchupReplay != nil {
			floor = r.catchupReplay.minimumOnline()
		}
		r.logger.Debug("get_ledger declined: below online-delete floor",
			"peer", msg.PeerID, "seq", l.Sequence(), "floor", floor)
		return
	}

	hash := l.Hash()
	var nodes []message.LedgerNode
	switch req.InfoType {
	case message.LedgerInfoBase:
		nodes = r.buildLedgerBaseNodes(ctx, l)
	case message.LedgerInfoAsNode:
		nodes = r.serveLedgerMapNodes(ctx, workspace, l.StateMapSnapshotContext, req, msg.PeerID, "state")
	case message.LedgerInfoTxNode:
		nodes = r.serveLedgerMapNodes(ctx, workspace, l.TxMapSnapshotContext, req, msg.PeerID, "tx")
	}
	if ctx.Err() != nil || len(nodes) == 0 {
		return
	}

	resp := &message.LedgerData{
		LedgerHash:       hash[:],
		LedgerSeq:        l.Sequence(),
		InfoType:         req.InfoType,
		Nodes:            nodes,
		RequestCookie:    uint32(req.RequestCookie),
		RequestCookieSet: req.HasRequestCookie(),
	}

	r.sendLedgerResponse(ctx, workspace, msg.PeerID, resp)
}

// buildLedgerBaseNodes builds the liBASE reply node set: node[0] is the
// wire header (no trailing hash); node[1] is the account-state root when
// the state tree is non-empty; node[2] is the transaction root when the
// ledger has transactions (txHash and tx-map hash both non-zero).
func (r *Router) buildLedgerBaseNodes(ctx context.Context, l *ledger.Ledger) []message.LedgerNode {
	if ctx.Err() != nil {
		return nil
	}
	nodes := []message.LedgerNode{{NodeData: header.AddRaw(l.Header(), false)}}

	if ctx.Err() != nil {
		return nil
	}
	stateHash, err := l.StateMapHash()
	if err != nil || stateHash == ([32]byte{}) {
		return nodes
	}
	if ctx.Err() != nil {
		return nil
	}
	stateMap, err := l.StateMapSnapshotContext(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.logger.Warn("ledger base: state snapshot failed", "error", err)
		return nodes
	}
	stateRoot, err := stateMap.SerializeRootContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		r.logger.Warn("ledger base: state root serialize failed", "error", err)
		return nodes
	}
	nodes = append(nodes, message.LedgerNode{NodeData: stateRoot})

	if ctx.Err() != nil {
		return nil
	}
	if l.Header().TxHash == ([32]byte{}) {
		return nodes
	}
	if ctx.Err() != nil {
		return nil
	}
	txHash, err := l.TxMapHash()
	if err != nil || txHash == ([32]byte{}) {
		return nodes
	}
	if ctx.Err() != nil {
		return nil
	}
	txMap, err := l.TxMapSnapshotContext(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		r.logger.Warn("ledger base: tx snapshot failed", "error", err)
		return nodes
	}
	txRoot, err := txMap.SerializeRootContext(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		r.logger.Warn("ledger base: tx root serialize failed", "error", err)
		return nodes
	}
	return append(nodes, message.LedgerNode{NodeData: txRoot})
}

// serveLedgerMapNodes walks the requested SHAMap node IDs of a ledger's
// state or transaction tree: fat nodes, QueryDepth levels deep, honouring
// the soft/hard reply caps. fatLeaves is true here — only liTS_CANDIDATE
// uses false.
func (r *Router) serveLedgerMapNodes(ctx context.Context, workspace *ledgerServeWorkspace, snapshot func(context.Context) (*shamap.SHAMap, error), req *message.GetLedger, peerID peermanagement.PeerID, label string) []message.LedgerNode {
	if ctx.Err() != nil {
		return nil
	}
	m, err := snapshot(ctx)
	if ctx.Err() != nil {
		return nil
	}
	if err != nil || m == nil {
		r.logger.Debug("ledger node serve: map snapshot unavailable", "error", err, "peer", peerID)
		return nil
	}
	queryDepth := r.ledgerQueryDepth(peerID, req)
	nodes, err := buildShaMapReplyNodes(ctx, workspace.walkBudget(), m, req.NodeIDs, queryDepth, true, r.logger, peerID,
		fmt.Sprintf("ledger %d %s", req.LedgerSeq, label))
	if err != nil {
		return nil
	}
	return nodes
}

// serveTxSet replies with the requested tx-set subtrees. Candidate replies
// omit boundary leaves because the requester may already have those transactions.
func (r *Router) serveTxSet(ctx context.Context, peerID peermanagement.PeerID, req *message.GetLedger) {
	if ctx.Err() != nil || len(req.LedgerHash) != 32 {
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], req.LedgerHash)

	ts, ok := r.adaptor.txSetCache.Get(txSetID)
	if !ok {
		// We don't have it — relay to a peer that advertised it (getTxSet).
		if r.maybeRelayGetLedger(ctx, peerID, req) {
			return
		}
		r.logger.Debug("peer requested tx-set we don't have",
			"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]))
		return
	}
	workspace := r.reserveLedgerServeWorkspace(ctx, false)
	if workspace == nil {
		return
	}
	defer workspace.close()

	// SHAMap is non-nil for any TxSet that reached the cache —
	// newTxSet returns an error before stashing on shamap.New failure.
	txMap := ts.shamap()

	queryDepth := r.ledgerQueryDepth(peerID, req)
	// fatLeaves is always false for liTS_CANDIDATE.
	const fatLeaves = false

	nodes, err := buildShaMapReplyNodes(ctx, workspace.walkBudget(), txMap, req.NodeIDs, queryDepth, fatLeaves, r.logger, peerID,
		fmt.Sprintf("txset %x", txSetID[:8]))
	if err != nil {
		return
	}

	resp := &message.LedgerData{
		LedgerHash:       req.LedgerHash,
		LedgerSeq:        0, // tx-set responses carry no ledger seq
		InfoType:         message.LedgerInfoTsCandidate,
		Nodes:            nodes,
		RequestCookie:    uint32(req.RequestCookie),
		RequestCookieSet: req.HasRequestCookie(),
	}

	if !r.sendLedgerResponse(ctx, workspace, peerID, resp) {
		return
	}
	r.logger.Debug("served tx-set to peer",
		"peer", peerID,
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"shamap_nodes", len(nodes),
		"txs", ts.Size(),
		"query_depth", queryDepth,
		"requested_nodes", len(req.NodeIDs))
}

func (r *Router) ledgerQueryDepth(peerID peermanagement.PeerID, req *message.GetLedger) int {
	if req != nil && req.HasQueryDepth() {
		return int(req.QueryDepth)
	}
	if latency, ok := r.serve.PeerLatency(uint64(peerID)); ok && latency >= 300*time.Millisecond {
		return 2
	}
	return 1
}

func (r *Router) sendLedgerResponse(ctx context.Context, workspace *ledgerServeWorkspace, peerID peermanagement.PeerID, resp *message.LedgerData) bool {
	if ctx.Err() != nil {
		return false
	}
	if r.serve.PeerSupportsNodeDepth(uint64(peerID)) && resp.InfoType != message.LedgerInfoBase {
		if err := useLedgerNodeDepth(ctx, resp.Nodes); err != nil {
			if ctx.Err() == nil {
				r.logger.Warn("failed to encode ledger node references", "error", err)
			}
			return false
		}
	}
	if ctx.Err() != nil {
		return false
	}
	frame, err := message.EncodeLedgerDataFrame(resp, workspace.wire)
	if ctx.Err() != nil {
		return false
	}
	if err != nil {
		r.logger.Warn("failed to encode ledger_data response", "error", err)
		return false
	}
	if err := r.serve.SendToPeer(uint64(peerID), frame); err != nil {
		r.logger.Debug("failed to send ledger_data to peer", "error", err, "peer", peerID)
		return false
	}
	return true
}

// buildShaMapReplyNodes builds the LedgerNode payload of a TMLedgerData reply
// (liTS_CANDIDATE / liAS_NODE / liTX_NODE), honouring requested
// NodeIDs/QueryDepth and soft/hard reply caps. label identifies the source map
// for logging.
func buildShaMapReplyNodes(
	ctx context.Context,
	budget *shamap.WireBudget,
	m *shamap.SHAMap,
	requestedNodeIDs [][]byte,
	queryDepth int,
	fatLeaves bool,
	logger logger,
	peerID peermanagement.PeerID,
	label string,
) ([]message.LedgerNode, error) {
	nodes := make([]message.LedgerNode, 0)
	for i, rawID := range requestedNodeIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Soft cap: stop starting new subtrees.
		if len(nodes) >= txSetSoftMaxReplyNodes {
			observability.RecordMemoryAdmission("ledger_serve", "truncated")
			logger.Debug("shamap serve: soft-cap reached, stopping subtree iteration",
				"peer", peerID, "map", label,
				"nodes_so_far", len(nodes), "remaining_requested", len(requestedNodeIDs)-i)
			break
		}
		path, depth, ok := parseSHAMapNodeID(rawID)
		if !ok {
			logger.Debug("shamap serve: bad SHAMapNodeID in request, skipping",
				"peer", peerID, "map", label,
				"node_idx", i, "len", len(rawID))
			continue
		}
		subtree, err := m.GetNodeFatByPathBounded(ctx, path, depth, queryDepth, fatLeaves, budget)
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		truncated := errors.Is(err, shamap.ErrWireBudget)
		if err != nil && !truncated {
			logger.Debug("shamap serve: GetNodeFatByPath failed, skipping",
				"peer", peerID, "map", label,
				"error", err.Error())
			continue
		}
		for _, n := range subtree {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			nodes = append(nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
		}
		if truncated {
			observability.RecordMemoryAdmission("ledger_serve", "truncated")
			return nodes, nil
		}
	}
	return nodes, nil
}

// parseSHAMapNodeID decodes the 33-byte wire representation into (path,
// depth).
func parseSHAMapNodeID(raw []byte) (path [32]byte, depth int, ok bool) {
	nodeID, err := shamap.ParseNodeID(raw)
	if err != nil {
		return path, 0, false
	}
	return nodeID.ID(), int(nodeID.Depth()), true
}
