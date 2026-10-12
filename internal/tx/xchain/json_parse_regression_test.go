package xchain

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/stretchr/testify/require"
)

func TestXChainClaimIDJSONBatchPreservesUint64(t *testing.T) {
	const (
		account      = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
		otherAccount = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	)
	bridge := bridgeMap(XChainBridge{
		LockingChainDoor:  account,
		LockingChainIssue: tx.Asset{Currency: "XRP"},
		IssuingChainDoor:  otherAccount,
		IssuingChainIssue: tx.Asset{Currency: "XRP"},
	})

	registerBinaryParseTransactionTypes()
	for _, value := range []uint64{0xABCDEF1234567890, ^uint64(0)} {
		t.Run(fmt.Sprintf("%016X", value), func(t *testing.T) {
			inner := func(claimID uint64, sequence uint32) map[string]any {
				return map[string]any{
					"Account":         account,
					"TransactionType": tx.TypeXChainCommit.String(),
					"XChainBridge":    bridge,
					"XChainClaimID":   claimID,
					"Amount":          "1000000",
					"Sequence":        sequence,
					"Fee":             "0",
					"SigningPubKey":   "",
					"Flags":           tx.TfInnerBatchTxn,
				}
			}
			first := inner(value, 2)
			second := inner(value^1, 3)
			fields := map[string]any{
				"Account":         account,
				"TransactionType": tx.TypeBatch.String(),
				"Sequence":        uint32(1),
				"Fee":             "40",
				"SigningPubKey":   "",
				"Flags":           batch.BatchFlagAllOrNothing,
				"RawTransactions": []any{
					map[string]any{"RawTransaction": first},
					map[string]any{"RawTransaction": second},
				},
			}

			jsonBlob, err := json.Marshal(fields)
			require.NoError(t, err)
			parsed, err := tx.ParseJSON(jsonBlob)
			require.NoError(t, err)
			parsedBatch, ok := parsed.(*batch.Batch)
			require.True(t, ok)
			require.Len(t, parsedBatch.RawTransactions, 2)

			for i, want := range []uint64{value, value ^ 1} {
				parsedInner, ok := parsedBatch.RawTransactions[i].RawTransaction.InnerTx.(*XChainCommit)
				require.True(t, ok)
				require.Equal(t, want, parsedInner.XChainClaimID)

				matches, err := tx.CurrentFieldsMatchRaw(parsedInner)
				require.NoError(t, err)
				require.True(t, matches)

				wireFields := map[string]any{}
				for name, field := range []map[string]any{first, second}[i] {
					wireFields[name] = field
				}
				wireFields["XChainClaimID"] = fmt.Sprintf("%X", want)
				wantRaw, err := binarycodec.EncodeBytes(wireFields)
				require.NoError(t, err)
				require.Equal(t, wantRaw, parsedInner.GetRawBytes())
			}

			flattened, err := parsedBatch.Flatten()
			require.NoError(t, err)
			gotCanonical, err := binarycodec.EncodeBytes(flattened)
			require.NoError(t, err)

			wireFirst := map[string]any{}
			for name, field := range first {
				wireFirst[name] = field
			}
			wireFirst["XChainClaimID"] = fmt.Sprintf("%X", value)
			wireSecond := map[string]any{}
			for name, field := range second {
				wireSecond[name] = field
			}
			wireSecond["XChainClaimID"] = fmt.Sprintf("%X", value^1)
			wireFields := map[string]any{}
			for name, field := range fields {
				wireFields[name] = field
			}
			wireFields["RawTransactions"] = []any{
				map[string]any{"RawTransaction": wireFirst},
				map[string]any{"RawTransaction": wireSecond},
			}
			wantCanonical, err := binarycodec.EncodeBytes(wireFields)
			require.NoError(t, err)
			require.Equal(t, wantCanonical, gotCanonical)
		})
	}
}
