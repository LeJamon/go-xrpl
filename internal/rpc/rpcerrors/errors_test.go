package rpcerrors

import (
	"encoding/json"
	"testing"
)

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
		err   *RpcError
		token string
		code  int
	}{
		{RpcErrorInvalidParams("x"), "invalidParams", 31},
		{RpcErrorMethodNotFound(), "unknownCmd", 32},
		{RpcErrorLgrNotFound("x"), "lgrNotFound", 21},
		{RpcErrorActNotFound("x"), "actNotFound", 19},
		{RpcErrorActMalformed("x"), "actMalformed", 35},
		{RpcErrorTxnNotFound("x"), "txnNotFound", 29},
		{RpcErrorInvalidHotWallet(), "invalidHotWallet", 30},
		{RpcErrorInternal(), "internal", 73},
		{RpcErrorTransactionSubmission(), "internal", 73},
		{RpcErrorDBDeserialization(), "dbDeserialization", 77},
		{RpcErrorNoPermission("m"), "noPermission", 6},
		{RpcErrorForbidden("m"), "forbidden", 3},
		{RpcErrorTooBusy(), "tooBusy", 9},
		{RpcErrorNotEnabled(""), "notEnabled", 12},
		{RpcErrorNotSupported(""), "notSupported", 75},
		{RpcErrorNoEvents(""), "noEvents", 7},
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
		{RpcErrorEntryNotFound("x"), "entryNotFound", RpcENTRY_NOT_FOUND},
		{RpcErrorEntryNotFoundBare("x"), "entryNotFound", RpcUNKNOWN},
		{RpcErrorUnexpectedLedgerType(), "unexpectedLedgerType", RpcUNEXPECTED_LEDGER_TYPE},
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
		{RpcErrorInvalidApiVersion("3"), "invalid_API_version", RpcINVALID_API_VERSION},
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

func TestRpcErrorMethodNotFoundMessage(t *testing.T) {
	if got := RpcErrorMethodNotFound().Message; got != "Unknown method." {
		t.Fatalf("message = %q, want %q", got, "Unknown method.")
	}
}

func TestRpcErrorDBDeserialization(t *testing.T) {
	err := RpcErrorDBDeserialization()
	if err.Message != "Database deserialization error." {
		t.Fatalf("message = %q, want %q", err.Message, "Database deserialization error.")
	}
}

func TestRpcErrorInvalidTransactionType(t *testing.T) {
	err := RpcErrorInvalidTransactionType(65535)
	if err.Code != RpcINTERNAL || err.ErrorString != "internal" {
		t.Fatalf("error = %#v, want internal RPC error", err)
	}
	want := "Exception while serializing transaction: Invalid transaction type 65535"
	if err.Message != want {
		t.Fatalf("message = %q, want %q", err.Message, want)
	}
}

// Bare-token errors mirror rippled handlers that set jvResult[jss::error]
// directly (e.g. VaultInfo.cpp:101, TransactionEntry.cpp:71): only `error` is
// wired, never error_code or error_message. Errors built through inject_error
// keep all three fields.
func TestBareTokenErrors(t *testing.T) {
	bare := []*RpcError{
		RpcErrorEntryNotFoundBare("x"),
		RpcErrorTransactionNotFound("x"),
		RpcErrorUnknownOption("x"),
		RpcErrorFieldNotFoundTransaction(),
		RpcErrorNotStandalone("x"),
		// rippled emits invalid_API_version bare on every transport that still
		// carries a result envelope (WS, batch-via-result): no code, no message.
		RpcErrorInvalidApiVersion("3"),
	}
	for _, e := range bare {
		if !e.IsBareToken() {
			t.Errorf("%q should be a bare token", e.ErrorString)
		}
	}
	notBare := []*RpcError{
		RpcErrorTxnNotFound("x"),
		RpcErrorLgrNotFound("x"),
		RpcErrorInternal(),
		RpcErrorActNotFound("x"),
		RpcErrorEntryNotFound("x"),
		RpcErrorUnexpectedLedgerType(),
	}
	for _, e := range notBare {
		if e.IsBareToken() {
			t.Errorf("%q must not be a bare token", e.ErrorString)
		}
	}
}

// RpcErrorEntryNotFound defaults an empty message to rippled's canonical
// "Entry not found." string (ErrorCodes.cpp:121); RpcErrorEntryNotFoundBare
// carries whatever the handler passes.
func TestEntryNotFoundDefaultMessage(t *testing.T) {
	if got := RpcErrorEntryNotFound("").Message; got != "Entry not found." {
		t.Errorf("default message = %q, want %q", got, "Entry not found.")
	}
	if got := RpcErrorEntryNotFound("custom").Message; got != "custom" {
		t.Errorf("explicit message = %q, want %q", got, "custom")
	}
}

var rippledErrorInfos = []rpcErrorInfo{
	{RpcACT_MALFORMED, "actMalformed", "Account malformed.", 200},
	{RpcACT_NOT_FOUND, "actNotFound", "Account not found.", 200},
	{RpcALREADY_MULTISIG, "alreadyMultisig", "Already multisigned.", 200},
	{RpcALREADY_SINGLE_SIG, "alreadySingleSig", "Already single-signed.", 200},
	{RpcAMENDMENT_BLOCKED, "amendmentBlocked", "Amendment blocked, need upgrade.", 503},
	{RpcEXPIRED_VALIDATOR_LIST, "unlBlocked", "Validator list expired.", 503},
	{RpcATX_DEPRECATED, "deprecated", "Use the new API or specify a ledger range.", 400},
	{RpcBAD_KEY_TYPE, "badKeyType", "Bad key type.", 400},
	{RpcBAD_FEATURE, "badFeature", "Feature unknown or invalid.", 500},
	{RpcBAD_ISSUER, "badIssuer", "Issuer account malformed.", 400},
	{RpcBAD_MARKET, "badMarket", "No such market.", 404},
	{RpcBAD_SECRET, "badSecret", "Secret does not match account.", 403},
	{RpcBAD_SEED, "badSeed", "Disallowed seed.", 403},
	{RpcBAD_SYNTAX, "badSyntax", "Syntax error.", 400},
	{RpcCHANNEL_MALFORMED, "channelMalformed", "Payment channel is malformed.", 400},
	{RpcCHANNEL_AMT_MALFORMED, "channelAmtMalformed", "Payment channel amount is malformed.", 400},
	{RpcMISSING_COMMAND, "commandMissing", "Missing command entry.", 400},
	{RpcDB_DESERIALIZATION, "dbDeserialization", "Database deserialization error.", 502},
	{RpcDST_ACT_MALFORMED, "dstActMalformed", "Destination account is malformed.", 400},
	{RpcDST_ACT_MISSING, "dstActMissing", "Destination account not provided.", 400},
	{RpcDST_ACT_NOT_FOUND, "dstActNotFound", "Destination account not found.", 404},
	{RpcDST_AMT_MALFORMED, "dstAmtMalformed", "Destination amount/currency/issuer is malformed.", 400},
	{RpcDST_AMT_MISSING, "dstAmtMissing", "Destination amount/currency/issuer is missing.", 400},
	{RpcDST_ISR_MALFORMED, "dstIsrMalformed", "Destination issuer is malformed.", 400},
	{RpcEXCESSIVE_LGR_RANGE, "excessiveLgrRange", "Ledger range exceeds 1000.", 400},
	{RpcFORBIDDEN, "forbidden", "Bad credentials.", 403},
	{RpcHIGH_FEE, "highFee", "Current transaction fee exceeds your limit.", 402},
	{RpcINTERNAL, "internal", "Internal error.", 500},
	{RpcINVALID_LGR_RANGE, "invalidLgrRange", "Ledger range is invalid.", 400},
	{RpcINVALID_PARAMS, "invalidParams", "Invalid parameters.", 400},
	{RpcINVALID_HOTWALLET, "invalidHotWallet", "Invalid hotwallet.", 400},
	{RpcISSUE_MALFORMED, "issueMalformed", "Issue is malformed.", 400},
	{RpcJSON_RPC, "json_rpc", "JSON-RPC transport error.", 500},
	{RpcLGR_IDXS_INVALID, "lgrIdxsInvalid", "Ledger indexes invalid.", 400},
	{RpcLGR_IDX_MALFORMED, "lgrIdxMalformed", "Ledger index malformed.", 400},
	{RpcLGR_NOT_FOUND, "lgrNotFound", "Ledger not found.", 404},
	{RpcLGR_NOT_VALIDATED, "lgrNotValidated", "Ledger not validated.", 202},
	{RpcMASTER_DISABLED, "masterDisabled", "Master key is disabled.", 403},
	{RpcNOT_ENABLED, "notEnabled", "Not enabled in configuration.", 501},
	{RpcNOT_IMPL, "notImpl", "Not implemented.", 501},
	{RpcNOT_READY, "notReady", "Not ready to handle this request.", 503},
	{RpcNOT_SUPPORTED, "notSupported", "Operation not supported.", 501},
	{RpcNO_CLOSED, "noClosed", "Closed ledger is unavailable.", 503},
	{RpcNO_CURRENT, "noCurrent", "Current ledger is unavailable.", 503},
	{RpcNOT_SYNCED, "notSynced", "Not synced to the network.", 503},
	{RpcNO_EVENTS, "noEvents", "Current transport does not support events.", 405},
	{RpcNO_NETWORK, "noNetwork", "Not synced to the network.", 503},
	{RpcWRONG_NETWORK, "wrongNetwork", "Wrong network.", 503},
	{RpcNO_PERMISSION, "noPermission", "You don't have permission for this command.", 401},
	{RpcNO_PF_REQUEST, "noPathRequest", "No pathfinding request in progress.", 404},
	{RpcOBJECT_NOT_FOUND, "objectNotFound", "The requested object was not found.", 404},
	{RpcPUBLIC_MALFORMED, "publicMalformed", "Public key is malformed.", 400},
	{RpcSENDMAX_MALFORMED, "sendMaxMalformed", "SendMax amount malformed.", 400},
	{RpcSIGNING_MALFORMED, "signingMalformed", "Signing of transaction is malformed.", 400},
	{RpcSLOW_DOWN, "slowDown", "You are placing too much load on the server.", 429},
	{RpcSRC_ACT_MALFORMED, "srcActMalformed", "Source account is malformed.", 400},
	{RpcSRC_ACT_MISSING, "srcActMissing", "Source account not provided.", 400},
	{RpcSRC_ACT_NOT_FOUND, "srcActNotFound", "Source account not found.", 404},
	{RpcDELEGATE_ACT_NOT_FOUND, "delegateActNotFound", "Delegate account not found.", 404},
	{RpcSRC_CUR_MALFORMED, "srcCurMalformed", "Source currency is malformed.", 400},
	{RpcSRC_ISR_MALFORMED, "srcIsrMalformed", "Source issuer is malformed.", 400},
	{RpcSTREAM_MALFORMED, "malformedStream", "Stream malformed.", 400},
	{RpcTOO_BUSY, "tooBusy", "The server is too busy to help you now.", 503},
	{RpcTXN_NOT_FOUND, "txnNotFound", "Transaction not found.", 404},
	{RpcMETHOD_NOT_FOUND, "unknownCmd", "Unknown method.", 405},
	{RpcORACLE_MALFORMED, "oracleMalformed", "Oracle request is malformed.", 400},
	{RpcBAD_CREDENTIALS, "badCredentials", "Credentials do not exist, are not accepted, or have expired.", 400},
	{RpcTX_SIGNED, "transactionSigned", "Transaction should not be signed.", 400},
	{RpcDOMAIN_MALFORMED, "domainMalformed", "Domain is malformed.", 400},
	{RpcENTRY_NOT_FOUND, "entryNotFound", "Entry not found.", 400},
	{RpcUNEXPECTED_LEDGER_TYPE, "unexpectedLedgerType", "Unexpected ledger type.", 400},
}

func TestRpcErrorInfoTableCoversRippledCodes(t *testing.T) {
	for _, want := range rippledErrorInfos {
		info, ok := rpcErrorInfos[want.Code]
		if !ok {
			t.Errorf("missing metadata for rippled error code %d", want.Code)
			continue
		}
		if info != want {
			t.Errorf("metadata[%d] = %#v, want %#v", want.Code, info, want)
		}
	}
	if len(rpcErrorInfos) != len(rippledErrorInfos) {
		t.Errorf("metadata rows = %d, want %d rippled rows", len(rpcErrorInfos), len(rippledErrorInfos))
	}
	unknown := rpcErrorInfoForCode(0)
	if unknown != unknownRpcErrorInfo {
		t.Errorf("unknown metadata = %#v, want %#v", unknown, unknownRpcErrorInfo)
	}
	if info := rpcErrorInfoForCode(RpcREPORTING_UNSUPPORTED); info != unknownRpcErrorInfo {
		t.Errorf("deprecated unlisted metadata = %#v, want %#v", info, unknownRpcErrorInfo)
	}
}

func TestRpcErrorInvalidParamsDefaultMessage(t *testing.T) {
	if got := RpcErrorInvalidParams("").Message; got != "Invalid parameters." {
		t.Fatalf("default message = %q, want %q", got, "Invalid parameters.")
	}
	if got := RpcErrorInvalidParams("custom").Message; got != "custom" {
		t.Fatalf("explicit message = %q, want %q", got, "custom")
	}
}

func TestRpcErrorWithExtraProjectsCanonicalFields(t *testing.T) {
	fields := map[string]any{
		"error":           "spoofed",
		"status":          "spoofed",
		"error_code":      999,
		"error_message":   "spoofed",
		"error_exception": "spoofed",
		"code":            999,
		"message":         "spoofed",
		"type":            "spoofed",
		"index":           7,
	}
	err := RpcErrorInvalidParams("").WithExtra(fields)
	fields["index"] = 8
	got := err.ResponseFields()
	if got["index"] != 7 {
		t.Fatalf("WithExtra retained caller map mutation: index = %v, want 7", got["index"])
	}
	if got["error"] != "invalidParams" || got["error_code"] != RpcINVALID_PARAMS || got["error_message"] != "Invalid parameters." {
		t.Fatalf("canonical fields = %#v", got)
	}
	for _, key := range []string{"status", "error_exception", "code", "message", "type"} {
		if _, ok := got[key]; ok {
			t.Errorf("reserved extra %q was projected: %#v", key, got)
		}
	}

	bare := RpcErrorEntryNotFoundBare("").WithExtra(map[string]any{
		"error_code":    999,
		"error_message": "spoofed",
		"index":         9,
	})
	bareFields := bare.ResponseFields()
	if _, ok := bareFields["error_code"]; ok {
		t.Errorf("bare error projected error_code: %#v", bareFields)
	}
	if _, ok := bareFields["error_message"]; ok {
		t.Errorf("bare error projected error_message: %#v", bareFields)
	}
	if bareFields["index"] != 9 {
		t.Errorf("bare error lost non-reserved extra: %#v", bareFields)
	}

	exception := RpcErrorInvalidTransaction("decode failed").WithExtra(map[string]any{
		"error":           "spoofed",
		"error_code":      999,
		"error_message":   "spoofed",
		"error_exception": "spoofed",
		"index":           10,
	})
	exceptionFields := exception.ResponseFields()
	if exceptionFields["error"] != "invalidTransaction" || exceptionFields["error_exception"] != "decode failed" || exceptionFields["index"] != 10 {
		t.Fatalf("exception fields = %#v", exceptionFields)
	}
	for _, key := range []string{"error_code", "error_message"} {
		if _, ok := exceptionFields[key]; ok {
			t.Errorf("exception error projected %q: %#v", key, exceptionFields)
		}
	}
}

func TestRpcErrorJSONOmitsInternalType(t *testing.T) {
	data, err := json.Marshal(RpcErrorInvalidParams("custom"))
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	if _, ok := object["type"]; ok {
		t.Fatalf("internal Type leaked from default JSON: %s", data)
	}
	if object["error"] != "invalidParams" || object["error_code"] != float64(RpcINVALID_PARAMS) {
		t.Fatalf("canonical JSON fields = %#v", object)
	}
}

// TestInvalidApiVersionError pins the rippled-exact token and the transport
// marker that drives the per-transport wire shape. rippled emits the bare
// jss::invalid_API_version token with no numeric code or message
// (ServerHandler.cpp:454-455, 689, 694-695).
func TestInvalidApiVersionError(t *testing.T) {
	e := RpcErrorInvalidApiVersion("3")
	if e.ErrorString != InvalidApiVersionToken {
		t.Errorf("token = %q, want %q", e.ErrorString, InvalidApiVersionToken)
	}
	if e.ErrorString != "invalid_API_version" {
		t.Errorf("token = %q, want underscored rippled spelling", e.ErrorString)
	}
	if !e.IsInvalidApiVersion() {
		t.Error("RpcErrorInvalidApiVersion should report IsInvalidApiVersion")
	}
	if e.Message != "" {
		t.Errorf("error_message = %q, want empty (rippled omits it)", e.Message)
	}
	// Only the invalid-version error carries the transport marker.
	if RpcErrorInternal().IsInvalidApiVersion() {
		t.Error("unrelated error must not report IsInvalidApiVersion")
	}
}
