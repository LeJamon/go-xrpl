// Inbound handlers for protocol messages that are pure transport plumbing
// (no consensus-router state). Each mirrors a PeerImp::onMessage path in
// rippled — see the per-handler comment for the reference line.

package peermanagement

import (
	"log/slog"
	"net"
	"strconv"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/protocol"
)

// peerSendQueueDropThreshold gates inbound handlers that would
// otherwise enqueue heavy outbound work (e.g. handleGetObjectsMessage
// queries). Mirrors rippled Tuning::dropSendQueue=192 (Tuning.h:49)
// against its deeper send queue; we scale to 75% of DefaultSendBufferSize so
// go-xrpl refuses new work before peer.Send returns
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
// known load/report-time for that node. After the registry-update
// loop we recompute the cluster-fee median over members reported
// within the last clusterFeeWindow and forward it through
// clusterFeeSink, mirroring rippled PeerImp.cpp:1175-1193 which calls
// getFeeTrack().setClusterFee(median). The trailing LoadSource gossip
// is imported into the resource manager so per-source charge
// accounting is shared across the cluster, mirroring rippled
// PeerImp.cpp:1157-1172.
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
	member, isMember := o.cluster.Member(pubToken.Bytes())
	if !isMember {
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

	// Recompute the cluster-fee median and forward it through the
	// LoadFeeTrack sink. Mirrors rippled PeerImp.cpp:1175-1193: take
	// the median of cluster-member LoadFees reported within the last
	// clusterFeeWindow, then setClusterFee. An empty set (no fresh
	// reports) yields no setClusterFee call — rippled also falls
	// through with clusterFee=0 in that case but we leave the prior
	// value intact, mirroring the more general "no signal → no
	// change" pattern.
	if o.clusterFeeSink != nil {
		if fee, ok := o.cluster.MedianFee(time.Now().Add(-clusterFeeWindow)); ok {
			o.clusterFeeSink(fee)
		}
	}

	// LoadSource gossip → resource manager. Mirrors rippled
	// PeerImp.cpp:1157-1172: when the frame carries at least one
	// TMLoadSource, build a resource.Gossip from the entries whose
	// name parses as an IP endpoint (rippled drops the rest via the
	// `item.address != Endpoint()` guard at PeerImp.cpp:1168 while
	// keeping the rest of the frame) and import it under this cluster
	// peer's configured name — rippled's importConsumers(name(), …) at
	// PeerImp.cpp:1171, where name() is the empty string for an unnamed
	// member. importConsumers is then called for the whole frame even if
	// every item was filtered out, matching rippled's gate on the raw
	// loadsources count rather than the surviving set.
	if o.resourceManager != nil && len(cm.LoadSources) != 0 {
		gossip := resource.Gossip{Items: make([]resource.GossipItem, 0, len(cm.LoadSources))}
		for _, src := range cm.LoadSources {
			if !validGossipAddress(src.Name) {
				continue
			}
			gossip.Items = append(gossip.Items, resource.GossipItem{
				Address: src.Name,
				Balance: int(src.Cost),
			})
		}
		o.resourceManager.ImportConsumers(member.Name, gossip)
	}
}

// validGossipAddress reports whether name parses as the IP endpoint that
// a TMLoadSource carries. Mirrors rippled's
// beast::IP::Endpoint::from_string + `!= Endpoint()` guard at
// PeerImp.cpp:1166-1168, which silently drops a load source whose name
// is not a valid endpoint. Both the rippled "ip:port" form (its exported
// keys canonicalise to port 0) and go-xrpl's bare-host form (resource
// keys strip the inbound port — see resource.normalizeAddr) round-trip.
// The port is range-checked as a uint16 to match from_string, which
// parses it into a uint16 and fails on an out-of-range or non-numeric
// port (IPEndpoint.cpp:179-182); net.ParseIP already rejects anything
// longer than from_string_checked's 64-char cap, so no separate guard.
func validGossipAddress(name string) bool {
	if name == "" {
		return false
	}
	host := name
	if h, port, err := net.SplitHostPort(name); err == nil {
		if port != "" {
			if _, err := strconv.ParseUint(port, 10, 16); err != nil {
				return false
			}
		}
		host = h
	}
	return net.ParseIP(host) != nil
}

// handleGetObjectsMessage processes mtGET_OBJECTS from a peer. Mirrors
// rippled PeerImp::onMessage(TMGetObjectByHash) at PeerImp.cpp:2442-2595.
//
// The wire object covers three feature surfaces:
//   - otFETCH_PACK requests/replies (bulk SHAMap-node prefetch);
//   - otTRANSACTIONS requests/replies (tx-reduce-relay back-fill);
//   - generic node-store object fetch by hash.
//
// Fetch-pack requests are served from the ledger provider (serveFetchPack)
// and fetch-pack replies are forwarded to the consensus router, which owns
// ledger acquisitions and the fetch-pack cache. tx-reduce-relay back-fill is
// gated on the operator opt-in (config.go EnableTxReduceRelay defaults to
// false), so that branch mirrors rippled's rejection path when off. The
// generic node-store object fetch is served from the local node store via
// serveGetObjects when a provider is wired.
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
		// threshold is 192 against a much deeper queue; go-xrpl's
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
			// Rippled at PeerImp.cpp:2458-2462 forwards to doFetchPack.
			// Build a pack of the predecessor ledger's SHAMap nodes and
			// reply (serveFetchPack), mirroring makeFetchPack.
			o.serveFetchPack(evt.PeerID, gob)
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

		// Generic node-store object fetch by hash. Mirrors rippled's
		// fetchNodeObject loop at PeerImp.cpp:2483-2538.
		o.serveGetObjects(evt.PeerID, gob)
		return
	}

	// Reply branch (query=false). Rippled adds the inbound objects to the
	// fetch-pack cache at PeerImp.cpp:2547-2593. The acquisition state and
	// the fetch-pack cache live in the consensus router, so forward the reply
	// onto the overlay→router channel exactly as every other peer-originated
	// reply (TMLedgerData, TMTransaction) is delivered. Other reply types have
	// no consumer and are dropped.
	if gob.ObjType == message.ObjectTypeFetchPack {
		select {
		case o.messages <- &InboundMessage{
			PeerID:  evt.PeerID,
			Type:    evt.MessageType,
			Payload: evt.Payload,
		}:
		default:
			o.droppedMessages.Add(1)
			slog.Warn("TMGetObjects fetch-pack reply dropped: channel full",
				"t", "Overlay", "peer", evt.PeerID)
		}
		return
	}
	slog.Debug("TMGetObjects reply received without outstanding request; dropping",
		"t", "Overlay", "peer", evt.PeerID)
}

// serveFetchPack answers an inbound mtGET_OBJECTS{otFETCH_PACK, query=true}.
// Mirrors rippled PeerImp::doFetchPack → LedgerMaster::makeFetchPack
// (PeerImp.cpp:2753-2784, LedgerMaster.cpp:2096-2225): build a pack of the
// SHAMap nodes for the predecessor of the requested ledger and reply with a
// query=false TMGetObjectByHash. The requested ledger hash must be 32 bytes;
// an unknown ledger or unavailable parent yields an empty pack which is
// dropped (rippled charges the peer there; we mirror the more permissive
// go-xrpl stance taken by serveDoTransactions and only charge a malformed hash).
func (o *Overlay) serveFetchPack(peerID PeerID, req *message.GetObjectByHash) {
	if len(req.LedgerHash) != 32 {
		o.IncPeerBadData(peerID, "fetch-pack-bad-hash")
		return
	}
	var haveHash [32]byte
	copy(haveHash[:], req.LedgerHash)

	// maxObjects=0 lets the provider apply its own per-pack cap.
	objects, err := o.ledgerSync.MakeFetchPack(haveHash, 0)
	if err != nil {
		slog.Debug("fetch-pack build failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	if len(objects) == 0 {
		return
	}

	reply := &message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      false,
		Seq:        req.Seq,
		LedgerHash: append([]byte(nil), req.LedgerHash...),
		Objects:    objects,
	}
	encoded, err := message.Encode(reply)
	if err != nil {
		slog.Debug("fetch-pack reply encode failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeGetObjects, encoded)
	if err != nil {
		slog.Debug("fetch-pack reply frame build failed",
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
		slog.Debug("fetch-pack reply send failed",
			"t", "Overlay", "peer", peerID, "err", sendErr)
	}
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
	txProvider := o.txProviderSnapshot()
	if txProvider == nil {
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
		if _, present := txProvider(hash); present {
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

// endpointsIngestMaxEntries bounds an inbound TMEndpoints frame.
// Mirrors rippled PeerImp.cpp:1206 — a frame at or above this count is
// rejected wholesale and the peer charged for useless data.
const endpointsIngestMaxEntries = 1024

// handleEndpointsMessage processes mtENDPOINTS from a peer and feeds the
// advertised addresses into Discovery, the gossip half of overlay peer
// discovery. Mirrors rippled PeerImp::onMessage(TMEndpoints) at
// PeerImp.cpp:1197-1251.
//
// Gating mirrors rippled exactly: ignore endpoints from a peer that is
// not tracking-converged or that speaks a version other than 2, and
// reject (with a charge) any frame advertising 1024+ entries.
//
// Per-entry, an unparseable address is skipped and charged as bad data
// — rippled accumulates a feeInvalidData charge per malformed endpoint
// rather than dropping the whole frame, since the remaining entries may
// still be valid. A hops==0 entry describes the sending peer itself; its
// self-reported host is untrustworthy, so we overwrite it with the
// socket's observed remote IP (keeping the advertised port), matching
// rippled's remote_address_.at_port(result->port()).
func (o *Overlay) handleEndpointsMessage(evt Event) {
	o.peersMu.RLock()
	peer, exists := o.peers[evt.PeerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}

	// Drop endpoints from peers we don't yet trust or that speak an
	// unsupported version (PeerImp.cpp:1201). No charge — a peer that
	// hasn't converged or predates v2 isn't misbehaving.
	if peer.Tracking() != PeerTrackingConverged {
		return
	}

	decoded, err := message.Decode(message.TypeEndpoints, evt.Payload)
	if err != nil {
		o.IncPeerBadData(evt.PeerID, "endpoints-decode")
		return
	}
	eps, ok := decoded.(*message.Endpoints)
	if !ok {
		return
	}
	if eps.Version != 2 {
		return
	}

	if len(eps.EndpointsV2) >= endpointsIngestMaxEntries {
		o.IncPeerBadData(evt.PeerID, "endpoints-too-large")
		return
	}

	remoteIP := peer.RemoteIP()
	for _, tm := range eps.EndpointsV2 {
		parsed, parseErr := ParseEndpoint(tm.Endpoint)
		if parseErr != nil || net.ParseIP(parsed.Host) == nil {
			// rippled's from_string_checked rejects anything that is not
			// a literal IP:port, charging the peer (PeerImp.cpp:1218-1226).
			// ParseEndpoint is laxer — it accepts hostnames for the
			// outbound Connect path — so the IP check is applied here.
			o.IncPeerBadData(evt.PeerID, "endpoints-malformed")
			continue
		}

		address := tm.Endpoint
		if tm.Hops == 0 {
			// hops==0 describes the sender; trust the socket IP over
			// the self-reported host (PeerImp.cpp:1234-1235).
			if remoteIP == "" {
				continue
			}
			address = Endpoint{Host: remoteIP, Port: parsed.Port}.String()
		}

		o.discovery.AddPeer(address, tm.Hops, evt.PeerID)
	}
}

// serveDoTransactions answers an inbound mtGET_OBJECTS query whose
// type is otTRANSACTIONS. Mirrors rippled PeerImp::doTransactions
// (PeerImp.cpp:2787-2839): walk the requested hashes, look each up,
// build a TMTransactions reply containing the blobs we have, and
// emit it. Hashes we don't have are charged feeMalformedRequest in
// rippled — we treat them as "skip", matching the more permissive
// go-xrpl stance that the peer may legitimately be a hop ahead.
func (o *Overlay) serveDoTransactions(peerID PeerID, req *message.GetObjectByHash) {
	const maxQueueSize = 64 // matches rippled reduce_relay::MAX_TX_QUEUE_SIZE
	if len(req.Objects) == 0 {
		return
	}
	if len(req.Objects) > maxQueueSize {
		o.IncPeerBadData(peerID, "get-objects-txn-too-big")
		return
	}
	txProvider := o.txProviderSnapshot()
	if txProvider == nil {
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
		blob, ok := txProvider(hash)
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

// serveGetObjects answers an inbound mtGET_OBJECTS query for generic
// node-store objects by hash. Mirrors rippled
// PeerImp::onMessage(TMGetObjectByHash) generic branch
// (PeerImp.cpp:2483-2538): echo the request's type/seq/ledger-hash into
// a query=false reply, look each requested hash up in the local node
// store, and append the blobs we hold.
//
// Unlike serveDoTransactions, this path ALWAYS sends a reply — even an
// empty one — mirroring rippled's unconditional send at
// PeerImp.cpp:2538 so a requester polling several peers can tell "I
// don't have these" from a peer that never answered.
func (o *Overlay) serveGetObjects(peerID PeerID, req *message.GetObjectByHash) {
	o.peersMu.RLock()
	peer, exists := o.peers[peerID]
	o.peersMu.RUnlock()
	if !exists {
		return
	}

	fetch := o.nodeObjectProviderSnapshot()
	if fetch == nil {
		// No node store wired (tests, or an overlay deployed without a
		// backing store). Drop without charging — the peer issued a
		// legitimate request we simply can't serve, and a charge would
		// punish honest peers for a capability we don't run.
		slog.Debug("TMGetObjects nodestore lookup unserved: no node store wired",
			"t", "Overlay", "peer", peerID)
		return
	}

	// Validate the optional ledger hash before doing any work. Rippled
	// charges feeMalformedRequest "ledger hash" on a wrong-sized field
	// and returns (PeerImp.cpp:2492-2501).
	if len(req.LedgerHash) != 0 && len(req.LedgerHash) != 32 {
		o.IncPeerBadData(peerID, "get-objects-ledgerhash")
		return
	}

	// A generic by-hash request is legitimate but moderately burdensome
	// to serve. Rippled charges feeModerateBurdenPeer once it reaches
	// this branch, ahead of the fetch loop (PeerImp.cpp:2503-2505).
	peer.Charge(resource.FeeModerateBurdenPeer, "get object by hash request")

	reply := &message.GetObjectByHash{
		Query:   false,
		ObjType: req.ObjType,
		Seq:     req.Seq,
		Objects: make([]message.IndexedObject, 0, len(req.Objects)),
	}
	if len(req.LedgerHash) != 0 {
		reply.LedgerHash = append([]byte(nil), req.LedgerHash...)
	}

	for _, obj := range req.Objects {
		// Rippled only processes objects carrying a uint256-sized hash
		// (PeerImp.cpp:2511); others are silently skipped.
		if len(obj.Hash) != 32 {
			continue
		}
		var hash [32]byte
		copy(hash[:], obj.Hash)
		blob, ok := fetch(hash)
		if !ok {
			continue
		}
		// Rippled echoes the request's nodeid into the reply's index
		// field and copies the ledger seq back (PeerImp.cpp:2526-2529).
		out := message.IndexedObject{
			Hash:      append([]byte(nil), obj.Hash...),
			Data:      blob,
			LedgerSeq: obj.LedgerSeq,
		}
		if len(obj.NodeID) != 0 {
			out.Index = append([]byte(nil), obj.NodeID...)
		}
		reply.Objects = append(reply.Objects, out)
	}

	encoded, err := message.Encode(reply)
	if err != nil {
		slog.Debug("TMGetObjectByHash reply encode failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	frame, err := message.BuildWireMessage(message.TypeGetObjects, encoded)
	if err != nil {
		slog.Debug("TMGetObjectByHash reply frame build failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	if sendErr := peer.Send(frame); sendErr != nil {
		slog.Debug("TMGetObjectByHash reply send failed",
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
// which go-xrpl doesn't implement (no tx-reduce-relay outbound queue
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

	// Record the number of transactions carried in this batch, mirroring
	// rippled addTxMetrics(m->transactions_size()) at PeerImp.cpp:2680.
	o.txm.addMissingTx(uint64(len(batch.Transactions)))

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
