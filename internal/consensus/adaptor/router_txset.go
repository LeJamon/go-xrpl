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
	// RequestTxSetMissingNodes; attempts counts those broadcasts so we can
	// surface failure after a cap rather than spinning forever.
	// peerNonProgress tracks consecutive TMLedgerData responses from a peer
	// that failed to extend the SHAMap; peers at or over the per-peer
	// threshold are skipped during the next broadcast.
	lastRequest     time.Time
	attempts        int
	peerNonProgress map[uint64]int

	// timedOut latches once the stall timer (retryStalledTxSetAcquires) has
	// re-triggered this acquisition — go-xrpl's analogue of rippled's
	// TransactionAcquire timeouts_ != 0. Once set, every subsequent
	// missing-nodes request (inbound or timer) is sent indirect
	// (query_type=qtINDIRECT) so peers relay it on our behalf.
	timedOut bool
}

// 60s covers a consensus round (~15s) plus retries with margin while
// bounding memory under a stalled consumer.
const txSetAcquireTTL = 60 * time.Second

// txSetRetryKnobs collects the tunable parameters of the tx-set acquire
// retry loop, which is event-driven (one tick per inbound TMLedgerData).
//
//   - MinInterval: minimum spacing between successive broadcasts for
//     the same acquisition (250ms), so a chatty peer can't drive the
//     cadence faster.
//   - MaxAttempts: hard cap on broadcasts per acquisition (20) before
//     the acquire gives up.
//   - PeerNonProgressThreshold: consecutive non-progressing
//     TMLedgerData replies from one peer before it is skipped on the
//     next broadcast. 3 is small enough to react quickly to a truly
//     stuck peer and large enough to ride out a transient empty reply.
type txSetRetryKnobs struct {
	MinInterval              time.Duration
	MaxAttempts              int
	PeerNonProgressThreshold int
}

func defaultTxSetRetryKnobs() txSetRetryKnobs {
	return txSetRetryKnobs{
		MinInterval:              250 * time.Millisecond,
		MaxAttempts:              20,
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
	// keep its counter pinned at zero.
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
			r.deleteTxSetAcquire(txSetID)
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
			// Throttle retries, cap total attempts, and route around
			// peers that have repeatedly failed to extend the SHAMap.
			// Without these guards a non-progressing peer triggers 100+
			// retries/sec until the 60s TTL sweep fires. A reply counts
			// as progress only when it was non-empty AND every non-root
			// node parsed and added cleanly. Any single bad non-root →
			// invalid reply → not progress for the originating peer.
			madeProgress := replyValid && (added > 0 || rootAccepted)
			r.txSetAcquireMu.Lock()
			// Knobs are read under txSetAcquireMu so a concurrent
			// SetTxSetRetryKnobsForTest (test-only API) can't tear a
			// half-updated struct into the hot path.
			knobs := r.txSetRetryKnobs
			if originPeer != 0 {
				if madeProgress {
					state.peerNonProgress[originPeer] = 0
				} else {
					state.peerNonProgress[originPeer]++
				}
			}
			if !state.lastRequest.IsZero() && time.Since(state.lastRequest) < knobs.MinInterval {
				r.txSetAcquireMu.Unlock()
				r.logger.Debug("tx-set sync: retry throttled",
					"t", "consensus", "event", "txset-retry-throttle",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"missing", len(remaining),
				)
				return
			}
			if state.attempts >= knobs.MaxAttempts {
				attempts := state.attempts
				delete(r.txSetAcquire, txSetID)
				r.txSetAcquireMu.Unlock()
				// Drop the entry rather than mark it permanently
				// failed: if consensus is still proposing this set, the
				// next inbound TMLedgerData should start a fresh acquire
				// rather than be silently dropped for the TTL window.
				r.logger.Info("tx-set sync: max attempts exceeded",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"attempts", attempts,
					"missing", len(remaining),
				)
				return
			}
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
			if reqErr := r.adaptor.RequestTxSetMissingNodes(txSetID, nodeIDs, excluded, indirect); reqErr != nil {
				r.logger.Info("tx-set sync: missing-nodes request failed",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"error", reqErr.Error())
			}
			return
		}
		// fall through: tree complete via local fill
	}

	// Walk leaves into blobs, feed the engine, drop the acquire so dispute
	// resolution flipping back to the same set starts fresh.
	blobs := make([][]byte, 0, added+1)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	r.deleteTxSetAcquire(txSetID)

	r.logger.Info("received tx-set from peer",
		"t", "consensus", "event", "txset-recv",
		"txset", fmt.Sprintf("%x", txSetID[:8]),
		"node_count", len(ld.Nodes),
		"tx_count", len(blobs))

	// Duplicate response after a completed acquire — no root, ForEach
	// yields 0 items, engine would fail with "tx set ID mismatch". Drop.
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
// in-flight acquisition for this set still exists, attempts and
// lastRequest are cleared so the next inbound TMLedgerData broadcasts
// immediately instead of being throttled or silently dropped past the
// max-attempts cap. A no-op if the router has no entry for txSetID
// (e.g. first request, or already completed and swept).
func (r *Router) MarkTxSetStillNeeded(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	state, ok := r.txSetAcquire[txSetID]
	if !ok {
		return
	}
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
// The inbound retry (handleTxSetData) only advances on an arriving
// TMLedgerData. When a peer falls silent mid-acquire — or every reply is
// throttled — nothing re-requests the remaining nodes, so the acquisition
// stalls until the 60s TTL sweep; under load that drops the node into
// wrongLedger and the mixed network below quorum. This timer is the missing
// driver. It reuses the same throttle/attempt-cap/peer-exclusion knobs as
// the inbound path so the two never compound into a request storm: because
// the inbound path keeps lastRequest fresh while it is making progress, the
// MinInterval gate keeps this timer out of an actively progressing acquire
// and only fires it once responses stop arriving.
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
		if state.attempts >= knobs.MaxAttempts {
			delete(r.txSetAcquire, id)
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
		// Drop rather than mark failed: if consensus still needs the set,
		// the next inbound TMLedgerData / MarkTxSetStillNeeded starts a
		// fresh acquire (mirrors handleTxSetData's max-attempts handling).
		r.logger.Info("tx-set sync: max attempts exceeded (timer)",
			"t", "consensus", "event", "txset-reject",
			"txset", fmt.Sprintf("%x", d.id[:8]),
			"attempts", d.attempts,
			"missing", d.missing,
		)
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
	r.deleteTxSetAcquire(txSetID)
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
