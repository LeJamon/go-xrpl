package tx

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx/invariants"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Action represents the type of modification to a ledger entry
type Action int

const (
	// ActionCache means the entry was read but not modified
	ActionCache Action = iota
	// ActionInsert means a new entry was created
	ActionInsert
	// ActionModify means an existing entry was modified
	ActionModify
	// ActionErase means an entry was deleted
	ActionErase
)

// TrackedEntry represents a ledger entry being tracked for changes
type TrackedEntry struct {
	Action   Action
	Original []byte // Original state (nil for inserts)
	Current  []byte // Current state (nil for deletes after erase)
}

// ApplyStateTable wraps a LedgerView and tracks all modifications
// for automatic metadata generation, similar to rippled's ApplyStateTable
type ApplyStateTable struct {
	base   LedgerView
	items  map[[32]byte]*TrackedEntry
	drops  drops.XRPAmount
	txHash [32]byte
	txSeq  uint32
	rules  *amendment.Rules
}

// NewApplyStateTable creates a new ApplyStateTable wrapping the given base view.
// rules controls amendment-gated behaviour (threading, metadata flags).
// If nil, defaults to all amendments enabled.
func NewApplyStateTable(base LedgerView, txHash [32]byte, txSeq uint32, rules *amendment.Rules) *ApplyStateTable {
	return &ApplyStateTable{
		base:   base,
		items:  make(map[[32]byte]*TrackedEntry),
		txHash: txHash,
		txSeq:  txSeq,
		rules:  rules,
	}
}

// Read reads a ledger entry, tracking it as cached
func (t *ApplyStateTable) Read(k keylet.Keylet) ([]byte, error) {
	// Check if already tracked
	if entry, exists := t.items[k.Key]; exists {
		if entry.Action == ActionErase {
			return nil, nil
		}
		return entry.Current, nil
	}

	// Read from base
	data, err := t.base.Read(k)
	if err != nil {
		return nil, err
	}

	// Only track entries that exist in the base
	if data != nil {
		t.items[k.Key] = &TrackedEntry{
			Action:   ActionCache,
			Original: data,
			Current:  data,
		}
	}

	return data, nil
}

// Exists checks if an entry exists
func (t *ApplyStateTable) Exists(k keylet.Keylet) (bool, error) {
	// Check if tracked
	if entry, exists := t.items[k.Key]; exists {
		return entry.Action != ActionErase, nil
	}

	// Check base
	return t.base.Exists(k)
}

// Insert adds a new entry
func (t *ApplyStateTable) Insert(k keylet.Keylet, data []byte) error {
	// Check if already exists in tracking
	if entry, exists := t.items[k.Key]; exists {
		if entry.Action != ActionErase {
			return fmt.Errorf("entry already exists")
		}
		// Re-inserting a deleted entry becomes a modify
		entry.Action = ActionModify
		entry.Current = data
		return nil
	}

	// Check base
	exists, err := t.base.Exists(k)
	if err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("entry already exists")
	}

	// Track as insert
	t.items[k.Key] = &TrackedEntry{
		Action:   ActionInsert,
		Original: nil,
		Current:  data,
	}

	return nil
}

// Update modifies an existing entry
func (t *ApplyStateTable) Update(k keylet.Keylet, data []byte) error {
	// Check if tracked
	if entry, exists := t.items[k.Key]; exists {
		if entry.Action == ActionErase {
			return fmt.Errorf("entry not found (deleted)")
		}
		if entry.Action == ActionCache {
			entry.Action = ActionModify
		}
		// For insert, keep it as insert with new data
		entry.Current = data
		return nil
	}

	// Read original from base to track it
	original, err := t.base.Read(k)
	if err != nil {
		return err
	}

	if original == nil {
		// Entry doesn't exist in base — treat as insert.
		// This happens when a prior transaction in the same ledger erased
		// the entry, then a subsequent operation re-creates it via Update().
		// Reference: rippled's ApplyView uses insert() for new entries.
		t.items[k.Key] = &TrackedEntry{
			Action:  ActionInsert,
			Current: data,
		}
	} else {
		// Track as modified
		t.items[k.Key] = &TrackedEntry{
			Action:   ActionModify,
			Original: original,
			Current:  data,
		}
	}

	return nil
}

// Erase removes an entry
func (t *ApplyStateTable) Erase(k keylet.Keylet) error {
	// Check if tracked
	if entry, exists := t.items[k.Key]; exists {
		if entry.Action == ActionErase {
			return fmt.Errorf("entry already deleted")
		}
		if entry.Action == ActionInsert {
			// Inserting then deleting = no change, remove from tracking
			delete(t.items, k.Key)
			return nil
		}
		// Cache or Modify -> Erase
		// Keep Current as the state before deletion (for metadata PreviousFields)
		entry.Action = ActionErase
		// Note: entry.Current keeps its value (state before deletion)
		return nil
	}

	// Read original from base
	original, err := t.base.Read(k)
	if err != nil {
		return err
	}

	// Track as erased - Current = Original since there were no modifications
	t.items[k.Key] = &TrackedEntry{
		Action:   ActionErase,
		Original: original,
		Current:  original, // Keep original as current (no modifications before deletion)
	}

	return nil
}

// IsErased returns true if the entry at the given key has been erased.
func (t *ApplyStateTable) IsErased(k keylet.Keylet) bool {
	if entry, exists := t.items[k.Key]; exists {
		return entry.Action == ActionErase
	}
	return false
}

// AdjustDropsDestroyed records destroyed XRP
func (t *ApplyStateTable) AdjustDropsDestroyed(drops drops.XRPAmount) {
	t.drops = t.drops.Add(drops)
}

// ForEach iterates over all state entries
func (t *ApplyStateTable) ForEach(fn func(key [32]byte, data []byte) bool) error {
	// This needs to iterate over base plus our modifications
	// For now, delegate to base (this is typically only used for debugging)
	return t.base.ForEach(fn)
}

// Succ returns the first entry with key > the given key.
// Combines local items with the base view's Succ.
// Reference: rippled ReadView::succ()
func (t *ApplyStateTable) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var bestKey [32]byte
	var bestData []byte
	found := false

	// Check local items for entries with key > the given key
	for k, entry := range t.items {
		if entry.Action == ActionErase {
			continue
		}
		if bytes.Compare(k[:], key[:]) > 0 {
			if !found || bytes.Compare(k[:], bestKey[:]) < 0 {
				bestKey = k
				bestData = entry.Current
				found = true
			}
		}
	}

	// Check base view, looping past locally-deleted entries.
	// When the base returns an entry that's been deleted in our overlay,
	// we must ask for the NEXT one (using the deleted key as new search key).
	searchBase := key
	for {
		baseKey, baseData, baseFound, err := t.base.Succ(searchBase)
		if err != nil {
			if found {
				return bestKey, bestData, true, nil
			}
			return [32]byte{}, nil, false, err
		}
		if !baseFound {
			break
		}

		if entry, exists := t.items[baseKey]; exists && entry.Action == ActionErase {
			// Base entry is deleted locally — skip it and look for the next one
			searchBase = baseKey
			continue
		}

		// If locally modified, use local data
		if entry, exists := t.items[baseKey]; exists {
			baseData = entry.Current
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

// Apply commits all changes to the base view and returns generated metadata.
// Threading is applied first (PreviousTxnID/PreviousTxnLgrSeq updates),
// then metadata is generated from the final state.
// Changes are written to the base view unless dry-run mode is set.
// Reference: rippled ApplyStateTable.cpp apply() lines 113-292
func (t *ApplyStateTable) Apply() (*Metadata, error) {
	return t.applyImpl(false)
}

// ApplyDryRun generates metadata without committing changes to the base view.
// Threading and metadata are computed identically to Apply(), but no state
// is written. Useful for transaction simulation and batch processing.
// Reference: rippled ApplyStateTable.cpp apply() isDryRun parameter
func (t *ApplyStateTable) ApplyDryRun() (*Metadata, error) {
	return t.applyImpl(true)
}

// applyImpl is the shared implementation for Apply and ApplyDryRun.
func (t *ApplyStateTable) applyImpl(isDryRun bool) (*Metadata, error) {
	// Phase 1: Apply threading to all entries
	// This updates PreviousTxnID/PreviousTxnLgrSeq on entries and their owners
	t.applyThreading()

	// Phase 2: Generate metadata and optionally apply to base
	metadata := &Metadata{
		AffectedNodes: make([]AffectedNode, 0),
	}

	for key, entry := range t.items {
		switch entry.Action {
		case ActionCache:
			// No change, skip
			continue

		case ActionInsert:
			node, err := t.buildCreatedNode(key, entry.Current)
			if err != nil {
				return nil, err
			}
			metadata.AffectedNodes = append(metadata.AffectedNodes, node)

			if !isDryRun {
				if err := t.base.Insert(keylet.Keylet{Key: key}, entry.Current); err != nil {
					return nil, err
				}
			}

		case ActionModify:
			// Skip if no actual change
			if bytesEqual(entry.Original, entry.Current) {
				continue
			}

			node, err := t.buildModifiedNode(key, entry.Original, entry.Current)
			if err != nil {
				return nil, err
			}
			metadata.AffectedNodes = append(metadata.AffectedNodes, node)

			if !isDryRun {
				if err := t.base.Update(keylet.Keylet{Key: key}, entry.Current); err != nil {
					return nil, err
				}
			}

		case ActionErase:
			node, err := t.buildDeletedNode(key, entry.Original, entry.Current)
			if err != nil {
				return nil, err
			}
			metadata.AffectedNodes = append(metadata.AffectedNodes, node)

			if !isDryRun {
				if err := t.base.Erase(keylet.Keylet{Key: key}); err != nil {
					return nil, err
				}
			}
		}
	}

	// Apply destroyed drops only when not dry-run
	if !isDryRun && t.drops.IsPositive() {
		t.base.AdjustDropsDestroyed(t.drops)
	}

	return metadata, nil
}

// effectiveRules returns the amendment rules, defaulting to all supported if nil.
func (t *ApplyStateTable) effectiveRules() *amendment.Rules {
	if t.rules != nil {
		return t.rules
	}
	return amendment.AllSupportedRules()
}

// applyThreading updates PreviousTxnID/PreviousTxnLgrSeq on affected entries
// and their owner accounts. This matches rippled's ApplyStateTable threading logic.
func (t *ApplyStateTable) applyThreading() {
	// Collect keys to process (avoid modifying map during iteration)
	type threadWork struct {
		key   [32]byte
		entry *TrackedEntry
	}
	var work []threadWork
	for key, entry := range t.items {
		if entry.Action == ActionInsert || entry.Action == ActionModify || entry.Action == ActionErase {
			work = append(work, threadWork{key, entry})
		}
	}

	// Check amendment state once for the entire threading pass.
	// Reference: rippled ApplyStateTable.cpp passes to.rules() to isThreadedType().
	fixPreviousTxnID := t.effectiveRules().Enabled(amendment.FeatureFixPreviousTxnID)
	fixCheckThreading := t.effectiveRules().Enabled(amendment.FeatureFixCheckThreading)

	for _, w := range work {
		entryType := getLedgerEntryType(w.entry.Current)
		if w.entry.Current == nil && w.entry.Original != nil {
			entryType = getLedgerEntryType(w.entry.Original)
		}

		switch w.entry.Action {
		case ActionInsert:
			// Thread the created entry itself (set PreviousTxnID/PreviousTxnLgrSeq)
			if isThreadedType(entryType, fixPreviousTxnID) {
				_, _, newData, changed := threadItem(w.entry.Current, t.txHash, t.txSeq)
				if changed {
					w.entry.Current = newData
				}
			}

			// Thread owner accounts
			t.threadOwners(w.entry.Current, entryType, fixCheckThreading)

		case ActionModify:
			// Thread the modified entry itself
			if isThreadedType(entryType, fixPreviousTxnID) {
				_, _, newData, changed := threadItem(w.entry.Current, t.txHash, t.txSeq)
				if changed {
					w.entry.Current = newData
				}
			}

		case ActionErase:
			// Thread owner accounts (the entry itself is being deleted)
			data := w.entry.Current
			if data == nil {
				data = w.entry.Original
			}
			t.threadOwners(data, entryType, fixCheckThreading)
		}
	}
}

// threadOwners updates PreviousTxnID/PreviousTxnLgrSeq on owner accounts
// of a given ledger entry. fixCheckThreading gates Check→Destination threading.
func (t *ApplyStateTable) threadOwners(data []byte, entryType string, fixCheckThreading bool) {
	if data == nil {
		return
	}

	owners := getOwnerAccounts(data, entryType, fixCheckThreading)
	for _, ownerID := range owners {
		ownerKey := keylet.Account(ownerID)

		// Check if already tracked
		if entry, exists := t.items[ownerKey.Key]; exists {
			if entry.Action == ActionErase {
				continue // Don't thread deleted accounts
			}
			// Thread the existing tracked entry
			_, _, newData, changed := threadItem(entry.Current, t.txHash, t.txSeq)
			if changed {
				entry.Current = newData
				if entry.Action == ActionCache {
					entry.Action = ActionModify
				}
			}
		} else {
			// Read from base and add to tracking
			ownerData, err := t.base.Read(ownerKey)
			if err != nil || ownerData == nil {
				continue // Owner doesn't exist, skip
			}
			_, _, newData, changed := threadItem(ownerData, t.txHash, t.txSeq)
			if changed {
				t.items[ownerKey.Key] = &TrackedEntry{
					Action:   ActionModify,
					Original: ownerData,
					Current:  newData,
				}
			}
		}
	}
}

// TxExists delegates to the base view to check if a transaction exists.
// Reference: rippled ReadView::txExists()
func (t *ApplyStateTable) TxExists(txID [32]byte) bool {
	return t.base.TxExists(txID)
}

// DropsDestroyed returns the amount of XRP destroyed
func (t *ApplyStateTable) DropsDestroyed() drops.XRPAmount {
	return t.drops
}

// CollectEntries returns all modified ledger entries (Insert/Modify/Erase) for invariant checking.
// Must be called BEFORE Apply() — entries are still in the table at this point.
func (t *ApplyStateTable) CollectEntries() []invariants.InvariantEntry {
	var entries []invariants.InvariantEntry
	for key, entry := range t.items {
		if entry.Action == ActionCache {
			continue
		}
		var before, after []byte
		switch entry.Action {
		case ActionInsert:
			before = nil
			after = entry.Current
		case ActionModify:
			before = entry.Original
			after = entry.Current
		case ActionErase:
			// Original holds the state when first read; Current holds state just before deletion.
			// Use Original as the canonical "before" state.
			before = entry.Original
			if before == nil {
				before = entry.Current // direct erase without prior read sets Current = Original
			}
			after = nil
		}
		typeData := after
		if typeData == nil {
			typeData = before
		}
		entries = append(entries, invariants.InvariantEntry{
			Key:       key,
			IsDelete:  entry.Action == ActionErase,
			Before:    before,
			After:     after,
			EntryType: getLedgerEntryType(typeData),
		})
	}
	return entries
}

// Size returns the number of entries that have been inserted, modified, or erased.
// This matches rippled's ApplyStateTable::size() which counts non-cache entries.
// Used for the oversizeMetaDataCap check (tecOVERSIZE when size > 5200).
// Reference: rippled/src/xrpld/ledger/detail/ApplyStateTable.cpp
func (t *ApplyStateTable) Size() int {
	count := 0
	for _, entry := range t.items {
		switch entry.Action {
		case ActionInsert, ActionModify, ActionErase:
			count++
		}
	}
	return count
}

// GetItems returns the tracked entries map for inspection.
// Used by the engine to collect deleted offers for tecOVERSIZE handling.
func (t *ApplyStateTable) GetItems() map[[32]byte]*TrackedEntry {
	return t.items
}

// buildCreatedNode creates metadata for a newly created entry
func (t *ApplyStateTable) buildCreatedNode(key [32]byte, data []byte) (AffectedNode, error) {
	entryType := getLedgerEntryType(data)

	node := AffectedNode{
		NodeType:        "CreatedNode",
		LedgerEntryType: entryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		NewFields:       make(map[string]any),
	}

	// Extract fields for NewFields based on entry type
	fields, err := extractLedgerFields(data, entryType)
	if err != nil {
		return node, err
	}

	// For CreatedNode, include all non-default fields with sMD_Create | sMD_Always
	for name, value := range fields {
		if shouldIncludeInCreate(name) && !state.IsDefaultValue(value) {
			node.NewFields[name] = value
		}
	}

	return node, nil
}

// buildModifiedNode creates metadata for a modified entry
func (t *ApplyStateTable) buildModifiedNode(key [32]byte, original, current []byte) (AffectedNode, error) {
	entryType := getLedgerEntryType(current)

	node := AffectedNode{
		NodeType:        "ModifiedNode",
		LedgerEntryType: entryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		FinalFields:     make(map[string]any),
		PreviousFields:  make(map[string]any),
	}

	// Extract fields from original and current
	origFields, err := extractLedgerFields(original, entryType)
	if err != nil {
		return node, err
	}

	currFields, err := extractLedgerFields(current, entryType)
	if err != nil {
		return node, err
	}

	// Extract PreviousTxnID and PreviousTxnLgrSeq from original
	if prevTxnID, ok := origFields["PreviousTxnID"]; ok {
		node.PreviousTxnID = fmt.Sprintf("%v", prevTxnID)
	}
	if prevTxnLgrSeq, ok := origFields["PreviousTxnLgrSeq"]; ok {
		if seq, ok := prevTxnLgrSeq.(uint32); ok {
			node.PreviousTxnLgrSeq = seq
		}
	}

	// PreviousFields: fields that changed (sMD_ChangeOrig)
	for name, origValue := range origFields {
		if shouldIncludeInPreviousFields(name) {
			if currValue, exists := currFields[name]; exists {
				if !fieldsEqual(origValue, currValue) {
					node.PreviousFields[name] = origValue
				}
			} else {
				// Field was removed
				node.PreviousFields[name] = origValue
			}
		}
	}

	// FinalFields: fields with sMD_Always | sMD_ChangeNew
	for name, currValue := range currFields {
		if shouldIncludeInFinalFields(name) {
			node.FinalFields[name] = currValue
		}
	}

	// Clean up empty maps
	if len(node.PreviousFields) == 0 {
		node.PreviousFields = nil
	}
	if len(node.FinalFields) == 0 {
		node.FinalFields = nil
	}

	return node, nil
}

// buildDeletedNode creates metadata for a deleted entry
// original = state when first read, current = state just before deletion
func (t *ApplyStateTable) buildDeletedNode(key [32]byte, original, current []byte) (AffectedNode, error) {
	// Use current for entry type (it's the state just before deletion)
	entryType := getLedgerEntryType(current)

	node := AffectedNode{
		NodeType:        "DeletedNode",
		LedgerEntryType: entryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		FinalFields:     make(map[string]any),
		PreviousFields:  make(map[string]any),
	}

	// Extract fields from both original and current
	origFields, err := extractLedgerFields(original, entryType)
	if err != nil {
		return node, err
	}

	currFields, err := extractLedgerFields(current, entryType)
	if err != nil {
		return node, err
	}

	// Extract PreviousTxnID and PreviousTxnLgrSeq from original
	if prevTxnID, ok := origFields["PreviousTxnID"]; ok {
		node.PreviousTxnID = fmt.Sprintf("%v", prevTxnID)
	}
	if prevTxnLgrSeq, ok := origFields["PreviousTxnLgrSeq"]; ok {
		if seq, ok := prevTxnLgrSeq.(uint32); ok {
			node.PreviousTxnLgrSeq = seq
		} else if seq, ok := prevTxnLgrSeq.(float64); ok {
			node.PreviousTxnLgrSeq = uint32(seq)
		} else if seq, ok := prevTxnLgrSeq.(int); ok {
			node.PreviousTxnLgrSeq = uint32(seq)
		}
	}

	// PreviousFields: fields that changed between original and current (sMD_ChangeOrig)
	// This captures any modifications made before deletion
	for name, origValue := range origFields {
		if shouldIncludeInPreviousFields(name) {
			if currValue, exists := currFields[name]; exists {
				if !fieldsEqual(origValue, currValue) {
					node.PreviousFields[name] = origValue
				}
			} else {
				// Field was removed before deletion
				node.PreviousFields[name] = origValue
			}
		}
	}

	// FinalFields: fields from current state with sMD_Always | sMD_DeleteFinal
	for name, value := range currFields {
		if shouldIncludeInDeleteFinal(name) {
			node.FinalFields[name] = value
		}
	}

	// Clean up empty maps
	if len(node.PreviousFields) == 0 {
		node.PreviousFields = nil
	}
	if len(node.FinalFields) == 0 {
		node.FinalFields = nil
	}

	return node, nil
}

// getLedgerEntryType extracts the ledger entry type from binary data
func getLedgerEntryType(data []byte) string {
	if len(data) < 4 {
		return "Unknown"
	}

	// Parse the binary to find LedgerEntryType field
	// LedgerEntryType is a UInt16 with type code 1 and field code 1
	// Header byte: 0x11 (type 1, field 1)
	offset := 0
	for offset < len(data)-2 {
		header := data[offset]
		offset++

		typeCode := (header >> 4) & 0x0F
		fieldCode := header & 0x0F

		if typeCode == 0 {
			if offset >= len(data) {
				break
			}
			typeCode = data[offset]
			offset++
		}

		if fieldCode == 0 {
			if offset >= len(data) {
				break
			}
			fieldCode = data[offset]
			offset++
		}

		// Check for LedgerEntryType (type 1 = UInt16, field 1)
		if typeCode == 1 && fieldCode == 1 {
			if offset+2 > len(data) {
				break
			}
			entryType := binary.BigEndian.Uint16(data[offset : offset+2])
			return ledgerEntryTypeName(entryType)
		}

		// Skip field value based on type
		switch typeCode {
		case 1: // UInt16
			offset += 2
		case 2: // UInt32
			offset += 4
		case 3: // UInt64
			offset += 8
		case 4: // Hash128
			offset += 16
		case 5: // Hash256
			offset += 32
		case 6: // Amount
			if offset < len(data) && (data[offset]&0x80) == 0 {
				offset += 8 // XRP
			} else {
				offset += 48 // IOU
			}
		case 7: // Blob (variable length)
			if offset >= len(data) {
				return "Unknown"
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return "Unknown"
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			offset += length
		case 8: // AccountID (variable-length encoded with length prefix)
			// Reference: rippled STAccount.cpp uses addVL() — 1-byte length prefix + data
			if offset >= len(data) {
				return "Unknown"
			}
			length := int(data[offset])
			offset++
			if length > 192 {
				if offset >= len(data) {
					return "Unknown"
				}
				length = 193 + ((length-193)<<8 | int(data[offset]))
				offset++
			}
			offset += length
		default:
			// Unknown type, can't continue
			return "Unknown"
		}
	}

	return "Unknown"
}

// ledgerEntryTypeName converts entry type code to name
// Based on rippled's ledger_entries.macro
func ledgerEntryTypeName(code uint16) string {
	switch code {
	// Active ledger entry types (from rippled ledger_entries.macro)
	case 0x0037:
		return "NFTokenOffer"
	case 0x0043:
		return "Check"
	case 0x0049:
		return "DID"
	case 0x004e:
		return "NegativeUNL"
	case 0x0050:
		return "NFTokenPage"
	case 0x0053:
		return "SignerList"
	case 0x0054:
		return "Ticket"
	case 0x0061:
		return "AccountRoot"
	case 0x0063:
		return "Contract" // deprecated
	case 0x0064:
		return "DirectoryNode"
	case 0x0066:
		return "Amendments"
	case 0x0068:
		return "LedgerHashes"
	case 0x0069:
		return "Bridge"
	case 0x006e:
		return "Nickname" // deprecated
	case 0x006f:
		return "Offer"
	case 0x0070:
		return "DepositPreauth"
	case 0x0071:
		return "XChainOwnedClaimID"
	case 0x0072:
		return "RippleState"
	case 0x0073:
		return "FeeSettings"
	case 0x0074:
		return "XChainOwnedCreateAccountClaimID"
	case 0x0075:
		return "Escrow"
	case 0x0078:
		return "PayChannel"
	case 0x0079:
		return "AMM"
	case 0x007e:
		return "MPTokenIssuance"
	case 0x007f:
		return "MPToken"
	case 0x0080:
		return "Oracle"
	case 0x0081:
		return "Credential"
	case 0x0082:
		return "PermissionedDomain"
	case 0x0083:
		return "Delegate"
	case 0x0084:
		return "Vault"
	default:
		return fmt.Sprintf("Unknown(0x%04x)", code)
	}
}

// bytesEqual compares two byte slices
func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fieldsEqual compares two field values
func fieldsEqual(a, b any) bool {
	// For maps (like Amount), compare recursively
	aMap, aIsMap := a.(map[string]any)
	bMap, bIsMap := b.(map[string]any)
	if aIsMap && bIsMap {
		if len(aMap) != len(bMap) {
			return false
		}
		for k, v := range aMap {
			if bv, ok := bMap[k]; !ok || !fieldsEqual(v, bv) {
				return false
			}
		}
		return true
	}

	// Direct comparison
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// Note: isDefaultValue is defined in state.go
