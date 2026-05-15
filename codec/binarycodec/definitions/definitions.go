// Package definitions contains XRPL binary codec field and type definitions.
package definitions

import (
	_ "embed"
	"encoding/json"
	"fmt"
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
// eagerly so every lookup is O(1).
type Definitions struct {
	Types                  map[string]int32
	LedgerEntryTypes       map[string]int32
	Fields                 fieldInstanceMap
	TransactionResults     map[string]int32
	TransactionTypes       map[string]int32
	FieldIDNameMap         map[FieldHeader]string
	GranularPermissions    map[string]int32
	DelegatablePermissions map[string]int32

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
		Types:              data.Types,
		Fields:             data.Fields,
		LedgerEntryTypes:   data.LedgerEntryTypes,
		TransactionResults: data.TransactionResults,
		TransactionTypes:   data.TransactionTypes,
	}

	addFieldHeadersAndOrdinals()
	createFieldIDNameMap()
	initializePermissions()
	buildReverseMaps()
}

func addFieldHeadersAndOrdinals() {
	for k := range definitions.Fields {
		t, _ := definitions.GetTypeCodeByTypeName(definitions.Fields[k].Type)

		if fi, ok := definitions.Fields[k]; ok {
			fi.FieldHeader = &FieldHeader{
				TypeCode:  t,
				FieldCode: definitions.Fields[k].Nth,
			}
			fi.Ordinal = (t<<16 | definitions.Fields[k].Nth)
		}
	}
}

func createFieldIDNameMap() {
	definitions.FieldIDNameMap = make(map[FieldHeader]string, len(definitions.Fields))
	for k := range definitions.Fields {
		fh, _ := definitions.GetFieldHeaderByFieldName(k)

		definitions.FieldIDNameMap[*fh] = k
	}
}

// Initializes granular permissions and delegatable permissions mappings for account permission delegation.
func initializePermissions() {
	definitions.GranularPermissions = map[string]int32{
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

	definitions.DelegatablePermissions = make(map[string]int32, len(definitions.GranularPermissions)+len(definitions.TransactionTypes))

	for name, value := range definitions.GranularPermissions {
		definitions.DelegatablePermissions[name] = value
	}

	for txType, value := range definitions.TransactionTypes {
		definitions.DelegatablePermissions[txType] = value + 1
	}
}

// buildReverseMaps populates code->name lookup tables once so that the
// public Get*Name helpers are O(1) instead of scanning the forward maps.
func buildReverseMaps() {
	definitions.transactionTypeNames = make(map[int32]string, len(definitions.TransactionTypes))
	for name, code := range definitions.TransactionTypes {
		definitions.transactionTypeNames[code] = name
	}
	definitions.transactionResultNames = make(map[int32]string, len(definitions.TransactionResults))
	for name, code := range definitions.TransactionResults {
		definitions.transactionResultNames[code] = name
	}
	definitions.ledgerEntryTypeNames = make(map[int32]string, len(definitions.LedgerEntryTypes))
	for name, code := range definitions.LedgerEntryTypes {
		definitions.ledgerEntryTypeNames[code] = name
	}
	definitions.delegatablePermissionNames = make(map[int32]string, len(definitions.DelegatablePermissions))
	for name, value := range definitions.DelegatablePermissions {
		definitions.delegatablePermissionNames[value] = name
	}
}
