package nftoken

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type issuerCreditFaultView struct {
	*mockView
	issuerKey keylet.Keylet
	readErr   error
	updateErr error
	reads     int
	updates   int
}

func (v *issuerCreditFaultView) Read(k keylet.Keylet) ([]byte, error) {
	if k.Key == v.issuerKey.Key {
		v.reads++
		if v.readErr != nil {
			return nil, v.readErr
		}
	}
	return v.mockView.Read(k)
}

func (v *issuerCreditFaultView) Update(k keylet.Keylet, data []byte) error {
	if k.Key == v.issuerKey.Key {
		v.updates++
		if v.updateErr != nil {
			return v.updateErr
		}
	}
	return v.mockView.Update(k, data)
}

func TestCreditNFTokenIssuerXRP(t *testing.T) {
	issuerID := [20]byte{1}
	sourceID := [20]byte{2}
	issuerAddress, err := state.EncodeAccountID(issuerID)
	require.NoError(t, err)

	newContext := func(t *testing.T, balance uint64) (*tx.ApplyContext, *issuerCreditFaultView) {
		t.Helper()
		view := &issuerCreditFaultView{
			mockView:  newMockView(),
			issuerKey: keylet.Account(issuerID),
		}
		issuerData, err := state.SerializeAccountRoot(&state.AccountRoot{
			Account: issuerAddress,
			Balance: balance,
		})
		require.NoError(t, err)
		view.store[view.issuerKey.Key] = issuerData
		return &tx.ApplyContext{
			View:      view,
			AccountID: sourceID,
			Account:   &state.AccountRoot{Balance: 100},
		}, view
	}

	t.Run("success", func(t *testing.T) {
		ctx, view := newContext(t, 100)

		require.Equal(t, ter.TesSUCCESS, creditNFTokenIssuerXRP(ctx, issuerID, 25))
		issuer, err := tx.ReadAccountRoot(view, issuerID)
		require.NoError(t, err)
		require.Equal(t, uint64(125), issuer.Balance)
		require.Equal(t, 2, view.reads)
		require.Equal(t, 1, view.updates)
	})

	t.Run("source alias", func(t *testing.T) {
		ctx, view := newContext(t, 100)
		ctx.AccountID = issuerID

		require.Equal(t, ter.TesSUCCESS, creditNFTokenIssuerXRP(ctx, issuerID, 25))
		require.Equal(t, uint64(125), ctx.Account.Balance)
		require.Zero(t, view.reads)
		require.Zero(t, view.updates)
	})

	t.Run("zero amount", func(t *testing.T) {
		ctx, view := newContext(t, 100)

		require.Equal(t, ter.TesSUCCESS, creditNFTokenIssuerXRP(ctx, issuerID, 0))
		require.Equal(t, uint64(100), ctx.Account.Balance)
		require.Zero(t, view.reads)
		require.Zero(t, view.updates)
	})

	for _, tc := range []struct {
		name  string
		setup func(*issuerCreditFaultView)
	}{
		{
			name: "read failure",
			setup: func(view *issuerCreditFaultView) {
				view.readErr = errors.New("storage read failure")
			},
		},
		{
			name: "missing account",
			setup: func(view *issuerCreditFaultView) {
				delete(view.store, view.issuerKey.Key)
			},
		},
		{
			name: "malformed account",
			setup: func(view *issuerCreditFaultView) {
				view.store[view.issuerKey.Key] = []byte{1, 2, 3}
			},
		},
		{
			name: "serialization failure",
			setup: func(view *issuerCreditFaultView) {
				issuerData, err := state.SerializeAccountRoot(&state.AccountRoot{
					Account: issuerAddress,
					Balance: state.MaxNativeDrops,
				})
				require.NoError(t, err)
				view.store[view.issuerKey.Key] = issuerData
			},
		},
		{
			name: "update failure",
			setup: func(view *issuerCreditFaultView) {
				view.updateErr = errors.New("storage update failure")
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, view := newContext(t, 100)
			tc.setup(view)
			before := append([]byte(nil), view.store[view.issuerKey.Key]...)

			require.Equal(t, ter.TefINTERNAL, creditNFTokenIssuerXRP(ctx, issuerID, 1))
			require.Equal(t, before, view.store[view.issuerKey.Key])
			require.Equal(t, uint64(100), ctx.Account.Balance)
		})
	}
}

func TestPayNFTokenXRP(t *testing.T) {
	ctxID := [20]byte{1}
	externalID := [20]byte{2}
	externalAddress, err := state.EncodeAccountID(externalID)
	require.NoError(t, err)

	for _, tc := range []struct {
		name        string
		fromContext bool
		open        bool
		balance     uint64
		amount      uint64
		result      ter.Result
	}{
		{name: "context insufficient closed", fromContext: true, balance: 99, amount: 100, result: ter.TecFAILED_PROCESSING},
		{name: "context insufficient open", fromContext: true, open: true, balance: 99, amount: 100, result: ter.TelFAILED_PROCESSING},
		{name: "external insufficient closed", balance: 99, amount: 100, result: ter.TecFAILED_PROCESSING},
		{name: "external insufficient open", open: true, balance: 99, amount: 100, result: ter.TelFAILED_PROCESSING},
		{name: "context exact balance", fromContext: true, balance: 100, amount: 100, result: ter.TesSUCCESS},
		{name: "external exact balance", balance: 100, amount: 100, result: ter.TesSUCCESS},
	} {
		t.Run(tc.name, func(t *testing.T) {
			view := newMockView()
			externalBalance := uint64(25)
			contextBalance := uint64(25)
			fromID, toID := externalID, ctxID
			if tc.fromContext {
				fromID, toID = ctxID, externalID
				contextBalance = tc.balance
			} else {
				externalBalance = tc.balance
			}
			externalData, err := state.SerializeAccountRoot(&state.AccountRoot{
				Account: externalAddress,
				Balance: externalBalance,
			})
			require.NoError(t, err)
			view.store[keylet.Account(externalID).Key] = externalData
			ctx := &tx.ApplyContext{
				View:      view,
				AccountID: ctxID,
				Account:   &state.AccountRoot{Balance: contextBalance},
				Config:    tx.EngineConfig{ViewOpen: tc.open},
			}

			require.Equal(t, tc.result, payNFTokenXRP(ctx, fromID, toID, tc.amount))

			external, err := tx.ReadAccountRoot(view, externalID)
			require.NoError(t, err)
			if tc.result == ter.TesSUCCESS {
				if tc.fromContext {
					require.Zero(t, ctx.Account.Balance)
					require.Equal(t, uint64(125), external.Balance)
				} else {
					require.Zero(t, external.Balance)
					require.Equal(t, uint64(125), ctx.Account.Balance)
				}
			} else {
				require.Equal(t, contextBalance, ctx.Account.Balance)
				require.Equal(t, externalBalance, external.Balance)
			}
		})
	}
}
