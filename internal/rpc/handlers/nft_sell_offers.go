package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// NftSellOffersMethod handles the nft_sell_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTSellOffers
type NftSellOffersMethod struct{}

func (m *NftSellOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
	var request struct {
		NFTokenID string `json:"nft_id"`
		types.LedgerSpecifier
		Limit  *uint32 `json:"limit,omitempty"`
		Marker string  `json:"marker,omitempty"`
	}

	if params != nil {
		if err := json.Unmarshal(params, &request); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid parameters: " + err.Error())
		}
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

	// Check if ledger service is available
	if types.Services == nil || types.Services.Ledger == nil {
		return nil, types.RpcErrorInternal("Ledger service not available")
	}

	// Determine ledger index to use
	ledgerIndex := "current"
	if request.LedgerIndex != "" {
		ledgerIndex = request.LedgerIndex.String()
	}

	// Parse and validate limit parameter matching rippled's readLimitField
	limit := nftOffersDefaultLimit
	if request.Limit != nil {
		limit = *request.Limit
		if limit < nftOffersMinLimit {
			limit = nftOffersMinLimit
		}
		if limit > nftOffersMaxLimit {
			limit = nftOffersMaxLimit
		}
	}

	// Validate marker if provided - must be a valid hex string
	marker := request.Marker
	if marker != "" {
		// Marker should be a 64-character hex string (offer index)
		if len(marker) != 64 {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		if _, err := hex.DecodeString(marker); err != nil {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
	}

	// Get NFT sell offers from the ledger service
	result, err := types.Services.Ledger.GetNFTSellOffers(nftID, ledgerIndex, limit, marker)
	if err != nil {
		// Check for specific error types
		if err.Error() == "ledger not found" {
			return nil, types.RpcErrorLgrNotFound("Ledger not found.")
		}
		if err.Error() == "object not found" || err.Error() == "directory not found" {
			return nil, types.RpcErrorObjectNotFound("The requested object was not found.")
		}
		if err.Error() == "invalid marker" {
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		return nil, types.RpcErrorInternal("Failed to get NFT sell offers: " + err.Error())
	}

	// Build offers array with proper field handling
	offers := make([]map[string]interface{}, len(result.Offers))
	for i, offer := range result.Offers {
		offerObj := map[string]interface{}{
			"nft_offer_index": offer.NFTOfferIndex,
			"flags":           offer.Flags,
			"owner":           offer.Owner,
			"amount":          offer.Amount,
		}

		// Add optional fields only if they have values
		if offer.Destination != "" {
			offerObj["destination"] = offer.Destination
		}
		if offer.Expiration > 0 {
			offerObj["expiration"] = offer.Expiration
		}

		offers[i] = offerObj
	}

	// Build response matching rippled format
	response := map[string]interface{}{
		"nft_id":       nftIDHex,
		"offers":       offers,
		"ledger_hash":  strings.ToUpper(FormatLedgerHash(result.LedgerHash)),
		"ledger_index": result.LedgerIndex,
		"validated":    result.Validated,
	}

	// Add limit and marker only if there are more results (pagination)
	if result.Marker != "" {
		response["limit"] = limit
		response["marker"] = result.Marker
	}

	return response, nil
}

func (m *NftSellOffersMethod) RequiredRole() types.Role {
	return types.RoleGuest
}

func (m *NftSellOffersMethod) SupportedApiVersions() []int {
	return []int{types.ApiVersion1, types.ApiVersion2, types.ApiVersion3}
}

func (m *NftSellOffersMethod) RequiredCondition() types.Condition {
	return types.NoCondition
}
