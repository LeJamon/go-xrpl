package unsafe_defined_type

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

type shadow rpcerrors.RpcError

func InternalError(private string) *rpcerrors.RpcError {
	value := shadow{Code: rpcerrors.RpcINTERNAL, Message: private}
	rpcErr := rpcerrors.RpcError(value)
	return &rpcErr
}
