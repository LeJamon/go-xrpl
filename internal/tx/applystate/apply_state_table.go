package applystate

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	ledgercore "github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/overlay"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
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
	Current  []byte // Current state (the final pre-erase image for deletes)

	// reinserted marks a modify produced by erase-then-insert of the same key
	// (rippled ApplyStateTable::insert after erase → Action::modify,
	// ApplyStateTable.cpp:483-487). For such a node rippled's curNode is a fresh
	// SLE, so threadItem reads a zero PreviousTxnID and emits no node-level
	// PreviousTxnID. A normal in-place modify (Cache→modify / update) instead
	// uses the original node's PreviousTxnID — even when goXRPL's serializer
	// drops it from Current and threading re-adds it.
	reinserted bool

	// threadPrev* capture Current's PreviousTxnID/PreviousTxnLgrSeq as they
	// existed BEFORE applyThreading threaded this tx onto the entry — the value
	// rippled's threadItem reads from curNode (ApplyStateTable.cpp:552-581 via
	// STLedgerEntry::thread). Used by buildModifiedNode for reinserted nodes.
	hasThreadPrev      bool
	threadPrevTxnID    [32]byte
	threadPrevTxnLgrSq uint32
}

// threadedOwner: thread-only modification to an SLE (PreviousTxnID /
// PreviousTxnLgrSeq bumped, business fields unchanged). Mirrors
// rippled's `Mods` table at ApplyStateTable.cpp:584-633 — kept out of
// items_ (Action::cache, skipped by apply loop :141-143) so the
// emitted AffectedNode carries only {LedgerIndex, LedgerEntryType,
// PreviousTxnID, PreviousTxnLgrSeq}, no FinalFields / PreviousFields.
type threadedOwner struct {
	EntryType            entry.Type
	OldPreviousTxnID     [32]byte
	OldPreviousTxnLgrSeq uint32
	Updated              []byte // SLE bytes after threadItem write
}

// ApplyStateTable wraps a LedgerView and tracks all modifications
// for automatic metadata generation, similar to rippled's ApplyStateTable
type ApplyStateTable struct {
	base        AtomicLedgerView
	recordError func(error)
	drops       drops.XRPAmount
	txHash      [32]byte
	txSeq       uint32
	rules       *amendment.Rules
	journal     *journal
}

type AtomicLedgerView interface {
	tx.LedgerView
	ledgercore.AtomicWriter
	ExecutionJournalConfig() overlay.Config
}

var _ AtomicLedgerView = (*ApplyStateTable)(nil)

// NewApplyStateTable creates a new ApplyStateTable wrapping the given base view.
// rules controls amendment-gated behaviour (threading, metadata flags).
func NewApplyStateTable(base AtomicLedgerView, txHash [32]byte, txSeq uint32, rules *amendment.Rules) *ApplyStateTable {
	return newApplyStateTable(base, txHash, txSeq, rules, nil)
}

// NewApplyStateTableWithErrorRecorder creates an ApplyStateTable that reports
// errors from its underlying view to record. This keeps ledger-operation
// failures observable when a transaction handler turns them into a TER.
func NewApplyStateTableWithErrorRecorder(
	base AtomicLedgerView,
	txHash [32]byte,
	txSeq uint32,
	rules *amendment.Rules,
	record func(error),
) *ApplyStateTable {
	return newApplyStateTable(base, txHash, txSeq, rules, record)
}

func newApplyStateTable(
	base AtomicLedgerView,
	txHash [32]byte,
	txSeq uint32,
	rules *amendment.Rules,
	record func(error),
) *ApplyStateTable {
	return &ApplyStateTable{
		base:        base,
		recordError: record,
		txHash:      txHash,
		txSeq:       txSeq,
		rules:       rules,
		journal:     newJournal(base.ExecutionJournalConfig()),
	}
}

func (t *ApplyStateTable) clone() *ApplyStateTable {
	journal := t.journal.fork()
	return &ApplyStateTable{
		base:        t.base,
		recordError: t.recordError,
		drops:       t.drops,
		txHash:      t.txHash,
		txSeq:       t.txSeq,
		rules:       t.rules,
		journal:     journal,
	}
}

func (t *ApplyStateTable) record(err error) error {
	if err != nil && t.recordError != nil {
		t.recordError(err)
	}
	return err
}

func (t *ApplyStateTable) adopt(staged *ApplyStateTable) {
	_ = t.journal.close()
	t.journal = staged.journal
	staged.journal = nil
	t.drops = staged.drops
}

// Close releases all bounded journal state. It is safe to call repeatedly.
func (t *ApplyStateTable) Close() error {
	if t == nil {
		return nil
	}
	return t.journal.close()
}

func (t *ApplyStateTable) ApplyAtomically(apply func(ledgercore.Writer) error) error {
	staged := t.clone()
	if staged.journal.err != nil {
		_ = staged.Close()
		return t.record(staged.journal.err)
	}
	if err := apply(staged); err != nil {
		_ = staged.Close()
		return err
	}
	t.adopt(staged)
	return nil
}

// AdjustDropsDestroyed records destroyed XRP
func (t *ApplyStateTable) AdjustDropsDestroyed(drops drops.XRPAmount) error {
	t.drops = t.drops.Add(drops)
	return nil
}

// Apply commits all changes to the base view and returns generated metadata.
// Threading and metadata generation complete before changes are flushed.
// Reference: rippled ApplyStateTable.cpp apply() lines 113-292
func (t *ApplyStateTable) Apply() (*tx.Metadata, error) {
	staged := t.clone()
	if staged.journal.err != nil {
		_ = staged.Close()
		return nil, t.record(staged.journal.err)
	}
	if err := staged.applyThreading(); err != nil {
		_ = staged.Close()
		return nil, err
	}
	metadata, err := staged.applyOrdered(staged.base)
	if err != nil {
		_ = staged.Close()
		return nil, err
	}
	t.adopt(staged)
	return metadata, nil
}

// Preview generates the same metadata as Apply without mutating the base view.
func (t *ApplyStateTable) Preview() (*tx.Metadata, error) {
	staged := t.clone()
	if staged.journal.err != nil {
		_ = staged.Close()
		return nil, t.record(staged.journal.err)
	}
	if err := staged.applyThreading(); err != nil {
		_ = staged.Close()
		return nil, err
	}
	metadata, err := staged.applyOrdered(nil)
	_ = staged.Close()
	return metadata, err
}

// ApplyUnthreaded commits changes without updating transaction threading.
func (t *ApplyStateTable) ApplyUnthreaded() error {
	staged := t.clone()
	if staged.journal.err != nil {
		_ = staged.Close()
		return t.record(staged.journal.err)
	}
	_, err := staged.applyOrdered(staged.base)
	if err != nil {
		_ = staged.Close()
		return err
	}
	t.adopt(staged)
	return nil
}

// effectiveRules returns the amendment rules for this apply table.
// Rules MUST be set by the caller. See Engine.rules in engine.go for
// the rationale — mirroring rippled's `Rules() = delete`, there is no
// silent fallback. Callers (engine.Apply, batch tx paths) thread
// rules through from the parent ledger's Amendments SLE; tests pass
// AllSupportedRules() or EmptyRules() explicitly.
func (t *ApplyStateTable) effectiveRules() *amendment.Rules {
	if t.rules == nil {
		panic("applystate.ApplyStateTable: rules is nil — caller must pass " +
			"amendment.Rules at construction (NewApplyStateTable).")
	}
	return t.rules
}

// TxExists delegates to the base view to check if a transaction exists.
// Reference: rippled ReadView::txExists()
func (t *ApplyStateTable) TxExists(txID [32]byte) (bool, error) {
	exists, err := t.base.TxExists(txID)
	return exists, t.record(err)
}

// Rules returns the amendment rules for this view.
func (t *ApplyStateTable) Rules() *amendment.Rules {
	return t.rules
}

// LedgerSeq returns the building ledger's sequence number.
// Reference: rippled ReadView::seq().
func (t *ApplyStateTable) LedgerSeq() uint32 {
	return t.txSeq
}

var errUnregisteredLedgerEntryType = errors.New("applystate: unregistered ledger entry type")

func metadataType(data []byte) (entry.Type, error) {
	entryType, err := state.DecodeType(data)
	if err != nil && len(data) >= 3 && data[0] == 0x11 {
		rawType := entry.Type(binary.BigEndian.Uint16(data[1:3]))
		return 0, fmt.Errorf("%w %q: %v", errUnregisteredLedgerEntryType, rawType, err)
	}
	return entryType, err
}

func metadataEntry(entryType entry.Type) (entry.Entry, error) {
	model := entry.New(entryType)
	if model == nil {
		return nil, fmt.Errorf("%w %q", errUnregisteredLedgerEntryType, entryType)
	}
	return model, nil
}

// buildCreatedNode creates metadata for a newly created entry
func (t *ApplyStateTable) buildCreatedNode(key [32]byte, data []byte) (tx.AffectedNode, error) {
	entryType, err := metadataType(data)
	if err != nil {
		return tx.AffectedNode{}, err
	}

	node := tx.AffectedNode{
		NodeType:        "CreatedNode",
		LedgerEntryType: entryType.String(),
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		NewFields:       make(map[string]any),
	}

	entry, err := metadataEntry(entryType)
	if err != nil {
		return node, err
	}
	if err := entry.Decode(data); err != nil {
		return node, err
	}
	entry.EmitNewFields(node.NewFields)
	return node, nil
}

// buildModifiedNode creates metadata for a modified entry
func (t *ApplyStateTable) buildModifiedNode(key [32]byte, original, current []byte) (tx.AffectedNode, error) {
	entryType, err := metadataType(current)
	if err != nil {
		return tx.AffectedNode{}, err
	}

	node := tx.AffectedNode{
		NodeType:        "ModifiedNode",
		LedgerEntryType: entryType.String(),
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		FinalFields:     make(map[string]any),
		PreviousFields:  make(map[string]any),
	}

	origEntry, err := metadataEntry(entryType)
	if err != nil {
		return node, err
	}
	currEntry, err := metadataEntry(entryType)
	if err != nil {
		return node, err
	}
	if err := origEntry.Decode(original); err != nil {
		return node, err
	}
	if err := currEntry.Decode(current); err != nil {
		return node, err
	}
	// PreviousTxnID/PreviousTxnLgrSeq are only emitted on ModifiedNode
	// when the entry is a threaded type. Rippled gates this at
	// STLedgerEntry::isThreadedType (libxrpl/protocol/STLedgerEntry.cpp:
	// 90-105) — DirectoryNode/Amendments/FeeSettings/NegativeUNL/AMM are
	// EXCLUDED from threading unless the fixPreviousTxnID amendment is
	// enabled. Without this gate, goxrpl emits an extra ~36 bytes per
	// dir-pagination modify (PreviousTxnID 32B + sfPreviousTxnLgrSeq 4B +
	// the two field markers); the tx-tree leaf bytes for that tx then
	// diverge from rippled byte-for-byte, transaction_hash diverges,
	// ledger_hash diverges, account_hash matches — exactly the iter7/8
	// stall pattern at vseq=7.
	// Default fixPreviousTxnID to false when rules is nil — this matches
	// rippled's pre-amendment behaviour (DirectoryNode is NOT threaded,
	// so its ModifiedNode meta gets no PreviousTxnID/PreviousTxnLgrSeq).
	// Production callers always set rules at NewApplyStateTable; tests
	// that build a bare table get the safe rippled-default classification.
	fixPreviousTxnID := false
	if t.rules != nil {
		fixPreviousTxnID = t.rules.Enabled(amendment.FeatureFixPreviousTxnID)
	}
	if isThreadedType(entryType, fixPreviousTxnID) {
		node.PreviousTxnID, node.PreviousTxnLgrSeq = t.modifiedNodePrevTxn(key, entryType, origEntry)
	}
	currEntry.EmitPreviousFields(origEntry, node.PreviousFields)
	currEntry.EmitFinalFields(node.FinalFields)
	// Detect rippled's "prevs holds an STI_NOTPRESENT entry" case
	// (ApplyStateTable.cpp:209-220, with the STObject iterator
	// surfacing template NOTPRESENT placeholders per STObject.cpp:
	// 156-168). When orig has an absent-in-instance template field
	// that cur has set, and the field carries sMD_ChangeOrig, rippled
	// emplaces the NOTPRESENT obj into prevs — making prevs.empty()
	// false (so the `if (!prevs.empty()) emplace_back(prevs)` gate
	// fires) while serializing zero inner bytes. The wire effect is
	// an empty `PreviousFields: {}` (`E6 E1`).
	//
	// Goxrpl uses generated EmitChangeOrigFields to enumerate the
	// MetaDefault (sMD_ChangeOrig-bearing) field set on each side.
	// MetaAlways fields like RootIndex are excluded — they appear in
	// FinalFields but lack sMD_ChangeOrig at the rippled level, so a
	// future MetaAlways field that transitions absent→present on a
	// Modify must not trip this heuristic. Only fires when no real
	// PreviousFields diff exists — a non-empty PreviousFields already
	// produces the wire wrapper.
	if len(node.PreviousFields) == 0 {
		origChangeOrig := make(map[string]any)
		origEntry.EmitChangeOrigFields(origChangeOrig)
		curChangeOrig := make(map[string]any)
		currEntry.EmitChangeOrigFields(curChangeOrig)
		for name := range curChangeOrig {
			if _, hadInOrig := origChangeOrig[name]; !hadInOrig {
				node.EmitEmptyPreviousFields = true
				break
			}
		}
	}
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
func (t *ApplyStateTable) buildDeletedNode(key [32]byte, original, current []byte) (tx.AffectedNode, error) {
	// Use current for entry type (it's the state just before deletion)
	entryType, err := metadataType(current)
	if err != nil {
		return tx.AffectedNode{}, err
	}

	node := tx.AffectedNode{
		NodeType:        "DeletedNode",
		LedgerEntryType: entryType.String(),
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(key[:])),
		FinalFields:     make(map[string]any),
		PreviousFields:  make(map[string]any),
	}

	origEntry, err := metadataEntry(entryType)
	if err != nil {
		return node, err
	}
	currEntry, err := metadataEntry(entryType)
	if err != nil {
		return node, err
	}
	if err := origEntry.Decode(original); err != nil {
		return node, err
	}
	if err := currEntry.Decode(current); err != nil {
		return node, err
	}
	node.PreviousTxnID, node.PreviousTxnLgrSeq = origEntry.PreviousTxn()
	currEntry.EmitDeletePreviousFields(origEntry, node.PreviousFields)
	currEntry.EmitDeleteFinalFields(node.FinalFields)
	restoreDeletedNodePrevTxn(node.FinalFields, node.PreviousTxnID, node.PreviousTxnLgrSeq)
	if len(node.PreviousFields) == 0 {
		node.PreviousFields = nil
	}
	if len(node.FinalFields) == 0 {
		node.FinalFields = nil
	}
	return node, nil
}

// restoreDeletedNodePrevTxn forces a DeletedNode's FinalFields to carry the
// entry's pre-tx PreviousTxnID/PreviousTxnLgrSeq (prevID/prevSeq, read from the
// pre-tx Original). rippled builds a deleted node's FinalFields from its
// in-memory SLE (curNode), which always retains the stored sfPreviousTxnID
// because it is never stripped on the delete path; both fields carry
// sMD_DeleteFinal, so they are emitted with the node's last-modified pointer.
//
// goXRPL's equivalent of curNode is the Current blob. Two cases leave Current
// without the correct pointer, both of which this restore corrects to the
// stored value from Original:
//   - An entry modified (threaded to the current tx) then erased within the
//     same tx (e.g. a resting offer partially then fully consumed during
//     crossing) holds the current tx as PreviousTxnID in Current — the correct
//     FinalFields value is the prior pointer.
//   - An entry whose serializer resets PreviousTxnID (e.g. NFTokenPage rebuilt
//     during a page merge before deletion) holds the required zero default in
//     Current, so the stored pointer must replace it.
//
// In both cases prevID/prevSeq come from the pre-tx Original, which equals the
// stored pointer rippled's curNode reports (an erased node is never re-threaded,
// so its stored pointer is its last-modified value). When prevID is empty the
// entry was never threaded (e.g. created and deleted), and nothing is stamped.
func restoreDeletedNodePrevTxn(finalFields map[string]any, prevID string, prevSeq uint32) {
	if prevID == "" {
		return
	}
	finalFields["PreviousTxnID"] = prevID
	finalFields["PreviousTxnLgrSeq"] = prevSeq
}

// Note: isDefaultValue is defined in state.go
