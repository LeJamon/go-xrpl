package unsafe_mutation

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

type rpcErrorAlias = rpctypes.RpcError

func Mutate(message string) *rpcErrorAlias {
	rpcErr := rpctypes.RpcErrorInvalidParams(message)
	rpcErr.Code = 72 + 1
	rpcErr.Message = message
	return rpcErr
}
