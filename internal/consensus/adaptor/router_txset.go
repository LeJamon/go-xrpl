package adaptor

import (
	"errors"
	"fmt"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type txSetAcquireState struct {
	txMap      *shamap.SHAMap
	startedAt  time.Time
	lastUpdate time.Time

	// Retry bookkeeping. lastRequest is when we most recently broadcast a
	// RequestTxSetMissingNodes. attempts is pure telemetry (the broadcast
	// count surfaced in logs). stallTicks counts CONSECUTIVE no-progress
	// timer ticks — the give-up signal — and is reset to 0 whenever an
	// inbound reply makes progress. peerNonProgress tracks consecutive
	// TMLedgerData responses from a peer that failed to extend the SHAMap;
	// peers at or over the per-peer threshold are skipped during the next
	// broadcast.
	lastRequest     time.Time
	attempts        int
	stallTicks      int
	peerNonProgress map[uint64]int

	// timedOut latches once the stall timer (retryStalledTxSetAcquires) has
	// re-triggered this acquisition — go-xrpl's analogue of rippled's
	// TransactionAcquire timeouts_ != 0. Once set, every subsequent
	// missing-nodes request (inbound or timer) is sent indirect
	// (query_type=qtINDIRECT) so peers relay it on our behalf.
	timedOut bool

	// dormant latches once stallTicks reaches MaxStallTicks: the timer stops
	// actively re-requesting, but the partial SHAMap is RETAINED so a later
	// MarkTxSetStillNeeded resumes the acquire from where it left off. The TTL
	// sweep still reclaims a truly-abandoned entry.
	dormant bool

	// haveRoot latches once the real root node for this tx-set hash has been
	// installed (mirrors rippled TransactionAcquire::mHaveRoot). A fresh
	// shamap.New carries a non-nil but EMPTY root, which would let an empty
	// tree "complete" with zero leaves; until haveRoot is set the acquire only
	// requests the root and can never complete.
	haveRoot bool

	// done latches a terminal acquire: completed (set built and fed to the
	// engine) or given-up (dormant past MaxStallTicks). A data reply for a
	// done acquire is dropped so a straggler can neither recreate a fresh empty
	// map nor fan out re-requests; MarkTxSetStillNeeded clears it to revive a
	// genuinely-needed set. Mirrors rippled's complete_/failed_ latches.
	done bool
}

// 60s covers a consensus round (~15s) plus retries with margin while
// bounding memory under a stalled consumer.
const txSetAcquireTTL = 60 * time.Second

// txSetRetryKnobs collects the tunable parameters of the tx-set acquire
// retry loop. The inbound path pipelines a re-request on every
// progressing reply (rate-limited by the RTT itself); the 250ms timer
// drives stalled acquires and owns the give-up decision.
//
//   - MinInterval: minimum spacing between successive TIMER broadcasts
//     for the same acquisition (250ms). An actively-progressing acquire
//     keeps lastRequest fresh, so the timer stays out of its way.
//   - MaxStallTicks: consecutive no-progress timer ticks before an
//     acquire goes dormant (20 ≈ 5s of continuous silence). Any
//     progressing inbound reply resets the counter, so this only fires
//     when a set is genuinely un-servable, not merely slow.
//   - PeerNonProgressThreshold: consecutive non-progressing
//     TMLedgerData replies from one peer before it is skipped on the
//     next broadcast. 3 is small enough to react quickly to a truly
//     stuck peer and large enough to ride out a transient empty reply.
type txSetRetryKnobs struct {
	MinInterval              time.Duration
	MaxStallTicks            int
	PeerNonProgressThreshold int
}

func defaultTxSetRetryKnobs() txSetRetryKnobs {
	return txSetRetryKnobs{
		MinInterval:              250 * time.Millisecond,
		MaxStallTicks:            20,
		PeerNonProgressThreshold: 3,
	}
}

// SetTxSetRetryKnobsForTest overrides the tx-set retry knobs on this
// Router. Tests use it to dial timings down so they don't sleep for the
// production throttle window. The lock matches the read in handleTxSetData
// so racing this against an active inbox goroutine is safe under -race;
// production is not expected to call this.
func (r *Router) SetTxSetRetryKnobsForTest(knobs txSetRetryKnobs) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	r.txSetRetryKnobs = knobs
}

// learnTxFromLeaf submits the transaction carried by an acquired tx-set
// leaf into the open-ledger pool and, on acceptance, actively relays it.
// A tx-set leaf is a tnTRANSACTION_NM node whose wire form is
// `tx_blob || WireTypeTransaction`; inner nodes and malformed data are
// skipped by the trailing-type-byte check, and a tx the open ledger already
// holds is not resubmitted. The submit is peer-sourced and the relay reuses
// relayTransaction exactly as handleTransaction does for an inbound
// TMTransaction (see handleTransaction), excluding originPeer as the tx's
// source — so a set the node only holds transiently still pushes its novel
// txs to peers instead of relying on the slower TMHaveTransactions announce.
//
// We deliberately do not keep a per-acquisition node cache: the acquired
// node is already held in txMap and the missing-leaf local-fill (below)
// re-sources tx leaves from the open-ledger pool, so a cache has no role
// here.
func (r *Router) learnTxFromLeaf(originPeer uint64, wire []byte) {
	if len(wire) < 2 || wire[len(wire)-1] != protocol.WireTypeTransaction {
		return
	}
	leaf, err := shamap.NewTransactionLeafFromWire(wire)
	if err != nil {
		return
	}
	item := leaf.Item()
	if item == nil {
		return
	}
	if r.adaptor.HasTx(consensus.TxID(item.Key())) {
		return
	}
	if res, err := r.adaptor.SubmitPendingTx(item.Data(), false); err == nil && res == openledger.ResultSuccess {
		r.relayTransaction(peermanagement.PeerID(originPeer), item.Data())
	}
}

// txLeafWire frames a raw transaction blob as a SHAMap transaction-leaf
// node: `tx_blob || WireTypeTransaction`. shamap.NewTransactionLeafFromWire
// and the DeserializeNodeFromWire dispatch are the inverse.
func txLeafWire(blob []byte) []byte {
	wire := make([]byte, len(blob)+1)
	copy(wire, blob)
	wire[len(blob)] = byte(protocol.WireTypeTransaction)
	return wire
}

// buildExcludedPeers returns the set of peer IDs whose consecutive
// non-progress count has reached threshold, so the next missing-nodes
// request can route around them. Returns nil when none qualify (a nil
// map is a valid empty exclusion set for RequestTxSetMissingNodes).
func buildExcludedPeers(peerNonProgress map[uint64]int, threshold int) map[uint64]bool {
	var excluded map[uint64]bool
	for pid, count := range peerNonProgress {
		if count >= threshold {
			if excluded == nil {
				excluded = make(map[uint64]bool)
			}
			excluded[pid] = true
		}
	}
	return excluded
}

// missingNodeIDs projects the wire NodeID bytes of every still-missing
// SHAMap node, in order, for RequestTxSetMissingNodes.
func missingNodeIDs(missing []shamap.MissingNode) [][]byte {
	nodeIDs := make([][]byte, len(missing))
	for i, m := range missing {
		nodeIDs[i] = m.NodeID.Bytes()
	}
	return nodeIDs
}

// handleTxSetData consumes a TMLedgerData{type=liTS_CANDIDATE} response.
// Each node is a SHAMap node (root/inner/leaf), not a raw transaction.
// It accumulates nodes across responses, then either finishes (→
// engine.OnTxSet) or requests missing nodes. State is keyed by tx-set ID
// so partial responses can resume.
//
// originPeer is the peer ID of the sender, used to attribute non-progress
// for per-peer de-prioritization.
func (r *Router) handleTxSetData(ld *message.LedgerData, originPeer uint64) {
	if len(ld.LedgerHash) != 32 {
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], ld.LedgerHash)

	r.txSetAcquireMu.Lock()
	state, exists := r.txSetAcquire[txSetID]
	if exists && state.done {
		// Terminal acquire (completed or given-up): drop the straggler so it
		// can neither recreate a fresh empty map nor fan out re-requests. Only
		// MarkTxSetStillNeeded revives a genuinely-needed set.
		r.txSetAcquireMu.Unlock()
		return
	}
	if !exists {
		txMap := shamap.New(shamap.TypeTransaction)
		if err := txMap.StartSync(); err != nil {
			r.txSetAcquireMu.Unlock()
			r.logger.Info("tx-set sync: StartSync failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
			return
		}
		state = &txSetAcquireState{
			txMap:           txMap,
			startedAt:       time.Now(),
			peerNonProgress: make(map[uint64]int),
		}
		r.txSetAcquire[txSetID] = state
	}
	state.lastUpdate = time.Now()
	r.sweepStaleTxSetAcquireLocked()
	txMap := state.txMap
	haveRoot := state.haveRoot
	r.txSetAcquireMu.Unlock()

	// Root NodeID is 33 zero bytes. AddRootNode is idempotent
	// (ErrRootAlreadySet treated as success). rootAccepted feeds
	// per-peer progress accounting so a peer whose reply contains
	// only the root still counts as making progress — any successful
	// add (root or non-root) is useful.
	rootAccepted := false
	for _, node := range ld.Nodes {
		if !isShamapRootNodeID(node.NodeID) {
			continue
		}
		err := txMap.AddRootNode([32]byte(txSetID), node.NodeData)
		switch {
		case err == nil, errors.Is(err, shamap.ErrRootAlreadySet):
			rootAccepted = true
		default:
			r.logger.Info("tx-set sync: AddRootNode failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
		}
		break
	}
	if rootAccepted && !haveRoot {
		haveRoot = true
		r.txSetAcquireMu.Lock()
		state.haveRoot = true
		r.txSetAcquireMu.Unlock()
	}

	// A hash-bound map with no root is not valid and must never complete
	// (mirrors mHaveRoot + isValid). Until the root arrives, only request it —
	// do NOT descend non-root nodes, FinishSync, complete, or delete. The fetch
	// goes to the replying peer (unicast); the timer broadcasts as fallback.
	if !haveRoot {
		r.txSetAcquireMu.Lock()
		indirect := state.timedOut
		state.lastRequest = time.Now()
		r.txSetAcquireMu.Unlock()
		if err := r.requestTxSetRoot(txSetID, originPeer, indirect); err != nil {
			r.logger.Info("tx-set sync: root request failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
		}
		r.logger.Debug("tx-set sync: awaiting root before completion",
			"t", "consensus", "event", "txset-await-root",
			"txset", fmt.Sprintf("%x", txSetID[:8]))
		return
	}

	// Use NodeID-based placement: path-based descent works when the
	// peer's response contains nodes deeper than our currently-loaded
	// layer of stubs. The previous hash-search approach
	// (AddKnownNodeUnchecked) silently rejected every node it couldn't
	// place beneath a loaded parent, producing the missing-nodes retry
	// storm of issue #413.
	//
	// replyValid stays true unless any non-root node fails parse or
	// AddKnownNodeByID. A provably-invalid non-root stops harvesting the
	// rest of the reply (stop-on-first-bad) and flags the whole reply as
	// non-progress, so a peer trickling junk alongside one good node can't
	// keep its counter pinned at zero. Only a fresh attach (NodeUseful) is
	// progress: a duplicate re-send of fat nodes must not keep the pipeline
	// firing.
	added := 0
	replyValid := true
	for _, node := range ld.Nodes {
		if isShamapRootNodeID(node.NodeID) {
			continue
		}
		if len(node.NodeData) == 0 {
			continue
		}
		parsedID, err := shamap.UnmarshalBinary(node.NodeID)
		if err != nil {
			replyValid = false
			r.logger.Debug("tx-set sync: malformed node ID",
				"t", "consensus", "event", "txset-node-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			continue
		}
		res, err := txMap.AddKnownNodeByID(parsedID, node.NodeData)
		if res == shamap.NodeReRequest {
			// Ahead of its frontier: re-requested by the next getMissingNodes
			// walk, not a poisoned reply.
			continue
		}
		if res == shamap.NodeInvalid {
			replyValid = false
			r.logger.Debug("tx-set sync: node rejected",
				"t", "consensus", "event", "txset-node-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_id", fmt.Sprintf("%x", node.NodeID),
				"node_data_len", len(node.NodeData),
				"error", err)
			break
		}
		if res == shamap.NodeDuplicate {
			// Slot already populated: nothing added, so not progress.
			continue
		}
		added++
		// Learn (submit + relay) the tx carried by this acquired leaf.
		r.learnTxFromLeaf(originPeer, node.NodeData)
	}

	if err := txMap.FinishSync(); err != nil {
		// Request the missing nodes. Before going to peers, fill any
		// missing TX-leaf nodes from our own pending pool. For
		// tnTRANSACTION_NM the leaf-node hash equals the tx ID, so a
		// single tx-ID lookup resolves the leaf. This is a round-trip
		// optimization, not a correctness requirement: a peer DOES return
		// a leaf requested directly by node ID, but local sourcing avoids
		// the extra request for a tx we already relayed.
		missing := txMap.GetMissingNodes(256, nil)
		if len(missing) == 0 {
			// Root present but the map is inconsistent: terminal. Latch done
			// and KEEP the entry (TTL reclaims it); MarkTxSetStillNeeded can
			// revive it. Deleting would let the next straggler recreate a fresh
			// empty acquire.
			r.markTxSetDone(txSetID)
			r.logger.Info("tx-set sync: stuck",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"err", err.Error())
			return
		}

		filledFromPool := r.fillTxSetFromLocalPool(txMap, missing)
		var remaining []shamap.MissingNode
		if filledFromPool > 0 {
			if syncErr := txMap.FinishSync(); syncErr == nil {
				// Tree complete after local fill — remaining stays empty,
				// falling through to the leaf-walk + engine feed below.
				r.logger.Info("tx-set sync: completed via local pool",
					"t", "consensus", "event", "txset-local-fill",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"filled", filledFromPool,
					"non_root_added", added,
				)
			} else {
				// Still incomplete — recompute remaining via the SHAMap
				// since AddKnownNode may have revealed deeper holes.
				remaining = txMap.GetMissingNodes(256, nil)
			}
		} else {
			// Nothing sourced locally; every node is still missing.
			remaining = missing
		}

		if len(remaining) > 0 {
			// A reply counts as progress only when it was non-empty AND
			// every non-root node parsed and added cleanly. Any single bad
			// non-root → invalid reply → not progress for the peer.
			madeProgress := replyValid && (added > 0 || rootAccepted)
			r.txSetAcquireMu.Lock()
			// Knobs are read under txSetAcquireMu so a concurrent
			// SetTxSetRetryKnobsForTest (test-only API) can't tear a
			// half-updated struct into the hot path.
			knobs := r.txSetRetryKnobs
			if !madeProgress {
				// No progress: do NOT re-request inline — that would let a
				// junk/empty-reply peer amplify into a broadcast storm. Just
				// charge the peer and let the 250ms stall timer drive it.
				if originPeer != 0 {
					state.peerNonProgress[originPeer]++
				}
				r.txSetAcquireMu.Unlock()
				r.logger.Debug("tx-set sync: no-progress reply, deferring to timer",
					"t", "consensus", "event", "txset-retry-defer",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"missing", len(remaining),
				)
				return
			}
			// Progress: pipeline the next missing-nodes request IMMEDIATELY.
			// The RTT itself rate-limits (one re-request per received reply),
			// so there is no storm and no MinInterval gate is needed. A fresh
			// lastRequest keeps the stall timer out of an actively-progressing
			// acquire; reset stallTicks and un-dormant so a resumed acquire
			// keeps pipelining. Give-up lives only on the timer.
			if originPeer != 0 {
				state.peerNonProgress[originPeer] = 0
			}
			state.dormant = false
			state.stallTicks = 0
			state.attempts++
			state.lastRequest = time.Now()
			excluded := buildExcludedPeers(state.peerNonProgress, knobs.PeerNonProgressThreshold)
			attempts := state.attempts
			indirect := state.timedOut
			r.txSetAcquireMu.Unlock()

			nodeIDs := missingNodeIDs(remaining)
			r.logger.Info("tx-set sync: requesting missing nodes",
				"t", "consensus", "event", "txset-retry",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"non_root_added", added,
				"filled_local", filledFromPool,
				"missing", len(remaining),
				"attempts", attempts,
				"excluded_peers", len(excluded),
				"indirect", indirect,
			)
			// Pipeline UNICAST to the replying peer (RTT rate-limits, no storm);
			// the 250ms timer owns the broadcast fallback for silent peers.
			if reqErr := r.requestTxSetMissingNodesUnicast(txSetID, nodeIDs, originPeer, excluded, indirect); reqErr != nil {
				r.logger.Info("tx-set sync: missing-nodes request failed",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"error", reqErr.Error())
			}
			return
		}
		// fall through: tree complete via local fill
	}

	// Walk leaves into blobs, feed the engine, then latch the acquire done and
	// KEEP it: stragglers for the finished set are dropped (see the top of
	// handleTxSetData) rather than recreating a fresh empty acquire. The TTL
	// sweep reclaims it.
	blobs := make([][]byte, 0, added+1)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	r.markTxSetDone(txSetID)

	r.logger.Info("received tx-set from peer",
		"t", "consensus", "event", "txset-recv",
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"node_count", len(ld.Nodes),
		"tx_count", len(blobs))

	if len(blobs) == 0 {
		return
	}

	r.submitTxSetToEngine(txSetID, blobs)
}

func (r *Router) deleteTxSetAcquire(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	delete(r.txSetAcquire, txSetID)
	r.txSetAcquireMu.Unlock()
}

// markTxSetDone latches a tx-set acquire terminal (completed or given-up) and
// KEEPS it, so later data replies are dropped rather than recreating a fresh
// empty acquire. The TTL sweep reclaims it; MarkTxSetStillNeeded clears the
// latch to revive a genuinely-needed set. No-op if the entry is gone.
func (r *Router) markTxSetDone(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	if state, ok := r.txSetAcquire[txSetID]; ok {
		state.done = true
	}
	r.txSetAcquireMu.Unlock()
}

// requestTxSetRoot (re)fetches the SHAMap root (the 33-byte zero node ID) of a
// tx-set. It unicasts to the replying peer when known (mirrors rippled
// trigger(peer) with !mHaveRoot), falling back to a broadcast when the origin
// is unknown. Both paths skip Adaptor.RequestTxSet so onTxSetRequested
// (MarkTxSetStillNeeded) does not reset the acquire's stall bookkeeping.
func (r *Router) requestTxSetRoot(txSetID consensus.TxSetID, originPeer uint64, indirect bool) error {
	rootID := [][]byte{make([]byte, shamap.NodeIDSize)}
	if originPeer != 0 {
		return r.adaptor.RequestTxSetMissingNodesFromPeer(txSetID, rootID, originPeer, indirect)
	}
	return r.adaptor.RequestTxSetMissingNodes(txSetID, rootID, nil, indirect)
}

// requestTxSetMissingNodesUnicast pipelines the next missing-nodes request to
// the single replying peer. It falls back to a filtered broadcast only when the
// origin is unknown (peerID 0, e.g. tests); the excluded set applies solely to
// that fallback, being irrelevant to a unicast.
func (r *Router) requestTxSetMissingNodesUnicast(txSetID consensus.TxSetID, nodeIDs [][]byte, originPeer uint64, excluded map[uint64]bool, indirect bool) error {
	if originPeer != 0 {
		return r.adaptor.RequestTxSetMissingNodesFromPeer(txSetID, nodeIDs, originPeer, indirect)
	}
	return r.adaptor.RequestTxSetMissingNodes(txSetID, nodeIDs, excluded, indirect)
}

// submitTxSetToEngine feeds a completed tx-set's blobs to the engine. An
// engine rejection is logged, not fatal — the engine re-checks the tx-set
// ID, so a stale or duplicate set is dropped rather than corrupting state.
func (r *Router) submitTxSetToEngine(txSetID consensus.TxSetID, blobs [][]byte) {
	if err := r.engine.OnTxSet(txSetID, blobs); err != nil {
		r.logger.Info("engine rejected tx-set",
			"t", "consensus", "event", "txset-reject",
			"error", err.Error(),
			"txset", fmt.Sprintf("%x", txSetID[:8]),
			"tx_count", len(blobs))
	}
}

// fillTxSetFromLocalPool satisfies as many of the still-missing tx-leaf
// nodes as it can from the local pending-tx pool, returning the number
// filled. For tnTRANSACTION_NM the leaf-node hash equals the tx ID, so a
// single GetTx lookup resolves the leaf — avoiding a peer round-trip for a
// tx we already hold. Shared by the inbound (handleTxSetData) and timer
// (retryStalledTxSetAcquires) paths. The caller owns txMap (both callers
// run on the Run() goroutine) and decides how to recompute the remaining
// set afterward.
func (r *Router) fillTxSetFromLocalPool(txMap *shamap.SHAMap, missing []shamap.MissingNode) int {
	filled := 0
	for _, m := range missing {
		blob, err := r.adaptor.GetTx(consensus.TxID(m.Hash))
		if err != nil || len(blob) == 0 {
			continue
		}
		if addErr := txMap.AddKnownNode(m.Hash, txLeafWire(blob)); addErr == nil {
			filled++
		}
	}
	return filled
}

// MarkTxSetStillNeeded is the active re-arm hook fired every time
// consensus re-asks for a tx-set via Adaptor.RequestTxSet. If an
// in-flight acquisition for this set still exists, its terminal/stall state is
// cleared: a done or dormant acquire wakes, the consecutive-no-progress counter
// resets, and the request-spacing latch is dropped so the next timer tick or
// data reply resumes from the retained partial map instead of waiting out the
// TTL. haveRoot is preserved — a revived acquire keeps any root it already had.
// A no-op if the router has no entry for txSetID. Mirrors rippled's stillNeed
// reset path.
func (r *Router) MarkTxSetStillNeeded(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	state, ok := r.txSetAcquire[txSetID]
	if !ok {
		return
	}
	state.done = false
	state.dormant = false
	state.stallTicks = 0
	state.attempts = 0
	state.lastRequest = time.Time{}
}

// sweepStaleTxSetAcquireLocked drops entries older than txSetAcquireTTL.
// Caller must hold r.txSetAcquireMu.
func (r *Router) sweepStaleTxSetAcquireLocked() {
	cutoff := time.Now().Add(-txSetAcquireTTL)
	for id, state := range r.txSetAcquire {
		if state.lastUpdate.Before(cutoff) {
			delete(r.txSetAcquire, id)
		}
	}
}

// retryStalledTxSetAcquires re-requests the still-missing nodes of any
// in-flight tx-set acquisition whose inbound responses have gone quiet, and
// sweeps entries past their TTL. It fires every 250ms.
//
// The inbound path (handleTxSetData) pipelines a re-request on every
// progressing reply. When a peer falls silent mid-acquire nothing re-requests
// the remaining nodes, so the acquisition would stall until the 60s TTL sweep;
// under load that drops the node into wrongLedger and the mixed network below
// quorum. This timer is the missing driver. Each firing on a stalled acquire
// is a consecutive no-progress tick (stallTicks); past MaxStallTicks the
// acquire goes dormant, RETAINING its partial map rather than deleting it, so
// a later MarkTxSetStillNeeded / progressing reply can resume it. Because the
// inbound path keeps lastRequest fresh while making progress, the MinInterval
// gate keeps this timer out of an actively-progressing acquire and only fires
// once responses stop arriving.
//
// Runs on the Run() message-loop goroutine (same as handleTxSetData), so
// reading state.txMap here never races the inbound path.
func (r *Router) retryStalledTxSetAcquires() {
	now := time.Now()
	type txSetKick struct {
		id       consensus.TxSetID
		nodeIDs  [][]byte
		excluded map[uint64]bool
		attempts int
		missing  int
	}
	type txSetDrop struct {
		id       consensus.TxSetID
		attempts int
		missing  int
	}
	type txSetComplete struct {
		id     consensus.TxSetID
		txMap  *shamap.SHAMap
		filled int
	}
	var kicks []txSetKick
	var drops []txSetDrop
	var completes []txSetComplete
	var rootKicks []consensus.TxSetID

	r.txSetAcquireMu.Lock()
	r.sweepStaleTxSetAcquireLocked()
	knobs := r.txSetRetryKnobs
	for id, state := range r.txSetAcquire {
		// Only re-trigger once the inbound path has been quiet for a full
		// cadence window. An actively progressing acquire keeps
		// lastRequest fresh and is skipped here.
		if !state.lastRequest.IsZero() && now.Sub(state.lastRequest) < knobs.MinInterval {
			continue
		}
		// A dormant acquire has given up actively re-requesting but keeps
		// its partial map for a MarkTxSetStillNeeded resume; the TTL sweep
		// reclaims it if it stays abandoned.
		if state.dormant {
			continue
		}
		if !state.haveRoot {
			// Rootless acquire: its empty root exposes no missing nodes, so
			// GetMissingNodes can't drive it. Re-request the root (broadcast
			// fallback for a silent peer) with the same stall accounting as any
			// stalled re-trigger.
			state.stallTicks++
			if state.stallTicks >= knobs.MaxStallTicks {
				state.dormant = true
				state.done = true
				drops = append(drops, txSetDrop{id: id, attempts: state.attempts, missing: 0})
				continue
			}
			state.attempts++
			state.lastRequest = now
			state.timedOut = true
			rootKicks = append(rootKicks, id)
			continue
		}
		missing := state.txMap.GetMissingNodes(256, nil)
		if len(missing) == 0 {
			// Tree is complete; the next inbound TMLedgerData (or a prior
			// completion path) finalises it. Nothing to request.
			continue
		}
		// Before re-requesting from peers, source any still-missing tx-leaf
		// nodes from our own pending pool — the same local fill the inbound
		// path performs (handleTxSetData). If the pool has grown since the
		// last inbound reply it can complete the tree outright. GetTx is a
		// local pool read and txMap is owned by this (Run) goroutine, so the
		// fill is safe under txSetAcquireMu.
		filled := r.fillTxSetFromLocalPool(state.txMap, missing)
		if filled > 0 {
			_ = state.txMap.FinishSync()
			missing = state.txMap.GetMissingNodes(256, nil)
		}
		if len(missing) == 0 {
			// Completed from the local pool. Feed the engine just like the
			// inbound completion path — peers are silent, so nothing else
			// will. Finalised after the lock is released.
			completes = append(completes, txSetComplete{id: id, txMap: state.txMap, filled: filled})
			continue
		}
		// A firing timer on a stalled acquire IS a consecutive no-progress
		// tick — no inbound reply has reset stallTicks since the last one.
		// Past MaxStallTicks the acquire goes dormant: it RETAINS its partial
		// map (only the TTL sweep or an explicit resume reclaims it) instead
		// of being deleted, so consensus re-asking picks up where it left off.
		state.stallTicks++
		if state.stallTicks >= knobs.MaxStallTicks {
			// Give up: latch dormant AND done so stragglers are dropped, while
			// KEEPING the partial map. Only MarkTxSetStillNeeded revives it; the
			// TTL sweep reclaims it if it stays abandoned.
			state.dormant = true
			state.done = true
			drops = append(drops, txSetDrop{id: id, attempts: state.attempts, missing: len(missing)})
			continue
		}
		state.attempts++
		state.lastRequest = now
		// The timer firing IS the stall signal: latch timedOut so this and
		// every later request for the set escalates to an indirect (relayable)
		// fetch, mirroring rippled's TransactionAcquire timeouts_ != 0 gate.
		state.timedOut = true
		excluded := buildExcludedPeers(state.peerNonProgress, knobs.PeerNonProgressThreshold)
		nodeIDs := missingNodeIDs(missing)
		kicks = append(kicks, txSetKick{id: id, nodeIDs: nodeIDs, excluded: excluded, attempts: state.attempts, missing: len(missing)})
	}
	r.txSetAcquireMu.Unlock()

	for _, c := range completes {
		r.finalizeLocalFilledTxSet(c.id, c.txMap, c.filled)
	}
	for _, d := range drops {
		// Dormant + done, not deleted: the partial map is retained so a later
		// MarkTxSetStillNeeded resumes the acquire, and the TTL sweep reclaims
		// it if it stays abandoned.
		r.logger.Info("tx-set sync: stall limit reached, acquire dormant",
			"t", "consensus", "event", "txset-dormant",
			"txset", fmt.Sprintf("%x", d.id[:8]),
			"attempts", d.attempts,
			"missing", d.missing,
		)
	}
	for _, id := range rootKicks {
		r.logger.Info("tx-set sync: timer root re-request",
			"t", "consensus", "event", "txset-timer-root",
			"txset", fmt.Sprintf("%x", id[:8]),
		)
		// Broadcast the root fetch (post-stall → indirect). Uses the
		// missing-nodes path, not RequestTxSet, so onTxSetRequested does not
		// reset the stall accounting we just advanced.
		if err := r.requestTxSetRoot(id, 0, true); err != nil {
			r.logger.Info("tx-set sync: timer root request failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", id[:8]),
				"error", err.Error())
		}
	}
	for _, k := range kicks {
		r.logger.Info("tx-set sync: timer re-trigger",
			"t", "consensus", "event", "txset-timer-retry",
			"txset", fmt.Sprintf("%x", k.id[:8]),
			"missing", k.missing,
			"attempts", k.attempts,
			"excluded_peers", len(k.excluded),
		)
		// Timer re-triggers are always post-stall, so always indirect.
		if err := r.adaptor.RequestTxSetMissingNodes(k.id, k.nodeIDs, k.excluded, true); err != nil {
			r.logger.Info("tx-set sync: timer missing-nodes request failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", k.id[:8]),
				"error", err.Error())
		}
	}
}

// finalizeLocalFilledTxSet feeds the engine a tx-set whose SHAMap the retry
// timer completed from the local pending pool, mirroring the inbound completion
// path in handleTxSetData. Runs on the Run() message-loop goroutine. The engine
// re-checks the tx-set ID, so a stale or duplicate set is rejected with a log
// rather than corrupting state.
func (r *Router) finalizeLocalFilledTxSet(txSetID consensus.TxSetID, txMap *shamap.SHAMap, filled int) {
	blobs := make([][]byte, 0)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	// Latch done + KEEP the entry so stragglers are dropped rather than
	// recreating a fresh empty acquire; the TTL sweep reclaims it.
	r.markTxSetDone(txSetID)
	if len(blobs) == 0 {
		return
	}
	r.logger.Info("tx-set sync: completed via local pool (timer)",
		"t", "consensus", "event", "txset-local-fill",
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"filled", filled,
		"tx_count", len(blobs))
	r.submitTxSetToEngine(txSetID, blobs)
}

// isShamapRootNodeID matches the SHAMap root wire encoding (33 zero bytes
// = zero path + depth=0).
func isShamapRootNodeID(b []byte) bool {
	if len(b) != shamap.NodeIDSize {
		return false
	}
	for _, by := range b {
		if by != 0 {
			return false
		}
	}
	return true
}
