package rpc_handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// LedgerMethod handles the ledger RPC method.
// PARTIAL: Ledger header info works. Missing:
//
// TODO [ledger]: Populate transactions list when transactions=true.
//   - Requires: LedgerService.GetLedgerTransactions(seq) returning tx hashes
//     (or full tx+meta if expand=true)
//   - When expand=false: return array of transaction hash strings
//   - When expand=true: return array of full tx_json objects with metadata
//   - When binary=true + expand=true: return tx_blob + meta hex strings
//   - Reference: rippled LedgerHandler.cpp lines 200-300
//
// TODO [ledger]: Populate accounts/state when accounts=true or full=true.
//   - Requires: LedgerService.GetLedgerData() (already exists for ledger_data RPC)
//   - When full=true: include all state objects and transactions
//   - Reference: rippled LedgerHandler.cpp
type LedgerMethod struct{}

func (m *LedgerMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	// Parse parameters
	var request struct {
		rpc_types.LedgerSpecifier
		Accounts     bool `json:"accounts,omitempty"`
		Full         bool `json:"full,omitempty"`
		Transactions bool `json:"transactions,omitempty"`
		Expand       bool `json:"expand,omitempty"`
		OwnerFunds   bool `json:"owner_funds,omitempty"`
		Binary       bool `json:"binary,omitempty"`
		Queue        bool `json:"queue,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	// Check if ledger service is available
	if rpc_types.Services == nil || rpc_types.Services.Ledger == nil {
		return nil, rpc_types.RpcErrorInternal("Ledger service not available")
	}

	// Determine which ledger to retrieve
	var targetLedger rpc_types.LedgerReader
	var validated bool
	var err error

	if request.LedgerHash != "" {
		// Look up by hash
		hashBytes, decErr := hex.DecodeString(request.LedgerHash)
		if decErr != nil || len(hashBytes) != 32 {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid ledger_hash")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		targetLedger, err = rpc_types.Services.Ledger.GetLedgerByHash(hash)
		if err != nil {
			return nil, &rpc_types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
		}
		validated = targetLedger.IsValidated()
	} else {
		// Look up by index
		ledgerIndex := request.LedgerIndex.String()
		if ledgerIndex == "" {
			ledgerIndex = "validated"
		}

		switch ledgerIndex {
		case "validated":
			seq := rpc_types.Services.Ledger.GetValidatedLedgerIndex()
			if seq == 0 {
				return nil, &rpc_types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "No validated ledger"}
			}
			targetLedger, err = rpc_types.Services.Ledger.GetLedgerBySequence(seq)
			validated = true
		case "current":
			seq := rpc_types.Services.Ledger.GetCurrentLedgerIndex()
			targetLedger, err = rpc_types.Services.Ledger.GetLedgerBySequence(seq)
			validated = false
		case "closed":
			seq := rpc_types.Services.Ledger.GetClosedLedgerIndex()
			targetLedger, err = rpc_types.Services.Ledger.GetLedgerBySequence(seq)
			validated = targetLedger != nil && targetLedger.IsValidated()
		default:
			// Parse as number
			seq, parseErr := strconv.ParseUint(ledgerIndex, 10, 32)
			if parseErr != nil {
				return nil, rpc_types.RpcErrorInvalidParams("Invalid ledger_index")
			}
			targetLedger, err = rpc_types.Services.Ledger.GetLedgerBySequence(uint32(seq))
			if err != nil {
				return nil, &rpc_types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
			}
			validated = targetLedger.IsValidated()
		}
	}

	if err != nil || targetLedger == nil {
		return nil, &rpc_types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
	}

	// Build ledger info
	hash := targetLedger.Hash()
	parent := targetLedger.ParentHash()
	ledgerHash := hex.EncodeToString(hash[:])
	parentHash := hex.EncodeToString(parent[:])

	ledgerInfo := map[string]interface{}{
		"accepted":     true,
		"close_flags":  0,
		"closed":       targetLedger.IsClosed(),
		"hash":         ledgerHash,
		"ledger_hash":  ledgerHash,
		"ledger_index": strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"parent_hash":  parentHash,
		"seqNum":       strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"totalCoins":   strconv.FormatUint(targetLedger.TotalDrops(), 10),
		"total_coins":  strconv.FormatUint(targetLedger.TotalDrops(), 10),
	}

	response := map[string]interface{}{
		"ledger":       ledgerInfo,
		"ledger_hash":  ledgerHash,
		"ledger_index": targetLedger.Sequence(),
		"validated":    validated,
	}

	// TODO [ledger]: populate with real transactions (see type-level TODO)
	if request.Transactions {
		response["ledger"].(map[string]interface{})["transactions"] = []interface{}{}
	}

	// Add queue if requested and this is current ledger
	if request.Queue {
		response["queue_data"] = []interface{}{}
	}

	return response, nil
}

func (m *LedgerMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *LedgerMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
