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
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
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
	created time.Time
	logger  *slog.Logger

	mu     sync.Mutex
	state  State
	err    error
	result *ledger.Ledger
	txs    []DecodedTx
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
	if logger == nil {
		logger = slog.Default()
	}
	return &ReplayDelta{
		hash:    hash,
		peerID:  peerID,
		parent:  parent,
		created: time.Now(),
		state:   StateWantBase,
		logger:  logger,
	}
}

// Hash returns the ledger hash being acquired.
func (r *ReplayDelta) Hash() [32]byte { return r.hash }

// PeerID returns the peer we asked for the delta.
func (r *ReplayDelta) PeerID() uint64 { return r.peerID }

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
	return time.Since(r.created) > replayDeltaTimeout
}

// Result returns the ledger reconstructed from the verified delta. Only
// valid after IsComplete() returns true.
func (r *ReplayDelta) Result() (*ledger.Ledger, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != StateComplete {
		return nil, fmt.Errorf("replay delta not complete (state=%d)", r.state)
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
	hdr, err := header.DeserializeHeader(resp.LedgerHeader, false)
	if err != nil {
		return fmt.Errorf("deserialize header: %w", err)
	}
	computed := genesis.CalculateLedgerHash(*hdr)
	var advertised [32]byte
	copy(advertised[:], resp.LedgerHash)
	if computed != advertised {
		return fmt.Errorf("header hash mismatch: computed %x advertised %x",
			computed[:8], advertised[:8])
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
