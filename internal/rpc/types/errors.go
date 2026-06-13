package types

// XRPL RPC error codes.
//
// rippled serializes the numeric error_code field over the wire and documents
// the value range as de-facto stable because clients depend on it
// (ErrorCodes.h:30-39). The constants below therefore mirror rippled's
// error_code_i enum (ErrorCodes.h:42-160) 1:1 by value. The matching token
// strings and default messages live in rippled's ErrorCodes.cpp errorInfo
// table. errors_test.go pins every token/code pair to that table.
//
// Names follow rippled's enum (Rpc-prefixed) except RpcMETHOD_NOT_FOUND
// (rippled rpcUNKNOWN_COMMAND) and RpcMISSING_COMMAND (rippled
// rpcCOMMAND_MISSING), kept under their long-standing go-xrpl spellings.
// A small go-xrpl-specific block holds codes for conditions rippled does not
// enumerate.

// RpcError represents an XRPL RPC error with code and message.
//
// ErrorException carries the rippled-style "error_exception" envelope
// used when STTx construction throws (Simulate.cpp:338-342). Only the
// `simulate` invalidTransaction path populates it today.
type RpcError struct {
	Code           int    `json:"error_code"`
	ErrorString    string `json:"error"`
	Type           string `json:"type"`
	Message        string `json:"error_message,omitempty"`
	ErrorException string `json:"error_exception,omitempty"`

	// bareToken marks errors rippled emits as a lone `error` token via a direct
	// jvResult[jss::error] = "..." assignment (e.g. VaultInfo.cpp:101,
	// LedgerEntry.cpp:1044, TransactionEntry.cpp:71) rather than RPC::inject_error.
	// rippled's bare path writes neither error_code nor error_message, so the
	// wire emitters omit both when this is set.
	bareToken bool

	// invalidApiVersion marks the unsupported-api_version rejection, which
	// rippled emits at the ServerHandler transport layer — never through the
	// error_code_i enum — with a shape that differs per transport
	// (ServerHandler.cpp:443-468, 685-697): HTTP single is a bare-string 400,
	// each batch element is a make_json_error JSON-RPC object, and WS is a lone
	// `error` token. Transport writers special-case this flag so go-xrpl mirrors
	// each path exactly without disturbing the table-driven error model.
	invalidApiVersion bool
}

// IsBareToken reports whether this error mirrors a rippled bare-token response
// (only the `error` field on the wire, no error_code / error_message). The
// invalid-api_version rejection is bare on every transport that still carries
// a JSON-RPC result envelope (WS, batch-via-result), so it counts as one.
func (e RpcError) IsBareToken() bool {
	return e.bareToken || e.invalidApiVersion
}

// IsInvalidApiVersion reports whether this error is the unsupported-api_version
// rejection, whose wire shape rippled varies by transport (HTTP single → 400
// bare string; batch element → make_json_error object; WS → bare token).
func (e RpcError) IsInvalidApiVersion() bool {
	return e.invalidApiVersion
}

func (e RpcError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.ErrorString
}

// ErrorObject renders the error as a standalone JSON object matching rippled's
// RPC::inject_error (ErrorCodes.h:228-251): exactly the keys error, error_code
// and error_message, without go-xrpl's internal `type` field. Use this when an
// error must be embedded as a value inside an otherwise-successful result
// (e.g. owner_info's per-ledger sections, where rippled assigns rpcError(...)
// directly), rather than marshalling the struct.
func (e RpcError) ErrorObject() map[string]any {
	return map[string]any{
		"error":         e.ErrorString,
		"error_code":    e.Code,
		"error_message": e.Message,
	}
}

// rippled error_code_i enum, mirrored 1:1 by value (ErrorCodes.h:42-160).
// Comments mark rippled's reserved/unused slots so the table is not
// "filled in", matching rippled's own append-only discipline.
const (
	// -1 represents codes not listed in rippled's enumeration. rippled returns
	// it (an empty ErrorInfo) for any code <= rpcSUCCESS or > rpcLAST, and for
	// handlers that set a bare error token without injecting a numeric code.
	RpcUNKNOWN = -1

	RpcBAD_SYNTAX    = 1
	RpcJSON_RPC      = 2
	RpcFORBIDDEN     = 3
	RpcWRONG_NETWORK = 4
	// 5 unused
	RpcNO_PERMISSION = 6
	RpcNO_EVENTS     = 7
	// 8 unused
	RpcTOO_BUSY          = 9
	RpcSLOW_DOWN         = 10
	RpcHIGH_FEE          = 11
	RpcNOT_ENABLED       = 12
	RpcNOT_READY         = 13
	RpcAMENDMENT_BLOCKED = 14

	// Networking
	RpcNO_CLOSED  = 15
	RpcNO_CURRENT = 16
	RpcNO_NETWORK = 17
	RpcNOT_SYNCED = 18

	// Ledger state
	RpcACT_NOT_FOUND = 19
	// 20 unused
	RpcLGR_NOT_FOUND     = 21
	RpcLGR_NOT_VALIDATED = 22
	RpcMASTER_DISABLED   = 23
	// 24-28 unused
	RpcTXN_NOT_FOUND     = 29
	RpcINVALID_HOTWALLET = 30

	// Malformed command
	RpcINVALID_PARAMS = 31
	// RpcMETHOD_NOT_FOUND is rippled rpcUNKNOWN_COMMAND (token "unknownCmd").
	RpcMETHOD_NOT_FOUND = 32
	RpcNO_PF_REQUEST    = 33

	// Bad parameter (34 not used: rippled rpcACT_BITCOIN, retired)
	RpcACT_MALFORMED      = 35
	RpcALREADY_MULTISIG   = 36
	RpcALREADY_SINGLE_SIG = 37
	// 38,39 unused in rippled (see RpcINVALID_API_VERSION below)
	RpcBAD_FEATURE           = 40
	RpcBAD_ISSUER            = 41
	RpcBAD_MARKET            = 42
	RpcBAD_SECRET            = 43
	RpcBAD_SEED              = 44
	RpcCHANNEL_MALFORMED     = 45
	RpcCHANNEL_AMT_MALFORMED = 46
	// RpcMISSING_COMMAND is rippled rpcCOMMAND_MISSING (token "commandMissing").
	RpcMISSING_COMMAND   = 47
	RpcDST_ACT_MALFORMED = 48
	RpcDST_ACT_MISSING   = 49
	RpcDST_ACT_NOT_FOUND = 50
	RpcDST_AMT_MALFORMED = 51
	RpcDST_AMT_MISSING   = 52
	RpcDST_ISR_MALFORMED = 53
	// 54-56 unused
	RpcLGR_IDXS_INVALID  = 57
	RpcLGR_IDX_MALFORMED = 58
	// 59-61 unused
	RpcPUBLIC_MALFORMED       = 62
	RpcSIGNING_MALFORMED      = 63
	RpcSENDMAX_MALFORMED      = 64
	RpcSRC_ACT_MALFORMED      = 65
	RpcSRC_ACT_MISSING        = 66
	RpcSRC_ACT_NOT_FOUND      = 67
	RpcDELEGATE_ACT_NOT_FOUND = 68
	RpcSRC_CUR_MALFORMED      = 69
	RpcSRC_ISR_MALFORMED      = 70
	RpcSTREAM_MALFORMED       = 71
	RpcATX_DEPRECATED         = 72

	// Internal error (should never happen)
	RpcINTERNAL               = 73
	RpcNOT_IMPL               = 74
	RpcNOT_SUPPORTED          = 75
	RpcBAD_KEY_TYPE           = 76
	RpcDB_DESERIALIZATION     = 77
	RpcEXCESSIVE_LGR_RANGE    = 78
	RpcINVALID_LGR_RANGE      = 79
	RpcEXPIRED_VALIDATOR_LIST = 80

	// 81-90 unused; 91 deprecated (rippled rpcREPORTING_UNSUPPORTED)
	RpcREPORTING_UNSUPPORTED = 91
	RpcOBJECT_NOT_FOUND      = 92
	RpcISSUE_MALFORMED       = 93 // AMM
	RpcORACLE_MALFORMED      = 94 // Oracle
	RpcBAD_CREDENTIALS       = 95 // deposit_authorized + credentials
	RpcTX_SIGNED             = 96 // Simulate
	RpcDOMAIN_MALFORMED      = 97 // Pathfinding
)

// go-xrpl-specific error codes for conditions rippled does not assign an
// error_code_i slot. These deliberately avoid colliding with any rippled code.
const (
	// RpcINVALID_API_VERSION reports an unsupported api_version. rippled
	// rejects this at the HTTP/ServerHandler layer (ServerHandler.cpp) and
	// never reaches the error_code_i enum; go-xrpl surfaces it through the
	// normal RpcError envelope, so it occupies rippled's explicitly-unused
	// slot 38 to stay distinct from every real rippled code.
	RpcINVALID_API_VERSION = 38

	// RpcNOT_STANDALONE has no rippled enum entry. rippled's ledger_accept
	// handler emits a bare "notStandAlone" token with no numeric code
	// (LedgerAccept.cpp:40); it maps to RpcUNKNOWN (-1), rippled's "code not
	// listed in this enumeration".
	RpcNOT_STANDALONE = RpcUNKNOWN
)

// Standard error constructors
func NewRpcError(code int, error, errorType, message string) *RpcError {
	return &RpcError{
		Code:        code,
		ErrorString: error,
		Type:        errorType,
		Message:     message,
	}
}

// Common error constructors matching rippled
func RpcErrorUnknown(message string) *RpcError {
	return NewRpcError(RpcUNKNOWN, "unknown", "unknown", message)
}

func RpcErrorInvalidParams(message string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams", message)
}

func RpcErrorMethodNotFound(method string) *RpcError {
	return NewRpcError(RpcMETHOD_NOT_FOUND, "unknownCmd", "unknownCmd", "Unknown method: "+method)
}

func RpcErrorLgrNotFound(message string) *RpcError {
	return NewRpcError(RpcLGR_NOT_FOUND, "lgrNotFound", "lgrNotFound", message)
}

func RpcErrorActNotFound(message string) *RpcError {
	return NewRpcError(RpcACT_NOT_FOUND, "actNotFound", "actNotFound", message)
}

func RpcErrorActMalformed(message string) *RpcError {
	return NewRpcError(RpcACT_MALFORMED, "actMalformed", "actMalformed", message)
}

func RpcErrorTxnNotFound(message string) *RpcError {
	return NewRpcError(RpcTXN_NOT_FOUND, "txnNotFound", "txnNotFound", message)
}

// RpcErrorIssueMalformed matches rippled rpcISSUE_MALFORMED (code 93, token
// "issueMalformed"), returned for an unparseable asset/asset2 issue object.
func RpcErrorIssueMalformed() *RpcError {
	return NewRpcError(RpcISSUE_MALFORMED, "issueMalformed", "issueMalformed", "Issue is malformed.")
}

// RpcErrorInvalidHotWallet matches rippled rpcINVALID_HOTWALLET (code 30,
// token "invalidHotWallet", message "Invalid hotwallet.").
func RpcErrorInvalidHotWallet() *RpcError {
	return NewRpcError(RpcINVALID_HOTWALLET, "invalidHotWallet", "invalidHotWallet", "Invalid hotwallet.")
}

func RpcErrorInternal(message string) *RpcError {
	return NewRpcError(RpcINTERNAL, "internal", "internal", message)
}

func RpcErrorNoPermission(method string) *RpcError {
	return NewRpcError(RpcNO_PERMISSION, "noPermission", "noPermission",
		"You don't have permission for this command.")
}

// RpcErrorForbidden matches rippled rpcFORBIDDEN (code 3, token "forbidden").
// Used by the WebSocket pre-dispatch admin gate, mirroring rippled
// ServerHandler.cpp:482-486 which writes rpcError(rpcFORBIDDEN) when
// requestRole returns Role::FORBID for an admin-required command.
func RpcErrorForbidden(method string) *RpcError {
	return NewRpcError(RpcFORBIDDEN, "forbidden", "forbidden",
		"You don't have permission for this command.")
}

// RpcErrorTooBusy returns the canonical rpcTOO_BUSY envelope. The
// message string is fixed to match rippled's ErrorCodes.cpp:114 INFOS
// entry so HTTP/WS clients see a byte-identical error_message.
func RpcErrorTooBusy() *RpcError {
	return NewRpcError(RpcTOO_BUSY, "tooBusy", "tooBusy",
		"The server is too busy to help you now.")
}

func RpcErrorSlowDown(message string) *RpcError {
	return NewRpcError(RpcSLOW_DOWN, "slowDown", "slowDown", message)
}

// RpcErrorNotStandalone mirrors rippled's ledger_accept handler
// (LedgerAccept.cpp:40), which emits a bare "notStandAlone" token with no
// numeric code or message when the node is not in standalone mode.
func RpcErrorNotStandalone(message string) *RpcError {
	e := NewRpcError(RpcNOT_STANDALONE, "notStandAlone", "notStandAlone", message)
	e.bareToken = true
	return e
}

// InvalidApiVersionToken is the literal rippled writes for an unsupported
// api_version (jss::invalid_API_version). rippled emits it bare — no numeric
// code, no message — on every transport; only the envelope differs
// (ServerHandler.cpp:454-455, 689, 694-695).
const InvalidApiVersionToken = "invalid_API_version"

// WrongVersionJSONRPCCode is the JSON-RPC error code rippled attaches to an
// invalid-api_version batch element via make_json_error (ServerHandler.cpp:608,
// wrong_version = -32606).
const WrongVersionJSONRPCCode = -32606

// RpcErrorInvalidApiVersion reports an unsupported api_version. rippled rejects
// this at the ServerHandler transport layer, never through the error_code_i
// enum, so the token is the bare jss::invalid_API_version and the per-transport
// wire shape is finalized by the transport writers (see IsInvalidApiVersion).
// The internal code stays at the go-xrpl slot 38 to keep this distinct from
// every real rippled code; it is not emitted on the wire.
func RpcErrorInvalidApiVersion(version string) *RpcError {
	e := NewRpcError(RpcINVALID_API_VERSION, InvalidApiVersionToken, InvalidApiVersionToken, "")
	e.invalidApiVersion = true
	return e
}

// RpcErrorNotEnabled returns rippled's rpcNOT_ENABLED (code 12, token
// "notEnabled"). An empty message defaults to rippled's canonical
// "Not enabled in configuration." string from ErrorCodes.cpp's errorInfo
// array. Reference: rippled ErrorCodes.h (rpcNOT_ENABLED) +
// ErrorCodes.cpp (errorInfo[rpcNOT_ENABLED]).
func RpcErrorNotEnabled(message string) *RpcError {
	if message == "" {
		message = "Not enabled in configuration."
	}
	return NewRpcError(RpcNOT_ENABLED, "notEnabled", "notEnabled", message)
}

// RpcErrorNotReady returns rippled's rpcNOT_READY (code 13, token
// "notReady"). An empty message defaults to rippled's canonical
// "Not ready to handle this request." string from ErrorCodes.cpp's errorInfo
// array. Reference: rippled ErrorCodes.h (rpcNOT_READY) +
// ErrorCodes.cpp (errorInfo[rpcNOT_READY]).
func RpcErrorNotReady(message string) *RpcError {
	if message == "" {
		message = "Not ready to handle this request."
	}
	return NewRpcError(RpcNOT_READY, "notReady", "notReady", message)
}

// RpcErrorNotSupported returns rippled's rpcNOT_SUPPORTED (code 75, token
// "notSupported"). An empty message defaults to rippled's canonical
// "Operation not supported." string from ErrorCodes.cpp's errorInfo array.
// Reference: rippled ErrorCodes.h (rpcNOT_SUPPORTED) +
// ErrorCodes.cpp (errorInfo[rpcNOT_SUPPORTED]).
func RpcErrorNotSupported(message string) *RpcError {
	if message == "" {
		message = "Operation not supported."
	}
	return NewRpcError(RpcNOT_SUPPORTED, "notSupported", "notSupported", message)
}

// RpcErrorNoEvents returns rippled's rpcNO_EVENTS (code 7, token "noEvents"),
// returned by handlers whose work requires a subscription-capable transport
// (path_find, etc.) when invoked over plain JSON-RPC. An empty message
// defaults to rippled's canonical "Current transport does not support events."
// string. Reference: rippled ErrorCodes.h (rpcNO_EVENTS) +
// ErrorCodes.cpp (errorInfo[rpcNO_EVENTS]) +
// rippled handler PathFind.cpp (rpcError(rpcNO_EVENTS) on !context.infoSub).
func RpcErrorNoEvents(message string) *RpcError {
	if message == "" {
		message = "Current transport does not support events."
	}
	return NewRpcError(RpcNO_EVENTS, "noEvents", "noEvents", message)
}

func RpcErrorAmendmentBlocked() *RpcError {
	return NewRpcError(RpcAMENDMENT_BLOCKED, "amendmentBlocked", "amendmentBlocked", "Amendment blocked, need upgrade.")
}

// RpcErrorBadFeature returns rippled's rpcBAD_FEATURE (code 40, token
// "badFeature"): the requested amendment is unknown or invalid.
func RpcErrorBadFeature(message string) *RpcError {
	return NewRpcError(RpcBAD_FEATURE, "badFeature", "badFeature", message)
}

// RpcErrorNoPathRequest returns an error when close/status is called without an active path_find session
func RpcErrorNoPathRequest() *RpcError {
	return NewRpcError(RpcNO_PF_REQUEST, "noPathRequest", "noPathRequest", "No pathfinding request in progress.")
}

// RpcErrorObjectNotFound returns an error for object not found (matches rippled rpcOBJECT_NOT_FOUND)
func RpcErrorObjectNotFound(message string) *RpcError {
	return NewRpcError(RpcOBJECT_NOT_FOUND, "objectNotFound", "objectNotFound", message)
}

// RpcErrorBadCredentials returns an error for credential validation failures (matches rippled rpcBAD_CREDENTIALS).
func RpcErrorBadCredentials(message string) *RpcError {
	return NewRpcError(RpcBAD_CREDENTIALS, "badCredentials", "badCredentials", message)
}

// RpcErrorHighFee returns an error when the auto-filled fee exceeds the requested limit (matches rippled rpcHIGH_FEE).
func RpcErrorHighFee(message string) *RpcError {
	return NewRpcError(RpcHIGH_FEE, "highFee", "highFee", message)
}

// RpcErrorExpectedField returns an error for a field that is not of the expected type
// (matches rippled's expected_field_message: "Invalid field '<name>', not <type>.")
func RpcErrorExpectedField(field, expectedType string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams",
		"Invalid field '"+field+"', not "+expectedType+".")
}

// RpcErrorExpectedFieldHighFee returns a highFee error for a field that is not of the expected type.
// rippled returns rpcHIGH_FEE (not rpcINVALID_PARAMS) when fee_mult_max or fee_div_max is not an integer.
func RpcErrorExpectedFieldHighFee(field, expectedType string) *RpcError {
	return NewRpcError(RpcHIGH_FEE, "highFee", "highFee",
		"Invalid field '"+field+"', not "+expectedType+".")
}

// RpcErrorSigningMalformed returns an error when a transaction's signing is malformed
// (matches rippled rpcSIGNING_MALFORMED, code 63, token "signingMalformed").
func RpcErrorSigningMalformed() *RpcError {
	return NewRpcError(RpcSIGNING_MALFORMED, "signingMalformed", "signingMalformed", "Signing of transaction is malformed.")
}

// RpcErrorPublicMalformed returns the error rippled emits for an unparseable
// public key (matches rippled rpcPUBLIC_MALFORMED, code 62, token
// "publicMalformed"; see ErrorCodes.cpp:103).
func RpcErrorPublicMalformed() *RpcError {
	return NewRpcError(RpcPUBLIC_MALFORMED, "publicMalformed", "publicMalformed", "Public key is malformed.")
}

// RpcErrorMissingField returns an error for missing required field (matches rippled missing_field_error)
func RpcErrorMissingField(field string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams", "Missing field '"+field+"'.")
}

// RpcErrorFieldNotFoundTransaction matches rippled TransactionEntry.cpp:48,
// which sets the bare "fieldNotFoundTransaction" token on the result body
// without a numeric code; we use rpcUNKNOWN (-1) as the closest approximation.
func RpcErrorFieldNotFoundTransaction() *RpcError {
	e := NewRpcError(RpcUNKNOWN, "fieldNotFoundTransaction", "fieldNotFoundTransaction", "Missing field 'tx_hash'.")
	e.bareToken = true
	return e
}

// RpcErrorInvalidField returns an error for invalid field value (matches rippled invalid_field_error)
func RpcErrorInvalidField(field string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams", "Invalid field '"+field+"'.")
}

// RpcErrorTxSigned returns an error when a transaction is pre-signed but should not be
// (matches rippled rpcTX_SIGNED, code 96, token "transactionSigned").
func RpcErrorTxSigned() *RpcError {
	return NewRpcError(RpcTX_SIGNED, "transactionSigned", "transactionSigned", "Transaction should not be signed.")
}

// RpcErrorSrcActMalformed returns an error when the source account address is malformed
// (matches rippled rpcSRC_ACT_MALFORMED, code 65, token "srcActMalformed").
func RpcErrorSrcActMalformed(message string) *RpcError {
	return NewRpcError(RpcSRC_ACT_MALFORMED, "srcActMalformed", "srcActMalformed", message)
}

// RpcErrorNotImpl returns an error for unimplemented features
// (matches rippled rpcNOT_IMPL, code 74, token "notImpl").
func RpcErrorNotImpl() *RpcError {
	return NewRpcError(RpcNOT_IMPL, "notImpl", "notImpl", "Not implemented.")
}

// RpcErrorOracleMalformed returns an error for malformed oracle requests
// (matches rippled rpcORACLE_MALFORMED, code 94, token "oracleMalformed").
func RpcErrorOracleMalformed() *RpcError {
	return NewRpcError(RpcORACLE_MALFORMED, "oracleMalformed", "oracleMalformed", "Oracle request is malformed.")
}

// RpcErrorEntryNotFound returns the error rippled emits for a missing ledger
// entry (LedgerEntry.cpp:1044, VaultInfo.cpp:101): a bare "entryNotFound"
// token with no numeric code, so the code is rpcUNKNOWN (-1).
func RpcErrorEntryNotFound(message string) *RpcError {
	e := NewRpcError(RpcUNKNOWN, "entryNotFound", "entryNotFound", message)
	e.bareToken = true
	return e
}

// RpcErrorTransactionNotFound returns the error rippled's transaction_entry
// handler emits when the transaction is absent (TransactionEntry.cpp:71): a bare
// "transactionNotFound" token with no numeric code and no error_message. Note
// the token differs from the `tx` command's "txnNotFound" (rpcTXN_NOT_FOUND=29).
func RpcErrorTransactionNotFound(message string) *RpcError {
	e := NewRpcError(RpcUNKNOWN, "transactionNotFound", "transactionNotFound", message)
	e.bareToken = true
	return e
}

// RpcErrorUnknownOption returns an error when no valid selector is provided
// (matches rippled "unknownOption", a bare token with no numeric code, -1).
func RpcErrorUnknownOption(message string) *RpcError {
	e := NewRpcError(RpcUNKNOWN, "unknownOption", "unknownOption", message)
	e.bareToken = true
	return e
}

// RpcErrorSrcActMissing returns an error when the source account is not provided
// (matches rippled rpcSRC_ACT_MISSING, code 66, token "srcActMissing").
func RpcErrorSrcActMissing(message string) *RpcError {
	return NewRpcError(RpcSRC_ACT_MISSING, "srcActMissing", "srcActMissing", message)
}

// RpcErrorSrcActNotFound returns an error when the source account does not
// exist in the ledger (matches rippled rpcSRC_ACT_NOT_FOUND, code 67, token
// "srcActNotFound"; see rippled ErrorCodes.cpp:109).
func RpcErrorSrcActNotFound(message string) *RpcError {
	return NewRpcError(RpcSRC_ACT_NOT_FOUND, "srcActNotFound", "srcActNotFound", message)
}

// RpcErrorInvalidTransaction returns the envelope rippled emits when STTx
// construction throws (Simulate.cpp:338-342): `error: "invalidTransaction"`
// + `error_exception: <reason>`. The error_code matches rippled's
// behaviour of leaving the field at the default rpcINVALID_PARAMS slot
// (the manual `jvResult[jss::error] = "invalidTransaction"` path does
// not go through `RPC::make_error`, so callers should not depend on the
// code value).
func RpcErrorInvalidTransaction(exception string) *RpcError {
	return &RpcError{
		Code:           RpcINVALID_PARAMS,
		ErrorString:    "invalidTransaction",
		Type:           "invalidTransaction",
		ErrorException: exception,
	}
}

// RpcErrorSrcCurMalformed returns an error when a source currency is malformed
// (matches rippled rpcSRC_CUR_MALFORMED, code 69, token "srcCurMalformed").
func RpcErrorSrcCurMalformed(message string) *RpcError {
	return NewRpcError(RpcSRC_CUR_MALFORMED, "srcCurMalformed", "srcCurMalformed", message)
}

// RpcErrorDstAmtMalformed returns an error when a destination amount/currency
// is malformed (matches rippled rpcDST_AMT_MALFORMED, code 51, token
// "dstAmtMalformed"). Used for taker_gets.currency parse failures per
// rippled BookOffers.cpp:90-96.
func RpcErrorDstAmtMalformed(message string) *RpcError {
	return NewRpcError(RpcDST_AMT_MALFORMED, "dstAmtMalformed", "dstAmtMalformed", message)
}

// RpcErrorSrcIsrMalformed returns an error when a source issuer is malformed
// (matches rippled rpcSRC_ISR_MALFORMED, code 70, token "srcIsrMalformed").
func RpcErrorSrcIsrMalformed(message string) *RpcError {
	return NewRpcError(RpcSRC_ISR_MALFORMED, "srcIsrMalformed", "srcIsrMalformed", message)
}

// RpcErrorDstIsrMalformed returns an error when a destination issuer is
// malformed (matches rippled rpcDST_ISR_MALFORMED, code 53, token
// "dstIsrMalformed").
func RpcErrorDstIsrMalformed(message string) *RpcError {
	return NewRpcError(RpcDST_ISR_MALFORMED, "dstIsrMalformed", "dstIsrMalformed", message)
}

// RpcErrorDstActMissing returns an error when the destination account is not
// provided (matches rippled rpcDST_ACT_MISSING, code 49, token
// "dstActMissing").
func RpcErrorDstActMissing(message string) *RpcError {
	return NewRpcError(RpcDST_ACT_MISSING, "dstActMissing", "dstActMissing", message)
}

// RpcErrorDstActMalformed returns an error when the destination account
// address is malformed (matches rippled rpcDST_ACT_MALFORMED, code 48, token
// "dstActMalformed").
func RpcErrorDstActMalformed(message string) *RpcError {
	return NewRpcError(RpcDST_ACT_MALFORMED, "dstActMalformed", "dstActMalformed", message)
}

// RpcErrorDstAmtMissing returns an error when the destination amount is not
// provided (matches rippled rpcDST_AMT_MISSING, code 52, token
// "dstAmtMissing").
func RpcErrorDstAmtMissing(message string) *RpcError {
	return NewRpcError(RpcDST_AMT_MISSING, "dstAmtMissing", "dstAmtMissing", message)
}

// RpcErrorSendMaxMalformed returns an error when send_max is malformed
// (matches rippled rpcSENDMAX_MALFORMED, code 64, token "sendMaxMalformed").
func RpcErrorSendMaxMalformed(message string) *RpcError {
	return NewRpcError(RpcSENDMAX_MALFORMED, "sendMaxMalformed", "sendMaxMalformed", message)
}

// RpcErrorBadMarket matches rippled rpcBAD_MARKET (code 42, token "badMarket"),
// returned when taker_pays and taker_gets describe the same asset.
// Reference: ErrorCodes.cpp:62 "No such market.".
func RpcErrorBadMarket() *RpcError {
	return NewRpcError(RpcBAD_MARKET, "badMarket", "badMarket", "No such market.")
}

// RpcErrorMalformedStream matches rippled rpcSTREAM_MALFORMED (code 71, token
// "malformedStream", message "Stream malformed."), returned for an unknown
// stream name in subscribe/unsubscribe.
func RpcErrorMalformedStream() *RpcError {
	return NewRpcError(RpcSTREAM_MALFORMED, "malformedStream", "malformedStream", "Stream malformed.")
}

// RpcErrorBadIssuer matches rippled rpcBAD_ISSUER (code 41, token "badIssuer",
// message "Issuer account malformed."), returned when a book subscription's
// taker does not parse as an account (Subscribe.cpp:301-305).
func RpcErrorBadIssuer() *RpcError {
	return NewRpcError(RpcBAD_ISSUER, "badIssuer", "badIssuer", "Issuer account malformed.")
}

// RpcErrorDomainMalformed matches rippled rpcDOMAIN_MALFORMED (code 97, token
// "domainMalformed"), returned when a request's domain parameter does not
// parse as a uint256 hex string. Callers pass the message rippled would emit
// for their callsite (e.g. BookOffers.cpp:183 uses "Unable to parse domain.",
// overriding the ErrorCodes.cpp:120 default "Domain is malformed.").
func RpcErrorDomainMalformed(message string) *RpcError {
	if message == "" {
		message = "Domain is malformed."
	}
	return NewRpcError(RpcDOMAIN_MALFORMED, "domainMalformed", "domainMalformed", message)
}

// RpcErrorDstActNotFound returns an error when the destination account is not found
// (matches rippled rpcDST_ACT_NOT_FOUND, code 50, token "dstActNotFound").
func RpcErrorDstActNotFound(message string) *RpcError {
	return NewRpcError(RpcDST_ACT_NOT_FOUND, "dstActNotFound", "dstActNotFound", message)
}
