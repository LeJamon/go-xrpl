package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func ammHolding(t *testing.T, e *amm.AMMTestEnv, holder *jtx.Account, asset tx.Asset) tx.Amount {
	t.Helper()
	issuer, err := state.DecodeAccountID(asset.Issuer)
	require.NoError(t, err)
	data, err := e.LedgerEntry(keylet.Line(holder.ID, issuer, asset.Currency))
	require.NoError(t, err)
	require.NotEmpty(t, data, "holding trust line must exist")
	line, err := state.ParseRippleState(data)
	require.NoError(t, err)
	amount := line.Balance
	if !keylet.IsLowAccount(holder.ID, issuer) {
		amount = amount.Negate()
	}
	require.Equal(t, asset.Currency, amount.Currency)
	amount.Issuer = asset.Issuer
	return amount
}
func ammIssuedAmount(t *testing.T, value string, asset tx.Asset) tx.Amount {
	t.Helper()
	amount, err := state.NewIssuedAmountFromDecimalString(value, asset.Currency, asset.Issuer)
	require.NoError(t, err)
	return amount
}
func requireAMMAmount(t *testing.T, got tx.Amount, value string) {
	t.Helper()
	require.Equal(t, ammIssuedAmount(t, value, tx.Asset{Currency: got.Currency, Issuer: got.Issuer}), got)
}
