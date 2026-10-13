package applystate

import (
	"bytes"
	"context"
	"fmt"

	"github.com/LeJamon/go-xrpl/amendment"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/invariants"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

var backgroundContext = context.Background()

// Read reads a ledger entry, tracking it as cached
func (t *ApplyStateTable) Read(k keylet.Keylet) ([]byte, error) {
	record, found, err := t.journal.get(backgroundContext, k.Key)
	if err != nil {
		return nil, t.record(err)
	}
	if found && record.item != nil {
		item := record.item
		if item.Action == ActionErase || !state.MatchesKeyletType(k, item.Current) {
			return nil, nil
		}
		return item.Current, nil
	}
	data, err := t.base.Read(k)
	if err != nil {
		return nil, t.record(err)
	}
	if data != nil && !state.MatchesKeyletType(k, data) {
		return nil, nil
	}
	if data != nil {
		item := &TrackedEntry{Action: ActionCache, Original: bytes.Clone(data), Current: bytes.Clone(data)}
		if err := t.journal.putItem(backgroundContext, k.Key, item); err != nil {
			return nil, t.record(err)
		}
	}
	return data, nil
}

// Exists checks if an entry exists
func (t *ApplyStateTable) Exists(k keylet.Keylet) (bool, error) {
	record, found, err := t.journal.get(backgroundContext, k.Key)
	if err != nil {
		return false, t.record(err)
	}
	if found && record.item != nil {
		return record.item.Action != ActionErase && state.MatchesKeyletType(k, record.item.Current), nil
	}
	exists, err := t.base.Exists(k)
	return exists, t.record(err)
}

// Insert adds a new entry
func (t *ApplyStateTable) Insert(k keylet.Keylet, data []byte) error {
	record, found, err := t.journal.get(backgroundContext, k.Key)
	if err != nil {
		return t.record(err)
	}
	if found && record.item != nil {
		item := record.item
		if item.Action != ActionErase {
			return fmt.Errorf("entry already exists")
		}
		item.Action = ActionModify
		item.Current = bytes.Clone(data)
		item.reinserted = true
		return t.record(t.journal.putItem(backgroundContext, k.Key, item))
	}
	exists, err := t.base.Exists(k)
	if err != nil {
		return t.record(err)
	}
	if exists {
		return fmt.Errorf("entry already exists")
	}
	return t.record(t.journal.putItem(backgroundContext, k.Key, &TrackedEntry{
		Action:  ActionInsert,
		Current: bytes.Clone(data),
	}))
}

// Update modifies an existing entry
func (t *ApplyStateTable) Update(k keylet.Keylet, data []byte) error {
	record, found, err := t.journal.get(backgroundContext, k.Key)
	if err != nil {
		return t.record(err)
	}
	if found && record.item != nil {
		item := record.item
		if item.Action == ActionErase {
			return fmt.Errorf("entry not found (deleted)")
		}
		if item.Action == ActionCache {
			item.Action = ActionModify
		}
		item.Current = bytes.Clone(data)
		return t.record(t.journal.putItem(backgroundContext, k.Key, item))
	}
	original, err := t.base.Read(k)
	if err != nil {
		return t.record(err)
	}
	item := &TrackedEntry{Action: ActionModify, Original: bytes.Clone(original), Current: bytes.Clone(data)}
	if original == nil {
		item.Action = ActionInsert
		item.Original = nil
	}
	return t.record(t.journal.putItem(backgroundContext, k.Key, item))
}

// Erase removes an entry
func (t *ApplyStateTable) Erase(k keylet.Keylet) error {
	record, found, err := t.journal.get(backgroundContext, k.Key)
	if err != nil {
		return t.record(err)
	}
	if found && record.item != nil {
		item := record.item
		if item.Action == ActionErase {
			return fmt.Errorf("entry already deleted")
		}
		if item.Action == ActionInsert {
			return t.record(t.journal.deleteItem(backgroundContext, k.Key))
		}
		item.Action = ActionErase
		return t.record(t.journal.putItem(backgroundContext, k.Key, item))
	}
	original, err := t.base.Read(k)
	if err != nil {
		return t.record(err)
	}
	return t.record(t.journal.putItem(backgroundContext, k.Key, &TrackedEntry{
		Action:   ActionErase,
		Original: bytes.Clone(original),
		Current:  bytes.Clone(original),
	}))
}

// IsErased returns true if the entry at the given key has been erased.
func (t *ApplyStateTable) IsErased(k keylet.Keylet) bool {
	key := k.Key
	record, found, err := t.journal.get(backgroundContext, key)
	if err != nil {
		t.record(err)
		return false
	}
	return found && record.item != nil && record.item.Action == ActionErase
}

// ForEach iterates over all state entries, reflecting local modifications.
// Locally inserted and modified entries use their current data.
// Locally erased entries are skipped.
// Base entries not touched locally are passed through unchanged.
func (t *ApplyStateTable) ForEach(fn func([32]byte, []byte) bool) error {
	if fn == nil {
		return nil
	}
	stopped := false
	if err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		if record.item == nil || record.item.Action == ActionErase {
			return true
		}
		if !fn(key, record.item.Current) {
			stopped = true
			return false
		}
		return true
	}); err != nil {
		return t.record(err)
	}
	if stopped {
		return nil
	}
	var callbackErr error
	err := t.base.ForEach(func(key [32]byte, data []byte) bool {
		record, found, err := t.journal.get(backgroundContext, key)
		if err != nil {
			callbackErr = err
			return false
		}
		if found && record.item != nil {
			return true
		}
		return fn(key, data)
	})
	if callbackErr != nil {
		return t.record(callbackErr)
	}
	return t.record(err)
}

// Succ returns the first entry with key > the given key.
// Combines local items with the base view's Succ.
// Reference: rippled ReadView::succ()
func (t *ApplyStateTable) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var localKey [32]byte
	var localData []byte
	localFound := false
	var iterErr error
	after := key
	iterErr = t.journal.iterateAfter(backgroundContext, &after, func(candidate [32]byte, record journalRecord) bool {
		if record.item == nil {
			return true
		}
		item := record.item
		if item.Action != ActionInsert && !(item.Action == ActionModify && item.Original == nil) {
			return true
		}
		localKey, localData, localFound = candidate, item.Current, true
		return false
	})
	if iterErr != nil {
		return [32]byte{}, nil, false, t.record(iterErr)
	}
	searchBase := key
	for {
		baseKey, baseData, found, err := t.base.Succ(searchBase)
		if err != nil {
			return [32]byte{}, nil, false, t.record(err)
		}
		if !found {
			break
		}
		record, tracked, err := t.journal.get(backgroundContext, baseKey)
		if err != nil {
			return [32]byte{}, nil, false, t.record(err)
		}
		if tracked && record.item != nil {
			if record.item.Action == ActionErase {
				searchBase = baseKey
				continue
			}
			baseData = record.item.Current
		}
		if !localFound || bytes.Compare(baseKey[:], localKey[:]) < 0 {
			return baseKey, baseData, true, nil
		}
		break
	}
	return localKey, localData, localFound, nil
}

// ForEachTracked streams tracked items in key order. Callers retaining bytes
// beyond the callback must clone them.
func (t *ApplyStateTable) ForEachTracked(fn func([32]byte, *TrackedEntry) bool) error {
	if fn == nil {
		return nil
	}
	return t.record(t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		return record.item == nil || fn(key, record.item)
	}))
}

func (t *ApplyStateTable) applyThreading() error {
	fixPreviousTxnID := t.effectiveRules().Enabled(amendment.FeatureFixPreviousTxnID)
	var callbackErr error
	err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		if record.item == nil || callbackErr != nil {
			return callbackErr == nil
		}
		item := record.item
		if item.Action != ActionInsert && item.Action != ActionModify && item.Action != ActionErase {
			return true
		}
		entryType, decodeErr := state.DecodeType(item.Current)
		if item.Current == nil && item.Original != nil {
			entryType, decodeErr = state.DecodeType(item.Original)
		}
		if decodeErr != nil {
			return true
		}
		switch item.Action {
		case ActionInsert:
			if isThreadedType(entryType, fixPreviousTxnID) {
				_, _, updated, changed := threadItem(item.Current, t.txHash, t.txSeq)
				if changed {
					item.Current = updated
					if callbackErr = t.journal.putItem(backgroundContext, key, item); callbackErr != nil {
						return false
					}
				}
			}
			callbackErr = t.threadOwners(key, item.Current, entryType)
		case ActionModify:
			if bytes.Equal(item.Original, item.Current) {
				return true
			}
			if isThreadedType(entryType, fixPreviousTxnID) {
				oldPrev, oldPrevSeq, updated, changed := threadItem(item.Current, t.txHash, t.txSeq)
				item.hasThreadPrev = true
				item.threadPrevTxnID = oldPrev
				item.threadPrevTxnLgrSq = oldPrevSeq
				if changed {
					item.Current = updated
				}
				if callbackErr = t.journal.putItem(backgroundContext, key, item); callbackErr != nil {
					return false
				}
			}
		case ActionErase:
			data := item.Original
			if data == nil {
				data = item.Current
			}
			callbackErr = t.threadOwners(key, data, entryType)
		}
		return callbackErr == nil
	})
	if err != nil {
		return t.record(err)
	}
	return t.record(callbackErr)
}

func (t *ApplyStateTable) threadOwners(sourceKey [32]byte, data []byte, entryType entry.Type) error {
	if data == nil {
		return nil
	}
	for _, ownerID := range getOwnerAccounts(data, entryType) {
		ownerKey := keylet.Account(ownerID)
		record, found, err := t.journal.get(backgroundContext, ownerKey.Key)
		if err != nil {
			return err
		}
		if found && record.item != nil {
			item := record.item
			if item.Action == ActionErase {
				continue
			}
			if item.Action != ActionCache {
				if bytes.Equal(item.Original, item.Current) && bytes.Compare(sourceKey[:], ownerKey.Key[:]) > 0 {
					oldPrev, oldPrevSeq, updated, changed := threadItem(item.Current, t.txHash, t.txSeq)
					if !changed {
						continue
					}
					ownerType, decodeErr := state.DecodeType(item.Current)
					if decodeErr != nil {
						continue
					}
					if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &threadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
						return err
					}
					continue
				}
				_, _, updated, changed := threadItem(item.Current, t.txHash, t.txSeq)
				if changed {
					item.Current = updated
					if err := t.journal.putItem(backgroundContext, ownerKey.Key, item); err != nil {
						return err
					}
				}
				continue
			}
			oldPrev, oldPrevSeq, updated, changed := threadItem(item.Current, t.txHash, t.txSeq)
			if !changed {
				continue
			}
			ownerType, decodeErr := state.DecodeType(item.Current)
			if decodeErr != nil {
				continue
			}
			if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &threadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
				return err
			}
			continue
		}

		ownerData := []byte(nil)
		if found && record.owner != nil {
			ownerData = record.owner.Updated
		} else {
			ownerData, err = t.base.Read(ownerKey)
			if err != nil {
				t.record(err)
				continue
			}
		}
		if ownerData == nil {
			continue
		}
		oldPrev, oldPrevSeq, updated, changed := threadItem(ownerData, t.txHash, t.txSeq)
		if !changed {
			continue
		}
		ownerType, decodeErr := state.DecodeType(ownerData)
		if decodeErr != nil {
			continue
		}
		if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &threadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
			return err
		}
	}
	return nil
}

func (t *ApplyStateTable) applyOrdered(atomicBase ledgercore.AtomicWriter) (*tx.Metadata, error) {
	metadata := &tx.Metadata{AffectedNodes: make([]tx.AffectedNode, 0)}
	var callbackErr error
	err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		if record.item == nil || callbackErr != nil {
			return callbackErr == nil
		}
		item := record.item
		switch item.Action {
		case ActionCache:
			return true
		case ActionInsert:
			var node tx.AffectedNode
			node, callbackErr = t.buildCreatedNode(key, item.Current)
			if callbackErr == nil {
				metadata.AffectedNodes = append(metadata.AffectedNodes, node)
			}
		case ActionModify:
			if bytes.Equal(item.Original, item.Current) {
				return true
			}
			var node tx.AffectedNode
			node, callbackErr = t.buildModifiedNode(key, item.Original, item.Current)
			if callbackErr == nil {
				metadata.AffectedNodes = append(metadata.AffectedNodes, node)
			}
		case ActionErase:
			var node tx.AffectedNode
			node, callbackErr = t.buildDeletedNode(key, item.Original, item.Current)
			if callbackErr == nil {
				metadata.AffectedNodes = append(metadata.AffectedNodes, node)
			}
		}
		return callbackErr == nil
	})
	if err != nil {
		return nil, t.record(err)
	}
	if callbackErr != nil {
		return nil, callbackErr
	}
	err = t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		owner := record.owner
		if owner == nil {
			return true
		}
		if item := record.item; item != nil {
			noopModify := item.Action == ActionModify && bytes.Equal(item.Original, item.Current)
			if item.Action != ActionCache && !noopModify {
				return true
			}
		}
		if owner.OldPreviousTxnID != ([32]byte{}) {
			metadata.AffectedNodes = append(metadata.AffectedNodes, tx.AffectedNode{
				NodeType:          "ModifiedNode",
				LedgerEntryType:   owner.EntryType.String(),
				LedgerIndex:       fmt.Sprintf("%X", key),
				PreviousTxnID:     fmt.Sprintf("%X", owner.OldPreviousTxnID),
				PreviousTxnLgrSeq: owner.OldPreviousTxnLgrSeq,
			})
		}
		return true
	})
	if err != nil {
		return nil, t.record(err)
	}
	if atomicBase == nil {
		return metadata, nil
	}
	err = atomicBase.ApplyAtomically(func(base ledgercore.Writer) error {
		var applyErr error
		if err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
			if record.item == nil || applyErr != nil {
				return applyErr == nil
			}
			item := record.item
			switch item.Action {
			case ActionInsert:
				applyErr = base.Insert(keylet.Keylet{Key: key}, item.Current)
			case ActionModify:
				if !bytes.Equal(item.Original, item.Current) {
					applyErr = base.Update(keylet.Keylet{Key: key}, item.Current)
				}
			case ActionErase:
				applyErr = base.Erase(keylet.Keylet{Key: key})
			}
			return applyErr == nil
		}); err != nil {
			return err
		}
		if applyErr != nil {
			return applyErr
		}
		if err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
			if record.owner == nil {
				return true
			}
			if applyErr = base.Update(keylet.Keylet{Key: key}, record.owner.Updated); applyErr != nil {
				return false
			}
			return true
		}); err != nil {
			return err
		}
		if applyErr != nil {
			return applyErr
		}
		if t.drops.IsPositive() {
			return base.AdjustDropsDestroyed(t.drops)
		}
		return nil
	})
	if err != nil {
		return nil, t.record(err)
	}
	return metadata, nil
}

// CollectEntries returns all modified ledger entries (Insert/Modify/Erase) for invariant checking.
// Must be called BEFORE Apply() — entries are still in the table at this point.
func (t *ApplyStateTable) CollectEntries() []invariants.InvariantEntry {
	var entries []invariants.InvariantEntry
	err := t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		item := record.item
		if item == nil || item.Action == ActionCache {
			return true
		}
		var before, after, deleteFinal []byte
		switch item.Action {
		case ActionInsert:
			after = bytes.Clone(item.Current)
		case ActionModify:
			before, after = bytes.Clone(item.Original), bytes.Clone(item.Current)
		case ActionErase:
			before = bytes.Clone(item.Original)
			if before == nil {
				before = bytes.Clone(item.Current)
			}
			deleteFinal = bytes.Clone(item.Current)
		}
		typeData := after
		if typeData == nil {
			typeData = before
		}
		entryType, _ := state.DecodeType(typeData)
		entries = append(entries, invariants.InvariantEntry{Key: key, IsDelete: item.Action == ActionErase, Before: before, After: after, DeleteFinal: deleteFinal, EntryType: entryType})
		return true
	})
	if err != nil {
		t.record(err)
	}
	return entries
}

// Size returns the number of entries that have been inserted, modified, or erased.
// This matches rippled's ApplyStateTable::size() which counts non-cache entries.
// Used for the oversizeMetaDataCap check (tecOVERSIZE when size > 5200).
// Reference: rippled/src/xrpld/ledger/detail/ApplyStateTable.cpp
func (t *ApplyStateTable) Size() int {
	count := 0
	err := t.journal.iterate(backgroundContext, func(_ [32]byte, record journalRecord) bool {
		if record.item != nil && record.item.Action != ActionCache {
			count++
		}
		return true
	})
	if err != nil {
		t.record(err)
	}
	return count
}

// modifiedNodePrevTxn returns the node-level PreviousTxnID/PreviousTxnLgrSeq
// for a ModifiedNode. The emitted value is the threaded SLE's PreviousTxnID as
// it existed BEFORE this tx threaded it (curNode's pre-thread value), omitted
// when that value is zero — rippled only emits a pointer when the SLE actually
// carried one (its threading gate is getFieldIndex(sfPreviousTxnID) != -1).
//
// For a normal in-place modify the original node carries the reliable pointer:
// the apply path peeks the SLE, so curNode and the original share it.
//
// For an erase+reinsert, and for a DirectoryNode, the curNode can be a FRESH
// SLE with no prior pointer even though the pre-tx object had one, so the
// original node is the WRONG source. An order-book directory page emptied and
// rebuilt in the same tx — an account replacing the only offer at a quality
// level removes it (page erased) then re-adds the new offer (page recreated from
// scratch by state.DirInsert) — serializes no PreviousTxnID, so rippled threads
// it to self with a zero prior pointer and emits none. There the captured
// pre-thread value of Current (zero for the rebuilt page) is correct; an
// in-place-modified page (e.g. an owner directory) keeps its pointer and still
// emits it. DirectoryNode is the only type with this erase→rebuild-flattened-to-
// modify case AND a serializer that preserves PreviousTxnID round-trip, so its
// Current is a faithful proxy for rippled's curNode field-presence gate. The
// other conditional-threading types (Amendments/FeeSettings/NegativeUNL/AMM) are
// only ever modified in place and several drop PreviousTxnID from Current, so
// they must source the pointer from the original node.
func (t *ApplyStateTable) modifiedNodePrevTxn(key [32]byte, entryType entry.Type, origEntry entry.Entry) (string, uint32) {
	record, found, err := t.journal.get(backgroundContext, key)
	if err != nil {
		t.record(err)
		return origEntry.PreviousTxn()
	}
	if found && record.item != nil && (record.item.reinserted || entryType == entry.TypeDirectoryNode) {
		if !record.item.hasThreadPrev || record.item.threadPrevTxnID == ([32]byte{}) {
			return "", 0
		}
		return fmt.Sprintf("%X", record.item.threadPrevTxnID), record.item.threadPrevTxnLgrSq
	}
	return origEntry.PreviousTxn()
}
