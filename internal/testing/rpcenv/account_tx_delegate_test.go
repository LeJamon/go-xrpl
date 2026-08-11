package rpcenv

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	delegatetx "github.com/LeJamon/go-xrpl/internal/tx/delegate"
	paymenttx "github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/stretchr/testify/require"
)

func TestAccountTxDelegateFiltering(t *testing.T) {
	env := New(t)
	env.EnableFeature("PermissionDelegationV1_1")
	owner := jtx.NewAccount("owner")
	authorizer := jtx.NewAccount("authorizer")
	destination := jtx.NewAccount("destination")
	env.Fund(owner, authorizer, destination)
	env.Close()
	ledgerMin := env.LedgerSeq()

	set := delegatetx.NewDelegateSet(owner.Address)
	set.Authorize = authorizer.Address
	set.Permissions = []delegatetx.Permission{delegatetx.NewPermission("Payment")}
	jtx.RequireTxSuccess(t, env.SubmitSignedWith(set, owner))
	env.Close()

	for range 2 {
		payment := paymenttx.NewPayment(owner.Address, destination.Address, txcore.NewXRPAmount(1_000_000))
		payment.Delegate = authorizer.Address
		jtx.RequireTxSuccess(t, env.SubmitSignedWith(payment, authorizer))
		env.Close()
	}

	for _, query := range []struct {
		name         string
		account      string
		filter       string
		counterparty string
	}{
		{name: "actor", account: owner.Address, filter: "actor", counterparty: authorizer.Address},
		{name: "authorizer", account: authorizer.Address, filter: "authorizer", counterparty: owner.Address},
	} {
		t.Run(query.name, func(t *testing.T) {
			result, rpcErr := env.RPCAs("account_tx", map[string]any{
				"account":          query.account,
				"forward":          true,
				"ledger_index_min": ledgerMin,
				"delegate": map[string]any{
					"delegate_filter": query.filter,
					"counter_party":   query.counterparty,
				},
			}, types.RoleAdmin, types.ApiVersion3)
			require.Nil(t, rpcErr)
			transactions := didJSONMap(t, result)["transactions"].([]any)
			require.Len(t, transactions, 2)
			for _, transaction := range transactions {
				fields := transaction.(map[string]any)["tx_json"].(map[string]any)
				require.Equal(t, owner.Address, fields["Account"])
				require.Equal(t, authorizer.Address, fields["Delegate"])
			}
		})
	}

	pageOneResult, pageOneErr := env.RPCAs("account_tx", map[string]any{
		"account":          owner.Address,
		"forward":          true,
		"ledger_index_min": ledgerMin,
		"limit":            1,
		"delegate":         map[string]any{"delegate_filter": "actor"},
	}, types.RoleAdmin, types.ApiVersion3)
	require.Nil(t, pageOneErr)
	pageOne := didJSONMap(t, pageOneResult)
	require.Len(t, pageOne["transactions"].([]any), 1)
	marker := pageOne["marker"].(map[string]any)
	require.Equal(t, true, marker["delegate"])

	pageTwoResult, pageTwoErr := env.RPCAs("account_tx", map[string]any{
		"account":          owner.Address,
		"forward":          true,
		"ledger_index_min": ledgerMin,
		"limit":            1,
		"marker":           marker,
		"delegate":         map[string]any{"delegate_filter": "actor"},
	}, types.RoleAdmin, types.ApiVersion3)
	require.Nil(t, pageTwoErr)
	require.Len(t, didJSONMap(t, pageTwoResult)["transactions"].([]any), 1)
}
