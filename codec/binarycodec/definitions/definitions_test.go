package definitions

import (
	"maps"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadDefinitions(t *testing.T) {
	defs := Get()
	require.Equal(t, int32(-1), defs.types["Done"])
	require.Equal(t, int32(4), defs.types["Hash128"])
	require.Equal(t, int32(97), defs.ledgerEntryTypes["AccountRoot"])
	require.Equal(t, int32(-399), defs.transactionResults["telLOCAL_ERROR"])
	require.Equal(t, int32(1), defs.transactionTypes["EscrowCreate"])
	require.Equal(t, &FieldInfo{Nth: 0, IsVLEncoded: false, IsSerialized: false, IsSigningField: false, Type: "Unknown"}, defs.fields["Generic"].FieldInfo)
	require.Equal(t, &FieldInfo{Nth: 28, IsVLEncoded: false, IsSerialized: true, IsSigningField: true, Type: "Hash256"}, defs.fields["NFTokenBuyOffer"].FieldInfo)
	require.Equal(t, &FieldInfo{Nth: 16, IsVLEncoded: false, IsSerialized: true, IsSigningField: true, Type: "UInt8"}, defs.fields["TickSize"].FieldInfo)
	require.Equal(t, &FieldHeader{TypeCode: 2, FieldCode: 4}, defs.fields["Sequence"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 18, FieldCode: 1}, defs.fields["Paths"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 2, FieldCode: 33}, defs.fields["SetFlag"].FieldHeader)
	require.Equal(t, &FieldHeader{TypeCode: 16, FieldCode: 16}, defs.fields["TickSize"].FieldHeader)
	require.Equal(t, "UInt32", defs.fields["TransferRate"].Type)
	require.Equal(t, "Sequence", defs.fieldIDNameMap[FieldHeader{TypeCode: 2, FieldCode: 4}])
	require.Equal(t, "OfferSequence", defs.fieldIDNameMap[FieldHeader{TypeCode: 2, FieldCode: 25}])
	require.Equal(t, "NFTokenSellOffer", defs.fieldIDNameMap[FieldHeader{TypeCode: 5, FieldCode: 29}])
	require.Equal(t, int32(131076), defs.fields["Sequence"].Ordinal)
	require.Equal(t, int32(131097), defs.fields["OfferSequence"].Ordinal)
	require.Equal(t, int32(65537), defs.granularPermissions["TrustlineAuthorize"])
	require.Equal(t, int32(1), defs.delegatablePermissions["Payment"])
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
		Get()
	}
}

func TestGet(t *testing.T) {
	require.Same(t, Get(), Get())
}

func TestValidateEmbeddedLedgerEntryTypesFailsClosed(t *testing.T) {
	valid := Get().LedgerEntryTypes()
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
