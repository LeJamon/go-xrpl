package unsafe_promoted_mutation

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

type wrappedError struct {
	*rpcerrors.RpcError
}

func Mutate(message string) *rpcerrors.RpcError {
	wrapper := wrappedError{RpcError: rpcerrors.RpcErrorInvalidParams(message)}
	wrapper.Message = message
	return wrapper.RpcError
}
