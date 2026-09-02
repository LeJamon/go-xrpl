package binarycodec

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	binarycodectypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/stretchr/testify/require"
)

const innerTemplateTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func TestEncodeAndDecodeBytesValidatesInnerObjectTemplates(t *testing.T) {
	tests := []struct {
		name      string
		object    map[string]any
		wantError string
	}{
		{
			name: "SignerEntry missing required SignerWeight",
			object: map[string]any{
				"SignerEntries": []any{map[string]any{
					"SignerEntry": map[string]any{"Account": innerTemplateTestAccount},
				}},
			},
			wantError: "Field 'SignerWeight' is required but missing.",
		},
		{
			name: "SignerEntry disallowed field",
			object: map[string]any{
				"SignerEntries": []any{map[string]any{
					"SignerEntry": map[string]any{
						"Account":      innerTemplateTestAccount,
						"Amount":       "1",
						"SignerWeight": 1,
					},
				}},
			},
			wantError: "Field 'Amount' found in disallowed location.",
		},
		{
			name: "tagged X-address adds disallowed inner field",
			object: map[string]any{
				"SignerEntries": []any{map[string]any{
					"SignerEntry": map[string]any{
						"Account":      "XVYRdEocC28DRx94ZFGP3qNJ1D5Ln7kXKTG5X57UCKzEwYx",
						"SignerWeight": 1,
					},
				}},
			},
			wantError: "Field 'SourceTag' found in disallowed location.",
		},
		{
			name: "PriceData missing required QuoteAsset",
			object: map[string]any{
				"PriceDataSeries": []any{map[string]any{
					"PriceData": map[string]any{"BaseAsset": "XRP"},
				}},
			},
			wantError: "Field 'QuoteAsset' is required but missing.",
		},
		{
			name: "direct CounterpartySignature disallowed field",
			object: map[string]any{
				"CounterpartySignature": map[string]any{"Amount": "1"},
			},
			wantError: "Field 'Amount' found in disallowed location.",
		},
		{
			name: "nested registered Signer missing required TxnSignature",
			object: map[string]any{
				"BatchSigners": []any{map[string]any{
					"BatchSigner": map[string]any{
						"Account": innerTemplateTestAccount,
						"Signers": []any{map[string]any{
							"Signer": map[string]any{
								"Account":       innerTemplateTestAccount,
								"SigningPubKey": "",
							},
						}},
					},
				}},
			},
			wantError: "Field 'TxnSignature' is required but missing.",
		},
		{
			name: "valid array and direct inner objects",
			object: map[string]any{
				"BatchSigners": []any{map[string]any{
					"BatchSigner": map[string]any{
						"Account": innerTemplateTestAccount,
						"Signers": []any{map[string]any{
							"Signer": map[string]any{
								"Account":       innerTemplateTestAccount,
								"SigningPubKey": "",
								"TxnSignature":  "",
							},
						}},
					},
				}},
				"CounterpartySignature": map[string]any{},
				"SignerEntries": []any{map[string]any{
					"SignerEntry": map[string]any{
						"Account":      innerTemplateTestAccount,
						"SignerWeight": 1,
					},
				}},
				"PriceDataSeries": []any{map[string]any{
					"PriceData": map[string]any{
						"AssetPrice": uint64(740),
						"BaseAsset":  "XRP",
						"QuoteAsset": "USD",
						"Scale":      1,
					},
				}},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			blob, err := EncodeBytes(test.object)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				require.Nil(t, blob)
				return
			}
			require.NoError(t, err)

			decoded, err := DecodeBytes(blob)
			require.NoError(t, err)
			require.NotNil(t, decoded)
		})
	}
}

func TestEncodeBytesOmitsDefaultInnerObjectFields(t *testing.T) {
	blob, err := EncodeBytes(map[string]any{
		"PriceDataSeries": []any{map[string]any{
			"PriceData": map[string]any{
				"BaseAsset":  "XRP",
				"QuoteAsset": "USD",
				"Scale":      float64(0),
			},
		}},
	})
	require.NoError(t, err)

	decoded, err := DecodeBytes(blob)
	require.NoError(t, err)
	priceData := decoded["PriceDataSeries"].([]any)[0].(map[string]any)["PriceData"].(map[string]any)
	require.NotContains(t, priceData, "Scale")
}

func TestDecodeBytesWithTemplateValidatesTopLevelObject(t *testing.T) {
	manifest := map[string]any{
		"PublicKey":       "",
		"MasterSignature": "",
		"Sequence":        uint32(1),
	}

	canonical, err := EncodeBytes(manifest)
	require.NoError(t, err)
	decoded, err := DecodeBytesWithTemplate(canonical, "Manifest")
	require.NoError(t, err)
	require.NotContains(t, decoded, "Version")

	manifest["Version"] = uint16(0)
	explicitDefault, err := EncodeBytes(manifest)
	require.NoError(t, err)
	_, err = DecodeBytesWithTemplate(explicitDefault, "Manifest")
	require.EqualError(t, err, "Field 'Version' may not be explicitly set to default.")

	manifest["Version"] = uint16(1)
	nonDefault, err := EncodeBytes(manifest)
	require.NoError(t, err)
	decoded, err = DecodeBytesWithTemplate(nonDefault, "Manifest")
	require.NoError(t, err)
	require.Equal(t, 1, decoded["Version"])
}

func TestDecodeBytesValidatesInnerObjectTemplates(t *testing.T) {
	child, err := EncodeBytes(map[string]any{"Account": innerTemplateTestAccount})
	require.NoError(t, err)

	wrapper := serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec())
	signerEntry, err := definitions.Get().FieldInstanceByName("SignerEntry")
	require.NoError(t, err)
	require.NoError(t, wrapper.WriteFieldAndValue(*signerEntry, child))

	arrayValue := append(append([]byte(nil), wrapper.Bytes()...), binarycodectypes.ArrayEndMarker)
	outer := serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec())
	signerEntries, err := definitions.Get().FieldInstanceByName("SignerEntries")
	require.NoError(t, err)
	require.NoError(t, outer.WriteFieldAndValue(*signerEntries, arrayValue))

	decoded, err := DecodeBytes(outer.Bytes())
	require.ErrorContains(t, err, "Field 'SignerWeight' is required but missing.")
	require.Nil(t, decoded)
}

func TestEncodeBytesNullInnerObjectSemantics(t *testing.T) {
	blob, err := EncodeBytes(map[string]any{"CounterpartySignature": nil})
	require.Error(t, err)
	require.Nil(t, blob)

	var typedNil map[string]any
	blob, err = EncodeBytes(map[string]any{"Memo": typedNil})
	require.Error(t, err)
	require.Nil(t, blob)

	blob, err = EncodeBytes(map[string]any{
		"Memos": []any{map[string]any{"Memo": typedNil}},
	})
	require.NoError(t, err)
	decoded, err := DecodeBytes(blob)
	require.NoError(t, err)
	require.Equal(t, map[string]any{}, decoded["Memos"].([]any)[0].(map[string]any)["Memo"])
}

func TestDecodeBytesRejectsObjectEndMarkerAtArrayScope(t *testing.T) {
	_, err := DecodeBytes([]byte{0xF8, 0xE1})
	require.ErrorContains(t, err, "Illegal terminator in array")
}

func TestDecodeBytesAllowsUntemplatedObjectWrapper(t *testing.T) {
	blob, err := EncodeBytes(map[string]any{
		"Permissions": []any{map[string]any{
			"Memo": map[string]any{"MemoData": ""},
		}},
	})
	require.NoError(t, err)

	decoded, err := DecodeBytes(blob)
	require.NoError(t, err)
	require.NotNil(t, decoded)
}
