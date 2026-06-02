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
// This allows testing the adaptor without a real network.
type NetworkSender interface {
	// BroadcastProposal / BroadcastValidation send OUR OWN traffic —
	// unfiltered, because rippled deliberately omits the squelch filter
	// for self-originated broadcasts (OverlayImpl.cpp:1133-1137).
	BroadcastProposal(proposal *consensus.Proposal) error
	BroadcastValidation(validation *consensus.Validation) error
	BroadcastStatusChange(sc *message.StatusChange) error
	// RelayProposal / RelayValidation forward a peer-originated message
	// to other peers, subject to the per-peer squelch filter and
	// excluding exceptPeer (the originator). Pass 0 for exceptPeer to
	// broadcast to all peers unfiltered — used only by synthetic test
	// paths. Proposal.SuppressionHash / Validation.SuppressionHash
	// (populated by the consensus router) is used by the overlay to
	// record each recipient under that key so a later duplicate from a
	// different peer can look up the whole known-haver set and feed
	// the reduce-relay slot with all of them (B3,
	// PeerImp.cpp:3010-3017 / 3044-3054).
	RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error
	RelayValidation(validation *consensus.Validation, exceptPeer uint64) error
	// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with
	// originPeer AND every peer in seenPeers (peers known to already
	// have the message per the overlay's reverse index). Drives the
	// mtSQUELCH selection logic. Mirrors rippled's
	// PeerImp::updateSlotAndSquelch with the full haveMessage set at
	// PeerImp.cpp:3013-3017. Implementations dedupe originPeer from
	// seenPeers to avoid double-counting.
	UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64)
	RequestTxSet(id consensus.TxSetID) error
	// RequestTxSetMissingNodes requests specific SHAMap nodes for an
	// in-progress tx-set acquisition. Mirrors rippled's
	// TransactionAcquire::trigger second branch
	// (TransactionAcquire.cpp:144-171): after the initial root
	// request returns a partial tree, follow up with a request for
	// each missing node by its SHAMap path-based NodeID. nodeIDs
	// must each be exactly 33 bytes (32 path bytes + 1 depth byte).
	// excluded carries peer IDs that should be skipped during this
	// broadcast — populated by the router with peers that have
	// repeatedly returned non-progressing TMLedgerData replies for
	// this acquisition. A nil or empty map is the unrestricted case.
	// Issue #420.
	RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool) error
	RequestLedger(id consensus.LedgerID) error
	RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	// RequestProofPath sends a TMProofPathRequest for the merkle proof
	// of (key, mapType) in the SHAMap of ledgerHash. Mirrors rippled's
	// SkipListAcquire::trigger
	// (rippled/src/xrpld/app/ledger/detail/SkipListAcquire.cpp:84-92).
	RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mapType message.LedgerMapType) error
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error
	RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error
	SendToPeer(peerID uint64, frame []byte) error
	// PeerSupportsReplay reports whether the peer identified by peerID
	// advertised the ledger-replay feature during handshake. Used by
	// the catchup policy to skip replay-delta requests against peers
	// that would silently drop them. Returns false conservatively when
	// the peer is unknown or the handshake has not completed.
	PeerSupportsReplay(peerID uint64) bool
	// ReplayCapablePeersExcluding returns up to `max` peer IDs that
	// advertised the ledger-replay feature in handshake, omitting
	// peer IDs in `excluded`. Used by the replay-delta retry loop to
	// rotate peers on a sub-task timeout — mirrors rippled's
	// LedgerReplayer peer-swap mechanism (LedgerDeltaAcquire::onTimer
	// picks a new PeerSet entry on each sub-task tick). Returns an
	// empty slice when no eligible peers exist.
	ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64
	// IncPeerBadData attributes a malformed/invalid-data event to the
	// peer so the overlay can charge it toward the eviction threshold.
	// Called by the consensus router when verification of a peer-sent
	// response (replay delta, ledger data, etc.) fails. Safe no-op for
	// unknown peers. `reason` is a short stable label for logs.
	IncPeerBadData(peerID uint64, reason string)
	// PeersThatHave returns the set of peer IDs the overlay knows have
	// the message whose router-level suppression hash is
	// suppressionHash (populated during outbound relay). Returns nil if
	// unknown or the bucket has aged out. B3: the router uses this to
	// feed the reduce-relay slot with all known-havers on a duplicate
	// arrival, matching rippled's haveMessage set semantics
	// (PeerImp.cpp:3010-3017).
	PeersThatHave(suppressionHash [32]byte) []uint64
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
func (n *noopSender) RequestTxSetMissingNodes(consensus.TxSetID, [][]byte, map[uint64]bool) error {
	return nil
}
func (n *noopSender) RequestLedger(consensus.LedgerID) error                   { return nil }
func (n *noopSender) RequestLedgerByHashAndSeq([32]byte, uint32) error         { return nil }
func (n *noopSender) RequestLedgerBaseFromPeer(uint64, [32]byte, uint32) error { return nil }
func (n *noopSender) RequestReplayDelta(uint64, [32]byte) error                { return nil }
func (n *noopSender) RequestProofPath(uint64, [32]byte, [32]byte, message.LedgerMapType) error {
	return nil
}
func (n *noopSender) RequestStateNodes(uint64, [32]byte, [][]byte) error       { return nil }
func (n *noopSender) RequestTransactionNodes(uint64, [32]byte, [][]byte) error { return nil }
func (n *noopSender) SendToPeer(uint64, []byte) error                          { return nil }
func (n *noopSender) PeerSupportsReplay(uint64) bool                           { return false }
func (n *noopSender) ReplayCapablePeersExcluding([]uint64, int) []uint64       { return nil }
func (n *noopSender) IncPeerBadData(uint64, string)                            {}
func (n *noopSender) PeersThatHave([32]byte) []uint64                          { return nil }

// Compile-time interface check.
var _ consensus.Adaptor = (*Adaptor)(nil)

// Adaptor implements consensus.Adaptor, bridging the consensus engine
// to the ledger service, transaction queue, and P2P network.
type Adaptor struct {
	// mu protects trustedValidators / trustedSet / trustedMasterKeys /
	// quorum / operatingMode. Plain Mutex rather than RWMutex: the
	// fields are mutated rarely (UNL changes, SetOperatingMode) and
	// read from a handful of getters per round, so contention is low
	// and the RWMutex overhead is not justified. The two hot RPC
	// paths (operatingMode, peerLCLs) already escape this lock — one
	// via the modeAtomic mirror, the other via peerLCLsMu.
	mu sync.Mutex

	ledgerService *service.Service
	sender        NetworkSender
	identity      *ValidatorIdentity

	// UNL: trusted validator public keys
	trustedValidators []consensus.NodeID
	trustedSet        map[consensus.NodeID]struct{}
	// trustedMasterKeys are the 33-byte master pubkeys for each entry
	// in trustedValidators, index-aligned. Populated when the operator
	// configures validators via base58 keys (the master pubkey is
	// available pre-hash); empty when the UNL was supplied as raw
	// NodeIDs (e.g. some tests). Required for NegativeUNL voting —
	// mirrors rippled's `app_.validators().getTrustedMasterKeys()` at
	// RCLConsensus.cpp:377.
	trustedMasterKeys [][33]byte
	quorum            int

	// Operating mode
	operatingMode consensus.OperatingMode

	// stateAcct tracks transition counts and cumulative durations per
	// operating mode for server_info.state_accounting.
	stateAcct *stateAccounting

	// Close time offset — adjusted each round toward network average.
	// Matches rippled's timeKeeper().closeTime() offset. Stored as
	// nanoseconds in an atomic so the consensus hot path (Now) avoids
	// lock contention.
	closeOffsetNs atomic.Int64

	// negUNLVoter produces the UNLModify pseudo-tx every voting ledger
	// (one ToDisable + one ToReEnable at most). Holds the local
	// NodeID and the new-validator skip table. Constructed in New()
	// from identity.NodeID; nil for non-validating adaptors.
	negUNLVoter *negativeunlvote.Voter

	// validationHistorian provides per-ledger trusted validation
	// lookups for buildScoreTable. Wired by the engine after the
	// ValidationTracker is constructed (see rcl.Engine.Run). Nil
	// before wiring — GenerateNegativeUNLPseudoTx degrades gracefully
	// (no vote) until then.
	validationHistorian consensus.ValidationHistorian

	// Transaction set cache
	txSetCache *TxSetCache

	// Peer-reported last-closed ledger hashes, keyed by overlay peer
	// ID. Populated by the router on every inbound statusChange so
	// the engine can include peer LCLs in getNetworkLedger even when
	// no fresh proposal has arrived from that peer yet.
	peerLCLsMu sync.RWMutex
	peerLCLs   map[uint64]consensus.LedgerID

	// cookie is a random 64-bit value generated at adaptor creation
	// (one-shot per boot), emitted via sfCookie on every validation.
	// Matches rippled's RCLConsensus.cpp:813-818 which reads from
	// std::random_device once per instance.
	cookie uint64

	// feeVote is this validator's fee-vote stance, copied from Config
	// at construction. Zero values mean "no vote".
	feeVote FeeVoteStance

	// amendmentStances is this validator's per-amendment voting
	// stance, seeded from the registry's per-feature Vote behavior
	// at construction and overridden by Config.AmendmentVote. Mirrors
	// rippled's amendmentMap_ vote field
	// (AmendmentTable.cpp:556-580). Amendments not in the map default
	// to VoteAbstain on lookup; obsolete amendments cannot be
	// overridden to VoteUp.
	amendmentStances map[[32]byte]amendmentvote.Stance

	// amendmentTable, when set, is the live amendment table this validator
	// sources vote stances from each round (so operator veto/upvote changes —
	// config or runtime feature RPC — take effect without restart) and into
	// which it stashes the per-round vote tallies (lastVote). nil falls back to
	// the construction-time amendmentStances map.
	amendmentTable *amendment.AmendmentTable

	// trustedVotes caches per-validator amendment votes for 24h to
	// dampen amendment "flapping" when a flaky validator drops
	// briefly. See trusted_votes.go and rippled's TrustedVotes at
	// AmendmentTable.cpp:75-286.
	trustedVotes *TrustedVotes

	// onTxSetRequested fires before every RequestTxSet broadcast so
	// the router can re-arm its in-flight tx-set acquisition state
	// (clear attempts and lastRequest), mirroring rippled's
	// TransactionAcquire::stillNeed reset path invoked from
	// InboundTransactionsImp::getSet at InboundTransactions.cpp:107-114.
	// Nil before SetOnTxSetRequested is called; nil callers are a no-op.
	// Issue #420.
	onTxSetRequested func(consensus.TxSetID)

	// onTxSetBuilt fires when BuildTxSet caches a new tx set, so the
	// overlay can broadcast mtHAVE_SET{tsHAVE} for it. Mirrors rippled's
	// post-consensus "we have this set" announce. Nil-safe — the
	// callback is invoked only when wired.
	onTxSetBuilt func(consensus.TxSetID)

	logger *slog.Logger
}

// goxrplServerVersionTag identifies this implementation in the
// sfServerVersion field. Rippled uses the top bit (0x8000...) as its
// own identifier; go-xrpl must NOT set that — setting it would
// misrepresent this node as a rippled instance in any peer counting
// version statistics on the network. We pick a distinct non-rippled
// high-byte pattern so operators running both implementations can
// tell them apart at a glance.
const goxrplServerVersionTag uint64 = 0x4000_0000_0000_0000

// FeeVoteStance is this validator's desired fee structure — what it
// wants the network to converge on at the next flag ledger. Emitted
// on every validation as either the legacy UINT triple
// (BaseFee/ReserveBase/ReserveIncrement) or the post-XRPFees AMOUNT
// triple (BaseFeeDrops/ReserveBaseDrops/ReserveIncrementDrops).
// The adaptor picks which set to emit based on the parent ledger's
// rules — matches rippled's FeeVoteImpl.cpp:120-192 hard if/else
// gate on featureXRPFees.
//
// Zero values on any individual field mean "operator did not set
// this field" — adaptor.New() substitutes the rippled FeeSetup
// default (Config.h:65-78) so an unconfigured validator votes
// toward those defaults rather than abstaining.
type FeeVoteStance struct {
	BaseFee          uint64
	ReserveBase      uint32
	ReserveIncrement uint32
}

// defaultFeeVote returns the rippled FeeSetup defaults — a validator
// with no [voting] stanza in its config votes toward these values.
// Mirrors the default-constructed FeeSetup at
// rippled/src/xrpld/core/Config.h:65-78
// (reference_fee=10, account_reserve=10*DROPS_PER_XRP=10_000_000,
// owner_reserve=2*DROPS_PER_XRP=2_000_000).
func defaultFeeVote() FeeVoteStance {
	return FeeVoteStance{
		BaseFee:          10,
		ReserveBase:      10_000_000,
		ReserveIncrement: 2_000_000,
	}
}

// Config holds configuration for the Adaptor.
type Config struct {
	LedgerService *service.Service
	Sender        NetworkSender
	Identity      *ValidatorIdentity
	Validators    []consensus.NodeID // UNL
	// ValidatorMasterKeys are the 33-byte compressed master public
	// keys for each entry in Validators, index-aligned. Optional —
	// when supplied, the adaptor can participate in NegativeUNL
	// voting (which requires emitting raw master pubkeys in the
	// UNLModify tx). NewFromConfig populates this from the operator's
	// base58-encoded [validators] stanza; bare-NodeID test fixtures
	// can leave it nil and the adaptor will gracefully skip
	// NegativeUNL votes.
	ValidatorMasterKeys [][33]byte
	// FeeVote is the validator's fee-vote stance. Zero values mean no
	// vote. Production callers wire this from the [voting] stanza of
	// the toml config (same semantics as rippled's FeeVoteSetup).
	FeeVote FeeVoteStance
	// AmendmentVote lists amendments (by name, as defined in the
	// amendment registry) this validator wishes to vote FOR on the
	// next flag ledger. Unknown names are dropped at construction
	// time with a warning; already-enabled amendments are filtered
	// on every emission (not at construction) since the enabled set
	// changes over time. Same semantics as rippled's [amendments]
	// stanza.
	AmendmentVote []string
	// AmendmentTable, when supplied, is the live amendment table that owns the
	// operator's veto/upvote preferences and the enabled/blocked state. When set
	// it is authoritative for amendment stances: vetoed amendments abstain and
	// upvoted amendments vote up, layered over the registry defaults. The same
	// instance is shared with the ledger service, which folds validated flag
	// ledgers into it via DoValidatedLedger.
	AmendmentTable *amendment.AmendmentTable
}

// New creates a new Adaptor.
func New(cfg Config) *Adaptor {
	sender := cfg.Sender
	if sender == nil {
		sender = &noopSender{}
	}

	trustedSet := make(map[consensus.NodeID]struct{}, len(cfg.Validators))
	for _, v := range cfg.Validators {
		trustedSet[v] = struct{}{}
	}

	// Quorum: ceil(n * 0.8)
	n := len(cfg.Validators)
	quorum := (n*4 + 4) / 5 // equivalent to ceil(n * 0.8)
	if quorum < 1 && n > 0 {
		quorum = 1
	}

	// Cookie: generate a random 64-bit value at boot. Matches
	// rippled's RCLConsensus.cpp:813-818 which reads one value from
	// std::random_device for the lifetime of the instance. On the
	// astronomically-improbable read error we fall back to a
	// time-derived value — any non-zero cookie satisfies the wire
	// format; the value carries no security-critical meaning.
	var cookieBytes [8]byte
	if _, err := rand.Read(cookieBytes[:]); err != nil {
		binary.BigEndian.PutUint64(cookieBytes[:], uint64(time.Now().UnixNano()))
	}
	cookie := binary.BigEndian.Uint64(cookieBytes[:])
	if cookie == 0 {
		// Serializer treats zero as "omit" — bump to 1 in the
		// infinitesimal case of an all-zero read so the field is
		// always emitted (matches rippled's always-populated contract).
		cookie = 1
	}

	// Seed per-amendment stances from the registry so an
	// unconfigured validator votes the way rippled would: every
	// supported feature defaults to its registered VoteBehavior
	// (DefaultYes → VoteUp, DefaultNo → VoteAbstain via map lookup,
	// Obsolete → VoteObsolete). Mirrors the constructor walk at
	// AmendmentTable.cpp:556-580.
	logger := slog.Default().With("component", "consensus-adaptor")
	amendmentStances := make(map[[32]byte]amendmentvote.Stance)
	for _, f := range amendment.AllFeatures() {
		switch {
		case f.Vote == amendment.VoteObsolete:
			amendmentStances[f.ID] = amendmentvote.VoteObsolete
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			amendmentStances[f.ID] = amendmentvote.VoteUp
		}
	}

	// Layer Config.AmendmentVote on top as operator overrides to
	// VoteUp — same role as rippled's [amendments] stanza. Unknown
	// names are logged and dropped (stale config must not block
	// boot). Obsolete amendments cannot be promoted to VoteUp:
	// rippled's persistVote refuses obsolete entries
	// (AmendmentTable.cpp:728-733).
	for _, name := range cfg.AmendmentVote {
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
			// rippled's doValidation only votes for supported amendments
			// (AmendmentTable.cpp:822); an unsupported upvote is never broadcast.
			logger.Warn("unsupported amendment cannot be voted up; ignoring", "name", name)
			continue
		}
		amendmentStances[f.ID] = amendmentvote.VoteUp
	}

	// Seed the amendment-vote cache with the initial UNL so
	// RecordVotes accepts validations from round one. Re-call
	// TrustChanged whenever the trusted set mutates at runtime.
	trustedVotes := NewTrustedVotes()
	trustedVotes.TrustChanged(cfg.Validators)

	// Substitute rippled FeeSetup defaults on a per-field basis: an
	// operator may set BaseFee but leave reserves zero, and we want
	// each unset field to fall back to the rippled default
	// (Config.h:65-78) rather than to "abstain". An empty config thus
	// votes toward 10/10_000_000/2_000_000 — matching rippled's
	// default-constructed FeeSetup.
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

	// NegativeUNL voter: owned per-adaptor, matches rippled's
	// nUnlVote_ member on RCLConsensus. Constructed only when we have
	// a local validator identity AND master keys for the UNL — the
	// algorithm needs both to vote (myID for the local-participation
	// check, master keys for the emitted UNLModify tx). For
	// non-validator nodes or bare-NodeID test fixtures, leave it nil
	// and GenerateNegativeUNLPseudoTx returns no votes.
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
		cookie:            cookie,
		feeVote:           feeVote,
		amendmentStances:  amendmentStances,
		amendmentTable:    cfg.AmendmentTable,
		trustedVotes:      trustedVotes,
		logger:            logger,
	}
}

// SetValidationHistorian wires per-ledger trusted-validation lookups
// into the adaptor. The engine calls this once after constructing its
// ValidationTracker (see rcl.Engine.Run). Required for NegativeUNL
// voting; until set, GenerateNegativeUNLPseudoTx emits no votes
// (graceful degradation). Satisfies consensus.WireableAdaptor.
func (a *Adaptor) SetValidationHistorian(h consensus.ValidationHistorian) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.validationHistorian = h
}

// UpdatePeerLCL records the last-closed-ledger hash a peer reported
// via statusChange. Called by the router on every inbound
// TMStatusChange so getNetworkLedger can fall back to peer-reported
// LCLs when proposal votes are absent or stale. The zero hash is
// treated as "unknown" and removes any existing entry.
func (a *Adaptor) UpdatePeerLCL(peerID uint64, ledger consensus.LedgerID) {
	a.peerLCLsMu.Lock()
	defer a.peerLCLsMu.Unlock()
	if ledger == (consensus.LedgerID{}) {
		delete(a.peerLCLs, peerID)
		return
	}
	a.peerLCLs[peerID] = ledger
}

// PeerReportedLedgers returns a snapshot of all known peer LCL
// hashes. Engine-side consensus.Adaptor implementation; see the
// interface docstring for semantics.
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

// --- Network operations ---

func (a *Adaptor) BroadcastProposal(proposal *consensus.Proposal) error {
	return a.sender.BroadcastProposal(proposal)
}

func (a *Adaptor) BroadcastValidation(validation *consensus.Validation) error {
	return a.sender.BroadcastValidation(validation)
}

// PeersThatHave returns the set of peer IDs the overlay knows have
// the message whose suppression-hash is `suppressionHash`. Thin
// delegate to NetworkSender.PeersThatHave so higher layers (the
// consensus router) can query without pulling in the overlay import.
func (a *Adaptor) PeersThatHave(suppressionHash [32]byte) []uint64 {
	return a.sender.PeersThatHave(suppressionHash)
}

// RelayProposal forwards a peer-originated proposal to other peers,
// excluding exceptPeer (the originator). Pass 0 for exceptPeer to
// forward to everyone. Uses proposal.SuppressionHash (populated by
// the consensus router) so the overlay can record each recipient in
// its reverse index — queried by the router on later duplicates to
// feed the full known-haver set into the reduce-relay slot (B3).
func (a *Adaptor) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	return a.sender.RelayProposal(proposal, exceptPeer)
}

// RelayValidation forwards a peer-originated validation to other peers,
// excluding exceptPeer (the originator). Mirrors RelayProposal; uses
// validation.SuppressionHash for the reverse-index record.
func (a *Adaptor) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	return a.sender.RelayValidation(validation, exceptPeer)
}

// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with
// originPeer AND every peer in seenPeers (known-havers). Called by
// the consensus router on every trusted proposal/validation duplicate
// to keep the squelch selection logic moving. Mirrors rippled's
// PeerImp::updateSlotAndSquelch with the full haveMessage set at
// PeerImp.cpp:3013-3017 — feeding multiple known-havers per
// duplicate is what lets selection converge at the same rate rippled
// does (B3).
func (a *Adaptor) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	a.sender.UpdateRelaySlot(validatorKey, originPeer, seenPeers)
}

// SetOnTxSetRequested registers a callback invoked at the start of
// every RequestTxSet. Used by the router to mirror rippled's
// stillNeed re-arm: every active re-ask from consensus resets the
// throttle/attempt bookkeeping on the in-flight acquisition so the
// next inbound TMLedgerData broadcasts immediately. Set once at
// startup; not safe for concurrent re-registration.
func (a *Adaptor) SetOnTxSetRequested(cb func(consensus.TxSetID)) {
	a.onTxSetRequested = cb
}

func (a *Adaptor) RequestTxSet(id consensus.TxSetID) error {
	if a.onTxSetRequested != nil {
		a.onTxSetRequested(id)
	}
	return a.sender.RequestTxSet(id)
}

func (a *Adaptor) RequestTxSetMissingNodes(id consensus.TxSetID, nodeIDs [][]byte, excluded map[uint64]bool) error {
	return a.sender.RequestTxSetMissingNodes(id, nodeIDs, excluded)
}

func (a *Adaptor) RequestLedger(id consensus.LedgerID) error {
	return a.sender.RequestLedger(id)
}

func (a *Adaptor) RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error {
	return a.sender.RequestLedgerByHashAndSeq(hash, seq)
}

func (a *Adaptor) RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error {
	return a.sender.RequestLedgerBaseFromPeer(peerID, hash, seq)
}

// RequestReplayDelta delegates to the network sender. Mirrors the
// outbound side of rippled's LedgerDeltaAcquire which sends a single
// TMReplayDeltaRequest and awaits one TMReplayDeltaResponse.
func (a *Adaptor) RequestReplayDelta(peerID uint64, hash [32]byte) error {
	return a.sender.RequestReplayDelta(peerID, hash)
}

func (a *Adaptor) RequestProofPath(peerID uint64, ledgerHash, key [32]byte, mapType message.LedgerMapType) error {
	return a.sender.RequestProofPath(peerID, ledgerHash, key, mapType)
}

func (a *Adaptor) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	return a.sender.RequestStateNodes(peerID, ledgerHash, nodeIDs)
}

func (a *Adaptor) RequestTransactionNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	return a.sender.RequestTransactionNodes(peerID, ledgerHash, nodeIDs)
}

// EngineConfigForReplay returns the shared (non-per-ledger)
// tx.EngineConfig used when replaying a historical ledger anchored on
// `parent`. Fees come from the parent's FeeSettings SLE; network and
// logger come from the service config.
//
// The caller (typically ReplayDelta.Apply) overrides the per-ledger
// fields — LedgerSequence, ParentCloseTime, ParentHash, Rules,
// ApplyFlags, OpenLedger — from the verified target header.
func (a *Adaptor) EngineConfigForReplay(parent *ledger.Ledger) tx.EngineConfig {
	if a.ledgerService == nil {
		return tx.EngineConfig{}
	}
	return a.ledgerService.EngineConfigForReplay(parent)
}

// PeerSupportsReplay reports whether the peer advertised the ledger-replay
// protocol feature during handshake. Delegates to the NetworkSender so the
// same decision applies to both real overlay peers and test doubles.
func (a *Adaptor) PeerSupportsReplay(peerID uint64) bool {
	return a.sender.PeerSupportsReplay(peerID)
}

// ReplayCapablePeersExcluding returns up to `max` peer IDs that
// advertised ledger-replay, omitting peer IDs in `excluded`. Used by
// the replay-delta retry loop to rotate peers on sub-task timeout —
// matches rippled's LedgerDeltaAcquire::onTimer peer-swap.
func (a *Adaptor) ReplayCapablePeersExcluding(excluded []uint64, max int) []uint64 {
	return a.sender.ReplayCapablePeersExcluding(excluded, max)
}

// IncPeerBadData attributes an invalid-data event to the peer via the
// underlying network sender so the overlay can charge it toward the
// eviction threshold. See NetworkSender.IncPeerBadData. Kept as a
// thin delegator so Router can call through the adaptor rather than
// reaching into the overlay directly.
func (a *Adaptor) IncPeerBadData(peerID uint64, reason string) {
	a.sender.IncPeerBadData(peerID, reason)
}

// GetParentLedgerForReplay returns the closed ledger at seq-1, which is
// the prior ledger needed to replay a delta into seq. Returns nil if the
// parent is unknown, the request is for a ledger we cannot anchor on
// (seq <= 1, no service wired), OR the parent is still open (its hash
// is unset until Close, so it cannot serve as a chain anchor — a
// goxrpl-1 enclave run reproduced a live-lock where the open ledger
// was returned and the replay-delta verifier kept rejecting valid
// responses against an all-zero parent hash). Mirrors the rippled
// LedgerDeltaAcquire::trigger requirement that the parent ledger is
// already locally available before issuing the delta request.
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

// LedgerService returns the underlying ledger service for direct queries.
func (a *Adaptor) LedgerService() *service.Service {
	return a.ledgerService
}

// --- Ledger operations ---

func (a *Adaptor) GetLedger(id consensus.LedgerID) (consensus.Ledger, error) {
	// Try to find the ledger by hash in the service
	l, err := a.ledgerService.GetLedgerByHash([32]byte(id))
	if err != nil {
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

// GetValidatedLedgerHash returns the hash of the most recent ledger
// the node considers fully validated. Mirrors rippled's
// LedgerMaster::getValidatedLedger consulted at RCLConsensus.cpp:858
// to populate sfValidatedHash. Returns the zero LedgerID when no
// ledger has yet crossed trusted-validation quorum (the engine-side
// consumer should not emit the field in that case).
func (a *Adaptor) GetValidatedLedgerHash() consensus.LedgerID {
	if a.ledgerService == nil {
		return consensus.LedgerID{}
	}
	vl := a.ledgerService.GetValidatedLedger()
	if vl == nil {
		return consensus.LedgerID{}
	}
	return consensus.LedgerID(vl.Hash())
}

func (a *Adaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, closeTimeCorrect bool) (consensus.Ledger, error) {
	// Unwrap the parent to get the concrete ledger for the service.
	// This is critical for chain switching: the parent may differ from
	// the service's internal closedLedger after wrong ledger detection.
	var parentLedger *ledger.Ledger
	if w, ok := parent.(*LedgerWrapper); ok {
		parentLedger = w.Unwrap()
	}
	// context.TODO: the consensus.Adaptor.BuildLedger interface does not
	// propagate a context. Until that interface gains one, persistence
	// here cannot be cancelled by the consensus engine. Tracked separately
	// from this issue (#185).
	seq, err := a.ledgerService.AcceptConsensusResult(context.TODO(), parentLedger, txSet.Txs(), closeTime, closeTimeCorrect)
	if err != nil {
		return nil, err
	}

	// Retrieve the newly created ledger
	l, err := a.ledgerService.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}
	return WrapLedger(l), nil
}

func (a *Adaptor) ValidateLedger(ledger consensus.Ledger) error {
	// Basic validation: ensure the ledger exists and hash is consistent
	wrapper, ok := ledger.(*LedgerWrapper)
	if !ok {
		return errors.New("unexpected ledger type")
	}
	l := wrapper.Unwrap()
	if l == nil {
		return errors.New("nil ledger")
	}
	// Verify state hash consistency
	if _, err := l.StateMapHash(); err != nil {
		return err
	}
	return nil
}

func (a *Adaptor) StoreLedger(ledger consensus.Ledger) error {
	// Ledger is already persisted by AcceptConsensusResult in BuildLedger.
	// This is a no-op for now; could be used for additional replication.
	return nil
}

// --- Transaction operations ---

// GetPendingTxs returns the raw tx blobs in the persistent open view.
// Used by the engine for the open-phase "anyTransactions" gate. Pointer-
// deref of openLedger().current()->txs — no per-call filter.
func (a *Adaptor) GetPendingTxs() [][]byte {
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.OpenLedgerTxs()
}

// GetProposableTxs returns the tx set the node will propose this round.
// Mirrors rippled RCLConsensus.cpp:333-349. parent is the prevLedger
// rippled threads through for negative-UNL / amendment-vote filtering;
// go-xrpl does not filter today so the two methods return the same
// snapshot, but the parameter is part of the rippled-faithful contract
// and the implementations will diverge once filtering lands.
func (a *Adaptor) GetProposableTxs(parent consensus.Ledger) [][]byte {
	_ = parent
	if a.ledgerService == nil {
		return nil
	}
	return a.ledgerService.OpenLedgerTxs()
}

// GenerateFlagLedgerPseudoTxs runs the fee-vote and amendment-vote producers
// and returns their concatenated pseudo-tx blobs for the proposal initial
// set. Mirrors RCLConsensus.cpp:354-367, including the negative-UNL filter
// (:358) and quorum gate (:361). XRPFees and fixAmendmentMajorityCalc
// behavior is read from the parsed Amendments SLE since Ledger.Rules is nil
// at this boundary.
func (a *Adaptor) GenerateFlagLedgerPseudoTxs(prevLedger consensus.Ledger, parentValidations []*consensus.Validation) [][]byte {
	if a.ledgerService == nil {
		return nil
	}
	prev, err := a.ledgerService.GetLedgerByHash([32]byte(prevLedger.ID()))
	if err != nil || prev == nil {
		return nil
	}
	upcomingSeq := prev.Sequence() + 1

	// negativeUNLFilter wrapping getTrustedForLedger — RCLConsensus.cpp:358.
	filtered := a.filterNegativeUNL(parentValidations)

	// Quorum gate — RCLConsensus.cpp:361. Standalone reports quorum 0.
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
// validators currently on the negative UNL. Mirrors rippled's
// ValidatorList::negativeUNLFilter at RCLConsensus.cpp:358.
func (a *Adaptor) filterNegativeUNL(vals []*consensus.Validation) []*consensus.Validation {
	return excludeNegativeUNL(vals, a.GetNegativeUNL())
}

// excludeNegativeUNL is the pure-arithmetic core of the negUNL
// filter — extracted for testability without standing up a
// NegativeUNL SLE in the ledger fixture. Empty negUNL returns vals
// unchanged (no allocation).
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
	if a.onTxSetBuilt != nil {
		a.onTxSetBuilt(ts.ID())
	}
	return ts, nil
}

// SetOnTxSetBuilt installs a callback invoked once per BuildTxSet,
// after the set has been cached. The CLI wires this to
// Overlay.BroadcastHaveTxSet so peers acquiring the set via
// mtHAVE_SET{tsNEED} can find a source without polling. Mirrors
// rippled's post-consensus mtHAVE_SET emission. Safe to call with
// nil to clear.
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
// Mirrors rippled NetworkOPsImp::apply → openLedger().modify
// (NetworkOPs.cpp:1507).
//
// local=true marks RPC-originated submissions, which get held in the
// LocalTxs pool until they apply or age out. local=false is for
// peer-relay submissions — the peer manages its own resends.
func (a *Adaptor) AddPendingTx(blob []byte, local bool) {
	_, _ = a.SubmitPendingTx(blob, local)
}

// SubmitPendingTx is AddPendingTx with the classification result surfaced
// to the caller. The peer-relay path in Router.handleTransaction uses this
// to gate immediate re-broadcast on a non-Failure result — rippled relays
// inside processTransaction at NetworkOPs.cpp:1685-1712 only when the tx
// applied, queued, or stayed plausible in a non-FULL mode, never on the
// permanent-drop classes. AddPendingTx remains the void variant for
// callers that don't care (RPC ingress, tests).
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

// --- Validator operations ---

func (a *Adaptor) IsValidator() bool {
	return a.identity != nil
}

func (a *Adaptor) GetValidatorKey() (consensus.NodeID, error) {
	if a.identity == nil {
		return consensus.NodeID{}, ErrNoValidatorKey
	}
	return a.identity.NodeID, nil
}

// GetValidatorSigningKey returns the local validator's 33-byte compressed
// signing public key (the ephemeral key in token mode, equal to the master
// key in seed-only mode). This is the value rippled exposes as
// getValidationPublicKey() and feeds to validator_info / server_info; the
// 20-byte NodeID from GetValidatorKey must NOT be substituted here.
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

// --- Trust operations ---

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

// SetTrustedValidators replaces the operator-trusted validator set
// atomically. Visible to subsequent GetTrustedValidators / IsTrusted /
// GetQuorum / GetNegativeUNL reads, and to the consensus engine's
// per-round delta scan in driveNegativeUNLNewValidatorsLocked — the
// added entries flow into OnUNLChange so the NegativeUNL voter's
// grace period (NewValidatorDisableSkip ledgers) covers them.
//
// validators and masterKeys are index-aligned and MUST be the same
// length (NodeIDs are derived from master keys via calcNodeID; every
// trusted validator therefore has exactly one master pubkey). A
// mismatch is logged at WARN and the trailing entries of whichever
// slice is longer are dropped — defensive only, since the canonical
// producer ParseValidatorKeysWithMaster always returns equal-length
// slices. Pass two empty slices (nil, nil) to clear the trusted set
// (standalone-mode transition).
//
// Mirrors the writable surface of rippled's ValidatorList:
// applyLists → updateTrusted publishes the new trusted set; the next
// preStartRound observes the delta and forwards `added` to
// nUnlVote_.newValidators (RCLConsensus.cpp:1041-1043). go-xrpl has no
// publisher-trust subsystem yet, so this is the single entry point
// every trigger (SIGHUP-driven config reload, future RPC admin
// method, future TMValidatorList ingress) plugs into.
//
// Safe for concurrent callers; copies inputs so callers may mutate
// their slices after return.
func (a *Adaptor) SetTrustedValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	if len(validators) != len(masterKeys) && (len(validators) > 0 || len(masterKeys) > 0) {
		a.logger.Warn("SetTrustedValidators: validators / masterKeys length mismatch; truncating to shorter",
			"validators_count", len(validators),
			"master_keys_count", len(masterKeys),
		)
		n := len(validators)
		if len(masterKeys) < n {
			n = len(masterKeys)
		}
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

	// trustedVotes is assigned once in New (adaptor.go:384) and never
	// reassigned thereafter, so the unlocked read is safe — TrustedVotes
	// owns its own internal mutex for the call itself. Calling after
	// releasing a.mu avoids lock nesting (TrustedVotes.TrustChanged
	// takes its mutex on the way in). Safe to call with an empty slice;
	// it just drops stale per-validator vote caches.
	if a.trustedVotes != nil {
		a.trustedVotes.TrustChanged(vCopy)
	}
}

// GetQuorum returns the current quorum requirement, recomputed on
// every call to account for negative-UNL changes. Matches rippled's
// ValidatorList.cpp:2061-2087 which recomputes quorum on every
// UNL/negUNL change as ceil(0.8 * (trusted - disabled)). Pre-R6b.3
// go-xrpl froze quorum at Adaptor construction, so partial-UNL
// outages slowed finality relative to rippled.
func (a *Adaptor) GetQuorum() int {
	// Take a.mu around the trustedValidators read — the slice header is
	// only mutated in New() today but the TrustChanged event will be
	// driven from the validator-manager wiring noted at line 376, after
	// which an unlocked read becomes a data race against the writer.
	// GetNegativeUNL has its own lock, so call it after release to
	// avoid nesting locks.
	a.mu.Lock()
	trusted := len(a.trustedValidators)
	a.mu.Unlock()
	disabled := len(a.GetNegativeUNL())
	return computeQuorum(trusted, disabled)
}

// computeQuorum is the pure arithmetic behind GetQuorum — extracted
// for testability. Returns the minimum number of trusted, non-negUNL
// validator signatures required to fully validate a ledger:
//
//   - standalone (trusted==0): 0 — no quorum gate.
//   - effective > 0: ceil(0.8 * effective). Minimum 1 to stay live.
//   - effective <= 0 with a non-empty trusted set (every validator
//     on negUNL): math.MaxInt. We return an unreachable quorum so
//     no validation count can ever fire checkFullValidation against
//     a fully-disabled UNL. The alternative (quorum==1) would let
//     any transient vote fire a spurious full-validation callback.
func computeQuorum(trusted, disabled int) int {
	if trusted == 0 {
		return 0
	}
	effective := trusted - disabled
	if effective <= 0 {
		return math.MaxInt
	}
	q := (effective*4 + 4) / 5
	if q < 1 {
		q = 1
	}
	return q
}

// GetNegativeUNLMasters reads the ltNEGATIVE_UNL SLE and returns the
// raw 33-byte master pubkeys of disabled validators (not the derived
// NodeIDs that GetNegativeUNL returns). Used by the `validators` RPC,
// which surfaces these as base58 NodePublic strings under the
// `NegativeUNL` key — matching rippled's getJson at
// ValidatorList.cpp:1737-1744.
//
// Returns nil under the same conditions GetNegativeUNL does: no ledger
// service, no validated ledger, no SLE, or parse failure.
func (a *Adaptor) GetNegativeUNLMasters() [][33]byte {
	if a.ledgerService == nil {
		return nil
	}
	l := a.ledgerService.GetValidatedLedger()
	if l == nil {
		return nil
	}
	data, err := l.Read(keylet.NegativeUNL())
	if err != nil || len(data) == 0 {
		return nil
	}
	sle, err := pseudo.ParseNegativeUNLSLE(data)
	if err != nil {
		return nil
	}
	if len(sle.DisabledValidators) == 0 {
		return nil
	}
	out := make([][33]byte, 0, len(sle.DisabledValidators))
	for _, pubKey := range sle.DisabledValidators {
		if len(pubKey) != 33 {
			continue
		}
		var master [33]byte
		copy(master[:], pubKey)
		out = append(out, master)
	}
	return out
}

// GetNegativeUNL reads the ltNEGATIVE_UNL SLE from the current validated
// ledger and returns the NodeIDs of disabled validators. Mirrors
// rippled's per-ledger NegativeUNL scan; without this the
// ValidationTracker's negUNL filter (validations.go:SetNegativeUNL)
// is dead code and a negative-UNL'd validator's vote would still
// count toward quorum on mainnet.
//
// Returns nil when:
//   - no ledger service is wired (test env);
//   - no validated ledger yet (pre-sync);
//   - no NegativeUNL SLE has been created (cluster is healthy);
//   - the SLE exists but parse fails (logged at warn, treated as empty
//     so a malformed SLE doesn't lock the tracker).
func (a *Adaptor) GetNegativeUNL() []consensus.NodeID {
	if a.ledgerService == nil {
		return nil
	}
	l := a.ledgerService.GetValidatedLedger()
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
	out := make([]consensus.NodeID, 0, len(sle.DisabledValidators))
	for _, pubKey := range sle.DisabledValidators {
		if len(pubKey) != 33 {
			// Silently skip malformed entries rather than failing the
			// whole lookup. A 32- or 34-byte entry is never going to
			// match an IsTrusted check anyway; skipping preserves the
			// rest of the list.
			continue
		}
		// NegativeUNL entries store 33-byte master public keys; the
		// in-memory NodeID they need to match against is the 20-byte
		// calcNodeID(master) digest. Mirrors rippled's
		// LedgerMaster.cpp:886,952 which compares against the
		// calcNodeID-derived identifier in the trust map.
		var master [33]byte
		copy(master[:], pubKey)
		out = append(out, consensus.CalcNodeID(master))
	}
	return out
}

// GetCookie returns this adaptor's boot-lifetime cookie for emission
// via sfCookie on every outgoing validation. Matches rippled's
// one-shot-per-boot semantics (RCLConsensus.cpp:813-818).
func (a *Adaptor) GetCookie() uint64 {
	return a.cookie
}

// GetServerVersion returns the 64-bit version identifier this
// validator advertises via sfServerVersion. The encoding deliberately
// differs from rippled's (top bit 0x8000...) to avoid misrepresenting
// go-xrpl as rippled in peer version-counting statistics; we use
// 0x4000... as a go-xrpl tag and OR in a package version number.
func (a *Adaptor) GetServerVersion() uint64 {
	// Low bits are available for a semantic version encoding in the
	// future; for now they stay zero so the tag byte is sufficient to
	// identify a go-xrpl validator.
	return goxrplServerVersionTag
}

// GetFeeVote returns this validator's fee-vote stance and whether the
// post-XRPFees rules should apply. postXRPFees is read from the
// parent ledger's rules so voting switches the instant the amendment
// activates — mirrors rippled's FeeVoteImpl.cpp:120-192 hard gate.
// Zero stance values mean "no vote" and the serializer will omit the
// fields.
// GetLoadFee returns the local load_fee advertised on outbound
// validations. Mirrors rippled RCLConsensus.cpp:870-875:
//
//	auto const fee = std::max(ft.getLocalFee(), ft.getClusterFee());
//	if (fee > ft.getLoadBase()) v.setFieldU32(sfLoadFee, fee);
//
// We return 0 — which the serializer treats as "omit" — whenever the
// max collapses to LoadBase, equivalent to rippled's `> getLoadBase()`
// gate (the local/cluster floors prevent values below LoadBase).
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

func (a *Adaptor) GetFeeVote() (baseFee, reserveBase, reserveIncrement uint64, postXRPFees bool) {
	return a.feeVote.BaseFee,
		uint64(a.feeVote.ReserveBase),
		uint64(a.feeVote.ReserveIncrement),
		a.IsFeatureEnabled("XRPFees")
}

// GetAmendmentVote returns the list of amendment IDs this validator
// wishes to vote FOR on the next flag ledger, filtered against the
// current ledger's already-enabled amendments so we don't re-vote for
// active ones. Matches rippled's AmendmentTable::doValidation.
//
// Returns nil when:
//   - the validator has no configured vote (AmendmentVote empty);
//   - no ledger is available to filter against (pre-sync);
//   - every configured amendment is already enabled on the current ledger.
//
// Output is a freshly-allocated slice; the result is canonically
// sorted by amendment ID so two validators with the same stance
// produce byte-identical validations.
// currentAmendmentStances returns the validator's live per-amendment vote
// stances. When a live amendment table is wired, stances are derived fresh from
// it (registry defaults, then operator veto → abstain and upvote → VoteUp) so
// config or runtime feature-RPC changes take effect without restart; otherwise
// the construction-time map is returned.
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
			// operator upvote, but only for supported amendments — rippled's
			// doValidation gates on `supported && vote==up` (AmendmentTable.cpp:822),
			// so an unsupported amendment is never voted for however it was voted.
			stances[f.ID] = amendmentvote.VoteUp
		case f.Supported == amendment.SupportedYes && f.Vote == amendment.VoteDefaultYes && !f.Retired:
			stances[f.ID] = amendmentvote.VoteUp
		}
	}
	return stances
}

func (a *Adaptor) GetAmendmentVote() [][32]byte {
	stances := a.currentAmendmentStances()
	if len(stances) == 0 {
		return nil
	}

	// Filter out amendments already enabled on the currently-validated
	// ledger. Absence of a ledger or rules defaults to "nothing
	// filtered" — safe because an un-synced node isn't validating.
	var rules *amendment.Rules
	if a.ledgerService != nil {
		if l := a.ledgerService.GetValidatedLedger(); l != nil {
			rules = l.Rules()
		}
	}

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

	// Canonical sort — rippled's sfAmendments is written in sorted
	// order, so emit the same ordering for byte-identical validations
	// between two validators with the same stance.
	sort.Slice(out, func(i, j int) bool {
		return bytes.Compare(out[i][:], out[j][:]) < 0
	})
	return out
}

// IsFeatureEnabled reports whether the named amendment is enabled on
// the rules of the currently-validated ledger. Used by the engine to
// gate optional STValidation fields rippled only emits under specific
// amendments (sfValidatedHash under featureHardenedValidations, etc).
//
// Returns true on "unknown" as a safe default:
//   - no ledger service wired (test harness): preserves mainnet-default
//     emission so behavior-pinning tests that don't bother with rules
//     still see the fields they expect;
//   - no validated ledger yet (pre-sync): we haven't learned the
//     network rules, but emission of fields gated by default-yes
//     amendments (like HardenedValidations, VoteDefaultYes) is the
//     safe assumption on mainnet;
//   - unknown feature name: treat as enabled so a typo doesn't silently
//     drop emission on mainnet. The test path exercises the false case
//     explicitly by passing rules with the feature disabled.
func (a *Adaptor) IsFeatureEnabled(name string) bool {
	if a.ledgerService == nil {
		return true
	}
	l := a.ledgerService.GetValidatedLedger()
	if l == nil {
		return true
	}
	rules := l.Rules()
	if rules == nil {
		return true
	}
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return true
	}
	return rules.Enabled(f.ID)
}

// IsFeatureEnabledOnLedger reports whether the named amendment is
// enabled in the rules of the supplied ledger. Mirrors rippled's
// `prevLedger->rules().enabled(featureX)` at RCLConsensus.cpp:370:
// rules are read from THAT specific ledger, not from the validated
// view, and a miss in the amendment table is "not enabled" (not
// "assume enabled" — the lax default of IsFeatureEnabled is for
// the validation-broadcast path, not for engine-level gates).
//
// Returns false when the ledger is nil, when it does not unwrap to
// a *ledger.Ledger this adaptor recognises, when rules are nil, or
// when the feature name is unknown. That matches rippled's
// Rules::enabled semantics for a strict gate.
func (a *Adaptor) IsFeatureEnabledOnLedger(l consensus.Ledger, name string) bool {
	if l == nil {
		return false
	}
	w, ok := l.(*LedgerWrapper)
	if !ok {
		return false
	}
	rules := w.Unwrap().Rules()
	if rules == nil {
		return false
	}
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return false
	}
	return rules.Enabled(f.ID)
}

// IsStandalone reports whether the node is configured for standalone
// (single-node) operation. Mirrors rippled's
// `app_.config().standalone()` at RCLConsensus.cpp:352. Used by the
// engine to bypass the proposing-mode gate on flag-ledger pseudo-tx
// injection (matching rippled's `standalone() || (proposing &&
// !wrongLCL)` OR-form).
func (a *Adaptor) IsStandalone() bool {
	if a.ledgerService == nil {
		return false
	}
	return a.ledgerService.IsStandalone()
}

// --- Time operations ---

func (a *Adaptor) Now() time.Time {
	return time.Now().Add(time.Duration(a.closeOffsetNs.Load()))
}

// CloseOffset returns the current consensus-derived close-time offset.
// Mirrors rippled TimeKeeper::closeOffset(); surfaced via server_info
// as close_time_offset when |offset| >= 60s (NetworkOPs.cpp:2946-2949).
func (a *Adaptor) CloseOffset() time.Duration {
	return time.Duration(a.closeOffsetNs.Load())
}

func (a *Adaptor) CloseTimeResolution() time.Duration {
	l := a.ledgerService.GetClosedLedger()
	if l != nil {
		res := l.Header().CloseTimeResolution
		if res >= 2 && res <= 120 {
			return time.Duration(res) * time.Second
		}
	}
	return 30 * time.Second // rippled default
}

// AdjustCloseTime computes the weighted average of raw close times
// and applies the quarter-step damping rippled uses to converge on
// the network's view of time. The caller-side averaging matches
// RCLConsensus.cpp:694-732; the damping branches match
// TimeKeeper::adjustCloseTime at TimeKeeper.h:88-116. All arithmetic
// is in whole seconds — rippled's NetClock is second-granular, and a
// straight nanosecond replace would never decay toward zero on small
// |by|.
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

	// Mirrors TimeKeeper.h:103-113. Integer division truncates toward
	// zero in both C++ (since C++11) and Go, so the branches translate
	// directly.
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

// --- Status operations ---

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
		// Held under a.mu so the operatingMode field and the
		// accounting transition observe the same serialization
		// order. The tracker has its own mutex; this nested take
		// is short and never re-enters a.mu.
		a.stateAcct.transition(mode)
	}
}

// StateAccounting returns the snapshot used by server_info to populate
// state_accounting + the top-level server_state_duration_us /
// initial_sync_duration_us fields. Returns the zero value when the
// adaptor was constructed without a tracker (legacy tests). Mirrors
// rippled's NetworkOPsImp::StateAccounting::json. stateAcct is set
// once in New() and never reassigned, so no Adaptor-level lock is
// needed; the tracker has its own mutex.
func (a *Adaptor) StateAccounting() StateAccountingSnapshot {
	if a.stateAcct == nil {
		return StateAccountingSnapshot{}
	}
	return a.stateAcct.snapshot()
}

// OnConsensusReached logs the close and fires the consensus-phase hook.
// The persistent open-ledger view has already been advanced by
// Service.AcceptConsensusResult → OpenLedger.Accept (mirrors rippled
// RCLConsensus.cpp:662-674), so there is nothing to do for the tx pool.
//
// NOTE: we intentionally do NOT mark the ledger validated here. The
// validated_ledger pointer only advances once trusted-validation quorum
// is reached — see OnLedgerFullyValidated, driven by the engine's
// ValidationTracker. This matches rippled's checkAccept() semantics
// where local consensus != network agreement.
func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation, roundTime time.Duration) {
	a.logger.Info("Consensus reached",
		"ledger_seq", ledger.Seq(),
		"validations", len(validations),
		"round_time", roundTime,
	)

	if a.ledgerService != nil {
		// Mirror rippled RCLConsensus.cpp:803-805: pass the round's
		// wall-clock duration into the ledger service so the TxQ's
		// processClosedLedger sees the timeLeap flag when consensus
		// crossed the 5s slow-consensus threshold.
		a.ledgerService.SetLastConsensusRoundTime(roundTime)

		if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
			go hooks.OnConsensusPhase("accepted")
		}
	}

	a.maybePromoteAfterConsensus(ledger)
}

// maybePromoteAfterConsensus mirrors rippled's endConsensus auto-promote
// (NetworkOPs.cpp:2190-2214): a successful consensus close is itself
// evidence that we are aligned with the network, so we advance the
// operating mode without waiting for a peer-acquired ledger.
//
//	CONNECTED | SYNCING  → TRACKING
//	CONNECTED | TRACKING → FULL when the just-closed ledger is recent
//	                           (now < ledger.CloseTime() + 2 * resolution;
//	                            equivalent to rippled's
//	                            `parentCloseTime + 2 * closeTimeResolution`
//	                            evaluated on the new open child, since the
//	                            argument here IS rippled's `current->parent`)
//
// Both branches are gated on !networkLedgerDiffers(ledger) — go-xrpl's
// realization of rippled's `!ledgerChange` (NetworkOPs.cpp:2192, 2203).
// rippled's `ledgerChange` falls out of checkLastClosedLedger, which
// picks the preferred LCL via validations.getPreferredLCL — trusted
// validations weighted through the ancestry trie, with peer-reported
// LCLs as the fallback when no trusted validations exist. We mirror
// that ordering. The gate stays conservative: when neither trusted
// validations nor peer LCL data are available (typical fresh-bootstrap),
// the preferred LCL is our own and we promote as before.
//
// rippled additionally gates the TRACKING promotion on
// `!needNetworkLedger_` (NetworkOPs.cpp:2197); go-xrpl has no
// equivalent flag because OnConsensusReached only fires after a
// completed round, which subsumes "we have a network ledger".
//
// Without this, a fresh genesis bootstrap deadlocks at OpModeConnected
// because none of the existing OpModeTracking transitions fire (they
// all require a peer ahead of us to acquire from). With it, the first
// successful observer round graduates us to FULL and the next round
// proposes normally — matching rippled's bootstrap sequence exactly.
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

// networkLedgerDiffers reports whether the network-preferred last
// closed ledger differs from the one we just closed — go-xrpl's
// equivalent of rippled's `switchLedgers` (the `ledgerChange` the
// promotion gate keys off, NetworkOPs.cpp:1931). It returns false when
// the preferred LCL is our own ledger, so promotion proceeds.
func (a *Adaptor) networkLedgerDiffers(ledger consensus.Ledger, mode consensus.OperatingMode) bool {
	ourLCL := ledger.ID()
	return a.preferredLCL(ourLCL, ledger.Seq(), mode) != ourLCL
}

// preferredLCL mirrors rippled's validations.getPreferredLCL
// (Validations.h:936-960) as collated by checkLastClosedLedger
// (NetworkOPs.cpp:1902-1933). Trusted validations weighted through the
// ancestry trie take priority; peer-reported LCL counts are the
// fallback used only when no trusted validations are available.
func (a *Adaptor) preferredLCL(ourLCL consensus.LedgerID, ourSeq uint32, mode consensus.OperatingMode) consensus.LedgerID {
	// minSeq is rippled's getValidLedgerIndex() (NetworkOPs.cpp:1928):
	// the trie's preferred ledger is only adopted when it is at least
	// our last fully-validated sequence, never rewinding behind it.
	var minSeq uint32
	if a.ledgerService != nil {
		minSeq = a.ledgerService.GetValidatedLedgerIndex()
	}

	// largestIssued is rippled's localSeqEnforcer_.largest() — the
	// highest seq THIS node has issued a validation for — used to seed
	// the trie's uncommitted support (Validations.h:855, trie.GetPreferred
	// floor). A non-validator observer issues none, so its value is 0;
	// a validator's tracks the ledgers it signed, for which the just-
	// closed seq is the faithful proxy (same value rcl/engine.go:3347
	// passes from its FULL-mode round boundary).
	var largestIssued uint32
	if a.IsValidator() {
		largestIssued = ourSeq
	}

	// Trusted-validation branch (getPreferredLCL:941-946). GetPreferred
	// consults the ancestry trie; PreferredFromValidations is its
	// no-trie fallback, matching the engine's round-boundary jump
	// (engine.go GetPreferred → PreferredFromValidations).
	if h := a.validationHistorian; h != nil {
		id, seq, ok := h.GetPreferred(largestIssued)
		if !ok {
			id, seq, ok = h.PreferredFromValidations(minSeq)
		}
		if ok {
			// Reconcile the raw trie tip against our current ledger,
			// mirroring getPreferred's curr-relative logic
			// (Validations.h:881-898): a preferred ledger that is not
			// ahead of us — our own ledger or a same-chain ancestor — is
			// not a switch, so we stick with ours. Approximated as
			// seq >= ourSeq, the same guard rcl/engine.go:3347 uses on
			// this pair, since per-seq ancestry is not available here.
			// The seq >= minSeq half is getPreferredLCL:946.
			if id != ourLCL && seq >= ourSeq && seq >= minSeq {
				return id
			}
			return ourLCL
		}
	}

	// Peer-LCL fallback (getPreferredLCL:948-959). Seed our own LCL at
	// zero and increment it when we are already >= TRACKING, mirroring
	// peerCounts in checkLastClosedLedger (NetworkOPs.cpp:1911-1913),
	// then pick the dominant ledger (ties broken by larger ID).
	counts := map[consensus.LedgerID]uint32{ourLCL: 0}
	if mode >= consensus.OpModeTracking {
		counts[ourLCL]++
	}
	for _, p := range a.PeerReportedLedgers() {
		counts[p]++
	}
	best := ourLCL
	for id, c := range counts {
		// Larger count wins; ties break on larger ID, matching rippled's
		// max_element over std::tie(count, id) (Validations.h:951-954).
		if c > counts[best] || (c == counts[best] && bytes.Compare(id[:], best[:]) > 0) {
			best = id
		}
	}
	return best
}

// OnLedgerFullyValidated fires when the engine's ValidationTracker sees
// trusted-validation quorum for a ledger. We flip the service's
// validated_ledger only if our stored ledger at that seq has the matching
// hash — fork safety, matching rippled's checkAccept which operates on
// the specific ledger pointer, not seq alone.
//
// After marking validated, we also refresh the LoadFeeTrack remoteFee
// from the median LoadFee across trusted validations for this ledger.
// Mirrors rippled LedgerMaster::checkAccept (LedgerMaster.cpp:977-1006):
// collect sfLoadFee from trusted validations (defaulting to LoadBase
// when omitted), take the median, and call setRemoteFee. Validations
// for the parent hash are also folded in by rippled; go-xrpl keys
// validations by the attested LedgerID, so we collect for the current
// hash only — the median converges identically once a few ledgers'
// worth of validations accumulate.
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

// refreshRemoteFee mirrors rippled LedgerMaster::checkAccept's
// post-validation block (LedgerMaster.cpp:977-1006). For each trusted
// validation, the sfLoadFee value is taken (defaulting to LoadBase when
// the validator omitted the field, matching rippled validations.fees's
// substitution). The median is then forwarded to LoadFeeTrack.
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
	sort.Slice(fees, func(i, j int) bool { return fees[i] < fees[j] })
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

	// Broadcast status change to peers so rippled knows our ledger state
	switch newPhase {
	case consensus.PhaseEstablish:
		a.broadcastStatus(message.NodeEventClosingLedger)
	case consensus.PhaseAccepted:
		a.broadcastStatus(message.NodeEventAcceptedLedger)
	}

	// Notify via hooks for WebSocket subscription broadcasting
	if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
		go hooks.OnConsensusPhase(newPhase.String())
	}
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

	// NetworkTime: XRPL epoch seconds (rippled sends seconds, not microseconds)
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
