package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// RequireLedgerService checks that the ledger service is available
// on the request's service container. Returns an RpcError if not.
func RequireLedgerService(services *types.ServiceContainer) *types.RpcError {
	if services == nil || services.Ledger == nil {
		return types.RpcErrorInternal("Ledger service not available")
	}
	return nil
}

// shedCheck returns the shedder when a gate should run: nil otherwise.
// Skips when ctx is missing, the shedder isn't wired, or the caller is
// unlimited (admin/identified) — mirroring rippled's isUnlimited(role)
// carve-out at RPCHandler.cpp:132 and LegacyPathFind.cpp:32-37.
func shedCheck(ctx *types.RpcContext) *types.ClientLoadShedder {
	if ctx == nil || ctx.Unlimited || ctx.Services == nil {
		return nil
	}
	return ctx.Services.ClientLoad
}

// RequireNotBusyClient is the generic RPC admission gate fired before
// every non-admin RPC dispatches. Mirrors rippled's fillHandler check
// at RPCHandler.cpp:132-141: shed when the jtCLIENT-or-higher job count
// exceeds Tuning::maxJobQueueClients (500).
func RequireNotBusyClient(ctx *types.RpcContext) *types.RpcError {
	s := shedCheck(ctx)
	if s == nil {
		return nil
	}
	if s.InFlight() > types.MaxJobQueueClients {
		return types.RpcErrorTooBusy()
	}
	return nil
}

// RequireNotBusyBookOffers is the book_offers-specific gate matching
// rippled BookOffers.cpp:42-43 (`getJobCountGE(jtCLIENT) > 200`). Fires
// in addition to the generic dispatcher-level gate.
func RequireNotBusyBookOffers(ctx *types.RpcContext) *types.RpcError {
	s := shedCheck(ctx)
	if s == nil {
		return nil
	}
	if s.InFlight() > types.MaxBookOffersClients {
		return types.RpcErrorTooBusy()
	}
	return nil
}

// AcquirePathfind admits a path-finding request, mirroring the
// LegacyPathFind ctor at rippled LegacyPathFind.cpp:30-60:
//
//  1. Admin/unlimited callers bypass the gate.
//  2. If in-flight RPCs exceed Tuning::maxPathfindJobCount (50), shed.
//  3. Otherwise CAS-increment the concurrent-path-find counter; if it
//     would exceed Tuning::maxPathfindsInProgress (2), shed.
//
// Returns a release func the caller MUST invoke (typically via defer)
// when admitted; release is nil on shed. The isLoadedLocal() check
// rippled performs in the same ctor will land alongside the LoadFeeTrack
// subsystem (ServiceContainer.LoadFactorFees is nil today).
func AcquirePathfind(ctx *types.RpcContext) (release func(), rpcErr *types.RpcError) {
	s := shedCheck(ctx)
	if s == nil {
		return func() {}, nil
	}
	if s.InFlight() > types.MaxPathfindClients {
		return nil, types.RpcErrorTooBusy()
	}
	if !s.AcquirePathfind() {
		return nil, types.RpcErrorTooBusy()
	}
	return s.ReleasePathfind, nil
}

// ParseParams unmarshals JSON params into dest, returning an RpcError on failure.
// If params is nil, dest is left untouched (zero value).
func ParseParams(params json.RawMessage, dest interface{}) *types.RpcError {
	if params == nil {
		return nil
	}
	if err := json.Unmarshal(params, dest); err != nil {
		return types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return nil
}

// RequireAccount checks that the account parameter is non-empty.
func RequireAccount(account string) *types.RpcError {
	if account == "" {
		return types.RpcErrorInvalidParams("Missing required parameter: account")
	}
	return nil
}

// ValidateAccount validates a base58-encoded XRPL account address.
// Returns rpcACT_MALFORMED (code 35) if malformed, matching rippled behavior.
func ValidateAccount(account string) *types.RpcError {
	if account == "" {
		return types.RpcErrorInvalidParams("Missing required parameter: account")
	}
	if !types.IsValidXRPLAddress(account) {
		return types.RpcErrorActMalformed("Malformed account.")
	}
	return nil
}

// FormatLedgerHash formats a 32-byte hash as uppercase hex string (matching rippled).
func FormatLedgerHash(hash [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

// FormatHash formats arbitrary bytes as uppercase hex string.
func FormatHash(b []byte) string {
	return strings.ToUpper(hex.EncodeToString(b))
}

// LimitRange defines the min, default, and max values for a paginated limit parameter.
// Matches rippled's Tuning::LimitRange struct.
type LimitRange struct {
	Min, Default, Max uint32
}

// Tuning constants matching rippled/src/xrpld/rpc/detail/Tuning.h
var (
	LimitAccountLines    = LimitRange{10, 200, 400}
	LimitAccountChannels = LimitRange{10, 200, 400}
	LimitAccountObjects  = LimitRange{10, 200, 400}
	LimitAccountOffers   = LimitRange{10, 200, 400}
	LimitBookOffers      = LimitRange{0, 60, 100}
	LimitNoRippleCheck   = LimitRange{10, 300, 400}
	LimitAccountNFTokens = LimitRange{20, 100, 400}
	LimitNFTOffers       = LimitRange{50, 250, 500}

	// LedgerData limits from rippled Tuning.h: pageLength(isBinary)
	// Binary mode: binaryPageLength = 2048
	// JSON mode: jsonPageLength = 256
	LimitLedgerData       = LimitRange{16, 256, 256}
	LimitLedgerDataBinary = LimitRange{16, 2048, 2048}
)

// ClampLimit applies rippled's readLimitField logic: if the user provides
// a limit, clamp it to [range.Min, range.Max] when unlimited is false;
// otherwise return the user value unchanged. unlimited is true for both
// admin and identified roles (matches rippled isUnlimited in Role.cpp).
// If the user does not provide a limit (0), use the default.
func ClampLimit(userLimit uint32, r LimitRange, unlimited bool) uint32 {
	if userLimit == 0 {
		return r.Default
	}
	if unlimited {
		return userLimit
	}
	if userLimit < r.Min {
		return r.Min
	}
	if userLimit > r.Max {
		return r.Max
	}
	return userLimit
}

// BaseHandler provides default implementations of RequiredRole (RoleGuest),
// SupportedApiVersions ([1,2,3]), and RequiredCondition (NoCondition).
// Embed this in handler structs to avoid repeating these 3 boilerplate methods.
type BaseHandler struct{}

func (BaseHandler) RequiredRole() types.Role { return types.RoleGuest }
func (BaseHandler) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
func (BaseHandler) RequiredCondition() types.Condition { return types.NoCondition }

// AdminHandler is like BaseHandler but defaults to RoleAdmin.
type AdminHandler struct{}

func (AdminHandler) RequiredRole() types.Role { return types.RoleAdmin }
func (AdminHandler) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}
func (AdminHandler) RequiredCondition() types.Condition { return types.NoCondition }

// decodeTxBlob decodes transaction data that may be in one of two formats:
//  1. VL-encoded binary blob: [VL-prefix][tx_bytes][VL-prefix][meta_bytes]
//     (produced by tx.CreateTxWithMetaBlob, stored via AddTransactionWithMeta)
//  2. JSON-marshaled StoredTransaction: {"tx_json":{...},"meta":{...}}
//     (produced by the submit handler)
//
// It tries VL binary decode first, then falls back to JSON unmarshal.
func decodeTxBlob(data []byte) (StoredTransaction, error) {
	// Try VL-encoded binary format first
	txBytes, metaBytes, err := tx.SplitTxWithMetaBlob(data)
	if err == nil {
		txJSON, decErr := binarycodec.Decode(hex.EncodeToString(txBytes))
		if decErr == nil {
			st := StoredTransaction{TxJSON: txJSON}
			if len(metaBytes) > 0 {
				metaJSON, metaErr := binarycodec.Decode(hex.EncodeToString(metaBytes))
				if metaErr == nil {
					st.Meta = metaJSON
				}
			}
			return st, nil
		}
	}

	// Fall back to JSON format
	var st StoredTransaction
	if jsonErr := json.Unmarshal(data, &st); jsonErr != nil {
		return StoredTransaction{}, jsonErr
	}
	return st, nil
}

// InjectDeliveredAmount adds the synthetic snake_case "delivered_amount" field
// to a transaction's metadata, mirroring rippled's RPC::insertDeliveredAmount
// (DeliveredAmount.cpp:128-160). It is emitted only for a successful Payment,
// CheckCash, or AccountDelete (canHaveDeliveredAmount: those three types plus
// tesSUCCESS; CheckCash also needs fix1623, which is enabled on every ledger
// goXRPL serves). The value is the real sfDeliveredAmount metadata field when
// present, otherwise the transaction's Amount (the ledger-4594095 / Feb-2014
// close-time gate always holds for ledgers goXRPL serves), otherwise the
// literal "unavailable". The real (PascalCase) DeliveredAmount metadata field
// is left untouched. nil meta is a no-op.
func InjectDeliveredAmount(txJSON map[string]interface{}, meta map[string]interface{}) {
	if meta == nil {
		return
	}

	switch txType, _ := txJSON["TransactionType"].(string); txType {
	case "Payment", "CheckCash", "AccountDelete":
	default:
		return
	}
	if result, _ := meta["TransactionResult"].(string); result != "tesSUCCESS" {
		return
	}

	// Idempotent: a caller (e.g. the engine's simulate metadata) may already
	// carry the real delivered amount under the snake_case key; keep it rather
	// than clobbering a partial-payment value with the full Amount fallback.
	if _, ok := meta["delivered_amount"]; ok {
		return
	}

	if da, ok := meta["DeliveredAmount"]; ok {
		meta["delivered_amount"] = da
	} else if amount, ok := txJSON["Amount"]; ok {
		meta["delivered_amount"] = amount
	} else {
		meta["delivered_amount"] = "unavailable"
	}
}

// toMetaMap normalises a metadata value to a generic JSON object. It returns v
// directly when it is already a map (the unit-test shape) and otherwise
// round-trips it through JSON (the production shape, where the engine hands back
// a *tx.Metadata). Returns nil when the value is not a JSON object.
func toMetaMap(v any) map[string]interface{} {
	if m, ok := v.(map[string]interface{}); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if json.Unmarshal(b, &m) != nil {
		return nil
	}
	return m
}
