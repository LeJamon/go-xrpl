package handlers

import (
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func setLoadReference(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(resource.FeeReferenceRPC().Cost())
}

func setLoadMedium(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(resource.FeeMediumBurdenRPC().Cost())
}

func setLoadHeavy(ctx *types.RpcContext) {
	ctx.LoadCost = uint32(resource.FeeHeavyBurdenRPC().Cost())
}
