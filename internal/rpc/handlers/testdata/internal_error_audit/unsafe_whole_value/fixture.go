package unsafewholevalue

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func mutate(err *rpcerrors.RpcError) {
	*err = rpcerrors.RpcError{Code: rpcerrors.RpcINTERNAL, Message: "private detail"}
}
