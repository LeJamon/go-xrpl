package inbound

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Sentinel errors returned by the replay-delta apply path. R5.16 —
// callers use errors.Is for matching so the wording can evolve
// without breaking test assertions on string contents.
var (
	// ErrReplayParentMismatch means the requested ledger is not a child of the selected replay parent.
	ErrReplayParentMismatch = errors.New("replay delta: parent hash mismatch")
	// ErrReplaySequenceMismatch means the response is not the selected parent's direct successor.
	ErrReplaySequenceMismatch = errors.New("replay delta: ledger seq mismatch")

	// ErrReplayTxParse wraps parse failures on peer-supplied tx blobs.
	// Either a peer fork or wire corruption that escaped GotResponse.
	ErrReplayTxParse = errors.New("replay delta: parse tx failed")

	// ErrReplayTxDiverged signals the engine returned a non-applied
	// result (terRETRY / tef* / tem* / tel*) on a tx that rippled
	// successfully applied (the peer served it in the delta, so its
	// canonical ledger embedded it). Rippled's BuildLedger.cpp:246 +
	// Transactor.cpp:1108,1215-1267 only rawTxInsert's the tx leaf
	// when applied==true (tes / tec); anything else drops the tx from
	// the view. Installing the peer-supplied leaf on non-applied
	// branches was a go-xrpl-only divergence (see R6.4) that papered
	// over a real engine disagreement — when the engine rejects a tx
	// that rippled accepted, AccountHash will diverge regardless, so
	// preserving the leaf bought nothing and obscured the root cause.
	// Fail loudly instead so the replay falls back to legacy catchup.
	ErrReplayTxDiverged = errors.New("replay delta: tx result diverges from peer")

	// ErrReplayLeafInstall wraps SHAMap AddTransactionWithMeta
	// failures when installing a verified leaf blob into the child
	// ledger's tx map. Rare — indicates a corrupt leaf byte stream
	// that survived GotResponse's hash check, which is theoretically
	// impossible without hash-collision-level corruption.
	ErrReplayLeafInstall = errors.New("replay delta: install tx leaf failed")
)

// replayDeltaTimeout caps the TOTAL budget a replay-delta acquisition
// is allowed across its entire retry loop (sub-task timeouts +
// peer-swaps + legacy fallback). Crossing this budget triggers the
// outer-failure path in the router, which abandons the acquisition
// and re-arms via the legacy mtGET_LEDGER path. Sized to comfortably
// cover rippled's inner budget (SubTaskRetry × Max + FallbackTime ≈
// 2.5s + 2s = 4.5s) with safety margin for slow WAN RTTs.
const replayDeltaTimeout = 10 * time.Second

// subTaskRetryInterval is the per-peer sub-task timeout. A request
// that hasn't received a matching response within this window is
// considered dropped and the router rotates to a new peer. Matches
// rippled's LedgerReplayer.h:49 SUB_TASK_TIMEOUT.
const subTaskRetryInterval = 250 * time.Millisecond

// subTaskRetryMax caps how many distinct peers are tried before the
// outer budget kicks in and we fall back to the legacy path. Matches
// rippled's LedgerReplayer.h:51 SUB_TASK_MAX_TIMEOUTS.
const subTaskRetryMax = 10

// DecodedTx pairs a verified transaction with its metadata-derived index.
// Returned (in TransactionIndex order) by ReplayDelta.OrderedTxs() once
// verification has succeeded — so consumers that re-apply the txs against
// the parent state map can do so in the same order rippled used when
// originally building the ledger.
type DecodedTx struct {
	// Index is sfTransactionIndex from the metadata. Mirrors the key
	// rippled uses when ordering txs at LedgerReplayMsgHandler.cpp:266.
	Index uint32
	// Hash is the canonical XRPL transaction ID
	// (sha512Half(HashPrefix::transactionID, txBytes)).
	Hash [32]byte
	// TxBytes is the binary-codec serialization of the transaction.
	TxBytes []byte
	// MetaBytes is the binary-codec serialization of the transaction
	// metadata (includes sfTransactionIndex). Carried alongside TxBytes
	// because tec/tef metadata is required to recompute the new state.
	MetaBytes []byte
	// LeafBlob is the original wire blob (VL(tx) + VL(meta)) as inserted
	// into the tx SHAMap. Re-emitting this avoids a second VL pass when
	// the consumer wants to mirror rippled's tx-with-meta leaf format.
	LeafBlob []byte
}

// ReplayDelta tracks an outbound mtREPLAY_DELTA_REQUEST and verifies the
// matching response. Mirrors rippled's LedgerReplayMsgHandler::
// processReplayDeltaResponse algorithm at LedgerReplayMsgHandler.cpp:221-293:
//
//  1. Reject responses that carry an error or an empty header.
//  2. Deserialize the header and recompute its hash; abort on mismatch.
//  3. Reconstruct the tx SHAMap by inserting every leaf blob keyed by its
//     tx hash, using the full wire blob (tx + metadata VLs) as the value
//     so the SHAMap root matches the header's tx hash.
//  4. Compare the rebuilt root against header.TxHash; abort on mismatch.
//
// On success the verified header and the ordered tx list become available
// via Result() and OrderedTxs(); the consumer (consensus router) then
// adopts the ledger by re-applying the txs against the parent state.
type ReplayDelta struct {
	hash    [32]byte
	peerID  uint64
	parent  *ledger.Ledger
	clock   Clock
	created time.Time
	logger  *slog.Logger

	mu      sync.Mutex
	state   State
	err     error
	result  *ledger.Ledger // pre-apply: parent state carried through
	derived *ledger.Ledger // post-apply: state map re-derived by the engine
	txs     []DecodedTx

	// subTaskStart is the time of the last wire request (initial send
	// or peer rotation). Drives IsSubTaskTimedOut without touching
	// `created` (which bounds the outer budget).
	subTaskStart time.Time
	// retryCount is the number of peer rotations performed so far. At
	// subTaskRetryMax the caller escalates to the legacy path.
	retryCount int
	// triedPeers remembers peers already asked; the router passes this
	// to ReplayCapablePeersExcluding so we don't loop back to a silent
	// peer. Stored as a slice (not a set) because subTaskRetryMax is
	// small and we need deterministic iteration.
	triedPeers []uint64
}

// NewReplayDelta creates a ReplayDelta acquisition for the ledger
// identified by hash. The acquisition is initialized in StateWantBase
// (the first State value) and transitions to StateComplete or StateFailed
// once GotResponse runs.
//
// parent is the validated ledger at seq-1. It anchors the resulting
// ledger's state map: rippled's downstream LedgerReplayer re-applies the
// verified txs against this parent's state to derive the final state.
// Phase B does not run that replay — it only verifies framing and exposes
// the ordered txs — so parent is held but not mutated here.
func NewReplayDelta(hash [32]byte, peerID uint64, parent *ledger.Ledger, logger *slog.Logger) *ReplayDelta {
	return NewReplayDeltaWithClock(hash, peerID, parent, logger, SystemClock)
}

// NewReplayDeltaWithClock is like NewReplayDelta but accepts an explicit
// Clock so tests (or any caller with its own time source) can drive
// timeout behavior without touching the wall clock.
func NewReplayDeltaWithClock(hash [32]byte, peerID uint64, parent *ledger.Ledger, logger *slog.Logger, clock Clock) *ReplayDelta {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = SystemClock
	}
	now := clock.Now()
	return &ReplayDelta{
		hash:         hash,
		peerID:       peerID,
		parent:       parent,
		clock:        clock,
		created:      now,
		subTaskStart: now,
		state:        StateWantBase,
		logger:       logger,
		triedPeers:   []uint64{peerID},
	}
}

// NewStoredLedgerReplay creates a complete replay from locally stored parent
// and target ledgers. The target's transaction leaves are decoded and ordered
// by sfTransactionIndex so Apply can rebuild the target deterministically.
func NewStoredLedgerReplay(parent, target *ledger.Ledger, logger *slog.Logger) (*ReplayDelta, error) {
	if parent == nil {
		return nil, errors.New("stored replay parent is nil")
	}
	if target == nil {
		return nil, errors.New("stored replay target is nil")
	}
	if !parent.IsClosed() {
		return nil, errors.New("stored replay parent is not closed")
	}
	if !target.IsClosed() {
		return nil, errors.New("stored replay target is not closed")
	}

	parentHash := parent.Hash()
	targetHeader := target.Header()
	if targetHeader.ParentHash != parentHash {
		return nil, fmt.Errorf(
			"%w: target parent %x, expected %x",
			ErrReplayParentMismatch,
			targetHeader.ParentHash[:8],
			parentHash[:8],
		)
	}
	if parent.Sequence() == ^uint32(0) {
		return nil, fmt.Errorf(
			"%w: parent sequence %d has no successor",
			ErrReplaySequenceMismatch,
			parent.Sequence(),
		)
	}
	expectedSequence := parent.Sequence() + 1
	if targetHeader.LedgerIndex != expectedSequence {
		return nil, fmt.Errorf(
			"%w: target %d, expected %d",
			ErrReplaySequenceMismatch,
			targetHeader.LedgerIndex,
			expectedSequence,
		)
	}
	parentHeader := parent.Header()
	expectedResolution := consensus.GetNextLedgerTimeResolution(
		uint32(parentHeader.CloseTimeResolution),
		parentHeader.GetCloseAgree(),
		targetHeader.LedgerIndex,
	)
	if uint32(targetHeader.CloseTimeResolution) != expectedResolution {
		return nil, fmt.Errorf(
			"stored replay target close time resolution: got %d, derived %d from parent",
			targetHeader.CloseTimeResolution,
			expectedResolution,
		)
	}
	targetHash := target.Hash()
	if calculated := header.CalculateHash(targetHeader); calculated != targetHash {
		return nil, fmt.Errorf(
			"stored replay target header hash mismatch: computed %x stored %x",
			calculated[:8],
			targetHash[:8],
		)
	}
	txRoot, err := target.TxMapHash()
	if err != nil {
		return nil, fmt.Errorf("stored replay target transaction map: %w", err)
	}
	if txRoot != targetHeader.TxHash {
		return nil, fmt.Errorf(
			"stored replay target transaction root mismatch: computed %x header %x",
			txRoot[:8],
			targetHeader.TxHash[:8],
		)
	}

	decoded := make([]DecodedTx, 0, target.TxCount())
	seenIndices := make(map[uint32][32]byte)
	var decodeErr error
	err = target.ForEachTransaction(func(itemHash [32]byte, leafBlob []byte) bool {
		txBytes, metaBytes, err := tx.SplitTxWithMetaBlob(leafBlob)
		if err != nil {
			decodeErr = fmt.Errorf("stored replay tx %x: split leaf: %w", itemHash[:8], err)
			return false
		}
		if len(metaBytes) == 0 {
			decodeErr = fmt.Errorf("stored replay tx %x: missing metadata", itemHash[:8])
			return false
		}
		txIndex, ok := tx.TransactionIndexFromMetadata(metaBytes)
		if !ok {
			decodeErr = fmt.Errorf("stored replay tx %x: missing transaction index", itemHash[:8])
			return false
		}
		if previous, duplicate := seenIndices[txIndex]; duplicate {
			decodeErr = fmt.Errorf(
				"stored replay duplicate transaction index %d for %x and %x",
				txIndex,
				previous[:8],
				itemHash[:8],
			)
			return false
		}
		computedHash := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBytes)
		if computedHash != itemHash {
			decodeErr = fmt.Errorf(
				"stored replay transaction hash mismatch: computed %x stored %x",
				computedHash[:8],
				itemHash[:8],
			)
			return false
		}

		seenIndices[txIndex] = itemHash
		decoded = append(decoded, DecodedTx{
			Index:     txIndex,
			Hash:      itemHash,
			TxBytes:   append([]byte(nil), txBytes...),
			MetaBytes: append([]byte(nil), metaBytes...),
			LeafBlob:  append([]byte(nil), leafBlob...),
		})
		return true
	})
	if err != nil {
		return nil, fmt.Errorf("walk stored replay transactions: %w", err)
	}
	if decodeErr != nil {
		return nil, decodeErr
	}

	sort.Slice(decoded, func(i, j int) bool { return decoded[i].Index < decoded[j].Index })
	for expected, dtx := range decoded {
		if dtx.Index != uint32(expected) {
			return nil, fmt.Errorf(
				"stored replay missing transaction index %d; next index is %d",
				expected,
				dtx.Index,
			)
		}
	}

	replay := NewReplayDelta(targetHash, 0, parent, logger)
	replay.result = target
	replay.txs = decoded
	replay.state = StateReplayReady
	return replay, nil
}

// Hash returns the ledger hash being acquired.
func (r *ReplayDelta) Hash() [32]byte { return r.hash }

// PeerID returns the peer we asked for the delta. Guarded by r.mu because
// NoteSubTaskRetry rebinds r.peerID on peer rotation.
func (r *ReplayDelta) PeerID() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peerID
}

// Parent returns the parent ledger this acquisition is anchored on.
// Used by the consensus router to source per-ledger engine config (fees,
// amendment rules) before invoking Apply(). Guarded by r.mu because
// SetParent may rebind r.parent after acquisition.
func (r *ReplayDelta) Parent() *ledger.Ledger {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.parent
}

// SetParent rebinds the parent ledger AFTER acquisition. Refuses to
// overwrite an already-bound parent, so a misuse can't silently
// corrupt the apply-with-wrong-parent path.
func (r *ReplayDelta) SetParent(parent *ledger.Ledger) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parent != nil && r.parent.Hash() != parent.Hash() {
		return fmt.Errorf("SetParent: parent already bound to %x, refusing to overwrite with %x",
			r.parent.Hash(), parent.Hash())
	}
	r.parent = parent
	return nil
}

// Seq returns the ledger sequence under acquisition. Derived from the
// parent ledger because the request itself only carries the hash.
// Guarded by r.mu because SetParent may rebind r.parent.
func (r *ReplayDelta) Seq() uint32 {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.parent == nil {
		return 0
	}
	return r.parent.Sequence() + 1
}

// State returns the current acquisition state.
func (r *ReplayDelta) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// IsComplete reports whether the acquisition has been verified and
// reconstructed.
func (r *ReplayDelta) IsComplete() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state == StateComplete
}

// IsTimedOut reports whether the request has outlived its OUTER
// budget (replayDeltaTimeout) — the hard ceiling beyond which the
// router abandons the replay-delta path entirely and falls back to
// the legacy mtGET_LEDGER acquisition. The sub-task retry loop
// typically recovers long before this ceiling fires; it exists as a
// safety net for edge cases like the entire tried-peer set going
// silent simultaneously.
func (r *ReplayDelta) IsTimedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == StateComplete || r.state == StateReplayReady || r.state == StateFailed {
		return false
	}
	return r.clock.Now().Sub(r.created) > replayDeltaTimeout
}

// IsSubTaskTimedOut reports whether the current peer has held the
// request past the sub-task window without delivering a response.
// The router rotates peers on this signal, matching rippled's
// LedgerDeltaAcquire::onTimer rotation semantics
// (LedgerDeltaAcquire.cpp, driven by SUB_TASK_TIMEOUT at
// LedgerReplayer.h:49).
func (r *ReplayDelta) IsSubTaskTimedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == StateComplete || r.state == StateReplayReady || r.state == StateFailed {
		return false
	}
	return r.clock.Now().Sub(r.subTaskStart) > subTaskRetryInterval
}

// RetriesExhausted reports whether we've already rotated through
// subTaskRetryMax peers without a successful response. When true,
// the router stops rotating and waits for the outer budget (or
// bypasses it by calling Abandon directly).
func (r *ReplayDelta) RetriesExhausted() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retryCount >= subTaskRetryMax
}

// RetryCount returns the number of peer rotations performed so far.
// Used by the router's maintenance tick and by tests to assert the
// retry loop ran as expected.
func (r *ReplayDelta) RetryCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.retryCount
}

// TriedPeers returns a snapshot of peer IDs we've already asked.
// The router hands this to ReplayCapablePeersExcluding so the next
// rotation picks a fresh peer.
func (r *ReplayDelta) TriedPeers() []uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]uint64, len(r.triedPeers))
	copy(out, r.triedPeers)
	return out
}

// WasTried reports whether a replay request for this acquisition was sent to
// peerID. Responses from earlier rotated peers remain legitimate while their
// requests are outstanding.
func (r *ReplayDelta) WasTried(peerID uint64) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, tried := range r.triedPeers {
		if tried == peerID {
			return true
		}
	}
	return false
}

// NoteSubTaskRetry advances the sub-task state to a new peer:
// updates peerID, resets the sub-task timer, and appends to
// triedPeers so subsequent rotations don't cycle back. Caller is
// responsible for issuing the new wire request to newPeerID.
// Matches rippled's LedgerDeltaAcquire::trigger-next-peer flow.
func (r *ReplayDelta) NoteSubTaskRetry(newPeerID uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peerID = newPeerID
	r.subTaskStart = r.clock.Now()
	r.retryCount++
	r.triedPeers = append(r.triedPeers, newPeerID)
}

// Result returns the ledger reconstructed from the verified delta. Only
// valid after IsComplete() returns true.
//
// Result is deliberately unavailable until Apply has successfully re-derived
// and verified the state map.
func (r *ReplayDelta) Result() (*ledger.Ledger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateComplete {
		return nil, fmt.Errorf("replay delta not complete (state=%d)", r.state)
	}
	if r.derived == nil {
		return nil, errors.New("replay delta complete without derived ledger")
	}
	return r.derived, nil
}

// OrderedTxs returns the verified transactions sorted by sfTransactionIndex
// so a consumer can re-apply them in the original execution order. Only
// valid once the response is verified so Apply can consume them.
func (r *ReplayDelta) OrderedTxs() []DecodedTx {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateReplayReady && r.state != StateComplete {
		return nil
	}
	out := make([]DecodedTx, len(r.txs))
	copy(out, r.txs)
	return out
}

// Err returns the verification error (nil unless state is StateFailed).
func (r *ReplayDelta) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// GotResponse verifies an inbound mtREPLAY_DELTA_RESPONSE against the
// expected ledger hash and reconstructs the tx SHAMap. Returns nil on
// success (state → StateReplayReady, OrderedTxs() populated)
// or the verification error on failure (state → StateFailed). Subsequent
// calls after a terminal state are no-ops.
func (r *ReplayDelta) GotResponse(resp *message.ReplayDeltaResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state == StateComplete || r.state == StateReplayReady || r.state == StateFailed {
		return r.err
	}

	if err := r.verifyAndBuild(resp); err != nil {
		r.state = StateFailed
		r.err = err
		return err
	}
	r.state = StateReplayReady
	return nil
}

// verifyAndBuild runs the full rippled algorithm. Caller holds r.mu.
func (r *ReplayDelta) verifyAndBuild(resp *message.ReplayDeltaResponse) error {
	if resp == nil {
		return errors.New("nil response")
	}
	if resp.Error != message.ReplyErrorNone {
		return fmt.Errorf("peer signaled error: %d", resp.Error)
	}
	if len(resp.LedgerHeader) == 0 {
		return errors.New("empty header")
	}
	if len(resp.LedgerHash) != 32 {
		return fmt.Errorf("bad hash length: %d", len(resp.LedgerHash))
	}

	// Check the exact on-the-wire header body before doing transaction work.
	advertised, ok := ToHash32(resp.LedgerHash)
	if !ok {
		return fmt.Errorf("bad hash length: %d", len(resp.LedgerHash))
	}
	computed := sha512half.Sum(protocol.HashPrefixLedgerMaster().Bytes(), resp.LedgerHeader)
	if computed != advertised {
		return fmt.Errorf("header hash mismatch: computed %x advertised %x",
			computed[:8], advertised[:8])
	}
	hdr, err := header.DeserializeHeader(resp.LedgerHeader, false)
	if err != nil {
		return fmt.Errorf("deserialize header: %w", err)
	}
	hdr.Hash = computed

	// Cross-check the parent linkage when we have a parent. rippled
	// doesn't perform this check inside processReplayDeltaResponse, but
	// it's an essentially-free invariant for us and catches a peer
	// serving a different fork than we expected.
	if r.parent != nil {
		parentHash := r.parent.Hash()
		if hdr.ParentHash != parentHash {
			return fmt.Errorf("%w: header parent %x, expected %x", ErrReplayParentMismatch,
				hdr.ParentHash[:8], parentHash[:8])
		}
		if hdr.LedgerIndex != r.parent.Sequence()+1 {
			return fmt.Errorf("%w: header %d, expected %d", ErrReplaySequenceMismatch,
				hdr.LedgerIndex, r.parent.Sequence()+1)
		}
		parentHeader := r.parent.Header()
		applicationResolution := consensus.GetNextLedgerTimeResolution(
			uint32(parentHeader.CloseTimeResolution),
			parentHeader.GetCloseAgree(),
			hdr.LedgerIndex,
		)
		if uint32(hdr.CloseTimeResolution) != applicationResolution {
			return fmt.Errorf(
				"target close time resolution: got %d, derived %d from parent",
				hdr.CloseTimeResolution, applicationResolution,
			)
		}
	}

	// Reconstruct the tx SHAMap by inserting every leaf blob keyed by
	// the tx hash (sha512Half(TXN prefix, txBytes)). The SHAMap value
	// is the FULL wire blob (VL(tx) + VL(metadata)) so the leaf hash
	// (sha512Half(SND prefix, blob, key)) matches what the sender
	// computed when serializing its tx-with-meta leaves.
	txMap := shamap.New(shamap.TypeTransaction)

	decoded := make([]DecodedTx, 0, len(resp.Transactions))
	for i, blob := range resp.Transactions {
		txBytes, metaBytes, err := splitTxWithMetaBlob(blob)
		if err != nil {
			return fmt.Errorf("tx %d: split blob: %w", i, err)
		}
		txID := sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), txBytes)
		txIndex, err := extractTransactionIndex(metaBytes)
		if err != nil {
			return fmt.Errorf("tx %d: extract index: %w", i, err)
		}
		// Store a fresh copy so the SHAMap can take ownership without
		// aliasing the slice the caller might mutate.
		leaf := append([]byte(nil), blob...)
		if err := txMap.PutWithNodeType(txID, leaf, shamap.NodeTypeTransactionWithMeta); err != nil {
			return fmt.Errorf("tx %d: insert into tx map: %w", i, err)
		}
		decoded = append(decoded, DecodedTx{
			Index:     txIndex,
			Hash:      txID,
			TxBytes:   txBytes,
			MetaBytes: metaBytes,
			LeafBlob:  leaf,
		})
	}

	if err := txMap.SetImmutable(); err != nil {
		return fmt.Errorf("freeze tx map: %w", err)
	}
	rootHash, err := txMap.Hash()
	if err != nil {
		return fmt.Errorf("compute tx map root: %w", err)
	}
	if rootHash != hdr.TxHash {
		return fmt.Errorf("tx map root mismatch: computed %x header %x",
			rootHash[:8], hdr.TxHash[:8])
	}

	// Sort by sfTransactionIndex so consumers can replay in order.
	sort.SliceStable(decoded, func(i, j int) bool { return decoded[i].Index < decoded[j].Index })

	// Build the result ledger. rippled does not commit a state map here
	// (the downstream LedgerReplayer re-applies the txs against the
	// parent state); we mirror that by carrying the parent's state map
	// snapshot through unchanged. A consumer that wants the verified
	// post-state can call ledger.NewOpen on the parent and apply
	// OrderedTxs(), then close — that round-trips through the normal
	// engine path and keeps Phase B free of replay-engine entanglement.
	stateMap, err := r.parentStateSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot parent state: %w", err)
	}

	r.result, err = ledger.NewFromHeader(*hdr, stateMap, txMap, drops.Fees{})
	if err != nil {
		return fmt.Errorf("construct replay ledger: %w", err)
	}
	r.txs = decoded

	r.logger.Info("replay delta verified",
		"seq", hdr.LedgerIndex,
		"hash", hex.EncodeToString(hdr.Hash[:8]),
		"txs", len(decoded),
		"peer", r.peerID,
	)
	return nil
}

// Apply re-derives the new ledger by replaying every orderedTx through the
// engine against a mutable copy of the parent's state, then verifies the
// resulting state-map and tx-map roots match the target header.
//
// Mirrors rippled's BuildLedger.cpp::buildLedger (replay variant): build a
// child of parent at header.closeTime, apply each tx in TransactionIndex
// order (naturally assigned by the engine), commit, verify both roots.
//
// Returns the fully-derived ledger on success, or an error with a clear
// divergence marker on failure. Only call after GotResponse succeeds; errors here
// mean either the peer lied or our engine diverges from rippled.
//
// The supplied EngineConfig provides shared (non-per-ledger) settings
// (BaseFee, ReserveBase, NetworkID, Logger, etc.). Per-ledger fields
// — LedgerSequence, ParentCloseTime, ParentHash, Rules — are overridden
// here from the verified target header / parent.
//
// Reference:
//   - rippled/src/xrpld/app/ledger/detail/BuildLedger.cpp:225-248
//   - rippled/src/xrpld/app/ledger/detail/BuildLedger.cpp:38-86
func (r *ReplayDelta) Apply(engineCfg tx.EngineConfig) (derived *ledger.Ledger, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateReplayReady {
		return nil, fmt.Errorf("Apply called before response verified (state=%d)", r.state)
	}
	defer func() {
		if err != nil {
			r.state = StateFailed
			r.err = err
		}
	}()
	if r.parent == nil {
		return nil, errors.New("cannot apply replay delta without parent ledger")
	}
	if r.result == nil {
		return nil, errors.New("verified result missing — replay delta state corrupt")
	}

	// The header verified by GotResponse is the one carried in r.result.
	// Use it as the source of truth for per-ledger fields the engine
	// needs — close time, sequence, drops baseline, target hashes.
	hdr := r.result.Header()

	// Build a mutable child ledger anchored on the parent. Mirror
	// rippled's Ledger(parent, closeTime) constructor: child inherits
	// the parent's totalCoins and chains its parent linkage from the
	// parent rather than the deserialized response header.
	stateMap, err := r.parent.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot parent state: %w", err)
	}
	txMap := shamap.New(shamap.TypeTransaction)
	parentHeader := r.parent.Header()
	applicationResolution := consensus.GetNextLedgerTimeResolution(
		uint32(parentHeader.CloseTimeResolution),
		parentHeader.GetCloseAgree(),
		hdr.LedgerIndex,
	)
	if uint32(hdr.CloseTimeResolution) != applicationResolution {
		return nil, fmt.Errorf(
			"target close time resolution: got %d, derived %d from parent",
			hdr.CloseTimeResolution, applicationResolution,
		)
	}
	applicationCloseTime := ledger.ApplicationViewCloseTime(r.parent.CloseTime(), hdr.CloseTime, applicationResolution)
	openHdr := header.LedgerHeader{
		LedgerIndex:         hdr.LedgerIndex,
		ParentHash:          r.parent.Hash(),
		ParentCloseTime:     r.parent.CloseTime(),
		CloseTime:           applicationCloseTime,
		CloseTimeResolution: hdr.CloseTimeResolution,
		// Drops baseline matches rippled's Ledger(parent, closeTime)
		// constructor: child inherits parent's totalCoins; Close()
		// subtracts dropsDestroyed accumulated during apply.
		Drops: r.parent.TotalDrops(),
	}
	child, err := ledger.NewOpenWithHeader(openHdr, stateMap, txMap, r.parent.GetFees())
	if err != nil {
		return nil, fmt.Errorf("construct replay ledger: %w", err)
	}

	// Override per-ledger config fields from the verified header /
	// parent. The caller's EngineConfig keeps fees, network ID, logger,
	// etc.; we only stamp the values that depend on which ledger we're
	// replaying. ApplyFlags = TapNONE matches rippled's
	// `retryAssured=false` for the replay path: replay is deterministic,
	// any terRETRY indicates divergence.
	engineCfg.LedgerSequence = hdr.LedgerIndex
	engineCfg.ParentCloseTime = parentCloseTimeRippleEpoch(r.parent)
	engineCfg.ApplicationCloseTime = protocol.ToRippleTime(applicationCloseTime)
	engineCfg.ApplicationCloseTimeSet = true
	engineCfg.ParentHash = r.parent.Hash()
	engineCfg.ApplyFlags = tx.TapNONE
	engineCfg.OpenLedger = false
	engineCfg.ViewOpen = false
	engineCfg.EnforceLoadFee = false
	if engineCfg.Rules == nil {
		// Caller didn't supply rules: derive from the parent's
		// Amendments SLE so the engine sees the same amendment set
		// that was active when these txs were first applied.
		rules, rulesErr := ledger.LoadAmendmentsFromLedger(r.parent)
		if rulesErr != nil {
			return nil, fmt.Errorf("load amendment rules from parent: %w", rulesErr)
		}
		engineCfg.Rules = rules
	}

	engine := txengine.NewEngine(child, engineCfg)

	// R6b.1: on a flag ledger, apply pending ValidatorToDisable /
	// ValidatorToReEnable transitions BEFORE applying any txs. Without this,
	// every flag ledger's replay-delta produces a wrong AccountHash and falls
	// back to legacy catchup.
	if protocol.IsFlagLedger(child.Sequence()) {
		if err := child.UpdateNegativeUNL(); err != nil {
			return nil, fmt.Errorf("flag-ledger updateNegativeUNL: %w", err)
		}
	}

	// Replay each tx in TransactionIndex order. The engine assigns
	// metadata.TransactionIndex internally from its txCount counter,
	// matching rippled's OpenView::txCount() behavior — so we don't
	// need to feed an index per tx.
	var expectedBatchInners []tx.AppliedInnerTransaction
	for _, dtx := range r.txs {
		txn, parseErr := tx.ParseFromBinary(dtx.TxBytes)
		if parseErr != nil {
			return nil, fmt.Errorf("%w: tx %x: %w", ErrReplayTxParse, dtx.Hash[:8], parseErr)
		}
		txn.SetRawBytes(dtx.TxBytes)
		isBatchInner := txn.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0
		if isBatchInner {
			if len(expectedBatchInners) == 0 {
				return nil, fmt.Errorf("%w: unexpected batch inner tx %x", ErrReplayTxDiverged, dtx.Hash[:8])
			}
			expected := expectedBatchInners[0]
			expectedHash, hashErr := tx.ComputeTransactionHash(expected.Transaction)
			if hashErr != nil || expectedHash != dtx.Hash || expected.Metadata == nil ||
				expected.Metadata.TransactionIndex != dtx.Index {
				return nil, fmt.Errorf("%w: batch inner tx %x does not match outer execution", ErrReplayTxDiverged, dtx.Hash[:8])
			}
			peerMeta, metaErr := binarycodec.Decode(hex.EncodeToString(dtx.MetaBytes))
			if metaErr != nil {
				return nil, fmt.Errorf("%w: decode batch inner metadata %x: %v", ErrReplayTxDiverged, dtx.Hash[:8], metaErr)
			}
			parentBatchID, _ := peerMeta["ParentBatchID"].(string)
			transactionResult, _ := peerMeta["TransactionResult"].(string)
			if expected.Metadata.ParentBatchID == nil ||
				!strings.EqualFold(parentBatchID, hex.EncodeToString(expected.Metadata.ParentBatchID[:])) ||
				transactionResult != expected.Metadata.TransactionResult.String() {
				return nil, fmt.Errorf("%w: batch inner metadata %x does not match outer execution", ErrReplayTxDiverged, dtx.Hash[:8])
			}
			if err := child.AddTransactionWithMeta(dtx.Hash, dtx.LeafBlob); err != nil {
				return nil, fmt.Errorf("%w: tx %x: %w", ErrReplayLeafInstall, dtx.Hash[:8], err)
			}
			expectedBatchInners = expectedBatchInners[1:]
			continue
		}
		if len(expectedBatchInners) != 0 {
			return nil, fmt.Errorf("%w: batch inner transactions missing before tx %x", ErrReplayTxDiverged, dtx.Hash[:8])
		}

		var result tx.ApplyResult
		if txn.TxType().IsPseudoTransaction() {
			result = engine.ApplyPseudo(txn)
		} else {
			result = engine.Apply(txn)
			result = engine.ApplyBatchInnerTransactions(context.Background(), txn, result)
		}

		// R6b.2a: compare engine-generated meta against the peer-supplied
		// meta so operators can see when our engine drifts from rippled's
		// AffectedNodes semantics. We still INSTALL peer meta (below) for
		// byte-parity of the tx map root with header.TxHash — the log is
		// pure telemetry for now. A later round can gate adoption on this
		// comparison and fall back to legacy on mismatch, but today we
		// don't have enough data on go-xrpl-vs-rippled meta drift rates to
		// risk catchup regressions. Rippled's BuildLedger.cpp:244-247
		// uses engine meta exclusively — that's the end-state we want.
		if result.Metadata != nil && len(dtx.MetaBytes) > 0 {
			if engineMeta, mErr := tx.SerializeMetadata(result.Metadata); mErr == nil {
				if len(engineMeta) > 0 && !bytes.Equal(engineMeta, dtx.MetaBytes) {
					r.logger.Warn("replay tx: engine-generated meta differs from peer meta — engine may diverge from rippled AffectedNodes semantics",
						"tx", fmt.Sprintf("%x", dtx.Hash[:8]),
						"engine_meta_len", len(engineMeta),
						"peer_meta_len", len(dtx.MetaBytes),
					)
				}
			}
		}

		// D5 — install the peer-supplied leaf only on applied==true
		// (tes / tec), matching rippled's Transactor.cpp:1108 +
		// 1215-1267 + BuildLedger.cpp:246. Anything else (ter / tef /
		// tem / tel) means the engine DROPPED the tx from the view;
		// rippled never rawTxInsert's such txs, so neither do we. If
		// the peer's canonical ledger contains that tx, AccountHash
		// will diverge at the post-Close check — but we fail here
		// instead, so the error message points at the actual engine
		// disagreement rather than a downstream hash symptom.
		//
		// Historical note: the pre-D5 switch (R5.11 + R6.4) tried to
		// paper over engine disagreements by installing the peer leaf
		// anyway and letting the AccountHash safety net catch genuine
		// divergence. The reasoning was that small preflight differences
		// were producing false-positive legacy-catchup fallbacks. That
		// trade-off was wrong — if the engine disagrees on whether a
		// tx applies, the state the peer claims we should reach is
		// unreachable from our engine regardless, so preserving the
		// leaf bought nothing and obscured the real divergence.
		if !result.Result.IsApplied() {
			r.logger.Warn("replay tx returned non-applied result — engine diverges from peer",
				"tx", fmt.Sprintf("%x", dtx.Hash[:8]),
				"ter", result.Result.String(),
				"note", "rippled only rawTxInsert's when applied==true (Transactor.cpp:1108,1215-1267)",
			)
			return nil, fmt.Errorf("%w: tx %x returned %s; rippled only embeds tes/tec txs",
				ErrReplayTxDiverged, dtx.Hash[:8], result.Result.String())
		}
		expectedBatchInners = append(expectedBatchInners, result.AppliedInnerTransactions...)
		// Applied path (tes / tec): anchor the verified peer leaf so the
		// rebuilt TxHash matches header.TxHash byte-for-byte. Using our
		// locally-generated metadata would diverge even when the
		// AffectedNodes are semantically equivalent.
		if err := child.AddTransactionWithMeta(dtx.Hash, dtx.LeafBlob); err != nil {
			return nil, fmt.Errorf("%w: tx %x: %w", ErrReplayLeafInstall, dtx.Hash[:8], err)
		}
	}
	if len(expectedBatchInners) != 0 {
		return nil, fmt.Errorf("%w: replay ended before all batch inner transactions", ErrReplayTxDiverged)
	}

	// Close the ledger. This freezes both maps, computes AccountHash and
	// TxHash from their roots, deducts dropsDestroyed from totalCoins,
	// updates the LedgerHashes skip list, and computes the final hash.
	// Mirrors rippled's buildLedgerImpl :60-66 sequence
	// (accum.apply / updateSkipList / flushDirty / setAccepted).
	if err := child.Close(hdr.CloseTime, hdr.CloseFlags); err != nil {
		return nil, fmt.Errorf("close replayed ledger: %w", err)
	}

	// Verify the rebuilt tx-map root. This should be impossible to fail
	// after GotResponse succeeded (we re-installed the same verified
	// leaves), but checking guards against silent leaf-blob corruption.
	gotTxRoot, err := child.TxMapHash()
	if err != nil {
		return nil, fmt.Errorf("compute replayed tx map hash: %w", err)
	}
	if gotTxRoot != hdr.TxHash {
		return nil, fmt.Errorf("tx map root mismatch after replay: computed %x header %x",
			gotTxRoot[:8], hdr.TxHash[:8])
	}

	// The critical correctness check: replayed state-map root must equal
	// the target header's AccountHash. Any divergence here means our
	// engine produced different state from rippled's — feeding such a
	// ledger into consensus would split us off the network.
	gotStateRoot, err := child.StateMapHash()
	if err != nil {
		return nil, fmt.Errorf("compute replayed state map hash: %w", err)
	}
	if gotStateRoot != hdr.AccountHash {
		return nil, fmt.Errorf(
			"state map root mismatch: expected %x got %x — engine diverges from rippled (seq=%d hash=%x)",
			hdr.AccountHash[:8], gotStateRoot[:8], hdr.LedgerIndex, hdr.Hash[:8])
	}

	// Sanity check: the canonical hash Close() computed from our maps
	// must match the verified header hash. If the two hashes match on
	// roots + the parent linkage, this is guaranteed by construction —
	// but we double-check rather than silently emitting a different hash
	// to downstream consumers.
	if child.Hash() != hdr.Hash {
		return nil, fmt.Errorf("ledger hash mismatch after close: got %x expected %x",
			child.Hash(), hdr.Hash)
	}

	r.logger.Info("replay delta applied",
		"seq", hdr.LedgerIndex,
		"hash", hex.EncodeToString(hdr.Hash[:8]),
		"txs", len(r.txs),
	)

	// Cache the derived ledger so subsequent Result() calls return it
	// instead of the pre-apply (stale state) ledger. Eliminates the
	// footgun where a caller forgets to use Apply's return value.
	r.derived = child
	r.state = StateComplete
	r.err = nil
	return child, nil
}

// parentCloseTimeRippleEpoch returns the parent ledger's close time as
// Ripple-epoch seconds (rippled's NetClock::time_point format). Mirrors
// the tx.EngineConfig.ParentCloseTime contract used elsewhere in the
// engine. The Ripple epoch is 2000-01-01 UTC.
func parentCloseTimeRippleEpoch(parent *ledger.Ledger) uint32 {
	return protocol.ToRippleTime(parent.CloseTime())
}

// parentStateSnapshot returns an immutable snapshot of the parent state
// map, or an empty state map if there is no parent (test scenarios).
func (r *ReplayDelta) parentStateSnapshot() (*shamap.SHAMap, error) {
	if r.parent == nil {
		return shamap.New(shamap.TypeState), nil
	}
	snap, err := r.parent.StateMapSnapshot()
	if err != nil {
		return nil, err
	}
	if err := snap.SetImmutable(); err != nil {
		return nil, err
	}
	return snap, nil
}

// ToHash32 returns h as [32]byte iff len(h) == 32. The bool return
// distinguishes a wrong-length input from an all-zero hash.
func ToHash32(h []byte) ([32]byte, bool) {
	var out [32]byte
	if len(h) != len(out) {
		return out, false
	}
	copy(out[:], h)
	return out, true
}

// splitTxWithMetaBlob extracts (txBytes, metaBytes) from a SHAMapItem
// wire blob using the XRPL VL framing. Mirrors rippled's
// processReplayDeltaResponse :253-257 where two `getSlice(getVLDataLength())`
// reads peel the tx and metadata in turn.
func splitTxWithMetaBlob(blob []byte) (txBytes, metaBytes []byte, err error) {
	if len(blob) == 0 {
		return nil, nil, errors.New("empty blob")
	}
	parser := serdes.NewBinaryParser(blob, nil)

	txLen, err := parser.ReadVariableLength()
	if err != nil {
		return nil, nil, fmt.Errorf("read tx VL: %w", err)
	}
	txBytes, err = parser.ReadBytes(txLen)
	if err != nil {
		return nil, nil, fmt.Errorf("read tx bytes: %w", err)
	}
	if !parser.HasMore() {
		return nil, nil, errors.New("missing metadata VL")
	}
	metaLen, err := parser.ReadVariableLength()
	if err != nil {
		return nil, nil, fmt.Errorf("read meta VL: %w", err)
	}
	metaBytes, err = parser.ReadBytes(metaLen)
	if err != nil {
		return nil, nil, fmt.Errorf("read meta bytes: %w", err)
	}
	return txBytes, metaBytes, nil
}

func extractTransactionIndex(metaBytes []byte) (uint32, error) {
	index, ok := tx.TransactionIndexFromMetadata(metaBytes)
	if !ok {
		return 0, errors.New("metadata missing or invalid TransactionIndex")
	}
	return index, nil
}
