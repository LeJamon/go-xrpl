package unsafe_reassignment

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func safeConstructor(_ int, _, _, message string) *rpctypes.RpcError {
	return rpctypes.RpcErrorInvalidParams(message)
}

func Build(message string) *rpctypes.RpcError {
	constructor := safeConstructor
	constructor = rpctypes.NewRpcError
	return constructor(72+1, "internal", "internal", message)
}
