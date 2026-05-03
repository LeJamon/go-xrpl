package handlers

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// NftBuyOffersMethod handles the nft_buy_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTBuyOffers
type NftBuyOffersMethod struct{ BaseHandler }

func (m *NftBuyOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
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

	if err := RequireLedgerService(ctx.Services); err != nil {
		return nil, err
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Apply limit clamping matching rippled's readLimitField with nftOffers tuning.
	// Reference: NFTOffers.cpp line 69: readLimitField(limit, RPC::Tuning::nftOffers, context)
	var userLimit uint32
	if request.Limit != nil {
		userLimit = *request.Limit
	}
	limit := ClampLimit(userLimit, LimitNFTOffers, ctx.IsAdmin)

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

	result, err := ctx.Services.Ledger.GetNFTBuyOffers(nftID, ledgerIndex, limit, marker)
	if err != nil {
		if err.Error() == "ledger not found" {
			return nil, types.RpcErrorLgrNotFound("Ledger not found.")
		}
		if err.Error() == "object not found" || err.Error() == "directory not found" {
			return nil, types.RpcErrorObjectNotFound("The requested object was not found.")
		}
		if err.Error() == "invalid marker" {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get NFT buy offers: %v", err))
	}

	return buildNFTOffersResponse(nftIDHex, result, limit), nil
}

// buildNFTOffersResponse builds the JSON response for NFT offer queries.
// Shared between nft_buy_offers and nft_sell_offers.
// Reference: rippled NFTOffers.cpp enumerateNFTOffers + appendNftOfferJson
func buildNFTOffersResponse(nftIDHex string, result *types.NFTOffersResult, limit uint32) map[string]interface{} {
	offers := make([]map[string]interface{}, len(result.Offers))
	for i, offer := range result.Offers {
		offerObj := map[string]interface{}{
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

	response := map[string]interface{}{
		"nft_id":       nftIDHex,
		"offers":       offers,
		"ledger_hash":  strings.ToUpper(FormatLedgerHash(result.LedgerHash)),
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
