package unsafe_wrapper

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func wrap(code int, message string) *rpctypes.RpcError {
	return rpctypes.NewRpcError(code, "internal", "internal", message)
}

func Build(message string) *rpctypes.RpcError {
	constructor := wrap
	return constructor(72+1, message)
}
