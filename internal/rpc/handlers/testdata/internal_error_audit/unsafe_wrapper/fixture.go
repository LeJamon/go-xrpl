package unsafe_wrapper

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func wrap(code int, message string) *rpcerrors.RpcError {
	return rpcerrors.NewRpcError(code, "internal", "internal", message)
}

func Build(message string) *rpcerrors.RpcError {
	constructor := wrap
	return constructor(72+1, message)
}
