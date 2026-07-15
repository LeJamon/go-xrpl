package unsafe_dynamic_composite

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

func Build(code int, message string) *rpctypes.RpcError {
	return &rpctypes.RpcError{
		Code:        code,
		ErrorString: "dynamic",
		Type:        "dynamic",
		Message:     message,
	}
}
