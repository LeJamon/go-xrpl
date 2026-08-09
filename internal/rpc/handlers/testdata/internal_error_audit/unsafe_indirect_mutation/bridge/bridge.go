package bridge

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Error() *rpcerrors.RpcError {
	return rpcerrors.RpcErrorInvalidParams("invalid")
}
