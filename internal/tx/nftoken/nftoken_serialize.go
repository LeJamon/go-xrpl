package nftoken

import (
	"encoding/hex"
	"fmt"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
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

	// Emit only once threaded (fresh pages are stamped by the apply layer) so a no-op
	// modify round-trips byte-identically and the unchanged-entry guard prunes it
	// (ApplyStateTable.cpp:154-157).
	if page.PreviousTxnID != emptyHash {
		jsonObj["PreviousTxnID"] = strings.ToUpper(hex.EncodeToString(page.PreviousTxnID[:]))
		jsonObj["PreviousTxnLgrSeq"] = page.PreviousTxnLgrSeq
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
		"LedgerEntryType": "NFTokenOffer",
		// rippled's NFTokenOffer ledger object uses sfOwner, not sfAccount
		// (ledger_entries.macro ltNFTOKEN_OFFER; NFTokenUtils.cpp:1074
		// (*offer)[sfOwner] = acctID). Emitting sfAccount diverges the SLE
		// bytes (account_hash fork) and the CreatedNode NewFields.
		"Owner":            ownerAddress,
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
	// The NFTokenOffer ledger object only carries lsfSellNFToken; the rest of the
	// transaction's flags (notably tfFullyCanonicalSig) must not leak into its
	// sfFlags. rippled sets (*offer)[sfFlags] = isSell ? lsfSellNFToken : 0.
	return serializeNFTokenOfferRaw(
		ownerID, tokenID,
		amountToCodecFormat(nftTx.Amount), nftTx.GetFlags()&NFTokenCreateOfferFlagSellNFToken,
		ownerNode, offerNode,
		nftTx.Destination, nftTx.Expiration,
	)
}
