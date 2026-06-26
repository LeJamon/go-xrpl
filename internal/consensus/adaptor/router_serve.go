package adaptor

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/shamap"
)

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

	// qtINDIRECT is the only valid query_type. A present-but-different
	// value is invalid data: charge the peer and drop the request without
	// disconnecting, mirroring rippled's onMessage(TMGetLedger). Absence of
	// the field (the common case) is always accepted.
	if req.QueryType != nil && *req.QueryType != message.QueryTypeIndirect {
		r.logger.Debug("get_ledger rejected: invalid query_type",
			"peer", msg.PeerID, "query_type", int32(*req.QueryType))
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "get-ledger-bad-querytype")
		return
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
		r.serveTxSet(msg.PeerID, req)
		return
	}

	// liBASE / liAS_NODE / liTX_NODE all operate on a stored ledger; only
	// these three plus liTS_CANDIDATE are served, so anything else is
	// dropped.
	switch req.InfoType {
	case message.LedgerInfoBase, message.LedgerInfoAsNode, message.LedgerInfoTxNode:
	default:
		return
	}

	// Reject a malformed lookup before touching the ledger service, the way
	// rippled's onMessage(TMGetLedger) does: a non-candidate request that names
	// neither a hash, a sequence, nor ltCLOSED has nothing to resolve, and an
	// ltype outside [ltACCEPTED, ltCLOSED] is invalid. Both are bad data —
	// charge the peer and drop without disconnecting.
	if len(req.LedgerHash) != 32 && req.LedgerSeq == 0 && req.LType != message.LedgerTypeClosed {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "get-ledger-invalid-request")
		return
	}
	if req.LType < message.LedgerTypeAccepted || req.LType > message.LedgerTypeClosed {
		r.adaptor.IncPeerBadData(uint64(msg.PeerID), "get-ledger-invalid-ltype")
		return
	}

	svc := r.adaptor.LedgerService()
	if svc == nil {
		return
	}

	// Load-shed ledger-BODY requests under load. The liTS_CANDIDATE branch
	// above is intentionally exempt, because consensus liveness depends on
	// tx-set acquisition always being served — so this gate runs only for
	// liBASE / liAS_NODE / liTX_NODE.
	loadedLocal := false
	if ft := svc.FeeTrack(); ft != nil {
		loadedLocal = ft.IsLoadedLocal()
	}
	if r.adaptor.ShouldShedLedgerRequest(uint64(msg.PeerID), loadedLocal) {
		r.logger.Debug("get_ledger shed under load",
			"peer", msg.PeerID, "itype", req.InfoType)
		return
	}

	var l *ledger.Ledger
	if len(req.LedgerHash) == 32 {
		var hash [32]byte
		copy(hash[:], req.LedgerHash)
		l, err = svc.GetLedgerByHash(hash)
	} else if req.LedgerSeq > 0 {
		l, err = svc.GetLedgerBySequence(req.LedgerSeq)
	} else if req.LType == message.LedgerTypeClosed {
		l = svc.GetClosedLedger()
	}
	if err != nil || l == nil {
		// We don't have it — relay to a peer that advertises it, mirroring
		// rippled's getLedger relay. Falls through to a silent drop when the
		// request isn't relayable or no peer qualifies.
		r.maybeRelayGetLedger(msg.PeerID, req)
		return
	}

	// Refuse to serve a ledger online-delete has reclaimed: rippled can't
	// serve what its store deleted, so neither do we (the in-memory history
	// window may still hold it). The liTS_CANDIDATE tx-set path returned
	// above is exempt — consensus liveness depends on it always serving.
	if r.belowFloor(l.Sequence()) {
		r.logger.Debug("get_ledger declined: below online-delete floor",
			"peer", msg.PeerID, "seq", l.Sequence(), "floor", r.floor.MinimumOnline())
		return
	}

	hash := l.Hash()
	var nodes []message.LedgerNode
	switch req.InfoType {
	case message.LedgerInfoBase:
		nodes = r.buildLedgerBaseNodes(l)
	case message.LedgerInfoAsNode:
		nodes = r.serveLedgerMapNodes(l.StateMapSnapshot, req, msg.PeerID, "state")
	case message.LedgerInfoTxNode:
		nodes = r.serveLedgerMapNodes(l.TxMapSnapshot, req, msg.PeerID, "tx")
	}
	if len(nodes) == 0 {
		return
	}

	resp := &message.LedgerData{
		LedgerHash:    hash[:],
		LedgerSeq:     l.Sequence(),
		InfoType:      req.InfoType,
		Nodes:         nodes,
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

// buildLedgerBaseNodes builds the liBASE reply node set: node[0] is the
// wire header (no trailing hash); node[1] is the account-state root when
// the state tree is non-empty; node[2] is the transaction root when the
// ledger has transactions (txHash and tx-map hash both non-zero).
func (r *Router) buildLedgerBaseNodes(l *ledger.Ledger) []message.LedgerNode {
	nodes := []message.LedgerNode{{NodeData: header.AddRaw(l.Header(), false)}}

	stateHash, err := l.StateMapHash()
	if err != nil || stateHash == ([32]byte{}) {
		return nodes
	}
	stateMap, err := l.StateMapSnapshot()
	if err != nil {
		r.logger.Warn("ledger base: state snapshot failed", "error", err)
		return nodes
	}
	stateRoot, err := stateMap.SerializeRoot()
	if err != nil {
		r.logger.Warn("ledger base: state root serialize failed", "error", err)
		return nodes
	}
	nodes = append(nodes, message.LedgerNode{NodeData: stateRoot})

	if l.Header().TxHash == ([32]byte{}) {
		return nodes
	}
	txHash, err := l.TxMapHash()
	if err != nil || txHash == ([32]byte{}) {
		return nodes
	}
	txMap, err := l.TxMapSnapshot()
	if err != nil {
		r.logger.Warn("ledger base: tx snapshot failed", "error", err)
		return nodes
	}
	txRoot, err := txMap.SerializeRoot()
	if err != nil {
		r.logger.Warn("ledger base: tx root serialize failed", "error", err)
		return nodes
	}
	return append(nodes, message.LedgerNode{NodeData: txRoot})
}

// serveLedgerMapNodes walks the requested SHAMap node IDs of a ledger's
// state or transaction tree: fat nodes, QueryDepth levels deep, honouring
// the soft/hard reply caps. fatLeaves is true here — only liTS_CANDIDATE
// uses false.
func (r *Router) serveLedgerMapNodes(snapshot func() (*shamap.SHAMap, error), req *message.GetLedger, peerID peermanagement.PeerID, label string) []message.LedgerNode {
	m, err := snapshot()
	if err != nil || m == nil {
		r.logger.Debug("ledger node serve: map snapshot unavailable", "error", err, "peer", peerID)
		return nil
	}
	queryDepth := int(req.QueryDepth)
	if queryDepth == 0 {
		queryDepth = defaultQueryDepth
	}
	return buildShaMapReplyNodes(m, req.NodeIDs, queryDepth, true, r.logger, peerID,
		fmt.Sprintf("ledger %d %s", req.LedgerSeq, label))
}

// serveTxSet replies to TMGetLedger{itype=liTS_CANDIDATE} with the tx set
// encoded as TMLedgerData{type=liTS_CANDIDATE, ledger_hash=<txSetID>,
// nodes=[<SHAMapNodeID, wire-serialized SHAMap node>...]}. For each
// requested NodeID, walk QueryDepth levels via GetNodeFatByPath, honouring
// soft/hard caps. Empty nodeids falls back to a full pre-order walk for
// legacy goxrpl→goxrpl fixtures; real requestors always send at least the
// root.
func (r *Router) serveTxSet(peerID peermanagement.PeerID, req *message.GetLedger) {
	if len(req.LedgerHash) != 32 {
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], req.LedgerHash)

	ts, ok := r.adaptor.txSetCache.Get(txSetID)
	if !ok {
		// We don't have it — relay to a peer that advertised it (getTxSet).
		if r.maybeRelayGetLedger(peerID, req) {
			return
		}
		r.logger.Debug("peer requested tx-set we don't have",
			"peer", peerID, "txset", fmt.Sprintf("%x", txSetID[:8]))
		return
	}

	// SHAMap is non-nil for any TxSet that reached the cache —
	// NewTxSet returns an error before stashing on shamap.New failure.
	txMap := ts.shamap()

	queryDepth := int(req.QueryDepth)
	if queryDepth == 0 {
		queryDepth = defaultQueryDepth
	}
	// fatLeaves is always false for liTS_CANDIDATE.
	const fatLeaves = false

	nodes := buildShaMapReplyNodes(txMap, req.NodeIDs, queryDepth, fatLeaves, r.logger, peerID,
		fmt.Sprintf("txset %x", txSetID[:8]))

	resp := &message.LedgerData{
		LedgerHash:    req.LedgerHash,
		LedgerSeq:     0, // tx-set responses carry no ledger seq
		InfoType:      message.LedgerInfoTsCandidate,
		Nodes:         nodes,
		RequestCookie: uint32(req.RequestCookie),
	}

	frame, err := encodeFrame(message.TypeLedgerData, resp)
	if err != nil {
		r.logger.Warn("failed to encode tx-set response", "error", err)
		return
	}

	if err := r.adaptor.SendToPeer(uint64(peerID), frame); err != nil {
		r.logger.Debug("failed to send tx-set response", "error", err, "peer", peerID)
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

// buildShaMapReplyNodes builds the LedgerNode payload of a TMLedgerData reply
// (liTS_CANDIDATE / liAS_NODE / liTX_NODE), honouring requested
// NodeIDs/QueryDepth and soft/hard reply caps. label identifies the source map
// for logging.
func buildShaMapReplyNodes(
	m *shamap.SHAMap,
	requestedNodeIDs [][]byte,
	queryDepth int,
	fatLeaves bool,
	logger logger,
	peerID peermanagement.PeerID,
	label string,
) []message.LedgerNode {
	if len(requestedNodeIDs) == 0 {
		wireNodes, err := m.WalkWireNodes()
		if err != nil {
			logger.Warn("failed to walk SHAMap for serve",
				"error", err, "peer", peerID, "map", label)
			return nil
		}
		nodes := make([]message.LedgerNode, 0, len(wireNodes))
		for _, n := range wireNodes {
			if len(nodes) >= txSetHardMaxReplyNodes {
				break
			}
			nodes = append(nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
		}
		return nodes
	}

	nodes := make([]message.LedgerNode, 0)
	for i, rawID := range requestedNodeIDs {
		// Soft cap: stop starting new subtrees.
		if len(nodes) >= txSetSoftMaxReplyNodes {
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
		subtree, err := m.GetNodeFatByPath(path, depth, queryDepth, fatLeaves)
		if err != nil {
			logger.Debug("shamap serve: GetNodeFatByPath failed, skipping",
				"peer", peerID, "map", label,
				"error", err.Error())
			continue
		}
		for _, n := range subtree {
			// Hard cap: truncate mid-subtree.
			if len(nodes) >= txSetHardMaxReplyNodes {
				logger.Debug("shamap serve: hard-cap reached, truncating subtree",
					"peer", peerID, "map", label,
					"nodes", len(nodes))
				return nodes
			}
			nodes = append(nodes, message.LedgerNode{NodeID: n.NodeID, NodeData: n.Data})
		}
	}
	return nodes
}

// parseSHAMapNodeID decodes the 33-byte wire representation into (path,
// depth).
func parseSHAMapNodeID(raw []byte) (path [32]byte, depth int, ok bool) {
	if len(raw) != shamapNodeIDLen {
		return path, 0, false
	}
	copy(path[:], raw[:32])
	depth = int(raw[32])
	if depth < 0 || depth > 64 {
		return path, 0, false
	}
	return path, depth, true
}
