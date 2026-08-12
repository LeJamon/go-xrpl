package tx

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type granularTemplateTx struct {
	BaseTx
	fields map[string]any
}

func newGranularTemplateTx(txType Type, flags uint32, fields ...string) *granularTemplateTx {
	txn := &granularTemplateTx{
		BaseTx: *NewBaseTx(txType, "rAccount"),
		fields: map[string]any{
			"Account":         "rAccount",
			"TransactionType": txType.String(),
		},
	}
	txn.SetFlags(flags)
	for _, field := range fields {
		txn.fields[field] = true
	}
	return txn
}

func (t *granularTemplateTx) Flatten() (map[string]any, error) {
	fields := make(map[string]any, len(t.fields)+1)
	for name, value := range t.fields {
		fields[name] = value
	}
	fields["Flags"] = t.GetFlags()
	return fields, nil
}

func TestGranularPermissionTemplates(t *testing.T) {
	tests := []struct {
		name   string
		value  uint32
		txType Type
		flags  uint32
		fields []string
	}{
		{"TrustlineAuthorize", GranularTrustlineAuthorize, TypeTrustSet, TfUniversal | 0x00010000, []string{"LimitAmount"}},
		{"TrustlineFreeze", GranularTrustlineFreeze, TypeTrustSet, TfUniversal | 0x00100000, []string{"LimitAmount"}},
		{"TrustlineUnfreeze", GranularTrustlineUnfreeze, TypeTrustSet, TfUniversal | 0x00200000, []string{"LimitAmount"}},
		{"AccountDomainSet", GranularAccountDomainSet, TypeAccountSet, TfUniversal, []string{"Domain"}},
		{"AccountEmailHashSet", GranularAccountEmailHashSet, TypeAccountSet, TfUniversal, []string{"EmailHash"}},
		{"AccountMessageKeySet", GranularAccountMessageKeySet, TypeAccountSet, TfUniversal, []string{"MessageKey"}},
		{"AccountTransferRateSet", GranularAccountTransferRateSet, TypeAccountSet, TfUniversal, []string{"TransferRate"}},
		{"AccountTickSizeSet", GranularAccountTickSizeSet, TypeAccountSet, TfUniversal, []string{"TickSize"}},
		{"PaymentMint", GranularPaymentMint, TypePayment, TfUniversal, []string{"Destination", "Amount", "SendMax", "InvoiceID", "DestinationTag", "CredentialIDs"}},
		{"PaymentBurn", GranularPaymentBurn, TypePayment, TfUniversal, []string{"Destination", "Amount", "SendMax", "InvoiceID", "DestinationTag", "CredentialIDs"}},
		{"MPTokenIssuanceLock", GranularMPTokenIssuanceLock, TypeMPTokenIssuanceSet, TfUniversal | 0x00000001, []string{"MPTokenIssuanceID", "Holder"}},
		{"MPTokenIssuanceUnlock", GranularMPTokenIssuanceUnlock, TypeMPTokenIssuanceSet, TfUniversal | 0x00000002, []string{"MPTokenIssuanceID", "Holder"}},
	}
	require.Len(t, granularPermissions, len(tests))

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			definition := granularPermissions[test.value]
			require.Equal(t, test.txType, definition.txType)
			require.Equal(t, test.flags, definition.allowedFlags)
			require.Equal(t, test.fields, definition.allowedFields)
			for _, field := range definition.allowedFields {
				require.Contains(t, txTemplateStyles[test.txType], field)
			}

			held := GranularPermissionsFor(test.txType, []uint32{test.value})
			require.Equal(t, []uint32{test.value}, held)
			require.True(t, CheckGranularPermissionTemplate(
				newGranularTemplateTx(test.txType, test.flags, test.fields...), held))

			require.False(t, CheckGranularPermissionTemplate(
				newGranularTemplateTx(test.txType, test.flags|0x00000004, test.fields...), held))
			require.False(t, CheckGranularPermissionTemplate(
				newGranularTemplateTx(test.txType, test.flags, append(test.fields, "ForeignField")...), held))
		})
	}
}

func TestGranularPermissionTemplateUnionsSameTransactionType(t *testing.T) {
	held := GranularPermissionsFor(TypeAccountSet, []uint32{
		GranularAccountDomainSet,
		GranularPaymentMint,
		GranularAccountEmailHashSet,
	})
	require.Equal(t, []uint32{GranularAccountDomainSet, GranularAccountEmailHashSet}, held)
	require.True(t, CheckGranularPermissionTemplate(
		newGranularTemplateTx(TypeAccountSet, TfUniversal, "Domain", "EmailHash"), held))
	require.False(t, CheckGranularPermissionTemplate(
		newGranularTemplateTx(TypePayment, TfUniversal, "Destination", "Amount"), held))
}
