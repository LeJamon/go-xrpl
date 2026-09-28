package handlers

import (
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func modernSyntheticMetadataContext() SyntheticMetadataContext {
	return SyntheticMetadataContext{LedgerSequence: deliveredAmountLedgerCutoff}
}

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

func TestInjectSyntheticFields_DeliveredAmount(t *testing.T) {
	t.Run("payment without engine amount falls back to tx Amount", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "AffectedNodes": []any{}}
		InjectSyntheticFields(map[string]any{"TransactionType": "Payment", "Amount": "100"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, "100", meta["delivered_amount"])
	})

	t.Run("engine-recorded delivered_amount is preserved", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "delivered_amount": "50", "AffectedNodes": []any{}}
		InjectSyntheticFields(map[string]any{"TransactionType": "Payment", "Amount": "100"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, "50", meta["delivered_amount"])
	})

	t.Run("non-payment gets no delivered_amount", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tesSUCCESS", "AffectedNodes": []any{}}
		InjectSyntheticFields(map[string]any{"TransactionType": "AccountSet"}, meta, modernSyntheticMetadataContext())
		assert.NotContains(t, meta, "delivered_amount")
	})

	t.Run("failed transaction is a no-op", func(t *testing.T) {
		meta := map[string]any{"TransactionResult": "tecUNFUNDED_PAYMENT", "AffectedNodes": []any{}}
		InjectSyntheticFields(map[string]any{"TransactionType": "Payment", "Amount": "100"}, meta, modernSyntheticMetadataContext())
		assert.NotContains(t, meta, "delivered_amount")
	})
}

func TestMetadataToMapClonesInput(t *testing.T) {
	original := map[string]any{"TransactionResult": "tesSUCCESS"}
	cloned := metadataToMap(original)
	cloned["mpt_issuance_id"] = "derived"

	assert.NotContains(t, original, "mpt_issuance_id")
}

func TestInjectSyntheticFields_NFTokenMint(t *testing.T) {
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
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenMint"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, idB, meta["nftoken_id"])
	})

	t.Run("created page (first token) reveals the added token", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", idB)},
		}
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenMint"}, meta, modernSyntheticMetadataContext())
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
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenMint", "Amount": "10"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, idB, meta["nftoken_id"])
		assert.Equal(t, offerIndex, meta["offer_id"])
	})
}

func TestInjectSyntheticFields_NFTokenOffers(t *testing.T) {
	idA := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF0000000A"
	idB := "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF0000000B"

	t.Run("accept-offer emits scalar nftoken_id", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{deletedOfferNode(idA)},
		}
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenAcceptOffer"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, idA, meta["nftoken_id"])
	})

	t.Run("cancel-offer emits sorted, de-duplicated nftoken_ids array", func(t *testing.T) {
		meta := map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes":     []any{deletedOfferNode(idB), deletedOfferNode(idA), deletedOfferNode(idB)},
		}
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenCancelOffer"}, meta, modernSyntheticMetadataContext())
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
		InjectSyntheticFields(map[string]any{"TransactionType": "NFTokenCreateOffer"}, meta, modernSyntheticMetadataContext())
		assert.Equal(t, offerIndex, meta["offer_id"])
	})
}

func TestInjectSyntheticFields_MPTokenIssuanceID(t *testing.T) {
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
	InjectSyntheticFields(map[string]any{"TransactionType": "MPTokenIssuanceCreate"}, meta, modernSyntheticMetadataContext())
	assert.Equal(t, want, meta["mpt_issuance_id"])

	malformed := map[string]any{
		"TransactionResult": "tesSUCCESS",
		"AffectedNodes": []any{
			map[string]any{"CreatedNode": map[string]any{
				"LedgerEntryType": "MPTokenIssuance",
				"NewFields":       map[string]any{"Sequence": 42.5, "Issuer": issuer},
			}},
		},
	}
	InjectSyntheticFields(map[string]any{"TransactionType": "MPTokenIssuanceCreate"}, malformed, modernSyntheticMetadataContext())
	assert.NotContains(t, malformed, "mpt_issuance_id")
}

func TestExpandStoredTransactionSyntheticMetadata(t *testing.T) {
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	seq := uint32(42)
	accountID, err := decodeAccountID(issuer)
	require.NoError(t, err)
	mptID := keylet.MakeMPTID(seq, accountID)
	want := strings.ToUpper(hex.EncodeToString(mptID[:]))

	newMPTMeta := func() map[string]any {
		return map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes": []any{
				map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "MPTokenIssuance",
					"NewFields":       map[string]any{"Sequence": float64(seq), "Issuer": issuer},
				}},
			},
		}
	}

	for _, tc := range []struct {
		name       string
		apiVersion int
		metaKey    string
	}{
		{name: "api v1", apiVersion: 1, metaKey: "metaData"},
		{name: "api v2", apiVersion: 2, metaKey: "meta"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response, err := expandStoredTransaction(StoredTransaction{
				TxJSON: map[string]any{"TransactionType": "MPTokenIssuanceCreate"},
				Meta:   newMPTMeta(),
			}, strings.Repeat("0", 64), false, tc.apiVersion, modernSyntheticMetadataContext())
			require.NoError(t, err)
			meta := response[tc.metaKey].(map[string]any)
			assert.Equal(t, want, meta["mpt_issuance_id"])
		})
	}

	offerID := strings.Repeat("A", 64)
	response, err := expandStoredTransaction(StoredTransaction{
		TxJSON: map[string]any{"TransactionType": "NFTokenCreateOffer"},
		Meta: map[string]any{
			"TransactionResult": "tesSUCCESS",
			"AffectedNodes": []any{
				map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "NFTokenOffer",
					"LedgerIndex":     offerID,
				}},
			},
		},
	}, strings.Repeat("0", 64), false, 2, modernSyntheticMetadataContext())
	require.NoError(t, err)
	assert.Equal(t, offerID, response["meta"].(map[string]any)["offer_id"])
}

func TestExpandStoredTransactionSyntheticNFTAndAccountDeleteMetadata(t *testing.T) {
	const (
		mintedID = "000800001234567890ABCDEF1234567890ABCDEF1234567890ABCDEF00000001"
		offerID  = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	)

	tests := []struct {
		name     string
		txJSON   map[string]any
		metadata func() map[string]any
		want     map[string]any
	}{
		{
			name:   "successful mint",
			txJSON: map[string]any{"TransactionType": "NFTokenMint"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", mintedID)},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", mintedID)},
				"nftoken_id":        mintedID,
			},
		},
		{
			name:   "failed mint has no synthetic fields",
			txJSON: map[string]any{"TransactionType": "NFTokenMint"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tecNO_PERMISSION",
					"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", mintedID)},
				}
			},
			want: map[string]any{
				"TransactionResult": "tecNO_PERMISSION",
				"AffectedNodes":     []any{nftPageNode("CreatedNode", "NewFields", mintedID)},
			},
		},
		{
			name:   "burn has no synthetic fields",
			txJSON: map[string]any{"TransactionType": "NFTokenBurn"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"AffectedNodes":     []any{nftPageNode("ModifiedNode", "FinalFields", mintedID)},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes":     []any{nftPageNode("ModifiedNode", "FinalFields", mintedID)},
			},
		},
		{
			name:   "accept offer emits nftoken_id",
			txJSON: map[string]any{"TransactionType": "NFTokenAcceptOffer"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"AffectedNodes":     []any{deletedOfferNode(mintedID)},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes":     []any{deletedOfferNode(mintedID)},
				"nftoken_id":        mintedID,
			},
		},
		{
			name:   "cancel offers emits nftoken_ids",
			txJSON: map[string]any{"TransactionType": "NFTokenCancelOffer"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"AffectedNodes":     []any{deletedOfferNode(offerID), deletedOfferNode(mintedID), deletedOfferNode(offerID)},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes":     []any{deletedOfferNode(offerID), deletedOfferNode(mintedID), deletedOfferNode(offerID)},
				"nftoken_ids":       []any{mintedID, offerID},
			},
		},
		{
			name:   "create offer emits offer_id",
			txJSON: map[string]any{"TransactionType": "NFTokenCreateOffer"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"AffectedNodes": []any{map[string]any{"CreatedNode": map[string]any{
						"LedgerEntryType": "NFTokenOffer",
						"LedgerIndex":     offerID,
					}}},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"AffectedNodes": []any{map[string]any{"CreatedNode": map[string]any{
					"LedgerEntryType": "NFTokenOffer",
					"LedgerIndex":     offerID,
				}}},
				"offer_id": offerID,
			},
		},
		{
			name:   "account delete copies delivered amount",
			txJSON: map[string]any{"TransactionType": "AccountDelete"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tesSUCCESS",
					"DeliveredAmount":   "25",
					"AffectedNodes":     []any{},
				}
			},
			want: map[string]any{
				"TransactionResult": "tesSUCCESS",
				"DeliveredAmount":   "25",
				"AffectedNodes":     []any{},
				"delivered_amount":  "25",
			},
		},
		{
			name:   "account delete failure has no delivered amount",
			txJSON: map[string]any{"TransactionType": "AccountDelete"},
			metadata: func() map[string]any {
				return map[string]any{
					"TransactionResult": "tecNO_PERMISSION",
					"DeliveredAmount":   "25",
					"AffectedNodes":     []any{},
				}
			},
			want: map[string]any{
				"TransactionResult": "tecNO_PERMISSION",
				"DeliveredAmount":   "25",
				"AffectedNodes":     []any{},
			},
		},
	}

	for _, tc := range tests {
		for _, apiVersion := range []int{1, 2, 3} {
			metaKey := "metaData"
			if apiVersion > 1 {
				metaKey = "meta"
			}
			t.Run(tc.name+"/api_v"+strconv.Itoa(apiVersion), func(t *testing.T) {
				response, err := expandStoredTransaction(StoredTransaction{
					TxJSON: tc.txJSON,
					Meta:   tc.metadata(),
				}, strings.Repeat("0", 64), false, apiVersion, modernSyntheticMetadataContext())
				require.NoError(t, err)
				assert.Equal(t, tc.want, response[metaKey])
				if apiVersion == 1 {
					assert.NotContains(t, response, "meta")
				} else {
					assert.NotContains(t, response, "metaData")
				}
			})
		}
	}
}

func TestExpandStoredTransactionBinaryMetadataRemainsRaw(t *testing.T) {
	const offerID = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	metadata := map[string]any{
		"TransactionResult": "tesSUCCESS",
		"AffectedNodes": []any{map[string]any{"CreatedNode": map[string]any{
			"LedgerEntryType": "NFTokenOffer",
			"LedgerIndex":     offerID,
		}}},
	}
	wantMetaBlob, err := binarycodec.Encode(metadata)
	require.NoError(t, err)

	for _, apiVersion := range []int{1, 2, 3} {
		response, err := expandStoredTransaction(StoredTransaction{
			TxJSON: map[string]any{"TransactionType": "NFTokenCreateOffer"},
			Meta:   metadata,
		}, strings.Repeat("0", 64), true, apiVersion, modernSyntheticMetadataContext())
		require.NoError(t, err)
		metaKey := "meta"
		if apiVersion > 1 {
			metaKey = "meta_blob"
		}
		assert.Equal(t, wantMetaBlob, response[metaKey])
	}
}

func TestExpandStoredTransactionProjection(t *testing.T) {
	const hash = "ABCDEF"
	newPayment := func() StoredTransaction {
		return StoredTransaction{
			TxJSON: map[string]any{"TransactionType": "Payment", "Amount": "100"},
			Meta:   map[string]any{"TransactionResult": "tesSUCCESS"},
		}
	}

	v1, err := expandStoredTransaction(newPayment(), hash, false, 1, modernSyntheticMetadataContext())
	require.NoError(t, err)
	assert.Equal(t, "100", v1["Amount"])
	assert.Equal(t, "100", v1["DeliverMax"])
	assert.Equal(t, hash, v1["hash"])
	assert.Equal(t, "100", v1["metaData"].(map[string]any)["delivered_amount"])

	v2, err := expandStoredTransaction(newPayment(), hash, false, 2, modernSyntheticMetadataContext())
	require.NoError(t, err)
	v2Tx := v2["tx_json"].(map[string]any)
	assert.NotContains(t, v2Tx, "Amount")
	assert.Equal(t, "100", v2Tx["DeliverMax"])
	assert.Equal(t, hash, v2["hash"])
	assert.Equal(t, "100", v2["meta"].(map[string]any)["delivered_amount"])

	v1Binary, err := expandStoredTransaction(newPayment(), hash, true, 1, modernSyntheticMetadataContext())
	require.NoError(t, err)
	assert.NotContains(t, v1Binary, "hash")
	v2Binary, err := expandStoredTransaction(newPayment(), hash, true, 2, modernSyntheticMetadataContext())
	require.NoError(t, err)
	assert.Equal(t, hash, v2Binary["hash"])

	_, err = expandTransaction([]byte{0xFF}, hash, true, 1, modernSyntheticMetadataContext())
	assert.Error(t, err)
	_, err = expandTransaction([]byte{0xFF}, hash, true, 2, modernSyntheticMetadataContext())
	assert.Error(t, err)

	accountDelete, err := expandStoredTransaction(StoredTransaction{
		TxJSON: map[string]any{"TransactionType": "AccountDelete"},
		Meta:   map[string]any{"TransactionResult": "tesSUCCESS"},
	}, hash, false, 2, modernSyntheticMetadataContext())
	require.NoError(t, err)
	assert.Equal(t, "unavailable", accountDelete["meta"].(map[string]any)["delivered_amount"])
}
