package inbound

import (
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

// Sentinel errors returned by the skip-list acquisition path. Callers
// match with errors.Is so wording can evolve without breaking assertions.
var (
	// ErrSkipListProofInvalid signals the Merkle proof failed to verify
	// against the target ledger's stateHash, OR the verified leaf was
	// not a decodable LedgerHashes SLE, OR the SLE's Hashes vector was
	// empty. In every case the peer either lied or is broken and must
	// be charged bad-data by the caller.
	ErrSkipListProofInvalid = errors.New("skiplist acquire: proof invalid")

	// ErrSkipListResponseMismatch signals the response carries fields
	// that don't match the request — wrong LedgerHash, wrong MapType,
	// wrong Key. Either a stale reply or a peer trying to feed us a
	// skip-list for a ledger we didn't ask about.
	ErrSkipListResponseMismatch = errors.New("skiplist acquire: response mismatch")
)

// SkipListAcquire tracks an outbound mtPROOF_PATH_REQUEST for the
// rolling-256 LedgerHashes entry of a target ledger, and verifies the
// matching mtPROOF_PATH_RESPONSE. Mirrors rippled's SkipListAcquire
// (rippled/src/xrpld/app/ledger/detail/SkipListAcquire.cpp):
//
//  1. Send TMProofPathRequest{LedgerHash=target, Key=keylet::skip(),
//     Type=lmACCOUNT_STATE}.
//  2. On response: verify the proof path against the target's
//     stateHash via shamap.VerifyProofPathWithValue. A nil return is
//     proof-invalid — peer charge.
//  3. Decode the verified leaf as a LedgerHashes SLE; extract the
//     Hashes vector.
//
// The Hashes vector is the rolling-256 ancestry list — Hashes[N-1] is
// target's grand-parent (target.ParentHash's parent), Hashes[N-2] one
// before that, and so on. Index N-i covers seq target.seq-i-1.
type SkipListAcquire struct {
	targetHash [32]byte // the ledger whose skip-list we're fetching
	stateHash  [32]byte // target.AccountHash, used to verify the proof
	peerID     uint64
	clock      Clock
	created    time.Time
	logger     *slog.Logger

	mu     sync.Mutex
	state  State
	err    error
	hashes [][32]byte // populated on StateComplete

	// subTaskStart / retryCount / triedPeers mirror ReplayDelta's
	// peer-rotation machinery so the coordinator can reuse the same
	// timeout-driven peer-swap path for both acquisition types.
	subTaskStart time.Time
	retryCount   int
	triedPeers   []uint64
}

// NewSkipListAcquire creates an acquisition for targetHash's
// skip-list. stateHash MUST be the target's AccountHash — without it
// the peer-supplied proof cannot be verified.
func NewSkipListAcquire(
	targetHash, stateHash [32]byte,
	peerID uint64,
	logger *slog.Logger,
) *SkipListAcquire {
	return NewSkipListAcquireWithClock(targetHash, stateHash, peerID, logger, SystemClock)
}

// NewSkipListAcquireWithClock is NewSkipListAcquire with an injected
// clock for deterministic timeout tests.
func NewSkipListAcquireWithClock(
	targetHash, stateHash [32]byte,
	peerID uint64,
	logger *slog.Logger,
	clock Clock,
) *SkipListAcquire {
	if logger == nil {
		logger = slog.Default()
	}
	if clock == nil {
		clock = SystemClock
	}
	now := clock.Now()
	return &SkipListAcquire{
		targetHash:   targetHash,
		stateHash:    stateHash,
		peerID:       peerID,
		clock:        clock,
		created:      now,
		subTaskStart: now,
		state:        StateWantBase,
		logger:       logger,
		triedPeers:   []uint64{peerID},
	}
}

func (s *SkipListAcquire) TargetHash() [32]byte { return s.targetHash }
func (s *SkipListAcquire) StateHash() [32]byte  { return s.stateHash }
func (s *SkipListAcquire) PeerID() uint64       { return s.peerID }

func (s *SkipListAcquire) State() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *SkipListAcquire) IsComplete() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state == StateComplete
}

func (s *SkipListAcquire) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Hashes returns a defensive copy of the verified ancestor list, or
// nil if not yet complete.
func (s *SkipListAcquire) Hashes() [][32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state != StateComplete {
		return nil
	}
	out := make([][32]byte, len(s.hashes))
	copy(out, s.hashes)
	return out
}

// IsTimedOut reports whether the acquisition has outlived the outer
// budget (shared with replay-delta so a deep catch-up has one shape).
func (s *SkipListAcquire) IsTimedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateComplete || s.state == StateFailed {
		return false
	}
	return s.clock.Now().Sub(s.created) > replayDeltaTimeout
}

// IsSubTaskTimedOut reports whether the current peer has held the
// request past the sub-task window without delivering a response.
// Mirrors rippled's SkipListAcquire::onTimer behaviour (driven by
// SUB_TASK_TIMEOUT at LedgerReplayer.h:49).
func (s *SkipListAcquire) IsSubTaskTimedOut() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state == StateComplete || s.state == StateFailed {
		return false
	}
	return s.clock.Now().Sub(s.subTaskStart) > subTaskRetryInterval
}

func (s *SkipListAcquire) RetriesExhausted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryCount >= subTaskRetryMax
}

func (s *SkipListAcquire) RetryCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.retryCount
}

func (s *SkipListAcquire) TriedPeers() []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]uint64, len(s.triedPeers))
	copy(out, s.triedPeers)
	return out
}

// NoteSubTaskRetry rotates to newPeerID, resets the sub-task timer,
// and records the new peer. Caller re-issues the wire request.
func (s *SkipListAcquire) NoteSubTaskRetry(newPeerID uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.peerID = newPeerID
	s.subTaskStart = s.clock.Now()
	s.retryCount++
	s.triedPeers = append(s.triedPeers, newPeerID)
}

// GotResponse verifies a mtPROOF_PATH_RESPONSE against the stored
// target/stateHash and decodes the LedgerHashes SLE. No-op after a
// terminal state.
func (s *SkipListAcquire) GotResponse(resp *message.ProofPathResponse) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.state == StateComplete || s.state == StateFailed {
		return s.err
	}

	if err := s.verifyAndDecode(resp); err != nil {
		s.state = StateFailed
		s.err = err
		return err
	}
	s.state = StateComplete
	return nil
}

func (s *SkipListAcquire) verifyAndDecode(resp *message.ProofPathResponse) error {
	if resp == nil {
		return fmt.Errorf("%w: nil response", ErrSkipListResponseMismatch)
	}
	if resp.Error != message.ReplyErrorNone {
		return fmt.Errorf("%w: peer signaled error %d",
			ErrSkipListResponseMismatch, resp.Error)
	}

	respHash, ok := ToHash32(resp.LedgerHash)
	if !ok || respHash != s.targetHash {
		return fmt.Errorf("%w: ledger hash %x want %x",
			ErrSkipListResponseMismatch,
			truncHash(resp.LedgerHash), s.targetHash[:8])
	}

	if resp.MapType != message.LedgerMapAccountState {
		return fmt.Errorf("%w: map type %d want %d",
			ErrSkipListResponseMismatch,
			resp.MapType, message.LedgerMapAccountState)
	}

	skipKL := keylet.LedgerHashes()
	respKey, ok := ToHash32(resp.Key)
	if !ok || respKey != skipKL.Key {
		return fmt.Errorf("%w: key %x want skip-list %x",
			ErrSkipListResponseMismatch,
			truncHash(resp.Key), skipKL.Key[:8])
	}

	if len(resp.Path) == 0 {
		return fmt.Errorf("%w: empty proof path", ErrSkipListProofInvalid)
	}

	// If the peer included a ledger header, cross-check that
	// calculateLedgerHash(header) == targetHash. Mirrors rippled's
	// processProofPathResponse
	// (rippled/src/xrpld/app/ledger/detail/LedgerReplayMsgHandler.cpp:127-135).
	if len(resp.LedgerHeader) > 0 {
		hdr, err := header.DeserializeHeader(resp.LedgerHeader, false)
		if err != nil {
			return fmt.Errorf("%w: header deserialize: %v",
				ErrSkipListResponseMismatch, err)
		}
		if genesis.CalculateLedgerHash(*hdr) != s.targetHash {
			return fmt.Errorf("%w: header hash mismatch for target %x",
				ErrSkipListResponseMismatch, s.targetHash[:8])
		}
	}

	payload := shamap.VerifyProofPathWithValue(s.stateHash, skipKL.Key, resp.Path)
	if payload == nil {
		return fmt.Errorf("%w: merkle verify failed against stateHash %x",
			ErrSkipListProofInvalid, s.stateHash[:8])
	}

	hashes, err := decodeLedgerHashesSLE(payload)
	if err != nil {
		return fmt.Errorf("%w: decode SLE: %v", ErrSkipListProofInvalid, err)
	}
	if len(hashes) == 0 {
		return fmt.Errorf("%w: empty Hashes vector", ErrSkipListProofInvalid)
	}

	s.hashes = hashes
	s.logger.Info("skip-list verified",
		"target", hex.EncodeToString(s.targetHash[:8]),
		"hashes", len(hashes),
		"peer", s.peerID,
	)
	return nil
}

// decodeLedgerHashesSLE parses the binary-codec payload of a
// LedgerHashes ledger entry and returns its Hashes vector.
func decodeLedgerHashesSLE(payload []byte) ([][32]byte, error) {
	obj, err := binarycodec.Decode(hex.EncodeToString(payload))
	if err != nil {
		return nil, fmt.Errorf("binarycodec.Decode: %w", err)
	}

	letRaw, ok := obj["LedgerEntryType"]
	if !ok {
		return nil, errors.New("missing LedgerEntryType")
	}
	if letStr, _ := letRaw.(string); letStr != "LedgerHashes" {
		return nil, fmt.Errorf("LedgerEntryType=%v want LedgerHashes", letRaw)
	}

	rawHashes, ok := obj["Hashes"]
	if !ok {
		return nil, errors.New("missing Hashes field")
	}

	var hashStrs []string
	switch v := rawHashes.(type) {
	case []string:
		hashStrs = v
	case []any:
		hashStrs = make([]string, len(v))
		for i, h := range v {
			s, ok := h.(string)
			if !ok {
				return nil, fmt.Errorf("hash[%d] is %T not string", i, h)
			}
			hashStrs[i] = s
		}
	default:
		return nil, fmt.Errorf("Hashes is %T not list", rawHashes)
	}

	out := make([][32]byte, 0, len(hashStrs))
	for i, hs := range hashStrs {
		b, err := hex.DecodeString(hs)
		if err != nil {
			return nil, fmt.Errorf("hash[%d] hex: %w", i, err)
		}
		if len(b) != 32 {
			return nil, fmt.Errorf("hash[%d] length %d want 32", i, len(b))
		}
		var h [32]byte
		copy(h[:], b)
		out = append(out, h)
	}
	return out, nil
}

func truncHash(b []byte) []byte {
	if len(b) >= 8 {
		return b[:8]
	}
	return b
}
