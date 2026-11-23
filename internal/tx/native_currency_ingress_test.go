package tx_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/stretchr/testify/require"
)

func TestNativeCurrencyInTransactionJSON(t *testing.T) {
	all.RegisterAll()
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, currency := range []string{"", "XRP", "0", strings.Repeat("0", 40)} {
		t.Run(fmt.Sprintf("%q", currency), func(t *testing.T) {
			deposit := fmt.Sprintf(`{"TransactionType":"AMMDeposit","Account":%q,"Flags":524288,"Amount":"100","Asset":{"currency":%q},"Asset2":{"currency":"USD","issuer":%q}}`, account, currency, account)
			transaction, err := tx.ParseJSON([]byte(deposit))
			require.NoError(t, err)
			require.NoError(t, transaction.Validate())
			fields, err := transaction.Flatten()
			require.NoError(t, err)
			require.Equal(t, map[string]any{"currency": "XRP"}, fields["Asset"])

			payment := fmt.Sprintf(`{"TransactionType":"Payment","Paths":[[{"currency":%q},{"account":%q}]]}`, currency, account)
			transaction, err = tx.ParseJSON([]byte(payment))
			require.NoError(t, err)
			fields, err = transaction.Flatten()
			require.NoError(t, err)
			require.Equal(t, []any{[]any{map[string]any{"currency": "XRP"}, map[string]any{"account": account}}}, fields["Paths"])
		})
	}
}

func TestNativeCurrencyIssuerAndMPTJSON(t *testing.T) {
	for _, issuer := range []string{"", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"} {
		var asset tx.Asset
		require.Error(t, json.Unmarshal([]byte(fmt.Sprintf(`{"currency":"0","issuer":%q}`, issuer)), &asset))
	}
	original := tx.Asset{MPTIssuanceID: strings.Repeat("A", 48)}
	data, err := json.Marshal(original)
	require.NoError(t, err)
	var decoded tx.Asset
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, original, decoded)
}
