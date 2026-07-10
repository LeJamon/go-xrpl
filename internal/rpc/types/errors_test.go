package types

import "testing"

// rippledEnum is rippled's error_code_i enum (ErrorCodes.h:42-160), the source
// of truth for the numeric error_code field on the wire. Every go-xrpl constant
// that mirrors a rippled code is pinned to its value here. If rippled appends a
// code, add the row; if a go-xrpl constant drifts, this test fails.
var rippledEnum = []struct {
	name string
	got  int
	want int
}{
	{"RpcUNKNOWN", RpcUNKNOWN, -1},
	{"RpcBAD_SYNTAX", RpcBAD_SYNTAX, 1},
	{"RpcJSON_RPC", RpcJSON_RPC, 2},
	{"RpcFORBIDDEN", RpcFORBIDDEN, 3},
	{"RpcWRONG_NETWORK", RpcWRONG_NETWORK, 4},
	{"RpcNO_PERMISSION", RpcNO_PERMISSION, 6},
	{"RpcNO_EVENTS", RpcNO_EVENTS, 7},
	{"RpcTOO_BUSY", RpcTOO_BUSY, 9},
	{"RpcSLOW_DOWN", RpcSLOW_DOWN, 10},
	{"RpcHIGH_FEE", RpcHIGH_FEE, 11},
	{"RpcNOT_ENABLED", RpcNOT_ENABLED, 12},
	{"RpcNOT_READY", RpcNOT_READY, 13},
	{"RpcAMENDMENT_BLOCKED", RpcAMENDMENT_BLOCKED, 14},
	{"RpcNO_CLOSED", RpcNO_CLOSED, 15},
	{"RpcNO_CURRENT", RpcNO_CURRENT, 16},
	{"RpcNO_NETWORK", RpcNO_NETWORK, 17},
	{"RpcNOT_SYNCED", RpcNOT_SYNCED, 18},
	{"RpcACT_NOT_FOUND", RpcACT_NOT_FOUND, 19},
	{"RpcLGR_NOT_FOUND", RpcLGR_NOT_FOUND, 21},
	{"RpcLGR_NOT_VALIDATED", RpcLGR_NOT_VALIDATED, 22},
	{"RpcMASTER_DISABLED", RpcMASTER_DISABLED, 23},
	{"RpcTXN_NOT_FOUND", RpcTXN_NOT_FOUND, 29},
	{"RpcINVALID_HOTWALLET", RpcINVALID_HOTWALLET, 30},
	{"RpcINVALID_PARAMS", RpcINVALID_PARAMS, 31},
	{"RpcMETHOD_NOT_FOUND", RpcMETHOD_NOT_FOUND, 32}, // rippled rpcUNKNOWN_COMMAND
	{"RpcNO_PF_REQUEST", RpcNO_PF_REQUEST, 33},
	{"RpcACT_MALFORMED", RpcACT_MALFORMED, 35},
	{"RpcALREADY_MULTISIG", RpcALREADY_MULTISIG, 36},
	{"RpcALREADY_SINGLE_SIG", RpcALREADY_SINGLE_SIG, 37},
	{"RpcBAD_FEATURE", RpcBAD_FEATURE, 40},
	{"RpcBAD_ISSUER", RpcBAD_ISSUER, 41},
	{"RpcBAD_MARKET", RpcBAD_MARKET, 42},
	{"RpcBAD_SECRET", RpcBAD_SECRET, 43},
	{"RpcBAD_SEED", RpcBAD_SEED, 44},
	{"RpcCHANNEL_MALFORMED", RpcCHANNEL_MALFORMED, 45},
	{"RpcCHANNEL_AMT_MALFORMED", RpcCHANNEL_AMT_MALFORMED, 46},
	{"RpcMISSING_COMMAND", RpcMISSING_COMMAND, 47}, // rippled rpcCOMMAND_MISSING
	{"RpcDST_ACT_MALFORMED", RpcDST_ACT_MALFORMED, 48},
	{"RpcDST_ACT_MISSING", RpcDST_ACT_MISSING, 49},
	{"RpcDST_ACT_NOT_FOUND", RpcDST_ACT_NOT_FOUND, 50},
	{"RpcDST_AMT_MALFORMED", RpcDST_AMT_MALFORMED, 51},
	{"RpcDST_AMT_MISSING", RpcDST_AMT_MISSING, 52},
	{"RpcDST_ISR_MALFORMED", RpcDST_ISR_MALFORMED, 53},
	{"RpcLGR_IDXS_INVALID", RpcLGR_IDXS_INVALID, 57},
	{"RpcLGR_IDX_MALFORMED", RpcLGR_IDX_MALFORMED, 58},
	{"RpcPUBLIC_MALFORMED", RpcPUBLIC_MALFORMED, 62},
	{"RpcSIGNING_MALFORMED", RpcSIGNING_MALFORMED, 63},
	{"RpcSENDMAX_MALFORMED", RpcSENDMAX_MALFORMED, 64},
	{"RpcSRC_ACT_MALFORMED", RpcSRC_ACT_MALFORMED, 65},
	{"RpcSRC_ACT_MISSING", RpcSRC_ACT_MISSING, 66},
	{"RpcSRC_ACT_NOT_FOUND", RpcSRC_ACT_NOT_FOUND, 67},
	{"RpcDELEGATE_ACT_NOT_FOUND", RpcDELEGATE_ACT_NOT_FOUND, 68},
	{"RpcSRC_CUR_MALFORMED", RpcSRC_CUR_MALFORMED, 69},
	{"RpcSRC_ISR_MALFORMED", RpcSRC_ISR_MALFORMED, 70},
	{"RpcSTREAM_MALFORMED", RpcSTREAM_MALFORMED, 71},
	{"RpcATX_DEPRECATED", RpcATX_DEPRECATED, 72},
	{"RpcINTERNAL", RpcINTERNAL, 73},
	{"RpcNOT_IMPL", RpcNOT_IMPL, 74},
	{"RpcNOT_SUPPORTED", RpcNOT_SUPPORTED, 75},
	{"RpcBAD_KEY_TYPE", RpcBAD_KEY_TYPE, 76},
	{"RpcDB_DESERIALIZATION", RpcDB_DESERIALIZATION, 77},
	{"RpcEXCESSIVE_LGR_RANGE", RpcEXCESSIVE_LGR_RANGE, 78},
	{"RpcINVALID_LGR_RANGE", RpcINVALID_LGR_RANGE, 79},
	{"RpcEXPIRED_VALIDATOR_LIST", RpcEXPIRED_VALIDATOR_LIST, 80},
	{"RpcREPORTING_UNSUPPORTED", RpcREPORTING_UNSUPPORTED, 91},
	{"RpcOBJECT_NOT_FOUND", RpcOBJECT_NOT_FOUND, 92},
	{"RpcISSUE_MALFORMED", RpcISSUE_MALFORMED, 93},
	{"RpcORACLE_MALFORMED", RpcORACLE_MALFORMED, 94},
	{"RpcBAD_CREDENTIALS", RpcBAD_CREDENTIALS, 95},
	{"RpcTX_SIGNED", RpcTX_SIGNED, 96},
	{"RpcDOMAIN_MALFORMED", RpcDOMAIN_MALFORMED, 97},
	{"RpcENTRY_NOT_FOUND", RpcENTRY_NOT_FOUND, 98},
	{"RpcUNEXPECTED_LEDGER_TYPE", RpcUNEXPECTED_LEDGER_TYPE, 99},
}

func TestErrorCodesMatchRippledEnum(t *testing.T) {
	for _, c := range rippledEnum {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (rippled error_code_i)", c.name, c.got, c.want)
		}
	}
}

// No two distinct rippled-enum codes may share a positive integer. rippled's
// own enum forbids re-using a value (ErrorCodes.h:38); go-xrpl previously
// aliased several integers to two tokens, which this guards against.
func TestErrorCodesArePositivelyUnique(t *testing.T) {
	seen := map[int]string{}
	for _, c := range rippledEnum {
		if c.want <= 0 {
			continue
		}
		if prev, ok := seen[c.want]; ok {
			t.Errorf("code %d assigned to both %s and %s", c.want, prev, c.name)
		}
		seen[c.want] = c.name
	}
}

// go-xrpl-specific codes must not collide with any real rippled enum value, so a
// response carrying one is never mistaken for a different rippled error.
func TestGoxrplSpecificCodesDoNotCollide(t *testing.T) {
	rippledValues := map[int]bool{}
	for _, c := range rippledEnum {
		if c.want > 0 {
			rippledValues[c.want] = true
		}
	}
	// 38 is an explicitly-unused rippled slot; -1 is rippled's "not enumerated".
	for _, c := range []struct {
		name string
		code int
	}{
		{"RpcINVALID_API_VERSION", RpcINVALID_API_VERSION},
		{"RpcNOT_STANDALONE", RpcNOT_STANDALONE},
	} {
		if rippledValues[c.code] {
			t.Errorf("%s = %d collides with a rippled error_code_i value", c.name, c.code)
		}
	}
}

// Constructors must emit the rippled token paired with the rippled code
// (ErrorCodes.cpp:51-120). This catches a constructor wired to the wrong slot.
func TestErrorConstructorsTokenCodePairs(t *testing.T) {
	cases := []struct {
		err   *RPCError
		token string
		code  int
	}{
		{RPCErrorUnknown("x"), "unknown", RpcUNKNOWN},
		{RPCErrorInvalidParams("x"), "invalidParams", 31},
		{RPCErrorMethodNotFound("m"), "unknownCmd", 32},
		{RPCErrorLgrNotFound("x"), "lgrNotFound", 21},
		{RPCErrorActNotFound("x"), "actNotFound", 19},
		{RPCErrorActMalformed("x"), "actMalformed", 35},
		{RPCErrorTxnNotFound("x"), "txnNotFound", 29},
		{RPCErrorInvalidHotWallet(), "invalidHotWallet", 30},
		{RPCErrorInternal("x"), "internal", 73},
		{RPCErrorNoPermission("m"), "noPermission", 6},
		{RPCErrorForbidden("m"), "forbidden", 3},
		{RPCErrorTooBusy(), "tooBusy", 9},
		{RPCErrorSlowDown("x"), "slowDown", 10},
		{RPCErrorNotEnabled(""), "notEnabled", 12},
		{RPCErrorNotSupported(""), "notSupported", 75},
		{RPCErrorNoEvents(""), "noEvents", 7},
		{RPCErrorAmendmentBlocked(), "amendmentBlocked", 14},
		{RPCErrorBadFeature("x"), "badFeature", 40},
		{RPCErrorNoPathRequest(), "noPathRequest", 33},
		{RPCErrorObjectNotFound("x"), "objectNotFound", 92},
		{RPCErrorBadCredentials("x"), "badCredentials", 95},
		{RPCErrorHighFee("x"), "highFee", 11},
		{RPCErrorSigningMalformed(), "signingMalformed", 63},
		{RPCErrorPublicMalformed(), "publicMalformed", 62},
		{RPCErrorTxSigned(), "transactionSigned", 96},
		{RPCErrorSrcActMalformed("x"), "srcActMalformed", 65},
		{RPCErrorNotImpl(), "notImpl", 74},
		{RPCErrorOracleMalformed(), "oracleMalformed", 94},
		{RPCErrorEntryNotFound("x"), "entryNotFound", RpcENTRY_NOT_FOUND},
		{RPCErrorEntryNotFoundBare("x"), "entryNotFound", RpcUNKNOWN},
		{RPCErrorUnexpectedLedgerType(), "unexpectedLedgerType", RpcUNEXPECTED_LEDGER_TYPE},
		{RPCErrorTransactionNotFound("x"), "transactionNotFound", RpcUNKNOWN},
		{RPCErrorNotStandalone("x"), "notStandAlone", RpcUNKNOWN},
		{RPCErrorUnknownOption("x"), "unknownOption", RpcUNKNOWN},
		{RPCErrorSrcActMissing("x"), "srcActMissing", 66},
		{RPCErrorSrcActNotFound("x"), "srcActNotFound", 67},
		{RPCErrorSrcCurMalformed("x"), "srcCurMalformed", 69},
		{RPCErrorDstAmtMalformed("x"), "dstAmtMalformed", 51},
		{RPCErrorSrcIsrMalformed("x"), "srcIsrMalformed", 70},
		{RPCErrorDstIsrMalformed("x"), "dstIsrMalformed", 53},
		{RPCErrorBadMarket(), "badMarket", 42},
		{RPCErrorDomainMalformed(""), "domainMalformed", 97},
		{RPCErrorDstActNotFound("x"), "dstActNotFound", 50},
		{RPCErrorFieldNotFoundTransaction(), "fieldNotFoundTransaction", RpcUNKNOWN},
		{RPCErrorInvalidApiVersion("3"), "invalid_API_version", RpcINVALID_API_VERSION},
	}
	for _, c := range cases {
		if c.err.ErrorString != c.token {
			t.Errorf("token = %q, want %q", c.err.ErrorString, c.token)
		}
		if c.err.Code != c.code {
			t.Errorf("token %q code = %d, want %d", c.token, c.err.Code, c.code)
		}
	}
}

// TestInternalErrorHidesDetail pins the rpcINTERNAL contract: the wire message
// is always the fixed rippled string, and the caller's detail is retained only
// on LogDetail for server-side logging — never serialized. This prevents
// leaking internal storage/codec state to the client and forking the API from
// rippled's fixed "Internal error." on every internal failure.
func TestInternalErrorHidesDetail(t *testing.T) {
	const detail = "pebble: key aabbcc size=4096 not found"
	e := RPCErrorInternal(detail)

	if e.Message != InternalErrorMessage {
		t.Errorf("wire message = %q, want the fixed %q", e.Message, InternalErrorMessage)
	}
	if e.Message != "Internal error." {
		t.Errorf("wire message = %q, want rippled's exact %q", e.Message, "Internal error.")
	}
	if e.LogDetail() != detail {
		t.Errorf("LogDetail = %q, want %q", e.LogDetail(), detail)
	}
	// The detail must not appear on any serialized field.
	if e.ErrorString == detail || e.Type == detail {
		t.Errorf("internal detail leaked onto a serialized field")
	}
}

// TestLogDetailEmptyForNonInternal confirms only rpcINTERNAL carries a log
// detail, so the dispatcher's detail-logging branch never fires for ordinary
// user errors.
func TestLogDetailEmptyForNonInternal(t *testing.T) {
	if got := RPCErrorInvalidParams("bad").LogDetail(); got != "" {
		t.Errorf("LogDetail = %q, want empty for a non-internal error", got)
	}
	if got := (*RPCError)(nil).LogDetail(); got != "" {
		t.Errorf("nil-receiver LogDetail = %q, want empty", got)
	}
}

// Bare-token errors mirror rippled handlers that set jvResult[jss::error]
// directly (e.g. VaultInfo.cpp:101, TransactionEntry.cpp:71): only `error` is
// wired, never error_code or error_message. Errors built through inject_error
// keep all three fields.
func TestBareTokenErrors(t *testing.T) {
	bare := []*RPCError{
		RPCErrorEntryNotFoundBare("x"),
		RPCErrorTransactionNotFound("x"),
		RPCErrorUnknownOption("x"),
		RPCErrorFieldNotFoundTransaction(),
		RPCErrorNotStandalone("x"),
		// rippled emits invalid_API_version bare on every transport that still
		// carries a result envelope (WS, batch-via-result): no code, no message.
		RPCErrorInvalidApiVersion("3"),
	}
	for _, e := range bare {
		if !e.IsBareToken() {
			t.Errorf("%q should be a bare token", e.ErrorString)
		}
	}
	notBare := []*RPCError{
		RPCErrorTxnNotFound("x"),
		RPCErrorLgrNotFound("x"),
		RPCErrorInternal("x"),
		RPCErrorActNotFound("x"),
		// rippled 3.0.0 promoted these ledger_entry paths to inject_error.
		RPCErrorEntryNotFound("x"),
		RPCErrorUnexpectedLedgerType(),
	}
	for _, e := range notBare {
		if e.IsBareToken() {
			t.Errorf("%q must not be a bare token", e.ErrorString)
		}
	}
}

// RPCErrorEntryNotFound defaults an empty message to rippled's canonical
// "Entry not found." string (ErrorCodes.cpp:121); RPCErrorEntryNotFoundBare
// carries whatever the handler passes.
func TestEntryNotFoundDefaultMessage(t *testing.T) {
	if got := RPCErrorEntryNotFound("").Message; got != "Entry not found." {
		t.Errorf("default message = %q, want %q", got, "Entry not found.")
	}
	if got := RPCErrorEntryNotFound("custom").Message; got != "custom" {
		t.Errorf("explicit message = %q, want %q", got, "custom")
	}
}

// TestInvalidApiVersionError pins the rippled-exact token and the transport
// marker that drives the per-transport wire shape. rippled emits the bare
// jss::invalid_API_version token with no numeric code or message
// (ServerHandler.cpp:454-455, 689, 694-695).
func TestInvalidApiVersionError(t *testing.T) {
	e := RPCErrorInvalidApiVersion("3")
	if e.ErrorString != InvalidApiVersionToken {
		t.Errorf("token = %q, want %q", e.ErrorString, InvalidApiVersionToken)
	}
	if e.ErrorString != "invalid_API_version" {
		t.Errorf("token = %q, want underscored rippled spelling", e.ErrorString)
	}
	if !e.IsInvalidApiVersion() {
		t.Error("RPCErrorInvalidApiVersion should report IsInvalidApiVersion")
	}
	if e.Message != "" {
		t.Errorf("error_message = %q, want empty (rippled omits it)", e.Message)
	}
	// Only the invalid-version error carries the transport marker.
	if RPCErrorInternal("x").IsInvalidApiVersion() {
		t.Error("unrelated error must not report IsInvalidApiVersion")
	}
}
