package unsafe_dynamic_composite

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

func Build(code int, message string) *rpcerrors.RpcError {
	return &rpcerrors.RpcError{
		Code:        code,
		ErrorString: "dynamic",
		Type:        "dynamic",
		Message:     message,
	}
}
