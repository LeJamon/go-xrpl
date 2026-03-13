package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// RequireLedgerService checks that the ledger service is available.
// Returns an RpcError if the service is nil.
func RequireLedgerService() *types.RpcError {
	if types.Services == nil || types.Services.Ledger == nil {
		return types.RpcErrorInternal("Ledger service not available")
	}
	return nil
}

// ParseParams unmarshals JSON params into dest, returning an RpcError on failure.
// If params is nil, dest is left untouched (zero value).
func ParseParams(params json.RawMessage, dest interface{}) *types.RpcError {
	if params == nil {
		return nil
	}
	if err := json.Unmarshal(params, dest); err != nil {
		return types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
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

// ClampLimit applies rippled's readLimitField logic: if the user provides a limit,
// clamp it to [range.Min, range.Max] for non-admin; admin gets unlimited.
// If the user does not provide a limit (0), use the default.
func ClampLimit(userLimit uint32, r LimitRange, isAdmin bool) uint32 {
	if userLimit == 0 {
		return r.Default
	}
	if isAdmin {
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

// InjectDeliveredAmount adds DeliveredAmount to metadata for Payment transactions.
// If meta has a "DeliveredAmount" field already, it is left as-is.
// If meta has a "delivered_amount" field, it is promoted to "DeliveredAmount".
// Otherwise, for Payment transactions, the Amount field from the transaction
// is used as a fallback for "DeliveredAmount".
// Non-Payment transactions and nil meta are no-ops.
func InjectDeliveredAmount(txJSON map[string]interface{}, meta map[string]interface{}) {
	txType, _ := txJSON["TransactionType"].(string)
	if txType != "Payment" {
		return
	}
	if meta == nil {
		return
	}

	// If DeliveredAmount already present in metadata, use it
	if _, ok := meta["DeliveredAmount"]; ok {
		return
	}

	// If delivered_amount is present, promote to DeliveredAmount
	if da, ok := meta["delivered_amount"]; ok {
		meta["DeliveredAmount"] = da
		return
	}

	// Fallback: use Amount from transaction as DeliveredAmount
	if amount, ok := txJSON["Amount"]; ok {
		meta["DeliveredAmount"] = amount
	}
}
