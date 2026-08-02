package service

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
)

const invalidTransactionIndex = ^uint32(0)

const maxLedgerPublicationGap uint32 = 100

const maxPublicationQueue = 1024

// LedgerAcceptedEvent contains information about an accepted ledger and its transactions
type LedgerAcceptedEvent struct {
	LedgerInfo         *LedgerInfo
	Ledger             *ledger.Ledger
	TransactionResults []TransactionResultEvent
}

// TransactionResultEvent contains transaction details for event broadcasting
type TransactionResultEvent struct {
	TxHash [32]byte
	TxData []byte

	// MetaData is the transaction metadata (nil if not available)
	MetaData []byte

	// Validated indicates if the transaction is in a validated ledger
	Validated bool

	LedgerIndex      uint32
	LedgerHash       [32]byte
	AffectedAccounts []string
}

type EventSink interface {
	// LedgerAccepted must not call Service.Stop synchronously.
	LedgerAccepted(event *LedgerAcceptedEvent)
}

type EventSinkFunc func(event *LedgerAcceptedEvent)

func (f EventSinkFunc) LedgerAccepted(event *LedgerAcceptedEvent) {
	if f != nil {
		f(event)
	}
}

// SubmittedTxEvent carries the inputs the WebSocket transactions_proposed
// publisher needs from a SubmitTransaction call.
type SubmittedTxEvent struct {
	RawBlob          []byte
	TxHash           [32]byte
	AffectedAccounts []string
	CurrentLedger    uint32
	OwnerFunds       string
	Result           Result
}

// Result is a slim mirror of tx.ApplyResult — copied here so the RPC
// layer can consume the event without importing internal/tx.
type Result struct {
	Code    int
	Name    string
	Message string
	Applied bool
}

// SubmittedTxCallback runs on the publication worker. It must return promptly
// and must not call Service.Stop synchronously.
type SubmittedTxCallback func(SubmittedTxEvent)

type publicationEvent struct {
	ledger    *LedgerAcceptedEvent
	submitted *SubmittedTxEvent
}

// Errors reports fatal publication failures that require runtime shutdown.
func (s *Service) Errors() <-chan error {
	return s.eventPublisher.publicationErrors
}

func (s *Service) SetEventSink(sink EventSink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventPublisher.setEventSink(sink)
}

func (p *eventPublisher) setEventSink(sink EventSink) {
	p.subscriberMu.Lock()
	defer p.subscriberMu.Unlock()
	p.eventSink = sink
}

func (p *eventPublisher) hasEventSink() bool {
	p.subscriberMu.RLock()
	defer p.subscriberMu.RUnlock()
	return p.eventSink != nil
}

func (p *eventPublisher) setSubmittedTxCallback(fn SubmittedTxCallback) {
	p.subscriberMu.Lock()
	defer p.subscriberMu.Unlock()
	p.submittedTxCallback = fn
}

func (p *eventPublisher) hasSubmittedTxCallback() bool {
	p.subscriberMu.RLock()
	defer p.subscriberMu.RUnlock()
	return p.submittedTxCallback != nil
}

// dispatchLedgerEvent enqueues accepted ledgers for FIFO, single-threaded
// delivery. Admission is bounded and fails closed on overload.
func (s *Service) dispatchLedgerEvent(event *LedgerAcceptedEvent) {
	s.eventPublisher.dispatchLedgerEvent(event)
}

func (p *eventPublisher) dispatchLedgerEvent(event *LedgerAcceptedEvent) {
	if event == nil {
		return
	}
	p.ledgerEventMu.Lock()
	if p.ledgerEventStopping {
		p.ledgerEventMu.Unlock()
		return
	}
	startDispatcher := p.startLedgerEventDispatcherLocked()
	if startDispatcher {
		go p.runLedgerEventDispatcher()
	}
	ready := p.ledgerEventsReadyToQueueLocked(event)
	for _, ledgerEvent := range ready {
		if !p.enqueuePublicationLocked(publicationEvent{ledger: ledgerEvent}) {
			break
		}
	}
	p.ledgerEventMu.Unlock()
}

func (p *eventPublisher) dispatchSubmittedTxEvent(event SubmittedTxEvent) {
	p.ledgerEventMu.Lock()
	if p.ledgerEventStopping {
		p.ledgerEventMu.Unlock()
		return
	}
	startDispatcher := p.startLedgerEventDispatcherLocked()
	if startDispatcher {
		go p.runLedgerEventDispatcher()
	}
	copy := event
	p.enqueuePublicationLocked(publicationEvent{submitted: &copy})
	p.ledgerEventMu.Unlock()
}

func (p *eventPublisher) enqueuePublicationLocked(event publicationEvent) bool {
	p.ensurePublicationQueueLocked()
	if p.ledgerEventStopping || p.publicationFailed {
		return false
	}
	if len(p.publicationQueue) >= p.publicationLimit {
		p.publicationFailed = true
		p.publicationFailureOnce.Do(func() {
			select {
			case p.publicationErrors <- fmt.Errorf("publication queue exceeded capacity %d", p.publicationLimit):
			default:
			}
		})
		return false
	}
	p.publicationQueue = append(p.publicationQueue, event)
	select {
	case p.ledgerEventWake <- struct{}{}:
	default:
	}
	return true
}

func (p *eventPublisher) ensurePublicationQueueLocked() {
	if p.publicationLimit <= 0 {
		p.publicationLimit = maxPublicationQueue
	}
	if p.publicationErrors == nil {
		p.publicationErrors = make(chan error, 1)
	}
}

func (p *eventPublisher) startLedgerEventDispatcherLocked() bool {
	if p.ledgerEventStarted || p.ledgerEventStopping {
		return false
	}
	p.ledgerEventWake = make(chan struct{}, 1)
	p.ensurePublicationQueueLocked()
	p.ledgerEventStarted = true
	p.ledgerEventWG.Add(1)
	return true
}

func (p *eventPublisher) start() {
	p.ledgerEventMu.Lock()
	startDispatcher := p.startLedgerEventDispatcherLocked()
	p.ledgerEventMu.Unlock()
	if startDispatcher {
		go p.runLedgerEventDispatcher()
	}
}

func (p *eventPublisher) stop() {
	p.ledgerEventMu.Lock()
	started := p.ledgerEventStarted
	if !p.ledgerEventStopping {
		p.ledgerEventStopping = true
		if started {
			select {
			case p.ledgerEventWake <- struct{}{}:
			default:
			}
		}
	}
	p.ledgerEventMu.Unlock()
	if started {
		p.ledgerEventWG.Wait()
	}
}

func (p *eventPublisher) ledgerEventsReadyToQueueLocked(event *LedgerAcceptedEvent) []*LedgerAcceptedEvent {
	if event.Ledger == nil || !event.Ledger.IsValidated() {
		return []*LedgerAcceptedEvent{event}
	}

	seq := event.Ledger.Sequence()
	hash := event.Ledger.Hash()
	if !p.ledgerEventHaveFrontier {
		p.ledgerEventHaveFrontier = true
		p.ledgerEventFrontierSeq = seq
		p.ledgerEventFrontierHash = hash
		p.pruneLedgerEventCandidatesLocked(seq)
		return []*LedgerAcceptedEvent{event}
	}
	if seq <= p.ledgerEventFrontierSeq {
		return nil
	}
	if seq-p.ledgerEventFrontierSeq > maxLedgerPublicationGap {
		p.ledgerEventFrontierSeq = seq
		p.ledgerEventFrontierHash = hash
		p.pruneLedgerEventCandidatesLocked(seq)
		return []*LedgerAcceptedEvent{event}
	}

	p.ledgerEventCandidates[seq] = event
	ready := make([]*LedgerAcceptedEvent, 0, len(p.ledgerEventCandidates))
	for p.ledgerEventFrontierSeq != ^uint32(0) {
		nextSeq := p.ledgerEventFrontierSeq + 1
		next, ok := p.ledgerEventCandidates[nextSeq]
		if !ok || next.Ledger.ParentHash() != p.ledgerEventFrontierHash {
			break
		}
		delete(p.ledgerEventCandidates, nextSeq)
		ready = append(ready, next)
		p.ledgerEventFrontierSeq = nextSeq
		p.ledgerEventFrontierHash = next.Ledger.Hash()
	}
	return ready
}

func (p *eventPublisher) pruneLedgerEventCandidatesLocked(frontier uint32) {
	for seq := range p.ledgerEventCandidates {
		if seq <= frontier {
			delete(p.ledgerEventCandidates, seq)
		}
	}
}

// runLedgerEventDispatcher is the single consumer that delivers accepted-ledger
// events in FIFO order. It drains the queue on Stop before exiting so a shutdown
// doesn't drop already-queued stream events.
func (p *eventPublisher) runLedgerEventDispatcher() {
	defer p.ledgerEventWG.Done()
	for {
		<-p.ledgerEventWake
		for {
			p.ledgerEventMu.Lock()
			if len(p.publicationQueue) != 0 {
				ev := p.publicationQueue[0]
				p.publicationQueue[0] = publicationEvent{}
				p.publicationQueue = p.publicationQueue[1:]
				p.ledgerEventMu.Unlock()
				p.deliverPublication(ev)
				continue
			}
			stopping := p.ledgerEventStopping
			p.ledgerEventMu.Unlock()
			if stopping {
				return
			}
			break
		}
	}
}

func (p *eventPublisher) deliverPublication(event publicationEvent) {
	if event.submitted != nil {
		p.subscriberMu.RLock()
		callback := p.submittedTxCallback
		p.subscriberMu.RUnlock()
		if callback != nil {
			callback(*event.submitted)
		}
		return
	}
	if event.ledger != nil {
		p.deliverLedgerEvent(event.ledger)
	}
}

// deliverLedgerEvent advances the published frontier at the ordered delivery
// boundary, then invokes the current callback outside the lock.
func (p *eventPublisher) deliverLedgerEvent(event *LedgerAcceptedEvent) {
	s := p.service
	s.mu.Lock()
	if event.Ledger != nil && event.Ledger.IsValidated() {
		seq := event.Ledger.Sequence()
		if !s.havePublished || seq > s.publishedLedgerSeq {
			s.publishedLedgerSeq = seq
			s.havePublished = true
		}
	}
	s.mu.Unlock()
	p.subscriberMu.RLock()
	sink := p.eventSink
	p.subscriberMu.RUnlock()
	if sink != nil {
		sink.LedgerAccepted(event)
	}
}

// SetSubmittedTxCallback registers the proposed-transaction sink. Pass nil to
// unwire. The callback contract is documented on SubmittedTxCallback.
func (s *Service) SetSubmittedTxCallback(fn SubmittedTxCallback) {
	s.eventPublisher.setSubmittedTxCallback(fn)
}

// SetTxRelay registers the per-tx broadcast handler invoked by
// OpenLedger.Accept's relay callback. Pass nil to unwire.
func (s *Service) SetTxRelay(fn func(blob []byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.txRelay = fn
}

// SetOnPendingValidationStashed registers a handler invoked off-thread
// when SetValidatedLedger stashes a validation that doesn't match a
// ledger we have. Pass nil to unwire.
func (s *Service) SetOnPendingValidationStashed(handler func(seq uint32, hash [32]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onPendingValidationStashed = handler
}

// PendingValidationResolver rechecks whether a stashed validation notification
// still has quorum when its ledger arrives and returns the current signing-time
// median. It runs while the service mutex is held and must not call the Service.
type PendingValidationResolver func(seq uint32, hash [32]byte) (time.Time, bool)

// SetPendingValidationResolver installs the adoption-time quorum recheck.
func (s *Service) SetPendingValidationResolver(resolver PendingValidationResolver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pendingValidationResolver = resolver
}

// SetOnValidatedLedger registers a handler invoked after the validated tip
// advances and the service lock has been released. Pass nil to unwire.
func (s *Service) SetOnValidatedLedger(handler func(seq uint32, hash, parentHash [32]byte)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onValidatedLedger = handler
}

type validatedLedgerNotification struct {
	handler    func(seq uint32, hash, parentHash [32]byte)
	seq        uint32
	hash       [32]byte
	parentHash [32]byte
}

// Caller must hold s.mu for writing and invoke notify only after unlocking.
func (s *Service) validatedLedgerNotificationLocked(previous *ledger.Ledger) validatedLedgerNotification {
	if s.onValidatedLedger == nil || s.validatedLedger == nil {
		return validatedLedgerNotification{}
	}
	if previous != nil && (s.validatedLedger.Sequence() < previous.Sequence() ||
		(s.validatedLedger.Sequence() == previous.Sequence() && s.validatedLedger.Hash() == previous.Hash())) {
		return validatedLedgerNotification{}
	}
	return validatedLedgerNotification{
		handler:    s.onValidatedLedger,
		seq:        s.validatedLedger.Sequence(),
		hash:       s.validatedLedger.Hash(),
		parentHash: s.validatedLedger.ParentHash(),
	}
}

func (s *Service) unlockAndNotifyValidatedLedger(previous *ledger.Ledger) {
	notification := s.validatedLedgerNotificationLocked(previous)
	s.mu.Unlock()
	notification.notify()
}

func (n validatedLedgerNotification) notify() {
	if n.handler != nil {
		n.handler(n.seq, n.hash, n.parentHash)
	}
}

type transactionResultSource interface {
	IsValidated() bool
	ForEachTransaction(func(txHash [32]byte, txData []byte) bool) error
}

type stagedTransactionResults struct {
	results          []TransactionResultEvent
	positions        map[[32]byte]uint32
	missingPositions [][32]byte
	ledgerSeq        uint32
}

// collectTransactionResults is the history-safe entry point for callers that
// are not already inside a frontier/history mutation.
func (s *Service) collectTransactionResults(l transactionResultSource, ledgerSeq uint32, ledgerHash [32]byte) ([]TransactionResultEvent, error) {
	staged, err := stageTransactionResults(l, ledgerSeq, ledgerHash)
	if err != nil {
		return nil, err
	}
	s.historyComponent.mu.Lock()
	defer s.historyComponent.mu.Unlock()
	s.commitTransactionResultsLocked(staged)
	return staged.results, nil
}

// collectTransactionResultsLocked gathers per-tx results and updates the
// history component's transaction indexes. Caller holds historyComponent.mu.
func (s *Service) collectTransactionResultsLocked(l transactionResultSource, ledgerSeq uint32, ledgerHash [32]byte) ([]TransactionResultEvent, error) {
	staged, err := stageTransactionResults(l, ledgerSeq, ledgerHash)
	if err != nil {
		return nil, err
	}
	s.commitTransactionResultsLocked(staged)
	return staged.results, nil
}

func stageTransactionResults(l transactionResultSource, ledgerSeq uint32, ledgerHash [32]byte) (*stagedTransactionResults, error) {
	staged := &stagedTransactionResults{
		positions: make(map[[32]byte]uint32),
		ledgerSeq: ledgerSeq,
	}
	var results []TransactionResultEvent
	validated := l.IsValidated()

	if err := l.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		result := TransactionResultEvent{
			TxHash:      txHash,
			TxData:      txData,
			Validated:   validated,
			LedgerIndex: ledgerSeq,
			LedgerHash:  ledgerHash,
		}
		result.AffectedAccounts = extractAffectedAccounts(txData)

		if txIndex, ok := txcore.TransactionIndexFromTxWithMetaBlob(txData); ok {
			staged.positions[txHash] = txIndex
		} else {
			staged.missingPositions = append(staged.missingPositions, txHash)
		}

		results = append(results, result)
		return true
	}); err != nil {
		return nil, fmt.Errorf("walk ledger transactions: %w", err)
	}
	sort.SliceStable(results, func(i, j int) bool {
		left, leftOK := staged.positions[results[i].TxHash]
		right, rightOK := staged.positions[results[j].TxHash]
		switch {
		case leftOK && rightOK:
			return left < right
		case leftOK:
			return true
		default:
			return false
		}
	})
	staged.results = results
	return staged, nil
}

func (s *Service) commitTransactionResultsLocked(staged *stagedTransactionResults) {
	if staged == nil {
		return
	}
	for _, result := range staged.results {
		s.txIndex[result.TxHash] = staged.ledgerSeq
	}
	for txHash, txIndex := range staged.positions {
		s.txPositionIndex[txHash] = txIndex
	}
	for _, txHash := range staged.missingPositions {
		delete(s.txPositionIndex, txHash)
	}
}

// extractAffectedAccounts returns the accounts named by the final state of each
// affected ledger node.
func extractAffectedAccounts(txWithMeta []byte) []string {
	if len(txWithMeta) == 0 {
		return nil
	}

	_, metaData, err := txcore.SplitTxWithMetaBlob(txWithMeta)
	if err != nil || len(metaData) == 0 {
		return nil
	}
	metaJSON, err := binarycodec.Decode(hex.EncodeToString(metaData))
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	add := func(account string) {
		if account != "" {
			seen[account] = struct{}{}
		}
	}

	nodes, _ := metaJSON["AffectedNodes"].([]any)
	for _, rawNode := range nodes {
		node, _ := rawNode.(map[string]any)
		var fields map[string]any
		if created, ok := node["CreatedNode"].(map[string]any); ok {
			fields, _ = created["NewFields"].(map[string]any)
		} else if modified, ok := node["ModifiedNode"].(map[string]any); ok {
			fields, _ = modified["FinalFields"].(map[string]any)
		} else if deleted, ok := node["DeletedNode"].(map[string]any); ok {
			fields, _ = deleted["FinalFields"].(map[string]any)
		}
		for name, value := range fields {
			field, fieldErr := definitions.Get().FieldInstanceByName(name)
			if fieldErr == nil && field.Type == "AccountID" {
				account, _ := value.(string)
				add(account)
				continue
			}
			switch name {
			case "LowLimit", "HighLimit", "TakerPays", "TakerGets":
				amount, ok := value.(map[string]any)
				if !ok {
					continue
				}
				if idText, _ := amount["mpt_issuance_id"].(string); idText != "" {
					if id, decodeErr := mptutil.DecodeID(idText); decodeErr == nil {
						add(state.EncodeAccountIDSafe(mptutil.Issuer(id)))
					}
					continue
				}
				issuer, _ := amount["issuer"].(string)
				if issuer == "" {
					issuer, _ = amount["Issuer"].(string)
				}
				add(issuer)
			case "MPTokenIssuanceID":
				idText, _ := value.(string)
				id, decodeErr := mptutil.DecodeID(idText)
				if decodeErr == nil {
					add(state.EncodeAccountIDSafe(mptutil.Issuer(id)))
				}
			}
		}
	}

	accounts := make([]string, 0, len(seen))
	for acc := range seen {
		accounts = append(accounts, acc)
	}
	sort.Strings(accounts)
	return accounts
}

func extractMentionedAccounts(rawSTTx []byte) []string {
	if len(rawSTTx) == 0 {
		return nil
	}
	txJSON, err := binarycodec.Decode(hex.EncodeToString(rawSTTx))
	if err != nil {
		return nil
	}

	seen := make(map[string]struct{})
	add := func(account string) {
		if account != "" {
			seen[account] = struct{}{}
		}
	}
	for name, value := range txJSON {
		field, fieldErr := definitions.Get().FieldInstanceByName(name)
		if fieldErr != nil {
			continue
		}
		switch field.Type {
		case "AccountID":
			account, _ := value.(string)
			add(account)
		case "Amount":
			amount, ok := value.(map[string]any)
			if !ok {
				continue
			}
			issuer, _ := amount["issuer"].(string)
			if issuer == "" {
				issuer, _ = amount["Issuer"].(string)
			}
			if issuer != "" {
				add(issuer)
				continue
			}
			issuanceID, _ := amount["mpt_issuance_id"].(string)
			if issuanceID == "" {
				issuanceID, _ = amount["MPTokenIssuanceID"].(string)
			}
			if id, decodeErr := mptutil.DecodeID(issuanceID); decodeErr == nil {
				add(state.EncodeAccountIDSafe(mptutil.Issuer(id)))
			}
		}
	}

	accounts := make([]string, 0, len(seen))
	for account := range seen {
		accounts = append(accounts, account)
	}
	sort.Strings(accounts)
	return accounts
}

func proposedOwnerFunds(rawSTTx []byte, view *ledger.Ledger) string {
	if len(rawSTTx) == 0 || view == nil {
		return ""
	}
	txJSON, err := binarycodec.Decode(hex.EncodeToString(rawSTTx))
	if err != nil || txJSON["TransactionType"] != "OfferCreate" {
		return ""
	}
	account, _ := txJSON["Account"].(string)
	if account == "" {
		return ""
	}
	encodedAmount, err := json.Marshal(txJSON["TakerGets"])
	if err != nil {
		return ""
	}
	amount, err := state.AmountFromJSON(encodedAmount)
	if err != nil {
		return ""
	}

	_, accountBytes, err := addresscodec.DecodeClassicAddressToAccountID(account)
	if err != nil || len(accountBytes) != 20 {
		return ""
	}
	var accountID [20]byte
	copy(accountID[:], accountBytes)

	if amount.IsMPT() {
		id, decodeErr := mptutil.DecodeID(amount.MPTIssuanceID())
		if decodeErr != nil || accountID == mptutil.Issuer(id) {
			return ""
		}
		funds, _ := mptutil.Funds(view, id, accountID, false)
		return state.NewMPTAmountWithIssuanceID(funds, "", amount.MPTIssuanceID()).Value()
	}
	if !amount.IsNative() && amount.Issuer == account {
		return ""
	}
	_, reserveBase, reserveInc := readFeesFromLedger(view)
	return txcore.AccountFunds(view, accountID, amount, false, reserveBase, reserveInc).Value()
}
