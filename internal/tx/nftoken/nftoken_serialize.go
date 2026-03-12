package nftoken

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
)

// ---------------------------------------------------------------------------
// Serialization helpers
// ---------------------------------------------------------------------------

// SerializeNFTokenPage serializes an NFToken page ledger entry.
// Exported so that LedgerStateFix can use it to repair pages.
func SerializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	return serializeNFTokenPage(page)
}

// serializeNFTokenPage serializes an NFToken page ledger entry
func serializeNFTokenPage(page *state.NFTokenPageData) ([]byte, error) {
	jsonObj := map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"Flags":           uint32(0),
	}

	var emptyHash [32]byte
	if page.PreviousPageMin != emptyHash {
		jsonObj["PreviousPageMin"] = strings.ToUpper(hex.EncodeToString(page.PreviousPageMin[:]))
	}

	if page.NextPageMin != emptyHash {
		jsonObj["NextPageMin"] = strings.ToUpper(hex.EncodeToString(page.NextPageMin[:]))
	}

	if len(page.NFTokens) > 0 {
		nfTokens := make([]map[string]any, len(page.NFTokens))
		for i, token := range page.NFTokens {
			nfToken := map[string]any{
				"NFToken": map[string]any{
					"NFTokenID": strings.ToUpper(hex.EncodeToString(token.NFTokenID[:])),
				},
			}
			if token.URI != "" {
				nfToken["NFToken"].(map[string]any)["URI"] = token.URI
			}
			nfTokens[i] = nfToken
		}
		jsonObj["NFTokens"] = nfTokens
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenPage: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// amountToCodecFormat converts a tx.Amount to the format expected by binarycodec.Encode.
// XRP → string of drops ("1000000"), IOU → map[string]any{"value":"10","currency":"USD","issuer":"rAddr"}
func amountToCodecFormat(amt tx.Amount) any {
	if amt.IsNative() {
		return fmt.Sprintf("%d", amt.Drops())
	}
	return map[string]any{
		"value":    amt.IOU().String(),
		"currency": amt.Currency,
		"issuer":   amt.Issuer,
	}
}

// serializeNFTokenOfferRaw serializes an NFToken offer ledger entry from primitive parameters.
// amount can be a string (XRP drops) or map[string]any (IOU).
func serializeNFTokenOfferRaw(
	ownerID [20]byte, tokenID [32]byte,
	amount any, flags uint32,
	ownerNode, offerNode uint64,
	destination string, expiration *uint32,
) ([]byte, error) {
	ownerAddress, err := addresscodec.EncodeAccountIDToClassicAddress(ownerID[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode owner address: %w", err)
	}

	jsonObj := map[string]any{
		"LedgerEntryType":  "NFTokenOffer",
		"Account":          ownerAddress,
		"Amount":           amount,
		"NFTokenID":        strings.ToUpper(hex.EncodeToString(tokenID[:])),
		"OwnerNode":        fmt.Sprintf("%x", ownerNode),
		"NFTokenOfferNode": fmt.Sprintf("%x", offerNode),
		"Flags":            flags,
	}

	if expiration != nil {
		jsonObj["Expiration"] = *expiration
	}

	if destination != "" {
		jsonObj["Destination"] = destination
	}

	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode NFTokenOffer: %w", err)
	}

	return hex.DecodeString(hexStr)
}

// serializeNFTokenOffer serializes an NFToken offer from an NFTokenCreateOffer transaction.
func serializeNFTokenOffer(nftTx *NFTokenCreateOffer, ownerID [20]byte, tokenID [32]byte, sequence uint32, ownerNode uint64, offerNode uint64) ([]byte, error) {
	return serializeNFTokenOfferRaw(
		ownerID, tokenID,
		amountToCodecFormat(nftTx.Amount), nftTx.GetFlags(),
		ownerNode, offerNode,
		nftTx.Destination, nftTx.Expiration,
	)
}
