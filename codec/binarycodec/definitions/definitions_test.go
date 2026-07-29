package definitions

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDefinitions(t *testing.T) {
	loadDefinitions()
	require.Equal(t, int32(-1), definitions.types["Done"])
	require.Equal(t, int32(4), definitions.types["Hash128"])
	require.Equal(t, int32(97), definitions.ledgerEntryTypes["AccountRoot"])
	require.Equal(t, int32(-399), definitions.transactionResults["telLOCAL_ERROR"])
	require.Equal(t, int32(1), definitions.transactionTypes["EscrowCreate"])
	require.Equal(t, &FieldInfo{Nth: 0, IsVLEncoded: false, IsSerialized: false, IsSigningField: false, Type: "Unknown"}, definitions.fields["Generic"].FieldInfo)
	require.Equal(t, &FieldInfo{Nth: 28, IsVLEncoded: false, IsSerialized: true, IsSigningField: true, Type: "Hash256"}, definitions.fields["NFTokenBuyOffer"].FieldInfo)
	require.Equal(t, &FieldInfo{Nth: 16, IsVLEncoded: false, IsSerialized: true, IsSigningField: true, Type: "UInt8"}, definitions.fields["TickSize"].FieldInfo)
	require.Equal(t, &FieldHeader{TypeCode: 2, FieldCode: 4}, definitions.fields["Sequence"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 18, FieldCode: 1}, definitions.fields["Paths"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 2, FieldCode: 33}, definitions.fields["SetFlag"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 16, FieldCode: 16}, definitions.fields["TickSize"].FieldHeader)
	require.Equal(t, "UInt32", definitions.fields["TransferRate"].Type)
	require.Equal(t, "Sequence", definitions.fieldIDNameMap[FieldHeader{TypeCode: 2, FieldCode: 4}])
	require.Equal(t, "OfferSequence", definitions.fieldIDNameMap[FieldHeader{TypeCode: 2, FieldCode: 25}])
	require.Equal(t, "NFTokenSellOffer", definitions.fieldIDNameMap[FieldHeader{TypeCode: 5, FieldCode: 29}])
	require.Equal(t, int32(131076), definitions.fields["Sequence"].Ordinal)
	require.Equal(t, int32(131097), definitions.fields["OfferSequence"].Ordinal)
	require.Equal(t, int32(65537), definitions.granularPermissions["TrustlineAuthorize"])
	require.Equal(t, int32(1), definitions.delegatablePermissions["Payment"])
}

// Helper functions to create and test ordinals.
// func CreateOrdinal(fh FieldHeader) int32 {
// 	return fh.TypeCode<<16 | fh.FieldCode
// }

// func TestCreateOrdinal(t *testing.T) {
// 	tt := []struct {
// 		description string
// 		input       FieldHeader
// 	}{
// 		{
// 			description: "test ordinal creation",
// 			input:       FieldHeader{TypeCode: 2, FieldCode: 25},
// 		},
// 	}

// 	for _, tc := range tt {
// 		t.Run(tc.description, func(t *testing.T) {
// 			fmt.Println("Ordinal:", CreateOrdinal(tc.input))
// 		})
// 	}
// }

// nolint
func BenchmarkLoadDefinitions(b *testing.B) {
	for i := 0; i < b.N; i++ {
		loadDefinitions()
	}
}

func TestGet(t *testing.T) {
	loadDefinitions()
	require.Equal(t, definitions, Get())
}

func TestValidateEmbeddedLedgerEntryTypesFailsClosed(t *testing.T) {
	valid := definitions.LedgerEntryTypes()
	require.NoError(t, validateEmbeddedLedgerEntryTypes(valid))

	missing := maps.Clone(valid)
	delete(missing, "AccountRoot")
	require.Error(t, validateEmbeddedLedgerEntryTypes(missing))

	wrong := maps.Clone(valid)
	wrong["AccountRoot"]++
	require.Error(t, validateEmbeddedLedgerEntryTypes(wrong))

	extra := maps.Clone(valid)
	extra["UnknownEntry"] = 1234
	require.Error(t, validateEmbeddedLedgerEntryTypes(extra))
}
