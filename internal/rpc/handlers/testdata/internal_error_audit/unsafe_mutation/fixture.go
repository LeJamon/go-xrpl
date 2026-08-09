package unsafe_mutation

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

type rpcErrorAlias = rpcerrors.RpcError

func Mutate(message string) *rpcErrorAlias {
	rpcErr := rpcerrors.RpcErrorInvalidParams(message)
	rpcErr.Code = 72 + 1
	rpcErr.Message = message
	return rpcErr
}
