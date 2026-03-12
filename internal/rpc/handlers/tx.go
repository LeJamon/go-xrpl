package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// TxMethod handles the tx RPC method
type TxMethod struct{}

func (m *TxMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		types.TransactionParam
		Binary    bool   `json:"binary,omitempty"`
		MinLedger uint32 `json:"min_ledger,omitempty"`
		MaxLedger uint32 `json:"max_ledger,omitempty"`
		CTID      string `json:"ctid,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// CTID lookup support
	if request.CTID != "" && request.Transaction == "" {
		ctidLedgerSeq, ctidTxIndex, err := parseCTID(request.CTID)
		if err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid ctid: " + err.Error())
		}
		return m.lookupByCTID(ctidLedgerSeq, ctidTxIndex, request.Binary)
	}

	if request.Transaction == "" {
		return nil, types.RpcErrorInvalidParams("Missing required parameter: transaction")
	}

	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	// Parse the transaction hash
	txHashBytes, err := hex.DecodeString(request.Transaction)
	if err != nil || len(txHashBytes) != 32 {
		return nil, types.RpcErrorInvalidParams("Invalid transaction hash")
	}

	var txHash [32]byte
	copy(txHash[:], txHashBytes)

	// Look up the transaction
	txInfo, err := types.Services.Ledger.GetTransaction(txHash)
	if err != nil {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found",
		}
	}

	// Parse the stored transaction data
	var storedTx StoredTransaction
	if err := json.Unmarshal(txInfo.TxData, &storedTx); err != nil {
		return nil, types.RpcErrorInternal("Failed to parse transaction data")
	}

	// Build the response
	response := map[string]interface{}{}

	if request.Binary {
		// Encode transaction to binary
		txBlob, err := binarycodec.Encode(storedTx.TxJSON)
		if err == nil {
			response["tx_blob"] = txBlob
		}
		if storedTx.Meta != nil {
			metaBlob, err := binarycodec.Encode(storedTx.Meta)
			if err == nil {
				response["meta"] = metaBlob
			}
		}
	} else {
		// Return JSON format
		for k, v := range storedTx.TxJSON {
			response[k] = v
		}
		if storedTx.Meta != nil {
			// Inject DeliveredAmount for Payment transactions
			InjectDeliveredAmount(storedTx.TxJSON, storedTx.Meta)
			response["meta"] = storedTx.Meta
		}
	}

	// Add ledger info
	response["hash"] = request.Transaction
	response["inLedger"] = txInfo.LedgerIndex
	response["ledger_index"] = txInfo.LedgerIndex
	response["ledger_hash"] = txInfo.LedgerHash
	response["validated"] = txInfo.Validated

	// Add close_time_iso from the containing ledger
	if txInfo.LedgerIndex > 0 {
		if ledger, err := types.Services.Ledger.GetLedgerBySequence(txInfo.LedgerIndex); err == nil {
			closeTimeSec := ledger.CloseTime()
			if closeTimeSec > 0 {
				closeTime := rippleEpochTime.Add(secondsToDuration(closeTimeSec))
				response["close_time_iso"] = closeTime.UTC().Format("2006-01-02T15:04:05Z")
			}
		}
	}

	return response, nil
}

// lookupByCTID looks up a transaction using a CTID (Compact Transaction ID)
func (m *TxMethod) lookupByCTID(ledgerSeq uint32, txIndex uint16, binary bool) (interface{}, *types.RpcError) {
	if err := RequireLedgerService(); err != nil {
		return nil, err
	}

	ledger, err := types.Services.Ledger.GetLedgerBySequence(ledgerSeq)
	if err != nil {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found (ledger not available)",
		}
	}

	// Iterate transactions to find the one at the given index
	var foundHash [32]byte
	var foundData []byte
	var currentIdx uint16
	var found bool

	ledger.ForEachTransaction(func(txHash [32]byte, txData []byte) bool {
		if currentIdx == txIndex {
			foundHash = txHash
			foundData = make([]byte, len(txData))
			copy(foundData, txData)
			found = true
			return false // stop iteration
		}
		currentIdx++
		return true
	})

	if !found {
		return nil, &types.RpcError{
			Code:        -1,
			ErrorString: "txnNotFound",
			Message:     "Transaction not found at specified index",
		}
	}

	response := map[string]interface{}{
		"hash":         hex.EncodeToString(foundHash[:]),
		"ledger_index": ledgerSeq,
		"validated":    ledger.IsValidated(),
	}

	if binary {
		response["tx_blob"] = hex.EncodeToString(foundData)
	} else {
		txHex := hex.EncodeToString(foundData)
		decoded, err := binarycodec.Decode(txHex)
		if err == nil {
			for k, v := range decoded {
				response[k] = v
			}
		}
	}

	// Add CTID to response
	response["ctid"] = encodeCTID(ledgerSeq, txIndex)

	return response, nil
}

// parseCTID decodes a CTID hex string to ledger sequence and tx index
// CTID format: 8 hex chars = 0xCLLLLLTT where C=0xC (marker), L=ledger_seq (28 bits), T=tx_index (16 bits)
func parseCTID(ctid string) (uint32, uint16, error) {
	if len(ctid) != 16 {
		return 0, 0, fmt.Errorf("CTID must be 16 hex characters")
	}
	bytes, err := hex.DecodeString(ctid)
	if err != nil || len(bytes) != 8 {
		return 0, 0, fmt.Errorf("invalid CTID hex")
	}

	// Validate marker nibble (high 4 bits should be 0xC)
	if bytes[0]>>4 != 0xC {
		return 0, 0, fmt.Errorf("invalid CTID marker")
	}

	// Extract network_id (ignored for now), ledger_seq, tx_index
	// Format: CCCCCCCC NNNNNNNN LLLLLLLL LLLLLLLL LLLLLLLL LLLLLLLL TTTTTTTT TTTTTTTT
	// Actually CTID is: 0xCNNNLLLLTTTT (C=marker, N=network_id 12 bits, L=ledger_seq 32 bits, T=tx_index 16 bits)
	// Simplified: bytes 0-3 contain marker+network+ledger, bytes 4-5 contain nothing, bytes 6-7 contain tx_index
	// The real format is: uint64 where bits [63:60]=0xC, [59:48]=network_id, [47:16]=ledger_seq, [15:0]=tx_index

	val := uint64(0)
	for _, b := range bytes {
		val = (val << 8) | uint64(b)
	}

	txIndex := uint16(val & 0xFFFF)
	ledgerSeq := uint32((val >> 16) & 0xFFFFFFFF)

	return ledgerSeq, txIndex, nil
}

// encodeCTID encodes ledger sequence and tx index into a CTID hex string
func encodeCTID(ledgerSeq uint32, txIndex uint16) string {
	// Network ID 0 for now
	val := uint64(0xC)<<60 | uint64(0)<<48 | uint64(ledgerSeq)<<16 | uint64(txIndex)
	return fmt.Sprintf("%016X", val)
}

// secondsToDuration converts ripple epoch seconds to a time.Duration
func secondsToDuration(secs int64) time.Duration {
	return time.Duration(secs) * time.Second
}

// StoredTransaction represents a transaction stored in the ledger
type StoredTransaction struct {
	TxJSON map[string]interface{} `json:"tx_json"`
	Meta   map[string]interface{} `json:"meta"`
}

func (m *TxMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *TxMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *TxMethod) RequiredCondition() types.Condition {
	return types.NeedsNetworkConnection
}
