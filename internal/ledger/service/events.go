package service

import (
	"encoding/hex"
	"encoding/json"
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

type EventCallback func(event *LedgerAcceptedEvent)

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

type SubmittedTxCallback func(SubmittedTxEvent)

// ledgerEventBufferDepth bounds the accepted-ledger event dispatch queue. Deep
// enough to absorb a catch-up adoption burst (many ledgers per second) without
// dropping stream events; a wedged subscriber past this is shed and counted.
const ledgerEventBufferDepth = 256

func (s *Service) SetEventCallback(callback EventCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.eventCallback = callback
}

// dispatchLedgerEvent hands an accepted-ledger event to the single ordered
// dispatcher so eventCallback runs FIFO and single-threaded, instead of the
// per-event goroutines that ran it concurrently with itself — a data race on
// the subscriber's state and out-of-order ledgerClosed delivery. rippled
// serializes the equivalent through NetworkOPs' single job-queue strand.
// Enqueue is non-blocking: a lagging subscriber drops the event with a counted
// warn rather than stalling ledger close.
func (s *Service) dispatchLedgerEvent(event *LedgerAcceptedEvent) {
	if event == nil {
		return
	}
	if s.ledgerEventCh == nil {
		// Dispatcher not started (paths that emit without Start). Deliver on a
		// fresh goroutine to preserve the "callback fires, never under s.mu"
		// contract; ordering isn't guaranteed here, but Start-anchored callers
		// (production and the event tests) always take the channel path.
		go s.deliverLedgerEvent(event)
		return
	}
	select {
	case s.ledgerEventCh <- event:
	default:
		n := s.droppedLedgerEvents.Add(1)
		s.logger.Warn("ledger event subscriber lagging; dropping event", "droppedTotal", n)
	}
}

// runLedgerEventDispatcher is the single consumer that delivers accepted-ledger
// events in FIFO order. It drains the queue on Stop before exiting so a shutdown
// doesn't drop already-queued stream events.
func (s *Service) runLedgerEventDispatcher() {
	defer s.ledgerEventWG.Done()
	for {
		select {
		case ev := <-s.ledgerEventCh:
			s.deliverLedgerEvent(ev)
		case <-s.ledgerEventQuit:
			for {
				select {
				case ev := <-s.ledgerEventCh:
					s.deliverLedgerEvent(ev)
				default:
					return
				}
			}
		}
	}
}

// deliverLedgerEvent reads the current callback under a read lock (so a
// late-wired or unwired callback is respected) and invokes it outside the lock,
// preserving the contract that subscriber callbacks never run under s.mu.
func (s *Service) deliverLedgerEvent(event *LedgerAcceptedEvent) {
	s.mu.RLock()
	cb := s.eventCallback
	s.mu.RUnlock()
	if cb != nil {
		cb(event)
	}
}

// DroppedLedgerEvents returns the cumulative count of accepted-ledger events
// shed because the subscriber lagged.
func (s *Service) DroppedLedgerEvents() uint64 { return s.droppedLedgerEvents.Load() }

// SetSubmittedTxCallback registers a sink fired from SubmitTransaction after
// every apply attempt. Pass nil to unwire.
func (s *Service) SetSubmittedTxCallback(fn SubmittedTxCallback) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submittedTxCallback = fn
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

func (s *Service) validatedLedgerSeqLocked() uint32 {
	if s.validatedLedger == nil {
		return 0
	}
	return s.validatedLedger.Sequence()
}

// Caller must hold s.mu for writing and invoke notify only after unlocking.
func (s *Service) validatedLedgerNotificationLocked(previousSeq uint32) validatedLedgerNotification {
	if s.onValidatedLedger == nil || s.validatedLedger == nil || s.validatedLedger.Sequence() <= previousSeq {
		return validatedLedgerNotification{}
	}
	return validatedLedgerNotification{
		handler:    s.onValidatedLedger,
		seq:        s.validatedLedger.Sequence(),
		hash:       s.validatedLedger.Hash(),
		parentHash: s.validatedLedger.ParentHash(),
	}
}

func (s *Service) unlockAndNotifyValidatedLedger(previousSeq uint32) {
	notification := s.validatedLedgerNotificationLocked(previousSeq)
	s.mu.Unlock()
	notification.notify()
}

func (n validatedLedgerNotification) notify() {
	if n.handler != nil {
		n.handler(n.seq, n.hash, n.parentHash)
	}
}

// SetEventHooks registers structured event hooks (richer than SetEventCallback).
func (s *Service) SetEventHooks(hooks *EventHooks) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hooks = hooks
}

// EventHooks returns the current event hooks (may be nil)
func (s *Service) EventHooks() *EventHooks {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hooks
}

// fireLedgerClosedHooksLocked fires hooks.OnLedgerClosed and OnTransaction for a
// closed ledger. Each hook runs on its own goroutine so subscriber callbacks
// can't deadlock against s.mu; safe with nil hooks. Caller must hold s.mu.
func (s *Service) fireLedgerClosedHooksLocked(
	info *LedgerInfo,
	txResults []TransactionResultEvent,
	closeTime time.Time,
	validatedLedgers string,
) {
	if s.hooks == nil {
		return
	}

	if s.hooks.OnLedgerClosed != nil {
		txCount := len(txResults)
		hooks := s.hooks
		capturedInfo := *info
		capturedRange := validatedLedgers
		go hooks.OnLedgerClosed(&capturedInfo, txCount, capturedRange)
	}

	if s.hooks.OnTransaction != nil {
		hooks := s.hooks
		ledgerSeq := info.Sequence
		ledgerHash := info.Hash
		closeTimeVal := closeTime
		for _, txResult := range txResults {
			txInfo := TransactionInfo{
				Hash:             txResult.TxHash,
				TxBlob:           txResult.TxData,
				AffectedAccounts: txResult.AffectedAccounts,
			}
			txIndex, ok := s.txPositionIndex[txResult.TxHash]
			if !ok {
				txIndex = invalidTransactionIndex
			}
			result := TxResult{
				Applied:  txResult.Validated,
				Metadata: txResult.MetaData,
				TxIndex:  txIndex,
			}
			go hooks.OnTransaction(txInfo, result, ledgerSeq, ledgerHash, closeTimeVal)
		}
	}
}

type transactionResultSource interface {
	IsValidated() bool
	ForEachTransaction(func(txHash [32]byte, txData []byte) bool) error
}

// collectTransactionResults gathers per-tx results from the closed ledger and
// populates s.txIndex/s.txPositionIndex (hash -> seq, metadata index). Idempotent with
// the Apply-time write; the sole index site for the Apply-less peer-adopt path.
func (s *Service) collectTransactionResults(l transactionResultSource, ledgerSeq uint32, ledgerHash [32]byte) []TransactionResultEvent {
	var results []TransactionResultEvent
	validated := l.IsValidated()

	l.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		result := TransactionResultEvent{
			TxHash:      txHash,
			TxData:      txData,
			Validated:   validated,
			LedgerIndex: ledgerSeq,
			LedgerHash:  ledgerHash,
		}
		result.AffectedAccounts = extractAffectedAccounts(txData)

		s.txIndex[txHash] = ledgerSeq
		if txIndex, ok := txcore.TransactionIndexFromTxWithMetaBlob(txData); ok {
			s.txPositionIndex[txHash] = txIndex
		} else {
			delete(s.txPositionIndex, txHash)
		}

		results = append(results, result)
		return true
	})

	return results
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
