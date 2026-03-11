package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// rippleEpochTime is 2000-01-01T00:00:00Z
var rippleEpochTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// LedgerMethod handles the ledger RPC method.
type LedgerMethod struct{}

func (m *LedgerMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.LedgerSpecifier
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
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Determine which ledger to retrieve
	var targetLedger types.LedgerReader
	var validated bool
	var err error

	if request.LedgerHash != "" {
		hashBytes, decErr := hex.DecodeString(request.LedgerHash)
		if decErr != nil || len(hashBytes) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid ledger_hash")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		targetLedger, err = types.Services.Ledger.GetLedgerByHash(hash)
		if err != nil {
			return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
		}
		validated = targetLedger.IsValidated()
	} else {
		ledgerIndex := request.LedgerIndex.String()
		if ledgerIndex == "" {
			ledgerIndex = "validated"
		}

		switch ledgerIndex {
		case "validated":
			seq := types.Services.Ledger.GetValidatedLedgerIndex()
			if seq == 0 {
				return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "No validated ledger"}
			}
			targetLedger, err = types.Services.Ledger.GetLedgerBySequence(seq)
			validated = true
		case "current":
			seq := types.Services.Ledger.GetCurrentLedgerIndex()
			targetLedger, err = types.Services.Ledger.GetLedgerBySequence(seq)
			validated = false
		case "closed":
			seq := types.Services.Ledger.GetClosedLedgerIndex()
			targetLedger, err = types.Services.Ledger.GetLedgerBySequence(seq)
			validated = targetLedger != nil && targetLedger.IsValidated()
		default:
			seq, parseErr := strconv.ParseUint(ledgerIndex, 10, 32)
			if parseErr != nil {
				return nil, types.RpcErrorInvalidParams("Invalid ledger_index")
			}
			targetLedger, err = types.Services.Ledger.GetLedgerBySequence(uint32(seq))
			if err != nil {
				return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
			}
			validated = targetLedger.IsValidated()
		}
	}

	if err != nil || targetLedger == nil {
		return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "Ledger not found"}
	}

	// Build ledger info
	hash := targetLedger.Hash()
	parent := targetLedger.ParentHash()
	txHash := targetLedger.TxMapHash()
	stateHash := targetLedger.StateMapHash()
	ledgerHash := strings.ToUpper(hex.EncodeToString(hash[:]))
	parentHash := strings.ToUpper(hex.EncodeToString(parent[:]))
	txHashStr := strings.ToUpper(hex.EncodeToString(txHash[:]))
	stateHashStr := strings.ToUpper(hex.EncodeToString(stateHash[:]))

	// Format close time
	closeTimeSec := targetLedger.CloseTime()
	closeTime := rippleEpochTime.Add(time.Duration(closeTimeSec) * time.Second)
	closeTimeHuman := closeTime.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC")
	closeTimeISO := closeTime.UTC().Format(time.RFC3339)

	// Get reserve info
	_, reserveBase, reserveInc := types.Services.Ledger.GetCurrentFees()

	ledgerInfo := map[string]interface{}{
		"accepted":              true,
		"account_hash":          stateHashStr,
		"close_flags":           targetLedger.CloseFlags(),
		"close_time":            closeTimeSec,
		"close_time_human":      closeTimeHuman,
		"close_time_iso":        closeTimeISO,
		"close_time_resolution": targetLedger.CloseTimeResolution(),
		"closed":                targetLedger.IsClosed(),
		"hash":                  ledgerHash,
		"ledger_hash":           ledgerHash,
		"ledger_index":          strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"parent_close_time":     targetLedger.ParentCloseTime(),
		"parent_hash":           parentHash,
		"seqNum":                strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"totalCoins":            strconv.FormatUint(targetLedger.TotalDrops(), 10),
		"total_coins":           strconv.FormatUint(targetLedger.TotalDrops(), 10),
		"transaction_hash":      txHashStr,
	}

	// Add transactions if requested
	if request.Transactions {
		var txList []interface{}
		targetLedger.ForEachTransaction(func(txHashKey [32]byte, txData []byte) bool {
			hashStr := strings.ToUpper(hex.EncodeToString(txHashKey[:]))
			if request.Expand {
				// Return full transaction objects
				if request.Binary {
					txList = append(txList, map[string]interface{}{
						"tx_blob": strings.ToUpper(hex.EncodeToString(txData)),
						"hash":    hashStr,
					})
				} else {
					// Decode transaction blob to JSON
					txHex := hex.EncodeToString(txData)
					decoded, err := binarycodec.Decode(txHex)
					if err != nil {
						txList = append(txList, map[string]interface{}{
							"hash":    hashStr,
							"tx_blob": strings.ToUpper(txHex),
						})
					} else {
						decoded["hash"] = hashStr
						txList = append(txList, decoded)
					}
				}
			} else {
				// Return just transaction hashes
				txList = append(txList, hashStr)
			}
			return true
		})
		if txList == nil {
			txList = []interface{}{}
		}
		ledgerInfo["transactions"] = txList
	}

	response := map[string]interface{}{
		"ledger":       ledgerInfo,
		"ledger_hash":  ledgerHash,
		"ledger_index": targetLedger.Sequence(),
		"validated":    validated,
	}

	// Add reserve info at top level
	response["reserve_base_drops"] = fmt.Sprintf("%d", reserveBase)
	response["reserve_inc_drops"] = fmt.Sprintf("%d", reserveInc)

	// Add queue data if requested
	if request.Queue {
		response["queue_data"] = []interface{}{}
	}

	return response, nil
}

func (m *LedgerMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *LedgerMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
