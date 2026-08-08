package rpcenv

import (
	"fmt"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/credential"
	"github.com/stretchr/testify/require"
)

func TestCredentialRPCViews(t *testing.T) {
	env := New(t)
	issuer := jtx.NewAccount("issuer")
	subject := jtx.NewAccount("subject")
	env.Fund(issuer, subject)
	env.Close()

	const credentialType = "rpc"
	const credentialTypeHex = "727063"
	jtx.RequireTxSuccess(t, env.Submit(
		credential.CredentialCreateText(issuer, subject, credentialType).Build(),
	))
	jtx.RequireTxSuccess(t, env.Submit(
		credential.CredentialAcceptText(subject, issuer, credentialType).Build(),
	))
	env.Close()

	credentialKey := jtx.CredentialKeylet(subject, issuer, credentialType)
	credentialIndex := fmt.Sprintf("%X", credentialKey.Key)
	for _, account := range []*jtx.Account{issuer, subject} {
		objectsResult, rpcErr := env.RPC("account_objects", map[string]any{
			"account":      account.Address,
			"type":         "credential",
			"ledger_index": "validated",
		})
		require.Nil(t, rpcErr)
		objects := didJSONMap(t, objectsResult)["account_objects"].([]any)
		require.Len(t, objects, 1)
		object := objects[0].(map[string]any)
		require.Equal(t, "Credential", object["LedgerEntryType"])
		require.Equal(t, credentialIndex, object["index"])
		require.Equal(t, issuer.Address, object["Issuer"])
		require.Equal(t, subject.Address, object["Subject"])
		require.Equal(t, credentialTypeHex, object["CredentialType"])
	}

	entryResult, rpcErr := env.RPC("ledger_entry", map[string]any{
		"credential": map[string]any{
			"subject":         subject.Address,
			"issuer":          issuer.Address,
			"credential_type": credentialTypeHex,
		},
		"ledger_index": "validated",
	})
	require.Nil(t, rpcErr)
	entry := didJSONMap(t, entryResult)
	require.Equal(t, credentialIndex, entry["index"])
	node := entry["node"].(map[string]any)
	require.Equal(t, "Credential", node["LedgerEntryType"])
	require.Equal(t, issuer.Address, node["Issuer"])
	require.Equal(t, subject.Address, node["Subject"])
	require.Equal(t, credentialTypeHex, node["CredentialType"])

	var expectedHashes []string
	for _, account := range []*jtx.Account{issuer, subject} {
		txResult, txErr := env.RPC("account_tx", map[string]any{
			"account": account.Address,
			"forward": true,
		})
		require.Nil(t, txErr)
		transactions := didJSONMap(t, txResult)["transactions"].([]any)
		require.Len(t, transactions, 2)
		types := make([]string, 0, len(transactions))
		hashes := make([]string, 0, len(transactions))
		for _, transaction := range transactions {
			fields := transaction.(map[string]any)["tx"].(map[string]any)
			types = append(types, fields["TransactionType"].(string))
			hashes = append(hashes, fields["hash"].(string))
		}
		require.Equal(t, []string{"CredentialCreate", "CredentialAccept"}, types)
		if expectedHashes == nil {
			expectedHashes = hashes
		} else {
			require.Equal(t, expectedHashes, hashes)
		}
	}
	pageOneResult, pageOneErr := env.RPC("account_tx", map[string]any{
		"account": issuer.Address,
		"forward": true,
		"limit":   1,
	})
	require.Nil(t, pageOneErr)
	pageOne := didJSONMap(t, pageOneResult)
	require.Len(t, pageOne["transactions"].([]any), 1)
	marker := pageOne["marker"].(map[string]any)
	pageTwoResult, pageTwoErr := env.RPC("account_tx", map[string]any{
		"account": issuer.Address,
		"forward": true,
		"limit":   1,
		"marker":  marker,
	})
	require.Nil(t, pageTwoErr)
	pageTwo := didJSONMap(t, pageTwoResult)
	require.Len(t, pageTwo["transactions"].([]any), 1)
	require.NotContains(t, pageTwo, "marker")

	jtx.RequireTxSuccess(t, env.Submit(
		credential.CredentialDeleteText(subject, subject, issuer, credentialType).Build(),
	))
	env.Close()
	_, rpcErr = env.RPC("ledger_entry", map[string]any{
		"credential":   credentialIndex,
		"ledger_index": "validated",
	})
	requireRPCError(t, rpcErr, "entryNotFound")
	for _, account := range []*jtx.Account{issuer, subject} {
		objectsResult, objectsErr := env.RPC("account_objects", map[string]any{
			"account":      account.Address,
			"type":         "credential",
			"ledger_index": "validated",
		})
		require.Nil(t, objectsErr)
		require.Empty(t, didJSONMap(t, objectsResult)["account_objects"])
	}
}
