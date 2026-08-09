package unsafe_address

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Mutate(message string) *rpcerrors.RpcError {
	rpcErr := rpcerrors.RpcErrorInvalidParams(message)
	field := &rpcErr.Code
	*field = rpcerrors.RpcINTERNAL
	return rpcErr
}
