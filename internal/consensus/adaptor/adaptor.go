// Package adaptor provides the concrete implementation of the consensus.Adaptor
// interface, bridging the consensus engine to the ledger service, P2P overlay,
// and transaction queue.
package adaptor

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/LeJamon/goXRPLd/internal/peermanagement/message"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

var (
	ErrTxSetNotFound  = errors.New("transaction set not found")
	ErrLedgerNotFound = errors.New("ledger not found")
)

// NetworkSender abstracts the P2P overlay for sending messages.
// This allows testing the adaptor without a real network.
type NetworkSender interface {
	BroadcastProposal(proposal *consensus.Proposal) error
	BroadcastValidation(validation *consensus.Validation) error
	BroadcastStatusChange(sc *message.StatusChange) error
	RelayProposal(proposal *consensus.Proposal) error
	RequestTxSet(id consensus.TxSetID) error
	RequestLedger(id consensus.LedgerID) error
	RequestLedgerByHashAndSeq(hash [32]byte, seq uint32) error
	RequestLedgerBaseFromPeer(peerID uint64, hash [32]byte, seq uint32) error
	RequestReplayDelta(peerID uint64, hash [32]byte) error
	RequestStateNodes(peerID uint64, ledgerHash [32]byte, nodeIDs [][]byte) error
	SendToPeer(peerID uint64, frame []byte) error
}

// noopSender is a no-op NetworkSender for standalone or test use.
type noopSender struct{}

func (n *noopSender) BroadcastProposal(*consensus.Proposal) error              { return nil }
func (n *noopSender) BroadcastValidation(*consensus.Validation) error          { return nil }
func (n *noopSender) BroadcastStatusChange(*message.StatusChange) error        { return nil }
func (n *noopSender) RelayProposal(*consensus.Proposal) error                  { return nil }
func (n *noopSender) RequestTxSet(consensus.TxSetID) error                     { return nil }
func (n *noopSender) RequestLedger(consensus.LedgerID) error                   { return nil }
func (n *noopSender) RequestLedgerByHashAndSeq([32]byte, uint32) error         { return nil }
func (n *noopSender) RequestLedgerBaseFromPeer(uint64, [32]byte, uint32) error { return nil }
func (n *noopSender) RequestReplayDelta(uint64, [32]byte) error                { return nil }
func (n *noopSender) RequestStateNodes(uint64, [32]byte, [][]byte) error       { return nil }
func (n *noopSender) SendToPeer(uint64, []byte) error                          { return nil }

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

	logger *slog.Logger
}

// Config holds configuration for the Adaptor.
type Config struct {
	LedgerService *service.Service
	Sender        NetworkSender
	Identity      *ValidatorIdentity
	Validators    []consensus.NodeID // UNL
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
		logger:            slog.Default().With("component", "consensus-adaptor"),
	}
}

// --- Network operations ---

func (a *Adaptor) BroadcastProposal(proposal *consensus.Proposal) error {
	return a.sender.BroadcastProposal(proposal)
}

func (a *Adaptor) BroadcastValidation(validation *consensus.Validation) error {
	return a.sender.BroadcastValidation(validation)
}

func (a *Adaptor) RelayProposal(proposal *consensus.Proposal) error {
	return a.sender.RelayProposal(proposal)
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

func (a *Adaptor) GetQuorum() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.quorum
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

	// Mark the ledger as validated in the service
	a.ledgerService.SetValidatedLedger(ledger.Seq())

	a.logger.Info("Consensus reached",
		"ledger_seq", ledger.Seq(),
		"validations", len(validations),
	)

	// Fire consensus phase hook if available
	if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
		go hooks.OnConsensusPhase("accepted")
	}
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
		FirstSeq:           firstSeq,
		LastSeq:            lastSeq,
	}

	if err := a.sender.BroadcastStatusChange(sc); err != nil {
		a.logger.Warn("failed to broadcast status change", "error", err)
	}
}
