package rpc

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

func TestPublishTransactionUsesSubscriberAPIVersion(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	manager := subscription.NewManager()
	streamV1 := addPublisherTestConnection(t, manager, "stream-v1")
	streamV2 := addPublisherTestConnection(t, manager, "stream-v2")
	accountV2 := addPublisherTestConnection(t, manager, "account-v2")
	proposedV2 := addPublisherTestConnection(t, manager, "proposed-v2")
	accountProposedV1 := addPublisherTestConnection(t, manager, "account-proposed-v1")

	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, streamV1), types.SubscriptionRequest{
		Streams:    []types.SubscriptionType{types.SubTransactions},
		ApiVersion: types.ApiVersion1,
	}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, streamV2), types.SubscriptionRequest{
		Streams:    []types.SubscriptionType{types.SubTransactions},
		ApiVersion: types.ApiVersion2,
	}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, accountV2), types.SubscriptionRequest{
		Accounts:   []string{account},
		ApiVersion: types.ApiVersion2,
	}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, proposedV2), types.SubscriptionRequest{
		Streams:    []types.SubscriptionType{types.SubTransactionsProposed},
		ApiVersion: types.ApiVersion2,
	}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, accountProposedV1), types.SubscriptionRequest{
		AccountsProposed: []string{account},
		ApiVersion:       types.ApiVersion1,
	}, true))

	event := publisherTestTransactionEvent()
	NewPublisher(manager).PublishTransaction(event, []string{account})

	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, streamV1), types.ApiVersion1)
	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, streamV2), types.ApiVersion2)
	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, accountV2), types.ApiVersion2)
	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, proposedV2), types.ApiVersion2)
	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, accountProposedV1), types.ApiVersion1)
}

func TestPublishProposedTransactionUsesSubscriberAPIVersion(t *testing.T) {
	const account = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	manager := subscription.NewManager()
	v1 := addPublisherTestConnection(t, manager, "proposed-stream-v1")
	v2 := addPublisherTestConnection(t, manager, "proposed-account-v2")
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, v1), types.SubscriptionRequest{
		Streams:    []types.SubscriptionType{types.SubTransactionsProposed},
		ApiVersion: types.ApiVersion1,
	}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, v2), types.SubscriptionRequest{
		AccountsProposed: []string{account},
		ApiVersion:       types.ApiVersion2,
	}, true))

	hash := strings.Repeat("A", 64)
	event := NewProposedTransactionEvent(
		json.RawMessage(`{"TransactionType":"Payment","Amount":"100"}`),
		"tesSUCCESS",
		0,
		"The transaction was applied.",
		2,
		hash,
	)
	NewPublisher(manager).PublishProposedTransaction(event, []string{account})

	assertPublishedProposedVersion(t, readPublisherTestEvent(t, v1), types.ApiVersion1, hash)
	assertPublishedProposedVersion(t, readPublisherTestEvent(t, v2), types.ApiVersion2, hash)
}

func TestPublishOrderBookChangeUsesSubscriberAPIVersion(t *testing.T) {
	manager := subscription.NewManager()
	bookV1 := addPublisherTestConnection(t, manager, "book-v1")
	bookV2 := addPublisherTestConnection(t, manager, "book-v2")

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	book := types.BookRequest{
		TakerGets: json.RawMessage(`{"currency":"XRP"}`),
		TakerPays: json.RawMessage(`{"currency":"USD","issuer":"` + issuer + `"}`),
	}
	for _, tc := range []struct {
		conn       *subscription.Connection
		apiVersion int
	}{
		{conn: bookV1, apiVersion: types.ApiVersion1},
		{conn: bookV2, apiVersion: types.ApiVersion2},
	} {
		require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, tc.conn), types.SubscriptionRequest{Books: []types.BookRequest{book}, ApiVersion: tc.apiVersion}, true))
	}

	NewPublisher(manager).PublishOrderBookChange(
		publisherTestTransactionEvent(),
		[]types.OrderBookSpec{{
			TakerGets: types.CurrencySpec{Currency: "XRP"},
			TakerPays: types.CurrencySpec{Currency: "USD", Issuer: issuer},
		}},
	)

	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, bookV1), types.ApiVersion1)
	assertPublishedTransactionVersion(t, readPublisherTestEvent(t, bookV2), types.ApiVersion2)
}

func TestPublishValidationUsesSubscriberAPIVersion(t *testing.T) {
	manager := subscription.NewManager()
	v1 := addPublisherTestConnection(t, manager, "validation-v1")
	v2 := addPublisherTestConnection(t, manager, "validation-v2")
	v3 := addPublisherTestConnection(t, manager, "validation-v3")
	none := addPublisherTestConnection(t, manager, "validation-none")
	for _, test := range []struct {
		conn    *subscription.Connection
		version int
	}{
		{v1, types.ApiVersion1},
		{v2, types.ApiVersion2},
		{v3, types.ApiVersion3},
	} {
		require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, test.conn), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubValidations}, ApiVersion: test.version}, true))
	}

	closeTime := uint32(0)
	event := &ValidationEvent{
		Type:                "validationReceived",
		Data:                "ABCD",
		Flags:               1,
		Full:                true,
		LedgerHash:          strings.Repeat("A", 64),
		LedgerIndex:         42,
		CloseTime:           &closeTime,
		NetworkID:           0,
		Signature:           "CAFE",
		SigningTime:         800000000,
		ValidationPublicKey: "n9Example",
	}
	NewPublisher(manager).PublishValidation(event)

	for _, test := range []struct {
		conn       *subscription.Connection
		wantLedger string
	}{
		{v1, `"42"`},
		{v2, "42"},
		{v3, "42"},
	} {
		var payload map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(readPublisherTestEvent(t, test.conn), &payload))
		require.Equal(t, test.wantLedger, string(payload["ledger_index"]))
		require.Equal(t, "0", string(payload["network_id"]))
		require.Equal(t, "0", string(payload["close_time"]))
		require.Equal(t, `"ABCD"`, string(payload["data"]))
	}
	select {
	case <-none.Outbound():
		t.Fatal("non-subscriber received validation")
	default:
	}
}

func TestPublishLedgerClosedFieldPresence(t *testing.T) {
	manager := subscription.NewManager()
	conn := addPublisherTestConnection(t, manager, "ledger")
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, conn), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true))
	publisher := NewPublisher(manager)
	event := &LedgerCloseEvent{
		Type:             "ledgerClosed",
		LedgerIndex:      42,
		NetworkID:        0,
		ValidatedLedgers: "1-42",
	}
	publisher.PublishLedgerClosed(event)
	var payload map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(readPublisherTestEvent(t, conn), &payload))
	require.Equal(t, "0", string(payload["network_id"]))
	require.NotContains(t, payload, "fee_ref")
	require.NotContains(t, payload, "validated_ledgers")

	feeRef := uint64(10)
	event.FeeRef = &feeRef
	event.ValidatedLedgers = "1-42"
	event.ValidatedLedgersPresent = true
	publisher.PublishLedgerClosed(event)
	require.NoError(t, json.Unmarshal(readPublisherTestEvent(t, conn), &payload))
	require.Equal(t, "10", string(payload["fee_ref"]))
	require.Equal(t, `"1-42"`, string(payload["validated_ledgers"]))
}

func TestPublishOrderBookChangeDeduplicatesAcrossAffectedBooks(t *testing.T) {
	const (
		issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		mptID  = "00000001C4F149B6F2A4B6A4C4A01C1570C4A040A3D9B221"
	)
	domain := strings.Repeat("A", 64)
	manager := subscription.NewManager()
	listener := addPublisherTestConnection(t, manager, "listener")
	other := addPublisherTestConnection(t, manager, "other")

	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, listener), types.SubscriptionRequest{Books: []types.BookRequest{
		{
			TakerGets: json.RawMessage(`{"currency":"XRP"}`),
			TakerPays: json.RawMessage(`{"currency":"USD","issuer":"` + issuer + `"}`),
			Domain:    strings.ToLower(domain),
		},
		{
			TakerGets: json.RawMessage(`{"mpt_issuance_id":"` + strings.ToLower(mptID) + `"}`),
			TakerPays: json.RawMessage(`{"currency":"XRP"}`),
		},
	}}, true))
	require.Nil(t, manager.HandleSubscribe(testRegistration(t, manager, other), types.SubscriptionRequest{Books: []types.BookRequest{
		{
			TakerGets: json.RawMessage(`{"currency":"XRP"}`),
			TakerPays: json.RawMessage(`{"currency":"USD","issuer":"` + issuer + `"}`),
			Domain:    strings.Repeat("B", 64),
		},
		{
			TakerGets: json.RawMessage(`{"mpt_issuance_id":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`),
			TakerPays: json.RawMessage(`{"currency":"XRP"}`),
		},
	}}, true))

	NewPublisher(manager).PublishOrderBookChange(publisherTestTransactionEvent(), []types.OrderBookSpec{
		{
			TakerGets: types.CurrencySpec{Currency: "XRP"},
			TakerPays: types.CurrencySpec{Currency: "USD", Issuer: issuer},
			Domain:    domain,
		},
		{
			TakerGets: types.CurrencySpec{MPTIssuanceID: mptID},
			TakerPays: types.CurrencySpec{Currency: "XRP"},
		},
	})

	readPublisherTestEvent(t, listener)
	select {
	case <-listener.Outbound():
		t.Fatal("listener received the transaction more than once")
	default:
	}
	select {
	case <-other.Outbound():
		t.Fatal("non-matching domain or MPT issuance received the transaction")
	default:
	}
}

func TestMarshalTransactionEventHandlesNullTransaction(t *testing.T) {
	event := publisherTestTransactionEvent()
	event.Transaction = json.RawMessage("null")

	data, err := marshalTransactionEvent(event, types.ApiVersion1)
	require.NoError(t, err)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(data, &payload))
	transaction, ok := payload["transaction"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, event.Hash, transaction["hash"])
}

func addPublisherTestConnection(t testing.TB, manager *subscription.Manager, id string) *subscription.Connection {
	t.Helper()
	conn := subscription.NewConnection(id, make(chan []byte, 2))
	_ = testRegistration(t, manager, conn)
	return conn
}

func publisherTestTransactionEvent() *TransactionEvent {
	return &TransactionEvent{
		Type:                "transaction",
		EngineResult:        "tesSUCCESS",
		EngineResultCode:    0,
		EngineResultMessage: "The transaction was applied.",
		LedgerHash:          strings.Repeat("B", 64),
		LedgerIndex:         2,
		Meta:                json.RawMessage(`{"TransactionResult":"tesSUCCESS","mpt_issuance_id":"ABCD"}`),
		Transaction:         json.RawMessage(`{"TransactionType":"Payment","Amount":"100"}`),
		Hash:                strings.Repeat("A", 64),
		Validated:           true,
		Status:              "closed",
	}
}

func readPublisherTestEvent(t *testing.T, conn *subscription.Connection) []byte {
	t.Helper()
	select {
	case data := <-conn.Outbound():
		return data
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published transaction")
		return nil
	}
}

func assertPublishedTransactionVersion(t *testing.T, data []byte, apiVersion int) {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal(data, &event))
	require.Equal(t, "closed", event["status"])
	require.Equal(t, "ABCD", event["meta"].(map[string]any)["mpt_issuance_id"])

	if apiVersion == types.ApiVersion1 {
		require.NotContains(t, event, "tx_json")
		require.NotContains(t, event, "hash")
		txJSON := event["transaction"].(map[string]any)
		require.Equal(t, "100", txJSON["Amount"])
		require.Equal(t, "100", txJSON["DeliverMax"])
		require.Equal(t, strings.Repeat("A", 64), txJSON["hash"])
		return
	}

	require.NotContains(t, event, "transaction")
	require.Equal(t, strings.Repeat("A", 64), event["hash"])
	txJSON := event["tx_json"].(map[string]any)
	require.NotContains(t, txJSON, "Amount")
	require.NotContains(t, txJSON, "hash")
	require.Equal(t, "100", txJSON["DeliverMax"])
}

func assertPublishedProposedVersion(t *testing.T, data []byte, apiVersion int, hash string) {
	t.Helper()
	var event map[string]any
	require.NoError(t, json.Unmarshal(data, &event))
	require.Equal(t, "proposed", event["status"])
	require.Equal(t, false, event["validated"])
	require.NotContains(t, event, "account")

	if apiVersion == types.ApiVersion1 {
		require.NotContains(t, event, "tx_json")
		require.NotContains(t, event, "hash")
		txJSON := event["transaction"].(map[string]any)
		require.Equal(t, hash, txJSON["hash"])
		require.Equal(t, "100", txJSON["Amount"])
		require.Equal(t, "100", txJSON["DeliverMax"])
		return
	}

	require.NotContains(t, event, "transaction")
	require.Equal(t, hash, event["hash"])
	txJSON := event["tx_json"].(map[string]any)
	require.NotContains(t, txJSON, "hash")
	require.NotContains(t, txJSON, "Amount")
	require.Equal(t, "100", txJSON["DeliverMax"])
}
