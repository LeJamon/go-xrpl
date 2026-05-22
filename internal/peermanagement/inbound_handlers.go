// Inbound handlers for protocol messages that are pure transport plumbing
// (no consensus-router state). Each mirrors a PeerImp::onMessage path in
// rippled — see the per-handler comment for the reference line.

package peermanagement

import (
	"log/slog"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/protocol"
)

// peerSendQueueDropThreshold gates inbound handlers that would
// otherwise enqueue heavy outbound work (e.g. handleGetObjectsMessage
// queries). Mirrors rippled Tuning::dropSendQueue=192 against its
// deeper send queue; we scale to 75% of DefaultSendBufferSize so
// goXRPL refuses new work before peer.Send returns
// ErrSendBufferFull.
const peerSendQueueDropThreshold = (DefaultSendBufferSize * 3) / 4

// handleClusterMessage processes mtCLUSTER from a peer. Mirrors rippled
// PeerImp::onMessage(TMCluster) at PeerImp.cpp:1125-1194.
//
// Acceptance rule: the SENDER must be in our [cluster_nodes] registry.
// Rippled gates this on Peer::cluster() which returns true when the
// peer's NodePublic was loaded from [cluster_nodes]; we mirror the
// same boundary via Overlay.cluster.Member(peer.RemotePublicKey()).
//
// Payload effect: each ClusterNode entry refreshes the registry's
// known load/report-time for that node. The LoadSource gossip and the
// median-cluster-fee computation that rippled performs are wired into
// its Resource::Manager + LoadFeeTrack subsystems; goXRPL has no
// analog yet, so we only adopt the membership state — closing the
// peer-protocol gap without standing up two more subsystems.
func (o *Overlay) handleClusterMessage(evt Event) {
	o.peersMu.RLock()
	peer, exists := o.peers[evt.PeerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}

	// Sender must be a cluster member. Rippled drops + charges
	// feeUselessData "unknown cluster" at PeerImp.cpp:1128-1131.
	pubToken := peer.RemotePublicKey()
	if pubToken == nil {
		o.IncPeerBadData(evt.PeerID, "cluster-no-pubkey")
		return
	}
	if _, ok := o.cluster.Member(pubToken.Bytes()); !ok {
		slog.Debug("TMCluster from non-cluster peer; dropping",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "cluster-not-member")
		return
	}

	decoded, err := message.Decode(message.TypeCluster, evt.Payload)
	if err != nil {
		o.IncPeerBadData(evt.PeerID, "cluster-decode")
		return
	}
	cm, ok := decoded.(*message.Cluster)
	if !ok {
		return
	}

	for _, node := range cm.ClusterNodes {
		identity, decErr := addresscodec.DecodeNodePublicKey(node.PublicKey)
		if decErr != nil || len(identity) == 0 {
			// Rippled comments at PeerImp.cpp:1145-1147 say we
			// should drop the peer on an unparseable key but the
			// loop body in fact silently skips — the "drop the
			// peer" line is an unimplemented TODO. Mirror the
			// shipped behaviour: skip without charging so a stale
			// cluster registry doesn't slowly accumulate
			// bad-data charge that rippled would not.
			continue
		}
		reportTime := time.Unix(int64(node.ReportTime), 0)
		o.cluster.Update(identity, node.NodeName, node.NodeLoad, reportTime)
	}

	// LoadSource gossip → Resource::Manager: not implemented in
	// goXRPL. The field is parsed by message.Decode so we don't
	// re-validate, but we don't propagate it anywhere. When goXRPL
	// grows a resource manager this is the call site to wire in.
}

// handleGetObjectsMessage processes mtGET_OBJECTS from a peer. Mirrors
// rippled PeerImp::onMessage(TMGetObjectByHash) at PeerImp.cpp:2442-2595.
//
// The wire object covers three feature surfaces:
//   - otFETCH_PACK requests (block-acceleration prefetch);
//   - otTRANSACTIONS requests/replies (tx-reduce-relay back-fill);
//   - generic node-store object fetch by hash.
//
// goXRPL does not implement fetch-packs (no LedgerMaster::gotFetchPack
// path) and does not advertise txReduceRelay by default (config.go
// EnableTxReduceRelay defaults to false). We therefore mirror rippled's
// rejection branches faithfully but stop short of the success paths
// they gate.
func (o *Overlay) handleGetObjectsMessage(evt Event) {
	decoded, err := message.Decode(message.TypeGetObjects, evt.Payload)
	if err != nil {
		o.IncPeerBadData(evt.PeerID, "get-objects-decode")
		return
	}
	gob, ok := decoded.(*message.GetObjectByHash)
	if !ok {
		return
	}

	if gob.Query {
		// Back-pressure gate — mirrors rippled
		// PeerImp.cpp:2452-2456's send_queue_.size() >=
		// Tuning::dropSendQueue early-return. Rippled's absolute
		// threshold is 192 against a much deeper queue; goXRPL's
		// peer.send channel is DefaultSendBufferSize=64 deep, so we
		// gate at 75% (peerSendQueueDropThreshold) to refuse new
		// heavy work before the channel saturates and the next
		// Send returns ErrSendBufferFull.
		o.peersMu.RLock()
		peer, peerOK := o.peers[evt.PeerID]
		o.peersMu.RUnlock()
		if peerOK && peer.SendQueueLen() >= peerSendQueueDropThreshold {
			slog.Debug("TMGetObjects dropped: peer send queue saturated",
				"t", "Overlay", "peer", evt.PeerID,
				"sendq", peer.SendQueueLen())
			return
		}
		switch gob.ObjType {
		case message.ObjectTypeFetchPack:
			// Rippled at PeerImp.cpp:2458-2462 forwards to
			// doFetchPack. goXRPL has no fetch-pack subsystem;
			// treat as an unsupported request and drop without
			// charging — the peer is using a feature we never
			// advertise and a charge here would punish honest
			// gossip-driven peers.
			slog.Debug("TMGetObjects fetch-pack request unsupported; dropping",
				"t", "Overlay", "peer", evt.PeerID)
			return
		case message.ObjectTypeTransactions:
			// Tx-reduce-relay back-fill request. Rippled gates on
			// txReduceRelayEnabled() at PeerImp.cpp:2466-2472 and
			// charges feeMalformedRequest "disabled" when off. We
			// only advertise tx-reduce-relay when the operator
			// opts in (cfg.EnableTxReduceRelay), so the symmetric
			// gate is whether the local config is opted-in AND
			// the peer also negotiated it.
			if !o.cfg.EnableTxReduceRelay || !o.PeerSupports(evt.PeerID, FeatureTxReduceRelay) {
				slog.Debug("TMGetObjects otTRANSACTIONS without negotiated tx-reduce-relay; dropping",
					"t", "Overlay", "peer", evt.PeerID)
				o.IncPeerBadData(evt.PeerID, "get-objects-txn-unnegotiated")
				return
			}
			o.serveDoTransactions(evt.PeerID, gob)
			return
		}

		// Generic node-store lookup. Rippled walks
		// app_.getNodeStore().fetchNodeObject for each requested
		// hash and replies inline (PeerImp.cpp:2483-2538). goXRPL
		// has the NodeStore but no peer-protocol surface that exposes
		// it — wiring requires plumbing nodestore.Store through to
		// the overlay. Out of scope for #497; drop without charging
		// and log so operators see the gap.
		slog.Debug("TMGetObjects query (nodestore lookup) unsupported; dropping",
			"t", "Overlay",
			"peer", evt.PeerID,
			"obj_type", gob.ObjType,
			"requested", len(gob.Objects),
		)
		return
	}

	// Reply branch (query=false). Rippled adds the inbound objects to
	// the fetch-pack cache at PeerImp.cpp:2547-2593. goXRPL has no
	// fetch-pack acquisition state — an unsolicited reply means the
	// peer is malformed or buggy.
	slog.Debug("TMGetObjects reply received without outstanding request; dropping",
		"t", "Overlay", "peer", evt.PeerID)
}

// handleHaveTransactionsMessage processes mtHAVE_TRANSACTIONS from a
// peer. Mirrors rippled PeerImp::onMessage(TMHaveTransactions) at
// PeerImp.cpp:2598-2614 + handleHaveTransactions:2616-2664.
//
// Semantics: the peer announces a list of tx hashes it holds; we reply
// with a TMGetObjectByHash{otTRANSACTIONS, query=true} for the subset
// we don't have. Both directions are part of the tx-reduce-relay
// feature bundle — rippled charges feeMalformedRequest "disabled"
// when the local node isn't running tx-reduce-relay.
func (o *Overlay) handleHaveTransactionsMessage(evt Event) {
	if !o.cfg.EnableTxReduceRelay || !o.PeerSupports(evt.PeerID, FeatureTxReduceRelay) {
		slog.Debug("TMHaveTransactions without negotiated tx-reduce-relay; dropping",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "have-transactions-unnegotiated")
		return
	}
	decoded, err := message.Decode(message.TypeHaveTransactions, evt.Payload)
	if err != nil {
		o.IncPeerBadData(evt.PeerID, "have-transactions-decode")
		return
	}
	ht, ok := decoded.(*message.HaveTransactions)
	if !ok {
		return
	}

	// Without a tx-lookup wired in, we can't tell cache-misses from
	// cache-hits — emitting a request containing every announced
	// hash would amplify network load for a load-reduction feature.
	// Drop the announcement silently in that case (the peer that
	// negotiated tx-reduce-relay isn't malformed).
	if o.txProvider == nil {
		return
	}

	missing := make([]message.IndexedObject, 0, len(ht.Hashes))
	for _, h := range ht.Hashes {
		if len(h) != 32 {
			o.IncPeerBadData(evt.PeerID, "have-transactions-hashsize")
			return
		}
		var hash [32]byte
		copy(hash[:], h)
		if _, present := o.txProvider(hash); present {
			continue
		}
		missing = append(missing, message.IndexedObject{
			Hash: append([]byte(nil), h...),
		})
	}
	if len(missing) == 0 {
		return
	}

	req := &message.GetObjectByHash{
		ObjType: message.ObjectTypeTransactions,
		Query:   true,
		Objects: missing,
	}
	encoded, encErr := message.Encode(req)
	if encErr != nil {
		slog.Debug("TMGetObjectByHash request encode failed",
			"t", "Overlay", "peer", evt.PeerID, "err", encErr)
		return
	}
	frame, frameErr := message.BuildWireMessage(message.TypeGetObjects, encoded)
	if frameErr != nil {
		slog.Debug("TMGetObjectByHash request frame build failed",
			"t", "Overlay", "peer", evt.PeerID, "err", frameErr)
		return
	}
	o.peersMu.RLock()
	peer, exists := o.peers[evt.PeerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}
	if sendErr := peer.Send(frame); sendErr != nil {
		slog.Debug("TMGetObjectByHash request send failed",
			"t", "Overlay", "peer", evt.PeerID, "err", sendErr)
	}
}

// serveDoTransactions answers an inbound mtGET_OBJECTS query whose
// type is otTRANSACTIONS. Mirrors rippled PeerImp::doTransactions
// (PeerImp.cpp:2787-2839): walk the requested hashes, look each up,
// build a TMTransactions reply containing the blobs we have, and
// emit it. Hashes we don't have are charged feeMalformedRequest in
// rippled — we treat them as "skip", matching the more permissive
// goXRPL stance that the peer may legitimately be a hop ahead.
func (o *Overlay) serveDoTransactions(peerID PeerID, req *message.GetObjectByHash) {
	const maxQueueSize = 64 // matches rippled reduce_relay::MAX_TX_QUEUE_SIZE
	if len(req.Objects) == 0 {
		return
	}
	if len(req.Objects) > maxQueueSize {
		o.IncPeerBadData(peerID, "get-objects-txn-too-big")
		return
	}
	if o.txProvider == nil {
		// Negotiated tx-reduce-relay but no lookup wired — silently
		// drop. An operator who flipped EnableTxReduceRelay but
		// hasn't wired SetTxProvider would otherwise spam this log.
		return
	}

	reply := &message.Transactions{
		Transactions: make([]message.Transaction, 0, len(req.Objects)),
	}
	for _, obj := range req.Objects {
		if len(obj.Hash) != 32 {
			o.IncPeerBadData(peerID, "get-objects-txn-hashsize")
			return
		}
		var hash [32]byte
		copy(hash[:], obj.Hash)
		blob, ok := o.txProvider(hash)
		if !ok {
			continue
		}
		reply.Transactions = append(reply.Transactions, message.Transaction{
			RawTransaction:   blob,
			Status:           message.TxStatusCurrent,
			ReceiveTimestamp: uint64(time.Now().Unix() - protocol.RippleEpochUnix),
		})
	}
	if len(reply.Transactions) == 0 {
		return
	}

	encoded, err := message.Encode(reply)
	if err != nil {
		slog.Debug("TMTransactions reply encode failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeTransactions, encoded)
	if err != nil {
		slog.Debug("TMTransactions reply frame build failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}
	if sendErr := peer.Send(frame); sendErr != nil {
		slog.Debug("TMTransactions reply send failed",
			"t", "Overlay", "peer", peerID, "err", sendErr)
	}
}

// handleTransactionsBatchMessage processes mtTRANSACTIONS (a batched
// list of TMTransaction frames). Mirrors rippled
// PeerImp::onMessage(TMTransactions) at PeerImp.cpp:2667-2688.
//
// Each inner TMTransaction is re-emitted onto o.messages so
// router.handleTransaction processes it identically to an unbundled
// TMTransaction frame. This matches rippled's pattern of calling
// handleTransaction(inner, eraseTxQueue=false, batch=true) for each
// child — the only behavioural difference rippled draws between
// batched and unbatched is the eraseTxQueue path on a duplicate hit,
// which goXRPL doesn't implement (no tx-reduce-relay outbound queue
// to erase from).
func (o *Overlay) handleTransactionsBatchMessage(evt Event) {
	if !o.cfg.EnableTxReduceRelay || !o.PeerSupports(evt.PeerID, FeatureTxReduceRelay) {
		slog.Debug("TMTransactions batch without negotiated tx-reduce-relay; dropping",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "transactions-batch-unnegotiated")
		return
	}
	decoded, err := message.Decode(message.TypeTransactions, evt.Payload)
	if err != nil {
		o.IncPeerBadData(evt.PeerID, "transactions-batch-decode")
		return
	}
	batch, ok := decoded.(*message.Transactions)
	if !ok {
		return
	}

	// Re-emit each inner TMTransaction as a standalone wire-encoded
	// inbound message so the router's handleTransaction path picks
	// it up. Encoding the inner protobuf is the cheapest path —
	// alternatively we could expose a router entrypoint that takes
	// a pre-decoded *message.Transaction, but the channel-based
	// dispatch is the established pattern for every other peer
	// message and keeps the relay-timing fix in one place.
	for i := range batch.Transactions {
		inner := &batch.Transactions[i]
		encoded, encErr := message.Encode(inner)
		if encErr != nil {
			slog.Debug("TMTransactions inner encode failed",
				"t", "Overlay", "peer", evt.PeerID, "idx", i, "err", encErr)
			continue
		}
		select {
		case o.messages <- &InboundMessage{
			PeerID:  evt.PeerID,
			Type:    uint16(message.TypeTransaction),
			Payload: encoded,
		}:
		default:
			o.droppedMessages.Add(1)
			slog.Warn("TMTransactions batch fanout dropped: channel full",
				"t", "Overlay", "peer", evt.PeerID, "idx", i)
		}
	}
}
