package batch

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	trustsettx "github.com/LeJamon/go-xrpl/internal/tx/trustset"
	"github.com/stretchr/testify/require"
)

func batchParserPaymentFields(sequence uint32, amount string, withExplicitEmptyFields bool) map[string]any {
	fields := map[string]any{
		"Account":         testOuter,
		"TransactionType": tx.TypePayment.String(),
		"Destination":     testSigner1,
		"Amount":          amount,
		"Sequence":        sequence,
		"Fee":             "0",
		"SigningPubKey":   "",
		"Flags":           tx.TfInnerBatchTxn,
	}
	if withExplicitEmptyFields {
		fields["Memos"] = []any{map[string]any{
			"Memo": map[string]any{"MemoData": ""},
		}}
		fields["SponsorSignature"] = map[string]any{"TxnSignature": ""}
		fields["TxnSignature"] = ""
		fields["Signers"] = []any{}
	}
	return fields
}

func batchParserFields(withExplicitEmptyFields bool) (map[string]any, []map[string]any) {
	first := batchParserPaymentFields(2, "1", withExplicitEmptyFields)
	second := batchParserPaymentFields(3, "2", false)
	fields := map[string]any{
		"Account":         testOuter,
		"TransactionType": tx.TypeBatch.String(),
		"Sequence":        uint32(1),
		"Fee":             "40",
		"SigningPubKey":   "",
		"Flags":           BatchFlagAllOrNothing,
		"RawTransactions": []any{
			map[string]any{"CreatedNode": first},
			map[string]any{"RawTransaction": second},
		},
	}
	return fields, []map[string]any{first, second}
}

func TestBatchParserBindsNestedRawBytesAndPreservesPresence(t *testing.T) {
	fields, innerFields := batchParserFields(true)
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)

	input := append([]byte(nil), blob...)
	parsed, err := tx.ParseFromBinary(input)
	require.NoError(t, err)
	parsedBatch, ok := parsed.(*Batch)
	require.True(t, ok)
	require.Equal(t, blob, parsedBatch.GetRawBytes())
	require.Equal(t, "CreatedNode", parsedBatch.RawTransactions[0].Wrapper)
	require.Equal(t, "RawTransaction", parsedBatch.RawTransactions[1].Wrapper)

	matches, err := tx.CurrentFieldsMatchRaw(parsedBatch)
	require.NoError(t, err)
	require.True(t, matches)
	for i, rawTransaction := range parsedBatch.RawTransactions {
		inner := rawTransaction.RawTransaction.InnerTx
		wantRaw, err := binarycodec.EncodeBytes(innerFields[i])
		require.NoError(t, err)
		require.Equal(t, wantRaw, inner.GetRawBytes())
		matches, err := tx.CurrentFieldsMatchRaw(inner)
		require.NoError(t, err)
		require.True(t, matches)
	}

	first := parsedBatch.RawTransactions[0].RawTransaction.InnerTx
	firstRaw := first.GetRawBytes()
	firstReturned := first.GetRawBytes()
	firstReturned[0] ^= 1
	require.Equal(t, firstRaw, first.GetRawBytes())
	require.True(t, first.GetCommon().HasField("Memos"))
	require.Len(t, first.GetCommon().Memos, 1)
	require.Empty(t, first.GetCommon().Memos[0].Memo.MemoData)
	require.NotNil(t, first.GetCommon().SponsorSignature)
	require.True(t, first.GetCommon().SponsorSignature.HasField("TxnSignature"))
	require.Empty(t, first.GetCommon().SponsorSignature.TxnSignature)
	require.True(t, first.GetCommon().HasField("TxnSignature"))
	require.True(t, first.GetCommon().HasField("Signers"))

	input[0] ^= 1
	require.Equal(t, blob, parsedBatch.GetRawBytes())
	returned := parsedBatch.GetRawBytes()
	returned[0] ^= 1
	require.Equal(t, blob, parsedBatch.GetRawBytes())

	parsedBatch.Fee = "41"
	matches, err = tx.CurrentFieldsMatchRaw(parsedBatch)
	require.NoError(t, err)
	require.False(t, matches)

	first.GetCommon().Memos[0].Memo.MemoData = "AA"
	matches, err = tx.CurrentFieldsMatchRaw(first)
	require.NoError(t, err)
	require.False(t, matches)

	jsonBlob, err := json.Marshal(fields)
	require.NoError(t, err)
	parsed, err = tx.ParseJSON(jsonBlob)
	require.NoError(t, err)
	parsedBatch, ok = parsed.(*Batch)
	require.True(t, ok)
	require.Empty(t, parsedBatch.GetRawBytes())
	require.Equal(t, "CreatedNode", parsedBatch.RawTransactions[0].Wrapper)
	require.Equal(t, "RawTransaction", parsedBatch.RawTransactions[1].Wrapper)
	for i, rawTransaction := range parsedBatch.RawTransactions {
		inner := rawTransaction.RawTransaction.InnerTx
		wantRaw, err := binarycodec.EncodeBytes(innerFields[i])
		require.NoError(t, err)
		require.Equal(t, wantRaw, inner.GetRawBytes())
		matches, err := tx.CurrentFieldsMatchRaw(inner)
		require.NoError(t, err)
		require.True(t, matches)
	}
	parsedBatch.RawTransactions[0].RawTransaction.InnerTx.GetCommon().TxnSignature = "AA"
	matches, err = tx.CurrentFieldsMatchRaw(parsedBatch.RawTransactions[0].RawTransaction.InnerTx)
	require.NoError(t, err)
	require.False(t, matches)
}

func TestBatchBinaryParserBindsInnerNoCurrencyIdentity(t *testing.T) {
	first := map[string]any{
		"Account":         testOuter,
		"TransactionType": tx.TypeTrustSet.String(),
		"LimitAmount": map[string]any{
			"value":    "1",
			"currency": "1",
			"issuer":   testSigner1,
		},
		"Sequence":      uint32(2),
		"Fee":           "0",
		"SigningPubKey": "",
		"Flags":         tx.TfInnerBatchTxn,
	}
	second := batchParserPaymentFields(3, "2", false)
	fields := map[string]any{
		"Account":         testOuter,
		"TransactionType": tx.TypeBatch.String(),
		"Sequence":        uint32(1),
		"Fee":             "40",
		"SigningPubKey":   "",
		"Flags":           BatchFlagAllOrNothing,
		"RawTransactions": []any{
			map[string]any{"CreatedNode": first},
			map[string]any{"RawTransaction": second},
		},
	}
	blob, err := binarycodec.EncodeBytes(fields)
	require.NoError(t, err)

	parsed, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	parsedBatch := parsed.(*Batch)
	inner := parsedBatch.RawTransactions[0].RawTransaction.InnerTx.(*trustsettx.TrustSet)
	require.Equal(t, "1", inner.LimitAmount.Currency)
	wantRaw, err := binarycodec.EncodeBytes(first)
	require.NoError(t, err)
	require.Equal(t, wantRaw, inner.GetRawBytes())
	matches, err := tx.CurrentFieldsMatchRaw(inner)
	require.NoError(t, err)
	require.True(t, matches)
}

func TestBatchParserRejectsInvalidInnerTemplates(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing required amount", mutate: func(inner map[string]any) { delete(inner, "Amount") }},
		{name: "disallowed field", mutate: func(inner map[string]any) {
			inner["EmailHash"] = "00000000000000000000000000000000"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, parser := range []string{"binary", "json"} {
				t.Run(parser, func(t *testing.T) {
					fields, innerFields := batchParserFields(false)
					test.mutate(innerFields[0])
					rawTransactions := fields["RawTransactions"].([]any)
					rawTransactions[0].(map[string]any)["CreatedNode"] = innerFields[0]

					var err error
					if parser == "binary" {
						blob, encodeErr := binarycodec.EncodeBytes(fields)
						require.NoError(t, encodeErr)
						_, err = tx.ParseFromBinary(blob)
					} else {
						blob, marshalErr := json.Marshal(fields)
						require.NoError(t, marshalErr)
						_, err = tx.ParseJSON(blob)
					}
					require.Error(t, err)
				})
			}
		})
	}
}

func TestBatchDirectJSONNormalizesXAddressAndHighPrecisionAmount(t *testing.T) {
	const (
		xAddress = "XVYRdEocC28DRx94ZFGP3qNJ1D5Ln7kXKTG5X57UCKzEwYx"
		classic  = "rLJ9FwQ3opJZBMsTjhqhHrbhRNALqAQJ5U"
		value    = "1234567890123456e-15"
	)
	first := map[string]any{
		"Account":         xAddress,
		"TransactionType": tx.TypePayment.String(),
		"Destination":     testSigner1,
		"Amount": map[string]any{
			"value":    value,
			"currency": "USD",
			"issuer":   testSigner2,
		},
		"Sequence":      uint32(2),
		"Fee":           "0",
		"SigningPubKey": "",
		"Flags":         tx.TfInnerBatchTxn,
	}
	second := batchParserPaymentFields(3, "2", false)
	fields := map[string]any{
		"Account":         testOuter,
		"TransactionType": tx.TypeBatch.String(),
		"Sequence":        uint32(1),
		"Fee":             "40",
		"SigningPubKey":   "",
		"Flags":           BatchFlagAllOrNothing,
		"RawTransactions": []any{
			map[string]any{"RawTransaction": first},
			map[string]any{"RawTransaction": second},
		},
	}
	jsonBlob, err := json.Marshal(fields)
	require.NoError(t, err)

	parsed, err := tx.ParseJSON(jsonBlob)
	require.NoError(t, err)
	parsedBatch := parsed.(*Batch)
	inner := parsedBatch.RawTransactions[0].RawTransaction.InnerTx.(*payment.Payment)
	require.Equal(t, classic, inner.Account)
	require.NotNil(t, inner.SourceTag)
	require.Equal(t, uint32(12345), *inner.SourceTag)
	wantAmount, err := state.NewIssuedAmountFromDecimalString(value, "USD", testSigner2)
	require.NoError(t, err)
	require.Equal(t, wantAmount, inner.Amount)
	innerFields, err := inner.Flatten()
	require.NoError(t, err)
	tx.PopulateRequiredWireFields(innerFields, inner.GetCommon())
	wantRaw, err := binarycodec.EncodeBytes(innerFields)
	require.NoError(t, err)
	require.Equal(t, wantRaw, inner.GetRawBytes())
	matches, err := tx.CurrentFieldsMatchRaw(inner)
	require.NoError(t, err)
	require.True(t, matches)
}

func TestBatchInnerJSONPreservesTypedErrorPrecedence(t *testing.T) {
	fields := batchParserPaymentFields(2, "1", false)
	fields["Destination"] = 7
	delete(fields, "Amount")
	data, err := json.Marshal(fields)
	require.NoError(t, err)
	var inner RawTransactionData
	err = json.Unmarshal(data, &inner)
	require.ErrorContains(t, err, "parse inner transaction JSON:")
	require.ErrorContains(t, err, "cannot unmarshal number")
	require.NotContains(t, err.Error(), "required but missing")
}
