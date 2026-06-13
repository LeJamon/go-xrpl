package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
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

// RequireTxTables gates tx-history-backed handlers (tx, account_tx,
// tx_history) the way rippled does: config().useTxTables() is checked
// before any parameter validation, so a node without a transaction
// database answers notEnabled even for otherwise-malformed requests.
// Services that don't implement types.TxTablesProvider are assumed to
// have history available.
func RequireTxTables(services *types.ServiceContainer) *types.RpcError {
	if err := RequireLedgerService(services); err != nil {
		return err
	}
	if p, ok := services.Ledger.(types.TxTablesProvider); ok && !p.UseTxTables() {
		return types.RpcErrorNotEnabled("")
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
func ParseParams(params json.RawMessage, dest any) *types.RpcError {
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
		return types.RpcErrorActMalformed("Account malformed.")
	}
	return nil
}

// resolveLedgerIndex returns the ledger selector for a request, defaulting
// to "current" when no ledger_index was supplied.
func resolveLedgerIndex(li types.LedgerIndex) string {
	if li != "" {
		return li.String()
	}
	return "current"
}

// normalizeLedgerSpecifier folds rippled's legacy combined `ledger` field into
// LedgerHash/LedgerIndex (RPCHelpers.cpp:367-374): a string longer than 12
// characters becomes a ledger_hash, anything else a ledger_index. Explicit
// ledger_hash / ledger_index always win over the legacy field.
func normalizeLedgerSpecifier(spec types.LedgerSpecifier) types.LedgerSpecifier {
	if spec.Ledger == "" || spec.LedgerHash != "" || spec.LedgerIndex != "" {
		spec.Ledger = ""
		return spec
	}
	if legacy := spec.Ledger.String(); len(legacy) > 12 {
		spec.LedgerHash = legacy
	} else {
		spec.LedgerIndex = spec.Ledger
	}
	spec.Ledger = ""
	return spec
}

// resolveLedgerSelector returns the string ledger selector the service query
// path expects, mirroring rippled's ledgerFromRequest (RPCHelpers.cpp:367-402).
// ledger_hash takes precedence over ledger_index when both are supplied, and the
// hash is threaded through verbatim so the service resolves the specific named
// ledger (its 64-char-hex branch) rather than collapsing to the latest validated
// one. A malformed hash maps to rpcINVALID_PARAMS, matching rippled's
// ledgerHashMalformed. With neither field set the request falls back to the open
// "current" ledger.
func resolveLedgerSelector(spec types.LedgerSpecifier) (string, *types.RpcError) {
	spec = normalizeLedgerSpecifier(spec)
	if spec.LedgerHash != "" {
		if len(spec.LedgerHash) != 64 {
			return "", types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		if _, err := hex.DecodeString(spec.LedgerHash); err != nil {
			return "", types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		return spec.LedgerHash, nil
	}
	if spec.LedgerIndex != "" {
		return spec.LedgerIndex.String(), nil
	}
	return "current", nil
}

// LookupLedger resolves the ledger a request targets and returns the reader plus
// whether that ledger is validated, mirroring rippled's RPC::lookupLedger /
// ledgerFromRequest (RPCHelpers.cpp:355-402). ledger_hash takes precedence over
// ledger_index; with neither supplied it defaults to the open (current) ledger.
// Errors use rippled's tokens: ledgerHashMalformed / ledgerIndexMalformed
// (rpcINVALID_PARAMS) for bad selectors and ledgerNotFound (rpcLGR_NOT_FOUND)
// for an absent ledger. It is the single resolution point the direct-ledger
// handlers share in place of hand-rolled validated/current/closed/numeric
// switches.
func LookupLedger(ctx *types.RpcContext, spec types.LedgerSpecifier) (types.LedgerReader, bool, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, false, err
	}
	svc := ctx.Services.Ledger
	spec = normalizeLedgerSpecifier(spec)

	if spec.LedgerHash != "" {
		if len(spec.LedgerHash) != 64 {
			return nil, false, types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		hashBytes, err := hex.DecodeString(spec.LedgerHash)
		if err != nil {
			return nil, false, types.RpcErrorInvalidParams("ledgerHashMalformed")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		l, err := svc.GetLedgerByHash(hash)
		if err != nil || l == nil {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	}

	switch idx := spec.LedgerIndex.String(); idx {
	case "", "current":
		l, err := svc.GetLedgerBySequence(svc.GetCurrentLedgerIndex())
		if err != nil || l == nil {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return l, false, nil
	case "validated":
		seq := svc.GetValidatedLedgerIndex()
		if seq == 0 {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		l, err := svc.GetLedgerBySequence(seq)
		if err != nil || l == nil {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return l, true, nil
	case "closed":
		l, err := svc.GetLedgerBySequence(svc.GetClosedLedgerIndex())
		if err != nil || l == nil {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	default:
		seq, perr := strconv.ParseUint(idx, 10, 32)
		if perr != nil {
			return nil, false, types.RpcErrorInvalidParams("ledgerIndexMalformed")
		}
		l, err := svc.GetLedgerBySequence(uint32(seq))
		if err != nil || l == nil {
			return nil, false, types.RpcErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	}
}

// mapLedgerLookupErr maps the ledger-resolution errors a ledger-backed account
// query can return into rippled RpcErrors (ledgerNotFound,
// ledgerIndexMalformed, ledgerHashMalformed). It returns nil when err is not a
// ledger-resolution error so callers fall through to their handler-specific
// mapping (account-not-found, etc.), mirroring how rippled's lookupLedger sits
// ahead of each handler's own checks.
func mapLedgerLookupErr(err error) *types.RpcError {
	switch {
	case errors.Is(err, svcerr.ErrLedgerNotFound):
		return types.RpcErrorLgrNotFound("ledgerNotFound")
	case errors.Is(err, svcerr.ErrInvalidLedgerIndex):
		return types.RpcErrorInvalidParams("ledgerIndexMalformed")
	case errors.Is(err, svcerr.ErrInvalidLedgerHash):
		return types.RpcErrorInvalidParams("ledgerHashMalformed")
	}
	return nil
}

// FormatLedgerHash formats a 32-byte hash as uppercase hex string (matching rippled).
func FormatLedgerHash(hash [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(hash[:]))
}

// isOpenLedgerSelector reports whether a resolved ledger selector refers to
// the open (current) ledger. The open ledger is selected by "current" or the
// empty default; "closed", "validated" and numeric indices all refer to
// closed ledgers.
func isOpenLedgerSelector(selector string) bool {
	return selector == "current" || selector == ""
}

// fillLedgerFields writes the ledger-identity fields of an RPC response,
// mirroring rippled's RPC::lookupLedger. For the open ledger it emits only
// ledger_current_index (rippled withholds the interim hash and index); for a
// closed ledger it emits ledger_hash and ledger_index. The validated flag is
// always emitted. ledgerHash must already be the formatted uppercase-hex hash.
func fillLedgerFields(response map[string]any, selector string, ledgerHash string, ledgerSeq uint32, validated bool) {
	if isOpenLedgerSelector(selector) {
		response["ledger_current_index"] = ledgerSeq
	} else {
		response["ledger_hash"] = ledgerHash
		response["ledger_index"] = ledgerSeq
	}
	response["validated"] = validated
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

// InjectDeliveredAmount adds DeliveredAmount to metadata for Payment transactions.
// If meta has a "DeliveredAmount" field already, it is left as-is.
// If meta has a "delivered_amount" field, it is promoted to "DeliveredAmount".
// Otherwise, for Payment transactions, the Amount field from the transaction
// is used as a fallback for "DeliveredAmount".
// Non-Payment transactions and nil meta are no-ops.
func InjectDeliveredAmount(txJSON map[string]any, meta map[string]any) {
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
