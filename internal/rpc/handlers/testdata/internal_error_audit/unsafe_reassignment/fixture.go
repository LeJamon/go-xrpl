package unsafe_reassignment

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func safeConstructor(_ int, _, _, message string) *rpcerrors.RpcError {
	return rpcerrors.RpcErrorInvalidParams(message)
}

func Build(message string) *rpcerrors.RpcError {
	constructor := safeConstructor
	constructor = rpcerrors.NewRpcError
	return constructor(72+1, "internal", "internal", message)
}
