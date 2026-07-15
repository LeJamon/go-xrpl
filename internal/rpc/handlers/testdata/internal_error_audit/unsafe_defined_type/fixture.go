package unsafe_defined_type

import "github.com/LeJamon/go-xrpl/internal/rpc/types"

type shadow types.RpcError

func InternalError(private string) *types.RpcError {
	value := shadow{Code: types.RpcINTERNAL, Message: private}
	rpcErr := types.RpcError(value)
	return &rpcErr
}
