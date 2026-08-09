package unsafe_range_mutation

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Mutate(message string) *rpcerrors.RpcError {
	rpcErr := rpcerrors.RpcErrorInvalidParams(message)
	for rpcErr.Code = range []int{rpcerrors.RpcINTERNAL} {
	}
	return rpcErr
}
