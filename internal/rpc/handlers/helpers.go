package handlers

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerselector "github.com/LeJamon/go-xrpl/internal/ledger/selector"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/internal/tx"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
)

func logRpcError(operation string, err error) {
	xrpllog.Named(xrpllog.PartitionRPC).Error(operation, "err", err)
}

func rpcInternalError(operation string, err error) *types.RpcError {
	logRpcError(operation, err)
	return types.RpcErrorInternal()
}

func rpcInternalInvariantError(operation string) *types.RpcError {
	xrpllog.Named(xrpllog.PartitionRPC).Error(operation)
	return types.RpcErrorInternal()
}

func rpcTransactionSubmissionError(operation string, err error) *types.RpcError {
	logRpcError(operation, err)
	return types.RpcErrorTransactionSubmission()
}

func rpcDBDeserializationError(operation string, err error) *types.RpcError {
	logRpcError(operation, err)
	return types.RpcErrorDBDeserialization()
}

func ledgerMapHashes(l types.LedgerReader) (txHash, stateHash [32]byte) {
	return l.TxMapHash(), l.StateMapHash()
}

func ledgerAmendmentRules(l types.LedgerReader) (*amendment.Rules, error) {
	if source, ok := l.(types.LedgerAmendmentRulesErrorSource); ok {
		rules, err := source.LedgerAmendmentRulesWithError()
		if err != nil {
			return nil, err
		}
		if rules != nil {
			return rules, nil
		}
	}
	if source, ok := l.(types.LedgerAmendmentRulesSource); ok {
		if rules := source.LedgerAmendmentRules(); rules != nil {
			return rules, nil
		}
	}
	return amendment.EmptyRules(), nil
}

// RequireLedgerService checks that the ledger service is available
// on the request's service container. Returns an RpcError if not.
func RequireLedgerService(services *types.ServiceContainer) *types.RpcError {
	if services == nil || services.Ledger == nil {
		return rpcInternalInvariantError("rpc: ledger service unavailable")
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

// RequirePathSearch gates the path-finding RPCs on the startup path-search
// capability. The check belongs at each transport/handler entry point so a
// disabled server rejects before parsing request parameters or charging load.
func RequirePathSearch(ctx *types.RpcContext) *types.RpcError {
	if ctx == nil || ctx.Services == nil || ctx.Services.Capabilities.PathSearchMax == 0 {
		return types.RpcErrorNotSupported("")
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

// RequireNotBusyClient rejects non-admin RPC requests when the client job queue is full.
func RequireNotBusyClient(ctx *types.RpcContext) *types.RpcError {
	s := shedCheck(ctx)
	if s == nil {
		return nil
	}
	if s.InFlight() >= types.MaxJobQueueClients {
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
//  1. Admin/unlimited callers bypass the load and concurrency gates but still
//     increment the shared in-progress counter.
//  2. If in-flight RPCs exceed Tuning::maxPathfindJobCount (50), shed.
//  3. Otherwise CAS-increment the concurrent-path-find counter; if it
//     would exceed Tuning::maxPathfindsInProgress (2), shed.
//
// Returns a release func the caller MUST invoke (typically via defer)
// when admitted; release is nil on shed. Local fee pressure is checked even
// when the in-flight client counter is not wired.
func AcquirePathfind(ctx *types.RpcContext) (release func(), rpcErr *types.RpcError) {
	if ctx == nil || ctx.Services == nil {
		return func() {}, nil
	}
	services := ctx.Services
	s := services.ClientLoad
	if ctx.Unlimited {
		if s == nil {
			return func() {}, nil
		}
		s.AcquirePathfindUnlimited()
		return s.ReleasePathfind, nil
	}
	if services.IsLoadedLocal != nil && services.IsLoadedLocal() {
		return nil, types.RpcErrorTooBusy()
	}
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

// WaitPathfind queues a default-ledger request behind the bounded path-finding
// workers until a slot is available or the request is canceled.
func WaitPathfind(ctx *types.RpcContext) (release func(), rpcErr *types.RpcError) {
	if ctx == nil || ctx.Services == nil || ctx.Services.ClientLoad == nil {
		return func() {}, nil
	}
	if !ctx.Services.ClientLoad.WaitPathfind(ctx.Context) {
		return nil, types.RpcErrorTooBusy()
	}
	return ctx.Services.ClientLoad.ReleasePathfind, nil
}

// ParseParams unmarshals JSON params into dest, returning an RpcError on failure.
// If params is nil, dest is left untouched (zero value).
func ParseParams(params json.RawMessage, dest any) *types.RpcError {
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
		return types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return nil
}

type requestField[T any] struct {
	value   T
	present bool
}

func (f *requestField[T]) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		return errors.New("null request field")
	}
	if err := json.Unmarshal(data, &f.value); err != nil {
		return err
	}
	f.present = true
	return nil
}

type jsonCppBoolField struct {
	value   bool
	present bool
}

func (f *jsonCppBoolField) UnmarshalJSON(data []byte) error {
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	f.value = jsonCppBoolRaw(data)
	f.present = true
	return nil
}

type jsonCppStringField struct {
	value   string
	present bool
}

func (f *jsonCppStringField) UnmarshalJSON(data []byte) error {
	value, ok := jsonCppStringRaw(data)
	if !ok {
		return errors.New("request field is not string-convertible")
	}
	f.value = value
	f.present = true
	return nil
}

func decodeRequestObject(params json.RawMessage, dest any) *types.RpcError {
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(params, &object); err != nil || object == nil {
		return types.RpcErrorInvalidParams("Invalid parameters.")
	}
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return rpcErr
	}
	if err := json.Unmarshal(params, dest); err != nil {
		return types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return nil
}

func parseLedgerSpecifier(params json.RawMessage) (types.LedgerSpecifier, bool, *types.RpcError) {
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
		return types.LedgerSpecifier{}, false, types.RpcErrorInvalidParams("Invalid parameters.")
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

func validateJsonCppIntegerRange(params json.RawMessage) *types.RpcError {
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
		return types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return nil
}

func ValidateJsonCppIntegerRange(params json.RawMessage) *types.RpcError {
	return validateJsonCppIntegerRange(params)
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
		return types.RpcErrorActMalformed("Account malformed.")
	}
	if !types.IsValidXRPLAddress(account) {
		return types.RpcErrorActMalformed("Account malformed.")
	}
	return nil
}

func resolveLedgerSelector(input any) (string, *types.RpcError) {
	selection, rpcErr := parseLedgerSelectorInput(input, ledgerselector.Current())
	if rpcErr != nil {
		return "", rpcErr
	}
	return selection.String(), nil
}

func preflightAccountPage(
	ctx *types.RpcContext,
	params json.RawMessage,
	account string,
	internalDetail string,
	includeLedgerFieldsOnError bool,
) (string, map[string]any, *types.RpcError) {
	selection, rpcErr := parseLedgerSelectorParams(params, ledgerselector.Current())
	if rpcErr != nil {
		return "", nil, rpcErr
	}
	resolved, rpcErr := resolveLedgerSelection(ctx, selection)
	if rpcErr != nil {
		return "", nil, rpcErr
	}
	reader := resolved.Value
	ledgerIndex := strconv.FormatUint(uint64(reader.Sequence()), 10)
	ledgerFields := make(map[string]any, 3)
	fillResolvedLedgerFields(ledgerFields, reader, resolved.Validated)
	if rpcErr := ValidateAccount(account); rpcErr != nil {
		if includeLedgerFieldsOnError {
			rpcErr = rpcErr.WithExtra(ledgerFields)
		}
		return "", nil, rpcErr
	}
	if _, err := ctx.Services.Ledger.GetAccountInfo(ctx.Context, account, ledgerIndex); err != nil {
		rpcErr := mapAccountQueryErr(err, internalDetail)
		return "", nil, rpcErr
	}
	return ledgerIndex, ledgerFields, nil
}

func mergeLedgerFields(response, ledgerFields map[string]any) {
	for key, value := range ledgerFields {
		response[key] = value
	}
}

func accountPageParams(params json.RawMessage) (map[string]json.RawMessage, string, *types.RpcError) {
	var fields map[string]json.RawMessage
	if len(params) > 0 && !isJSONNull(params) {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, "", types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}
	rawAccount, ok := fields["account"]
	if !ok {
		return fields, "", types.RpcErrorMissingField("account")
	}
	var account string
	if isJSONNull(rawAccount) || json.Unmarshal(rawAccount, &account) != nil {
		return fields, "", types.RpcErrorInvalidField("account")
	}
	return fields, account, nil
}

func parseLedgerSelectorParams(
	params json.RawMessage,
	defaultSelection ledgerselector.Selector,
) (ledgerselector.Selector, *types.RpcError) {
	if len(params) == 0 || isJSONNull(params) {
		return defaultSelection, nil
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(params, &raw); err != nil {
		return ledgerselector.Selector{}, types.RpcErrorInvalidParams(fmt.Sprintf("Invalid parameters: %v", err))
	}
	return parseRawLedgerSelector(raw, defaultSelection, lookupLedgerSelectorErrors)
}

func parseLedgerSelectorInput(
	input any,
	defaultSelection ledgerselector.Selector,
) (ledgerselector.Selector, *types.RpcError) {
	switch value := input.(type) {
	case nil:
		return defaultSelection, nil
	case json.RawMessage:
		return parseLedgerSelectorParams(value, defaultSelection)
	case types.LedgerSpecifier:
		raw := make(map[string]json.RawMessage, 3)
		if value.Ledger != "" {
			raw["ledger"] = json.RawMessage(strconv.Quote(value.Ledger.String()))
		}
		if value.LedgerHash != "" {
			raw["ledger_hash"] = json.RawMessage(strconv.Quote(value.LedgerHash))
		}
		if value.LedgerIndex != "" {
			raw["ledger_index"] = json.RawMessage(strconv.Quote(value.LedgerIndex.String()))
		}
		return parseRawLedgerSelector(raw, defaultSelection, lookupLedgerSelectorErrors)
	default:
		return ledgerselector.Selector{}, types.RpcErrorInvalidParams("Invalid parameters.")
	}
}

type rawLedgerSelectorErrors struct {
	hashType       func() *types.RpcError
	hashMalformed  func() *types.RpcError
	indexType      func(field string) *types.RpcError
	indexMalformed func(field string) *types.RpcError
}

var lookupLedgerSelectorErrors = rawLedgerSelectorErrors{
	hashType: func() *types.RpcError {
		return types.RpcErrorExpectedField("ledger_hash", "hex string")
	},
	hashMalformed: func() *types.RpcError {
		return types.RpcErrorExpectedField("ledger_hash", "hex string")
	},
	indexType: func(field string) *types.RpcError {
		return types.RpcErrorExpectedField(field, "string or number")
	},
	indexMalformed: func(field string) *types.RpcError {
		return types.RpcErrorExpectedField(field, "string or number")
	},
}

func parseRawLedgerSelector(
	raw map[string]json.RawMessage,
	defaultSelection ledgerselector.Selector,
	errorStyle rawLedgerSelectorErrors,
) (ledgerselector.Selector, *types.RpcError) {
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
			return ledgerselector.Selector{}, types.RpcErrorInvalidParams(
				"Exactly one of 'ledger', 'ledger_hash', or 'ledger_index' can be specified.",
			)
		}
		return ledgerselector.Selector{}, types.RpcErrorInvalidParams(
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
	ctx *types.RpcContext,
	selection ledgerselector.Selector,
) (ledgerselector.Result[types.LedgerReader], *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return ledgerselector.Result[types.LedgerReader]{}, err
	}
	svc := ctx.Services.Ledger

	bySequence := func(sequence uint32) (types.LedgerReader, bool, error) {
		reader, err := svc.GetLedgerBySequence(sequence)
		return reader, reader != nil, err
	}
	byHash := func(hash [32]byte) (types.LedgerReader, bool, error) {
		reader, err := getLedgerByHashContext(ctx.Context, svc, hash)
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
		if selection.Kind() == ledgerselector.KindHash {
			if errors.Is(err, svcerr.ErrLedgerNotFound) || errors.Is(err, ledgerselector.ErrLedgerNotFound) {
				return ledgerselector.Result[types.LedgerReader]{}, types.RpcErrorLgrNotFound("ledgerNotFound")
			}
			return ledgerselector.Result[types.LedgerReader]{}, rpcInternalError("ledger lookup: hash query failed", err)
		}
		switch selection.Kind() {
		case ledgerselector.KindAbsent, ledgerselector.KindCurrent, ledgerselector.KindClosed, ledgerselector.KindValidated:
			if ctx.ApiVersion <= types.ApiVersion1 {
				return ledgerselector.Result[types.LedgerReader]{}, types.RpcErrorNoNetwork("InsufficientNetworkMode")
			}
			return ledgerselector.Result[types.LedgerReader]{}, types.RpcErrorNotSynced("notSynced")
		}
		return ledgerselector.Result[types.LedgerReader]{}, types.RpcErrorLgrNotFound("ledgerNotFound")
	}
	if selection.Kind() == ledgerselector.KindCurrent || selection.Kind() == ledgerselector.KindAbsent {
		resolved.Validated = false
	} else if selection.Kind() == ledgerselector.KindValidated {
		resolved.Validated = true
	}
	return resolved, nil
}

// LookupLedger resolves a request's ledger selector, defaulting to current.
func LookupLedger(ctx *types.RpcContext, input any) (types.LedgerReader, bool, *types.RpcError) {
	selection, rpcErr := parseLedgerSelectorInput(input, ledgerselector.Current())
	if rpcErr != nil {
		return nil, false, rpcErr
	}
	resolved, rpcErr := resolveLedgerSelection(ctx, selection)
	if rpcErr != nil {
		return nil, false, rpcErr
	}
	return resolved.Value, resolved.Validated, nil
}

func getLedgerByHashContext(ctx context.Context, svc types.LedgerService, hash [32]byte) (types.LedgerReader, error) {
	if contextual, ok := svc.(interface {
		GetLedgerByHashContext(context.Context, [32]byte) (types.LedgerReader, error)
	}); ok {
		return contextual.GetLedgerByHashContext(ctx, hash)
	}
	return svc.GetLedgerByHash(hash)
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

func mapAccountQueryErr(err error, internalDetail string) *types.RpcError {
	if rpcErr := mapLedgerLookupErr(err); rpcErr != nil {
		return rpcErr
	}
	switch {
	case errors.Is(err, svcerr.ErrAccountMalformed):
		return types.RpcErrorActMalformed("Account malformed.")
	case errors.Is(err, svcerr.ErrAccountNotFound):
		return types.RpcErrorActNotFound("Account not found.")
	case errors.Is(err, svcerr.ErrInvalidMarker):
		return types.RpcErrorInvalidField("marker")
	default:
		return rpcInternalError(internalDetail, err)
	}
}

// markerString extracts an opaque pagination marker while preserving the
// distinction between an absent member and a present JSON null.
func markerString(marker json.RawMessage) (string, *types.RpcError) {
	if marker == nil {
		return "", nil
	}
	if isJSONNull(marker) {
		return "", types.RpcErrorExpectedField("marker", "string")
	}
	var s string
	if err := json.Unmarshal(marker, &s); err != nil {
		return "", types.RpcErrorExpectedField("marker", "string")
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
func ReadLimitField(params json.RawMessage, r LimitRange, unlimited bool) (uint32, *types.RpcError) {
	raw, present := rawLimitField(params)
	if !present || isJSONNull(raw) {
		return r.Default, nil
	}
	var v uint32
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, types.RpcErrorExpectedField("limit", "unsigned integer")
	}
	if v == 0 {
		return 0, types.RpcErrorInvalidField("limit")
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

// arraySizeRpcError maps a binarycodec.Encode failure that is a JSON array-size
// overflow to invalidParams (matching rippled's STParsedJSON cap), returning nil
// for any other error so the caller keeps its existing mapping.
func arraySizeRpcError(err error) *types.RpcError {
	if msg, ok := binarycodec.AsArrayTooLargeError(err); ok {
		return types.RpcErrorInvalidParams(msg)
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
	return decodeTxBlobWithMetadataMode(data, false, true, false)
}

func decodeTxBlobForTx(data []byte) (StoredTransaction, error) {
	return decodeTxBlobWithMetadataMode(data, true, false, false)
}

// decodeOpenTxBlob decodes the transaction-only VL form returned for an
// accepted transaction that is still in the open ledger. Open rows must not
// carry metadata; closed transaction rows continue through decodeTxBlobForTx
// and retain their strict metadata validation.
func decodeOpenTxBlob(data []byte) (StoredTransaction, error) {
	txBytes, metaBytes, err := tx.SplitTxWithMetaBlob(data)
	if err != nil {
		return StoredTransaction{}, err
	}
	if metaBytes != nil {
		return StoredTransaction{}, errors.New("open transaction unexpectedly contains metadata")
	}
	txJSON, err := binarycodec.DecodeBytes(txBytes)
	if err != nil {
		return StoredTransaction{}, fmt.Errorf("decode open transaction: %w", err)
	}
	stored := StoredTransaction{TxJSON: txJSON}
	if err := validateStoredTransaction(&stored, false); err != nil {
		return StoredTransaction{}, err
	}
	return stored, nil
}

func decodeTxBlobForTransactionEntry(data []byte) (StoredTransaction, error) {
	return decodeTxBlobWithMetadataMode(data, false, true, true)
}

func decodeTxBlobWithMetadataMode(data []byte, requireMetadataFields, preserveEmptyMetadata, requireBinaryMetadata bool) (StoredTransaction, error) {
	// Try VL-encoded binary format first
	txBytes, metaBytes, err := tx.SplitTxWithMetaBlob(data)
	if err == nil {
		if requireBinaryMetadata && metaBytes == nil {
			return StoredTransaction{}, errors.New("stored transaction is missing binary metadata")
		}
		txJSON, decErr := binarycodec.DecodeBytes(txBytes)
		if decErr == nil {
			st := StoredTransaction{TxJSON: txJSON}
			if len(metaBytes) > 0 {
				metaJSON, metaErr := binarycodec.DecodeBytes(metaBytes)
				if metaErr != nil {
					return StoredTransaction{}, fmt.Errorf("decode transaction metadata: %w", metaErr)
				}
				st.Meta = metaJSON
			} else if metaBytes != nil && (preserveEmptyMetadata || requireMetadataFields) {
				st.Meta = map[string]any{}
			}
			if err := validateStoredTransaction(&st, requireMetadataFields); err != nil {
				return StoredTransaction{}, err
			}
			return st, nil
		}
	}

	// Fall back to JSON format
	var st StoredTransaction
	if jsonErr := json.Unmarshal(data, &st); jsonErr != nil {
		return StoredTransaction{}, jsonErr
	}
	if err := validateStoredTransaction(&st, requireMetadataFields); err != nil {
		return StoredTransaction{}, err
	}
	return st, nil
}

func validateStoredTransaction(st *StoredTransaction, requireMetadataFields bool) error {
	if st.TxJSON == nil {
		return errors.New("stored transaction is missing tx_json")
	}
	canonicalTx, err := tx.CanonicalizeSerializedTransaction(st.TxJSON)
	if err != nil {
		return fmt.Errorf("validate stored transaction: %w", err)
	}
	st.TxJSON = canonicalTx
	if st.Meta != nil {
		var canonicalMeta map[string]any
		if requireMetadataFields {
			canonicalMeta, err = tx.CanonicalizeSerializedMetadata(st.Meta)
		} else {
			canonicalMeta, err = tx.CanonicalizeSerializedObject(st.Meta)
		}
		if err != nil {
			return fmt.Errorf("validate stored transaction metadata: %w", err)
		}
		st.Meta = canonicalMeta
	}
	return nil
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
