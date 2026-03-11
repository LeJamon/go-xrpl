package types

// XRPL RPC Error Codes - matching rippled implementation
// These error codes must match exactly with rippled for protocol compatibility

// RpcError represents an XRPL RPC error with code and message
type RpcError struct {
	Code        int    `json:"error_code"`
	ErrorString string `json:"error"`
	Type        string `json:"type"`
	Message     string `json:"error_message,omitempty"`
}

func (e RpcError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return e.ErrorString
}

// Standard XRPL Error Codes - must match rippled exactly
const (
	// Universal errors
	RpcUNKNOWN          = -1
	RpcJSON_RPC         = -32600
	RpcMETHOD_NOT_FOUND = -32601
	RpcINVALID_PARAMS   = -32602
	RpcINTERNAL         = -32603
	RpcPARSE_ERROR      = -32700

	// General purpose errors
	RpcGENERAL           = 1
	RpcMISSING_COMMAND   = 2
	RpcCOMMAND_UNTRUSTED = 3
	RpcNO_CURRENT        = 4
	RpcNO_NETWORK        = 5
	RpcTOO_BUSY          = 6
	RpcSLOW_DOWN         = 7

	// Networking
	RpcNOT_STANDALONE = 10
	RpcSHUT_DOWN      = 11
	RpcREPORTING      = 12

	// Ledger errors
	RpcLGR_NOT_FOUND     = 15
	RpcLGR_IDXS_INVALID  = 16
	RpcLGR_NOT_VALIDATED = 17

	// Transaction errors
	RpcTXN_NOT_FOUND = 24
	RpcTXN_NOT_READY = 25

	// Account errors
	RpcACT_NOT_FOUND      = 19
	RpcACT_LINES          = 20
	RpcACT_CHANNELS       = 21
	RpcACT_OBJECTS        = 22
	RpcACT_ROOT_NOT_FOUND = 23
	RpcACT_MALFORMED      = 50
	RpcSRC_ACT_NOT_FOUND  = 51
	RpcDST_ACT_NOT_FOUND  = 52

	// Server state
	RpcSERVER_INFO = 18

	// Subscription errors
	RpcSTREAM_MALFORMED = 26
	RpcPATH_MALFORMED   = 27
	RpcPATH_DRY         = 28

	// Amendment errors
	RpcNOT_ENABLED   = 31
	RpcNOT_SUPPORTED = 32

	// WebSocket specific
	RpcCOMMAND_MISSING        = 34
	RpcCOMMAND_IS_NOT_A_STRING = 35

	// Rate limiting
	RpcSLOW_DOWN_INVALID_IP = 36

	// Oracle errors
	RpcORACLE_MALFORMED = 37

	// Amendment and feature errors
	RpcINVALID_API_VERSION = 38
	RpcUNSUPPORTED_FEATURE = 39
	RpcAMENDMENT_BLOCKED   = 40

	// Database errors
	RpcDB_DESERIALIZATION_ERROR = 41

	// Additional transaction errors
	RpcTXN_TYPE_NOT_SUPPORTED = 42
	RpcINVALID_FIELD          = 43
	RpcINVALID_HASH           = 44
	RpcINVALID_LGR_RANGE      = 45

	// Path finding errors
	RpcNO_PATH = 46

	// Implementation status errors
	RpcNOT_IMPL      = 47 // Feature not implemented
	RpcNOT_VALIDATOR = 48 // Server is not configured as a validator
	RpcNOT_SYNCED    = 49 // Not synced to network

	// Signing/Key errors - must match rippled exactly
	RpcBAD_SEED             = 44 // Disallowed seed
	RpcCHANNEL_MALFORMED    = 45 // Payment channel is malformed
	RpcCHANNEL_AMT_MALFORMED = 46 // Payment channel amount is malformed
	RpcPUBLIC_MALFORMED     = 62 // Public key is malformed
	RpcBAD_KEY_TYPE         = 76 // Bad key type

	// Object errors - must match rippled exactly
	RpcOBJECT_NOT_FOUND = 92 // Object not found
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

func RpcErrorTxnNotFound(message string) *RpcError {
	return NewRpcError(RpcTXN_NOT_FOUND, "txnNotFound", "txnNotFound", message)
}

func RpcErrorInternal(message string) *RpcError {
	return NewRpcError(RpcINTERNAL, "internal", "internal", message)
}

func RpcErrorTooBusy(message string) *RpcError {
	return NewRpcError(RpcTOO_BUSY, "tooBusy", "tooBusy", message)
}

func RpcErrorSlowDown(message string) *RpcError {
	return NewRpcError(RpcSLOW_DOWN, "slowDown", "slowDown", message)
}

func RpcErrorNotStandalone(message string) *RpcError {
	return NewRpcError(RpcNOT_STANDALONE, "notStandalone", "notStandalone", message)
}

func RpcErrorShutDown(message string) *RpcError {
	return NewRpcError(RpcSHUT_DOWN, "shutDown", "shutDown", message)
}

func RpcErrorInvalidApiVersion(version string) *RpcError {
	return NewRpcError(RpcINVALID_API_VERSION, "invalidApiVersion", "invalidApiVersion", "Invalid API version: "+version)
}

func RpcErrorNotEnabled(feature string) *RpcError {
	return NewRpcError(RpcNOT_ENABLED, "notEnabled", "notEnabled", "Feature not enabled: "+feature)
}

func RpcErrorAmendmentBlocked() *RpcError {
	return NewRpcError(RpcAMENDMENT_BLOCKED, "amendmentBlocked", "amendmentBlocked", "Amendment blocked, need upgrade.")
}

// RpcErrorObjectNotFound returns an error for object not found (matches rippled rpcOBJECT_NOT_FOUND)
func RpcErrorObjectNotFound(message string) *RpcError {
	return NewRpcError(RpcOBJECT_NOT_FOUND, "objectNotFound", "objectNotFound", message)
}

// RpcErrorMissingField returns an error for missing required field (matches rippled missing_field_error)
func RpcErrorMissingField(field string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams", "Missing field '"+field+"'.")
}

// RpcErrorInvalidField returns an error for invalid field value (matches rippled invalid_field_error)
func RpcErrorInvalidField(field string) *RpcError {
	return NewRpcError(RpcINVALID_PARAMS, "invalidParams", "invalidParams", "Invalid field '"+field+"'.")
}
