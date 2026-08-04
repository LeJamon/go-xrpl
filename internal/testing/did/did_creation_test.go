package did_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/did"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
)

func TestSetValidInitial(t *testing.T) {
	runWithFeatureSets(t, testSetValidInitial)
}

func testSetValidInitial(t *testing.T, fixEmptyDID bool) {
	env := setupEnv(t, fixEmptyDID)

	tests := []struct {
		name     string
		account  *jtx.Account
		uri      string
		document string
		data     string
	}{
		{name: "URI", account: jtx.NewAccount("alice"), uri: "uri"},
		{name: "DIDDocument", account: jtx.NewAccount("bob"), document: "data"},
		{name: "Data", account: jtx.NewAccount("charlie"), data: "data"},
		{name: "URI and Data", account: jtx.NewAccount("dave"), uri: "uri", data: "attest"},
		{name: "URI and DIDDocument", account: jtx.NewAccount("edna"), uri: "uri", document: "data"},
		{name: "DIDDocument and Data", account: jtx.NewAccount("francis"), document: "data", data: "attest"},
		{name: "all fields", account: jtx.NewAccount("george"), uri: "uri", document: "data", data: "attest"},
	}
	accounts := make([]*jtx.Account, 0, len(tests))
	for _, tc := range tests {
		accounts = append(accounts, tc.account)
	}
	env.Fund(accounts...)
	env.Close()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			builder := did.DIDSet(tc.account)
			if tc.uri != "" {
				builder.URI(tc.uri)
			}
			if tc.document != "" {
				builder.Document(tc.document)
			}
			if tc.data != "" {
				builder.Data(tc.data)
			}
			transaction := builder.Build()
			jtx.RequireTxSuccess(t, env.Submit(transaction))
			require.Equal(t, uint32(1), env.OwnerCount(tc.account))

			entry := getDIDEntry(t, env, tc.account)
			require.NotNil(t, entry)
			require.Equal(t, tc.account.ID, entry.Account)
			require.Zero(t, entry.OwnerNode)
			assertDIDValue(t, "URI", entry.URI, tc.uri)
			assertDIDValue(t, "DIDDocument", entry.DIDDocument, tc.document)
			assertDIDValue(t, "Data", entry.Data, tc.data)
			hash, err := txcore.ComputeTransactionHash(transaction)
			require.NoError(t, err)
			require.Equal(t, hash, entry.PreviousTxnID)
			require.Equal(t, env.LedgerSeq(), entry.PreviousTxnLgrSeq)
		})
	}
}

func assertDIDValue(t *testing.T, name, actual, expected string) {
	t.Helper()
	if expected == "" {
		requireFieldAbsent(t, name, actual)
		return
	}
	requireFieldPresent(t, name, actual)
	checkVL(t, name, actual, expected)
}
