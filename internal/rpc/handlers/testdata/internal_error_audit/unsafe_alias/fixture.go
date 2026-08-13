package unsafe_alias

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Build(message string) *rpcerrors.RpcError {
	constructor := rpcerrors.NewRpcError
	return constructor(rpcerrors.RpcINTERNAL, "internal", "internal", message)
}
