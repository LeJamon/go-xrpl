package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ledgerInfoJSON renders the closed-ledger header fields shared by the `ledger`
// and `ledger_request` RPCs, mirroring rippled's LedgerFill at info level 0
// (LedgerToJson.cpp fillJson).
func ledgerInfoJSON(l types.LedgerReader) map[string]interface{} {
	hash := l.Hash()
	parent := l.ParentHash()
	txHash := l.TxMapHash()
	stateHash := l.StateMapHash()
	closeTimeSec := l.CloseTime()
	closeTime := rippleEpochTime.Add(time.Duration(closeTimeSec) * time.Second)
	seqStr := strconv.FormatUint(uint64(l.Sequence()), 10)

	return map[string]interface{}{
		"accepted":              true,
		"account_hash":          strings.ToUpper(hex.EncodeToString(stateHash[:])),
		"close_flags":           l.CloseFlags(),
		"close_time":            closeTimeSec,
		"close_time_human":      closeTime.UTC().Format("2006-Jan-02 15:04:05.000000000 UTC"),
		"close_time_iso":        closeTime.UTC().Format(time.RFC3339),
		"close_time_resolution": l.CloseTimeResolution(),
		"closed":                l.IsClosed(),
		"ledger_hash":           strings.ToUpper(hex.EncodeToString(hash[:])),
		"ledger_index":          seqStr,
		"parent_close_time":     l.ParentCloseTime(),
		"parent_hash":           strings.ToUpper(hex.EncodeToString(parent[:])),
		"seqNum":                seqStr,
		"totalCoins":            strconv.FormatUint(l.TotalDrops(), 10),
		"total_coins":           strconv.FormatUint(l.TotalDrops(), 10),
		"transaction_hash":      strings.ToUpper(hex.EncodeToString(txHash[:])),
	}
}

// LedgerRequestMethod handles the ledger_request RPC method: it returns a
// locally-available ledger, or triggers acquisition of a missing one from
// peers and reports the in-progress acquisition.
//
// Mirrors rippled's getLedgerByContext (RPCHelpers.cpp:1027) / doLedgerRequest
// (LedgerRequest.cpp:36): exactly one of ledger_hash / ledger_index, the
// validated-ledger bounds on a sequence request, a generic
// InboundLedgers::acquire when the ledger isn't local, and the `acquiring`
// snapshot returned alongside lgrNotFound while a fetch is in flight.
type LedgerRequestMethod struct{ AdminHandler }

func (m *LedgerRequestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		LedgerHash  string          `json:"ledger_hash,omitempty"`
		LedgerIndex json.RawMessage `json:"ledger_index,omitempty"`
	}
	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters")
		}
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	hasHash := request.LedgerHash != ""
	hasIndex := len(request.LedgerIndex) > 0
	if (hasHash && hasIndex) || (!hasHash && !hasIndex) {
		return nil, types.RpcErrorInvalidParams("Exactly one of ledger_hash and ledger_index can be set.")
	}

	var targetHash [32]byte
	var targetSeq uint32

	if hasHash {
		hb, err := hex.DecodeString(request.LedgerHash)
		if err != nil || len(hb) != 32 {
			return nil, types.RpcErrorInvalidParams("Invalid field 'ledger_hash'.")
		}
		copy(targetHash[:], hb)

		if l, err := ctx.Services.Ledger.GetLedgerByHash(targetHash); err == nil && l != nil {
			return ledgerRequestSuccess(l), nil
		}
	} else {
		var idx int64
		if err := json.Unmarshal(request.LedgerIndex, &idx); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid field 'ledger_index'.")
		}

		// A sequence request needs a validated ledger to bound it and to
		// resolve the sequence to a hash (rippled's getValidatedLedger gate).
		validatedSeq := ctx.Services.Ledger.GetValidatedLedgerIndex()
		if validatedSeq == 0 {
			return nil, types.NewRpcError(types.RpcNOT_SYNCED, "notSynced", "notSynced",
				"Not synced to the network")
		}
		if idx <= 0 {
			return nil, types.RpcErrorInvalidParams("Ledger index too small")
		}
		if uint32(idx) >= validatedSeq {
			return nil, types.RpcErrorInvalidParams("Ledger index too large")
		}
		targetSeq = uint32(idx)

		if l, err := ctx.Services.Ledger.GetLedgerBySequence(targetSeq); err == nil && l != nil {
			return ledgerRequestSuccess(l), nil
		}
	}

	// Not local — trigger (or join) a generic acquisition from peers. When no
	// acquisition subsystem is wired (standalone / RPC-only) the ledger is
	// simply reported as not found, matching rippled's standalone fallback.
	if ctx.Services.RequestLedger != nil {
		if acquiring, started := ctx.Services.RequestLedger(targetHash, targetSeq); started {
			result := types.RpcErrorLgrNotFound("acquiring ledger containing requested index").ErrorObject()
			if acquiring != nil {
				result["acquiring"] = acquiring
			}
			return result, nil
		}
	}

	return nil, types.RpcErrorLgrNotFound("Ledger not found")
}

// ledgerRequestSuccess builds the success response for a locally-available
// ledger: {ledger_index, ledger} per rippled doLedgerRequest.
func ledgerRequestSuccess(l types.LedgerReader) map[string]interface{} {
	return map[string]interface{}{
		"ledger_index": l.Sequence(),
		"ledger":       ledgerInfoJSON(l),
	}
}
