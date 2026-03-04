package rpc_handlers

import (
	"encoding/json"

	"github.com/LeJamon/goXRPLd/internal/rpc/rpc_types"
)

// BookChangesMethod handles the book_changes RPC method.
// STUB: Returns notImplemented. Requires transaction metadata iteration.
//
// TODO [ledger]: Implement book_changes — computes OHLCV data for all currency
//   pairs that had offer changes in a ledger.
//   - Requires: Ability to iterate transactions in a closed ledger and access
//     their metadata (AffectedNodes). The ledger service already stores
//     transaction metadata via StoreTransaction().
//   - Reference: rippled BookChanges.h (computeBookChanges)
//   - Steps:
//     1. Resolve target ledger from ledger_hash/ledger_index params
//     2. Iterate all transactions in that ledger
//     3. For each transaction, examine AffectedNodes in metadata
//     4. For ltOFFER nodes that are Modified or Deleted (not Created):
//        a. Compare FinalFields vs PreviousFields for TakerGets/TakerPays
//        b. Filter out explicitly cancelled offers (not crossed)
//        c. Compute delta in gets and pays
//        d. Calculate exchange rate = delta_pays / delta_gets
//     5. Accumulate OHLCV data per currency pair (keyed by "currA/currB")
//     6. Return: { type: "bookChanges", changes: [...], ledger_index, ledger_hash,
//        ledger_time, validated }
//   - Each change entry: { currency_a, currency_b, volume_a, volume_b,
//     high, low, open, close }
//   - Dependency: LedgerService needs a method to iterate transactions in a
//     closed ledger (e.g., GetLedgerTransactions(seq) returning tx+meta pairs)
type BookChangesMethod struct{}

func (m *BookChangesMethod) Handle(ctx *rpc_types.RpcContext, params json.RawMessage) (interface{}, *rpc_types.RpcError) {
	var request struct {
		rpc_types.LedgerSpecifier
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, rpc_types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
	}

	if rpc_types.Services == nil || rpc_types.Services.Ledger == nil {
		return nil, rpc_types.RpcErrorInternal("Ledger service not available")
	}

	return nil, rpc_types.NewRpcError(rpc_types.RpcNOT_IMPL, "notImpl", "notImpl",
		"book_changes not yet implemented — requires ledger transaction iteration")
}

func (m *BookChangesMethod) RequiredRole() rpc_types.Role {
	return rpc_types.RoleGuest
}

func (m *BookChangesMethod) SupportedApiVersions() []int {
	return []int{rpc_types.ApiVersion1, rpc_types.ApiVersion2, rpc_types.ApiVersion3}
}
