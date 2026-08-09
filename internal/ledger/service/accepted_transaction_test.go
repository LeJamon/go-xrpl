package service

import (
	"bytes"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/stretchr/testify/require"
)

func TestAcceptedTransactionOwnsAndProtectsDecodedState(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	transaction := map[string]any{
		"Account":         account,
		"Amount":          "1",
		"Destination":     "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	}
	metadata := map[string]any{
		"AffectedNodes": []any{map[string]any{"ModifiedNode": map[string]any{
			"LedgerEntryType": "AccountRoot",
			"FinalFields":     map[string]any{"Account": account},
		}}},
		"TransactionIndex":  uint32(7),
		"TransactionResult": "tesSUCCESS",
	}
	raw := acceptedLeaf(t, transaction, metadata)
	wantRaw := append([]byte(nil), raw...)
	accepted := ParseAcceptedTransaction(raw)
	require.NoError(t, accepted.ParseError())
	require.Equal(t, ter.TesSUCCESS, accepted.Result())
	require.Equal(t, uint32(7), mustAcceptedIndex(t, accepted))
	require.Equal(t, []string{account}, accepted.AffectedAccounts())
	projection, err := accepted.Projection()
	require.NoError(t, err)
	require.Equal(t, ter.TesSUCCESS, projection.Result)
	require.Equal(t, uint32(7), projection.TransactionIndex)

	for i := range raw {
		raw[i] = 0
	}
	require.Equal(t, wantRaw, accepted.Raw())

	returnedRaw := accepted.Raw()
	returnedRaw[0] ^= 0xff
	require.Equal(t, wantRaw, accepted.Raw())
	txBlob := accepted.TransactionBlob()
	txBlob[0] ^= 0xff
	require.NotEqual(t, txBlob, accepted.TransactionBlob())
	metaBlob := accepted.MetadataBlob()
	metaBlob[0] ^= 0xff
	require.NotEqual(t, metaBlob, accepted.MetadataBlob())

	returnedTx := accepted.Transaction()
	returnedTx["TransactionType"] = "AccountSet"
	require.Equal(t, "Payment", accepted.Transaction()["TransactionType"])

	returnedMeta := accepted.Metadata()
	nodes := returnedMeta["AffectedNodes"].([]any)
	nodes[0].(map[string]any)["ModifiedNode"].(map[string]any)["FinalFields"].(map[string]any)["Account"] = "changed"
	storedNodes := accepted.Metadata()["AffectedNodes"].([]any)
	require.Equal(t, account, storedNodes[0].(map[string]any)["ModifiedNode"].(map[string]any)["FinalFields"].(map[string]any)["Account"])

	accounts := accepted.AffectedAccounts()
	accounts[0] = "changed"
	require.Equal(t, []string{account}, accepted.AffectedAccounts())

	projection.Transaction["TransactionType"] = "AccountSet"
	projectionNodes := projection.Metadata["AffectedNodes"].([]any)
	projectionNodes[0].(map[string]any)["ModifiedNode"].(map[string]any)["FinalFields"].(map[string]any)["Account"] = "changed"
	projection.AffectedAccounts[0] = "changed"
	freshProjection, err := accepted.Projection()
	require.NoError(t, err)
	require.Equal(t, "Payment", freshProjection.Transaction["TransactionType"])
	freshNodes := freshProjection.Metadata["AffectedNodes"].([]any)
	require.Equal(t, account, freshNodes[0].(map[string]any)["ModifiedNode"].(map[string]any)["FinalFields"].(map[string]any)["Account"])
	require.Equal(t, []string{account}, freshProjection.AffectedAccounts)
}

func TestAcceptedTransactionOwnsVector256Fields(t *testing.T) {
	const offerID = "73734B611DDA23D3F5F62E20A173B78AB8406AC5015094DA53F53D39B9EDB06C"
	transaction := map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":             "10",
		"NFTokenOffers":   []string{offerID},
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "NFTokenCancelOffer",
	}
	metadata := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}
	accepted := ParseAcceptedTransaction(acceptedLeaf(t, transaction, metadata))
	require.NoError(t, accepted.ParseError())

	returned := accepted.Transaction()["NFTokenOffers"].([]string)
	returned[0] = "changed"
	projection, err := accepted.Projection()
	require.NoError(t, err)
	projection.Transaction["NFTokenOffers"].([]string)[0] = "changed"

	require.Equal(t, []string{offerID}, accepted.Transaction()["NFTokenOffers"])
	fresh, err := accepted.Projection()
	require.NoError(t, err)
	require.Equal(t, []string{offerID}, fresh.Transaction["NFTokenOffers"])
}

func TestAcceptedTransactionParseErrorDoesNotExposeRetainedJoin(t *testing.T) {
	accepted := &AcceptedTransaction{parseErr: errors.Join(errors.New("first"), errors.New("second"))}
	returned := accepted.ParseError()
	require.EqualError(t, returned, "first\nsecond")

	multi, ok := returned.(interface{ Unwrap() []error })
	if ok {
		unwrapped := multi.Unwrap()
		unwrapped[0] = nil
	}

	require.EqualError(t, accepted.ParseError(), "first\nsecond")
}

func TestAcceptedTransactionMalformedStateNeverReportsSuccess(t *testing.T) {
	var zero AcceptedTransaction
	require.Error(t, zero.ParseError())
	require.NotEqual(t, ter.TesSUCCESS, zero.Result())
	require.NotEqual(t, ter.TesSUCCESS, (*AcceptedTransaction)(nil).Result())

	metadata := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(3),
		"TransactionResult": "tesSUCCESS",
	}
	metaBlob, err := binarycodec.EncodeBytes(metadata)
	require.NoError(t, err)
	txField, err := txcore.EncodeWithVL([]byte{0x12, 0x00})
	require.NoError(t, err)
	metaField, err := txcore.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	accepted := ParseAcceptedTransaction(append(txField, metaField...))
	require.Error(t, accepted.ParseError())
	require.Equal(t, uint32(3), mustAcceptedIndex(t, accepted))
	require.NotEqual(t, ter.TesSUCCESS, accepted.Result())
}

func TestAcceptedTransactionValidatesRequiredFieldsWithoutLosingMetadataIndex(t *testing.T) {
	transaction := validAcceptedPayment()
	delete(transaction, "Destination")
	accepted := ParseAcceptedTransaction(acceptedLeaf(t, transaction, map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(11),
		"TransactionResult": "tecUNFUNDED_PAYMENT",
	}))
	require.ErrorContains(t, accepted.ParseError(), "Destination")
	require.Equal(t, uint32(11), mustAcceptedIndex(t, accepted))
	require.NotEqual(t, ter.TesSUCCESS, accepted.Result())
}

func TestAcceptedTransactionRejectsMalformedInnerObjectTemplate(t *testing.T) {
	transaction := map[string]any{
		"Account":  "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Fee":      "10",
		"Sequence": uint32(1),
		"SignerEntries": []any{map[string]any{
			"SignerEntry": map[string]any{
				"Account":      "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
				"SignerWeight": uint16(1),
			},
		}},
		"SignerQuorum":    uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "SignerListSet",
	}
	metadata := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(12),
		"TransactionResult": "tesSUCCESS",
	}

	accepted := ParseAcceptedTransaction(acceptedLeafWithoutSignerWeight(t, transaction, metadata))
	require.ErrorContains(t, accepted.ParseError(), "Field 'SignerWeight' is required but missing.")
	require.Equal(t, uint32(12), mustAcceptedIndex(t, accepted))
	require.Equal(t, ter.TemMALFORMED, accepted.Result())
	require.Nil(t, accepted.Transaction())
}

func TestAcceptedTransactionRejectsObjectTerminatorInAffectedNodes(t *testing.T) {
	txBlob, err := binarycodec.EncodeBytes(validAcceptedPayment())
	require.NoError(t, err)
	metaBlob, err := binarycodec.EncodeBytes(map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(13),
		"TransactionResult": "tesSUCCESS",
	})
	require.NoError(t, err)
	terminator := bytes.Index(metaBlob, []byte{0xF8, 0xF1})
	require.NotEqual(t, -1, terminator)
	metaBlob[terminator+1] = 0xE1

	txField, err := txcore.EncodeWithVL(txBlob)
	require.NoError(t, err)
	metaField, err := txcore.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	accepted := ParseAcceptedTransaction(bytes.Join([][]byte{txField, metaField}, nil))

	require.ErrorContains(t, accepted.ParseError(), "Illegal terminator in array")
	require.Equal(t, ter.TemMALFORMED, accepted.Result())
	_, hasIndex := accepted.TransactionIndex()
	require.False(t, hasIndex)
	require.Nil(t, accepted.Transaction())
	require.Nil(t, accepted.Metadata())
}

func TestValidateAcceptedMetadataRejectsWrongRequiredFieldTypes(t *testing.T) {
	valid := map[string]any{
		"AffectedNodes":     []any{},
		"TransactionIndex":  uint32(0),
		"TransactionResult": "tesSUCCESS",
	}
	tests := []struct {
		name  string
		field string
		value any
	}{
		{name: "transaction index", field: "TransactionIndex", value: "0"},
		{name: "affected nodes", field: "AffectedNodes", value: map[string]any{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := make(map[string]any, len(valid))
			for name, value := range valid {
				metadata[name] = value
			}
			metadata[test.field] = test.value
			require.ErrorContains(t, validateAcceptedMetadata(metadata), test.field)
		})
	}
}

func TestAcceptedTransactionProjectionFailsClosed(t *testing.T) {
	malformed := ParseAcceptedTransaction([]byte("malformed"))
	tests := []struct {
		name     string
		accepted *AcceptedTransaction
	}{
		{name: "nil"},
		{name: "zero", accepted: &AcceptedTransaction{}},
		{name: "malformed", accepted: malformed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parseErr := test.accepted.ParseError()
			projection, err := test.accepted.Projection()
			require.EqualError(t, err, parseErr.Error())
			require.NotEqual(t, ter.TesSUCCESS, projection.Result)
			require.Nil(t, projection.Transaction)
			require.Nil(t, projection.Metadata)
			require.Nil(t, projection.AffectedAccounts)
			require.Nil(t, test.accepted.Transaction())
			require.Nil(t, test.accepted.Metadata())
		})
	}
}

func acceptedLeaf(t *testing.T, transaction, metadata map[string]any) []byte {
	t.Helper()
	txBlob, err := binarycodec.EncodeBytes(transaction)
	require.NoError(t, err)
	metaBlob, err := binarycodec.EncodeBytes(metadata)
	require.NoError(t, err)
	txField, err := txcore.EncodeWithVL(txBlob)
	require.NoError(t, err)
	metaField, err := txcore.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	return bytes.Join([][]byte{txField, metaField}, nil)
}

func acceptedLeafWithoutSignerWeight(t *testing.T, transaction, metadata map[string]any) []byte {
	t.Helper()
	txBlob, err := binarycodec.EncodeBytes(transaction)
	require.NoError(t, err)
	signerWeight, err := binarycodec.EncodeBytes(map[string]any{"SignerWeight": uint16(1)})
	require.NoError(t, err)
	offset := bytes.Index(txBlob, signerWeight)
	require.NotEqual(t, -1, offset)
	txBlob = append(txBlob[:offset:offset], txBlob[offset+len(signerWeight):]...)

	metaBlob, err := binarycodec.EncodeBytes(metadata)
	require.NoError(t, err)
	txField, err := txcore.EncodeWithVL(txBlob)
	require.NoError(t, err)
	metaField, err := txcore.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	return bytes.Join([][]byte{txField, metaField}, nil)
}

func validAcceptedPayment() map[string]any {
	return map[string]any{
		"Account":         "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Amount":          "1",
		"Destination":     "r4bbzCamAis69rNoRdSaMSmPb1kDUHXcAL",
		"Fee":             "10",
		"Sequence":        uint32(1),
		"SigningPubKey":   "",
		"TransactionType": "Payment",
	}
}

func mustAcceptedIndex(t *testing.T, accepted *AcceptedTransaction) uint32 {
	t.Helper()
	index, ok := accepted.TransactionIndex()
	require.True(t, ok)
	return index
}
