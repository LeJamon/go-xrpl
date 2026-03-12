package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerHeaderMethod handles the ledger_header RPC method.
// Supports lookup by ledger_index (string/numeric) and ledger_hash.
// Note: This method is deprecated in rippled in favor of 'ledger'.
type LedgerHeaderMethod struct{}

func (m *LedgerHeaderMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	var ledger types.LedgerReader
	var lookupErr error

	if request.LedgerHash != "" {
		// Lookup by hash
		hashBytes, err := hex.DecodeString(request.LedgerHash)
		if err != nil || len(hashBytes) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid ledger_hash: must be 64 hex characters")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		ledger, lookupErr = types.Services.Ledger.GetLedgerByHash(hash)
	} else if request.LedgerIndex != "" {
		ledgerIndexStr := request.LedgerIndex.String()
		switch ledgerIndexStr {
		case "validated":
			seq := types.Services.Ledger.GetValidatedLedgerIndex()
			ledger, lookupErr = types.Services.Ledger.GetLedgerBySequence(seq)
		case "closed":
			seq := types.Services.Ledger.GetClosedLedgerIndex()
			ledger, lookupErr = types.Services.Ledger.GetLedgerBySequence(seq)
		case "current":
			seq := types.Services.Ledger.GetCurrentLedgerIndex()
			ledger, lookupErr = types.Services.Ledger.GetLedgerBySequence(seq)
		default:
			var seq uint32
			if _, scanErr := fmt.Sscanf(ledgerIndexStr, "%d", &seq); scanErr == nil {
				ledger, lookupErr = types.Services.Ledger.GetLedgerBySequence(seq)
			} else {
				return nil, types.RpcErrorInvalidParams("Invalid ledger_index: " + ledgerIndexStr)
			}
		}
	} else {
		// Default to validated
		seq := types.Services.Ledger.GetValidatedLedgerIndex()
		ledger, lookupErr = types.Services.Ledger.GetLedgerBySequence(seq)
	}

	if lookupErr != nil {
		return nil, types.RpcErrorLgrNotFound("Ledger not found: " + lookupErr.Error())
	}

	response := map[string]interface{}{
		"ledger_index": ledger.Sequence(),
		"closed":       ledger.IsClosed(),
	}

	hash := ledger.Hash()
	if hash != [32]byte{} {
		response["ledger_hash"] = fmt.Sprintf("%X", hash)
	}
	parentHash := ledger.ParentHash()
	if parentHash != [32]byte{} {
		response["parent_hash"] = fmt.Sprintf("%X", parentHash)
	}

	return response, nil
}

func (m *LedgerHeaderMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerHeaderMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1}
}

func (m *LedgerHeaderMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
