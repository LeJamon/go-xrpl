package handlers

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// NftSellOffersMethod handles the nft_sell_offers RPC method
// Reference: rippled NFTOffers.cpp doNFTSellOffers
type NftSellOffersMethod struct{ BaseHandler }

func (m *NftSellOffersMethod) Handle(ctx *types.RpcContext, params json.RawMessage) (any, *types.RpcError) {
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

	ledgerIndex := resolveLedgerIndex(request.LedgerIndex)

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

	result, err := ctx.Services.Ledger.GetNFTSellOffers(ctx.Context, nftID, ledgerIndex, limit, marker)
	if err != nil {
		switch {
		case errors.Is(err, svcerr.ErrLedgerNotFound):
			return nil, types.RpcErrorLgrNotFound("Ledger not found.")
		case errors.Is(err, svcerr.ErrObjectNotFound):
			return nil, types.RpcErrorObjectNotFound("The requested object was not found.")
		case errors.Is(err, svcerr.ErrInvalidMarker):
			return nil, types.RpcErrorInvalidParams("Invalid marker")
		}
		return nil, types.RpcErrorInternal(fmt.Sprintf("Failed to get NFT sell offers: %v", err))
	}

	return buildNFTOffersResponse(nftIDHex, result, limit), nil
}
