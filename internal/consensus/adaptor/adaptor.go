// Package adaptor provides the concrete implementation of the consensus.Adaptor
// interface, bridging the consensus engine to the ledger service, P2P overlay,
// and transaction queue.
package adaptor

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/amendmentvote"
	"github.com/LeJamon/go-xrpl/internal/consensus/negativeunlvote"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

var (
	ErrTxSetNotFound  = errors.New("transaction set not found")
	ErrLedgerNotFound = errors.New("ledger not found")
)

// Compile-time interface check.
var _ consensus.Adaptor = (*Adaptor)(nil)

// Adaptor implements consensus.Adaptor, bridging the consensus engine
// to the ledger service, transaction queue, and P2P network.
type Adaptor struct {
	trustUpdateMu sync.Mutex
	// trustTransitionMu serializes a complete trust snapshot publication and
	// its callback. Readers still use trustUpdateMu and may re-enter from the
	// callback; the callback itself must not synchronously start another trust
	// transition.
	trustTransitionMu sync.Mutex
	// trustTransitioning closes the finality window between publishing a new
	// trust set and installing its matching tracker snapshot.
	trustTransitioning atomic.Bool

	// mu protects trustedValidators / trustedSet / trustedMasterKeys /
	// operatingMode. Plain Mutex: these are mutated rarely and read
	// a few times per round, so RWMutex isn't justified.
	mu sync.Mutex

	ledgerService *service.Service
	txLookup      openLedgerTxLookup
	sender        consensusNetwork
	identity      *ValidatorIdentity

	// UNL: trusted validator public keys
	trustedValidators []consensus.NodeID
	trustedSet        map[consensus.NodeID]struct{}
	// trustedMasterKeys are the 33-byte master pubkeys index-aligned with
	// trustedValidators; empty when the UNL was supplied as raw NodeIDs
	// (some tests). Required for NegativeUNL voting.
	trustedMasterKeys [][33]byte

	operatingMode consensus.OperatingMode
	onModeChange  func(consensus.OperatingMode)
	modeChanges   []operatingModeChange
	modeDraining  bool

	// stateAcct tracks transition counts and cumulative durations per
	// operating mode for server_info.state_accounting.
	stateAcct *stateAccounting

	// Close-time offset, adjusted each round toward the network average.
	// Atomic ns so the Now() hot path avoids lock contention.
	closeOffsetNs atomic.Int64

	// consensusMode mirrors the engine's live consensus mode (stored by
	// OnModeChange, which the engine serializes with the phase callbacks);
	// broadcastStatus reads it to substitute LOST_SYNC while building on
	// the wrong LCL.
	consensusMode atomic.Int32

	// negUNLVoter produces the UNLModify pseudo-tx each voting ledger (at most
	// one ToDisable + one ToReEnable). nil for non-validating adaptors.
	negUNLVoter *negativeunlvote.Voter

	// validationHistorian provides per-ledger trusted-validation lookups,
	// wired by the engine after the ValidationTracker is built. Nil before
	// wiring — GenerateNegativeUNLPseudoTx degrades to no vote.
	validationHistorian consensus.ValidationHistorian

	// remoteFeeMu serializes validated-ledger callbacks so a delayed older
	// notification cannot overwrite the fee computed for a newer ledger.
	remoteFeeMu  sync.Mutex
	remoteFeeSeq uint32

	txSetCache *TxSetCache

	// Peer-reported last-closed ledger hashes, keyed by overlay peer ID.
	// Populated from the handshake and subsequent status changes so
	// getNetworkLedger can use peer LCLs before a proposal arrives.
	peerLCLsMu sync.RWMutex
	peerLCLs   map[uint64]consensus.LedgerID

	// cookie is a random 64-bit value generated at adaptor creation
	// (one-shot per boot), emitted via sfCookie on every validation.
	cookie uint64

	// feeVote is this validator's fee-vote stance, copied from Config at construction.
	feeVote FeeVoteStance

	// amendmentStances is this validator's per-amendment voting stance,
	// seeded from registry Vote behavior and overridden by Config.AmendmentVote.
	// Absent → VoteAbstain; obsolete amendments can't be overridden to VoteUp.
	amendmentStances map[[32]byte]amendmentvote.Stance

	// amendmentTable, when set, is the live amendment table this validator
	// sources vote stances from each round (so operator veto/upvote changes
	// take effect without restart) and stashes per-round tallies into. nil
	// falls back to the construction-time amendmentStances map.
	amendmentTable *amendment.Table

	// trustedVotes caches per-validator amendment votes for 24h to dampen
	// flapping when a flaky validator drops briefly.
	trustedVotes *TrustedVotes

	// onTxSetRequested fires before every RequestTxSet broadcast so the router
	// can re-arm its in-flight tx-set acquisition state. nil-safe.
	onTxSetRequested func(consensus.TxSetID)

	// onLedgerRequested turns the engine's exact wrong-LCL choice into a
	// tracked consensus acquisition. nil keeps the direct-sender fallback used
	// by standalone adaptors and focused tests.
	onLedgerRequested func(consensus.LedgerID) error

	// Set once by newRouter before the engine starts.
	onLedgerSwitched func(seq uint32, hash, parentHash [32]byte, historyFloor uint32)
	// Set once by newRouter before validation processing starts.
	onLedgerFullyValidated func(seq uint32, hash [32]byte)
	onLedgerBuilt          func(seq uint32, hash [32]byte)

	// onTxSetBuilt fires when BuildTxSet caches a new tx set, so the overlay
	// can broadcast mtHAVE_SET{tsHAVE} for it. nil-safe.
	onTxSetBuilt func(consensus.TxSetID)

	// unlBlocked reports the validator-list aggregator's UNL lock-down flag.
	// Wired at startup when publisher lists are configured; nil (no
	// publishers) means never blocked. Written once before the engine
	// starts, then only read.
	unlBlocked func() bool
	// quorumUnavailable reports whether publisher availability makes a safe
	// validation quorum unachievable. It is distinct from the consensus
	// participation lock-down when multiple publishers are configured.
	quorumUnavailable func() bool

	// refreshUNL re-evaluates the aggregator's trust view against the live
	// clock (promote rotations, latch/clear the lock-down flag). Wired to
	// the aggregator's Tick so consensus can drive it once per round —
	// rippled refreshes via updateTrusted at every ledger close, but
	// goXRPL's standalone 30s ticker leaves the flag stale for several
	// rounds after a list lapses. nil (no publishers) means no-op. Same
	// write-once-before-start lifetime as unlBlocked.
	refreshUNL func()

	// refreshInFlight single-flights RefreshUNLState's background refresh so
	// per-round dispatches can't pile up goroutines if a tick runs slow.
	refreshInFlight atomic.Bool

	// relayValidations is the operator's [relay_validations] stance.
	// Immutable after New — read without a.mu.
	relayValidations RelayValidationsPolicy

	// listedFn resolves validator-list membership (Aggregator.IsListed).
	// nil when no publisher trust is configured — nothing is listed.
	listedFn func(consensus.NodeID) bool

	// onTrustChanged publishes the matching trusted/quorum snapshot after
	// every SetTrustedValidators swap. nil-safe.
	onTrustChanged func([]consensus.NodeID, int)
	// onTrustSettled runs after the trust-change callback returns and the
	// transition gate reopens. nil-safe.
	onTrustSettled func()
	// onValidationConfigChanged runs after the validated ledger advances.
	onValidationConfigChanged func()

	// lastIssuedValidationSeq is the highest ledger seq this node has
	// broadcast a validation for — rippled's localSeqEnforcer_.largest(),
	// the trie-descent floor for preferredLCL. Zero for a non-validator.
	lastIssuedValidationSeq atomic.Uint32

	// maxDisallowedSeq is the highest ledger seq persisted before this
	// process started; the engine never proposes or validates at or below
	// it (anti-double-sign across restarts). Immutable after New.
	maxDisallowedSeq uint32

	// networkValidatedSeq is the highest sequence observed at trusted full-
	// validation quorum, including ledgers not yet available locally.
	networkValidatedSeq atomic.Uint32

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

type openLedgerTxLookup interface {
	OpenLedgerHasTx([32]byte) (bool, error)
}

// goxrplServerVersionTag identifies go-xrpl in the sfServerVersion field.
// rippled uses the top bit (0x8000...); we must NOT set it or this node
// would be counted as a rippled instance in peer version statistics.
const goxrplServerVersionTag uint64 = 0x4000_0000_0000_0000

// FeeVoteStance is this validator's desired fee structure. The Set fields
// distinguish an explicit zero from an omitted configuration value.
type FeeVoteStance struct {
	BaseFee             uint64
	ReserveBase         uint32
	ReserveIncrement    uint32
	BaseFeeSet          bool
	ReserveBaseSet      bool
	ReserveIncrementSet bool
}

// defaultFeeVote returns the fee setup a validator votes toward with no
// [voting] config.
func defaultFeeVote() FeeVoteStance {
	return FeeVoteStance{
		BaseFee:             10,
		ReserveBase:         10_000_000,
		ReserveIncrement:    2_000_000,
		BaseFeeSet:          true,
		ReserveBaseSet:      true,
		ReserveIncrementSet: true,
	}
}

// RelayValidationsPolicy mirrors rippled's RELAY_UNTRUSTED_VALIDATIONS
// tri-state, set by the [relay_validations] config key: forward verified
// current validations from outside the UNL (default), forward trusted only,
// or drop untrusted before signature verification.
type RelayValidationsPolicy int

const (
	// RelayValidationsAll relays untrusted validations too — rippled's
	// default (RELAY_UNTRUSTED_VALIDATIONS = 1). The zero value, so bare
	// Config{} adaptors get the network-friendly default.
	RelayValidationsAll RelayValidationsPolicy = iota
	// RelayValidationsTrusted verifies and processes untrusted validations
	// but relays only trusted ones (rippled 0).
	RelayValidationsTrusted
	// RelayValidationsDropUntrusted drops untrusted validations at the
	// router before signature verification (rippled -1).
	RelayValidationsDropUntrusted
)

// ParseRelayValidationsPolicy maps the [relay_validations] config string
// (case-insensitive; "" = default "all") to its policy. Unknown values are
// rejected by config validation upstream; fall back to the default here.
func ParseRelayValidationsPolicy(s string) RelayValidationsPolicy {
	switch strings.ToLower(s) {
	case "trusted":
		return RelayValidationsTrusted
	case "drop_untrusted":
		return RelayValidationsDropUntrusted
	default:
		return RelayValidationsAll
	}
}

type Config struct {
	LedgerService *service.Service
	Sender        consensusNetwork
	OnTxSetBuilt  func(consensus.TxSetID)
	Identity      *ValidatorIdentity
	Validators    []consensus.NodeID // UNL
	// ValidatorMasterKeys are the 33-byte master pubkeys index-aligned with
	// Validators. Optional — required for NegativeUNL voting (which emits raw
	// master pubkeys); nil skips NegativeUNL votes (bare-NodeID test fixtures).
	ValidatorMasterKeys [][33]byte
	// FeeVote is the validator's fee-vote stance.
	FeeVote FeeVoteStance
	// AmendmentVote lists amendments (by registry name) to vote FOR on the next
	// flag ledger. Unknown names are dropped at construction; already-enabled
	// ones are filtered per-emission since the enabled set changes over time.
	AmendmentVote []string
	// Table, when supplied, is the live amendment table owning the
	// operator's veto/upvote preferences; authoritative for stances (vetoed →
	// abstain, upvoted → up) over registry defaults. Shared with the ledger
	// service, which folds validated flag ledgers in via DoValidatedLedger.
	Table *amendment.Table
	// RelayValidations is the operator's [relay_validations] stance; the
	// zero value relays untrusted validations (rippled's default).
	RelayValidations RelayValidationsPolicy
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
		f := amendment.FeatureByName(name)
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

	cookie := generateCookie()

	logger := slog.Default().With("component", "consensus-adaptor")
	amendmentStances := seedAmendmentStances(cfg.AmendmentVote, logger)

	// Seed the amendment-vote cache with the initial UNL so
	// RecordVotes accepts validations from round one. Re-call
	// TrustChanged whenever the trusted set mutates at runtime.
	trustedVotes := NewTrustedVotes()
	trustedVotes.TrustChanged(cfg.Validators)

	feeVote := cfg.FeeVote
	defaults := defaultFeeVote()
	if !feeVote.BaseFeeSet && feeVote.BaseFee == 0 {
		feeVote.BaseFee = defaults.BaseFee
	}
	if !feeVote.ReserveBaseSet && feeVote.ReserveBase == 0 {
		feeVote.ReserveBase = defaults.ReserveBase
	}
	if !feeVote.ReserveIncrementSet && feeVote.ReserveIncrement == 0 {
		feeVote.ReserveIncrement = defaults.ReserveIncrement
	}
	feeVote.BaseFeeSet = true
	feeVote.ReserveBaseSet = true
	feeVote.ReserveIncrementSet = true

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
	if len(cfg.ValidatorMasterKeys) == len(cfg.Validators) && len(cfg.ValidatorMasterKeys) > 0 {
		trustedMasterKeys = make([][33]byte, len(cfg.ValidatorMasterKeys))
		copy(trustedMasterKeys, cfg.ValidatorMasterKeys)
	}
	if cfg.Identity != nil && len(trustedMasterKeys) > 0 {
		negUNLVoter = negativeunlvote.NewVoter(cfg.Identity.NodeID)
	}
	var txLookup openLedgerTxLookup
	if cfg.LedgerService != nil {
		txLookup = cfg.LedgerService
	}

	a := &Adaptor{
		ledgerService:     cfg.LedgerService,
		txLookup:          txLookup,
		sender:            sender,
		identity:          cfg.Identity,
		trustedValidators: cfg.Validators,
		trustedSet:        trustedSet,
		trustedMasterKeys: trustedMasterKeys,
		operatingMode:     consensus.OpModeDisconnected,
		stateAcct:         newStateAccounting(consensus.OpModeDisconnected, time.Now),
		negUNLVoter:       negUNLVoter,
		txSetCache:        NewTxSetCache(),
		peerLCLs:          make(map[uint64]consensus.LedgerID),
		reqLedgerLast:     make(map[consensus.LedgerID]time.Time),
		announcedSets:     make(map[consensus.TxSetID]struct{}),
		onTxSetBuilt:      cfg.OnTxSetBuilt,
		maxDisallowedSeq:  maxDisallowedSeq,
		cookie:            cookie,
		feeVote:           feeVote,
		amendmentStances:  amendmentStances,
		amendmentTable:    cfg.Table,
		trustedVotes:      trustedVotes,
		relayValidations:  cfg.RelayValidations,
		logger:            logger,
	}
	if cfg.LedgerService != nil {
		cfg.LedgerService.SetValidatedLedgerAgeClock(a.Now)
		cfg.LedgerService.SetOnValidatedLedger(a.onValidatedLedger)
	}
	return a
}

func (a *Adaptor) onValidatedLedger(seq uint32, hash, parentHash [32]byte) {
	a.mu.Lock()
	onValidationConfigChanged := a.onValidationConfigChanged
	a.mu.Unlock()
	if onValidationConfigChanged != nil {
		onValidationConfigChanged()
	}
	a.refreshRemoteFee(seq, consensus.LedgerID(hash), consensus.LedgerID(parentHash))
	a.logger.Info("Ledger fully validated",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
}

// SetValidationHistorian wires per-ledger trusted-validation lookups into the
// adaptor; the engine calls it once after building its ValidationTracker.
// Until set, GenerateNegativeUNLPseudoTx emits no votes.
func (a *Adaptor) SetValidationHistorian(h consensus.ValidationHistorian) {
	a.mu.Lock()
	a.validationHistorian = h
	a.mu.Unlock()
}

type validationRecheckResult uint8

const (
	validationRecheckAccepted validationRecheckResult = iota
	validationRecheckNoEvidence
	validationRecheckBelowQuorum
	validationRecheckUnavailable
)

func (a *Adaptor) recheckFullyValidated(seq uint32, hash [32]byte) (time.Time, validationRecheckResult) {
	a.mu.Lock()
	historian := a.validationHistorian
	a.mu.Unlock()
	rechecker, ok := historian.(consensus.ValidationQuorumRechecker)
	if !ok {
		return time.Time{}, validationRecheckUnavailable
	}
	validations, quorum, accepted := rechecker.RecheckFullyValidated(consensus.LedgerID(hash), seq)
	if a.IsQuorumUnavailable() {
		return time.Time{}, validationRecheckUnavailable
	}
	if len(validations) == 0 {
		return time.Time{}, validationRecheckNoEvidence
	}
	if !accepted || len(validations) < quorum {
		return time.Time{}, validationRecheckBelowQuorum
	}
	signTime, count := sampleValidatedSignTime(validations, seq)
	if count < quorum {
		return time.Time{}, validationRecheckBelowQuorum
	}
	return signTime, validationRecheckAccepted
}

// UpdatePeerLCL records a peer's last-closed-ledger hash so getNetworkLedger
// can fall back to peer LCLs when proposal votes are absent or stale. The zero
// hash removes any existing entry.
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

// RelayProposal forwards a peer-originated proposal, excluding exceptPeer
// (0 = everyone).
func (a *Adaptor) RelayProposal(proposal *consensus.Proposal, exceptPeer uint64) error {
	return a.sender.RelayProposal(proposal, exceptPeer)
}

// RelayValidation forwards a peer-originated validation, excluding exceptPeer.
// Mirrors RelayProposal.
func (a *Adaptor) RelayValidation(validation *consensus.Validation, exceptPeer uint64) error {
	return a.sender.RelayValidation(validation, exceptPeer)
}

// UpdateRelaySlot feeds the reduce-relay slot for validatorKey with originPeer
// and seenPeers (known-havers).
func (a *Adaptor) UpdateRelaySlot(validatorKey []byte, originPeer uint64, seenPeers []uint64) {
	a.sender.UpdateRelaySlot(validatorKey, originPeer, seenPeers)
}

// SetOnTxSetRequested registers a callback invoked at the start of every
// RequestTxSet, so the router can reset throttle/attempt bookkeeping on the
// in-flight acquisition. Set once at startup; not concurrency-safe.
func (a *Adaptor) SetOnTxSetRequested(cb func(consensus.TxSetID)) {
	a.onTxSetRequested = cb
}

func (a *Adaptor) SetOnLedgerRequested(cb func(consensus.LedgerID) error) {
	a.onLedgerRequested = cb
}

func (a *Adaptor) setOnLedgerSwitched(cb func(uint32, [32]byte, [32]byte, uint32)) {
	a.onLedgerSwitched = cb
}

func (a *Adaptor) setOnLedgerFullyValidated(cb func(uint32, [32]byte)) {
	a.onLedgerFullyValidated = cb
}

func (a *Adaptor) setOnLedgerBuilt(cb func(uint32, [32]byte)) {
	a.onLedgerBuilt = cb
}

func (a *Adaptor) RequestTxSet(id consensus.TxSetID) error {
	if id == (consensus.TxSetID{}) {
		return nil
	}
	if a.onTxSetRequested != nil {
		a.onTxSetRequested(id)
	}
	return a.sender.RequestTxSet(id)
}

func (a *Adaptor) RequestLedger(id consensus.LedgerID) error {
	if a.onLedgerRequested != nil {
		return a.onLedgerRequested(id)
	}

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

// EngineConfigForReplay returns the shared (non-per-ledger) tx.EngineConfig
// for replaying a ledger anchored on parent (fees from parent's FeeSettings
// SLE). The caller overrides the per-ledger fields from the target header.
func (a *Adaptor) EngineConfigForReplay(parent *ledger.Ledger) tx.EngineConfig {
	if a.ledgerService == nil {
		return tx.EngineConfig{}
	}
	return a.ledgerService.EngineConfigForReplay(parent)
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
	l, err := a.ledgerService.AdoptedLedgerBySequence(seq)
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

func (a *Adaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time, closeTimeCorrect bool, disputedTxs [][]byte) (consensus.Ledger, error) {
	var parentLedger *ledger.Ledger
	if w, ok := parent.(*LedgerWrapper); ok {
		parentLedger = w.Unwrap()
	}
	// context.TODO: BuildLedger's interface has no context, so persistence
	// here can't be cancelled by the engine (#185).
	seq, err := a.ledgerService.AcceptConsensusResult(context.TODO(), parentLedger, txSet.Txs(), disputedTxs, closeTime, closeTimeCorrect)
	if err != nil {
		return nil, err
	}

	l, err := a.ledgerService.GetLedgerBySequence(seq)
	if err != nil {
		return nil, err
	}
	if signTime, result := a.recheckFullyValidated(seq, l.Hash()); result == validationRecheckAccepted {
		a.ledgerService.SetValidatedLedgerAt(seq, l.Hash(), signTime)
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

	// The producer boundary is intentionally defensive: only full validations
	// for the exact parent hash/sequence may influence fee or amendment votes.
	// The engine normally supplies this already-filtered snapshot from the
	// tracker, but callers such as replay tools and tests can invoke the
	// adaptor directly.
	parentID := consensus.LedgerID(prev.ParentHash())
	var parentSeq uint32
	if prev.Sequence() > 0 {
		parentSeq = prev.Sequence() - 1
	}
	filtered := filterFullValidations(parentValidations, parentID, parentSeq, a.IsTrusted)
	filtered = a.filterNegativeUNL(filtered)

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

func filterFullValidations(
	vals []*consensus.Validation,
	ledgerID consensus.LedgerID,
	ledgerSeq uint32,
	isTrusted func(consensus.NodeID) bool,
) []*consensus.Validation {
	if len(vals) == 0 {
		return vals
	}
	out := make([]*consensus.Validation, 0, len(vals))
	for _, validation := range vals {
		if validation == nil || !validation.Full || validation.LedgerID != ledgerID ||
			validation.LedgerSeq != ledgerSeq || (isTrusted != nil && !isTrusted(validation.NodeID)) {
			continue
		}
		out = append(out, validation)
	}
	return out
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

// HasTx reports whether the persistent open view contains this tx.
// Used by the peer protocol for HaveSet / txSet-acquire negotiation.
func (a *Adaptor) HasTx(id consensus.TxID) (bool, error) {
	if a.txLookup == nil {
		return false, nil
	}
	return a.txLookup.OpenLedgerHasTx([32]byte(id))
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
			uint32(hdr.CloseTimeResolution),
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
