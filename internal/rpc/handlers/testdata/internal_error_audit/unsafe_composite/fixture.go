package unsafe_composite

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

type rpcErrorAlias = rpcerrors.RpcError

func Build(message string) *rpcErrorAlias {
	return &rpcErrorAlias{
		Code:        72 + 1,
		ErrorString: "internal",
		Type:        "internal",
		Message:     message,
	}
}
