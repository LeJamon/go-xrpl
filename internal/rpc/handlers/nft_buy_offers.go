package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// NftBuyOffersMethod handles the nft_buy_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTBuyOffers
type NftBuyOffersMethod struct{ BaseHandler }

func (m *NftBuyOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}
	return handleNFTOffers(ctx, params, ctx.Services.Ledger.GetNFTBuyOffers)
}

// handleNFTOffers is the shared nft_buy_offers / nft_sell_offers flow; the only
// difference between buy and sell is the fetch function. The caller guards the
// ledger service before binding fetch.
// Reference: rippled NFTOffers.cpp doNFTBuyOffers / doNFTSellOffers
func handleNFTOffers(ctx *types.RpcContext, params json.RawMessage, fetch func(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error)) (any, *types.RpcError) {
	if rpcErr := validateJsonCppIntegerRange(params); rpcErr != nil {
		return nil, rpcErr
	}
	fields := make(map[string]json.RawMessage)
	if params != nil {
		if err := json.Unmarshal(params, &fields); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}

	nftIDRaw, hasNFTID := fields["nft_id"]
	if !hasNFTID {
		return nil, types.RpcErrorMissingField("nft_id")
	}
	nftIDValue, validString := jsonCppStringRaw(nftIDRaw)
	if !validString {
		return nil, types.RpcErrorInvalidField("nft_id")
	}

	// Validate and parse the NFT ID - must be a 64-character hex string (32 bytes)
	nftIDHex := strings.ToUpper(nftIDValue)
	if len(nftIDHex) != 64 {
		return nil, types.RpcErrorInvalidField("nft_id")
	}

	nftIDBytes, err := hex.DecodeString(nftIDHex)
	if err != nil {
		return nil, types.RpcErrorInvalidField("nft_id")
	}

	var nftID [32]byte
	copy(nftID[:], nftIDBytes)

	// Apply rippled's readLimitField with nftOffers tuning (NFTOffers.cpp:69):
	// absent limit -> default, explicit 0 -> invalidParams, else clamp.
	limit, limitErr := ReadLimitField(params, LimitNFTOffers, ctx.Unlimited)
	if limitErr != nil {
		return nil, limitErr
	}
	parsedLedgerSpec, _, ledgerSpecErr := parseLedgerSpecifier(params)
	if ledgerSpecErr != nil {
		return nil, ledgerSpecErr
	}
	ledgerIndex, selErr := resolveLedgerSelector(parsedLedgerSpec)
	if selErr != nil {
		return nil, selErr
	}
	if _, _, lookupErr := LookupLedger(ctx, parsedLedgerSpec); lookupErr != nil {
		return nil, lookupErr
	}

	marker := ""
	if markerRaw, hasMarker := fields["marker"]; hasMarker {
		var markerIsString bool
		marker, markerIsString = rawJSONString(markerRaw)
		if !markerIsString || marker == "" {
			if _, err := fetch(ctx.Context, nftID, ledgerIndex, limit, ""); errors.Is(err, svcerr.ErrObjectNotFound) {
				return nil, types.RpcErrorObjectNotFound("The requested object was not found.")
			}
			if !markerIsString {
				return nil, types.RpcErrorExpectedField("marker", "string")
			}
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
	}

	result, err := fetch(ctx.Context, nftID, ledgerIndex, limit, marker)
	if err != nil {
		if lgrErr := mapLedgerLookupErr(err); lgrErr != nil {
			return nil, lgrErr
		}
		switch {
		case errors.Is(err, svcerr.ErrObjectNotFound):
			return nil, types.RpcErrorObjectNotFound("The requested object was not found.")
		case errors.Is(err, svcerr.ErrInvalidMarker):
			return nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		return nil, rpcInternalError("nft_offers: ledger query failed", err)
	}

	setLoadMedium(ctx)
	return buildNFTOffersResponse(nftIDHex, result, limit), nil
}

// buildNFTOffersResponse builds the JSON response for NFT offer queries.
// Shared between nft_buy_offers and nft_sell_offers.
// Reference: rippled NFTOffers.cpp enumerateNFTOffers + appendNftOfferJson
func buildNFTOffersResponse(nftIDHex string, result *types.NFTOffersResult, limit uint32) map[string]any {
	offers := make([]map[string]any, len(result.Offers))
	for i, offer := range result.Offers {
		offerObj := map[string]any{
			"nft_offer_index": offer.NFTOfferIndex,
			"flags":           offer.Flags,
			"owner":           offer.Owner,
			"amount":          offer.Amount,
		}

		if offer.Destination != "" {
			offerObj["destination"] = offer.Destination
		}
		if offer.Expiration > 0 {
			offerObj["expiration"] = offer.Expiration
		}

		offers[i] = offerObj
	}

	response := map[string]any{
		"nft_id": nftIDHex,
		"offers": offers,
	}

	// rippled includes limit and marker only when there are more results (pagination).
	// Reference: NFTOffers.cpp lines 136-141
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response
}
