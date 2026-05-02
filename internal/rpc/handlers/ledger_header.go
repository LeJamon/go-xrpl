package handlers

import (
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// LedgerHeaderMethod handles the ledger_header RPC method.
// Supports lookup by ledger_index (string/numeric) and ledger_hash.
// Note: This method is deprecated in rippled in favor of 'ledger'.
//
// Reference: rippled/src/xrpld/rpc/handlers/LedgerHeader.cpp
// doLedgerHeader calls lookupLedger, serializes the header via addRaw,
// and returns both ledger_data (binary hex) and a ledger JSON object.
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Resolve the target ledger (equivalent to rippled's lookupLedger)
	var targetLedger types.LedgerReader
	var lookupErr error

	if request.LedgerHash != "" {
		hashBytes, err := hex.DecodeString(request.LedgerHash)
		if err != nil || len(hashBytes) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid ledger_hash: must be 64 hex characters")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerByHash(hash)
	} else if request.LedgerIndex != "" {
		ledgerIndexStr := request.LedgerIndex.String()
		switch ledgerIndexStr {
		case "validated":
			seq := ctx.Services.Ledger.GetValidatedLedgerIndex()
			targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerBySequence(seq)
		case "closed":
			seq := ctx.Services.Ledger.GetClosedLedgerIndex()
			targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerBySequence(seq)
		case "current":
			seq := ctx.Services.Ledger.GetCurrentLedgerIndex()
			targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerBySequence(seq)
		default:
			var seq uint32
			if _, scanErr := fmt.Sscanf(ledgerIndexStr, "%d", &seq); scanErr == nil {
				targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerBySequence(seq)
			} else {
				return nil, types.RpcErrorInvalidParams("Invalid ledger_index: " + ledgerIndexStr)
			}
		}
	} else {
		// Default to validated (rippled defaults to current via lookupLedger,
		// but for ledger_header the common usage is validated)
		seq := ctx.Services.Ledger.GetValidatedLedgerIndex()
		targetLedger, lookupErr = ctx.Services.Ledger.GetLedgerBySequence(seq)
	}

	if lookupErr != nil || targetLedger == nil {
		return nil, types.RpcErrorLgrNotFound("Ledger not found")
	}

	closed := targetLedger.IsClosed()
	validated := targetLedger.IsValidated()

	// Build top-level response (equivalent to rippled lookupLedger output)
	response := map[string]interface{}{}

	if closed {
		hash := targetLedger.Hash()
		response["ledger_hash"] = FormatLedgerHash(hash)
		response["ledger_index"] = targetLedger.Sequence()
	} else {
		response["ledger_current_index"] = targetLedger.Sequence()
	}
	response["validated"] = validated

	// Serialize header to binary hex (rippled addRaw format).
	// In rippled's doLedgerHeader, this is always set (even for open ledgers).
	// Format matches rippled/src/libxrpl/protocol/LedgerHeader.cpp addRaw():
	//   uint32 seq, uint64 drops, hash256 parentHash, hash256 txHash,
	//   hash256 accountHash, uint32 parentCloseTime, uint32 closeTime,
	//   uint8 closeTimeResolution, uint8 closeFlags
	ledgerData := serializeLedgerHeader(targetLedger)
	response["ledger_data"] = strings.ToUpper(hex.EncodeToString(ledgerData))

	// Build the nested "ledger" JSON object (equivalent to addJson with options=0).
	// Reference: rippled LedgerToJson.cpp fillJson()
	ledgerObj := buildLedgerHeaderJSON(targetLedger, closed)
	response["ledger"] = ledgerObj

	return response, nil
}

// serializeLedgerHeader serializes a ledger header to binary in rippled's addRaw format.
// This matches rippled/src/libxrpl/protocol/LedgerHeader.cpp addRaw(info, s, false).
// Field layout (big-endian):
//
//	 4B  seq (uint32)
//	 8B  drops (uint64)
//	32B  parentHash
//	32B  txHash
//	32B  accountHash
//	 4B  parentCloseTime (uint32, ripple epoch seconds)
//	 4B  closeTime (uint32, ripple epoch seconds)
//	 1B  closeTimeResolution (uint8)
//	 1B  closeFlags (uint8)
//
// Total: 118 bytes
func serializeLedgerHeader(lr types.LedgerReader) []byte {
	buf := make([]byte, 0, 118)

	// seq
	var tmp4 [4]byte
	binary.BigEndian.PutUint32(tmp4[:], lr.Sequence())
	buf = append(buf, tmp4[:]...)

	// drops
	var tmp8 [8]byte
	binary.BigEndian.PutUint64(tmp8[:], lr.TotalDrops())
	buf = append(buf, tmp8[:]...)

	// parentHash
	parentHash := lr.ParentHash()
	buf = append(buf, parentHash[:]...)

	// txHash
	txHash := lr.TxMapHash()
	buf = append(buf, txHash[:]...)

	// accountHash
	stateHash := lr.StateMapHash()
	buf = append(buf, stateHash[:]...)

	// parentCloseTime (uint32, ripple epoch seconds)
	pct := lr.ParentCloseTime()
	if pct < 0 {
		pct = 0
	}
	binary.BigEndian.PutUint32(tmp4[:], uint32(pct))
	buf = append(buf, tmp4[:]...)

	// closeTime (uint32, ripple epoch seconds)
	ct := lr.CloseTime()
	if ct < 0 {
		ct = 0
	}
	binary.BigEndian.PutUint32(tmp4[:], uint32(ct))
	buf = append(buf, tmp4[:]...)

	// closeTimeResolution (uint8) - rippled stores this as 1 byte
	buf = append(buf, uint8(lr.CloseTimeResolution()))

	// closeFlags (uint8)
	buf = append(buf, lr.CloseFlags())

	return buf
}

// buildLedgerHeaderJSON builds the "ledger" JSON object matching rippled's
// fillJson(json, closed, info, bFull=false, apiVersion=1).
// Reference: rippled/src/xrpld/app/ledger/detail/LedgerToJson.cpp fillJson()
func buildLedgerHeaderJSON(lr types.LedgerReader, closed bool) map[string]interface{} {
	ledgerObj := map[string]interface{}{}

	// parent_hash is always present
	parentHash := lr.ParentHash()
	ledgerObj["parent_hash"] = FormatLedgerHash(parentHash)

	// ledger_index as string (API v1 behavior, which is the only version this method supports)
	ledgerObj["ledger_index"] = strconv.FormatUint(uint64(lr.Sequence()), 10)

	if !closed {
		ledgerObj["closed"] = false
		return ledgerObj
	}

	// For closed ledgers, include full header fields
	ledgerObj["closed"] = true

	hash := lr.Hash()
	txHash := lr.TxMapHash()
	stateHash := lr.StateMapHash()

	ledgerObj["ledger_hash"] = FormatLedgerHash(hash)
	ledgerObj["transaction_hash"] = FormatLedgerHash(txHash)
	ledgerObj["account_hash"] = FormatLedgerHash(stateHash)
	ledgerObj["total_coins"] = strconv.FormatUint(lr.TotalDrops(), 10)

	ledgerObj["close_flags"] = lr.CloseFlags()

	// Fields that contribute to the ledger hash (always shown)
	pct := lr.ParentCloseTime()
	ct := lr.CloseTime()
	ledgerObj["parent_close_time"] = pct
	ledgerObj["close_time"] = ct
	ledgerObj["close_time_resolution"] = lr.CloseTimeResolution()

	// close_time_human and close_time_iso only when closeTime > 0
	if ct > 0 {
		closeTimeUTC := rippleEpochTime.Add(time.Duration(ct) * time.Second)
		ledgerObj["close_time_human"] = closeTimeUTC.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC")
		ledgerObj["close_time_iso"] = closeTimeUTC.UTC().Format(time.RFC3339)

		// close_time_estimated only when there was no consensus on close time
		if (lr.CloseFlags() & 0x01) != 0 {
			ledgerObj["close_time_estimated"] = true
		}
	}

	return ledgerObj
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
