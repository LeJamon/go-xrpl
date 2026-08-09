// Inbound handlers for protocol messages that are pure transport plumbing
// (no consensus-router state). Each mirrors a PeerImp::onMessage path in
// rippled — see the per-handler comment for the reference line.

package peermanagement

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
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

const (
	// These allowances exceed the protobuf tags, lengths, and repeated-message
	// envelope for one reply item, keeping the pre-marshal budget conservative.
	serveReplyObjectOverhead      = int64(64)
	serveReplyTransactionOverhead = int64(32)
)

type serveReplyBudget struct {
	remaining int64
}

func newServeReplyBudget() serveReplyBudget {
	return serveReplyBudget{remaining: int64(message.MaxMessageSize)}
}

func (b *serveReplyBudget) reserve(overhead int64, fields ...[]byte) bool {
	if overhead < 0 || overhead > b.remaining {
		return false
	}
	required := overhead
	for _, field := range fields {
		if int64(len(field)) > b.remaining-required {
			return false
		}
		required += int64(len(field))
	}
	b.remaining -= required
	return true
}

func limitIndexedObjectsToReplyBudget(objects []message.IndexedObject, fixedFields ...[]byte) []message.IndexedObject {
	budget := newServeReplyBudget()
	if !budget.reserve(serveReplyObjectOverhead, fixedFields...) {
		clear(objects)
		return objects[:0]
	}
	kept := 0
	for i := range objects {
		object := &objects[i]
		if !budget.reserve(serveReplyObjectOverhead, object.Hash, object.NodeID, object.Index, object.Data) {
			break
		}
		objects[kept] = *object
		kept++
	}
	clear(objects[kept:])
	return objects[:kept]
}

// handleClusterMessage processes mtCLUSTER from a peer. Mirrors rippled
// PeerImp::onMessage(TMCluster) at PeerImp.cpp:1125-1194.
//
// Acceptance rule: the SENDER must be in our [cluster_nodes] registry.
// Rippled gates this on Peer::cluster() which returns true when the
// peer's NodePublic was loaded from [cluster_nodes]; we mirror the
// same boundary via Overlay.cluster.Member(peer.RemotePublicKeyBytes()).
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
	peer, exists := o.getPeer(evt.PeerID)
	if !exists {
		return
	}

	// Sender must be a cluster member. Rippled drops + charges
	// feeUselessData "unknown cluster" at PeerImp.cpp:1128-1131.
	pubKey := peer.RemotePublicKeyBytes()
	if len(pubKey) == 0 {
		o.IncPeerBadData(evt.PeerID, "cluster-no-pubkey")
		return
	}
	member, isMember := o.cluster.Member(pubKey)
	if !isMember {
		slog.Debug("TMCluster from non-cluster peer; dropping",
			"t", "Overlay", "peer", evt.PeerID)
		o.IncPeerBadData(evt.PeerID, "cluster-not-member")
		return
	}
	origin := peer.resourceGossipOrigin(member.Name)

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
		var reportTime time.Time
		if node.ReportTime != 0 {
			reportTime = protocol.FromRippleTime(node.ReportTime)
		}
		o.cluster.Update(identity, node.NodeName, node.NodeLoad, reportTime)
	}

	// Recompute the cluster-fee median and forward it through the
	// LoadFeeTrack sink. An empty fresh set publishes zero.
	if sink := o.clusterFeeSinkSnapshot(); sink != nil {
		now := time.Now()
		if o.clock != nil {
			now = o.clock()
		}
		fee, _ := o.cluster.MedianFee(now.Add(-clusterFeeWindow))
		sink(fee)
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
			address, ok := canonicalGossipAddress(src.Name)
			if !ok {
				continue
			}
			gossip.Items = append(gossip.Items, resource.GossipItem{
				Address: address,
				Balance: src.Cost,
			})
		}
		if err := o.resourceManager.ImportConsumers(origin, gossip); err != nil {
			slog.Warn("Cluster load-source snapshot rejected", "t", "Overlay", "peer", evt.PeerID, "err", err)
		}
	}
}

func validGossipAddress(name string) bool {
	_, ok := canonicalGossipAddress(name)
	return ok
}

func canonicalGossipAddress(name string) (string, bool) {
	if len(name) > 64 {
		return "", false
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}

	if addr, err := netip.ParseAddr(name); err == nil {
		if addr.Zone() != "" {
			return "", false
		}
		return addr.Unmap().String(), true
	}
	if strings.HasPrefix(name, "[") && strings.HasSuffix(name, "]") {
		if addr, err := netip.ParseAddr(name[1 : len(name)-1]); err == nil && addr.Zone() == "" {
			return addr.Unmap().String(), true
		}
	}
	if endpoint, err := netip.ParseAddrPort(name); err == nil {
		if endpoint.Addr().Zone() != "" {
			return "", false
		}
		return netip.AddrPortFrom(endpoint.Addr().Unmap(), endpoint.Port()).String(), true
	}

	fields := strings.Fields(name)
	if len(fields) != 2 {
		return "", false
	}
	host := fields[0]
	if strings.HasPrefix(host, "[") || strings.HasSuffix(host, "]") {
		if !strings.HasPrefix(host, "[") || !strings.HasSuffix(host, "]") || len(host) < 3 {
			return "", false
		}
		host = host[1 : len(host)-1]
	}
	addr, err := netip.ParseAddr(host)
	if err != nil || addr.Zone() != "" {
		return "", false
	}
	port, err := strconv.ParseUint(fields[1], 10, 16)
	if err != nil {
		return "", false
	}
	return netip.AddrPortFrom(addr.Unmap(), uint16(port)).String(), true
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
		peer, peerOK := o.getPeer(evt.PeerID)
		if peerOK && peer.SendQueueLen() >= peerSendQueueDropThreshold {
			slog.Debug("TMGetObjects dropped: peer send queue saturated",
				"t", "Overlay", "peer", evt.PeerID,
				"sendq", peer.SendQueueLen())
			return
		}
		switch gob.ObjType {
		case message.ObjectTypeFetchPack:
			if len(gob.LedgerHash) != 32 {
				if peerOK {
					peer.Charge(resource.FeeMalformedRequest(), "fetch pack ledger hash")
				}
				return
			}
			// Rippled at PeerImp.cpp:2458-2462 forwards to doFetchPack.
			// Build a pack of the predecessor ledger's SHAMap nodes and
			// reply (serveFetchPack), mirroring makeFetchPack. Offloaded
			// to the serve-worker pool — building a pack snapshots the
			// state+tx tree (capped at fetchPackMaxObjects nodes) and
			// must not run on the event loop.
			receivedAt := time.Now()
			// The heavy charge is deferred until the worker has passed the
			// provider's busy/stale guard. Charging at admission would make a
			// request that was immediately refused for local load look served.
			o.submitRetainedServe(evt, resource.Charge{},
				func(ctx context.Context) {
					deadlineCtx, cancel := context.WithDeadline(ctx, receivedAt.Add(time.Second))
					defer cancel()
					o.serveFetchPackContext(deadlineCtx, evt.PeerID, gob)
				})
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
			o.submitRetainedServe(evt, resource.FeeModerateBurdenPeer(),
				func(ctx context.Context) { o.serveDoTransactionsContext(ctx, evt.PeerID, gob) })
			return
		}

		// Generic node-store object fetch by hash. Mirrors rippled's
		// fetchNodeObject loop at PeerImp.cpp:2483-2538. Offloaded to the
		// serve-worker pool — up to N node-store fetches per request.
		if gob.HasLedgerHash() && len(gob.LedgerHash) != 32 {
			o.IncPeerBadData(evt.PeerID, "get-objects-ledgerhash")
			return
		}
		if len(gob.Objects) > hardMaxReplyNodes {
			if peer, ok := o.getPeer(evt.PeerID); ok {
				peer.Charge(resource.FeeInvalidData(), "oversized get object request")
			}
			return
		}
		o.submitRetainedServe(evt, resource.FeeModerateBurdenPeer(),
			func(ctx context.Context) { o.serveGetObjectsContext(ctx, evt.PeerID, gob) })
		return
	}

	// Reply branch (query=false). Rippled adds the inbound objects to the
	// fetch-pack cache at PeerImp.cpp:2547-2593. The acquisition state and
	// the fetch-pack cache live in the consensus router, so forward the reply
	// onto the same overlay→router channel other peer-originated replies use.
	// Both the bulk fetch-pack reply and the otSTATE_NODE/otTRANSACTION_NODE
	// nodes served for a by-hash acquisition escalation (issue #985) carry
	// SHAMap nodes the router caches; other reply types have no consumer and
	// are dropped.
	switch gob.ObjType {
	case message.ObjectTypeFetchPack, message.ObjectTypeStateNode, message.ObjectTypeTransactionNode:
		o.forwardLedgerData(evt.retainedInboundMessage())
		return
	}
	slog.Debug("TMGetObjects reply received without outstanding request; dropping",
		"t", "Overlay", "peer", evt.PeerID)
}

func (o *Overlay) submitRetainedServe(
	evt Event,
	admission resource.Charge,
	job func(context.Context),
) {
	reservation := evt.reservation.retain()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(reservation.release) }
	if !o.submitServeForPeerOwned(evt.PeerID, admission, func(ctx context.Context) {
		defer release()
		job(ctx)
	}, release) {
		release()
	}
}

// serveFetchPack answers an inbound mtGET_OBJECTS{otFETCH_PACK, query=true}.
// Mirrors rippled PeerImp::doFetchPack → LedgerMaster::makeFetchPack
// (PeerImp.cpp:2753-2784, LedgerMaster.cpp:2096-2225): build a pack of the
// SHAMap nodes for the predecessor of the requested ledger and reply with a
// query=false TMGetObjectByHash. The requested ledger hash must be 32 bytes; an
// unknown ledger or unavailable parent yields an empty pack which is dropped.
// A request below the serving range is dropped with an additional malformed
// request charge. The heavy-burden charge is applied after the provider's
// busy/stale guard, so a locally busy request is dropped without either a
// heavy or no-reply charge. Building a pack snapshots the want ledger's
// state+tx tree and walks up to fetchPackMaxObjects nodes — heavier than
// rippled's diff. go-xrpl builds the pack inline (no jtPACK job queue to bound),
// so the send-queue back-pressure gate in handleGetObjectsMessage handles
// admission while the provider reports its own busy/stale state at execution.
func (o *Overlay) serveFetchPack(peerID PeerID, req *message.GetObjectByHash) {
	o.serveFetchPackContext(context.Background(), peerID, req)
}

func (o *Overlay) chargeServePeer(peerID PeerID, fee resource.Charge, reason string) {
	if o.ledgerSync != nil {
		o.ledgerSync.mu.RLock()
		charge := o.ledgerSync.chargePeer
		o.ledgerSync.mu.RUnlock()
		if charge != nil {
			charge(peerID, fee, reason)
			return
		}
	}
	if peer, ok := o.getPeer(peerID); ok {
		peer.Charge(fee, reason)
	}
}

func (o *Overlay) serveFetchPackContext(ctx context.Context, peerID PeerID, req *message.GetObjectByHash) {
	if err := ctx.Err(); err != nil {
		return
	}
	if len(req.LedgerHash) != 32 {
		o.chargeServePeer(peerID, resource.FeeMalformedRequest(), "fetch pack ledger hash")
		return
	}

	peer, exists := o.getPeer(peerID)
	if !exists {
		return
	}
	var haveHash [32]byte
	copy(haveHash[:], req.LedgerHash)

	// maxObjects=0 lets the provider apply its own per-pack cap.
	objects, err := o.ledgerSync.MakeFetchPackContext(ctx, haveHash, 0)
	if errors.Is(err, ErrFetchPackBusy) {
		slog.Debug("fetch-pack build busy", "t", "Overlay", "peer", peerID)
		return
	}
	// Charge only after the provider has passed its local busy/stale guard.
	// Unknown ledgers and other unavailable outcomes still incur the heavy
	// request cost followed by the protocol's no-reply charge.
	o.chargeServePeer(peerID, resource.FeeHeavyBurdenPeer(), "fetch pack request")
	if err != nil {
		if errors.Is(err, ErrFetchPackTooEarly) || errors.Is(err, ErrFetchPackOpen) {
			o.chargeServePeer(peerID, resource.FeeMalformedRequest(), "fetch pack malformed request")
		} else if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			o.chargeServePeer(peerID, resource.FeeRequestNoReply(), "fetch pack request unavailable")
		}
		slog.Debug("fetch-pack build failed",
			"t", "Overlay", "peer", peerID, "err", err)
		return
	}
	if len(objects) == 0 {
		o.chargeServePeer(peerID, resource.FeeRequestNoReply(), "fetch pack request unavailable")
		return
	}
	objects = limitIndexedObjectsToReplyBudget(objects, req.LedgerHash)
	if len(objects) == 0 {
		return
	}

	reply := &message.GetObjectByHash{
		ObjType:    message.ObjectTypeFetchPack,
		Query:      false,
		LedgerHash: append([]byte(nil), req.LedgerHash...),
		Objects:    objects,
	}
	if ctx.Err() != nil {
		return
	}
	frame, err := message.EncodeFrame(reply)
	if err != nil || len(frame) > message.MaxMessageSize {
		o.chargeServePeer(peerID, resource.FeeRequestNoReply(), "fetch pack response oversized")
		return
	}
	if err := peer.SendPriority(frame); err != nil {
		slog.Debug("fetch-pack priority send failed", "t", "Overlay", "peer", peerID, "err", err)
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
	if o.txRecordProviderSnapshot() == nil && o.txProviderSnapshot() == nil {
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
		if _, present := o.lookupTxRecord(hash); present {
			// Rippled removes this hash from the peer's deferred queue when the
			// peer confirms it already has the transaction.
			if peer, exists := o.getPeer(evt.PeerID); exists {
				peer.removeTxQueue(hash)
			}
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
	peer, exists := o.getPeer(evt.PeerID)
	if !exists {
		return
	}
	encodeAndSend(peer, req, "TMGetObjectByHash request")
}

// endpointsIngestMaxEntries bounds an inbound TMEndpoints frame.
// Mirrors rippled PeerImp.cpp:1206 — a frame at or above this count is
// rejected wholesale and the peer charged for useless data.
const endpointsIngestMaxEntries = 1024

// endpointsIngestSampleMax bounds how many addresses a single accepted
// TMEndpoints frame may contribute to Discovery. Mirrors rippled's
// PeerFinder numberOfEndpointsMax (Tuning.h:116, Logic.h:792-796): a
// larger accepted set is shuffled and truncated to this many before
// ingest, so one frame cannot enqueue an unbounded address batch even
// while staying under the 1024 wholesale-reject bound.
const endpointsIngestSampleMax = 64

// isValidGossipAddress mirrors rippled PeerFinder is_valid_address
// (Logic.h): an address advertised in TMEndpoints gossip is usable only
// if it is specified, not loopback, publicly routable, and carries a
// non-zero port. The is_loopback check is redundant with isPublicIP but
// kept for structural fidelity with rippled.
func isValidGossipAddress(host net.IP, port uint16) bool {
	if host == nil || host.IsUnspecified() {
		return false
	}
	if host.IsLoopback() {
		return false
	}
	if !isPublicIP(host) {
		return false
	}
	return port != 0
}

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
//
// The surviving set is then bounded like rippled's PeerFinder before it
// reaches Discovery: sampled down to numberOfEndpointsMax (64), gated by
// the per-peer secondsPerMessage rate-limit (one accepted frame per
// window), and clipped to our discovery horizon (hops<=MaxHops). Without
// these an announced address stream would grow d.peers without bound
// (issue #1170).
func (o *Overlay) handleEndpointsMessage(evt Event) {
	peer, exists := o.getPeer(evt.PeerID)
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
		reason := "endpoints-decode"
		if errors.Is(err, message.ErrWireLimit) {
			reason = wirePreflightChargeReason(err)
		}
		o.IncPeerBadData(evt.PeerID, reason)
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
	type ingestEndpoint struct {
		address string
		hops    uint32
	}
	accepted := make([]ingestEndpoint, 0, len(eps.EndpointsV2))
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
		host := parsed.Host
		if tm.Hops == 0 {
			// hops==0 describes the sender; trust the socket IP over
			// the self-reported host (PeerImp.cpp:1234-1235).
			if remoteIP == "" {
				continue
			}
			host = remoteIP
			address = Endpoint{Host: remoteIP, Port: parsed.Port}.String()
		}

		// Discard addresses that aren't publicly routable when endpoint
		// verification is on (rippled PeerFinder is_valid_address, gated
		// on config verifyEndpoints). The check runs on the post-rewrite
		// host so a hops==0 peer behind a private socket is dropped too.
		// A silent drop, not a charge: the address parsed fine, it is
		// just not gossip-worthy.
		if o.cfg.VerifyEndpoints && !isValidGossipAddress(net.ParseIP(host), parsed.Port) {
			continue
		}

		accepted = append(accepted, ingestEndpoint{address: address, hops: tm.Hops})
	}

	// rippled hands the parsed set to PeerFinder only when at least one
	// entry survived (PeerImp.cpp:1249); an all-malformed frame never
	// touches the rate-limit window.
	if len(accepted) == 0 {
		return
	}

	// Sample down to numberOfEndpointsMax before ingest so an oversized
	// (but sub-1024) frame cannot enqueue its whole batch.
	if len(accepted) > endpointsIngestSampleMax {
		rand.Shuffle(len(accepted), func(i, j int) {
			accepted[i], accepted[j] = accepted[j], accepted[i]
		})
		accepted = accepted[:endpointsIngestSampleMax]
	}

	// Per-peer inbound rate-limit: one accepted frame per window. A frame
	// arriving inside the window is dropped silently (no charge), matching
	// rippled's whenAcceptEndpoints gate.
	if !peer.acceptEndpoints() {
		return
	}

	for _, e := range accepted {
		// Drop endpoints beyond our discovery horizon rather than storing
		// addresses SelectPeersToConnect would never dial; mirrors
		// rippled's hops>maxHops preprocess drop.
		if e.hops > MaxHops {
			continue
		}
		o.discovery.AddPeer(e.address, e.hops, evt.PeerID)
	}
}

// serveDoTransactions answers an inbound mtGET_OBJECTS query whose
// type is otTRANSACTIONS. Mirrors rippled PeerImp::doTransactions
// (PeerImp.cpp:2787-2839): walk the requested hashes, look each up,
// build a TMTransactions reply containing the blobs we have, and
// emit it. Hashes we don't have are charged feeMalformedRequest and
// abort the reply, matching rippled's doTransactions path.
func (o *Overlay) serveDoTransactions(peerID PeerID, req *message.GetObjectByHash) {
	o.serveDoTransactionsContext(context.Background(), peerID, req)
}

func (o *Overlay) serveDoTransactionsContext(ctx context.Context, peerID PeerID, req *message.GetObjectByHash) {
	if err := ctx.Err(); err != nil {
		return
	}
	const maxQueueSize = peerTxQueueMax
	if len(req.Objects) == 0 {
		return
	}
	if len(req.Objects) > maxQueueSize {
		o.chargeMalformedTransactionRequest(peerID, "get-objects-txn-too-big")
		return
	}
	if o.txRecordProviderSnapshot() == nil && o.txProviderSnapshot() == nil {
		// Negotiated tx-reduce-relay but no lookup wired — silently
		// drop. An operator who flipped EnableTxReduceRelay but
		// hasn't wired a transaction provider would otherwise spam this log.
		return
	}

	reply := &message.Transactions{
		Transactions: make([]message.Transaction, 0, len(req.Objects)),
	}
	budget := newServeReplyBudget()
	for _, obj := range req.Objects {
		if ctx.Err() != nil {
			return
		}
		if len(obj.Hash) != 32 {
			o.chargeMalformedTransactionRequest(peerID, "get-objects-txn-hashsize")
			return
		}
		var hash [32]byte
		copy(hash[:], obj.Hash)
		record, ok := o.lookupTxRecord(hash)
		if !ok {
			o.chargeMalformedTransactionRequest(peerID, "get-objects-txn-missing")
			return
		}
		if !budget.reserve(serveReplyTransactionOverhead, record.RawTransaction) {
			break
		}
		status := record.Status
		if status == 0 {
			status = message.TxStatusCurrent
		}
		receiveTimestamp := uint64(protocol.RippleSeconds(time.Now()))
		reply.Transactions = append(reply.Transactions, message.Transaction{
			RawTransaction:   append([]byte(nil), record.RawTransaction...),
			Status:           status,
			ReceiveTimestamp: receiveTimestamp,
			Deferred:         record.Deferred,
		})
	}
	if len(reply.Transactions) == 0 {
		return
	}

	peer, exists := o.getPeer(peerID)
	if !exists {
		return
	}
	encodeAndSendPriority(peer, reply, "TMTransactions reply")
}

func (o *Overlay) chargeMalformedTransactionRequest(peerID PeerID, reason string) {
	peer, ok := o.getPeer(peerID)
	if ok {
		peer.Charge(resource.FeeMalformedRequest(), reason)
	}
}

func (o *Overlay) lookupTxRecord(hash [32]byte) (TxRecord, bool) {
	if provider := o.txRecordProviderSnapshot(); provider != nil {
		record, ok := provider(hash)
		if ok {
			record.RawTransaction = append([]byte(nil), record.RawTransaction...)
		}
		return record, ok
	}
	provider := o.txProviderSnapshot()
	if provider == nil {
		return TxRecord{}, false
	}
	blob, ok := provider(hash)
	if !ok {
		return TxRecord{}, false
	}
	return TxRecord{
		RawTransaction: append([]byte(nil), blob...),
		Status:         message.TxStatusCurrent,
	}, true
}

// hardMaxReplyNodes bounds a single generic by-hash request. Mirrors
// rippled Tuning::kHardMaxReplyNodes: each requested object costs a
// NodeStore fetch, so an unbounded query is a per-object fetch DoS. A
// request carrying more than this many objects is rejected outright
// (feeInvalidData); the fetch loop is capped at the same value as
// defense in depth.
const hardMaxReplyNodes = 12288

// Differential pricing for generic get-object-by-hash requests, mirroring
// rippled 3.2.0's Tuning.h get-object constants. The first
// getObjFreeObjects requested objects are free; beyond that each billable
// object costs getObjCostPerHit (store hit) or getObjCostPerMiss (store
// miss, billed first) plus a one-shot size-band surcharge. Tuned so a
// single all-miss max-size request exceeds resource.DropThreshold in one
// message while an honest 8-hash request stays free.
const (
	getObjFreeObjects    = 16
	getObjCostPerHit     = 1
	getObjCostPerMiss    = 8
	getObjBandSmallMax   = 4 * 16 // 4 legit hashes/type * SHAMap branch factor
	getObjBandMediumMax  = getObjBandSmallMax * 16
	getObjBandSmallCost  = 0
	getObjBandMediumCost = 100
	getObjBandLargeCost  = 1000
)

// getObjectByHashFee computes the work-proportional charge for a served
// generic get-object-by-hash request. Misses are billed ahead of hits, so
// a request full of non-existent hashes is the most expensive. The request
// size (not the number of lookups actually performed) drives the charge,
// to discourage large speculative requests.
func getObjectByHashFee(requested, found int) resource.Charge {
	billable := requested - getObjFreeObjects
	if billable < 0 {
		billable = 0
	}
	billableMisses := requested - found
	if billableMisses < 0 {
		billableMisses = 0
	}
	if billableMisses > billable {
		billableMisses = billable
	}
	billableHits := billable - billableMisses

	band := getObjBandSmallCost
	switch {
	case requested > getObjBandMediumMax:
		band = getObjBandLargeCost
	case requested > getObjBandSmallMax:
		band = getObjBandMediumCost
	}

	dynamic := billableHits*getObjCostPerHit + billableMisses*getObjCostPerMiss + band
	return resource.NewCharge(dynamic, "GetObject differential")
}

// serveGetObjects answers an inbound mtGET_OBJECTS query for generic
// node-store objects by hash. Mirrors rippled
// PeerImp::onMessage(TMGetObjectByHash) generic branch
// (PeerImp.cpp:2483-2538): echo the request's type/ledger-hash into
// a query=false reply, look each requested hash up in the local node
// store, and append the blobs we hold.
//
// Unlike serveDoTransactions, this path ALWAYS sends a reply — even an
// empty one — mirroring rippled's unconditional send at
// PeerImp.cpp:2538 so a requester polling several peers can tell "I
// don't have these" from a peer that never answered.
func (o *Overlay) serveGetObjects(peerID PeerID, req *message.GetObjectByHash) {
	if o.nodeObjectProviderSnapshot() != nil &&
		(len(req.LedgerHash) == 0 || len(req.LedgerHash) == 32) &&
		len(req.Objects) <= hardMaxReplyNodes {
		if peer, ok := o.getPeer(peerID); ok {
			peer.Charge(resource.FeeModerateBurdenPeer(), "get object by hash request")
		}
	}
	o.serveGetObjectsContext(context.Background(), peerID, req)
}

func (o *Overlay) serveGetObjectsContext(ctx context.Context, peerID PeerID, req *message.GetObjectByHash) {
	if err := ctx.Err(); err != nil {
		return
	}
	peer, exists := o.getPeer(peerID)
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
		peer.Charge(resource.FeeMalformedRequest(), "get object ledger hash")
		return
	}

	// Reject oversized requests before touching the node store. The
	// legitimate upper bound (an InboundLedger asks for at most 8 hashes)
	// is far below this cap; anything past it is non-conforming and is
	// charged feeInvalidData (PeerImp.cpp:2500-2506).
	if len(req.Objects) > hardMaxReplyNodes {
		peer.Charge(resource.FeeInvalidData(), "oversized get object request")
		return
	}

	// Base burden for a legitimate by-hash request, charged ahead of the
	// fetch loop; the work-proportional differential is added afterwards.
	// Rippled charges the base at admission in onMessage and the
	// differential in the worker (PeerImp.cpp:2544, 2656).
	reply := &message.GetObjectByHash{
		Query:   false,
		ObjType: req.ObjType,
		Objects: make([]message.IndexedObject, 0, len(req.Objects)),
	}
	if len(req.LedgerHash) != 0 {
		reply.LedgerHash = append([]byte(nil), req.LedgerHash...)
	}
	budget := newServeReplyBudget()
	if !budget.reserve(serveReplyObjectOverhead, req.LedgerHash) {
		return
	}

	// Defense in depth: the oversize gate above already rejects requests
	// larger than hardMaxReplyNodes, but cap the fetch loop at the same
	// value so a future caller invoking this directly can't drive
	// unbounded node-store lookups (PeerImp.cpp:2622-2623).
	requested := len(req.Objects)
	iterLimit := requested
	if iterLimit > hardMaxReplyNodes {
		iterLimit = hardMaxReplyNodes
	}
	for i := 0; i < iterLimit; i++ {
		if ctx.Err() != nil {
			return
		}
		obj := req.Objects[i]
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
		if !budget.reserve(serveReplyObjectOverhead, obj.Hash, obj.NodeID, blob) {
			break
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

	// Work-proportional differential charge on top of the base burden:
	// billed per requested object beyond the free tier (misses first) plus
	// a size-band surcharge, discouraging large speculative requests
	// (computeGetObjectByHashFee, PeerImp.cpp:2656).
	peer.Charge(getObjectByHashFee(requested, len(reply.Objects)), "processed get object by hash request")

	encodeAndSendPriority(peer, reply, "TMGetObjectByHash reply")
}

// handleTransactionsBatchMessage processes mtTRANSACTIONS (a batched
// list of TMTransaction frames). Mirrors rippled
// PeerImp::onMessage(TMTransactions) at PeerImp.cpp:2667-2688.
//
// Each inner TMTransaction is fanned out onto the tx lane
// (o.txMessages) carrying its already-decoded form so
// router.handleTransaction processes it identically to an unbundled
// TMTransaction frame. Like rippled, which
// hands the decoded inner straight to handleTransaction, we never
// re-serialize: the decode happened once when the batch was parsed.
// The batched path shares the same per-peer deferred queue as the unbatched
// path; HAVE_TRANSACTIONS acknowledgements remove queued hashes before this
// handler forwards the batch to the transaction router.
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

	// Fan out each inner TMTransaction onto the tx lane so the router's
	// handleTransaction path picks it up. The decoded transaction rides
	// along in Tx, so the router need not re-serialize and re-parse it —
	// the batch decode above already produced the decoded form. The lane
	// is shared with the wire path, so batch frames are subject to the
	// same MaxTransactions ceiling and jq_trans_overflow accounting.
	for i := range batch.Transactions {
		inbound := evt.retainedInboundMessage()
		inbound.Type = message.TypeTransaction
		inbound.Payload = nil
		inbound.Tx = &batch.Transactions[i]
		o.forwardTransaction(inbound)
	}
}
