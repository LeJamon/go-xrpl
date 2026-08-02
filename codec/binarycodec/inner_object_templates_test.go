package binarycodec

import (
	"testing"

	"github.com/stretchr/testify/require"
)

const innerTemplateTestAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

func TestDecodeBytesValidatesInnerObjectTemplates(t *testing.T) {
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
			name: "PriceData missing required QuoteAsset",
			object: map[string]any{
				"PriceDataSeries": []any{map[string]any{
					"PriceData": map[string]any{"BaseAsset": "XRP"},
				}},
			},
			wantError: "Field 'QuoteAsset' is required but missing.",
		},
		{
			name: "PriceData explicitly sets default Scale",
			object: map[string]any{
				"PriceDataSeries": []any{map[string]any{
					"PriceData": map[string]any{
						"BaseAsset":  "XRP",
						"QuoteAsset": "USD",
						"Scale":      0,
					},
				}},
			},
			wantError: "Field 'Scale' may not be explicitly set to default.",
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
			require.NoError(t, err)

			decoded, err := DecodeBytes(blob)
			if test.wantError != "" {
				require.ErrorContains(t, err, test.wantError)
				require.Nil(t, decoded)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, decoded)
		})
	}
}
