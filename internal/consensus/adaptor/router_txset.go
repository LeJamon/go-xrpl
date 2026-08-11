package adaptor

import (
	"context"
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
	txMap         *shamap.SHAMap
	startedAt     time.Time
	lastUpdate    time.Time
	retainedBytes int64

	// Retry bookkeeping. lastRequest is when we most recently broadcast a
	// RequestTxSetMissingNodes. attempts is pure telemetry (the broadcast
	// count surfaced in logs). stallTicks counts no-progress timer ticks —
	// the give-up signal. peerNonProgress tracks consecutive
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

	// dormant latches once stallTicks exceeds MaxStallTicks: the timer stops
	// actively re-requesting, but the partial SHAMap is RETAINED so a later
	// MarkTxSetStillNeeded resumes the acquire from where it left off. The TTL
	// sweep still reclaims a truly-abandoned entry.
	dormant bool

	// completed distinguishes a successfully acquired set from recoverable
	// terminal failures. stillNeed revives failed acquisitions, but never a set
	// that was already delivered to consensus.
	completed bool

	// haveRoot latches once the real root node for this tx-set hash has been
	// installed. A fresh shamap.New carries a non-nil but EMPTY root, which
	// would let an empty tree "complete" with zero leaves; until haveRoot is
	// set the acquire only requests the root and can never complete.
	haveRoot bool

	// done latches a terminal acquire. Straggling data is charged and dropped;
	// only recoverable failures can be revived.
	done bool
}

const (
	// 60s covers a consensus round (~15s) plus retries with margin.
	txSetAcquireTTL = 60 * time.Second

	// Acquisition limits bound both the number of independently addressable
	// SHAMaps and the work or payload that any one peer response can retain.
	// The node ceiling matches the production serve-path hard cap. The byte
	// limits independently prevent a peer from turning a requested set into
	// unbounded process memory.
	txSetAcquireMaxActive      = 64
	txSetAcquireMaxReplyNodes  = 12288
	txSetAcquireMaxReplyBytes  = int64(message.MaxMessageSize)
	txSetAcquireMaxSetBytes    = int64(message.MaxMessageSize)
	txSetAcquireMaxGlobalBytes = 4 * txSetAcquireMaxSetBytes
)

func txSetReplyBytes(ld *message.LedgerData) (int64, bool) {
	if len(ld.Nodes) > txSetAcquireMaxReplyNodes {
		return 0, false
	}
	var total int64
	for _, node := range ld.Nodes {
		nodeBytes := int64(len(node.NodeID)) + int64(len(node.NodeData))
		if nodeBytes > txSetAcquireMaxReplyBytes-total {
			return 0, false
		}
		total += nodeBytes
	}
	return total, true
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) txSetRetainedBytesLocked() int64 {
	var total int64
	for _, state := range r.txSetAcquire {
		total += state.retainedBytes
	}
	return total
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) makeTxSetByteRoomLocked(state *txSetAcquireState, additional int64) bool {
	if additional < 0 || additional > txSetAcquireMaxSetBytes-state.retainedBytes {
		return false
	}
	for additional > txSetAcquireMaxGlobalBytes-r.txSetRetainedBytesLocked() {
		oldestID, ok := r.oldestDoneTxSetAcquireLocked(true, true, state)
		if !ok {
			return false
		}
		r.deleteTxSetAcquireLocked(oldestID)
	}
	return true
}

func (r *Router) reserveTxSetNodeBytes(txSetID consensus.TxSetID, state *txSetAcquireState, txMap *shamap.SHAMap, nodeBytes int64) bool {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	current, ok := r.txSetAcquire[txSetID]
	if !ok || current != state || current.txMap != txMap || current.done ||
		!r.makeTxSetByteRoomLocked(current, nodeBytes) {
		return false
	}
	current.retainedBytes += nodeBytes
	return true
}

func (r *Router) releaseTxSetNodeBytes(txSetID consensus.TxSetID, state *txSetAcquireState, txMap *shamap.SHAMap, nodeBytes int64) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	current, ok := r.txSetAcquire[txSetID]
	if !ok || current != state || current.txMap != txMap {
		return
	}
	current.retainedBytes -= nodeBytes
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) releaseTxSetMapLocked(state *txSetAcquireState) {
	state.txMap = nil
	state.retainedBytes = 0
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) deleteTxSetAcquireLocked(txSetID consensus.TxSetID) {
	if state, ok := r.txSetAcquire[txSetID]; ok {
		r.releaseTxSetMapLocked(state)
		delete(r.txSetAcquire, txSetID)
	}
}

func txSetIDLess(left, right consensus.TxSetID) bool {
	for i := range left {
		if left[i] != right[i] {
			return left[i] < right[i]
		}
	}
	return false
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) oldestDoneTxSetAcquireLocked(
	dormant bool,
	retainedOnly bool,
	exclude *txSetAcquireState,
) (consensus.TxSetID, bool) {
	var oldestID consensus.TxSetID
	var oldestUpdate time.Time
	found := false
	for id, state := range r.txSetAcquire {
		if state == exclude || !state.done || state.dormant != dormant ||
			(retainedOnly && state.retainedBytes == 0) {
			continue
		}
		if !found || state.lastUpdate.Before(oldestUpdate) ||
			(state.lastUpdate.Equal(oldestUpdate) && txSetIDLess(id, oldestID)) {
			oldestID = id
			oldestUpdate = state.lastUpdate
			found = true
		}
	}
	return oldestID, found
}

// Caller must hold r.txSetAcquireMu.
func (r *Router) makeTxSetAcquireRoomLocked() bool {
	if len(r.txSetAcquire) < txSetAcquireMaxActive {
		return true
	}
	for _, dormant := range []bool{false, true} {
		oldestID, ok := r.oldestDoneTxSetAcquireLocked(dormant, false, nil)
		if ok {
			r.deleteTxSetAcquireLocked(oldestID)
			return true
		}
	}

	// Every entry is active. A newly requested consensus position must not be
	// suppressed by older work, so replace the least-recently-updated acquire.
	// The ID tie-break keeps admission deterministic when timestamps match.
	var oldestID consensus.TxSetID
	var oldestUpdate time.Time
	found := false
	for id, state := range r.txSetAcquire {
		if state.done {
			continue
		}
		if !found || state.lastUpdate.Before(oldestUpdate) ||
			(state.lastUpdate.Equal(oldestUpdate) && txSetIDLess(id, oldestID)) {
			oldestID = id
			oldestUpdate = state.lastUpdate
			found = true
		}
	}
	if !found {
		return false
	}
	r.deleteTxSetAcquireLocked(oldestID)
	return true
}

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
	NormalTimeouts           int
	MaxStallTicks            int
	PeerNonProgressThreshold int
}

type txSetNetwork interface {
	RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error
	RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error
	PeerWithTxSet(target [32]byte, exclude uint64) (uint64, bool)
	NotePeerHasTxSet(peerID uint64, hash [32]byte) bool
}

func defaultTxSetRetryKnobs() txSetRetryKnobs {
	return txSetRetryKnobs{
		MinInterval:              250 * time.Millisecond,
		NormalTimeouts:           4,
		MaxStallTicks:            20,
		PeerNonProgressThreshold: 3,
	}
}

// setTxSetRetryKnobsForTest overrides the tx-set retry knobs on this
// Router. Tests use it to dial timings down so they don't sleep for the
// production throttle window. The lock matches the read in handleTxSetData
// so racing this against an active inbox goroutine is safe under -race;
// production is not expected to call this.
func (r *Router) setTxSetRetryKnobsForTest(knobs txSetRetryKnobs) {
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
	if len(wire) < 2 || protocol.WireType(wire[len(wire)-1]) != protocol.WireTypeTransaction {
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
	exists, err := r.adaptor.HasTx(consensus.TxID(item.Key()))
	if err != nil || exists {
		return
	}
	if outcome, err := r.adaptor.SubmitPendingTx(item.Data(), false); err == nil && outcome.Class == openledger.ResultSuccess {
		r.relayTransaction(peermanagement.PeerID(originPeer), item.Data(), outcome.Queued)
	}
}

// txLeafWire frames a raw transaction blob as a SHAMap transaction-leaf
// node: `tx_blob || WireTypeTransaction`. The SHAMap wire decoders reverse
// this framing.
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
	_, withinReplyLimit := txSetReplyBytes(ld)
	if !withinReplyLimit {
		r.logger.Info("tx-set sync: reply exceeds resource limit",
			"t", "consensus", "event", "txset-resource-limit",
			"node_count", len(ld.Nodes))
		if originPeer != 0 {
			r.gossip.IncPeerBadData(originPeer, "txset-resource-limit")
		}
		return
	}
	var txSetID consensus.TxSetID
	copy(txSetID[:], ld.LedgerHash)

	r.txSetAcquireMu.Lock()
	r.sweepStaleTxSetAcquireLocked()
	state, exists := r.txSetAcquire[txSetID]
	if !exists {
		r.txSetAcquireMu.Unlock()
		if originPeer != 0 {
			r.gossip.IncPeerBadData(originPeer, "txset-useless-unneeded")
		}
		return
	}
	if state.done {
		r.txSetAcquireMu.Unlock()
		if originPeer != 0 {
			r.gossip.IncPeerBadData(originPeer, "txset-useless-unneeded")
		}
		return
	}

	if len(ld.Nodes) == 0 {
		state.peerNonProgress[originPeer]++
		r.txSetAcquireMu.Unlock()
		if originPeer != 0 {
			r.gossip.IncPeerBadData(originPeer, "txset-useless-nonprogress")
		}
		return
	}
	for _, node := range ld.Nodes {
		if len(node.NodeID) == 0 || len(node.NodeData) == 0 {
			r.txSetAcquireMu.Unlock()
			if originPeer != 0 {
				r.gossip.IncPeerBadData(originPeer, "ledger-data-decode")
			}
			return
		}
		if _, err := shamap.ParseNodeID(node.NodeID); err != nil {
			r.txSetAcquireMu.Unlock()
			if originPeer != 0 {
				r.gossip.IncPeerBadData(originPeer, "txset-baddata-nodeid")
			}
			return
		}
	}

	if state.txMap == nil {
		state.txMap = shamap.New(shamap.TypeTransaction)
		if err := state.txMap.StartSync(); err != nil {
			r.deleteTxSetAcquireLocked(txSetID)
			r.txSetAcquireMu.Unlock()
			r.logger.Info("tx-set sync: StartSync failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
			return
		}
	}
	state.lastUpdate = time.Now()
	txMap := state.txMap
	haveRoot := state.haveRoot
	r.txSetAcquireMu.Unlock()

	resourceLimited := false
	defer func() {
		if !resourceLimited {
			return
		}
		r.txSetAcquireMu.Lock()
		state, exists := r.txSetAcquire[txSetID]
		if !exists || state.retainedBytes != 0 {
			r.txSetAcquireMu.Unlock()
			return
		}
		r.deleteTxSetAcquireLocked(txSetID)
		r.txSetAcquireMu.Unlock()
	}()

	// Root NodeID is 33 zero bytes. AddRootNode is idempotent
	// (ErrRootAlreadySet treated as success). rootAccepted feeds
	// per-peer progress accounting so a peer whose reply contains
	// only the root still counts as making progress.
	rootAccepted := false
	badRootProvided := false
	for _, node := range ld.Nodes {
		if !isShamapRootNodeID(node.NodeID) {
			continue
		}
		nodeBytes := int64(len(node.NodeID)) + int64(len(node.NodeData))
		reserved := haveRoot || r.reserveTxSetNodeBytes(txSetID, state, txMap, nodeBytes)
		if !reserved {
			resourceLimited = true
			continue
		}
		err := txMap.AddRootNode([32]byte(txSetID), node.NodeData)
		switch {
		case err == nil:
			rootAccepted = true
		case errors.Is(err, shamap.ErrRootAlreadySet):
			rootAccepted = true
			if !haveRoot {
				r.releaseTxSetNodeBytes(txSetID, state, txMap, nodeBytes)
			}
		default:
			badRootProvided = true
			if !haveRoot {
				r.releaseTxSetNodeBytes(txSetID, state, txMap, nodeBytes)
			}
			r.logger.Info("tx-set sync: AddRootNode failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
		}
		continue
	}
	if rootAccepted && !haveRoot {
		haveRoot = true
		r.txSetAcquireMu.Lock()
		state.haveRoot = true
		r.txSetAcquireMu.Unlock()
	}

	// Process nodes in wire order. A bad root is tolerated and processing
	// continues, while a bad non-root invalidates the reply immediately.
	added := 0
	duplicates := 0
	replyValid := true
	for _, node := range ld.Nodes {
		if isShamapRootNodeID(node.NodeID) {
			continue
		}
		parsedID, err := shamap.ParseNodeID(node.NodeID)
		if err != nil {
			replyValid = false
			r.logger.Debug("tx-set sync: malformed node ID",
				"t", "consensus", "event", "txset-node-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"node_id_len", len(node.NodeID),
				"error", err.Error())
			break
		}
		nodeBytes := int64(len(node.NodeID)) + int64(len(node.NodeData))
		if !r.reserveTxSetNodeBytes(txSetID, state, txMap, nodeBytes) {
			resourceLimited = true
			continue
		}
		res, err := txMap.AddKnownNodeByID(parsedID, node.NodeData)
		if res != shamap.NodeUseful {
			r.releaseTxSetNodeBytes(txSetID, state, txMap, nodeBytes)
		}
		if res == shamap.NodeReRequest {
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
			duplicates++
			continue
		}
		added++
		r.learnTxFromLeaf(originPeer, node.NodeData)
	}

	if !replyValid {
		r.txSetAcquireMu.Lock()
		if originPeer != 0 {
			state.peerNonProgress[originPeer]++
		}
		r.txSetAcquireMu.Unlock()
		if originPeer != 0 {
			r.gossip.IncPeerBadData(originPeer, "txset-useless-nonprogress")
		}
		if !haveRoot {
			r.txSetAcquireMu.Lock()
			indirect := state.timedOut
			r.txSetAcquireMu.Unlock()
			if err := r.requestTxSetRoot(txSetID, originPeer, indirect); err != nil {
				r.logger.Info("tx-set sync: root request failed",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"error", err.Error())
			}
		}
		return
	}

	if !haveRoot {
		r.txSetAcquireMu.Lock()
		if originPeer != 0 {
			if badRootProvided {
				state.peerNonProgress[originPeer] = 0
			} else {
				state.peerNonProgress[originPeer]++
			}
		}
		if badRootProvided {
			now := time.Now()
			state.dormant = false
			state.attempts++
			state.lastRequest = now
			state.lastUpdate = now
		}
		indirect := state.timedOut
		r.txSetAcquireMu.Unlock()
		if originPeer != 0 && !badRootProvided {
			r.gossip.IncPeerBadData(originPeer, "txset-useless-nonprogress")
		}
		if err := r.requestTxSetRoot(txSetID, originPeer, indirect); err != nil {
			r.logger.Info("tx-set sync: root request failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"error", err.Error())
		}
		return
	}

	if err := txMap.FinishSync(); err != nil {
		// Request the missing nodes. Before going to peers, fill any
		// missing TX-leaf nodes from our own pending pool. For
		// tnTRANSACTION_NM the leaf-node hash equals the tx ID, so a
		// single tx-ID lookup resolves the leaf.
		missing := txMap.GetMissingNodes(256, nil)
		if len(missing) == 0 {
			// Root present but the map is inconsistent: terminal. Latch done
			// and KEEP the entry (TTL reclaims it); MarkTxSetStillNeeded can
			// revive it. Deleting would let the next straggler recreate a fresh
			// empty acquire.
			r.markTxSetFailed(txSetID)
			r.logger.Info("tx-set sync: stuck",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", txSetID[:8]),
				"err", err.Error())
			return
		}

		r.txSetAcquireMu.Lock()
		filledFromPool := r.fillTxSetFromLocalPool(txSetID, state, missing)
		r.txSetAcquireMu.Unlock()
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
			madeProgress := added > 0 || rootAccepted
			validAllDuplicate := duplicates > 0 && added == 0 && !rootAccepted
			r.txSetAcquireMu.Lock()
			// Knobs are read under txSetAcquireMu so a concurrent
			// setTxSetRetryKnobsForTest (test-only API) can't tear a
			// half-updated struct into the hot path.
			knobs := r.txSetRetryKnobs
			if !madeProgress && !validAllDuplicate {
				// No fresh attach: do NOT re-request inline — that would let a
				// junk/empty-reply peer amplify into a broadcast storm; let the
				// 250ms stall timer drive it.
				if originPeer != 0 && !resourceLimited {
					state.peerNonProgress[originPeer]++
				}
				r.txSetAcquireMu.Unlock()
				if originPeer != 0 {
					r.gossip.IncPeerBadData(originPeer, "txset-useless-nonprogress")
				}
				r.logger.Debug("tx-set sync: no-progress reply, deferring to timer",
					"t", "consensus", "event", "txset-retry-defer",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"missing", len(remaining),
				)
				return
			}
			// Useful reply: pipeline the next missing-nodes request
			// IMMEDIATELY. A valid duplicate is useful even though it does not
			// attach a fresh node.
			// The RTT itself rate-limits (one re-request per received reply),
			// so there is no storm and no MinInterval gate is needed. A fresh
			// lastRequest keeps the stall timer out of an actively-progressing
			// acquire; un-dormant so a resumed acquire keeps pipelining.
			// Give-up lives only on the timer.
			if originPeer != 0 {
				state.peerNonProgress[originPeer] = 0
			}
			state.dormant = false
			state.attempts++
			state.lastRequest = time.Now()
			state.lastUpdate = state.lastRequest
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
			if reqErr := r.requestTxSetMissingNodesUnicast(txSetID, nodeIDs, originPeer, excluded, indirect); reqErr != nil {
				r.logger.Info("tx-set sync: missing-nodes request failed",
					"t", "consensus", "event", "txset-reject",
					"txset", fmt.Sprintf("%x", txSetID[:8]),
					"error", reqErr.Error())
			}
			return
		}
	}

	// Walk leaves into blobs, then replace the completed map with a lightweight
	// terminal tombstone. Stragglers are dropped without retaining the SHAMap
	// payload until the TTL sweep.
	blobs := make([][]byte, 0, added+1)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	r.markTxSetComplete(txSetID)

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
	r.deleteTxSetAcquireLocked(txSetID)
	r.txSetAcquireMu.Unlock()
}

func (r *Router) markTxSetComplete(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	if state, ok := r.txSetAcquire[txSetID]; ok {
		state.done = true
		state.completed = true
		r.releaseTxSetMapLocked(state)
	}
	r.txSetAcquireMu.Unlock()
}

func (r *Router) markTxSetFailed(txSetID consensus.TxSetID) {
	r.txSetAcquireMu.Lock()
	if state, ok := r.txSetAcquire[txSetID]; ok {
		state.done = true
		state.dormant = true
		state.completed = false
	}
	r.txSetAcquireMu.Unlock()
}

// requestTxSetRoot (re)fetches the SHAMap root (the 33-byte zero node ID) of a
// tx-set. It unicasts to the replying peer when known, falling back to a
// broadcast when the origin is unknown. Both paths skip Adaptor.RequestTxSet so
// onTxSetRequested (MarkTxSetStillNeeded) does not reset the acquire's stall
// bookkeeping.
func (r *Router) requestTxSetRoot(txSetID consensus.TxSetID, originPeer uint64, indirect bool) error {
	rootID := [][]byte{make([]byte, shamap.NodeIDSize)}
	if originPeer != 0 {
		return r.txSetNet.RequestTxSetMissingNodesFromPeer(txSetID, rootID, originPeer, indirect)
	}
	return r.txSetNet.RequestTxSetMissingNodes(txSetID, rootID, nil, indirect)
}

// requestTxSetMissingNodesUnicast pipelines the next missing-nodes request to
// the single replying peer. It falls back to a filtered broadcast only when the
// origin is unknown (peerID 0, e.g. tests); the excluded set applies solely to
// that fallback, being irrelevant to a unicast.
func (r *Router) requestTxSetMissingNodesUnicast(txSetID consensus.TxSetID, nodeIDs [][]byte, originPeer uint64, excluded map[uint64]bool, indirect bool) error {
	if originPeer != 0 {
		return r.txSetNet.RequestTxSetMissingNodesFromPeer(txSetID, nodeIDs, originPeer, indirect)
	}
	return r.txSetNet.RequestTxSetMissingNodes(txSetID, nodeIDs, excluded, indirect)
}

// submitTxSetToEngine feeds a completed tx-set's blobs to the engine. An
// engine rejection is logged, not fatal — the engine re-checks the tx-set
// ID, so a stale or duplicate set is dropped rather than corrupting state.
func (r *Router) submitTxSetToEngine(txSetID consensus.TxSetID, blobs [][]byte) {
	// Verify signatures off-strand so the accept build hits the sig-cache
	// instead of a cold check per acquired tx under the apply mutex. Async:
	// consensus sees the set immediately; anything unreached verifies in-strand.
	if prewarm := r.prewarmSignatures; prewarm != nil {
		r.runLifecycleTask(func(ctx context.Context) {
			prewarm(ctx, blobs)
		})
	}
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
// (retryStalledTxSetAcquires) paths. Both callers run on the Run() goroutine
// and hold txSetAcquireMu while this updates retained-byte accounting.
func (r *Router) fillTxSetFromLocalPool(txSetID consensus.TxSetID, state *txSetAcquireState, missing []shamap.MissingNode) int {
	filled := 0
	for _, m := range missing {
		blob, err := r.adaptor.GetTx(consensus.TxID(m.Hash))
		if err != nil || len(blob) == 0 {
			continue
		}
		wire := txLeafWire(blob)
		nodeBytes := int64(shamap.NodeIDSize) + int64(len(wire))
		current, ok := r.txSetAcquire[txSetID]
		if !ok || current != state || current.txMap == nil ||
			!r.makeTxSetByteRoomLocked(current, nodeBytes) {
			continue
		}
		if addErr := current.txMap.AddKnownNode(m.Hash, wire); addErr == nil {
			current.retainedBytes += nodeBytes
			filled++
		}
	}
	return filled
}

// MarkTxSetStillNeeded revives recoverable acquisitions while leaving completed
// acquisitions latched. Timeout history is clamped rather than reset.
func (r *Router) admitTxSetStillNeeded(txSetID consensus.TxSetID) bool {
	if txSetID == (consensus.TxSetID{}) {
		return false
	}
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	r.sweepStaleTxSetAcquireLocked()
	now := time.Now()
	state, ok := r.txSetAcquire[txSetID]
	if !ok {
		if !r.makeTxSetAcquireRoomLocked() {
			return false
		}
		r.txSetAcquire[txSetID] = &txSetAcquireState{
			startedAt:       now,
			lastUpdate:      now,
			lastRequest:     now,
			peerNonProgress: make(map[uint64]int),
		}
		return true
	}
	state.lastUpdate = now
	if state.done && state.completed {
		return true
	}
	state.done = false
	state.dormant = false
	state.stallTicks = min(state.stallTicks, r.txSetRetryKnobs.NormalTimeouts)
	state.attempts = 0
	state.lastRequest = now
	return true
}

func (r *Router) MarkTxSetStillNeeded(txSetID consensus.TxSetID) {
	_ = r.admitTxSetStillNeeded(txSetID)
}

// sweepStaleTxSetAcquireLocked drops entries older than txSetAcquireTTL.
// Caller must hold r.txSetAcquireMu.
func (r *Router) sweepStaleTxSetAcquireLocked() {
	cutoff := time.Now().Add(-txSetAcquireTTL)
	for id, state := range r.txSetAcquire {
		if state.lastUpdate.Before(cutoff) {
			r.deleteTxSetAcquireLocked(id)
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
// is a no-progress tick (stallTicks); past MaxStallTicks the
// acquire goes dormant, RETAINING its partial map rather than deleting it, so
// a later MarkTxSetStillNeeded / progressing reply can resume it. Because the
// inbound path keeps lastRequest fresh while making progress, the MinInterval
// gate keeps this timer out of an actively-progressing acquire and only fires
// once responses stop arriving.
//
// A stalled tick re-requests by broadcast (filtered by the non-progress
// exclusion set) rather than adding one targeted peer at a time: under the
// high-throughput stalls this drives, fanning out to find any server fast
// matters more than minimising duplicate requests.
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
	type txSetTerminal struct {
		id       consensus.TxSetID
		attempts int
		err      error
	}
	var kicks []txSetKick
	var drops []txSetDrop
	var completes []txSetComplete
	var terminals []txSetTerminal
	var rootKicks []consensus.TxSetID

	r.txSetAcquireMu.Lock()
	r.sweepStaleTxSetAcquireLocked()
	knobs := r.txSetRetryKnobs
	for id, state := range r.txSetAcquire {
		if state.done {
			continue
		}
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
			state.timedOut = true
			if state.stallTicks > knobs.MaxStallTicks {
				state.dormant = true
				state.done = true
				drops = append(drops, txSetDrop{id: id, attempts: state.attempts, missing: 0})
				continue
			}
			if state.stallTicks < knobs.NormalTimeouts {
				continue
			}
			state.attempts++
			state.lastRequest = now
			rootKicks = append(rootKicks, id)
			continue
		}
		missing := state.txMap.GetMissingNodes(256, nil)
		if len(missing) == 0 {
			state.done = true
			if err := state.txMap.FinishSync(); err == nil {
				state.completed = true
				completes = append(completes, txSetComplete{id: id, txMap: state.txMap})
			} else {
				state.dormant = true
				state.completed = false
				terminals = append(terminals, txSetTerminal{id: id, attempts: state.attempts, err: err})
			}
			continue
		}
		// Before re-requesting from peers, source any still-missing tx-leaf
		// nodes from our own pending pool — the same local fill the inbound
		// path performs (handleTxSetData). If the pool has grown since the
		// last inbound reply it can complete the tree outright. GetTx is a
		// local pool read and txMap is owned by this (Run) goroutine, so the
		// fill is safe under txSetAcquireMu.
		filled := r.fillTxSetFromLocalPool(id, state, missing)
		var finishErr error
		if filled > 0 {
			finishErr = state.txMap.FinishSync()
			if finishErr == nil {
				state.done = true
				state.completed = true
				completes = append(completes, txSetComplete{id: id, txMap: state.txMap, filled: filled})
				continue
			}
			missing = state.txMap.GetMissingNodes(256, nil)
		}
		if len(missing) == 0 {
			state.done = true
			state.dormant = true
			state.completed = false
			terminals = append(terminals, txSetTerminal{id: id, attempts: state.attempts, err: finishErr})
			continue
		}
		// Past MaxStallTicks the acquire goes dormant: it RETAINS its partial
		// map (only the TTL sweep or an explicit resume reclaims it) instead
		// of being deleted, so consensus re-asking picks up where it left off.
		state.stallTicks++
		state.timedOut = true
		if state.stallTicks > knobs.MaxStallTicks {
			// Give up: latch dormant AND done so stragglers are dropped, while
			// KEEPING the partial map. Only MarkTxSetStillNeeded revives it; the
			// TTL sweep reclaims it if it stays abandoned.
			state.dormant = true
			state.done = true
			drops = append(drops, txSetDrop{id: id, attempts: state.attempts, missing: len(missing)})
			continue
		}
		if state.stallTicks < knobs.NormalTimeouts {
			continue
		}
		state.attempts++
		state.lastRequest = now
		excluded := buildExcludedPeers(state.peerNonProgress, knobs.PeerNonProgressThreshold)
		nodeIDs := missingNodeIDs(missing)
		kicks = append(kicks, txSetKick{id: id, nodeIDs: nodeIDs, excluded: excluded, attempts: state.attempts, missing: len(missing)})
	}
	r.txSetAcquireMu.Unlock()

	for _, c := range completes {
		r.finalizeCompletedTxSet(c.id, c.txMap, c.filled)
	}
	for _, terminal := range terminals {
		r.logger.Info("tx-set sync: terminal invalid map",
			"t", "consensus", "event", "txset-reject",
			"txset", fmt.Sprintf("%x", terminal.id[:8]),
			"attempts", terminal.attempts,
			"error", terminal.err,
		)
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
		if err := r.txSetNet.RequestTxSetMissingNodes(k.id, k.nodeIDs, k.excluded, true); err != nil {
			r.logger.Info("tx-set sync: timer missing-nodes request failed",
				"t", "consensus", "event", "txset-reject",
				"txset", fmt.Sprintf("%x", k.id[:8]),
				"error", err.Error())
		}
	}
}

func (r *Router) finalizeCompletedTxSet(txSetID consensus.TxSetID, txMap *shamap.SHAMap, filled int) {
	blobs := make([][]byte, 0)
	if err := txMap.ForEach(func(item *shamap.Item) bool {
		blobs = append(blobs, item.Data())
		return true
	}); err != nil {
		r.deleteTxSetAcquire(txSetID)
		return
	}
	// Keep a lightweight tombstone so stragglers are dropped; the retained
	// SHAMap payload is released immediately.
	r.markTxSetComplete(txSetID)
	if len(blobs) == 0 {
		return
	}
	r.logger.Info("tx-set sync: completed on retry",
		"t", "consensus", "event", "txset-complete",
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
