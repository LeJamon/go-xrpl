package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

const rpcMPTID = "00000004AE123A8556F3CF91154711376AFB0F894F832B3D"

func TestBookOffersMPTAssetParsing(t *testing.T) {
	inner := map[string]json.RawMessage{
		"mpt_issuance_id": json.RawMessage(`"00000004ae123a8556f3cf91154711376afb0f894f832b3d"`),
	}
	require.Nil(t, validateTakerBookJSON(inner, "taker_pays"))

	amount, issuer, rpcErr := parseTakerBookAsset(inner, true)
	require.Nil(t, rpcErr)
	require.True(t, amount.IsMPT())
	require.Equal(t, rpcMPTID, amount.MPTIssuanceID)
	id, err := mptutil.DecodeID(rpcMPTID)
	require.NoError(t, err)
	require.Equal(t, mptutil.Issuer(id), issuer)

	other := amount
	require.True(t, sameBookAsset(amount, issuer, other, issuer))
	other.MPTIssuanceID = "00000005AE123A8556F3CF91154711376AFB0F894F832B3D"
	require.False(t, sameBookAsset(amount, issuer, other, issuer))
}

func TestBookOffersMPTValidation(t *testing.T) {
	for _, inner := range []map[string]json.RawMessage{
		{
			"currency":        json.RawMessage(`"USD"`),
			"mpt_issuance_id": json.RawMessage(`"` + rpcMPTID + `"`),
		},
		{
			"issuer":          json.RawMessage(`"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"`),
			"mpt_issuance_id": json.RawMessage(`"` + rpcMPTID + `"`),
		},
	} {
		rpcErr := validateTakerBookJSON(inner, "taker_pays")
		require.NotNil(t, rpcErr)
		require.Equal(t, "Invalid field 'taker_pays'.", rpcErr.Message)
	}

	_, _, rpcErr := parseTakerBookAsset(map[string]json.RawMessage{
		"mpt_issuance_id": json.RawMessage(`"bad"`),
	}, true)
	require.NotNil(t, rpcErr)
	require.Equal(t, "srcCurMalformed", rpcErr.ErrorString)
}

func TestPathFindMPTSourceAndFormatting(t *testing.T) {
	probe := map[string]json.RawMessage{
		"source_currencies": json.RawMessage(`[{"mpt_issuance_id":"` + rpcMPTID + `"}]`),
	}
	issues, rpcErr := parseSourceCurrencies(probe, [20]byte{1}, nil)
	require.Nil(t, rpcErr)
	require.Len(t, issues, 1)
	id, err := mptutil.DecodeID(rpcMPTID)
	require.NoError(t, err)
	require.True(t, issues[0].Equal(payment.NewMPTIssue(id)))

	formatted := formatAmountJSON(state.NewMPTAmountWithIssuanceID(25, "", rpcMPTID))
	require.Equal(t, map[string]string{
		"mpt_issuance_id": rpcMPTID,
		"value":           "25",
	}, formatted)
}

func TestPathFindMPTSourceRejectsConflictingMembers(t *testing.T) {
	for _, sourceCurrencies := range []string{
		`[{"mpt_issuance_id":"` + rpcMPTID + `","currency":null}]`,
		`[{"mpt_issuance_id":"` + rpcMPTID + `","issuer":null}]`,
	} {
		_, rpcErr := parseSourceCurrencies(map[string]json.RawMessage{
			"source_currencies": json.RawMessage(sourceCurrencies),
		}, [20]byte{1}, nil)
		require.NotNil(t, rpcErr)
		require.Equal(t, "srcCurMalformed", rpcErr.ErrorString)
	}
}

func TestBookChangesMPTAmountParsing(t *testing.T) {
	amount := parseAmount(map[string]any{
		"mpt_issuance_id": "00000004ae123a8556f3cf91154711376afb0f894f832b3d",
		"value":           "50",
	})
	require.NotNil(t, amount)
	require.Equal(t, rpcMPTID, amount.mptIssuanceID)
	require.Equal(t, rpcMPTID, formatCurrencyKey(amount))
}

func TestComputeBookChangesEmitsMPTAsset(t *testing.T) {
	blob, err := json.Marshal(StoredTransaction{
		TxJSON: map[string]any{"TransactionType": "Payment"},
		Meta: map[string]any{
			"AffectedNodes": []any{
				map[string]any{
					"ModifiedNode": map[string]any{
						"LedgerEntryType": "Offer",
						"PreviousFields": map[string]any{
							"TakerGets": map[string]any{"mpt_issuance_id": rpcMPTID, "value": "100"},
							"TakerPays": "10000000",
						},
						"FinalFields": map[string]any{
							"TakerGets": map[string]any{"mpt_issuance_id": rpcMPTID, "value": "40"},
							"TakerPays": "4000000",
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	result := ComputeBookChanges(mptBookChangesLedger{blob: blob})
	changes, ok := result["changes"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, changes, 1)
	require.Equal(t, "XRP_drops", changes[0]["currency_a"])
	require.Equal(t, rpcMPTID, changes[0]["mpt_issuance_id_b"])
	require.NotContains(t, changes[0], "currency_b")
}

func TestComputeBookChangesPreservesLargeMPTVolumeAndRate(t *testing.T) {
	const aboveFloat64IntegerPrecision = "9007199254740993"

	change := computeMPTBookChange(t, aboveFloat64IntegerPrecision, "1")
	require.Equal(t, aboveFloat64IntegerPrecision, change["volume_a"])
	require.Equal(t, "1", change["volume_b"])
	for _, field := range []string{"high", "low", "open", "close"} {
		require.Equal(t, aboveFloat64IntegerPrecision, change[field], field)
	}
}

func TestComputeBookChangesPreservesMaxInt64MPTVolume(t *testing.T) {
	const maxMPTAmount = "9223372036854775807"

	change := computeMPTBookChange(t, maxMPTAmount, maxMPTAmount)
	require.Equal(t, maxMPTAmount, change["volume_a"])
	require.Equal(t, maxMPTAmount, change["volume_b"])
	for _, field := range []string{"high", "low", "open", "close"} {
		require.Equal(t, "1", change[field], field)
	}
}

func computeMPTBookChange(t *testing.T, takerGets, takerPays string) map[string]any {
	t.Helper()
	const otherMPTID = "00000005AE123A8556F3CF91154711376AFB0F894F832B3D"

	blob, err := json.Marshal(StoredTransaction{
		TxJSON: map[string]any{"TransactionType": "Payment"},
		Meta: map[string]any{
			"AffectedNodes": []any{
				map[string]any{
					"ModifiedNode": map[string]any{
						"LedgerEntryType": "Offer",
						"PreviousFields": map[string]any{
							"TakerGets": map[string]any{"mpt_issuance_id": rpcMPTID, "value": takerGets},
							"TakerPays": map[string]any{"mpt_issuance_id": otherMPTID, "value": takerPays},
						},
						"FinalFields": map[string]any{
							"TakerGets": map[string]any{"mpt_issuance_id": rpcMPTID, "value": "0"},
							"TakerPays": map[string]any{"mpt_issuance_id": otherMPTID, "value": "0"},
						},
					},
				},
			},
		},
	})
	require.NoError(t, err)

	result := ComputeBookChanges(mptBookChangesLedger{blob: blob})
	changes, ok := result["changes"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, changes, 1)
	require.Equal(t, rpcMPTID, changes[0]["mpt_issuance_id_a"])
	require.Equal(t, otherMPTID, changes[0]["mpt_issuance_id_b"])
	return changes[0]
}

type mptBookChangesLedger struct{ blob []byte }

func (l mptBookChangesLedger) ForEachTransaction(fn func([32]byte, []byte) bool) error {
	fn([32]byte{1}, l.blob)
	return nil
}

func (mptBookChangesLedger) Sequence() uint32  { return 1 }
func (mptBookChangesLedger) Hash() [32]byte    { return [32]byte{2} }
func (mptBookChangesLedger) CloseTime() int64  { return 3 }
func (mptBookChangesLedger) IsValidated() bool { return true }
