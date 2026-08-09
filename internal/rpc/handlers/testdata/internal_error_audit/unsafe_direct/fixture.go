package unsafe_direct

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Build(message string) *rpcerrors.RpcError {
	return rpcerrors.NewRpcError(72+1, "internal", "internal", message)
}
