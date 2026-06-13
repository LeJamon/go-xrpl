package handlers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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

// handleNFTOffers implements the shared nft_buy_offers / nft_sell_offers flow:
// parse, validate the nft_id and marker, clamp the limit, resolve the ledger
// selector, run the supplied fetch, map errors, and build the response. The
// caller guards the ledger service before binding fetch; the only difference
// between buy and sell is the fetch function.
// Reference: rippled NFTOffers.cpp doNFTBuyOffers / doNFTSellOffers
func handleNFTOffers(ctx *types.RpcContext, params json.RawMessage, fetch func(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*types.NFTOffersResult, error)) (any, *types.RpcError) {
	var request struct {
		NFTokenID string `json:"nft_id"`
		types.LedgerSpecifier
		Limit  *uint32 `json:"limit,omitempty"`
		Marker string  `json:"marker,omitempty"`
	}

	if err := ParseParams(params, &request); err != nil {
		return nil, err
	}

	// Check for missing nft_id parameter - matching rippled's missing_field_error
	if request.NFTokenID == "" {
		return nil, types.RpcErrorMissingField("nft_id")
	}

	// Validate and parse the NFT ID - must be a 64-character hex string (32 bytes)
	nftIDHex := strings.ToUpper(request.NFTokenID)
	if len(nftIDHex) != 64 {
		return nil, types.RpcErrorInvalidField("nft_id")
	}

	nftIDBytes, err := hex.DecodeString(nftIDHex)
	if err != nil {
		return nil, types.RpcErrorInvalidField("nft_id")
	}

	var nftID [32]byte
	copy(nftID[:], nftIDBytes)

	ledgerIndex, selErr := resolveLedgerSelector(request.LedgerSpecifier)
	if selErr != nil {
		return nil, selErr
	}

	// Apply limit clamping matching rippled's readLimitField with nftOffers tuning.
	// Reference: NFTOffers.cpp line 69: readLimitField(limit, RPC::Tuning::nftOffers, context)
	var userLimit uint32
	if request.Limit != nil {
		userLimit = *request.Limit
	}
	limit := ClampLimit(userLimit, LimitNFTOffers, ctx.Unlimited)

	// Validate marker if provided - must be a valid hex string
	marker := request.Marker
	if marker != "" {
		if len(marker) != 64 {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		if _, err := hex.DecodeString(marker); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
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
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get NFT offers: %v", err))
	}

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
		"nft_id":       nftIDHex,
		"offers":       offers,
		"ledger_hash":  FormatLedgerHash(result.LedgerHash),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// rippled includes limit and marker only when there are more results (pagination).
	// Reference: NFTOffers.cpp lines 136-141
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response
}
