// Package adaptor provides the concrete implementation of the consensus.Adaptor
// interface, bridging the consensus engine to the ledger service, P2P overlay,
// and transaction queue.
package adaptor

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
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
	RelayProposal(proposal *consensus.Proposal) error
	RequestTxSet(id consensus.TxSetID) error
	RequestLedger(id consensus.LedgerID) error
}

// noopSender is a no-op NetworkSender for standalone or test use.
type noopSender struct{}

func (n *noopSender) BroadcastProposal(*consensus.Proposal) error     { return nil }
func (n *noopSender) BroadcastValidation(*consensus.Validation) error { return nil }
func (n *noopSender) RelayProposal(*consensus.Proposal) error         { return nil }
func (n *noopSender) RequestTxSet(consensus.TxSetID) error            { return nil }
func (n *noopSender) RequestLedger(consensus.LedgerID) error          { return nil }

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
	// Apply the consensus-agreed transaction set to produce a new ledger
	seq, err := a.ledgerService.AcceptConsensusResult(txSet.Txs(), closeTime)
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

// ClearPendingTxs removes all pending transactions (after ledger close).
func (a *Adaptor) ClearPendingTxs() {
	a.pendingTxsMu.Lock()
	defer a.pendingTxsMu.Unlock()
	a.pendingTxs = make(map[consensus.TxID][]byte)
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
	return time.Now()
}

func (a *Adaptor) CloseTimeResolution() time.Duration {
	return consensus.DefaultTiming().LedgerGranularity
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
	// Clear pending transactions that were included in the consensus
	a.ClearPendingTxs()

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

func (a *Adaptor) OnPhaseChange(oldPhase, newPhase consensus.Phase) {
	a.logger.Debug("Consensus phase changed",
		"from", oldPhase.String(),
		"to", newPhase.String(),
	)

	// Notify via hooks for WebSocket subscription broadcasting
	if hooks := a.ledgerService.GetEventHooks(); hooks != nil && hooks.OnConsensusPhase != nil {
		go hooks.OnConsensusPhase(newPhase.String())
	}
}
