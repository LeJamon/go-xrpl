package bridge

import "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Error() *types.RpcError {
	return types.RpcErrorInvalidParams("invalid")
}
