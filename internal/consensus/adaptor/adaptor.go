// Package adaptor provides the concrete implementation of the consensus.Adaptor
// interface, bridging the consensus engine to the ledger service, P2P overlay,
// and transaction queue.
package adaptor

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/pseudo"
	"github.com/LeJamon/goXRPLd/keylet"
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
	RequestLedger(id consensus.LedgerID) error
	RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error
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

func (n *noopSender) BroadcastProposal(*consensus.Proposal) error              { return nil }
func (n *noopSender) BroadcastValidation(*consensus.Validation) error          { return nil }
func (n *noopSender) BroadcastStatusChange(*message.StatusChange) error        { return nil }
func (n *noopSender) RelayProposal(*consensus.Proposal, uint64) error          { return nil }
func (n *noopSender) RelayValidation(*consensus.Validation, uint64) error      { return nil }
func (n *noopSender) UpdateRelaySlot([]byte, uint64, []uint64)                 {}
func (n *noopSender) RequestTxSet(consensus.TxSetID) error                     { return nil }
func (n *noopSender) RequestLedger(consensus.LedgerID) error                   { return nil }
func (n *noopSender) RequestLedgerByHashAndSeq([32]byte, uint32) error         { return nil }
func (n *noopSender) RequestLedgerBaseFromPeer(uint64, [32]byte, uint32) error { return nil }
func (n *noopSender) RequestReplayDelta(uint64, [32]byte) error                { return nil }
func (n *noopSender) RequestStateNodes(uint64, [32]byte, [][]byte) error       { return nil }
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
	mu sync.RWMutex

	ledgerService *service.Service
	sender        NetworkSender
	identity      *ValidatorIdentity

	// UNL: trusted validator public keys
	trustedValidators []consensus.NodeID
	trustedSet        map[consensus.NodeID]struct{}
	quorum            int

	// Operating mode
	operatingMode consensus.OperatingMode

	// Close time offset — adjusted each round toward network average.
	// Matches rippled's timeKeeper().closeTime() offset.
	closeOffset time.Duration

	// Transaction set cache
	txSetCache *TxSetCache

	// Pending transactions (raw blobs) from RPC submissions and peer relay
	pendingTxsMu sync.RWMutex
	pendingTxs   map[consensus.TxID][]byte

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

	// amendmentVoteIDs are the amendment IDs this validator wishes to
	// vote FOR on the next flag ledger. Resolved from Config.AmendmentVote
	// names at construction (unknown names logged and dropped).
	amendmentVoteIDs [][32]byte

	logger *slog.Logger
}

// goXRPLServerVersionTag identifies this implementation in the
// sfServerVersion field. Rippled uses the top bit (0x8000...) as its
// own identifier; goXRPL must NOT set that — setting it would
// misrepresent this node as a rippled instance in any peer counting
// version statistics on the network. We pick a distinct non-rippled
// high-byte pattern so operators running both implementations can
// tell them apart at a glance.
const goXRPLServerVersionTag uint64 = 0x4000_0000_0000_0000

// FeeVoteStance is this validator's desired fee structure — what it
// wants the network to converge on at the next flag ledger. Emitted
// on every validation as either the legacy UINT triple
// (BaseFee/ReserveBase/ReserveIncrement) or the post-XRPFees AMOUNT
// triple (BaseFeeDrops/ReserveBaseDrops/ReserveIncrementDrops).
// The adaptor picks which set to emit based on the parent ledger's
// rules — matches rippled's FeeVoteImpl.cpp:120-192 hard if/else
// gate on featureXRPFees.
//
// Zero values mean "no vote" — the serializer skips the fields.
type FeeVoteStance struct {
	BaseFee          uint64
	ReserveBase      uint32
	ReserveIncrement uint32
}

// Config holds configuration for the Adaptor.
type Config struct {
	LedgerService *service.Service
	Sender        NetworkSender
	Identity      *ValidatorIdentity
	Validators    []consensus.NodeID // UNL
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

	// Resolve amendment-vote names to IDs. Unknown names are logged
	// and dropped — an operator with a stale config shouldn't block
	// node boot. Same behavior as rippled silently skipping unknown
	// amendments from [amendments].
	logger := slog.Default().With("component", "consensus-adaptor")
	var amendmentVoteIDs [][32]byte
	for _, name := range cfg.AmendmentVote {
		f := amendment.GetFeatureByName(name)
		if f == nil {
			logger.Warn("unknown amendment in vote config; ignoring", "name", name)
			continue
		}
		amendmentVoteIDs = append(amendmentVoteIDs, f.ID)
	}

	return &Adaptor{
		ledgerService:     cfg.LedgerService,
		sender:            sender,
		identity:          cfg.Identity,
		trustedValidators: cfg.Validators,
		trustedSet:        trustedSet,
		quorum:            quorum,
		operatingMode:     consensus.OpModeDisconnected,
		txSetCache:        NewTxSetCache(),
		pendingTxs:        make(map[consensus.TxID][]byte),
		peerLCLs:          make(map[uint64]consensus.LedgerID),
		cookie:            cookie,
		feeVote:           cfg.FeeVote,
		amendmentVoteIDs:  amendmentVoteIDs,
		logger:            logger,
	}
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

func (a *Adaptor) RequestTxSet(id consensus.TxSetID) error {
	return a.sender.RequestTxSet(id)
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

func (a *Adaptor) RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error {
	return a.sender.RequestStateNodes(peerID, ledgerHash, nodeIDs)
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

// GetParentLedgerForReplay returns the validated ledger at seq-1, which is
// the prior ledger needed to replay a delta into seq. Returns nil if the
// parent is unknown or the request is for a ledger we cannot anchor on
// (seq <= 1, no service wired). Mirrors the rippled
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

func (a *Adaptor) BuildLedger(parent consensus.Ledger, txSet consensus.TxSet, closeTime time.Time) (consensus.Ledger, error) {
	// Unwrap the parent to get the concrete ledger for the service.
	// This is critical for chain switching: the parent may differ from
	// the service's internal closedLedger after wrong ledger detection.
	var parentLedger *ledger.Ledger
	if w, ok := parent.(*LedgerWrapper); ok {
		parentLedger = w.Unwrap()
	}
	seq, err := a.ledgerService.AcceptConsensusResult(parentLedger, txSet.Txs(), closeTime)
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

func (a *Adaptor) GetPendingTxs() [][]byte {
	a.pendingTxsMu.RLock()
	defer a.pendingTxsMu.RUnlock()

	blobs := make([][]byte, 0, len(a.pendingTxs))
	for _, blob := range a.pendingTxs {
		blobs = append(blobs, blob)
	}
	return blobs
}

func (a *Adaptor) GetTxSet(id consensus.TxSetID) (consensus.TxSet, error) {
	ts, ok := a.txSetCache.Get(id)
	if !ok {
		return nil, ErrTxSetNotFound
	}
	return ts, nil
}

func (a *Adaptor) BuildTxSet(txs [][]byte) (consensus.TxSet, error) {
	ts := NewTxSet(txs)
	a.txSetCache.Put(ts)
	return ts, nil
}

func (a *Adaptor) HasTx(id consensus.TxID) bool {
	a.pendingTxsMu.RLock()
	defer a.pendingTxsMu.RUnlock()
	_, ok := a.pendingTxs[id]
	return ok
}

func (a *Adaptor) GetTx(id consensus.TxID) ([]byte, error) {
	a.pendingTxsMu.RLock()
	defer a.pendingTxsMu.RUnlock()
	blob, ok := a.pendingTxs[id]
	if !ok {
		return nil, errors.New("transaction not found")
	}
	return blob, nil
}

// AddPendingTx adds a transaction to the pending pool.
func (a *Adaptor) AddPendingTx(blob []byte) {
	txID := computeTxID(blob)
	a.pendingTxsMu.Lock()
	defer a.pendingTxsMu.Unlock()
	a.pendingTxs[txID] = blob
}

// ClearPendingTxs removes all pending transactions.
func (a *Adaptor) ClearPendingTxs() {
	a.pendingTxsMu.Lock()
	defer a.pendingTxsMu.Unlock()
	a.pendingTxs = make(map[consensus.TxID][]byte)
}

// RemovePendingTxs removes specific transactions from the pending pool.
// Used after consensus to remove only txs that were included in the ledger,
// keeping any txs that arrived after the tx set was built.
func (a *Adaptor) RemovePendingTxs(txBlobs [][]byte) {
	a.pendingTxsMu.Lock()
	defer a.pendingTxsMu.Unlock()
	for _, blob := range txBlobs {
		txID := computeTxID(blob)
		delete(a.pendingTxs, txID)
	}
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
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.trustedSet[node]
	return ok
}

func (a *Adaptor) GetTrustedValidators() []consensus.NodeID {
	a.mu.RLock()
	defer a.mu.RUnlock()
	result := make([]consensus.NodeID, len(a.trustedValidators))
	copy(result, a.trustedValidators)
	return result
}

// GetQuorum returns the current quorum requirement, recomputed on
// every call to account for negative-UNL changes. Matches rippled's
// ValidatorList.cpp:2061-2087 which recomputes quorum on every
// UNL/negUNL change as ceil(0.8 * (trusted - disabled)). Pre-R6b.3
// goXRPL froze quorum at Adaptor construction, so partial-UNL
// outages slowed finality relative to rippled.
func (a *Adaptor) GetQuorum() int {
	trusted := len(a.trustedValidators)
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
		var id consensus.NodeID
		copy(id[:], pubKey)
		out = append(out, id)
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
// goXRPL as rippled in peer version-counting statistics; we use
// 0x4000... as a goXRPL tag and OR in a package version number.
func (a *Adaptor) GetServerVersion() uint64 {
	// Low bits are available for a semantic version encoding in the
	// future; for now they stay zero so the tag byte is sufficient to
	// identify a goXRPL validator.
	return goXRPLServerVersionTag
}

// GetFeeVote returns this validator's fee-vote stance and whether the
// post-XRPFees rules should apply. postXRPFees is read from the
// parent ledger's rules so voting switches the instant the amendment
// activates — mirrors rippled's FeeVoteImpl.cpp:120-192 hard gate.
// Zero stance values mean "no vote" and the serializer will omit the
// fields.
// GetLoadFee returns the local load_fee advertised on outbound
// validations. Today we have no feedback loop so we always return 0
// — the serializer treats that as "omit", matching rippled's
// behavior on a validator with minimum load. Future work can wire
// this to a LoadFeeTrack-equivalent.
func (a *Adaptor) GetLoadFee() uint32 {
	return 0
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
func (a *Adaptor) GetAmendmentVote() [][32]byte {
	if len(a.amendmentVoteIDs) == 0 {
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

	out := make([][32]byte, 0, len(a.amendmentVoteIDs))
	for _, id := range a.amendmentVoteIDs {
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

// --- Time operations ---

func (a *Adaptor) Now() time.Time {
	a.mu.RLock()
	offset := a.closeOffset
	a.mu.RUnlock()
	return time.Now().Add(offset)
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

// AdjustCloseTime computes the weighted average of all raw close times
// and adjusts our clock offset toward the network. Matches rippled's
// adjustCloseTime() in RCLConsensus.cpp:694-732.
func (a *Adaptor) AdjustCloseTime(rawCloseTimes consensus.CloseTimes) {
	if rawCloseTimes.Self.IsZero() {
		return
	}

	totalSecs := rawCloseTimes.Self.Unix()
	count := int64(1)
	for t, v := range rawCloseTimes.Peers {
		count += int64(v)
		totalSecs += t.Unix() * int64(v)
	}
	avgSecs := (totalSecs + count/2) / count
	avg := time.Unix(avgSecs, 0)

	offset := avg.Sub(rawCloseTimes.Self)

	a.mu.Lock()
	a.closeOffset = offset
	a.mu.Unlock()

	if offset != 0 {
		a.logger.Debug("adjusted close time offset",
			"offset_ms", offset.Milliseconds(),
			"peers", len(rawCloseTimes.Peers),
		)
	}
}

// --- Status operations ---

func (a *Adaptor) GetOperatingMode() consensus.OperatingMode {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.operatingMode
}

func (a *Adaptor) SetOperatingMode(mode consensus.OperatingMode) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.operatingMode = mode
}

func (a *Adaptor) OnConsensusReached(ledger consensus.Ledger, validations []*consensus.Validation) {
	// Remove only txs that were included in the closed ledger.
	// Txs that arrived after the tx set was built stay in the pool
	// for the next round — matching rippled's LocalTxs behavior.
	wrapper, ok := ledger.(*LedgerWrapper)
	if ok {
		l := wrapper.Unwrap()
		l.ForEachTransaction(func(txHash [32]byte, _ []byte) bool {
			a.pendingTxsMu.Lock()
			delete(a.pendingTxs, consensus.TxID(txHash))
			a.pendingTxsMu.Unlock()
			return true
		})
	}

	// NOTE: we intentionally do NOT mark the ledger validated here.
	// The validated_ledger pointer only advances once trusted-validation
	// quorum is reached — see OnLedgerFullyValidated, driven by the
	// engine's ValidationTracker. This matches rippled's checkAccept()
	// semantics where local consensus != network agreement.

	a.logger.Info("Consensus reached",
		"ledger_seq", ledger.Seq(),
		"validations", len(validations),
	)

	// Fire consensus phase hook if available
	if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
		go hooks.OnConsensusPhase("accepted")
	}
}

// OnLedgerFullyValidated fires when the engine's ValidationTracker sees
// trusted-validation quorum for a ledger. We flip the service's
// validated_ledger only if our stored ledger at that seq has the matching
// hash — fork safety, matching rippled's checkAccept which operates on
// the specific ledger pointer, not seq alone.
func (a *Adaptor) OnLedgerFullyValidated(ledgerID consensus.LedgerID, seq uint32) {
	var hash [32]byte
	copy(hash[:], ledgerID[:])
	a.ledgerService.SetValidatedLedger(seq, hash)
	a.logger.Info("Ledger fully validated",
		"seq", seq,
		"hash", fmt.Sprintf("%x", hash[:8]),
	)
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
	networkTime := uint64(time.Now().Unix() - xrplEpochOffset)

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
