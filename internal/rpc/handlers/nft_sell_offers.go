package handlers

import (
	"encoding/json"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// NftSellOffersMethod handles the nft_sell_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTSellOffers
type NftSellOffersMethod struct{ BaseHandler }

func (m *NftSellOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	return handleNFTOffers(ctx, params, ctx.Services.Ledger.GetNFTSellOffers)
}
