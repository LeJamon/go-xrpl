// Package definitions contains XRPL binary codec field and type definitions.
package definitions

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
)

var (
	//go:embed definitions.json
	docBytes []byte

	// definitions is the singleton instance of the Definitions struct.
	definitions *Definitions
)

// Definitions holds the binary serialization definitions for the XRP Ledger,
// loaded once at package init from the embedded RFC JSON document.
//
// All forward maps (name -> code) and reverse maps (code -> name) are built
// eagerly so every lookup is O(1). The maps are unexported so the shared
// process-wide tables cannot be mutated; use the lookup methods, or the
// copy-returning map accessors, to read them.
type Definitions struct {
	types                  map[string]int32
	ledgerEntryTypes       map[string]int32
	fields                 fieldInstanceMap
	transactionResults     map[string]int32
	transactionTypes       map[string]int32
	fieldIDNameMap         map[FieldHeader]string
	granularPermissions    map[string]int32
	delegatablePermissions map[string]int32

	// Reverse lookup maps used by enumToStr-style decoders.
	transactionTypeNames       map[int32]string
	transactionResultNames     map[int32]string
	ledgerEntryTypeNames       map[int32]string
	delegatablePermissionNames map[int32]string
}

// Get returns the singleton instance of Definitions.
func Get() *Definitions {
	return definitions
}

// Types returns a copy of the type-name -> type-code map.
func (d *Definitions) Types() map[string]int32 { return maps.Clone(d.types) }

// LedgerEntryTypes returns a copy of the ledger-entry-type-name -> code map.
func (d *Definitions) LedgerEntryTypes() map[string]int32 { return maps.Clone(d.ledgerEntryTypes) }

// TransactionTypes returns a copy of the transaction-type-name -> code map.
func (d *Definitions) TransactionTypes() map[string]int32 { return maps.Clone(d.transactionTypes) }

// TransactionResults returns a copy of the transaction-result-name -> code map.
func (d *Definitions) TransactionResults() map[string]int32 { return maps.Clone(d.transactionResults) }

// Fields returns a copy of the field-name -> field-instance map. The
// FieldInstance pointers are shared; callers must not mutate them.
func (d *Definitions) Fields() map[string]*FieldInstance { return maps.Clone(d.fields) }

// HasField reports whether name is a known serialized field.
func (d *Definitions) HasField(name string) bool {
	_, ok := d.fields[name]
	return ok
}

// IsGranularPermission reports whether value is a registered granular
// permission value.
func (d *Definitions) IsGranularPermission(value int32) bool {
	for _, v := range d.granularPermissions {
		if v == value {
			return true
		}
	}
	return false
}

type definitionsDoc struct {
	Types              map[string]int32 `json:"TYPES"`
	LedgerEntryTypes   map[string]int32 `json:"LEDGER_ENTRY_TYPES"`
	Fields             fieldInstanceMap `json:"FIELDS"`
	TransactionResults map[string]int32 `json:"TRANSACTION_RESULTS"`
	TransactionTypes   map[string]int32 `json:"TRANSACTION_TYPES"`
}

// loadDefinitions decodes the embedded JSON definitions document and
// populates the singleton. It panics if the embedded document is malformed
// — that condition is a build-time bug, not a runtime input failure.
func loadDefinitions() {
	var data definitionsDoc
	if err := json.Unmarshal(docBytes, &data); err != nil {
		panic(fmt.Errorf("definitions: decode embedded JSON: %w", err))
	}

	definitions = &Definitions{
		types:              data.Types,
		fields:             data.Fields,
		ledgerEntryTypes:   data.LedgerEntryTypes,
		transactionResults: data.TransactionResults,
		transactionTypes:   data.TransactionTypes,
	}

	addFieldHeadersAndOrdinals()
	createFieldIDNameMap()
	initializePermissions()
	buildReverseMaps()
}

func addFieldHeadersAndOrdinals() {
	for k := range definitions.fields {
		t, _ := definitions.TypeCode(definitions.fields[k].Type)

		if fi, ok := definitions.fields[k]; ok {
			fi.FieldHeader = &FieldHeader{
				TypeCode:  t,
				FieldCode: definitions.fields[k].Nth,
			}
			fi.Ordinal = (t<<16 | definitions.fields[k].Nth)
		}
	}
}

func createFieldIDNameMap() {
	definitions.fieldIDNameMap = make(map[FieldHeader]string, len(definitions.fields))
	for k := range definitions.fields {
		fh, _ := definitions.FieldHeaderByName(k)

		definitions.fieldIDNameMap[*fh] = k
	}
}

// Initializes granular permissions and delegatable permissions mappings for account permission delegation.
func initializePermissions() {
	definitions.granularPermissions = map[string]int32{
		"TrustlineAuthorize":     65537,
		"TrustlineFreeze":        65538,
		"TrustlineUnfreeze":      65539,
		"AccountDomainSet":       65540,
		"AccountEmailHashSet":    65541,
		"AccountMessageKeySet":   65542,
		"AccountTransferRateSet": 65543,
		"AccountTickSizeSet":     65544,
		"PaymentMint":            65545,
		"PaymentBurn":            65546,
		"MPTokenIssuanceLock":    65547,
		"MPTokenIssuanceUnlock":  65548,
	}

	definitions.delegatablePermissions = make(map[string]int32, len(definitions.granularPermissions)+len(definitions.transactionTypes))

	maps.Copy(definitions.delegatablePermissions, definitions.granularPermissions)

	for txType, value := range definitions.transactionTypes {
		definitions.delegatablePermissions[txType] = value + 1
	}
}

// buildReverseMaps populates code->name lookup tables once so that the
// public Get*Name helpers are O(1) instead of scanning the forward maps.
func buildReverseMaps() {
	definitions.transactionTypeNames = make(map[int32]string, len(definitions.transactionTypes))
	for name, code := range definitions.transactionTypes {
		definitions.transactionTypeNames[code] = name
	}
	definitions.transactionResultNames = make(map[int32]string, len(definitions.transactionResults))
	for name, code := range definitions.transactionResults {
		definitions.transactionResultNames[code] = name
	}
	definitions.ledgerEntryTypeNames = make(map[int32]string, len(definitions.ledgerEntryTypes))
	for name, code := range definitions.ledgerEntryTypes {
		definitions.ledgerEntryTypeNames[code] = name
	}
	definitions.delegatablePermissionNames = make(map[int32]string, len(definitions.delegatablePermissions))
	for name, value := range definitions.delegatablePermissions {
		definitions.delegatablePermissionNames[value] = name
	}
}
