package unsafe_address

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Mutate(message string) *rpctypes.RpcError {
	rpcErr := rpctypes.RpcErrorInvalidParams(message)
	field := &rpcErr.Code
	*field = rpctypes.RpcINTERNAL
	return rpcErr
}
