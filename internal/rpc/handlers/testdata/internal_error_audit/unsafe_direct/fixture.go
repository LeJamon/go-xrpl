package unsafe_direct

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Build(message string) *rpctypes.RpcError {
	return rpctypes.NewRpcError(72+1, "internal", "internal", message)
}
