// Package adaptor provides the concrete implementation of the consensus.Adaptor
// interface, bridging the consensus engine to the ledger service, P2P overlay,
// and transaction queue.
package adaptor

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/amendmentvote"
	"github.com/LeJamon/go-xrpl/internal/consensus/negativeunlvote"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

var (
	ErrTxSetNotFound  = errors.New("transaction set not found")
	ErrLedgerNotFound = errors.New("ledger not found")
)

// NetworkSender abstracts the P2P overlay for sending messages.
type NetworkSender interface {
	// BroadcastProposal / BroadcastValidation send our own traffic, unfiltered
	// (the squelch filter applies only to relayed peer traffic).
	BroadcastProposal(proposal *consensus.Proposal) error
	BroadcastValidation(validation *consensus.Validation) error
	BroadcastStatusChange(sc *message.StatusChange) error
	// RelayProposal / RelayValidation forward a peer-originated message to
	// other peers, subject to the squelch filter, excluding exceptPeer (the
	// originator; 0 = unfiltered, test-only). SuppressionHash keys the
	// overlay's reverse index so a later duplicate can recover the
	// known-haver set for the reduce-relay slot.
	RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error
	RelayValidation(validation *consensus.Validation, exceptPeer uint64) error
	// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with
	// originPeer and seenPeers (known-havers), driving squelch selection.
	// Implementations dedupe originPeer from seenPeers.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)
	RequestTxSet(id consensus.TxSetID) error
	// RequestTxSetMissingNodes requests specific SHAMap nodes (by 33-byte path
	// NodeID: 32 path + 1 depth) for an in-progress tx-set acquisition.
	// excluded peers are skipped (nil = unrestricted). indirect sets
	// query_type=qtINDIRECT; set it once the acquisition has timed out at
	// least once (see RequestStateNodes).
	RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error
	// RequestTxSetMissingNodesFromPeer is the unicast variant of
	// RequestTxSetMissingNodes: the request is sent only to the replying peer.
	// The inbound acquire pipeline uses it so a progressing reply re-requests
	// from the peer that just served; the broadcast variant stays the timer's
	// stalled-acquire fallback. nodeIDs may carry the 33-byte zero root ID to
	// (re)fetch the root.
	RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error
	RequestLedger(id consensus.LedgerID) error
	RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	// RequestStateNodes / RequestTransactionNodes fetch outstanding
	// account-state / transaction SHAMap nodes of an in-flight acquisition.
	// indirect (query_type=qtINDIRECT) must be false on the first attempt and
	// true once the acquisition has timed out at least once
	// (rippled InboundLedger::trigger timeouts_ != 0).
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, indirect bool) error
	RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, indirect bool) error
	SendToPeer(peerID uint64, frame []byte) error
	// PeerSupportsReplay reports whether the peer advertised the ledger-replay
	// feature during handshake (false for unknown/incomplete handshakes), so
	// the catchup policy can skip peers that would silently drop the request.
	PeerSupportsReplay(peerID uint64) bool
	// ReplayCapablePeersExcluding returns up to max peer IDs that advertised
	// ledger-replay, omitting those in excluded. Used by the replay-delta
	// retry loop to rotate peers on a sub-task timeout.
	ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64
	// IncPeerBadData attributes a malformed/invalid-data event to the peer so
	// the overlay can charge it toward the eviction threshold. No-op for
	// unknown peers; reason is a short stable label for logs.
	IncPeerBadData(peerID uint64, reason string)
	// PeersThatHave returns the peer IDs the overlay knows have the message
	// keyed by suppressionHash (nil if unknown or aged out). The router feeds
	// these known-havers into the reduce-relay slot on a duplicate arrival.
	PeersThatHave(suppressionHash [32]byte) []uint64
	// ShouldShedLedgerRequest reports whether a ledger-BODY request (liBASE /
	// liAS_NODE / liTX_NODE) from peerID should be dropped under load: peer
	// send-queue saturated, or local node fee-loaded and peer not clustered.
	// NEVER call for liTS_CANDIDATE (tx-set) requests — those serve on a
	// separate branch so consensus liveness isn't starved. False for unknown peers.
	ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool
	// PeerWithLedger returns a connected peer (other than exclude) that can
	// serve ledger (target, seq), to relay an unsatisfiable GetLedger.
	// ok is false when none qualifies.
	PeerWithLedger(target [32]byte, seq uint32, exclude uint64) (uint64, bool)
	// PeersWithLedger returns up to max connected peers (other than excluded)
	// that can serve ledger (target, seq), best-first, to broaden a stalled
	// acquisition's source set per no-progress timeout.
	PeersWithLedger(target [32]byte, seq uint32, excluded []uint64, max int) []uint64
	// PeerWithTxSet returns a connected peer (other than exclude) that
	// advertised tx-set root target, to relay an unsatisfiable
	// liTS_CANDIDATE GetLedger.
	PeerWithTxSet(target [32]byte, exclude uint64) (uint64, bool)
	// NotePeerHasTxSet records that peerID advertised tx-set root hash,
	// feeding PeerWithTxSet.
	NotePeerHasTxSet(peerID uint64, hash [32]byte)
}

// noopSender is a no-op NetworkSender for standalone or test use.
type noopSender struct{}

func (n *noopSender) BroadcastProposal(*consensus.Proposal) error         { return nil }
func (n *noopSender) BroadcastValidation(*consensus.Validation) error     { return nil }
func (n *noopSender) BroadcastStatusChange(*message.StatusChange) error   { return nil }
func (n *noopSender) RelayProposal(*consensus.Proposal, uint64) error     { return nil }
func (n *noopSender) RelayValidation(*consensus.Validation, uint64) error { return nil }
func (n *noopSender) UpdateRelaySlot([]byte, uint64, []uint64)            {}
func (n *noopSender) RequestTxSet(consensus.TxSetID) error                { return nil }
func (n *noopSender) RequestTxSetMissingNodes(consensus.TxSetID, [][]byte, map[uint64]bool, bool) error {
	return nil
}
func (n *noopSender) RequestTxSetMissingNodesFromPeer(consensus.TxSetID, [][]byte, uint64, bool) error {
	return nil
}
func (n *noopSender) RequestLedger(consensus.LedgerID) error                         { return nil }
func (n *noopSender) RequestLedgerByHashAndSeq([32]byte, uint32) error               { return nil }
func (n *noopSender) RequestLedgerBaseFromPeer(uint64, [32]byte, uint32) error       { return nil }
func (n *noopSender) RequestReplayDelta(uint64, [32]byte) error                      { return nil }
func (n *noopSender) RequestStateNodes(uint64, [32]byte, [][]byte, bool) error       { return nil }
func (n *noopSender) RequestTransactionNodes(uint64, [32]byte, [][]byte, bool) error { return nil }
func (n *noopSender) SendToPeer(uint64, []byte) error                                { return nil }
func (n *noopSender) PeerSupportsReplay(uint64) bool                                 { return false }
func (n *noopSender) ReplayCapablePeersExcluding([]uint64, int) []uint64             { return nil }
func (n *noopSender) IncPeerBadData(uint64, string)                                  {}
func (n *noopSender) PeersThatHave([32]byte) []uint64                                { return nil }
func (n *noopSender) ShouldShedLedgerRequest(uint64, bool) bool                      { return false }
func (n *noopSender) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool)         { return 0, false }
func (n *noopSender) PeersWithLedger([32]byte, uint32, []uint64, int) []uint64       { return nil }
func (n *noopSender) PeerWithTxSet([32]byte, uint64) (uint64, bool)                  { return 0, false }
func (n *noopSender) NotePeerHasTxSet(uint64, [32]byte)                              {}

// Compile-time interface check.
var _ consensus.Adaptor = (*Adaptor)(nil)

// Adaptor implements consensus.Adaptor, bridging the consensus engine
// to the ledger service, transaction queue, and P2P network.
type Adaptor struct {
	// mu protects trustedValidators / trustedSet / trustedMasterKeys /
	// quorum / operatingMode. Plain Mutex: these are mutated rarely and read
	// a few times per round, so RWMutex isn't justified.
	mu sync.Mutex

	ledgerService *service.Service
	sender        NetworkSender
	identity      *ValidatorIdentity

	// UNL: trusted validator public keys
	trustedValidators []consensus.NodeID
	trustedSet        map[consensus.NodeID]struct{}
	// trustedMasterKeys are the 33-byte master pubkeys index-aligned with
	// trustedValidators; empty when the UNL was supplied as raw NodeIDs
	// (some tests). Required for NegativeUNL voting.
	trustedMasterKeys [][33]byte
	quorum            int

	operatingMode consensus.OperatingMode

	// stateAcct tracks transition counts and cumulative durations per
	// operating mode for server_info.state_accounting.
	stateAcct *stateAccounting

	// Close-time offset, adjusted each round toward the network average.
	// Atomic ns so the Now() hot path avoids lock contention.
	closeOffsetNs atomic.Int64

	// consensusPhaseCh serializes consensus-phase notifications to the ledger
	// service's OnConsensusPhase hook: a single dispatcher goroutine drains it
	// in order (preventing out-of-order delivery). Enqueue is non-blocking, so
	// a slow hook can't stall the consensus path.
	consensusPhaseCh   chan string
	consensusPhaseOnce sync.Once

	// negUNLVoter produces the UNLModify pseudo-tx each voting ledger (at most
	// one ToDisable + one ToReEnable). nil for non-validating adaptors.
	negUNLVoter *negativeunlvote.Voter

	// validationHistorian provides per-ledger trusted-validation lookups,
	// wired by the engine after the ValidationTracker is built. Nil before
	// wiring — GenerateNegativeUNLPseudoTx degrades to no vote.
	validationHistorian consensus.ValidationHistorian

	txSetCache *TxSetCache

	// Peer-reported last-closed ledger hashes, keyed by overlay peer ID.
	// Populated by the router on every inbound statusChange so getNetworkLedger
	// can use peer LCLs even without a fresh proposal from that peer.
	peerLCLsMu sync.RWMutex
	peerLCLs   map[uint64]consensus.LedgerID

	// cookie is a random 64-bit value generated at adaptor creation
	// (one-shot per boot), emitted via sfCookie on every validation.
	cookie uint64

	// feeVote is this validator's fee-vote stance, copied from Config
	// at construction. Zero values mean "no vote".
	feeVote FeeVoteStance

	// amendmentStances is this validator's per-amendment voting stance,
	// seeded from registry Vote behavior and overridden by Config.AmendmentVote.
	// Absent → VoteAbstain; obsolete amendments can't be overridden to VoteUp.
	amendmentStances map[[32]byte]amendmentvote.Stance

	// amendmentTable, when set, is the live amendment table this validator
	// sources vote stances from each round (so operator veto/upvote changes
	// take effect without restart) and stashes per-round tallies into. nil
	// falls back to the construction-time amendmentStances map.
	amendmentTable *amendment.AmendmentTable

	// trustedVotes caches per-validator amendment votes for 24h to dampen
	// flapping when a flaky validator drops briefly.
	trustedVotes *TrustedVotes

	// onTxSetRequested fires before every RequestTxSet broadcast so the router
	// can re-arm its in-flight tx-set acquisition state. nil-safe.
	onTxSetRequested func(consensus.TxSetID)

	// onTxSetBuilt fires when BuildTxSet caches a new tx set, so the overlay
	// can broadcast mtHAVE_SET{tsHAVE} for it. nil-safe.
	onTxSetBuilt func(consensus.TxSetID)

	// lastIssuedValidationSeq is the highest ledger seq this node has
	// broadcast a validation for — rippled's localSeqEnforcer_.largest(),
	// the trie-descent floor for preferredLCL. Zero for a non-validator.
	lastIssuedValidationSeq atomic.Uint32

	// maxDisallowedSeq is the highest ledger seq persisted before this
	// process started; the engine never proposes or validates at or below
	// it (anti-double-sign across restarts). Immutable after New.
	maxDisallowedSeq uint32

	// reqLedgerLast rate-limits per-hash broadcast TMGetLedger retries from
	// the engine's checkLedger heartbeat (see RequestLedger).
	reqLedgerMu   sync.Mutex
	reqLedgerLast map[consensus.LedgerID]time.Time

	// announcedSets de-duplicates tsHAVE announcements per set hash (see
	// BuildTxSet).
	announcedSetsMu sync.Mutex
	announcedSets   map[consensus.TxSetID]struct{}

	logger *slog.Logger
}

// goxrplServerVersionTag identifies go-xrpl in the sfServerVersion field.
// rippled uses the top bit (0x8000...); we must NOT set it or this node
// would be counted as a rippled instance in peer version statistics.
const goxrplServerVersionTag uint64 = 0x4000_0000_0000_0000

// FeeVoteStance is this validator's desired fee structure for the next flag
// ledger, emitted on every validation (legacy UINT triple, or the post-XRPFees
// AMOUNT triple under featureXRPFees). A zero field means "operator did not
// set this field"; New() substitutes the default so an unconfigured validator
// votes toward defaults rather than abstaining.
type FeeVoteStance struct {
	BaseFee          uint64
	ReserveBase      uint32
	ReserveIncrement uint32
}

// defaultFeeVote returns the fee setup a validator votes toward with no
// [voting] config (reference_fee=10, account_reserve=10 XRP, owner_reserve=2 XRP).
func defaultFeeVote() FeeVoteStance {
	return FeeVoteStance{
		BaseFee:          10,
		ReserveBase:      10_000_000,
		ReserveIncrement: 2_000_000,
	}
}

type Config struct {
	LedgerService *service.Service
	Sender        NetworkSender
	Identity      *ValidatorIdentity
	Validators    []consensus.NodeID // UNL
	// ValidatorMasterKeys are the 33-byte master pubkeys index-aligned with
	// Validators. Optional — required for NegativeUNL voting (which emits raw
	// master pubkeys); nil skips NegativeUNL votes (bare-NodeID test fixtures).
	ValidatorMasterKeys [][33]byte
	// FeeVote is the validator's fee-vote stance; zero means no vote.
	FeeVote FeeVoteStance
	// AmendmentVote lists amendments (by registry name) to vote FOR on the next
	// flag ledger. Unknown names are dropped at construction; already-enabled
	// ones are filtered per-emission since the enabled set changes over time.
	AmendmentVote []string
	// AmendmentTable, when supplied, is the live amendment table owning the
	// operator's veto/upvote preferences; authoritative for stances (vetoed →
	// abstain, upvoted → up) over registry defaults. Shared with the ledger
	// service, which folds validated flag ledgers in via DoValidatedLedger.
	AmendmentTable *amendment.AmendmentTable
}

// generateCookie returns a non-zero random 64-bit cookie. On a read error it
// falls back to a time-derived value (the value carries no security meaning).
// Zero is bumped to 1 because the serializer treats zero as "omit".
func generateCookie() uint64 {
	var cookieBytes [8]byte
	if _, err := rand.Read(cookieBytes[:]); err != nil {
		binary.BigEndian.PutUint64(cookieBytes[:], uint64(time.Now().UnixNano()))
	}
	cookie := binary.BigEndian.Uint64(cookieBytes[:])
	if cookie == 0 {
		cookie = 1
	}
	return cookie
}

// seedAmendmentStances builds the initial per-amendment vote map: supported
// features default to their registered VoteBehavior (DefaultYes → VoteUp,
// DefaultNo → abstain, Obsolete → VoteObsolete), then operator amendmentVote
// names are layered on as VoteUp. Unknown/obsolete/unsupported names are dropped.
func seedAmendmentStances(amendmentVote []string, logger *slog.Logger) map[[32]byte]amendmentvote.Stance {
	stances := make(map[[32]byte]amendmentvote.Stance)
	for _, f := range amendment.AllFeatures() {
		switch {
		case f.Vote == amendment.VoteObsolete:
			stances[f.ID] = amendmentvote.VoteObsolete
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			stances[f.ID] = amendmentvote.VoteUp
		}
	}
	for _, name := range amendmentVote {
		f := amendment.GetFeatureByName(name)
		if f == nil {
			logger.Warn("unknown amendment in vote config; ignoring", "name", name)
			continue
		}
		if f.Vote == amendment.VoteObsolete {
			logger.Warn("obsolete amendment cannot be voted up; ignoring", "name", name)
			continue
		}
		if f.Supported != amendment.SupportedYes {
			logger.Warn("unsupported amendment cannot be voted up; ignoring", "name", name)
			continue
		}
		stances[f.ID] = amendmentvote.VoteUp
	}
	return stances
}

func New(cfg Config) *Adaptor {
	sender := cfg.Sender
	if sender == nil {
		sender = &noopSender{}
	}

	trustedSet := make(map[consensus.NodeID]struct{}, len(cfg.Validators))
	for _, v := range cfg.Validators {
		trustedSet[v] = struct{}{}
	}

	// Quorum: ceil(n * 0.8). computeQuorum with zero disabled validators
	// is equivalent and avoids duplicating the formula.
	quorum := computeQuorum(len(cfg.Validators), 0)

	cookie := generateCookie()

	logger := slog.Default().With("component", "consensus-adaptor")
	amendmentStances := seedAmendmentStances(cfg.AmendmentVote, logger)

	// Seed the amendment-vote cache with the initial UNL so
	// RecordVotes accepts validations from round one. Re-call
	// TrustChanged whenever the trusted set mutates at runtime.
	trustedVotes := NewTrustedVotes()
	trustedVotes.TrustChanged(cfg.Validators)

	// Substitute fee defaults per-field: an unset field falls back to the
	// default rather than "abstain".
	feeVote := cfg.FeeVote
	defaults := defaultFeeVote()
	if feeVote.BaseFee == 0 {
		feeVote.BaseFee = defaults.BaseFee
	}
	if feeVote.ReserveBase == 0 {
		feeVote.ReserveBase = defaults.ReserveBase
	}
	if feeVote.ReserveIncrement == 0 {
		feeVote.ReserveIncrement = defaults.ReserveIncrement
	}

	// Non-validators never emit, so skip the floor read (see maxDisallowedSeq).
	var maxDisallowedSeq uint32
	if cfg.Identity != nil && cfg.LedgerService != nil {
		maxDisallowedSeq = cfg.LedgerService.MaxPersistedLedgerSeq(context.Background())
		if maxDisallowedSeq > 0 {
			logger.Info("max persisted ledger floor for validations", "seq", maxDisallowedSeq)
		}
	}

	// NegativeUNL voter: constructed only with both a local identity and UNL
	// master keys (needed for the local-participation check and the emitted
	// UNLModify tx). nil otherwise — GenerateNegativeUNLPseudoTx returns no votes.
	var negUNLVoter *negativeunlvote.Voter
	var trustedMasterKeys [][33]byte
	if cfg.Identity != nil && len(cfg.ValidatorMasterKeys) == len(cfg.Validators) && len(cfg.ValidatorMasterKeys) > 0 {
		trustedMasterKeys = make([][33]byte, len(cfg.ValidatorMasterKeys))
		copy(trustedMasterKeys, cfg.ValidatorMasterKeys)
		negUNLVoter = negativeunlvote.NewVoter(cfg.Identity.NodeID)
	}

	return &Adaptor{
		ledgerService:     cfg.LedgerService,
		sender:            sender,
		identity:          cfg.Identity,
		trustedValidators: cfg.Validators,
		trustedSet:        trustedSet,
		trustedMasterKeys: trustedMasterKeys,
		quorum:            quorum,
		operatingMode:     consensus.OpModeDisconnected,
		stateAcct:         newStateAccounting(consensus.OpModeDisconnected, time.Now),
		negUNLVoter:       negUNLVoter,
		txSetCache:        NewTxSetCache(),
		peerLCLs:          make(map[uint64]consensus.LedgerID),
		reqLedgerLast:     make(map[consensus.LedgerID]time.Time),
		announcedSets:     make(map[consensus.TxSetID]struct{}),
		maxDisallowedSeq:  maxDisallowedSeq,
		cookie:            cookie,
		feeVote:           feeVote,
		amendmentStances:  amendmentStances,
		amendmentTable:    cfg.AmendmentTable,
		trustedVotes:      trustedVotes,
		logger:            logger,
	}
}

// SetValidationHistorian wires per-ledger trusted-validation lookups into the
// adaptor; the engine calls it once after building its ValidationTracker.
// Until set, GenerateNegativeUNLPseudoTx emits no votes.
func (a *Adaptor) SetValidationHistorian(h consensus.ValidationHistorian) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validationHistorian = h
}

// UpdatePeerLCL records the last-closed-ledger hash a peer reported via
// statusChange, so getNetworkLedger can fall back to peer LCLs when proposal
// votes are absent or stale. The zero hash removes any existing entry.
func (a *Adaptor) UpdatePeerLCL(peerID uint64, ledger consensus.LedgerID) {
	a.peerLCLsMu.Lock()
	defer a.peerLCLsMu.Unlock()
	if ledger == (consensus.LedgerID{}) {
		delete(a.peerLCLs, peerID)
		return
	}
	a.peerLCLs[peerID] = ledger
}

// PeerReportedLedgers returns a snapshot of all known peer LCL hashes.
func (a *Adaptor) PeerReportedLedgers() []consensus.LedgerID {
	a.peerLCLsMu.RLock()
	defer a.peerLCLsMu.RUnlock()
	if len(a.peerLCLs) == 0 {
		return nil
	}
	out := make([]consensus.LedgerID, 0, len(a.peerLCLs))
	for _, h := range a.peerLCLs {
		out = append(out, h)
	}
	return out
}

func (a *Adaptor) BroadcastProposal(proposal *consensus.Proposal) error {
	return a.sender.BroadcastProposal(proposal)
}

func (a *Adaptor) BroadcastValidation(validation *consensus.Validation) error {
	if validation != nil {
		for {
			cur := a.lastIssuedValidationSeq.Load()
			if validation.LedgerSeq <= cur ||
				a.lastIssuedValidationSeq.CompareAndSwap(cur, validation.LedgerSeq) {
				break
			}
		}
	}
	return a.sender.BroadcastValidation(validation)
}

// PeersThatHave delegates to NetworkSender so higher layers can query without
// importing the overlay. See NetworkSender.PeersThatHave.
func (a *Adaptor) PeersThatHave(suppressionHash [32]byte) []uint64 {
	return a.sender.PeersThatHave(suppressionHash)
}

// RelayProposal forwards a peer-originated proposal, excluding exceptPeer
// (0 = everyone). See NetworkSender.RelayProposal.
func (a *Adaptor) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	return a.sender.RelayProposal(proposal, exceptPeer)
}

// RelayValidation forwards a peer-originated validation, excluding exceptPeer.
// Mirrors RelayProposal.
func (a *Adaptor) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	return a.sender.RelayValidation(validation, exceptPeer)
}

// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with originPeer
// and seenPeers (known-havers). See NetworkSender.UpdateRelaySlot.
func (a *Adaptor) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	a.sender.UpdateRelaySlot(validatorKey, originPeer, seenPeers)
}

// SetOnTxSetRequested registers a callback invoked at the start of every
// RequestTxSet, so the router can reset throttle/attempt bookkeeping on the
// in-flight acquisition. Set once at startup; not concurrency-safe.
func (a *Adaptor) SetOnTxSetRequested(cb func(consensus.TxSetID)) {
	a.onTxSetRequested = cb
}

func (a *Adaptor) RequestTxSet(id consensus.TxSetID) error {
	if a.onTxSetRequested != nil {
		a.onTxSetRequested(id)
	}
	return a.sender.RequestTxSet(id)
}

func (a *Adaptor) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool, indirect bool) error {
	return a.sender.RequestTxSetMissingNodes(id, nodeIDs, excluded, indirect)
}

func (a *Adaptor) RequestTxSetMissingNodesFromPeer(id consensus.TxSetID, nodeIDs [][]byte, peerID uint64, indirect bool) error {
	return a.sender.RequestTxSetMissingNodesFromPeer(id, nodeIDs, peerID, indirect)
}

func (a *Adaptor) RequestLedger(id consensus.LedgerID) error {
	// Each call is a BROADCAST TMGetLedger charged at every peer, and checkLedger
	// retries every heartbeat; rippled paces retries on the InboundLedger timer
	// (~3s), so rate-limit per hash to match.
	now := time.Now()
	a.reqLedgerMu.Lock()
	if last, ok := a.reqLedgerLast[id]; ok && now.Sub(last) < 3*time.Second {
		a.reqLedgerMu.Unlock()
		return nil
	}
	if len(a.reqLedgerLast) > 64 {
		clear(a.reqLedgerLast)
	}
	a.reqLedgerLast[id] = now
	a.reqLedgerMu.Unlock()
	return a.sender.RequestLedger(id)
}

func (a *Adaptor) RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error {
	return a.sender.RequestLedgerByHashAndSeq(hash, seq)
}

func (a *Adaptor) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error {
	return a.sender.RequestLedgerBaseFromPeer(peerID, hash, seq)
}

// RequestReplayDelta delegates to the network sender, sending a single
// TMReplayDeltaRequest and awaiting one TMReplayDeltaResponse.
func (a *Adaptor) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	return a.sender.RequestReplayDelta(peerID, hash)
}

func (a *Adaptor) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, indirect bool) error {
	return a.sender.RequestStateNodes(peerID, ledgerHash, nodeIDs, indirect)
}

func (a *Adaptor) RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte, indirect bool) error {
	return a.sender.RequestTransactionNodes(peerID, ledgerHash, nodeIDs, indirect)
}

// EngineConfigForReplay returns the shared (non-per-ledger) tx.EngineConfig
// for replaying a ledger anchored on parent (fees from parent's FeeSettings
// SLE). The caller overrides the per-ledger fields from the target header.
func (a *Adaptor) EngineConfigForReplay(parent *ledger.Ledger) tx.EngineConfig {
	if a.ledgerService == nil {
		return tx.EngineConfig{}
	}
	return a.ledgerService.EngineConfigForReplay(parent)
}

// PeerSupportsReplay reports whether the peer advertised ledger-replay during
// handshake. Delegates to NetworkSender.
func (a *Adaptor) PeerSupportsReplay(peerID uint64) bool {
	return a.sender.PeerSupportsReplay(peerID)
}

// ReplayCapablePeersExcluding returns up to max ledger-replay peers, omitting
// excluded. See NetworkSender.ReplayCapablePeersExcluding.
func (a *Adaptor) ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64 {
	return a.sender.ReplayCapablePeersExcluding(excluded, max)
}

// IncPeerBadData delegates to NetworkSender so Router can charge a peer through
// the adaptor. See NetworkSender.IncPeerBadData.
func (a *Adaptor) IncPeerBadData(peerID uint64, reason string) {
	a.sender.IncPeerBadData(peerID, reason)
}

// GetParentLedgerForReplay returns the closed ledger at seq-1 (the anchor for
// replaying a delta into seq). Returns nil if the parent is unknown, seq <= 1,
// no service is wired, or the parent is still open — an open ledger's hash is
// unset until Close, so it cannot anchor the chain.
func (a *Adaptor) GetParentLedgerForReplay(seq uint32) *ledger.Ledger {
	if seq <= 1 || a.ledgerService == nil {
		return nil
	}
	parent, err := a.ledgerService.GetLedgerBySequence(seq - 1)
	if err != nil || parent == nil {
		return nil
	}
	if !parent.IsClosed() {
		return nil
	}
	return parent
}

func (a *Adaptor) SendToPeer(peerID uint64, frame []byte) error {
	return a.sender.SendToPeer(peerID, frame)
}

// ShouldShedLedgerRequest delegates to NetworkSender so Router can gate
// ledger-body serving through the adaptor.
func (a *Adaptor) ShouldShedLedgerRequest(peerID uint64, loadedLocal bool) bool {
	return a.sender.ShouldShedLedgerRequest(peerID, loadedLocal)
}

// PeerWithLedger delegates to NetworkSender; the Router uses it to relay an
// unsatisfiable GetLedger to a peer that can serve the ledger.
func (a *Adaptor) PeerWithLedger(target [32]byte, seq uint32, exclude uint64) (uint64, bool) {
	return a.sender.PeerWithLedger(target, seq, exclude)
}

// PeersWithLedger delegates to NetworkSender; the Router uses it to broaden a
// stalled acquisition's source-peer set. See Overlay.PeersWithLedger.
func (a *Adaptor) PeersWithLedger(target [32]byte, seq uint32, excluded []uint64, max int) []uint64 {
	return a.sender.PeersWithLedger(target, seq, excluded, max)
}

// PeerWithTxSet delegates to NetworkSender; the Router uses it to relay an
// unsatisfiable liTS_CANDIDATE GetLedger to a peer that advertised the
// tx-set.
func (a *Adaptor) PeerWithTxSet(target [32]byte, exclude uint64) (uint64, bool) {
	return a.sender.PeerWithTxSet(target, exclude)
}

// NotePeerHasTxSet delegates to NetworkSender; the Router calls it on
// inbound mtHAVE_TRANSACTION_SET{tsHAVE} so PeerWithTxSet can later find
// the advertising peer.
func (a *Adaptor) NotePeerHasTxSet(peerID uint64, hash [32]byte) {
	a.sender.NotePeerHasTxSet(peerID, hash)
}

// LedgerService returns the underlying ledger service for direct queries.
func (a *Adaptor) LedgerService() *service.Service {
	return a.ledgerService
}

func (a *Adaptor) GetLedger(id consensus.LedgerID) (consensus.Ledger, error) {
	l, err := a.ledgerService.GetLedgerByHash([32]byte(id))
	if err != nil {
		return nil, ErrLedgerNotFound
	}
	return WrapLedger(l), nil
}

// GetLedgerBySeq returns the locally-held CLOSED ledger at seq from adopted
// history only — never the mutable open ledger — so the catch-up walk can't
// adopt an unclosed ledger as prevLedger.
func (a *Adaptor) GetLedgerBySeq(seq uint32) (consensus.Ledger, error) {
	l, err := a.ledgerService.GetAdoptedLedgerBySequence(seq)
	if err != nil || l == nil {
		return nil, ErrLedgerNotFound
	}
	return WrapLedger(l), nil
}

func (a *Adaptor) GetLastClosedLedger() (consensus.Ledger, error) {
	l := a.ledgerService.GetClosedLedger()
	if l == nil {
		return nil, ErrLedgerNotFound
	}
	return WrapLedger(l), nil
}

// GetValidatedLedgerHash returns the hash of the most recent fully-validated
// ledger (for sfValidatedHash), or the zero LedgerID when none has crossed
// trusted-validation quorum.
func (a *Adaptor) GetValidatedLedgerHash() consensus.LedgerID {
	vl := a.validatedLedger()
	if vl == nil {
		return consensus.LedgerID{}
	}
	return consensus.LedgerID(vl.Hash())
}

func (a *Adaptor) GetMaxDisallowedLedgerSeq() uint32 {
	return a.maxDisallowedSeq
}

func (a *Adaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, closeTimeCorrect bool) (consensus.Ledger, error) {
	// Unwrap the parent for the service. Critical for chain switching: the
	// parent may differ from the service's closedLedger after wrong-ledger detection.
	var parentLedger *ledger.Ledger
	if w, ok := parent.(*LedgerWrapper); ok {
		parentLedger = w.Unwrap()
	}
	// context.TODO: BuildLedger's interface has no context, so persistence
	// here can't be cancelled by the engine (#185).
	seq, err := a.ledgerService.AcceptConsensusResult(context.TODO(), parentLedger, txSet.Txs(), closeTime, closeTimeCorrect)
	if err != nil {
		return nil, err
	}

	l, err := a.ledgerService.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}
	return WrapLedger(l), nil
}

func (a *Adaptor) ValidateLedger(ledger consensus.Ledger) error {
	wrapper, ok := ledger.(*LedgerWrapper)
	if !ok {
		return errors.New("unexpected ledger type")
	}
	l := wrapper.Unwrap()
	if l == nil {
		return errors.New("nil ledger")
	}
	if _, err := l.StateMapHash(); err != nil {
		return err
	}
	return nil
}

func (a *Adaptor) StoreLedger(ledger consensus.Ledger) error {
	// Already persisted by AcceptConsensusResult in BuildLedger; no-op for now.
	return nil
}

// GetPendingTxs returns the raw tx blobs in the persistent open view.
// Used by the engine for the open-phase "anyTransactions" gate. No
// per-call filter.
func (a *Adaptor) GetPendingTxs() [][]byte {
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.OpenLedgerTxs()
}

// GetProposableTxs returns the tx set the node will propose this round.
// parent is threaded through for future negative-UNL / amendment-vote
// filtering; today it returns the same snapshot as GetPendingTxs.
func (a *Adaptor) GetProposableTxs(parent consensus.Ledger) [][]byte {
	_ = parent
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.OpenLedgerTxs()
}

// GenerateFlagLedgerPseudoTxs runs the fee-vote and amendment-vote producers
// and returns their concatenated pseudo-tx blobs for the proposal initial
// set, applying the negative-UNL filter and quorum gate. XRPFees and
// fixAmendmentMajorityCalc behavior is read from the parsed Amendments SLE
// since Ledger.Rules is nil at this boundary.
func (a *Adaptor) GenerateFlagLedgerPseudoTxs(prevLedger consensus.Ledger, parentValidations []*consensus.Validation) [][]byte {
	if a.ledgerService == nil {
		return nil
	}
	prev, err := a.ledgerService.GetLedgerByHash([32]byte(prevLedger.ID()))
	if err != nil || prev == nil {
		return nil
	}
	upcomingSeq := prev.Sequence() + 1

	filtered := a.filterNegativeUNL(parentValidations)

	// Quorum gate. Standalone reports quorum 0.
	if len(filtered) < a.GetQuorum() {
		return nil
	}

	enabled, majorities, ok := a.readAmendmentsSLE(prev)
	if !ok {
		return nil
	}

	var blobs [][]byte
	if extra := a.runFeeVote(prev, upcomingSeq, filtered, enabled); len(extra) > 0 {
		blobs = append(blobs, extra...)
	}
	if extra := a.runAmendmentVote(prev, upcomingSeq, filtered, enabled, majorities); len(extra) > 0 {
		blobs = append(blobs, extra...)
	}
	return blobs
}

// filterNegativeUNL returns vals minus any validations signed by
// validators currently on the negative UNL.
func (a *Adaptor) filterNegativeUNL(vals []*consensus.Validation) []*consensus.Validation {
	return excludeNegativeUNL(vals, a.GetNegativeUNL())
}

// excludeNegativeUNL is the pure core of the negUNL filter. Empty negUNL
// returns vals unchanged.
func excludeNegativeUNL(vals []*consensus.Validation, negUNL []consensus.NodeID) []*consensus.Validation {
	if len(vals) == 0 || len(negUNL) == 0 {
		return vals
	}
	skip := make(map[consensus.NodeID]struct{}, len(negUNL))
	for _, id := range negUNL {
		skip[id] = struct{}{}
	}
	out := make([]*consensus.Validation, 0, len(vals))
	for _, v := range vals {
		if _, banned := skip[v.NodeID]; banned {
			continue
		}
		out = append(out, v)
	}
	return out
}

func (a *Adaptor) GetTxSet(id consensus.TxSetID) (consensus.TxSet, error) {
	ts, ok := a.txSetCache.Get(id)
	if !ok {
		return nil, ErrTxSetNotFound
	}
	return ts, nil
}

func (a *Adaptor) BuildTxSet(txs [][]byte) (consensus.TxSet, error) {
	ts, err := NewTxSet(txs)
	if err != nil {
		return nil, err
	}
	a.txSetCache.Put(ts)
	// Announce each set hash at most once (and never the empty set): the engine
	// rebuilds sets frequently and peers charge "useless data" for every
	// duplicate tsHAVE — rippled never re-announces a hash a peer has seen.
	id := ts.ID()
	if a.onTxSetBuilt != nil && id != (consensus.TxSetID{}) {
		a.announcedSetsMu.Lock()
		_, dup := a.announcedSets[id]
		if !dup {
			if len(a.announcedSets) > 512 {
				clear(a.announcedSets)
			}
			a.announcedSets[id] = struct{}{}
		}
		a.announcedSetsMu.Unlock()
		if !dup {
			a.onTxSetBuilt(id)
		}
	}
	return ts, nil
}

// SetOnTxSetBuilt installs a callback invoked once per BuildTxSet (after
// caching); the CLI wires it to Overlay.BroadcastHaveTxSet. nil clears.
func (a *Adaptor) SetOnTxSetBuilt(cb func(consensus.TxSetID)) {
	a.onTxSetBuilt = cb
}

// HasTx reports whether the persistent open view contains this tx.
// Used by the peer protocol for HaveSet / txSet-acquire negotiation.
func (a *Adaptor) HasTx(id consensus.TxID) bool {
	if a.ledgerService == nil {
		return false
	}
	return a.ledgerService.OpenLedgerHasTx([32]byte(id))
}

// GetTx returns the raw tx blob if it is in the persistent open view.
func (a *Adaptor) GetTx(id consensus.TxID) ([]byte, error) {
	if a.ledgerService == nil {
		return nil, errors.New("ledgerService unavailable")
	}
	blob, ok := a.ledgerService.OpenLedgerGetTx([32]byte(id))
	if !ok {
		return nil, errors.New("transaction not found")
	}
	return blob, nil
}

// AddPendingTx submits a tx blob through the persistent open-ledger view.
// local=true (RPC) holds it in the LocalTxs pool until it applies or ages
// out; local=false (peer-relay) leaves resends to the peer.
func (a *Adaptor) AddPendingTx(blob []byte, local bool) {
	_, _ = a.SubmitPendingTx(blob, local)
}

// SubmitPendingTx is AddPendingTx with the classification result surfaced, so
// the peer-relay path can gate re-broadcast on a non-Failure result.
func (a *Adaptor) SubmitPendingTx(blob []byte, local bool) (openledger.Result, error) {
	if a.ledgerService == nil {
		return openledger.ResultFailure, errors.New("ledger service unavailable")
	}
	res, err := a.ledgerService.SubmitOpenLedgerTx(blob, local)
	if err != nil {
		a.logger.Warn("openLedger submit failed",
			"err", err,
			"blob_size", len(blob),
			"local", local,
		)
	}
	return res, err
}

func (a *Adaptor) IsValidator() bool {
	return a.identity != nil
}

func (a *Adaptor) GetValidatorKey() (consensus.NodeID, error) {
	if a.identity == nil {
		return consensus.NodeID{}, ErrNoValidatorKey
	}
	return a.identity.NodeID, nil
}

// GetValidatorSigningKey returns the validator's 33-byte signing pubkey
// (ephemeral in token mode, master in seed-only mode) for validator_info /
// server_info. The 20-byte NodeID from GetValidatorKey must NOT be used here.
func (a *Adaptor) GetValidatorSigningKey() ([33]byte, error) {
	if a.identity == nil {
		return [33]byte{}, ErrNoValidatorKey
	}
	return a.identity.SigningKey, nil
}

func (a *Adaptor) SignProposal(proposal *consensus.Proposal) error {
	if a.identity == nil {
		return ErrNoValidatorKey
	}
	return a.identity.SignProposal(proposal)
}

func (a *Adaptor) SignValidation(validation *consensus.Validation) error {
	if a.identity == nil {
		return ErrNoValidatorKey
	}
	return a.identity.SignValidation(validation)
}

func (a *Adaptor) VerifyProposal(proposal *consensus.Proposal) error {
	return VerifyProposal(proposal)
}

func (a *Adaptor) VerifyValidation(validation *consensus.Validation) error {
	return VerifyValidation(validation)
}

func (a *Adaptor) IsTrusted(node consensus.NodeID) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	_, ok := a.trustedSet[node]
	return ok
}

func (a *Adaptor) GetTrustedValidators() []consensus.NodeID {
	a.mu.Lock()
	defer a.mu.Unlock()
	result := make([]consensus.NodeID, len(a.trustedValidators))
	copy(result, a.trustedValidators)
	return result
}

// SetTrustedValidators atomically replaces the operator-trusted validator set.
//
// validators and masterKeys are index-aligned and MUST be the same length; a
// mismatch is logged at WARN and the longer slice truncated (defensive only).
// Pass (nil, nil) to clear the set (standalone transition). The single entry
// point for every UNL-change trigger. Concurrency-safe; copies its inputs.
func (a *Adaptor) SetTrustedValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	if len(validators) != len(masterKeys) && (len(validators) > 0 || len(masterKeys) > 0) {
		a.logger.Warn("SetTrustedValidators: validators / masterKeys length mismatch; truncating to shorter",
			"validators_count", len(validators),
			"master_keys_count", len(masterKeys),
		)
		n := min(len(masterKeys), len(validators))
		validators = validators[:n]
		masterKeys = masterKeys[:n]
	}

	vCopy := make([]consensus.NodeID, len(validators))
	copy(vCopy, validators)
	newSet := make(map[consensus.NodeID]struct{}, len(validators))
	for _, v := range validators {
		newSet[v] = struct{}{}
	}
	var mkCopy [][33]byte
	if len(masterKeys) > 0 {
		mkCopy = make([][33]byte, len(masterKeys))
		copy(mkCopy, masterKeys)
	}

	a.mu.Lock()
	a.trustedValidators = vCopy
	a.trustedSet = newSet
	a.trustedMasterKeys = mkCopy
	a.mu.Unlock()

	// trustedVotes is assigned once in New and never reassigned, so the
	// unlocked read is safe (TrustedVotes has its own mutex). Called after
	// releasing a.mu to avoid lock nesting.
	if a.trustedVotes != nil {
		a.trustedVotes.TrustChanged(vCopy)
	}
}

// GetQuorum returns the current quorum requirement, recomputed on
// every call to account for negative-UNL changes:
// max(ceil(0.8 * (trusted - disabled)), ceil(0.6 * trusted)).
func (a *Adaptor) GetQuorum() int {
	// GetNegativeUNL takes its own lock, so resolve it before locking a.mu.
	negUNL := a.GetNegativeUNL()

	a.mu.Lock()
	trusted := len(a.trustedValidators)
	// Count only negUNL entries that are actually in our trusted UNL: a
	// disabled validator we don't trust must not lower our quorum (rippled
	// ValidatorList::updateTrusted intersects the negUNL with the trusted
	// keys, ValidatorList.cpp:2064-2070).
	disabled := 0
	for _, id := range negUNL {
		if _, ok := a.trustedSet[id]; ok {
			disabled++
		}
	}
	a.mu.Unlock()
	return computeQuorum(trusted, disabled)
}

// computeQuorum is the pure arithmetic behind GetQuorum: the minimum trusted,
// non-negUNL signatures to fully validate a ledger.
//
//   - standalone (trusted==0): 0 — no quorum gate.
//   - effective > 0: max(ceil(0.8 * effective), ceil(0.6 * trusted)). The 0.6
//     term is the AbsoluteMinimumQuorum floor (negative-UNL amendment) so a
//     large negUNL can't drop the bar below 60% of the full UNL.
//   - effective <= 0 (whole UNL on negUNL): math.MaxInt — an unreachable
//     quorum so no transient vote fires a spurious full-validation callback.
func computeQuorum(trusted, disabled int) int {
	if trusted == 0 {
		return 0
	}
	effective := trusted - disabled
	if effective <= 0 {
		return math.MaxInt
	}
	return max((effective*4+4)/5, (trusted*3+4)/5)
}

// disabledValidatorMasters reads the ltNEGATIVE_UNL SLE from the validated
// ledger and returns the 33-byte master pubkeys of disabled validators.
// Returns nil when there's no ledger service, no validated ledger, no SLE, or
// a malformed SLE (logged at warn, treated as empty). Bad entries are skipped.
func (a *Adaptor) disabledValidatorMasters() [][33]byte {
	l := a.validatedLedger()
	if l == nil {
		return nil
	}
	data, err := l.Read(keylet.NegativeUNL())
	if err != nil || len(data) == 0 {
		return nil
	}
	sle, err := pseudo.ParseNegativeUNLSLE(data)
	if err != nil {
		a.logger.Warn("failed to parse NegativeUNL SLE; treating as empty",
			"err", err,
			"seq", l.Sequence(),
		)
		return nil
	}
	if len(sle.DisabledValidators) == 0 {
		return nil
	}
	out := make([][33]byte, 0, len(sle.DisabledValidators))
	for _, dv := range sle.DisabledValidators {
		if len(dv.PublicKey) != 33 {
			continue
		}
		var master [33]byte
		copy(master[:], dv.PublicKey)
		out = append(out, master)
	}
	return out
}

// GetNegativeUNLMasters returns the 33-byte master pubkeys of disabled
// validators (raw, not the NodeIDs GetNegativeUNL returns). Used by the
// `validators` RPC.
func (a *Adaptor) GetNegativeUNLMasters() [][33]byte {
	return a.disabledValidatorMasters()
}

// GetNegativeUNL returns the NodeIDs of validators disabled on the validated
// ledger's ltNEGATIVE_UNL SLE. Returns nil when there's no ledger service, no
// validated ledger, no SLE, or a parse failure (logged at warn).
func (a *Adaptor) GetNegativeUNL() []consensus.NodeID {
	masters := a.disabledValidatorMasters()
	if masters == nil {
		return nil
	}
	// NegativeUNL stores 33-byte master keys; match against the 20-byte
	// calcNodeID(master) digest.
	out := make([]consensus.NodeID, 0, len(masters))
	for _, master := range masters {
		out = append(out, consensus.CalcNodeID(master))
	}
	return out
}

// GetCookie returns this adaptor's boot-lifetime cookie for emission
// via sfCookie on every outgoing validation.
func (a *Adaptor) GetCookie() uint64 {
	return a.cookie
}

// GetServerVersion returns the 64-bit sfServerVersion identifier. It avoids
// rippled's top bit (0x8000...) so go-xrpl isn't counted as rippled in peer
// version statistics.
func (a *Adaptor) GetServerVersion() uint64 {
	// Low bits reserved for a future semantic version; zero for now.
	return goxrplServerVersionTag
}

// GetLoadFee returns the local load_fee for outbound validations: the max of
// the local and cluster fee, or 0 ("omit") when that collapses to LoadBase.
func (a *Adaptor) GetLoadFee() uint32 {
	if a.ledgerService == nil {
		return 0
	}
	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		return 0
	}
	fee := ft.GetLocalFee()
	if c := ft.GetClusterFee(); c > fee {
		fee = c
	}
	if fee <= ft.GetLoadBase() {
		return 0
	}
	return fee
}

// GetFeeVote returns this validator's fee-vote stance and whether the
// post-XRPFees rules apply (featureXRPFees enabled).
func (a *Adaptor) GetFeeVote() consensus.FeeVoteResult {
	return consensus.FeeVoteResult{
		BaseFee:          a.feeVote.BaseFee,
		ReserveBase:      uint64(a.feeVote.ReserveBase),
		ReserveIncrement: uint64(a.feeVote.ReserveIncrement),
		PostXRPFees:      a.IsFeatureEnabled("XRPFees"),
	}
}

// currentAmendmentStances returns the validator's live per-amendment vote
// stances. With a live amendment table wired, stances are derived fresh from it
// (registry defaults, then operator veto → abstain, upvote → VoteUp) so changes
// take effect without restart; otherwise the construction-time map is returned.
func (a *Adaptor) currentAmendmentStances() map[[32]byte]amendmentvote.Stance {
	if a.amendmentTable == nil {
		return a.amendmentStances
	}
	stances := make(map[[32]byte]amendmentvote.Stance)
	for _, f := range amendment.AllFeatures() {
		switch {
		case f.Vote == amendment.VoteObsolete:
			stances[f.ID] = amendmentvote.VoteObsolete
		case a.amendmentTable.IsVetoed(f.ID):
			// vetoed → abstain (leave unset)
		case f.Supported == amendment.SupportedYes && a.amendmentTable.IsUpVoted(f.ID):
			// Operator upvote, supported amendments only.
			stances[f.ID] = amendmentvote.VoteUp
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			stances[f.ID] = amendmentvote.VoteUp
		}
	}
	return stances
}

// validatedLedger returns the most recent fully-validated ledger, or nil
// when no ledger service is wired or no ledger has been validated yet.
func (a *Adaptor) validatedLedger() *ledger.Ledger {
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.GetValidatedLedger()
}

// validatedRules returns the amendment Rules of the currently-validated
// ledger, or nil when no ledger service is wired or no ledger has been
// validated yet.
func (a *Adaptor) validatedRules() *amendment.Rules {
	if l := a.validatedLedger(); l != nil {
		return l.Rules()
	}
	return nil
}

// featureEnabled reports whether the named amendment is enabled in rules.
// unknownDefault is returned when rules is nil or the feature name is not
// recognised — lax (true) for the validation-broadcast path, strict
// (false) for engine-level gates.
func featureEnabled(rules *amendment.Rules, name string, unknownDefault bool) bool {
	if rules == nil {
		return unknownDefault
	}
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return unknownDefault
	}
	return rules.Enabled(f.ID)
}

// GetAmendmentVote returns the amendment IDs this validator votes FOR on the
// next flag ledger, filtered against already-enabled amendments and canonically
// sorted (so equal stances yield byte-identical validations). nil when there's
// nothing to vote for.
func (a *Adaptor) GetAmendmentVote() [][32]byte {
	stances := a.currentAmendmentStances()
	if len(stances) == 0 {
		return nil
	}

	// Filter out amendments already enabled on the validated ledger. No
	// ledger/rules → nothing filtered (an un-synced node isn't validating).
	rules := a.validatedRules()

	out := make([][32]byte, 0, len(stances))
	for id, stance := range stances {
		if stance != amendmentvote.VoteUp {
			continue
		}
		if rules != nil && rules.Enabled(id) {
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		return nil
	}

	// Canonical sort for byte-identical validations across validators.
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// IsFeatureEnabled reports whether the named amendment is enabled on the
// validated ledger's rules, gating optional STValidation fields (e.g.
// sfValidatedHash under featureHardenedValidations). Returns true on "unknown"
// (no service, pre-sync, or unrecognised name) as the safe mainnet default.
func (a *Adaptor) IsFeatureEnabled(name string) bool {
	return featureEnabled(a.validatedRules(), name, true)
}

// IsFeatureEnabledOnLedger reports whether the named amendment is enabled in
// the supplied ledger's own rules — a strict gate: any miss (nil ledger,
// unrecognised type, nil rules, unknown name) is "not enabled".
func (a *Adaptor) IsFeatureEnabledOnLedger(l consensus.Ledger, name string) bool {
	if l == nil {
		return false
	}
	w, ok := l.(*LedgerWrapper)
	if !ok {
		return false
	}
	return featureEnabled(w.Unwrap().Rules(), name, false)
}

// IsStandalone reports whether the node is configured for standalone
// (single-node) operation. Used by the engine to bypass the
// proposing-mode gate on flag-ledger pseudo-tx injection.
func (a *Adaptor) IsStandalone() bool {
	if a.ledgerService == nil {
		return false
	}
	return a.ledgerService.IsStandalone()
}

func (a *Adaptor) Now() time.Time {
	return time.Now().Add(time.Duration(a.closeOffsetNs.Load()))
}

// CloseOffset returns the current consensus-derived close-time offset.
// Surfaced via server_info as close_time_offset when |offset| >= 60s.
func (a *Adaptor) CloseOffset() time.Duration {
	return time.Duration(a.closeOffsetNs.Load())
}

func (a *Adaptor) CloseTimeResolution() time.Duration {
	// Round on the resolution of the ledger BEING BUILT — the parent's stepped
	// one rung on the ladder (rippled Consensus.h:724-727
	// getNextLedgerTimeResolution). The parent's raw value would round close-time
	// votes differently at ladder boundaries: a different agreed close time is a
	// different ledger hash — a fork.
	l := a.ledgerService.GetClosedLedger()
	if l != nil {
		hdr := l.Header()
		res := consensus.GetNextLedgerTimeResolution(
			hdr.CloseTimeResolution,
			hdr.GetCloseAgree(),
			hdr.LedgerIndex+1,
		)
		if res >= 2 && res <= 120 {
			return time.Duration(res) * time.Second
		}
	}
	return 30 * time.Second // protocol default
}

// PrevCloseTimeResolution returns the closed ledger's raw stored resolution,
// the basis for the empty-ledger idle interval (rippled Consensus.h:1212-1214
// uses previousLedger_.closeTimeResolution(), not the next-ledger value).
func (a *Adaptor) PrevCloseTimeResolution() time.Duration {
	if l := a.ledgerService.GetClosedLedger(); l != nil {
		if res := l.Header().CloseTimeResolution; res >= 2 && res <= 120 {
			return time.Duration(res) * time.Second
		}
	}
	return 30 * time.Second // protocol default
}

// AdjustCloseTime weight-averages raw close times and applies quarter-step
// damping toward the network's view of time. Arithmetic is in whole seconds:
// NetClock is second-granular and a ns replace would never decay toward zero.
func (a *Adaptor) AdjustCloseTime(rawCloseTimes consensus.CloseTimes) {
	if rawCloseTimes.Self.IsZero() {
		return
	}

	selfSecs := rawCloseTimes.Self.Unix()
	totalSecs := selfSecs
	count := int64(1)
	for t, v := range rawCloseTimes.Peers {
		count += int64(v)
		totalSecs += t.Unix() * int64(v)
	}
	avgSecs := (totalSecs + count/2) / count
	bySecs := avgSecs - selfSecs

	currentSecs := int64(time.Duration(a.closeOffsetNs.Load()) / time.Second)
	if bySecs == 0 && currentSecs == 0 {
		return
	}

	// Integer division truncates toward zero, which the quarter-step
	// damping branches below rely on.
	var newSecs int64
	switch {
	case bySecs > 1:
		newSecs = currentSecs + (bySecs+3)/4
	case bySecs < -1:
		newSecs = currentSecs + (bySecs-3)/4
	default:
		newSecs = (currentSecs * 3) / 4
	}

	a.closeOffsetNs.Store(int64(time.Duration(newSecs) * time.Second))

	if newSecs != currentSecs {
		a.logger.Debug("adjusted close time offset",
			"offset_s", newSecs,
			"by_s", bySecs,
			"peers", len(rawCloseTimes.Peers),
		)
	}
}

func (a *Adaptor) GetOperatingMode() consensus.OperatingMode {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.operatingMode
}

func (a *Adaptor) SetOperatingMode(mode consensus.OperatingMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.operatingMode = mode
	if a.stateAcct != nil {
		// Held under a.mu so the field and the accounting transition share one
		// serialization order; the tracker's own mutex never re-enters a.mu.
		a.stateAcct.transition(mode)
	}
}

// StateAccounting returns the snapshot server_info uses for state_accounting
// and the server_state_duration_us / initial_sync_duration_us fields. Zero
// value when constructed without a tracker. stateAcct is set once in New(), so
// no Adaptor lock is needed (the tracker has its own mutex).
func (a *Adaptor) StateAccounting() StateAccountingSnapshot {
	if a.stateAcct == nil {
		return StateAccountingSnapshot{}
	}
	return a.stateAcct.snapshot()
}

// OnConsensusReached logs the close and fires the consensus-phase hook; the
// open-ledger view is already advanced by AcceptConsensusResult.
//
// Does NOT mark the ledger validated — that only happens at trusted-validation
// quorum (OnLedgerFullyValidated). Local consensus != network agreement.
func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation, roundTime time.Duration) {
	a.logger.Info("Consensus reached",
		"ledger_seq", ledger.Seq(),
		"validations", len(validations),
		"round_time", roundTime,
	)

	if a.ledgerService != nil {
		// Feed round duration to the service so TxQ sees the timeLeap flag when
		// consensus crossed the 5s slow-consensus threshold.
		a.ledgerService.SetLastConsensusRoundTime(roundTime)

		a.emitConsensusPhase("accepted")
	}

	a.maybePromoteAfterConsensus(ledger)
}

// emitConsensusPhase delivers a consensus-phase notification through a single
// ordered dispatcher (started on first use). Enqueue is non-blocking: a slow
// hook drops the (advisory) notification rather than stalling consensus.
func (a *Adaptor) emitConsensusPhase(phase string) {
	if a.ledgerService == nil {
		return
	}
	a.consensusPhaseOnce.Do(func() {
		a.consensusPhaseCh = make(chan string, 64)
		go func() {
			for p := range a.consensusPhaseCh {
				if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
					hooks.OnConsensusPhase(p)
				}
			}
		}()
	})
	select {
	case a.consensusPhaseCh <- phase:
	default:
		slog.Warn("consensus phase hook buffer full; dropping notification",
			"t", "adaptor.emitConsensusPhase", "phase", phase)
	}
}

// maybePromoteAfterConsensus auto-promotes the operating mode after a
// successful consensus close (a completed round is evidence we're aligned):
//
//	CONNECTED | SYNCING  → TRACKING
//	CONNECTED | TRACKING → FULL when the just-closed ledger is recent
//	                       (now < ledger.CloseTime() + 2 * resolution)
//
// Both branches are gated on !networkLedgerDiffers(ledger). Without this a
// fresh genesis bootstrap would deadlock at OpModeConnected (no peer to acquire
// from fires the normal Tracking transitions).
func (a *Adaptor) maybePromoteAfterConsensus(ledger consensus.Ledger) {
	if ledger == nil {
		return
	}
	current := a.GetOperatingMode()
	if current == consensus.OpModeDisconnected || current == consensus.OpModeFull {
		return
	}

	if a.networkLedgerDiffers(ledger, current) {
		a.logger.Info("operating mode promotion deferred — network prefers a different LCL",
			"mode", current.String(),
			"ledger_seq", ledger.Seq(),
		)
		return
	}

	target := current
	if current == consensus.OpModeConnected || current == consensus.OpModeSyncing {
		target = consensus.OpModeTracking
	}
	if target == consensus.OpModeConnected || target == consensus.OpModeTracking {
		resolution := a.CloseTimeResolution()
		if a.Now().Before(ledger.CloseTime().Add(2 * resolution)) {
			target = consensus.OpModeFull
		}
	}
	if target == current {
		return
	}
	a.SetOperatingMode(target)
	a.logger.Info("operating mode auto-promoted after consensus",
		"from", current.String(),
		"to", target.String(),
		"ledger_seq", ledger.Seq(),
	)
}

// networkLedgerDiffers reports whether the network-preferred LCL differs from
// the one we just closed (the promotion gate's signal). False when the
// preferred LCL is our own.
func (a *Adaptor) networkLedgerDiffers(ledger consensus.Ledger, mode consensus.OperatingMode) bool {
	return a.preferredLCL(ledger, mode) != ledger.ID()
}

// preferredLCL picks the network-preferred last closed ledger, mirroring
// rippled Validations::getPreferredLCL (Validations.h:935-960): the
// trie-preferred ledger first, the most-supported trusted-validation tip
// when the trie has none, and the dominant peer-reported LCL as the last
// fallback. The only sequence gate is the last fully-validated index —
// never rewinding behind it; a preferred ledger at or below our own seq on
// a different chain is still a switch (Validations.h:892-895).
func (a *Adaptor) preferredLCL(ledger consensus.Ledger, mode consensus.OperatingMode) consensus.LedgerID {
	ourLCL := ledger.ID()
	var minSeq uint32
	if a.ledgerService != nil {
		minSeq = a.ledgerService.GetValidatedLedgerIndex()
	}

	if h := a.validationHistorian; h != nil {
		if id, seq, ok := h.GetPreferred(a.lastIssuedValidationSeq.Load()); ok {
			id, seq = a.resolvePreferredVsCurrent(id, seq, ledger)
			if seq >= minSeq {
				return id
			}
			return ourLCL
		}
		// No-trie fallback over trusted-validation tips (the acquiring_
		// majority is handled inside GetPreferred); already filtered to
		// seq >= minSeq.
		if id, _, ok := h.PreferredFromValidations(minSeq); ok {
			return id
		}
	}

	// Peer-LCL fallback. Seed our own LCL at zero and increment it when
	// we are already >= TRACKING, then pick the dominant ledger (ties
	// broken by larger ID).
	counts := map[consensus.LedgerID]uint32{ourLCL: 0}
	if mode >= consensus.OpModeTracking {
		counts[ourLCL]++
	}
	for _, p := range a.PeerReportedLedgers() {
		counts[p]++
	}
	best := ourLCL
	for id, c := range counts {
		// Larger count wins; ties break on larger ID.
		if c > counts[best] || (c == counts[best] && bytes.Compare(id[:], best[:]) > 0) {
			best = id
		}
	}
	return best
}

// resolvePreferredVsCurrent applies rippled getPreferred's stay/switch rules
// (Validations.h:881-898) to the trie-preferred tip: our own immediate child
// on our chain is not a switch (we may be about to build it), a tip ahead of
// us always wins, and a tip at or below our seq wins only when our chain's
// ledger at that seq differs (a fork).
func (a *Adaptor) resolvePreferredVsCurrent(prefID consensus.LedgerID, prefSeq uint32, ledger consensus.Ledger) (consensus.LedgerID, uint32) {
	ourLCL := ledger.ID()
	ourSeq := ledger.Seq()
	if prefSeq == ourSeq+1 {
		if l, err := a.GetLedger(prefID); err == nil && l != nil && l.ParentID() == ourLCL {
			return ourLCL, ourSeq
		}
	}
	if prefSeq > ourSeq {
		return prefID, prefSeq
	}
	if a.ancestorOf(ledger, prefSeq) != prefID {
		return prefID, prefSeq
	}
	return ourLCL, ourSeq
}

// ancestorOf resolves our chain's ledger ID at targetSeq, starting from
// ledger's own parent link and walking locally-held parents. Returns the
// zero ID when the ancestry is not locally resolvable — treated as a
// different chain, like rippled's out-of-skip-list ID{0}
// (RCLValidations.cpp:78-95).
func (a *Adaptor) ancestorOf(ledger consensus.Ledger, targetSeq uint32) consensus.LedgerID {
	const maxWalk = 256 // rippled's skip-list reach
	seq := ledger.Seq()
	if targetSeq > seq || seq-targetSeq > maxWalk {
		return consensus.LedgerID{}
	}
	if targetSeq == seq {
		return ledger.ID()
	}
	cur := ledger.ParentID()
	for s := seq - 1; s > targetSeq; s-- {
		l, err := a.GetLedger(cur)
		if err != nil || l == nil {
			return consensus.LedgerID{}
		}
		cur = l.ParentID()
	}
	return cur
}

// OnLedgerFullyValidated fires at trusted-validation quorum. It advances the
// service's validated_ledger only if our stored ledger at that seq has the
// matching hash (fork safety, keyed on the ledger not seq alone), then refreshes
// LoadFeeTrack's remoteFee from the median sfLoadFee across trusted validations.
func (a *Adaptor) OnLedgerFullyValidated(ledgerID consensus.LedgerID, seq uint32) {
	var hash [32]byte
	copy(hash[:], ledgerID[:])
	a.ledgerService.SetValidatedLedger(seq, hash)
	a.refreshRemoteFee(ledgerID)
	a.logger.Info("Ledger fully validated",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
}

// refreshRemoteFee takes the sfLoadFee of each trusted validation
// (defaulting to LoadBase when the validator omitted the field) and
// forwards the median to LoadFeeTrack.
func (a *Adaptor) refreshRemoteFee(ledgerID consensus.LedgerID) {
	if a.ledgerService == nil || a.validationHistorian == nil {
		return
	}
	ft := a.ledgerService.FeeTrack()
	if ft == nil {
		return
	}
	vals := a.validationHistorian.GetTrustedValidations(ledgerID)
	if len(vals) == 0 {
		return
	}
	base := ft.GetLoadBase()
	fees := make([]uint32, 0, len(vals))
	for _, v := range vals {
		if v == nil {
			continue
		}
		fee := v.LoadFee
		if fee == 0 {
			fee = base
		}
		fees = append(fees, fee)
	}
	if len(fees) == 0 {
		return
	}
	slices.Sort(fees)
	ft.SetRemoteFee(fees[len(fees)/2])
}

func (a *Adaptor) OnModeChange(oldMode, newMode consensus.Mode) {
	a.logger.Info("Consensus mode changed",
		"from", oldMode.String(),
		"to", newMode.String(),
	)
}

// NeedsInitialSync returns true if the node hasn't yet adopted a ledger from peers.
func (a *Adaptor) NeedsInitialSync() bool {
	return a.ledgerService.NeedsInitialSync()
}

// AdoptLedgerFromHeader adopts a peer's ledger from a serialized header.
func (a *Adaptor) AdoptLedgerFromHeader(headerData []byte) error {
	h, err := header.DeserializePrefixedHeader(headerData, true)
	if err != nil {
		// Try without prefix (some responses omit it)
		h, err = header.DeserializeHeader(headerData, true)
		if err != nil {
			return fmt.Errorf("deserialize header: %w", err)
		}
	}

	if err := a.ledgerService.AdoptLedgerHeader(h); err != nil {
		return fmt.Errorf("adopt ledger: %w", err)
	}

	// Transition to Tracking mode — the router manages the Full transition
	// once we verify our LCL matches the network.
	a.SetOperatingMode(consensus.OpModeTracking)

	a.logger.Info("Adopted peer ledger",
		"seq", h.LedgerIndex,
		"hash", fmt.Sprintf("%x", h.Hash[:8]),
	)
	return nil
}

func (a *Adaptor) OnPhaseChange(oldPhase, newPhase consensus.Phase) {
	a.logger.Debug("Consensus phase changed",
		"from", oldPhase.String(),
		"to", newPhase.String(),
	)

	// Broadcast status change so peers learn our ledger state.
	switch newPhase {
	case consensus.PhaseEstablish:
		a.broadcastStatus(message.NodeEventClosingLedger)
	case consensus.PhaseAccepted:
		a.broadcastStatus(message.NodeEventAcceptedLedger)
	}

	// Notify via the ordered dispatcher for WebSocket subscription
	// broadcasting.
	a.emitConsensusPhase(newPhase.String())
}

// broadcastStatus sends a TMStatusChange message to all peers.
func (a *Adaptor) broadcastStatus(event message.NodeEvent) {
	l := a.ledgerService.GetClosedLedger()
	if l == nil {
		return
	}

	hash := l.Hash()
	parentHash := l.ParentHash()

	status := message.NodeStatusConnected
	if a.IsValidator() {
		status = message.NodeStatusValidating
	}

	// NetworkTime is XRPL epoch seconds on the wire, not microseconds.
	networkTime := uint64(time.Now().Unix() - protocol.RippleEpochUnix)

	firstSeq := uint32(2) // genesis sequence
	lastSeq := l.Sequence()

	sc := &message.StatusChange{
		NewStatus:          status,
		NewEvent:           event,
		LedgerSeq:          l.Sequence(),
		LedgerHash:         hash[:],
		LedgerHashPrevious: parentHash[:],
		NetworkTime:        networkTime,
		FirstSeq:           &firstSeq,
		LastSeq:            &lastSeq,
	}

	if err := a.sender.BroadcastStatusChange(sc); err != nil {
		a.logger.Warn("failed to broadcast status change", "error", err)
	}
}
