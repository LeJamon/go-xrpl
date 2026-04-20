package inbound

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/codec/binarycodec/serdes"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
)

// replayDeltaTimeout caps how long a single replay-delta acquisition can
// wait for its response. Mirrors rippled's PeerSet timeout for inbound
// ledger requests (~30s). After timing out the consensus router falls
// back to the legacy mtGET_LEDGER acquisition path.
const replayDeltaTimeout = 30 * time.Second

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
	return &ReplayDelta{
		hash:    hash,
		peerID:  peerID,
		parent:  parent,
		clock:   clock,
		created: clock.Now(),
		state:   StateWantBase,
		logger:  logger,
	}
}

// Hash returns the ledger hash being acquired.
func (r *ReplayDelta) Hash() [32]byte { return r.hash }

// PeerID returns the peer we asked for the delta.
func (r *ReplayDelta) PeerID() uint64 { return r.peerID }

// Parent returns the parent ledger this acquisition is anchored on.
// Used by the consensus router to source per-ledger engine config (fees,
// amendment rules) before invoking Apply().
func (r *ReplayDelta) Parent() *ledger.Ledger { return r.parent }

// Seq returns the ledger sequence under acquisition. Derived from the
// parent ledger because the request itself only carries the hash.
func (r *ReplayDelta) Seq() uint32 {
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

// IsTimedOut reports whether the request has outlived its budget without
// reaching a terminal state. The consensus router polls this and falls
// back to the legacy acquisition path when it returns true.
func (r *ReplayDelta) IsTimedOut() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state == StateComplete || r.state == StateFailed {
		return false
	}
	return r.clock.Now().Sub(r.created) > replayDeltaTimeout
}

// Result returns the ledger reconstructed from the verified delta. Only
// valid after IsComplete() returns true.
//
// If Apply has been called successfully, Result returns the DERIVED ledger
// (verified header + verified tx map + engine-derived state map). Otherwise
// it returns the ledger with the parent's state map unchanged — safe for
// inspection but NOT safe to feed to consensus without running Apply first.
// Callers that need the canonical post-state should call Apply directly and
// use its return value.
func (r *ReplayDelta) Result() (*ledger.Ledger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateComplete {
		return nil, fmt.Errorf("replay delta not complete (state=%d)", r.state)
	}
	if r.derived != nil {
		return r.derived, nil
	}
	return r.result, nil
}

// OrderedTxs returns the verified transactions sorted by sfTransactionIndex
// so a consumer can re-apply them in the original execution order. Only
// valid after IsComplete() returns true.
func (r *ReplayDelta) OrderedTxs() []DecodedTx {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateComplete {
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
// success (state → StateComplete, Result() and OrderedTxs() populated)
// or the verification error on failure (state → StateFailed). Subsequent
// calls after a terminal state are no-ops.
func (r *ReplayDelta) GotResponse(resp *message.ReplayDeltaResponse) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state == StateComplete || r.state == StateFailed {
		return r.err
	}

	if err := r.verifyAndBuild(resp); err != nil {
		r.state = StateFailed
		r.err = err
		return err
	}
	r.state = StateComplete
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

	// Mirror rippled :228 — check the header hash before any tx work.
	// Hash the on-the-wire header bytes directly with the LWR prefix
	// rather than going through the parsed struct: the parse-then-hash
	// path passes through xrplEpochToTime which collapses an epoch of 0
	// (the XRPL ripple epoch) into a Go zero time, defeating the
	// reverse arithmetic CalculateLedgerHash relies on. The byte-level
	// hash is what rippled computes on the sender side and is the only
	// invariant guaranteed to round-trip.
	advertised, ok := toHash32(resp.LedgerHash)
	if !ok {
		return fmt.Errorf("bad hash length: %d", len(resp.LedgerHash))
	}
	computed := common.Sha512Half(protocol.HashPrefixLedgerMaster.Bytes(), resp.LedgerHeader)
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
			return fmt.Errorf("parent hash mismatch: header parent %x, expected %x",
				hdr.ParentHash[:8], parentHash[:8])
		}
		if hdr.LedgerIndex != r.parent.Sequence()+1 {
			return fmt.Errorf("ledger seq mismatch: header %d, expected %d",
				hdr.LedgerIndex, r.parent.Sequence()+1)
		}
	}

	// Reconstruct the tx SHAMap by inserting every leaf blob keyed by
	// the tx hash (sha512Half(TXN prefix, txBytes)). The SHAMap value
	// is the FULL wire blob (VL(tx) + VL(metadata)) so the leaf hash
	// (sha512Half(SND prefix, blob, key)) matches what the sender
	// computed when serializing its tx-with-meta leaves.
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return fmt.Errorf("create tx map: %w", err)
	}

	decoded := make([]DecodedTx, 0, len(resp.Transactions))
	for i, blob := range resp.Transactions {
		txBytes, metaBytes, err := splitTxWithMetaBlob(blob)
		if err != nil {
			return fmt.Errorf("tx %d: split blob: %w", i, err)
		}
		txID := common.Sha512Half(protocol.HashPrefixTransactionID[:], txBytes)
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

	r.result = ledger.NewFromHeader(*hdr, stateMap, txMap, drops.Fees{})
	r.txs = decoded

	r.logger.Info("replay delta verified",
		"seq", hdr.LedgerIndex,
		"hash", hex.EncodeToString(hdr.Hash[:8]),
		"txs", len(decoded),
		"peer", r.peerID,
	)
	return nil
}

// AppendTxForTest appends a synthetic DecodedTx to r.txs so a sibling
// package's test can simulate a peer sending a divergent tx set
// (e.g., a duplicate hash that triggers tefALREADY on the second
// apply). Production code never calls this — it lives outside a
// *_test.go because it must be reachable from internal/testing/p2p.
func (r *ReplayDelta) AppendTxForTest(dtx DecodedTx) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.txs = append(r.txs, dtx)
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
// divergence marker on failure. Only call after IsComplete(); errors here
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
func (r *ReplayDelta) Apply(engineCfg tx.EngineConfig) (*ledger.Ledger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.state != StateComplete {
		return nil, fmt.Errorf("Apply called before response verified (state=%d)", r.state)
	}
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
	// PARENT (not the deserialized response header) — the
	// xrplEpochToTime round-trip in DeserializeHeader collapses an
	// XRPL epoch of 0 to a Go zero time, which would then produce a
	// nonsense uint32 in calculateLedgerHash. The parent's in-memory
	// time.Time round-trips faithfully through Close().
	stateMap, err := r.parent.StateMapSnapshot()
	if err != nil {
		return nil, fmt.Errorf("snapshot parent state: %w", err)
	}
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return nil, fmt.Errorf("create empty tx map: %w", err)
	}
	openHdr := header.LedgerHeader{
		LedgerIndex:         hdr.LedgerIndex,
		ParentHash:          r.parent.Hash(),
		ParentCloseTime:     r.parent.CloseTime(),
		CloseTime:           hdr.CloseTime,
		CloseTimeResolution: hdr.CloseTimeResolution,
		// Drops baseline matches rippled's Ledger(parent, closeTime)
		// constructor: child inherits parent's totalCoins; Close()
		// subtracts dropsDestroyed accumulated during apply.
		Drops: r.parent.TotalDrops(),
	}
	child := ledger.NewOpenWithHeader(openHdr, stateMap, txMap, r.parent.GetFees())

	// Override per-ledger config fields from the verified header /
	// parent. The caller's EngineConfig keeps fees, network ID, logger,
	// etc.; we only stamp the values that depend on which ledger we're
	// replaying. ApplyFlags = TapNONE matches rippled's
	// `retryAssured=false` for the replay path: replay is deterministic,
	// any terRETRY indicates divergence.
	engineCfg.LedgerSequence = hdr.LedgerIndex
	engineCfg.ParentCloseTime = parentCloseTimeRippleEpoch(r.parent)
	engineCfg.ParentHash = r.parent.Hash()
	engineCfg.ApplyFlags = tx.TapNONE
	engineCfg.OpenLedger = false
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

	engine := tx.NewEngine(child, engineCfg)

	// Replay each tx in TransactionIndex order. The engine assigns
	// metadata.TransactionIndex internally from its txCount counter,
	// matching rippled's OpenView::txCount() behavior — so we don't
	// need to feed an index per tx.
	for _, dtx := range r.txs {
		txn, parseErr := tx.ParseFromBinary(dtx.TxBytes)
		if parseErr != nil {
			return nil, fmt.Errorf("parse tx %x: %w", dtx.Hash[:8], parseErr)
		}
		txn.SetRawBytes(dtx.TxBytes)

		result := engine.Apply(txn)
		switch {
		case result.Result.IsSuccess(), result.Result.IsTec():
			// Both produce ledger entries and consume a TransactionIndex.
			// Anchor the verified leaf blob (peer's tx + peer's metadata,
			// already verified by GotResponse) into the child's tx map so
			// the resulting TxHash matches header.TxHash exactly. Using
			// our locally-generated metadata would diverge byte-for-byte
			// from the peer's even when the AffectedNodes are
			// semantically equivalent.
			if err := child.AddTransactionWithMeta(dtx.Hash, dtx.LeafBlob); err != nil {
				return nil, fmt.Errorf("install tx leaf %x: %w", dtx.Hash[:8], err)
			}
		case result.Result.ShouldRetry():
			return nil, fmt.Errorf("tx %x requires retry but replay is non-retryable (got %s)",
				dtx.Hash[:8], result.Result.String())
		default:
			// tef*, tem*, tel* — these indicate the tx shouldn't have
			// been in this ledger. Either the peer is lying or our
			// engine diverges from rippled.
			return nil, fmt.Errorf("tx %x diverged from rippled: result=%s (engine bug or peer fork)",
				dtx.Hash[:8], result.Result.String())
		}
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
	return child, nil
}

// parentCloseTimeRippleEpoch returns the parent ledger's close time as
// Ripple-epoch seconds (rippled's NetClock::time_point format). Mirrors
// the tx.EngineConfig.ParentCloseTime contract used elsewhere in the
// engine. The Ripple epoch is 2000-01-01 UTC.
func parentCloseTimeRippleEpoch(parent *ledger.Ledger) uint32 {
	const rippleEpochUnix int64 = 946684800
	t := parent.CloseTime()
	if t.IsZero() {
		return 0
	}
	secs := t.Unix() - rippleEpochUnix
	if secs < 0 {
		return 0
	}
	return uint32(secs)
}

// parentStateSnapshot returns an immutable snapshot of the parent state
// map, or an empty state map if there is no parent (test scenarios).
func (r *ReplayDelta) parentStateSnapshot() (*shamap.SHAMap, error) {
	if r.parent == nil {
		return shamap.New(shamap.TypeState)
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

// toHash32 returns h as [32]byte iff len(h) == 32. The bool return
// distinguishes a wrong-length input from an all-zero hash.
func toHash32(h []byte) ([32]byte, bool) {
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

// extractTransactionIndex decodes the metadata STObject and returns the
// sfTransactionIndex value. Mirrors rippled's `meta[sfTransactionIndex]`
// access in processReplayDeltaResponse :265.
func extractTransactionIndex(metaBytes []byte) (uint32, error) {
	if len(metaBytes) == 0 {
		return 0, errors.New("empty metadata")
	}
	decoded, err := binarycodec.Decode(hex.EncodeToString(metaBytes))
	if err != nil {
		return 0, fmt.Errorf("decode metadata: %w", err)
	}
	raw, ok := decoded["TransactionIndex"]
	if !ok {
		return 0, errors.New("metadata missing TransactionIndex")
	}
	switch v := raw.(type) {
	case uint32:
		return v, nil
	case int:
		return uint32(v), nil
	case int64:
		return uint32(v), nil
	case uint64:
		return uint32(v), nil
	case float64:
		return uint32(v), nil
	default:
		return 0, fmt.Errorf("metadata TransactionIndex has unexpected type %T", raw)
	}
}
