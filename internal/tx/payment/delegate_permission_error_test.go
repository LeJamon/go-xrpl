package payment

import (
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type delegatePermissionReadErrorView struct {
	tx.LedgerView
	err error
}

func (v *delegatePermissionReadErrorView) Read(keylet.Keylet) ([]byte, error) {
	return nil, v.err
}

func TestPaymentMintBurnPropagatesTrustlineReadError(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const destination = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	amount := state.NewIssuedAmountFromFloat64(1, "USD", account)
	view := &delegatePermissionReadErrorView{err: errors.New("storage failure")}
	pc := tx.DelegatePermissionContext{
		View:        view,
		Permissions: []uint32{tx.GranularPaymentMint},
	}

	require.Equal(t, ter.TefINTERNAL, paymentMintBurn(pc, amount, account, destination))
}
