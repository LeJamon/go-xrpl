package safe

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

const RpcINTERNAL = 72 + 1

type RpcError struct {
	Code    int
	Message string
}

func NewRpcError(code int, message string) *RpcError {
	return &RpcError{Code: code, Message: message}
}

func Build(message string) (*rpcerrors.RpcError, *RpcError) {
	_ = rpcerrors.RpcErrorInternal()
	_ = rpcerrors.RpcErrorTransactionSubmission()
	remote := rpcerrors.NewRpcError(rpcerrors.RpcINVALID_PARAMS, "invalidParams", "invalidParams", message)

	constructor := NewRpcError
	local := constructor(RpcINTERNAL, message)
	local.Code = 73
	local.Message = message
	return remote, local
}
