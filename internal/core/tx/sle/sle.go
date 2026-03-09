package sle

import (
	"encoding/binary"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
)

// FieldMeta defines how a field should be included in metadata
type FieldMeta int

const (
	// FieldMetaNever - never include in metadata
	FieldMetaNever FieldMeta = 0x00
	// FieldMetaChangeOrig - include original value when it changes (PreviousFields)
	FieldMetaChangeOrig FieldMeta = 0x01
	// FieldMetaChangeNew - include new value when it changes (FinalFields for modifications)
	FieldMetaChangeNew FieldMeta = 0x02
	// FieldMetaDeleteFinal - include in FinalFields when deleted
	FieldMetaDeleteFinal FieldMeta = 0x04
	// FieldMetaCreate - include in NewFields when created
	FieldMetaCreate FieldMeta = 0x08
	// FieldMetaAlways - always include when node is affected
	FieldMetaAlways FieldMeta = 0x10
	// FieldMetaBaseTen - serialize as base-10 (decimal) in metadata JSON, not base-16 (hex).
	// Used for UInt64 amount fields (MaximumAmount, OutstandingAmount, MPTAmount, LockedAmount).
	// Reference: rippled SField.h sMD_BaseTen = 0x20
	FieldMetaBaseTen FieldMeta = 0x20
	// FieldMetaDefault - default metadata behavior (change tracking + create + delete)
	FieldMetaDefault = FieldMetaChangeOrig | FieldMetaChangeNew | FieldMetaDeleteFinal | FieldMetaCreate
)

// SLEAction represents what action was taken on the SLE
type SLEAction int

const (
	SLEActionCache  SLEAction = iota // Read-only, no changes
	SLEActionInsert                  // Newly created
	SLEActionModify                  // Existing entry modified
	SLEActionDelete                  // Entry deleted
)

// FieldInfo contains information about a field's metadata behavior
type FieldInfo struct {
	Name string
	Meta FieldMeta
}

// SLEBase provides common functionality for all SLE types
type SLEBase struct {
	LedgerIndex     [32]byte
	LedgerEntryType string
	Action          SLEAction
	original        map[string]any
	current         map[string]any
	fieldMeta       map[string]FieldMeta
}

// NewSLEBase creates a new SLE base
func NewSLEBase(ledgerIndex [32]byte, entryType string) *SLEBase {
	return &SLEBase{
		LedgerIndex:     ledgerIndex,
		LedgerEntryType: entryType,
		Action:          SLEActionCache,
		original:        make(map[string]any),
		current:         make(map[string]any),
		fieldMeta:       make(map[string]FieldMeta),
	}
}

// SetFieldMeta sets the metadata behavior for a field
func (s *SLEBase) SetFieldMeta(name string, meta FieldMeta) {
	s.fieldMeta[name] = meta
}

// GetFieldMeta returns the metadata behavior for a field
func (s *SLEBase) GetFieldMeta(name string) FieldMeta {
	if meta, ok := s.fieldMeta[name]; ok {
		return meta
	}
	return FieldMetaDefault
}

// SetOriginal sets the original value of a field (called when loading from ledger)
func (s *SLEBase) SetOriginal(name string, value any) {
	s.original[name] = value
	s.current[name] = value
}

// SetField sets a field value (tracks changes from original)
func (s *SLEBase) SetField(name string, value any) {
	if s.Action == SLEActionCache {
		s.Action = SLEActionModify
	}
	s.current[name] = value
}

// GetField returns the current value of a field
func (s *SLEBase) GetField(name string) (any, bool) {
	val, ok := s.current[name]
	return val, ok
}

// HasFieldChanged returns true if the field has changed from its original value
func (s *SLEBase) HasFieldChanged(name string) bool {
	origVal, hasOrig := s.original[name]
	curVal, hasCur := s.current[name]

	if !hasOrig && !hasCur {
		return false
	}
	if hasOrig != hasCur {
		return true
	}
	return !reflect.DeepEqual(origVal, curVal)
}

// MarkAsCreated marks this SLE as newly created
func (s *SLEBase) MarkAsCreated() {
	s.Action = SLEActionInsert
}

// MarkAsDeleted marks this SLE as deleted
func (s *SLEBase) MarkAsDeleted() {
	s.Action = SLEActionDelete
}

// GenerateAffectedNode generates the AffectedNode for metadata
func (s *SLEBase) GenerateAffectedNode() *AffectedNode {
	switch s.Action {
	case SLEActionCache:
		return nil // No changes, no metadata
	case SLEActionInsert:
		return s.generateCreatedNode()
	case SLEActionModify:
		return s.generateModifiedNode()
	case SLEActionDelete:
		return s.generateDeletedNode()
	}
	return nil
}

// generateCreatedNode generates metadata for a newly created node
func (s *SLEBase) generateCreatedNode() *AffectedNode {
	newFields := make(map[string]any)

	for name, value := range s.current {
		meta := s.GetFieldMeta(name)
		// Include if Create or Always flag is set, and value is not default
		if (meta&FieldMetaCreate != 0 || meta&FieldMetaAlways != 0) && !IsDefaultValue(value) {
			newFields[name] = value
		}
	}

	return &AffectedNode{
		NodeType:        "CreatedNode",
		LedgerEntryType: s.LedgerEntryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(s.LedgerIndex[:])),
		NewFields:       newFields,
	}
}

// generateModifiedNode generates metadata for a modified node
func (s *SLEBase) generateModifiedNode() *AffectedNode {
	previousFields := make(map[string]any)
	finalFields := make(map[string]any)
	anyFieldChanged := false // Track if ANY field changed (including sMD_Never ones)

	// Collect all field names
	allFields := make(map[string]bool)
	for name := range s.original {
		allFields[name] = true
	}
	for name := range s.current {
		allFields[name] = true
	}

	for name := range allFields {
		meta := s.GetFieldMeta(name)
		origVal, hasOrig := s.original[name]
		curVal, hasCur := s.current[name]

		// Check if field changed (for ANY field, including sMD_Never)
		changed := false
		if hasOrig != hasCur {
			changed = true
		} else if hasOrig && hasCur && !reflect.DeepEqual(origVal, curVal) {
			changed = true
		}

		if changed {
			anyFieldChanged = true
			// Add to PreviousFields if ChangeOrig flag is set AND field actually changed
			// (skip fields with sMD_Never)
			if meta&FieldMetaChangeOrig != 0 && hasOrig {
				previousFields[name] = origVal
			}
		}

		// Add to FinalFields if field has Always OR ChangeNew flag (matching rippled behavior)
		// rippled: if (obj.getFName().shouldMeta(SField::sMD_Always | SField::sMD_ChangeNew))
		// (skip fields with sMD_Never)
		if meta != FieldMetaNever && (meta&FieldMetaAlways != 0 || meta&FieldMetaChangeNew != 0) && hasCur {
			finalFields[name] = curVal
		}
	}

	// Emit ModifiedNode if any field changed (rippled compares whole node)
	if !anyFieldChanged {
		return nil
	}

	node := &AffectedNode{
		NodeType:        "ModifiedNode",
		LedgerEntryType: s.LedgerEntryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(s.LedgerIndex[:])),
	}

	if len(finalFields) > 0 {
		node.FinalFields = finalFields
	}
	if len(previousFields) > 0 {
		node.PreviousFields = previousFields
	}

	return node
}

// generateDeletedNode generates metadata for a deleted node
// Reference: rippled ApplyStateTable.cpp - for deleted nodes, FinalFields includes
// all fields with sMD_Always or sMD_DeleteFinal flags, WITHOUT checking isDefault().
func (s *SLEBase) generateDeletedNode() *AffectedNode {
	finalFields := make(map[string]any)
	previousFields := make(map[string]any)

	// For deleted nodes, FinalFields come from current values (the state being deleted).
	// rippled uses curNode for FinalFields in deleted nodes.
	// Include ALL fields with DeleteFinal or Always flag - no default value filtering!
	for name, value := range s.current {
		meta := s.GetFieldMeta(name)
		// Skip fields with FieldMetaNever
		if meta == FieldMetaNever {
			continue
		}
		// Include in FinalFields if DeleteFinal or Always flag is set
		// Unlike CreatedNode, we do NOT skip default values for DeletedNode
		if meta&FieldMetaDeleteFinal != 0 || meta&FieldMetaAlways != 0 {
			finalFields[name] = value
		}
	}

	// Also check original values for fields not in current
	// (in case current is empty but original has data)
	for name, origVal := range s.original {
		if _, exists := s.current[name]; exists {
			continue // Already processed from current
		}
		meta := s.GetFieldMeta(name)
		if meta == FieldMetaNever {
			continue
		}
		if meta&FieldMetaDeleteFinal != 0 || meta&FieldMetaAlways != 0 {
			finalFields[name] = origVal
		}
	}

	// Check for changes from original (for PreviousFields)
	// Reference: rippled checks origNode for fields with sMD_ChangeOrig that differ from curNode
	for name, origVal := range s.original {
		meta := s.GetFieldMeta(name)
		curVal, hasCur := s.current[name]
		if hasCur && meta&FieldMetaChangeOrig != 0 && !reflect.DeepEqual(origVal, curVal) {
			previousFields[name] = origVal
		}
	}

	node := &AffectedNode{
		NodeType:        "DeletedNode",
		LedgerEntryType: s.LedgerEntryType,
		LedgerIndex:     strings.ToUpper(hex.EncodeToString(s.LedgerIndex[:])),
	}

	if len(finalFields) > 0 {
		node.FinalFields = finalFields
	}
	if len(previousFields) > 0 {
		node.PreviousFields = previousFields
	}

	return node
}

// IsDefaultValue checks if a value is a "default" value that should be omitted
func IsDefaultValue(value any) bool {
	if value == nil {
		return true
	}
	switch v := value.(type) {
	case int:
		return v == 0
	case int64:
		return v == 0
	case uint32:
		return v == 0
	case uint64:
		return v == 0
	case float64:
		return v == 0
	case string:
		if v == "" || v == "0" {
			return true
		}
		// Check for all-zero hex strings (default values for Hash160, Hash256, UInt64 etc.)
		if isAllZeroHex(v) {
			return true
		}
		return false
	case []byte:
		return len(v) == 0
	case [32]byte:
		var zero [32]byte
		return v == zero
	case map[string]any:
		// IOU amounts (maps with value/currency/issuer) are never default when present
		// in serialized data - even zero-value amounts carry currency/issuer info.
		// A field is "default" only if it's absent from the serialized data entirely.
		return false
	}
	return false
}

// isAllZeroHex checks if a string is a hex representation of all zeros
func isAllZeroHex(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if c != '0' {
			return false
		}
	}
	return true
}

// SLETracker tracks all SLEs modified during transaction application
type SLETracker struct {
	entries map[[32]byte]*SLEBase
	order   [][32]byte // Preserve insertion order
}

// NewSLETracker creates a new SLE tracker
func NewSLETracker() *SLETracker {
	return &SLETracker{
		entries: make(map[[32]byte]*SLEBase),
		order:   make([][32]byte, 0),
	}
}

// Track adds or retrieves an SLE for tracking
func (t *SLETracker) Track(ledgerIndex [32]byte, entryType string) *SLEBase {
	if sle, exists := t.entries[ledgerIndex]; exists {
		return sle
	}
	sle := NewSLEBase(ledgerIndex, entryType)
	t.entries[ledgerIndex] = sle
	t.order = append(t.order, ledgerIndex)
	return sle
}

// Get retrieves a tracked SLE
func (t *SLETracker) Get(ledgerIndex [32]byte) (*SLEBase, bool) {
	sle, exists := t.entries[ledgerIndex]
	return sle, exists
}

// GenerateAffectedNodes generates all AffectedNodes for the tracked SLEs
func (t *SLETracker) GenerateAffectedNodes() []AffectedNode {
	var nodes []AffectedNode
	for _, key := range t.order {
		sle := t.entries[key]
		if node := sle.GenerateAffectedNode(); node != nil {
			nodes = append(nodes, *node)
		}
	}
	return nodes
}

// GetLedgerEntryType extracts the LedgerEntryType (UInt16, field code 1) from raw
// binary SLE data without a full codec decode. The first field in XRPL binary
// format is always the LedgerEntryType encoded as header byte 0x11 + 2 bytes value.
func GetLedgerEntryType(data []byte) (uint16, error) {
	if len(data) < 3 {
		return 0, errors.New("data too short to contain LedgerEntryType")
	}
	// Header byte for UInt16 (type 1) + field code 1 = 0x11
	if data[0] != 0x11 {
		return 0, errors.New("unexpected header byte, expected 0x11 for LedgerEntryType")
	}
	return binary.BigEndian.Uint16(data[1:3]), nil
}
