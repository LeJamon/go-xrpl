package unsafe_promoted_mutation

import rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"

type wrappedError struct {
	*rpctypes.RpcError
}

func Mutate(message string) *rpctypes.RpcError {
	wrapper := wrappedError{RpcError: rpctypes.RpcErrorInvalidParams(message)}
	wrapper.Message = message
	return wrapper.RpcError
}
