package handlers

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// nftPageNode builds an NFTokenPage affected node with the given NFTokenIDs in
// the named fields object (NewFields / FinalFields / PreviousFields).
func nftPageNode(action, fieldsKey string, ids ...string) map[string]any {
	tokens := make([]any, len(ids))
	for i, id := range ids {
		tokens[i] = map[string]any{"NFToken": map[string]any{"NFTokenID": id}}
	}
	return map[string]any{
		action: map[string]any{
			"LedgerEntryType": "NFTokenPage",
			fieldsKey:         map[string]any{"NFTokens": tokens},
		},
	}
}

func deletedOfferNode(nftokenID string) map[string]any {
	return map[string]any{
		"DeletedNode": map[string]any{
			"LedgerEntryType": "NFTokenOffer",
			"FinalFields":     map[string]any{"NFTokenID": nftokenID},
		},
	}
}

func TestEnrichSimulateMeta_DeliveredAmount(t *testing.T) {
	t.Run("payment without engine amount falls back to tx Amount", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "AffectedNodes": []any{}}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "Payment", "Amount": "100"})
		assert.Equal(t, "100", meta["delivered_amount"])
	})

	t.Run("engine-recorded delivered_amount is preserved", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "delivered_amount": "50", "AffectedNodes": []any{}}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "Payment", "Amount": "100"})
		assert.Equal(t, "50", meta["delivered_amount"])
	})

	t.Run("non-payment gets no delivered_amount", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "AffectedNodes": []any{}}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "AccountSet"})
		assert.NotContains(t, meta, "delivered_amount")
	})

	t.Run("failed transaction is a no-op", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tecUNFUNDED_PAYMENT", "AffectedNodes": []any{}}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "Payment", "Amount": "100"})
		assert.NotContains(t, meta, "delivered_amount")
	})
}

func TestEnrichSimulateMeta_NFTokenMint(t *testing.T) {
	idA := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF00000001"
	idB := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF00000002"

	t.Run("modified page reveals the added token", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes": []any{
				map[string]any{"ModifiedNode": map[string]any{
					"LedgerEntryType": "NFTokenPage",
					"PreviousFields":  map[string]any{"NFTokens": []any{map[string]any{"NFToken": map[string]any{"NFTokenID": idA}}}},
					"FinalFields": map[string]any{"NFTokens": []any{
						map[string]any{"NFToken": map[string]any{"NFTokenID": idA}},
						map[string]any{"NFToken": map[string]any{"NFTokenID": idB}},
					}},
				}},
			},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenMint"})
		assert.Equal(t, idB, meta["nftoken_id"])
	})

	t.Run("created page (first token) reveals the added token", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", idB)},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenMint"})
		assert.Equal(t, idB, meta["nftoken_id"])
	})

	t.Run("mint that lists the token also emits offer_id", func(t *testing.T) {
		offerIndex := "AAAA0000000000000000000000000000000000000000000000000000000000FF"
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes": []any{
				nftPageNode("CreatedNode", "NewFields", idB),
				map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "NFTokenOffer",
					"LedgerIndex":     offerIndex,
				}},
			},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenMint", "Amount": "10"})
		assert.Equal(t, idB, meta["nftoken_id"])
		assert.Equal(t, offerIndex, meta["offer_id"])
	})
}

func TestEnrichSimulateMeta_NFTokenOffers(t *testing.T) {
	idA := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF0000000A"
	idB := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF0000000B"

	t.Run("accept-offer emits scalar nftoken_id", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{deletedOfferNode(idA)},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenAcceptOffer"})
		assert.Equal(t, idA, meta["nftoken_id"])
	})

	t.Run("cancel-offer emits sorted, de-duplicated nftoken_ids array", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{deletedOfferNode(idB), deletedOfferNode(idA), deletedOfferNode(idB)},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenCancelOffer"})
		assert.Equal(t, []any{idA, idB}, meta["nftoken_ids"])
	})

	t.Run("create-offer emits offer_id from the created offer", func(t *testing.T) {
		offerIndex := "BBBB0000000000000000000000000000000000000000000000000000000000FF"
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes": []any{
				map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "NFTokenOffer",
					"LedgerIndex":     offerIndex,
				}},
			},
		}
		enrichSimulateMeta(meta, map[string]any{"TransactionType": "NFTokenCreateOffer"})
		assert.Equal(t, offerIndex, meta["offer_id"])
	})
}

func TestEnrichSimulateMeta_MPTokenIssuanceID(t *testing.T) {
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	seq := uint32(42)

	accountID, err := decodeAccountID(issuer)
	require.NoError(t, err)
	mptID := keylet.MakeMPTID(seq, accountID)
	want := strings.ToUpper(hex.EncodeToString(mptID[:]))

	meta := map[string]any{
		"TransactionResult": "tesSUCCESS",
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "MPTokenIssuance",
				"NewFields":       map[string]any{"Sequence": float64(seq), "Issuer": issuer},
			}},
		},
	}
	enrichSimulateMeta(meta, map[string]any{"TransactionType": "MPTokenIssuanceCreate"})
	assert.Equal(t, want, meta["mpt_issuance_id"])
}
