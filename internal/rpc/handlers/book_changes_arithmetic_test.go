package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestComputeBookChangesCanonicalArithmeticAndRendering(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	tests := []struct {
		name        string
		gets, pays  string
		wantVolumeB string
		wantRate    string
	}{
		{
			name:        "divide rounds one third like STAmount",
			gets:        "1",
			pays:        "3",
			wantVolumeB: "3",
			wantRate:    "0.3333333333333334",
		},
		{
			name:        "scientific output uses IOU Number formatting",
			gets:        "1",
			pays:        "1000000000000000e5",
			wantVolumeB: "1e20",
			wantRate:    "1e-20",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			amount := func(value string) map[string]any {
				return map[string]any{"currency": "USD", "issuer": issuer, "value": value}
			}
			transaction := validBookChangesTxJSON()
			metadata := map[string]any{"AffectedNodes": []any{
				map[string]any{"ModifiedNode": map[string]any{
					"LedgerEntryType": "Offer",
					"PreviousFields": map[string]any{
						"TakerGets": test.gets,
						"TakerPays": amount(test.pays),
					},
					"FinalFields": map[string]any{
						"TakerGets": "0",
						"TakerPays": amount("0"),
					},
				}},
			}}
			blob, err := json.Marshal(StoredTransaction{TxJSON: transaction, Meta: metadata})
			require.NoError(t, err)

			ledger := mptBookChangesLedger{blob: blob}
			result := ComputeBookChanges(ledger)
			predecoded := ComputeBookChangesFromTransactions(ledger, []BookChangesTransaction{{
				Transaction: transaction,
				Metadata:    metadata,
			}})
			require.Equal(t, result, predecoded)
			changes := result["changes"].([]map[string]any)
			require.Len(t, changes, 1)
			change := changes[0]
			require.Equal(t, "1", change["volume_a"])
			require.Equal(t, test.wantVolumeB, change["volume_b"])
			for _, field := range []string{"high", "low", "open", "close"} {
				require.Equal(t, test.wantRate, change[field], field)
			}
		})
	}
}

func validBookChangesTxJSON() map[string]any {
	return map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "AccountSet",
	}
}
