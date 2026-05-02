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
type LedgerMethod struct{ BaseHandler }

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

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
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
		targetLedger, err = ctx.Services.Ledger.GetLedgerByHash(hash)
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
			seq := ctx.Services.Ledger.GetValidatedLedgerIndex()
			if seq == 0 {
				return nil, &types.RpcError{Code: -1, ErrorString: "lgrNotFound", Message: "No validated ledger"}
			}
			targetLedger, err = ctx.Services.Ledger.GetLedgerBySequence(seq)
			validated = true
		case "current":
			seq := ctx.Services.Ledger.GetCurrentLedgerIndex()
			targetLedger, err = ctx.Services.Ledger.GetLedgerBySequence(seq)
			validated = false
		case "closed":
			seq := ctx.Services.Ledger.GetClosedLedgerIndex()
			targetLedger, err = ctx.Services.Ledger.GetLedgerBySequence(seq)
			validated = targetLedger != nil && targetLedger.IsValidated()
		default:
			seq, parseErr := strconv.ParseUint(ledgerIndex, 10, 32)
			if parseErr != nil {
				return nil, types.RpcErrorInvalidParams("Invalid ledger_index")
			}
			targetLedger, err = ctx.Services.Ledger.GetLedgerBySequence(uint32(seq))
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

	_, reserveBase, reserveInc := ctx.Services.Ledger.GetCurrentFees()

	ledgerInfo := map[string]interface{}{
		"accepted":              true,
		"account_hash":          stateHashStr,
		"close_flags":           targetLedger.CloseFlags(),
		"close_time":            closeTimeSec,
		"close_time_human":      closeTimeHuman,
		"close_time_iso":        closeTimeISO,
		"close_time_resolution": targetLedger.CloseTimeResolution(),
		"closed":                targetLedger.IsClosed(),
		"ledger_hash":           ledgerHash,
		"ledger_index":          strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"parent_close_time":     targetLedger.ParentCloseTime(),
		"parent_hash":           parentHash,
		"seqNum":                strconv.FormatUint(uint64(targetLedger.Sequence()), 10),
		"totalCoins":            strconv.FormatUint(targetLedger.TotalDrops(), 10),
		"total_coins":           strconv.FormatUint(targetLedger.TotalDrops(), 10),
		"transaction_hash":      txHashStr,
	}

	if request.Transactions {
		var txList []interface{}
		apiVersion := ctx.ApiVersion
		targetLedger.ForEachTransaction(func(txHashKey [32]byte, txData []byte) bool {
			hashStr := strings.ToUpper(hex.EncodeToString(txHashKey[:]))
			if request.Expand {
				txEntry := expandTransaction(txData, hashStr, request.Binary, apiVersion)
				// Add per-entry context fields for v2+
				if apiVersion > 1 && !request.Binary {
					if targetLedger.IsClosed() {
						txEntry["ledger_hash"] = ledgerHash
					}
					txEntry["validated"] = validated
					if validated {
						txEntry["ledger_index"] = targetLedger.Sequence()
						if closeTimeSec > 0 {
							txEntry["close_time_iso"] = closeTimeISO
						}
					}
				}
				txList = append(txList, txEntry)
			} else {
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

	response["reserve_base_drops"] = fmt.Sprintf("%d", reserveBase)
	response["reserve_inc_drops"] = fmt.Sprintf("%d", reserveInc)

	if request.Queue {
		response["queue_data"] = []interface{}{}
	}

	return response, nil
}

// expandTransaction builds an expanded transaction object from raw txData.
// It handles VL-encoded binary blobs and JSON StoredTransaction format.
//
// The output format varies by API version:
//   - API v1: tx fields at top level + "metaData" for metadata
//   - API v2+: "tx_json" key + "meta" key + "hash"
//
// For binary mode, tx_blob and meta_blob/meta are returned as hex strings.
// Reference: rippled LedgerToJson.cpp fillJsonTx()
func expandTransaction(txData []byte, hashStr string, binary bool, apiVersion int) map[string]interface{} {
	storedTx, err := decodeTxBlob(txData)
	if err == nil && storedTx.TxJSON != nil {
		return expandStoredTransaction(storedTx, hashStr, binary, apiVersion)
	}

	// Cannot decode: return raw blob
	txEntry := map[string]interface{}{}
	txEntry["tx_blob"] = strings.ToUpper(hex.EncodeToString(txData))
	txEntry["hash"] = hashStr
	return txEntry
}

// expandStoredTransaction formats a JSON-stored transaction for the response.
func expandStoredTransaction(storedTx StoredTransaction, hashStr string, binary bool, apiVersion int) map[string]interface{} {
	txEntry := map[string]interface{}{}

	if binary {
		// Encode tx_json back to binary hex
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			txEntry["tx_blob"] = txBlob
		}
		txEntry["hash"] = hashStr
		// Encode metadata to binary hex
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				if apiVersion > 1 {
					txEntry["meta_blob"] = metaBlob
				} else {
					txEntry["meta"] = metaBlob
				}
			}
		}
		return txEntry
	}

	if apiVersion > 1 {
		// API v2+: use tx_json and meta keys
		txEntry["tx_json"] = storedTx.TxJSON
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			txEntry["meta"] = storedTx.Meta
		}
	} else {
		// API v1: flatten tx fields at top level, metadata under "metaData"
		for k, v := range storedTx.TxJSON {
			txEntry[k] = v
		}
		txEntry["hash"] = hashStr
		if storedTx.Meta != nil {
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			txEntry["metaData"] = storedTx.Meta
		}
	}
	return txEntry
}
