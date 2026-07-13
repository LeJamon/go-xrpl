package handlers

import (
	"github.com/LeJamon/go-xrpl/internal/rpc/loadtrack"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func setLoadReference(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(loadtrack.LoadReference)
}

func setLoadMedium(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(loadtrack.LoadMedium)
}

func setLoadHeavy(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(loadtrack.LoadHeavy)
}
