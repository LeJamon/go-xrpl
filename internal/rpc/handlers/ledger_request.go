package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

// ledgerInfoJSON renders the closed-ledger header fields shared by the `ledger`
// and `ledger_request` RPCs, mirroring rippled's LedgerFill at info level 0
// (LedgerToJson.cpp fillJson).
func ledgerInfoJSON(l types.LedgerReader) map[string]any {
	hash := l.Hash()
	parent := l.ParentHash()
	txHash, stateHash := ledgerMapHashes(l)
	closeTimeSec := l.CloseTime()
	seqStr := strconv.FormatUint(uint64(l.Sequence()), 10)

	result := map[string]any{
		"accepted":              true,
		"account_hash":          strings.ToUpper(hex.EncodeToString(stateHash[:])),
		"close_flags":           l.CloseFlags(),
		"close_time":            closeTimeSec,
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
	if closeTimeSec > 0 {
		closeTime := protocol.FromRippleTime(uint32(closeTimeSec))
		result["close_time_human"] = protocol.FormatCloseTimeHuman(closeTime)
		result["close_time_iso"] = protocol.FormatCloseTimeISO(closeTime)
	}
	return result
}

// LedgerRequestMethod handles the ledger_request RPC method: it returns a
// locally-available ledger, or triggers acquisition of a missing one from
// peers and reports the in-progress acquisition.
//
// Mirrors rippled's getLedgerByContext (RPCHelpers.cpp:1027) / doLedgerRequest
// (LedgerRequest.cpp:36): exactly one of ledger_hash / ledger_index, the
// validated-ledger bounds on a sequence request, and a generic
// InboundLedgers::acquire when the ledger isn't local. While a fetch is in
// flight it returns the bare acquisition snapshot for the target ledger, or
// lgrNotFound + acquiring when a reference ledger is being fetched first to
// resolve a deep sequence's hash.
type LedgerRequestMethod struct{ adminHandler }

func (m *LedgerRequestMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *rpcerrors.RpcError) {
	probe := map[string]json.RawMessage{}
	if params != nil {
		if err := json.Unmarshal(params, &probe); err != nil {
			return nil, rpcerrors.RpcErrorInvalidParams("Invalid parameters")
		}
	}

	if err := requireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// rippled getOrAcquireLedger (RPCLedgerHelpers.cpp:396-440): presence is
	// isMember, so an empty or null selector still counts as given.
	rawHash, hasHash := probe["ledger_hash"]
	rawIndex, hasIndex := probe["ledger_index"]
	if hasHash == hasIndex {
		return nil, rpcerrors.RpcErrorInvalidParams("Exactly one of 'ledger_hash' or 'ledger_index' can be specified.")
	}
	var targetHash [32]byte
	var targetSeq uint32

	if hasHash {
		var hashString string
		if err := json.Unmarshal(rawHash, &hashString); err != nil || !isJSONString(rawHash) {
			return nil, rpcerrors.RpcErrorExpectedField("ledger_hash", "hex string")
		}
		hb, err := hex.DecodeString(hashString)
		if err != nil || len(hb) != 32 {
			return nil, rpcerrors.RpcErrorExpectedField("ledger_hash", "hex string")
		}
		copy(targetHash[:], hb)

		l, err := getLedgerByHashContext(ctx.Context, ctx.Services.Ledger(), targetHash)
		if err == nil && l != nil {
			return ledgerRequestSuccess(l), nil
		}
		if err != nil && !errors.Is(err, svcerr.ErrLedgerNotFound) {
			return nil, rpcInternalError("ledger_request: hash lookup failed", err)
		}
	} else {
		var idx int64
		if err := json.Unmarshal(rawIndex, &idx); err != nil || isJSONNull(rawIndex) {
			return nil, rpcerrors.RpcErrorExpectedField("ledger_index", "number")
		}

		// A sequence request needs a validated ledger to bound it and to
		// resolve the sequence to a hash (rippled's getValidatedLedger gate).
		// rippled distinguishes API v1 (rpcNO_CURRENT) from later versions
		// (rpcNOT_SYNCED) here.
		validatedSeq := ctx.Services.Ledger().GetValidatedLedgerIndex()
		if validatedSeq == 0 {
			if ctx.ApiVersion == types.ApiVersion1 {
				return nil, rpcerrors.NewRpcError(rpcerrors.RpcNO_CURRENT, "noCurrent", "noCurrent",
					"Current ledger is unavailable.")
			}
			return nil, rpcerrors.NewRpcError(rpcerrors.RpcNOT_SYNCED, "notSynced", "notSynced",
				"Not synced to the network")
		}
		// rippled stores asInt() in a uint32 and checks "too large" first,
		// so a negative index wraps and is reported as too large.
		if idx < 0 || idx > math.MaxUint32 || uint32(idx) >= validatedSeq {
			return nil, rpcerrors.RpcErrorInvalidParams("Ledger index too large")
		}
		if idx == 0 {
			return nil, rpcerrors.RpcErrorInvalidParams("Ledger index too small")
		}
		targetSeq = uint32(idx)

		if l, err := ctx.Services.Ledger().GetLedgerBySequence(targetSeq); err == nil && l != nil {
			return ledgerRequestSuccess(l), nil
		}
	}

	// Not local — trigger (or join) a generic acquisition from peers. When no
	// acquisition subsystem is wired (standalone / RPC-only) the ledger is
	// simply reported as not found, matching rippled's standalone fallback.
	if ctx.Services.RequestLedger() != nil {
		if acquiring, started, reference := ctx.Services.RequestLedger()(targetHash, targetSeq); started {
			if reference {
				// Acquiring a reference ledger only to learn the target's hash:
				// rippled wraps the snapshot as lgrNotFound + acquiring
				// (RPCHelpers.cpp:1096-1110).
				result := rpcerrors.RpcErrorLgrNotFound("acquiring ledger containing requested index").ErrorObject()
				if acquiring != nil {
					result["acquiring"] = acquiring
				}
				return result, nil
			}
			// Acquiring the target itself: rippled returns the bare acquisition
			// snapshot as the result (RPCHelpers.cpp:1137-1138).
			return acquiring, nil
		}
	}

	return nil, rpcerrors.RpcErrorLgrNotFound("Ledger not found")
}

// ledgerRequestSuccess builds the success response for a locally-available
// ledger: {ledger_index, ledger} per rippled doLedgerRequest.
func ledgerRequestSuccess(l types.LedgerReader) map[string]any {
	return map[string]any{
		"ledger_index": l.Sequence(),
		"ledger":       ledgerInfoJSON(l),
	}
}
