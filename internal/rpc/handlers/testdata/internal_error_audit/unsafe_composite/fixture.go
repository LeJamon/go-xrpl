package unsafe_composite

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

type rpcErrorAlias = rpctypes.RpcError

func Build(message string) *rpcErrorAlias {
	return &rpcErrorAlias{
		Code:        72 + 1,
		ErrorString: "internal",
		Type:        "internal",
		Message:     message,
	}
}
