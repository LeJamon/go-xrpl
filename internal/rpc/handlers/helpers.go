package handlers

import (
	"encoding/hex"
	"encoding/json"

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

// FormatLedgerHash formats a 32-byte hash as hex string
func FormatLedgerHash(hash [32]byte) string {
	return hex.EncodeToString(hash[:])
}

// BaseHandler provides default implementations of RequiredRole (RoleGuest),
// SupportedApiVersions ([1,2,3]), and RequiredCondition (NoCondition).
// Embed this in handler structs to avoid repeating these 3 boilerplate methods.
type BaseHandler struct{}

func (BaseHandler) RequiredRole() types.Role           { return types.RoleGuest }
func (BaseHandler) SupportedApiVersions() []int        { return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3} }
func (BaseHandler) RequiredCondition() types.Condition  { return types.NoCondition }

// AdminHandler is like BaseHandler but defaults to RoleAdmin.
type AdminHandler struct{}

func (AdminHandler) RequiredRole() types.Role           { return types.RoleAdmin }
func (AdminHandler) SupportedApiVersions() []int        { return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3} }
func (AdminHandler) RequiredCondition() types.Condition  { return types.NoCondition }

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
