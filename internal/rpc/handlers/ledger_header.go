package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// LedgerHeaderMethod handles the ledger_header RPC method.
// Supports lookup by ledger_index (string/numeric) and ledger_hash.
// Note: This method is deprecated in rippled in favor of 'ledger'.
//
// Reference: rippled/src/xrpld/rpc/handlers/LedgerHeader.cpp
// doLedgerHeader calls lookupLedger, serializes the header via addRaw,
// and returns both ledger_data (binary hex) and a ledger JSON object.
type LedgerHeaderMethod struct{ baseHandler }

func (m *LedgerHeaderMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	var request struct {
	}

	if err := parseParams(params, &request); err != nil {
		return nil, err
	}

	// Resolve the target ledger through the shared lookup (rippled
	// RPC::lookupLedger): defaults to current, threads ledger_hash, and emits
	// rippled's ledgerHashMalformed / ledgerIndexMalformed / ledgerNotFound.
	targetLedger, validated, lerr := lookupLedger(ctx, params)
	if lerr != nil {
		return nil, lerr
	}

	closed := targetLedger.IsClosed()

	// Build top-level response (equivalent to rippled lookupLedger output)
	response := map[string]any{}

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
	txHash, stateHash := ledgerMapHashes(targetLedger)
	ledgerData := ledgerheader.AddRaw(ledgerheader.LedgerHeader{
		LedgerIndex:         targetLedger.Sequence(),
		ParentCloseTime:     protocol.FromRippleTime(uint32(max(targetLedger.ParentCloseTime(), 0))),
		ParentHash:          targetLedger.ParentHash(),
		TxHash:              txHash,
		AccountHash:         stateHash,
		Drops:               targetLedger.TotalDrops(),
		CloseFlags:          targetLedger.CloseFlags(),
		CloseTimeResolution: uint8(targetLedger.CloseTimeResolution()),
		CloseTime:           protocol.FromRippleTime(uint32(max(targetLedger.CloseTime(), 0))),
	}, false)
	response["ledger_data"] = strings.ToUpper(hex.EncodeToString(ledgerData))

	// Build the nested "ledger" JSON object (equivalent to addJson with options=0).
	// Reference: rippled LedgerToJson.cpp fillJson()
	response["ledger"] = buildLedgerHeaderJSON(targetLedger, closed)

	return response, nil
}

// buildLedgerHeaderJSON builds the "ledger" JSON object matching rippled's
// fillJson(json, closed, info, bFull=false, apiVersion=1).
// Reference: rippled/src/xrpld/app/ledger/detail/LedgerToJson.cpp fillJson()
func buildLedgerHeaderJSON(lr types.LedgerReader, closed bool) map[string]any {
	return buildLedgerSummaryJSON(lr, closed, types.ApiVersion1)
}

func buildLedgerSummaryJSON(lr types.LedgerReader, closed bool, apiVersion int) map[string]any {
	ledgerObj := map[string]any{}

	// parent_hash is always present
	parentHash := lr.ParentHash()
	ledgerObj["parent_hash"] = FormatLedgerHash(parentHash)

	if apiVersion > types.ApiVersion1 {
		ledgerObj["ledger_index"] = lr.Sequence()
	} else {
		ledgerObj["ledger_index"] = strconv.FormatUint(uint64(lr.Sequence()), 10)
	}

	if !closed {
		ledgerObj["closed"] = false
		return ledgerObj
	}

	// For closed ledgers, include full header fields
	ledgerObj["closed"] = true

	hash := lr.Hash()
	txHash, stateHash := ledgerMapHashes(lr)

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
		closeTimeUTC := protocol.FromRippleTime(uint32(ct))
		ledgerObj["close_time_human"] = protocol.FormatCloseTimeHuman(closeTimeUTC)
		ledgerObj["close_time_iso"] = protocol.FormatCloseTimeISO(closeTimeUTC)

		// close_time_estimated only when there was no consensus on close time
		if (lr.CloseFlags() & 0x01) != 0 {
			ledgerObj["close_time_estimated"] = true
		}
	}

	return ledgerObj
}

func (m *LedgerHeaderMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1}
}
