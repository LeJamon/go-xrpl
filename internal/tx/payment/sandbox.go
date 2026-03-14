package payment

import (
	"bytes"

	"github.com/LeJamon/goXRPLd/drops"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// PaymentSandbox provides isolated, reversible state changes for payment processing.
// It wraps a LedgerView and tracks modifications without immediately applying them.
// Credits made during payment execution are tracked via DeferredCredits to prevent
// liquidity double-counting across different payment paths.
//
// Based on rippled's PaymentSandbox implementation.
type PaymentSandbox struct {
	// parent is the parent PaymentSandbox (nil for root sandbox)
	parent *PaymentSandbox

	// view is the underlying ledger view (used only for root sandbox)
	view tx.LedgerView

	// modifications holds modified ledger entries (key -> new data)
	modifications map[[32]byte][]byte

	// preImages holds original ledger entries before modification (for metadata)
	preImages map[[32]byte][]byte

	// insertions holds newly created ledger entries
	insertions map[[32]byte][]byte

	// deletions holds deleted ledger entry keys
	deletions map[[32]byte]bool

	// deletedFinalStates holds the final state of modified entries before deletion
	// This is needed for correct metadata generation (PreviousFields/FinalFields in DeletedNode)
	deletedFinalStates map[[32]byte][]byte

	// tab holds deferred credits for balance adjustments
	tab *DeferredCredits

	// dropsDestroyed tracks XRP destroyed during this sandbox's operations
	dropsDestroyed drops.XRPAmount

	// txHash is the current transaction hash (for PreviousTxnID updates)
	txHash [32]byte

	// ledgerSeq is the current ledger sequence (for PreviousTxnLgrSeq updates)
	ledgerSeq uint32
}

// DeferredCredits tracks credits that shouldn't be spendable mid-transaction.
// This prevents consuming liquidity from one path from affecting other paths.
type DeferredCredits struct {
	// credits maps (lowAccount, highAccount, currency) -> adjustment
	credits map[deferredKey]*deferredValue

	// ownerCounts tracks maximum owner count seen for each account
	ownerCounts map[[20]byte]uint32
}

// deferredKey is the key for deferred credits lookup
// Accounts are stored in canonical order (low < high)
type deferredKey struct {
	low      [20]byte
	high     [20]byte
	currency string
}

// deferredValue holds the accumulated credits and original balance
type deferredValue struct {
	lowAcctCredits     tx.Amount
	highAcctCredits    tx.Amount
	lowAcctOrigBalance tx.Amount
}

// Adjustment represents the deferred credit adjustments for a balance lookup
type Adjustment struct {
	Debits      tx.Amount
	Credits     tx.Amount
	OrigBalance tx.Amount
}

// NewPaymentSandbox creates a new root PaymentSandbox wrapping the given LedgerView
func NewPaymentSandbox(view tx.LedgerView) *PaymentSandbox {
	return &PaymentSandbox{
		parent:             nil,
		view:               view,
		modifications:      make(map[[32]byte][]byte),
		preImages:          make(map[[32]byte][]byte),
		insertions:         make(map[[32]byte][]byte),
		deletions:          make(map[[32]byte]bool),
		deletedFinalStates: make(map[[32]byte][]byte),
		tab:                newDeferredCredits(),
	}
}

// DebugCounts returns the number of modifications, insertions, and deletions in this sandbox
func (s *PaymentSandbox) DebugCounts() (int, int, int) {
	return len(s.modifications), len(s.insertions), len(s.deletions)
}

// SetTransactionContext sets the current transaction hash and ledger sequence
// for PreviousTxnID/PreviousTxnLgrSeq updates when modifying ledger objects.
func (s *PaymentSandbox) SetTransactionContext(txHash [32]byte, ledgerSeq uint32) {
	s.txHash = txHash
	s.ledgerSeq = ledgerSeq
}

// GetTransactionContext returns the current transaction hash and ledger sequence
func (s *PaymentSandbox) GetTransactionContext() ([32]byte, uint32) {
	// Walk up the chain to find the root sandbox with the context
	if s.txHash != [32]byte{} || s.ledgerSeq != 0 {
		return s.txHash, s.ledgerSeq
	}
	if s.parent != nil {
		return s.parent.GetTransactionContext()
	}
	return s.txHash, s.ledgerSeq
}

// NewChildSandbox creates a child PaymentSandbox on top of a parent.
// Changes are pushed to the parent when Apply() is called.
func NewChildSandbox(parent *PaymentSandbox) *PaymentSandbox {
	txHash, ledgerSeq := parent.GetTransactionContext()
	return &PaymentSandbox{
		parent:             parent,
		txHash:             txHash,
		ledgerSeq:          ledgerSeq,
		view:               nil, // Use parent's view
		modifications:      make(map[[32]byte][]byte),
		preImages:          make(map[[32]byte][]byte),
		insertions:         make(map[[32]byte][]byte),
		deletions:          make(map[[32]byte]bool),
		deletedFinalStates: make(map[[32]byte][]byte),
		tab:                newDeferredCredits(),
	}
}

// newDeferredCredits creates a new DeferredCredits instance
func newDeferredCredits() *DeferredCredits {
	return &DeferredCredits{
		credits:     make(map[deferredKey]*deferredValue),
		ownerCounts: make(map[[20]byte]uint32),
	}
}

// makeKey creates a canonical key for deferred credits lookup.
// Accounts are ordered so that low < high.
func makeKey(a1, a2 [20]byte, currency string) deferredKey {
	if bytes.Compare(a1[:], a2[:]) < 0 {
		return deferredKey{low: a1, high: a2, currency: currency}
	}
	return deferredKey{low: a2, high: a1, currency: currency}
}

// Read reads a ledger entry from the sandbox or underlying view
func (s *PaymentSandbox) Read(k keylet.Keylet) ([]byte, error) {
	key := k.Key

	// Check deletions first
	if s.deletions[key] {
		return nil, nil
	}

	// Check local modifications
	if data, ok := s.modifications[key]; ok {
		return data, nil
	}

	// Check local insertions
	if data, ok := s.insertions[key]; ok {
		return data, nil
	}

	// Check parent sandbox
	if s.parent != nil {
		return s.parent.Read(k)
	}

	// Read from underlying view
	if s.view != nil {
		return s.view.Read(k)
	}

	return nil, nil
}

// Exists checks if a ledger entry exists in the sandbox or underlying view
func (s *PaymentSandbox) Exists(k keylet.Keylet) (bool, error) {
	key := k.Key

	// Check deletions first
	if s.deletions[key] {
		return false, nil
	}

	// Check local modifications or insertions
	if _, ok := s.modifications[key]; ok {
		return true, nil
	}
	if _, ok := s.insertions[key]; ok {
		return true, nil
	}

	// Check parent sandbox
	if s.parent != nil {
		return s.parent.Exists(k)
	}

	// Check underlying view
	if s.view != nil {
		return s.view.Exists(k)
	}

	return false, nil
}

// Insert adds a new ledger entry to the sandbox
func (s *PaymentSandbox) Insert(k keylet.Keylet, data []byte) error {
	key := k.Key
	// Copy data to avoid external mutations
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	// If re-inserting a previously deleted entry, check whether it existed
	// in the base view. If so, this is effectively a modification (the base
	// view still has the old entry), not a fresh insertion. Without this,
	// ApplyToView would attempt Insert on the base view and fail with
	// "entry already exists".
	// Reference: rippled OpenView::insert → if erased && existed in parent → modify
	if _, wasDeleted := s.deletions[key]; wasDeleted {
		delete(s.deletions, key)
		// Check if the entry existed in the base view before this sandbox
		origData, _ := s.readOriginal(k)
		if origData != nil {
			// Entry exists in base → treat as modification
			s.modifications[key] = dataCopy
			return nil
		}
		// Entry was inserted+deleted within this sandbox → re-insert
		s.insertions[key] = dataCopy
		return nil
	}

	s.insertions[key] = dataCopy
	return nil
}

// Update modifies an existing ledger entry in the sandbox
func (s *PaymentSandbox) Update(k keylet.Keylet, data []byte) error {
	key := k.Key
	// Copy data to avoid external mutations
	dataCopy := make([]byte, len(data))
	copy(dataCopy, data)

	// If it was an insertion, update the insertion
	if _, ok := s.insertions[key]; ok {
		s.insertions[key] = dataCopy
		return nil
	}

	// Capture pre-image if we haven't already
	if _, hasPreImage := s.preImages[key]; !hasPreImage {
		// Get the original value from parent chain or underlying view
		origData, err := s.readOriginal(k)
		if err == nil && origData != nil {
			preImageCopy := make([]byte, len(origData))
			copy(preImageCopy, origData)
			s.preImages[key] = preImageCopy
		}
	}

	s.modifications[key] = dataCopy
	return nil
}

// readOriginal reads the original value from parent chain or underlying view,
// bypassing any local modifications in this sandbox.
func (s *PaymentSandbox) readOriginal(k keylet.Keylet) ([]byte, error) {
	// If we have a parent, ask the parent for the original
	if s.parent != nil {
		return s.parent.Read(k)
	}
	// Otherwise read from the underlying view
	if s.view != nil {
		return s.view.Read(k)
	}
	return nil, nil
}

// Erase marks a ledger entry for deletion
func (s *PaymentSandbox) Erase(k keylet.Keylet) error {
	key := k.Key
	// If this entry was modified, save the final state before deletion
	// This is needed for correct metadata generation (PreviousFields vs FinalFields)
	if modData, ok := s.modifications[key]; ok {
		// Save the modified data as the final state before deletion
		finalCopy := make([]byte, len(modData))
		copy(finalCopy, modData)
		if s.deletedFinalStates == nil {
			s.deletedFinalStates = make(map[[32]byte][]byte)
		}
		s.deletedFinalStates[key] = finalCopy
	}
	delete(s.modifications, key)
	delete(s.insertions, key)
	s.deletions[key] = true
	return nil
}

// getDeletedFinalState retrieves the final state of a deleted entry
func (s *PaymentSandbox) getDeletedFinalState(key [32]byte) []byte {
	if s.deletedFinalStates == nil {
		return nil
	}
	return s.deletedFinalStates[key]
}

// AdjustDropsDestroyed records XRP that has been destroyed
func (s *PaymentSandbox) AdjustDropsDestroyed(drops drops.XRPAmount) {
	s.dropsDestroyed = s.dropsDestroyed.Add(drops)
}

// TxExists delegates to the parent sandbox or underlying view.
// Reference: rippled ReadView::txExists()
func (s *PaymentSandbox) TxExists(txID [32]byte) bool {
	if s.parent != nil {
		return s.parent.TxExists(txID)
	}
	if s.view != nil {
		return s.view.TxExists(txID)
	}
	return false
}

// ForEach iterates over all state entries visible from this sandbox
func (s *PaymentSandbox) ForEach(fn func(key [32]byte, data []byte) bool) error {
	// Track visited keys to avoid duplicates
	visited := make(map[[32]byte]bool)

	// Visit local insertions
	for key, data := range s.insertions {
		if s.deletions[key] {
			continue
		}
		visited[key] = true
		if !fn(key, data) {
			return nil
		}
	}

	// Visit local modifications
	for key, data := range s.modifications {
		if s.deletions[key] || visited[key] {
			continue
		}
		visited[key] = true
		if !fn(key, data) {
			return nil
		}
	}

	// Visit parent or underlying view
	if s.parent != nil {
		return s.parent.ForEach(func(key [32]byte, data []byte) bool {
			if s.deletions[key] || visited[key] {
				return true // Skip but continue iteration
			}
			return fn(key, data)
		})
	}

	if s.view != nil {
		return s.view.ForEach(func(key [32]byte, data []byte) bool {
			if s.deletions[key] || visited[key] {
				return true // Skip but continue iteration
			}
			return fn(key, data)
		})
	}

	return nil
}

// Succ returns the first entry with key > the given key.
// Combines local entries with parent/view results.
// Reference: rippled ReadView::succ()
func (s *PaymentSandbox) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var bestKey [32]byte
	var bestData []byte
	found := false

	// Check local insertions
	for k, data := range s.insertions {
		if s.deletions[k] {
			continue
		}
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], bestKey[:]) < 0 {
				bestKey = k
				bestData = data
				found = true
			}
		}
	}

	// Check local modifications
	for k, data := range s.modifications {
		if s.deletions[k] {
			continue
		}
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], bestKey[:]) < 0 {
				bestKey = k
				bestData = data
				found = true
			}
		}
	}

	// Delegate to parent or view, looping past deleted entries.
	// When the base returns an entry that's been locally deleted,
	// we must ask for the NEXT one (using the deleted key as new search key).
	searchBase := key
	for {
		var baseKey [32]byte
		var baseData []byte
		var baseFound bool
		var err error

		if s.parent != nil {
			baseKey, baseData, baseFound, err = s.parent.Succ(searchBase)
		} else if s.view != nil {
			baseKey, baseData, baseFound, err = s.view.Succ(searchBase)
		}
		if err != nil {
			if found {
				return bestKey, bestData, true, nil
			}
			return [32]byte{}, nil, false, err
		}

		if !baseFound {
			break
		}

		if s.deletions[baseKey] {
			// This entry was deleted locally — skip it and look for the next one
			searchBase = baseKey
			continue
		}

		// Check if local data overrides base data (modification)
		if modData, ok := s.modifications[baseKey]; ok {
			baseData = modData
		}

		if !found || bytes.Compare(baseKey[:], bestKey[:]) < 0 {
			bestKey = baseKey
			bestData = baseData
			found = true
		}
		break
	}

	return bestKey, bestData, found, nil
}

// Credit records a credit from sender to receiver.
// This is called when currency moves between accounts during payment execution.
func (s *PaymentSandbox) Credit(sender, receiver [20]byte, amount tx.Amount, preCreditSenderBalance tx.Amount) {
	s.tab.credit(sender, receiver, amount, preCreditSenderBalance)
}

// CreditHook records a credit from sender to receiver.
// This is the hook interface used by View implementations to track credits.
// Reference: rippled PaymentSandbox::creditHook in PaymentSandbox.cpp
func (s *PaymentSandbox) CreditHook(sender, receiver [20]byte, amount tx.Amount, preCreditSenderBalance tx.Amount) {
	s.tab.credit(sender, receiver, amount, preCreditSenderBalance)
}

// credit records a credit in the deferred credits table
func (dc *DeferredCredits) credit(sender, receiver [20]byte, amount tx.Amount, preCreditSenderBalance tx.Amount) {
	if sender == receiver {
		return // No self-credits
	}

	// Amount should be non-negative
	if amount.IsNegative() {
		return
	}

	key := makeKey(sender, receiver, amount.Currency)
	v, exists := dc.credits[key]

	if !exists {
		zeroAmt := tx.NewIssuedAmount(0, -100, amount.Currency, amount.Issuer)
		v = &deferredValue{
			lowAcctCredits:     zeroAmt,
			highAcctCredits:    zeroAmt,
			lowAcctOrigBalance: zeroAmt,
		}

		if bytes.Compare(sender[:], receiver[:]) < 0 {
			// sender is low account
			v.highAcctCredits = amount
			v.lowAcctOrigBalance = preCreditSenderBalance
		} else {
			// sender is high account
			v.lowAcctCredits = amount
			v.lowAcctOrigBalance = preCreditSenderBalance.Negate()
		}

		dc.credits[key] = v
	} else {
		// Only record the balance the first time
		if bytes.Compare(sender[:], receiver[:]) < 0 {
			v.highAcctCredits, _ = v.highAcctCredits.Add(amount)
		} else {
			v.lowAcctCredits, _ = v.lowAcctCredits.Add(amount)
		}
	}
}

// adjustments returns the deferred credit adjustments for a balance lookup
func (dc *DeferredCredits) adjustments(main, other [20]byte, currency string) *Adjustment {
	key := makeKey(main, other, currency)
	v, exists := dc.credits[key]
	if !exists {
		return nil
	}

	if bytes.Compare(main[:], other[:]) < 0 {
		// main is low account
		return &Adjustment{
			Debits:      v.highAcctCredits,
			Credits:     v.lowAcctCredits,
			OrigBalance: v.lowAcctOrigBalance,
		}
	}

	// main is high account
	return &Adjustment{
		Debits:      v.lowAcctCredits,
		Credits:     v.highAcctCredits,
		OrigBalance: v.lowAcctOrigBalance.Negate(),
	}
}

// ownerCount records the owner count for an account
func (dc *DeferredCredits) ownerCount(id [20]byte, cur, next uint32) {
	v := cur
	if next > v {
		v = next
	}

	existing, exists := dc.ownerCounts[id]
	if !exists {
		dc.ownerCounts[id] = v
	} else if v > existing {
		dc.ownerCounts[id] = v
	}
}

// getOwnerCount returns the recorded owner count for an account
func (dc *DeferredCredits) getOwnerCount(id [20]byte) (uint32, bool) {
	v, ok := dc.ownerCounts[id]
	return v, ok
}

// apply merges deferred credits into another DeferredCredits
func (dc *DeferredCredits) apply(to *DeferredCredits) {
	for key, fromVal := range dc.credits {
		toVal, exists := to.credits[key]
		if !exists {
			// Copy the entire value
			to.credits[key] = &deferredValue{
				lowAcctCredits:     fromVal.lowAcctCredits,
				highAcctCredits:    fromVal.highAcctCredits,
				lowAcctOrigBalance: fromVal.lowAcctOrigBalance,
			}
		} else {
			// Accumulate credits, don't update origBalance
			toVal.lowAcctCredits, _ = toVal.lowAcctCredits.Add(fromVal.lowAcctCredits)
			toVal.highAcctCredits, _ = toVal.highAcctCredits.Add(fromVal.highAcctCredits)
		}
	}

	for id, fromCount := range dc.ownerCounts {
		toCount, exists := to.ownerCounts[id]
		if !exists {
			to.ownerCounts[id] = fromCount
		} else if fromCount > toCount {
			to.ownerCounts[id] = fromCount
		}
	}
}

// BalanceHook adjusts a reported balance by subtracting deferred credits.
// This prevents liquidity double-counting across payment paths.
func (s *PaymentSandbox) BalanceHook(account, issuer [20]byte, amount tx.Amount) tx.Amount {
	currency := amount.Currency

	// Initialize with zeroed amount for delta tracking
	delta := tx.NewIssuedAmount(0, -100, currency, amount.Issuer)
	lastBal := amount
	minBal := amount

	// Walk up the sandbox chain
	for curSB := s; curSB != nil; curSB = curSB.parent {
		if adj := curSB.tab.adjustments(account, issuer, currency); adj != nil {
			delta, _ = delta.Add(adj.Debits)
			lastBal = adj.OrigBalance
			if lastBal.Compare(minBal) < 0 {
				minBal = lastBal
			}
		}
	}

	// Compute adjusted amount: min(amount, lastBal - delta, minBal)
	adjustedFromOrig, _ := lastBal.Sub(delta)

	result := amount
	if adjustedFromOrig.Compare(result) < 0 {
		result = adjustedFromOrig
	}
	if minBal.Compare(result) < 0 {
		result = minBal
	}

	// A negative XRP balance is not an error - just return zero
	if issuer == [20]byte{} && result.IsNegative() {
		return tx.NewIssuedAmount(0, -100, currency, amount.Issuer)
	}

	return result
}

// OwnerCountHook returns the maximum owner count seen for an account
// during payment processing.
func (s *PaymentSandbox) OwnerCountHook(account [20]byte, count uint32) uint32 {
	result := count
	for curSB := s; curSB != nil; curSB = curSB.parent {
		if adj, ok := curSB.tab.getOwnerCount(account); ok {
			if adj > result {
				result = adj
			}
		}
	}
	return result
}

// AdjustOwnerCount records an owner count change for an account
func (s *PaymentSandbox) AdjustOwnerCount(account [20]byte, cur, next uint32) {
	s.tab.ownerCount(account, cur, next)
}

// Apply merges this sandbox's changes into a parent PaymentSandbox
func (s *PaymentSandbox) Apply(to *PaymentSandbox) {
	if s.parent != to {
		panic("PaymentSandbox.Apply: parent mismatch")
	}

	// Apply ledger item changes
	for key := range s.deletions {
		// Before marking as deleted in parent, save the final state if we have one
		// or if parent has a modification that should become the final state
		if finalState, ok := s.deletedFinalStates[key]; ok {
			// Child has a final state for this deletion
			if to.deletedFinalStates == nil {
				to.deletedFinalStates = make(map[[32]byte][]byte)
			}
			finalCopy := make([]byte, len(finalState))
			copy(finalCopy, finalState)
			to.deletedFinalStates[key] = finalCopy
		} else if modData, ok := to.modifications[key]; ok {
			// Parent has a modification that will be deleted - save it as final state
			if to.deletedFinalStates == nil {
				to.deletedFinalStates = make(map[[32]byte][]byte)
			}
			finalCopy := make([]byte, len(modData))
			copy(finalCopy, modData)
			to.deletedFinalStates[key] = finalCopy
		}
		to.deletions[key] = true
		delete(to.modifications, key)
		delete(to.insertions, key)
	}

	for key, data := range s.insertions {
		if to.deletions[key] {
			delete(to.deletions, key)
		}
		// If parent has this as a modification, keep it as modification
		if _, ok := to.modifications[key]; ok {
			to.modifications[key] = data
		} else {
			to.insertions[key] = data
		}
	}

	for key, data := range s.modifications {
		if to.deletions[key] {
			continue
		}
		to.modifications[key] = data
	}

	// Propagate preImages (only if parent doesn't already have one)
	for key, data := range s.preImages {
		if _, hasPreImage := to.preImages[key]; !hasPreImage {
			preImageCopy := make([]byte, len(data))
			copy(preImageCopy, data)
			to.preImages[key] = preImageCopy
		}
	}

	// Propagate deletedFinalStates (only if parent doesn't already have one)
	for key, data := range s.deletedFinalStates {
		if _, hasFinalState := to.deletedFinalStates[key]; !hasFinalState {
			if to.deletedFinalStates == nil {
				to.deletedFinalStates = make(map[[32]byte][]byte)
			}
			finalCopy := make([]byte, len(data))
			copy(finalCopy, data)
			to.deletedFinalStates[key] = finalCopy
		}
	}

	// Apply deferred credits
	s.tab.apply(to.tab)

	// Apply drops destroyed
	to.dropsDestroyed = to.dropsDestroyed.Add(s.dropsDestroyed)
}

// ApplyToView merges this sandbox's changes into an underlying LedgerView.
// This should only be called on a root sandbox (parent == nil).
func (s *PaymentSandbox) ApplyToView(view tx.LedgerView) error {
	if s.parent != nil {
		panic("PaymentSandbox.ApplyToView: not a root sandbox")
	}

	// Apply deletions - but first apply any final state modifications
	for key := range s.deletions {
		if finalState := s.getDeletedFinalState(key); finalState != nil {
			if err := view.Update(keylet.Keylet{Key: key}, finalState); err != nil {
				_ = err
			}
		}
		if err := view.Erase(keylet.Keylet{Key: key}); err != nil {
			return err
		}
	}

	// Apply insertions
	for key, data := range s.insertions {
		if err := view.Insert(keylet.Keylet{Key: key}, data); err != nil {
			return err
		}
	}

	// Apply modifications
	for key, data := range s.modifications {
		if err := view.Update(keylet.Keylet{Key: key}, data); err != nil {
			return err
		}
	}

	// Apply drops destroyed
	if s.dropsDestroyed.Drops() != 0 {
		view.AdjustDropsDestroyed(s.dropsDestroyed)
	}

	return nil
}

// Reset clears all changes from this sandbox
func (s *PaymentSandbox) Reset() {
	s.modifications = make(map[[32]byte][]byte)
	s.preImages = make(map[[32]byte][]byte)
	s.insertions = make(map[[32]byte][]byte)
	s.deletions = make(map[[32]byte]bool)
	s.deletedFinalStates = make(map[[32]byte][]byte)
	s.tab = newDeferredCredits()
	s.dropsDestroyed = drops.XRPAmount(0)
}

// GetView returns the underlying LedgerView for this sandbox chain
func (s *PaymentSandbox) GetView() tx.LedgerView {
	if s.view != nil {
		return s.view
	}
	if s.parent != nil {
		return s.parent.GetView()
	}
	return nil
}

// GetModifications returns the modifications map for debugging
func (s *PaymentSandbox) GetModifications() map[[32]byte][]byte {
	return s.modifications
}

// GetInsertions returns the insertions map for debugging
func (s *PaymentSandbox) GetInsertions() map[[32]byte][]byte {
	return s.insertions
}

// GetDeletions returns the deletions map for debugging
func (s *PaymentSandbox) GetDeletions() map[[32]byte]bool {
	return s.deletions
}

// ZeroIOUAmount creates a zero IOU Amount with the given currency and issuer
func ZeroIOUAmount(currency, issuer string) tx.Amount {
	return tx.NewIssuedAmount(0, -100, currency, issuer)
}
