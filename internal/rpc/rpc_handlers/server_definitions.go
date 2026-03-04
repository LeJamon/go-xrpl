package rpc_handlers

import (
	"encoding/json"
	"sort"

	definitions "github.com/LeJamon/goXRPLd/internal/codec/binary-codec/definitions"
	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// ServerDefinitionsMethod handles the server_definitions RPC method.
// Returns the transaction, ledger entry, field, and result type definitions
// used by the binary codec for serialization.
// Reference: rippled ServerDefinitions.cpp
type ServerDefinitionsMethod struct{}

func (m *ServerDefinitionsMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	defs := definitions.Get()

	// Build FIELDS array matching rippled format:
	// Each entry is [fieldName, {nth, isVLEncoded, isSerialized, isSigningField, type}]
	fields := make([]interface{}, 0, len(defs.Fields))

	// Collect field names for deterministic ordering
	fieldNames := make([]string, 0, len(defs.Fields))
	for name := range defs.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	for _, name := range fieldNames {
		fi := defs.Fields[name]
		entry := []interface{}{
			name,
			map[string]interface{}{
				"nth":            fi.Nth,
				"isVLEncoded":    fi.IsVLEncoded,
				"isSerialized":   fi.IsSerialized,
				"isSigningField": fi.IsSigningField,
				"type":           fi.Type,
			},
		}
		fields = append(fields, entry)
	}

	response := map[string]interface{}{
		"TYPES":              defs.Types,
		"FIELDS":             fields,
		"LEDGER_ENTRY_TYPES": defs.LedgerEntryTypes,
		"TRANSACTION_TYPES":  defs.TransactionTypes,
		"TRANSACTION_RESULTS": defs.TransactionResults,
	}

	return response, nil
}

func (m *ServerDefinitionsMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *ServerDefinitionsMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
