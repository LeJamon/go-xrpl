package handlers

import (
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// NftSellOffersMethod handles the nft_sell_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTSellOffers
type NftSellOffersMethod struct{ BaseHandler }

func (m *NftSellOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (interface{}, *types.RpcError) {
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

	result, err := ctx.Services.Ledger.GetNFTSellOffers(nftID, ledgerIndex, limit, marker)
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
		return nil, types.RpcErrorInternal("Failed to get NFT sell offers: " + err.Error())
	}

	return buildNFTOffersResponse(nftIDHex, result, limit), nil
}
