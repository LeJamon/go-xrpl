package handlers

import (
	"bytes"
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
// on the request's service container. Returns an RPCError if not.
func RequireLedgerService(services *types.ServiceContainer) *types.RPCError {
	if services == nil || services.Ledger == nil {
		return types.RPCErrorInternal("Ledger service not available")
	}
	return nil
}

// RequireTxTables gates tx-history-backed handlers (tx, account_tx,
// tx_history) the way rippled does: config().useTxTables() is checked
// before any parameter validation, so a node without a transaction
// database answers notEnabled even for otherwise-malformed requests.
// Services that don't implement types.TxTablesProvider are assumed to
// have history available.
func RequireTxTables(services *types.ServiceContainer) *types.RPCError {
	if err := RequireLedgerService(services); err != nil {
		return err
	}
	if p, ok := services.Ledger.(types.TxTablesProvider); ok && !p.UseTxTables() {
		return types.RPCErrorNotEnabled("")
	}
	return nil
}

// shedCheck returns the shedder when a gate should run: nil otherwise.
// Skips when ctx is missing, the shedder isn't wired, or the caller is
// unlimited (admin/identified) — mirroring rippled's isUnlimited(role)
// carve-out at RPCHandler.cpp:132 and LegacyPathFind.cpp:32-37.
func shedCheck(ctx *types.RPCContext) *types.ClientLoadShedder {
	if ctx == nil || ctx.Unlimited || ctx.Services == nil {
		return nil
	}
	return ctx.Services.ClientLoad
}

// RequireNotBusyClient is the generic RPC admission gate fired before
// every non-admin RPC dispatches. Mirrors rippled's fillHandler check
// at RPCHandler.cpp:132-141: shed when the jtCLIENT-or-higher job count
// exceeds Tuning::maxJobQueueClients (500).
func RequireNotBusyClient(ctx *types.RPCContext) *types.RPCError {
	s := shedCheck(ctx)
	if s == nil {
		return nil
	}
	if s.InFlight() > types.MaxJobQueueClients {
		return types.RPCErrorTooBusy()
	}
	return nil
}

// RequireNotBusyBookOffers is the book_offers-specific gate matching
// rippled BookOffers.cpp:42-43 (`getJobCountGE(jtCLIENT) > 200`). Fires
// in addition to the generic dispatcher-level gate.
func RequireNotBusyBookOffers(ctx *types.RPCContext) *types.RPCError {
	s := shedCheck(ctx)
	if s == nil {
		return nil
	}
	if s.InFlight() > types.MaxBookOffersClients {
		return types.RPCErrorTooBusy()
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
func AcquirePathfind(ctx *types.RPCContext) (release func(), rpcErr *types.RPCError) {
	s := shedCheck(ctx)
	if s == nil {
		return func() {}, nil
	}
	if s.InFlight() > types.MaxPathfindClients {
		return nil, types.RPCErrorTooBusy()
	}
	if !s.AcquirePathfind() {
		return nil, types.RPCErrorTooBusy()
	}
	return s.ReleasePathfind, nil
}

// ParseParams unmarshals JSON params into dest, returning an RPCError on failure.
// If params is nil, dest is left untouched (zero value).
func ParseParams(params json.RawMessage, dest any) *types.RPCError {
	if params == nil {
		return nil
	}
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return rpcErr
	}
	if _, ok := dest.(interface{ UsesLedgerSpecifier() }); ok {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(params, &fields); err == nil {
			delete(fields, "ledger")
			delete(fields, "ledger_hash")
			delete(fields, "ledger_index")
			if stripped, err := json.Marshal(fields); err == nil {
				params = stripped
			}
		}
	}
	if err := json.Unmarshal(params, dest); err != nil {
		return types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return nil
}

func parseLedgerSpecifier(params json.RawMessage) (types.LedgerSpecifier, bool, *types.RPCError) {
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return types.LedgerSpecifier{}, false, rpcErr
	}
	hasSelector, rpcErr := ledgerRequestHasSelector(params)
	if rpcErr != nil {
		return types.LedgerSpecifier{}, false, rpcErr
	}
	if !hasSelector {
		return types.LedgerSpecifier{}, false, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(params, &fields); err != nil {
		return types.LedgerSpecifier{}, false, types.RPCErrorInvalidParams("Invalid parameters.")
	}
	var spec types.LedgerSpecifier
	if raw, ok := fields["ledger_hash"]; ok {
		_ = json.Unmarshal(raw, &spec.LedgerHash)
		return spec, true, nil
	}
	name := "ledger_index"
	raw, ok := fields[name]
	if !ok {
		name = "ledger"
		raw = fields[name]
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		value = strings.TrimSpace(string(raw))
		if value == "-0" {
			value = "0"
		}
	}
	if name == "ledger" {
		spec.Ledger = types.LedgerIndex(value)
	} else {
		spec.LedgerIndex = types.LedgerIndex(value)
	}
	return spec, true, nil
}

func validateJsonCppIntegerRange(params json.RawMessage) *types.RPCError {
	if params == nil {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(params))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil
	}
	var invalid bool
	var walk func(any)
	walk = func(current any) {
		if invalid {
			return
		}
		switch typed := current.(type) {
		case json.Number:
			raw := typed.String()
			if strings.ContainsAny(raw, ".eE") {
				return
			}
			if strings.HasPrefix(raw, "-") {
				_, err := strconv.ParseInt(raw, 10, 32)
				invalid = err != nil
				return
			}
			_, err := strconv.ParseUint(raw, 10, 32)
			invalid = err != nil
		case map[string]any:
			for _, child := range typed {
				walk(child)
			}
		case []any:
			for _, child := range typed {
				walk(child)
			}
		}
	}
	walk(value)
	if invalid {
		return types.RPCErrorInvalidParams("Invalid parameters.")
	}
	return nil
}

func ValidateJsonCppIntegerRange(params json.RawMessage) *types.RPCError {
	return validateJsonCppIntegerRange(params)
}

// RequireAccount checks that the account parameter is non-empty.
func RequireAccount(account string) *types.RPCError {
	if account == "" {
		return types.RPCErrorInvalidParams("Missing required parameter: account")
	}
	return nil
}

// ValidateAccount validates a base58-encoded XRPL account address.
// Returns rpcACT_MALFORMED (code 35) if malformed, matching rippled behavior.
func ValidateAccount(account string) *types.RPCError {
	if account == "" {
		return types.RPCErrorInvalidParams("Missing required parameter: account")
	}
	if !types.IsValidXRPLAddress(account) {
		return types.RPCErrorActMalformed("Account malformed.")
	}
	return nil
}

// normalizeLedgerSpecifier folds the legacy ledger field into the selector used
// by the service. Exactly 64 characters denote a hash; all other strings denote
// an index.
func normalizeLedgerSpecifier(spec types.LedgerSpecifier) (types.LedgerSpecifier, string, string) {
	hashField := "ledger_hash"
	indexField := "ledger_index"
	if spec.Ledger != "" {
		if legacy := spec.Ledger.String(); len(legacy) == 64 {
			spec.LedgerHash = legacy
			hashField = "ledger"
		} else {
			spec.LedgerIndex = spec.Ledger
			indexField = "ledger"
		}
		spec.Ledger = ""
	}
	return spec, hashField, indexField
}

func validateLedgerSpecifierConflict(spec types.LedgerSpecifier) *types.RPCError {
	count := 0
	for _, present := range []bool{spec.Ledger != "", spec.LedgerHash != "", spec.LedgerIndex != ""} {
		if present {
			count++
		}
	}
	if count <= 1 {
		return nil
	}
	if spec.Ledger != "" {
		return types.RPCErrorInvalidParams("Exactly one of 'ledger', 'ledger_hash', or 'ledger_index' can be specified.")
	}
	return types.RPCErrorInvalidParams("Exactly one of 'ledger_hash' or 'ledger_index' can be specified.")
}

func parseLedgerIndex(index string) (uint64, error) {
	if strings.HasPrefix(index, "+") && len(index) > 1 {
		index = index[1:]
	}
	return strconv.ParseUint(index, 10, 32)
}

// resolveLedgerSelector returns the string ledger selector the service query
// path expects. Multiple selector fields are rejected, hashes are threaded
// through verbatim, and an absent selector defaults to the current ledger.
func resolveLedgerSelector(spec types.LedgerSpecifier) (string, *types.RPCError) {
	if err := validateLedgerSpecifierConflict(spec); err != nil {
		return "", err
	}
	var hashField, indexField string
	spec, hashField, indexField = normalizeLedgerSpecifier(spec)
	if spec.LedgerHash != "" {
		if len(spec.LedgerHash) != 64 {
			return "", types.RPCErrorExpectedField(hashField, "hex string")
		}
		if _, err := hex.DecodeString(spec.LedgerHash); err != nil {
			return "", types.RPCErrorExpectedField(hashField, "hex string")
		}
		return spec.LedgerHash, nil
	}
	if spec.LedgerIndex != "" {
		index := spec.LedgerIndex.String()
		switch index {
		case "current", "validated", "closed":
			return index, nil
		}
		sequence, err := parseLedgerIndex(index)
		if err != nil {
			return "", types.RPCErrorExpectedField(indexField, "string or number")
		}
		return strconv.FormatUint(sequence, 10), nil
	}
	return "current", nil
}

// LookupLedger resolves the ledger a request targets and returns the reader plus
// whether that ledger is validated. Multiple selector fields are rejected and
// an absent selector defaults to the current ledger. Invalid selectors use
// field-specific rpcINVALID_PARAMS messages; absent ledgers use
// ledgerNotFound (rpcLGR_NOT_FOUND).
func LookupLedger(ctx *types.RPCContext, spec types.LedgerSpecifier) (types.LedgerReader, bool, *types.RPCError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, false, err
	}
	svc := ctx.Services.Ledger
	if err := validateLedgerSpecifierConflict(spec); err != nil {
		return nil, false, err
	}
	var hashField, indexField string
	spec, hashField, indexField = normalizeLedgerSpecifier(spec)

	if spec.LedgerHash != "" {
		if len(spec.LedgerHash) != 64 {
			return nil, false, types.RPCErrorExpectedField(hashField, "hex string")
		}
		hashBytes, err := hex.DecodeString(spec.LedgerHash)
		if err != nil {
			return nil, false, types.RPCErrorExpectedField(hashField, "hex string")
		}
		var hash [32]byte
		copy(hash[:], hashBytes)
		l, err := svc.GetLedgerByHash(hash)
		if err != nil || l == nil {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	}

	switch idx := spec.LedgerIndex.String(); idx {
	case "", "current":
		l, err := svc.GetLedgerBySequence(svc.GetCurrentLedgerIndex())
		if err != nil || l == nil {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return l, false, nil
	case "validated":
		seq := svc.GetValidatedLedgerIndex()
		if seq == 0 {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		l, err := svc.GetLedgerBySequence(seq)
		if err != nil || l == nil {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return l, true, nil
	case "closed":
		l, err := svc.GetLedgerBySequence(svc.GetClosedLedgerIndex())
		if err != nil || l == nil {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	default:
		seq, perr := parseLedgerIndex(idx)
		if perr != nil {
			return nil, false, types.RPCErrorExpectedField(indexField, "string or number")
		}
		l, err := svc.GetLedgerBySequence(uint32(seq))
		if err != nil || l == nil {
			return nil, false, types.RPCErrorLgrNotFound("ledgerNotFound")
		}
		return l, l.IsValidated(), nil
	}
}

// mapLedgerLookupErr maps the ledger-resolution errors a ledger-backed account
// query can return into rippled RPCErrors (ledgerNotFound,
// ledgerIndexMalformed, ledgerHashMalformed). It returns nil when err is not a
// ledger-resolution error so callers fall through to their handler-specific
// mapping (account-not-found, etc.), mirroring how rippled's lookupLedger sits
// ahead of each handler's own checks.
func mapLedgerLookupErr(err error) *types.RPCError {
	switch {
	case errors.Is(err, svcerr.ErrLedgerNotFound):
		return types.RPCErrorLgrNotFound("ledgerNotFound")
	case errors.Is(err, svcerr.ErrInvalidLedgerIndex):
		return types.RPCErrorInvalidParams("ledgerIndexMalformed")
	case errors.Is(err, svcerr.ErrInvalidLedgerHash):
		return types.RPCErrorInvalidParams("ledgerHashMalformed")
	}
	return nil
}

// markerString extracts the opaque pagination marker from a request's
// PaginationParams.Marker (decoded as `any`). A nil marker means "first page";
// a non-string marker is rejected as rippled's expected_field_error(marker,
// "string"). The service validates the marker's contents (a 64-hex ledger-state
// key).
func markerString(marker any) (string, *types.RPCError) {
	if marker == nil {
		return "", nil
	}
	s, ok := marker.(string)
	if !ok {
		return "", types.RPCErrorExpectedField("marker", "string")
	}
	return s, nil
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
	LimitAccountTx       = LimitRange{10, 200, 400}
	LimitBookOffers      = LimitRange{1, 60, 100}
	LimitNoRippleCheck   = LimitRange{10, 300, 400}
	LimitAccountNFTokens = LimitRange{20, 100, 400}
	LimitNFTOffers       = LimitRange{50, 250, 500}

	// LedgerData limits from rippled Tuning.h: pageLength(isBinary)
	// Binary mode: binaryPageLength = 2048
	// JSON mode: jsonPageLength = 256
	LimitLedgerData       = LimitRange{16, 256, 256}
	LimitLedgerDataBinary = LimitRange{16, 2048, 2048}
)

// ClampLimit applies rippled's pre-3.1.3 clamp logic: if the user provides
// a limit, clamp it to [range.Min, range.Max] when unlimited is false;
// otherwise return the user value unchanged. unlimited is true for both
// admin and identified roles (matches rippled isUnlimited in Role.cpp).
// If the user does not provide a limit (0), use the default.
//
// Prefer ReadLimitField for commands that route through rippled's readLimitField
// (which rejects an explicit limit=0). ClampLimit is retained for ledger_data,
// whose rippled handler does not use readLimitField and still maps 0 to default.
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

// ReadLimitField mirrors rippled's readLimitField (RPCHelpers.cpp): it reads the
// "limit" field from the raw request params. An absent or null limit yields the
// range default; a non-integer or negative value is expected_field_error; an
// explicit 0 is invalid_field_error — rejected for every role, before clamping;
// otherwise the value is clamped to [Min, Max] for non-unlimited roles.
func ReadLimitField(params json.RawMessage, r LimitRange, unlimited bool) (uint32, *types.RPCError) {
	raw, present := rawLimitField(params)
	if !present || isJSONNull(raw) {
		return r.Default, nil
	}
	var v uint32
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, types.RPCErrorExpectedField("limit", "unsigned integer")
	}
	if v == 0 {
		return 0, types.RPCErrorInvalidField("limit")
	}
	if !unlimited {
		if v < r.Min {
			v = r.Min
		}
		if v > r.Max {
			v = r.Max
		}
	}
	return v, nil
}

// rawLimitField extracts the raw "limit" value from a params object, reporting
// whether it was present.
func rawLimitField(params json.RawMessage) (json.RawMessage, bool) {
	if len(params) == 0 {
		return nil, false
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(params, &probe); err != nil {
		return nil, false
	}
	raw, ok := probe["limit"]
	return raw, ok
}

// arraySizeRPCError maps a binarycodec.Encode failure that is a JSON array-size
// overflow to invalidParams (matching rippled's STParsedJSON cap), returning nil
// for any other error so the caller keeps its existing mapping.
func arraySizeRPCError(err error) *types.RPCError {
	if msg, ok := binarycodec.AsArrayTooLargeError(err); ok {
		return types.RPCErrorInvalidParams(msg)
	}
	return nil
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

const (
	deliveredAmountLedgerCutoff    uint32 = 4_594_095
	deliveredAmountCloseTimeCutoff int64  = 446_000_000
)

// SyntheticMetadataContext identifies the ledger that produced transaction
// metadata. CloseTime is expressed in Ripple-epoch seconds.
type SyntheticMetadataContext struct {
	LedgerSequence uint32
	CloseTime      int64
}

// InjectDeliveredAmount adds the synthetic snake_case "delivered_amount" field
// to metadata for an eligible successful transaction.
func InjectDeliveredAmount(txJSON map[string]any, meta map[string]any, ctx SyntheticMetadataContext) {
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

	// Idempotent: a caller may already carry the real delivered amount under the
	// snake_case key (e.g. a partial-payment value); keep it rather than
	// clobbering it with the full Amount fallback.
	if _, ok := meta["delivered_amount"]; ok {
		return
	}

	if da, ok := meta["DeliveredAmount"]; ok {
		meta["delivered_amount"] = da
	} else if amount, ok := txJSON["Amount"]; ok &&
		(ctx.LedgerSequence >= deliveredAmountLedgerCutoff || ctx.CloseTime > deliveredAmountCloseTimeCutoff) {
		meta["delivered_amount"] = amount
	} else {
		meta["delivered_amount"] = "unavailable"
	}
}
