package safe

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

const RpcINTERNAL = 72 + 1

type RpcError struct {
	Code    int
	Message string
}

func NewRpcError(code int, message string) *RpcError {
	return &RpcError{Code: code, Message: message}
}

func Build(message string) (*rpctypes.RpcError, *RpcError) {
	_ = rpctypes.RpcErrorInternal()
	_ = rpctypes.RpcErrorTransactionSubmission()
	remote := rpctypes.NewRpcError(rpctypes.RpcINVALID_PARAMS, "invalidParams", "invalidParams", message)

	constructor := NewRpcError
	local := constructor(RpcINTERNAL, message)
	local.Code = 73
	local.Message = message
	return remote, local
}
