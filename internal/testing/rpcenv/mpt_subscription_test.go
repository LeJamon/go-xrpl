package rpcenv_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/testing/rpcenv"
)

// Based on rippled Subscribe_test.cpp testSubMPT: the metadata of real
// MPTokenIssuanceCreate and MPTokenAuthorize transactions reaches an issuance
// subscriber, while an unrelated payment does not.
func TestMPTIssuanceSubscriberReceivesRealTransactions(t *testing.T) {
	env := rpcenv.New(t)
	alice := jtx.NewAccount("alice")
	bob := jtx.NewAccount("bob")
	carol := jtx.NewAccount("carol")
	issuance := mpt.NewMPTTester(t, env.TestEnv, alice, mpt.MPTInit{Holders: []*jtx.Account{bob}})
	env.Fund(carol)
	issuance.Create(mpt.CreateOpts{})
	issuance.Authorize(mpt.AuthorizeOpts{Account: bob})
	jtx.RequireTxSuccess(t, env.Submit(payment.Pay(bob, carol, uint64(jtx.XRP(1))).Build()))
	env.Close()

	manager := subscription.NewManager()
	conn := subscription.NewConnection("mpt", make(chan []byte, 8))
	registration, ok := manager.Attach(conn)
	require.True(t, ok)
	require.Nil(t, manager.HandleSubscribe(registration, types.SubscriptionRequest{
		MPTIssuances: []string{issuance.IssuanceID()},
	}, false))
	publisher := rpc.NewPublisher(manager)

	published := map[string]bool{}
	for _, account := range []*jtx.Account{alice, bob} {
		result, rpcErr := env.RPCAs("account_tx", map[string]any{"account": account.Address}, types.RoleAdmin, types.ApiVersion1)
		require.Nil(t, rpcErr)
		encoded, err := json.Marshal(result)
		require.NoError(t, err)
		var page struct {
			Transactions []struct {
				Tx   json.RawMessage `json:"tx"`
				Meta json.RawMessage `json:"meta"`
			} `json:"transactions"`
		}
		require.NoError(t, json.Unmarshal(encoded, &page))
		for _, entry := range page.Transactions {
			var tx struct {
				Hash string `json:"hash"`
			}
			require.NoError(t, json.Unmarshal(entry.Tx, &tx))
			if published[tx.Hash] {
				continue
			}
			published[tx.Hash] = true
			publisher.PublishTransaction(&rpc.TransactionEvent{
				Type: "transaction", EngineResult: "tesSUCCESS", Status: "closed", Validated: true,
				Transaction: entry.Tx, Meta: entry.Meta, Hash: tx.Hash,
			}, nil)
		}
	}
	require.NotEmpty(t, published)

	var received []string
	for {
		select {
		case data := <-conn.Outbound():
			var event struct {
				Type        string         `json:"type"`
				Transaction map[string]any `json:"transaction"`
			}
			require.NoError(t, json.Unmarshal(data, &event))
			require.Equal(t, "transaction", event.Type)
			received = append(received, event.Transaction["TransactionType"].(string))
			continue
		case <-time.After(100 * time.Millisecond):
		}
		break
	}
	require.ElementsMatch(t, []string{"MPTokenIssuanceCreate", "MPTokenAuthorize"}, received,
		"the bob→carol XRP payment touches no issuance")
}
