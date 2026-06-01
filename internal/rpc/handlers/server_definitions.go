package handlers

import (
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	definitions "github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/crypto/common"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// ServerDefinitionsMethod handles the server_definitions RPC method.
// Returns the transaction, ledger entry, field, and result type definitions
// used by the binary codec for serialization.
// Reference: rippled ServerInfo.cpp doServerDefinitions.
type ServerDefinitionsMethod struct{ BaseHandler }

// The definitions document is static, so the response body and its hash are
// computed once and shared across calls, mirroring rippled's
// `static detail::ServerDefinitions` (ServerInfo.cpp:311).
var (
	serverDefsOnce sync.Once
	serverDefsBase map[string]interface{}
	serverDefsHash string
)

func buildServerDefinitions() {
	defs := definitions.Get()

	// Collect field names for deterministic ordering.
	fieldNames := make([]string, 0, len(defs.Fields))
	for name := range defs.Fields {
		fieldNames = append(fieldNames, name)
	}
	sort.Strings(fieldNames)

	// Build FIELDS array matching rippled format:
	// Each entry is [fieldName, {nth, isVLEncoded, isSerialized, isSigningField, type}]
	fields := make([]interface{}, 0, len(defs.Fields))
	for _, name := range fieldNames {
		fi := defs.Fields[name]
		fields = append(fields, []interface{}{
			name,
			map[string]interface{}{
				"nth":            fi.Nth,
				"isVLEncoded":    fi.IsVLEncoded,
				"isSerialized":   fi.IsSerialized,
				"isSigningField": fi.IsSigningField,
				"type":           fi.Type,
			},
		})
	}

	serverDefsBase = map[string]interface{}{
		"TYPES":               defs.Types,
		"FIELDS":              fields,
		"LEDGER_ENTRY_TYPES":  defs.LedgerEntryTypes,
		"TRANSACTION_TYPES":   defs.TransactionTypes,
		"TRANSACTION_RESULTS": defs.TransactionResults,
	}

	// Hash follows rippled's approach (ServerInfo.cpp:288-293) — sha512Half over
	// the serialized definitions document, emitted as the response `hash` field
	// so clients can cache it and short-circuit on subsequent calls. encoding/json
	// sorts map keys, so the serialization is deterministic across calls. The
	// value is a per-server cache token (the client echoes back the hash this
	// server gave it), not a cross-implementation constant: it intentionally
	// need not equal rippled's, whose Json::FastWriter serializes differently.
	encoded, _ := json.Marshal(serverDefsBase)
	sum := common.Sha512Half(encoded)
	serverDefsHash = strings.ToUpper(hex.EncodeToString(sum[:]))
}

func (m *ServerDefinitionsMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	serverDefsOnce.Do(buildServerDefinitions)

	// When the client echoes a matching hash, return just the hash so it can
	// reuse its cached definitions (rippled ServerInfo.cpp:304-317).
	if len(params) > 0 {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(params, &raw); err == nil {
			if hashRaw, ok := raw["hash"]; ok {
				var hashStr string
				if err := json.Unmarshal(hashRaw, &hashStr); err != nil || !isValidDefinitionsHash(hashStr) {
					return nil, types.RpcErrorInvalidField("hash")
				}
				if strings.EqualFold(hashStr, serverDefsHash) {
					return map[string]interface{}{"hash": serverDefsHash}, nil
				}
			}
		}
	}

	response := make(map[string]interface{}, len(serverDefsBase)+1)
	for k, v := range serverDefsBase {
		response[k] = v
	}
	response["hash"] = serverDefsHash
	return response, nil
}

// isValidDefinitionsHash reports whether s is a 256-bit hash in hex form,
// matching rippled's uint256::parseHex requirement (ServerInfo.cpp:307).
func isValidDefinitionsHash(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
