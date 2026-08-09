package types

import "github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"

// RpcError remains an alias in the contract package so service interfaces can
// refer to the neutral error type without owning its metadata or projection.
type RpcError = rpcerrors.RpcError
