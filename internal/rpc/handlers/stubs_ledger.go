package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// OwnerInfoMethod handles the owner_info RPC method.
// Mirrors rippled OwnerInfo.cpp: returns owner-directory contents for an
// account in both the closed ("accepted") and current ledgers, grouped into
// offers and trust lines (ripple_lines), matching
// NetworkOPsImp::getOwnerInfo.
type OwnerInfoMethod struct{}

func (m *OwnerInfoMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		Account *string `json:"account"`
		Ident   *string `json:"ident"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
		}
	}

	// rippled OwnerInfo.cpp:37-41 — missing only when neither account nor
	// ident is present. A present-but-empty value is handled below.
	if request.Account == nil && request.Ident == nil {
		return nil, types.RpcErrorMissingField("account")
	}
	strIdent := ""
	if request.Account != nil {
		strIdent = *request.Account
	} else {
		strIdent = *request.Ident
	}

	// rippled OwnerInfo.cpp:50-58 — parseBase58<AccountID> (classic only;
	// X-addresses are rejected) failing on an unparseable identifier (empty
	// string included) is not a top-level error: each section carries
	// actMalformed while the overall response stays a success.
	if !types.IsValidClassicAddress(strIdent) {
		malformed := types.RpcErrorActMalformed("Account malformed.")
		return map[string]interface{}{
			"accepted": malformed,
			"current":  malformed,
		}, nil
	}

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	walker, ok := ctx.Services.Ledger.(types.OwnerDirectoryReader)
	if !ok {
		return nil, types.RpcErrorInternal("owner_info: ledger service cannot walk owner directories")
	}

	accepted, rpcErr := ownerInfoSection(ctx, walker, strIdent, "closed")
	if rpcErr != nil {
		return nil, rpcErr
	}
	current, rpcErr := ownerInfoSection(ctx, walker, strIdent, "current")
	if rpcErr != nil {
		return nil, rpcErr
	}

	return map[string]interface{}{
		"accepted": accepted,
		"current":  current,
	}, nil
}

// ownerInfoSection walks one ledger's owner directory and builds the section
// map. Mirroring rippled's getOwnerInfo, the "offers" / "ripple_lines" keys are
// emitted only when that object type is present, so an account with no owner
// directory yields an empty object.
func ownerInfoSection(ctx *types.RpcContext, walker types.OwnerDirectoryReader, account, ledgerIndex string) (map[string]interface{}, *types.RpcError) {
	result, err := walker.GetOwnerInfo(ctx.Context, account, ledgerIndex)
	if err != nil {
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get owner info: %v", err))
	}

	section := make(map[string]interface{})
	if len(result.Offers) > 0 {
		offers := make([]interface{}, 0, len(result.Offers))
		for _, obj := range result.Offers {
			offers = append(offers, decodeOwnerObject(obj))
		}
		section["offers"] = offers
	}
	if len(result.RippleLines) > 0 {
		lines := make([]interface{}, 0, len(result.RippleLines))
		for _, obj := range result.RippleLines {
			lines = append(lines, decodeOwnerObject(obj))
		}
		section["ripple_lines"] = lines
	}
	return section, nil
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
