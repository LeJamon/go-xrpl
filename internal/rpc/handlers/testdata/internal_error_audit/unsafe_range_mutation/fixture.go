package unsafe_range_mutation

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Mutate(message string) *rpctypes.RpcError {
	rpcErr := rpctypes.RpcErrorInvalidParams(message)
	for rpcErr.Code = range []int{rpctypes.RpcINTERNAL} {
	}
	return rpcErr
}
