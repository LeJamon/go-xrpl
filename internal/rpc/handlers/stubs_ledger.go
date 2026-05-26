package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/service/svcerr"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// ownerInfoMaxObjects is the largest page GetAccountObjects serves in one call
// (it caps anything above 400). owner_info has no marker pagination, so request
// the maximum the service allows.
const ownerInfoMaxObjects = 400

// OwnerInfoMethod handles the owner_info RPC method.
// Mirrors rippled OwnerInfo.cpp: returns owner-directory contents for an
// account in both the closed ("accepted") and current ledgers, grouped into
// offers and trust lines (ripple_lines), matching
// NetworkOPsImp::getOwnerInfo.
type OwnerInfoMethod struct{}

func (m *OwnerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Account string `json:"account,omitempty"`
		Ident   string `json:"ident,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	account := request.Account
	if account == "" {
		account = request.Ident
	}
	if account == "" {
		return nil, types.RpcErrorMissingField("account")
	}
	if err := ValidateAccount(account); err != nil {
		return nil, err
	}
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	accepted, rpcErr := ownerInfoSection(ctx, account, "closed")
	if rpcErr != nil {
		return nil, rpcErr
	}
	current, rpcErr := ownerInfoSection(ctx, account, "current")
	if rpcErr != nil {
		return nil, rpcErr
	}

	return map[string]interface{}{
		"accepted": accepted,
		"current":  current,
	}, nil
}

// ownerInfoSection walks the account's owned objects in the given ledger and
// groups offers and trust lines, mirroring rippled's getOwnerInfo. An unfunded
// account yields empty arrays rather than an error, matching rippled's
// behaviour when no owner directory exists.
func ownerInfoSection(ctx *types.RpcContext, account, ledgerIndex string) (map[string]interface{}, *types.RpcError) {
	offers := make([]interface{}, 0)
	rippleLines := make([]interface{}, 0)

	result, err := ctx.Services.Ledger.GetAccountObjects(ctx.Context, account, ledgerIndex, "", ownerInfoMaxObjects)
	if err != nil {
		if errors.Is(err, svcerr.ErrAccountNotFound) {
			return map[string]interface{}{"offers": offers, "ripple_lines": rippleLines}, nil
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get owner info: %v", err))
	}

	for _, obj := range result.AccountObjects {
		switch obj.LedgerEntryType {
		case "Offer":
			offers = append(offers, decodeOwnerObject(obj))
		case "RippleState":
			rippleLines = append(rippleLines, decodeOwnerObject(obj))
		}
	}

	return map[string]interface{}{
		"offers":       offers,
		"ripple_lines": rippleLines,
	}, nil
}

// decodeOwnerObject deserializes a single owned ledger object, falling back to
// raw hex if decoding fails, matching the account_objects handler.
func decodeOwnerObject(obj types.AccountObjectItem) map[string]interface{} {
	hexData := hex.EncodeToString(obj.Data)
	decoded, err := binarycodec.Decode(hexData)
	if err != nil {
		return map[string]interface{}{
			"index":           strings.ToUpper(obj.Index),
			"LedgerEntryType": obj.LedgerEntryType,
			"data":            strings.ToUpper(hexData),
		}
	}
	decoded["index"] = strings.ToUpper(obj.Index)
	return decoded
}

func (m *OwnerInfoMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *OwnerInfoMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *OwnerInfoMethod) RequiredCondition() types.Condition {
	return types.NeedsCurrentLedger
}

// LedgerDiffMethod handles the ledger_diff RPC method.
// STUB: Returns error. Only available via gRPC in rippled.
//
// NOTE: This is gRPC-only in rippled and is NOT available via JSON-RPC.
//
//	It computes the state diff between two ledger versions.
//	This stub exists for completeness but may never need implementation.
type LedgerDiffMethod struct{ AdminHandler }

func (m *LedgerDiffMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	return nil, types.NewRpcError(types.RpcNOT_IMPL, "notImplemented", "notImplemented",
		"ledger_diff is only available via gRPC in rippled — JSON-RPC not supported")
}
