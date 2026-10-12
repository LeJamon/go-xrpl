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

func (t *ApplyStateTable) readBounded(k keylet.Keylet) ([]byte, error) {
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

func (t *ApplyStateTable) existsBounded(k keylet.Keylet) (bool, error) {
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

func (t *ApplyStateTable) insertBounded(k keylet.Keylet, data []byte) error {
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

func (t *ApplyStateTable) updateBounded(k keylet.Keylet, data []byte) error {
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

func (t *ApplyStateTable) eraseBounded(k keylet.Keylet) error {
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

func (t *ApplyStateTable) isErasedBounded(key [32]byte) bool {
	record, found, err := t.journal.get(backgroundContext, key)
	if err != nil {
		t.record(err)
		return false
	}
	return found && record.item != nil && record.item.Action == ActionErase
}

func (t *ApplyStateTable) forEachBounded(fn func([32]byte, []byte) bool) error {
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

func (t *ApplyStateTable) succBounded(key [32]byte) ([32]byte, []byte, bool, error) {
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

// ForEachTracked streams the final tracked item records in key order. The
// bounded path supplies an owned, short-lived record to each callback; callers
// that retain bytes must clone them.
func (t *ApplyStateTable) ForEachTracked(fn func([32]byte, *TrackedEntry) bool) error {
	if fn == nil {
		return nil
	}
	return t.record(t.journal.iterate(backgroundContext, func(key [32]byte, record journalRecord) bool {
		return record.item == nil || fn(key, record.item)
	}))
}

func (t *ApplyStateTable) applyThreadingBounded() error {
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
			callbackErr = t.threadOwnersBounded(key, item.Current, entryType)
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
			callbackErr = t.threadOwnersBounded(key, data, entryType)
		}
		return callbackErr == nil
	})
	if err != nil {
		return t.record(err)
	}
	return t.record(callbackErr)
}

func (t *ApplyStateTable) threadOwnersBounded(sourceKey [32]byte, data []byte, entryType entry.Type) error {
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
					if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &ThreadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
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
			if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &ThreadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
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
		if err := t.journal.putOwner(backgroundContext, ownerKey.Key, &ThreadedOwner{EntryType: ownerType, OldPreviousTxnID: oldPrev, OldPreviousTxnLgrSeq: oldPrevSeq, Updated: updated}); err != nil {
			return err
		}
	}
	return nil
}

func (t *ApplyStateTable) applyOrderedBounded(atomicBase ledgercore.AtomicWriter) (*tx.Metadata, error) {
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

func (t *ApplyStateTable) collectEntriesBounded() []invariants.InvariantEntry {
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

func (t *ApplyStateTable) sizeBounded() int {
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

func (t *ApplyStateTable) modifiedNodePrevTxnBounded(key [32]byte, entryType entry.Type, origEntry entry.Entry) (string, uint32) {
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

// Keep these references in this file so accidental removal of the bounded
// path's interfaces is caught by the compiler when engine callers evolve.
var _ tx.LedgerView = (*ApplyStateTable)(nil)
var _ ledgercore.AtomicWriter = (*ApplyStateTable)(nil)
