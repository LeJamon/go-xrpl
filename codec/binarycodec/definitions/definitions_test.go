package definitions

import (
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

func TestSponsorDefinitions(t *testing.T) {
	loadDefinitions()
	require.Equal(t, int32(90), definitions.transactionTypes["SponsorshipTransfer"])
	require.Equal(t, int32(91), definitions.transactionTypes["SponsorshipSet"])
	require.Equal(t, int32(144), definitions.ledgerEntryTypes["Sponsorship"])

	tests := []struct {
		name      string
		typeName  string
		typeCode  int32
		fieldCode int32
		variable  bool
		signing   bool
	}{
		{"SponsoredOwnerCount", "UInt32", 2, 70, false, true},
		{"SponsoringOwnerCount", "UInt32", 2, 71, false, true},
		{"SponsoringAccountCount", "UInt32", 2, 72, false, true},
		{"RemainingOwnerCount", "UInt32", 2, 73, false, true},
		{"SponsorFlags", "UInt32", 2, 74, false, true},
		{"SponseeNode", "UInt64", 3, 33, false, true},
		{"ObjectID", "Hash256", 5, 41, false, true},
		{"FeeAmount", "Amount", 6, 32, false, true},
		{"MaxFee", "Amount", 6, 33, false, true},
		{"Sponsor", "AccountID", 8, 27, true, true},
		{"HighSponsor", "AccountID", 8, 28, true, true},
		{"LowSponsor", "AccountID", 8, 29, true, true},
		{"CounterpartySponsor", "AccountID", 8, 30, true, true},
		{"Sponsee", "AccountID", 8, 31, true, true},
		{"SponsorSignature", "STObject", 14, 38, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			field, ok := definitions.fields[test.name]
			require.True(t, ok)
			require.Equal(t, &FieldInfo{
				Nth:            test.fieldCode,
				IsVLEncoded:    test.variable,
				IsSerialized:   true,
				IsSigningField: test.signing,
				Type:           test.typeName,
			}, field.FieldInfo)
			require.Equal(t, &FieldHeader{TypeCode: test.typeCode, FieldCode: test.fieldCode}, field.FieldHeader)
			require.Equal(t, test.name, definitions.fieldIDNameMap[*field.FieldHeader])
		})
	}
}

func TestImmutableFlagsDefinition(t *testing.T) {
	loadDefinitions()
	field, ok := definitions.fields["ImmutableFlags"]
	require.True(t, ok)
	require.Equal(t, &FieldInfo{
		Nth:            53,
		IsVLEncoded:    false,
		IsSerialized:   true,
		IsSigningField: true,
		Type:           "UInt32",
	}, field.FieldInfo)
	require.Equal(t, &FieldHeader{TypeCode: 2, FieldCode: 53}, field.FieldHeader)
	require.Equal(t, int32(131125), field.Ordinal)
	require.Equal(t, "ImmutableFlags", definitions.fieldIDNameMap[*field.FieldHeader])
	require.NotContains(t, definitions.fields, "MutableFlags")
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
