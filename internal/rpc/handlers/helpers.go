package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/protocol"
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
	if err := json.Unmarshal(params, dest); err != nil {
		return types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return nil
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

func resolveLedgerSelector(params json.RawMessage) (string, *types.RPCError) {
	selection, rpcErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if rpcErr != nil {
		return "", rpcErr
	}
	return selection.String(), nil
}

func preflightAccountPage(
	ctx *types.RPCContext,
	params json.RawMessage,
	account string,
	internalDetail string,
) (string, *types.RPCError) {
	selection, rpcErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if rpcErr != nil {
		return "", rpcErr
	}
	ledgerIndex := selection.String()
	if _, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, account, ledgerIndex); err != nil {
		if errors.Is(err, svcerr.ErrLedgerNotFound) {
			switch selection.Kind() {
			case ledgerselector.KindAbsent, ledgerselector.KindCurrent, ledgerselector.KindClosed, ledgerselector.KindValidated:
				if ctx.ApiVersion <= types.ApiVersion1 {
					return "", types.RPCErrorNoNetwork("InsufficientNetworkMode")
				}
				return "", types.RPCErrorNotSynced("notSynced")
			}
		}
		return "", mapAccountQueryErr(err, internalDetail)
	}
	if rpcErr := ValidateAccount(account); rpcErr != nil {
		return "", rpcErr
	}
	return ledgerIndex, nil
}

func accountPageParams(params json.RawMessage) (map[string]json.RawMessage, string, *types.RPCError) {
	var fields map[string]json.RawMessage
	if len(params) > 0 && !isJSONNull(params) {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, "", types.RPCErrorInvalidParams("Invalid parameters.")
		}
	}
	rawAccount, ok := fields["account"]
	if !ok {
		return fields, "", types.RPCErrorMissingField("account")
	}
	var account string
	if isJSONNull(rawAccount) || json.Unmarshal(rawAccount, &account) != nil {
		return fields, "", types.RPCErrorInvalidField("account")
	}
	return fields, account, nil
}

func legacyBoolValue(raw json.RawMessage) bool {
	if raw == nil || isJSONNull(raw) {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return typed != ""
	case float64:
		return typed != 0
	default:
		return false
	}
}

func parseLedgerSelectorParams(
	params json.RawMessage,
	defaultSelection ledgerselector.Selector,
) (ledgerselector.Selector, *types.RPCError) {
	if len(params) == 0 || isJSONNull(params) {
		return defaultSelection, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return ledgerselector.Selector{}, types.RPCErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return parseRawLedgerSelector(raw, defaultSelection, lookupLedgerSelectorErrors)
}

type rawLedgerSelectorErrors struct {
	hashType       func() *types.RPCError
	hashMalformed  func() *types.RPCError
	indexType      func(field string) *types.RPCError
	indexMalformed func(field string) *types.RPCError
}

var lookupLedgerSelectorErrors = rawLedgerSelectorErrors{
	hashType: func() *types.RPCError {
		return types.RPCErrorExpectedField("ledger_hash", "hex string")
	},
	hashMalformed: func() *types.RPCError {
		return types.RPCErrorExpectedField("ledger_hash", "hex string")
	},
	indexType: func(field string) *types.RPCError {
		return types.RPCErrorExpectedField(field, "string or number")
	},
	indexMalformed: func(field string) *types.RPCError {
		return types.RPCErrorExpectedField(field, "string or number")
	},
}

func parseRawLedgerSelector(
	raw map[string]json.RawMessage,
	defaultSelection ledgerselector.Selector,
	errorStyle rawLedgerSelectorErrors,
) (ledgerselector.Selector, *types.RPCError) {
	rawLedger, hasLedger := raw["ledger"]
	rawHash, hasHash := raw["ledger_hash"]
	rawIndex, hasIndex := raw["ledger_index"]
	selectorCount := 0
	for _, present := range []bool{hasLedger, hasHash, hasIndex} {
		if present {
			selectorCount++
		}
	}
	if selectorCount > 1 {
		if hasLedger {
			return ledgerselector.Selector{}, types.RPCErrorInvalidParams(
				"Exactly one of 'ledger', 'ledger_hash', or 'ledger_index' can be specified.",
			)
		}
		return ledgerselector.Selector{}, types.RPCErrorInvalidParams(
			"Exactly one of 'ledger_hash' or 'ledger_index' can be specified.",
		)
	}
	if selectorCount == 0 {
		return defaultSelection, nil
	}

	spec := types.LedgerSpecifier{}
	if hasHash {
		if isJSONNull(rawHash) || json.Unmarshal(rawHash, &spec.LedgerHash) != nil {
			return ledgerselector.Selector{}, errorStyle.hashType()
		}
		selection, err := ledgerselector.ParseHash(spec.LedgerHash)
		if err != nil {
			return ledgerselector.Selector{}, errorStyle.hashMalformed()
		}
		return selection, nil
	}

	field := "ledger_index"
	value := rawIndex
	if hasLedger {
		field = "ledger"
		value = rawLedger
	}
	var index types.LedgerIndex
	if isJSONNull(value) || json.Unmarshal(value, &index) != nil {
		return ledgerselector.Selector{}, errorStyle.indexType(field)
	}
	parse := ledgerselector.ParseIndex
	if hasLedger {
		parse = ledgerselector.Parse
	}
	selection, err := parse(index.String())
	if err != nil || selection.Kind() == ledgerselector.KindAbsent {
		return ledgerselector.Selector{}, errorStyle.indexMalformed(field)
	}
	return selection, nil
}

func resolveLedgerSelection(
	ctx *types.RPCContext,
	selection ledgerselector.Selector,
) (ledgerselector.Result[types.LedgerReader], *types.RPCError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return ledgerselector.Result[types.LedgerReader]{}, err
	}
	svc := ctx.Services.Ledger

	bySequence := func(sequence uint32) (types.LedgerReader, bool, error) {
		reader, err := svc.GetLedgerBySequence(sequence)
		return reader, reader != nil, err
	}
	byHash := func(hash [32]byte) (types.LedgerReader, bool, error) {
		reader, err := svc.GetLedgerByHash(hash)
		return reader, reader != nil, err
	}
	current := func() (types.LedgerReader, bool, error) {
		return bySequence(svc.GetCurrentLedgerIndex())
	}
	closed := func() (types.LedgerReader, bool, error) {
		return bySequence(svc.GetClosedLedgerIndex())
	}
	validated := func() (types.LedgerReader, bool, error) {
		sequence := svc.GetValidatedLedgerIndex()
		if sequence == 0 {
			return nil, false, nil
		}
		return bySequence(sequence)
	}

	resolved, err := ledgerselector.Resolve(selection, ledgerselector.Callbacks[types.LedgerReader]{
		Absent:     current,
		Current:    current,
		Closed:     closed,
		Validated:  validated,
		BySequence: bySequence,
		ByHash:     byHash,
	})
	if err != nil {
		switch selection.Kind() {
		case ledgerselector.KindAbsent, ledgerselector.KindCurrent, ledgerselector.KindClosed, ledgerselector.KindValidated:
			if ctx.ApiVersion <= types.ApiVersion1 {
				return ledgerselector.Result[types.LedgerReader]{}, types.RPCErrorNoNetwork("InsufficientNetworkMode")
			}
			return ledgerselector.Result[types.LedgerReader]{}, types.RPCErrorNotSynced("notSynced")
		}
		return ledgerselector.Result[types.LedgerReader]{}, types.RPCErrorLgrNotFound("ledgerNotFound")
	}
	if selection.Kind() == ledgerselector.KindCurrent || selection.Kind() == ledgerselector.KindAbsent {
		resolved.Validated = false
	} else if selection.Kind() == ledgerselector.KindValidated {
		resolved.Validated = true
	}
	return resolved, nil
}

// LookupLedger resolves a request's ledger selector, defaulting to current.
func LookupLedger(ctx *types.RPCContext, params json.RawMessage) (types.LedgerReader, bool, *types.RPCError) {
	selection, rpcErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if rpcErr != nil {
		return nil, false, rpcErr
	}
	resolved, rpcErr := resolveLedgerSelection(ctx, selection)
	if rpcErr != nil {
		return nil, false, rpcErr
	}
	return resolved.Value, resolved.Validated, nil
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

func mapAccountQueryErr(err error, internalDetail string) *types.RPCError {
	if rpcErr := mapLedgerLookupErr(err); rpcErr != nil {
		return rpcErr
	}
	switch {
	case errors.Is(err, svcerr.ErrAccountMalformed):
		return types.RPCErrorActMalformed("Account malformed.")
	case errors.Is(err, svcerr.ErrAccountNotFound):
		return types.RPCErrorActNotFound("Account not found.")
	case errors.Is(err, svcerr.ErrInvalidMarker):
		return types.RPCErrorInvalidField("marker")
	default:
		return types.RPCErrorInternal(internalDetail)
	}
}

// markerString extracts an opaque pagination marker while preserving the
// distinction between an absent member and a present JSON null.
func markerString(marker json.RawMessage) (string, *types.RPCError) {
	if marker == nil {
		return "", nil
	}
	if isJSONNull(marker) {
		return "", types.RPCErrorExpectedField("marker", "string")
	}
	var s string
	if err := json.Unmarshal(marker, &s); err != nil {
		return "", types.RPCErrorExpectedField("marker", "string")
	}
	return s, nil
}

// FormatLedgerHash formats a 32-byte hash as uppercase hex string (matching rippled).
func FormatLedgerHash(hash [32]byte) string {
	return protocol.Hash256Hex(hash)
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
func fillLedgerFields(
	response map[string]any,
	selector string,
	ledgerHash string,
	ledgerSeq uint32,
	currentLedgerSeq uint32,
	validated bool,
) {
	if isOpenLedgerSelector(selector) || ledgerSeq == currentLedgerSeq {
		response["ledger_current_index"] = ledgerSeq
	} else {
		response["ledger_hash"] = ledgerHash
		response["ledger_index"] = ledgerSeq
	}
	response["validated"] = validated
}

func fillResolvedLedgerFields(response map[string]any, reader types.LedgerReader, validated bool) {
	if reader.IsClosed() {
		response["ledger_hash"] = FormatLedgerHash(reader.Hash())
		response["ledger_index"] = reader.Sequence()
	} else {
		response["ledger_current_index"] = reader.Sequence()
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

func decodeBinaryObject(data []byte) (map[string]any, error) {
	return binarycodec.Decode(hex.EncodeToString(data))
}

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
		txJSON, decErr := decodeBinaryObject(txBytes)
		if decErr == nil {
			st := StoredTransaction{TxJSON: txJSON}
			if len(metaBytes) > 0 {
				metaJSON, metaErr := decodeBinaryObject(metaBytes)
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
// to a transaction's metadata, matching rippled's RPC::insertDeliveredAmount.
// It is emitted only for a successful Payment, CheckCash, or AccountDelete
// (rippled's canHaveDeliveredAmount: those three types plus tesSUCCESS; CheckCash
// also requires fix1623, which is enabled on every ledger go-xrpl serves). The
// value is the real serialized sfDeliveredAmount metadata field when present,
// otherwise the transaction's Amount (rippled's ledger-index / close-time gate
// always holds for served ledgers), otherwise the literal "unavailable". The
// real PascalCase "DeliveredAmount" metadata field is left untouched — only the
// synthetic snake_case field is written. nil meta is a no-op.
func InjectDeliveredAmount(txJSON map[string]any, meta map[string]any) {
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
	} else if amount, ok := txJSON["Amount"]; ok {
		meta["delivered_amount"] = amount
	} else {
		meta["delivered_amount"] = "unavailable"
	}
}
