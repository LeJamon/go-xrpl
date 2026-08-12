package delegate_test

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/require"
)

type delegatePreclaimErrorView struct {
	tx.LedgerView
	authorizeKey  [32]byte
	authorizeData []byte
	delegateKey   [32]byte
}

func (v *delegatePreclaimErrorView) Read(k keylet.Keylet) ([]byte, error) {
	switch k.Key {
	case v.authorizeKey:
		return v.authorizeData, nil
	case v.delegateKey:
		return nil, errors.New("storage failure")
	default:
		return nil, nil
	}
}

func TestDelegateSetPreclaimPropagatesDelegateReadError(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const authorize = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	accountID, err := state.DecodeAccountID(account)
	require.NoError(t, err)
	authorizeID, err := state.DecodeAccountID(authorize)
	require.NoError(t, err)
	authorizeData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account:  authorize,
		Balance:  1,
		Sequence: 1,
	})
	require.NoError(t, err)

	view := &delegatePreclaimErrorView{
		authorizeKey:  keylet.Account(authorizeID).Key,
		authorizeData: authorizeData,
		delegateKey:   keylet.Delegate(accountID, authorizeID).Key,
	}
	transaction := delegatetx.NewDelegateSet(account)
	transaction.Authorize = authorize
	require.Equal(t, ter.TefINTERNAL, transaction.Preclaim(view, tx.EngineConfig{}))
}

func TestDelegateSetApplyPropagatesDelegateReadError(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	const authorize = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
	accountID, err := state.DecodeAccountID(account)
	require.NoError(t, err)
	authorizeID, err := state.DecodeAccountID(authorize)
	require.NoError(t, err)

	view := &delegatePreclaimErrorView{
		delegateKey: keylet.Delegate(accountID, authorizeID).Key,
	}
	transaction := delegatetx.NewDelegateSet(account)
	transaction.Authorize = authorize
	transaction.Permissions = []delegatetx.Permission{delegatetx.NewPermission("Payment")}
	ctx := &tx.ApplyContext{
		View:      view,
		Account:   &state.AccountRoot{Account: account, Balance: 1},
		AccountID: accountID,
		Log:       xrpllog.Discard(),
		Ctx:       context.Background(),
	}

	require.Equal(t, ter.TefINTERNAL, transaction.Apply(ctx))
}
