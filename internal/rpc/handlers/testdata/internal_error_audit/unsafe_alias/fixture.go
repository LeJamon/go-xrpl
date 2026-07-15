package unsafe_alias

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Build(message string) *rpctypes.RpcError {
	constructor := rpctypes.NewRpcError
	return constructor(rpctypes.RpcINTERNAL, "internal", "internal", message)
}
