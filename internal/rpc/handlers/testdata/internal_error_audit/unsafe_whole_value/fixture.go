package unsafewholevalue

import "github.com/LeJamon/go-xrpl/internal/rpc/types"

func mutate(err *types.RpcError) {
	*err = types.RpcError{Code: types.RpcINTERNAL, Message: "private detail"}
}
