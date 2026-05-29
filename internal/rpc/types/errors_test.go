package types

import "testing"

// rippledEnum is rippled's error_code_i enum (ErrorCodes.h:42-160), the source
// of truth for the numeric error_code field on the wire. Every goXRPL constant
// that mirrors a rippled code is pinned to its value here. If rippled appends a
// code, add the row; if a goXRPL constant drifts, this test fails.
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
}

func TestErrorCodesMatchRippledEnum(t *testing.T) {
	for _, c := range rippledEnum {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (rippled error_code_i)", c.name, c.got, c.want)
		}
	}
}

// No two distinct rippled-enum codes may share a positive integer. rippled's
// own enum forbids re-using a value (ErrorCodes.h:38); goXRPL previously
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

// goXRPL-specific codes must not collide with any real rippled enum value, so a
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
		{"RpcSHUT_DOWN", RpcSHUT_DOWN},
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
		err   *RpcError
		token string
		code  int
	}{
		{RpcErrorUnknown("x"), "unknown", RpcUNKNOWN},
		{RpcErrorInvalidParams("x"), "invalidParams", 31},
		{RpcErrorMethodNotFound("m"), "unknownCmd", 32},
		{RpcErrorLgrNotFound("x"), "lgrNotFound", 21},
		{RpcErrorActNotFound("x"), "actNotFound", 19},
		{RpcErrorActMalformed("x"), "actMalformed", 35},
		{RpcErrorTxnNotFound("x"), "txnNotFound", 29},
		{RpcErrorInternal("x"), "internal", 73},
		{RpcErrorNoPermission("m"), "noPermission", 6},
		{RpcErrorForbidden("m"), "forbidden", 3},
		{RpcErrorTooBusy(), "tooBusy", 9},
		{RpcErrorSlowDown("x"), "slowDown", 10},
		{RpcErrorNotEnabled(""), "notEnabled", 12},
		{RpcErrorNotSupported(""), "notSupported", 75},
		{RpcErrorNoEvents(""), "noEvents", 7},
		{RpcErrorAmendmentBlocked(), "amendmentBlocked", 14},
		{RpcErrorBadFeature("x"), "badFeature", 40},
		{RpcErrorNoPathRequest(), "noPathRequest", 33},
		{RpcErrorObjectNotFound("x"), "objectNotFound", 92},
		{RpcErrorBadCredentials("x"), "badCredentials", 95},
		{RpcErrorHighFee("x"), "highFee", 11},
		{RpcErrorSigningMalformed(), "signingMalformed", 63},
		{RpcErrorPublicMalformed(), "publicMalformed", 62},
		{RpcErrorTxSigned(), "transactionSigned", 96},
		{RpcErrorSrcActMalformed("x"), "srcActMalformed", 65},
		{RpcErrorNotImpl(), "notImpl", 74},
		{RpcErrorOracleMalformed(), "oracleMalformed", 94},
		{RpcErrorEntryNotFound("x"), "entryNotFound", RpcUNKNOWN},
		{RpcErrorTransactionNotFound("x"), "transactionNotFound", RpcUNKNOWN},
		{RpcErrorNotStandalone("x"), "notStandAlone", RpcUNKNOWN},
		{RpcErrorUnknownOption("x"), "unknownOption", RpcUNKNOWN},
		{RpcErrorSrcActMissing("x"), "srcActMissing", 66},
		{RpcErrorSrcActNotFound("x"), "srcActNotFound", 67},
		{RpcErrorSrcCurMalformed("x"), "srcCurMalformed", 69},
		{RpcErrorDstAmtMalformed("x"), "dstAmtMalformed", 51},
		{RpcErrorSrcIsrMalformed("x"), "srcIsrMalformed", 70},
		{RpcErrorDstIsrMalformed("x"), "dstIsrMalformed", 53},
		{RpcErrorBadMarket(), "badMarket", 42},
		{RpcErrorDomainMalformed(""), "domainMalformed", 97},
		{RpcErrorDstActNotFound("x"), "dstActNotFound", 50},
		{RpcErrorFieldNotFoundTransaction(), "fieldNotFoundTransaction", RpcUNKNOWN},
		{RpcErrorInvalidApiVersion("3"), "invalidApiVersion", RpcINVALID_API_VERSION},
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

// Bare-token errors mirror rippled handlers that set jvResult[jss::error]
// directly (e.g. VaultInfo.cpp:101, LedgerEntry.cpp:1044,
// TransactionEntry.cpp:71): only `error` is wired, never error_code or
// error_message. Errors built through inject_error keep all three fields.
func TestBareTokenErrors(t *testing.T) {
	bare := []*RpcError{
		RpcErrorEntryNotFound("x"),
		RpcErrorTransactionNotFound("x"),
		RpcErrorUnknownOption("x"),
		RpcErrorFieldNotFoundTransaction(),
		RpcErrorNotStandalone("x"),
	}
	for _, e := range bare {
		if !e.IsBareToken() {
			t.Errorf("%q should be a bare token", e.ErrorString)
		}
	}
	notBare := []*RpcError{
		RpcErrorTxnNotFound("x"),
		RpcErrorLgrNotFound("x"),
		RpcErrorInternal("x"),
		RpcErrorActNotFound("x"),
	}
	for _, e := range notBare {
		if e.IsBareToken() {
			t.Errorf("%q must not be a bare token", e.ErrorString)
		}
	}
}
