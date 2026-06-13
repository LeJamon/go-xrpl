package rpc

// subscribe_conformance_test.go
//
// Conformance tests based on rippled Subscribe_test.cpp.
// These tests cover gaps not addressed in subscribe_test.go.
//
// Rippled reference sections covered:
//   - testServer()            -> server stream subscribe/unsubscribe
//   - testLedger()            -> subscribe response contains ledger info
//   - testSubErrors(true)     -> badMarket, empty accounts, malformed stream
//   - testSubErrors(false)    -> unsubscribe error cases
//   - testTransactions_APIv1  -> unsubscribe stops delivery
//   - testSubBookChanges()    -> book_changes stream
//   - Concurrent safety       -> goroutine-safe subscription management

import (
	"encoding/json"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Bad Market Tests
// Based on rippled Subscribe_test.cpp testSubErrors(): badMarket
// rippled returns "badMarket" / "No such market." when taker_pays and
// taker_gets specify the same currency+issuer pair.

// TestSubscribeConformanceBadMarket tests that subscribing to a book where both
// sides are the same currency/issuer is rejected.
func TestSubscribeConformanceBadMarket(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// Same non-XRP currency on both sides: USD/gateway for USD/gateway
	takerPays, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				TakerGets: takerGets,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request, true)
	require.NotNil(t, err, "same asset on both sides must be rejected")
	assert.Equal(t, types.RpcBAD_MARKET, err.Code)
	assert.Equal(t, "badMarket", err.ErrorString)
	assert.Equal(t, "No such market.", err.Message)
}

// TestSubscribeConformanceBadMarketXRP tests badMarket with XRP on both sides.
func TestSubscribeConformanceBadMarketXRP(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	takerPays, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})
	takerGets, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				TakerGets: takerGets,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request, true)
	require.NotNil(t, err, "XRP/XRP book must be rejected")
	assert.Equal(t, types.RpcBAD_MARKET, err.Code)
	assert.Equal(t, "badMarket", err.ErrorString)
	assert.Equal(t, "No such market.", err.Message)
}

// Unsubscribe Stops Message Delivery Tests
// Based on rippled Subscribe_test.cpp testServer() and testTransactions_APIv1()
// After unsubscribing from a stream, the connection should NOT receive messages
// that are subsequently broadcast to that stream.

// TestSubscribeConformanceUnsubscribeStopsDelivery verifies that after
// unsubscribing from a stream, no further messages are delivered.
func TestSubscribeConformanceUnsubscribeStopsDelivery(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// Subscribe to ledger stream
	subscribeReq := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(conn, subscribeReq, true)
	require.Nil(t, err)

	// Broadcast should reach the connection
	msg1 := []byte(`{"type":"ledgerClosed","ledger_index":100}`)
	sm.BroadcastToStream(types.SubLedger, msg1, nil)

	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, msg1, received, "Should receive message while subscribed")
	default:
		t.Fatal("Expected to receive broadcast message while subscribed")
	}

	// Now unsubscribe
	unsubscribeReq := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeReq, true)
	require.Nil(t, err)

	// Broadcast again - should NOT be received
	msg2 := []byte(`{"type":"ledgerClosed","ledger_index":101}`)
	sm.BroadcastToStream(types.SubLedger, msg2, nil)

	select {
	case <-conn.SendChannel:
		t.Fatal("Should NOT receive broadcast message after unsubscribing")
	default:
		// Expected: no message received
	}
}

// TestSubscribeConformanceUnsubscribeAccountStopsDelivery verifies that after
// unsubscribing from an account, transactions for that account are no longer delivered.
func TestSubscribeConformanceUnsubscribeAccountStopsDelivery(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Subscribe to alice's account
	subscribeReq := types.SubscriptionRequest{
		Accounts: []string{alice},
	}
	err := sm.HandleSubscribe(conn, subscribeReq, true)
	require.Nil(t, err)

	// Broadcast for alice - should reach connection
	msg1 := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccounts(msg1, []string{alice})

	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, msg1, received)
	default:
		t.Fatal("Expected to receive message for subscribed account")
	}

	// Unsubscribe from alice
	unsubscribeReq := types.SubscriptionRequest{
		Accounts: []string{alice},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeReq, true)
	require.Nil(t, err)

	// Broadcast for alice again - should NOT be received
	msg2 := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","seq":2}`)
	sm.BroadcastToAccounts(msg2, []string{alice})

	select {
	case <-conn.SendChannel:
		t.Fatal("Should NOT receive message after unsubscribing from account")
	default:
		// Expected: no message
	}
}

// Multiple Connections: One Unsubscribes, Others Still Receive
// Based on rippled testLedger() / testTransactions_APIv1() patterns

// TestSubscribeConformancePartialUnsubscribe verifies that when one connection
// unsubscribes, other connections still receive messages.
func TestSubscribeConformancePartialUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")
	sm.AddConnection(conn1)
	sm.AddConnection(conn2)
	defer sm.RemoveConnection(conn1.ID)
	defer sm.RemoveConnection(conn2.ID)

	// Both subscribe to ledger
	req := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	require.Nil(t, sm.HandleSubscribe(conn1, req, true))
	require.Nil(t, sm.HandleSubscribe(conn2, req, true))

	// conn1 unsubscribes
	require.Nil(t, sm.HandleUnsubscribe(conn1, req, true))

	// Broadcast
	msg := []byte(`{"type":"ledgerClosed","ledger_index":200}`)
	sm.BroadcastToStream(types.SubLedger, msg, nil)

	// conn1 should NOT receive
	select {
	case <-conn1.SendChannel:
		t.Fatal("conn1 should NOT receive after unsubscribing")
	default:
	}

	// conn2 should still receive
	select {
	case received := <-conn2.SendChannel:
		assert.Equal(t, msg, received)
	default:
		t.Fatal("conn2 should still receive messages")
	}
}

// Subscribe/Unsubscribe Full Lifecycle on Same Connection
// Based on rippled testTransactions_APIv1(): subscribe transactions, unsub,
// subscribe accounts, unsub

// TestSubscribeConformanceFullLifecycle tests the full lifecycle of
// subscribe -> receive -> unsubscribe -> re-subscribe to different stream.
func TestSubscribeConformanceFullLifecycle(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// Step 1: Subscribe to transactions
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}, true)
	require.Nil(t, err)
	assert.Contains(t, conn.Subscriptions, types.SubTransactions)

	// Step 2: Receive a transaction broadcast
	txMsg := []byte(`{"type":"transaction","tx":{"TransactionType":"Payment"}}`)
	sm.BroadcastToStream(types.SubTransactions, txMsg, nil)
	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, txMsg, received)
	default:
		t.Fatal("Expected transaction message")
	}

	// Step 3: Unsubscribe from transactions
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}, true)
	require.Nil(t, err)
	assert.NotContains(t, conn.Subscriptions, types.SubTransactions)

	// Step 4: Subscribe to accounts
	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	err = sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Accounts: []string{alice},
	}, true)
	require.Nil(t, err)
	assert.Contains(t, conn.Subscriptions, types.SubAccounts)

	// Step 5: Transaction for a different account should NOT be received
	sm.BroadcastToAccounts(
		[]byte(`{"type":"transaction","account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"}`),
		[]string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	)
	select {
	case <-conn.SendChannel:
		t.Fatal("Should not receive message for unsubscribed account")
	default:
	}

	// Step 6: Transaction for alice should be received
	aliceMsg := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccounts(aliceMsg, []string{alice})
	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, aliceMsg, received)
	default:
		t.Fatal("Expected message for subscribed account")
	}

	// Step 7: Unsubscribe from accounts
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Accounts: []string{alice},
	}, true)
	require.Nil(t, err)
}

// Accounts Proposed Unsubscribe Tests
// Based on rippled Subscribe_test.cpp testSubErrors() for accounts_proposed

// TestSubscribeConformanceAccountsProposedUnsubscribe tests the full lifecycle
// of subscribing and unsubscribing from accounts_proposed.
func TestSubscribeConformanceAccountsProposedUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	// Subscribe to accounts_proposed
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		AccountsProposed: accounts,
	}, true)
	require.Nil(t, err)

	// Verify subscription was recorded
	config, exists := conn.Subscriptions[types.SubAccountsProposed]
	require.True(t, exists, "accounts_proposed subscription should be recorded")
	assert.Equal(t, 2, len(config.Accounts))

	// Unsubscribe from accounts_proposed by removing the subscription type directly
	// (The HandleUnsubscribe currently only handles Accounts, not AccountsProposed;
	// this test documents the current behavior.)
	delete(conn.Subscriptions, types.SubAccountsProposed)
	_, exists = conn.Subscriptions[types.SubAccountsProposed]
	assert.False(t, exists, "accounts_proposed subscription should be removed")
}

// Empty Subscription Request Tests
// Based on rippled: sending subscribe with no params still returns success

// TestSubscribeConformanceEmptyRequest verifies that subscribing with an empty
// request (no streams, accounts, or books) succeeds.
func TestSubscribeConformanceEmptyRequest(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{}, true)
	require.Nil(t, err, "Empty subscribe request should succeed")
	assert.Equal(t, 0, len(conn.Subscriptions), "No subscriptions should be added")
}

// TestSubscribeConformanceEmptyUnsubscribeRequest verifies that unsubscribing
// with an empty request succeeds.
func TestSubscribeConformanceEmptyUnsubscribeRequest(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// First subscribe to something
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 1, len(conn.Subscriptions))

	// Empty unsubscribe should not remove anything
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{}, true)
	require.Nil(t, err, "Empty unsubscribe request should succeed")
	assert.Equal(t, 1, len(conn.Subscriptions), "Existing subscriptions should remain")
}

// Subscribe Response Contains Ledger Info Tests
// Based on rippled Subscribe_test.cpp testLedger():
//   jv[result][ledger_index] == 2
//   jv[result][network_id] == env.app().config().NETWORK_ID

// TestSubscribeConformanceLedgerResponseFields verifies that the subscribe
// response for a ledger stream contains the expected fields.
func TestSubscribeConformanceLedgerResponseFields(t *testing.T) {
	sm := newTestSubscriptionManager()

	response := sm.GetSubscribeResponse(
		2, // ledgerIndex
		"ABC123DEF456ABC123DEF456ABC123DEF456ABC123DEF456ABC123DEF456AB", // ledgerHash (64 hex)
		735000000, // ledgerTime
		10,        // feeBase
		10000000,  // reserveBase
		2000000,   // reserveInc
	)

	// Verify all required fields per rippled conformance
	assert.Equal(t, "success", response.Status, "Response status should be 'success'")
	assert.Equal(t, uint32(2), response.LedgerIndex, "LedgerIndex should match")
	assert.NotEmpty(t, response.LedgerHash, "LedgerHash should be present")
	assert.Equal(t, uint32(735000000), response.LedgerTime, "LedgerTime should match")
	assert.Equal(t, uint64(10), response.FeeBase, "FeeBase should match")
	assert.Equal(t, uint64(10000000), response.ReserveBase, "ReserveBase should match")
	assert.Equal(t, uint64(2000000), response.ReserveInc, "ReserveInc should match")
}

// book_changes Stream Tests
// Based on rippled Subscribe_test.cpp testSubBookChanges()

// TestSubscribeConformanceBookChangesStream verifies that subscribing to the
// per-ledger book_changes aggregate stream works correctly.
func TestSubscribeConformanceBookChangesStream(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubBookChanges},
	}

	err := sm.HandleSubscribe(conn, request, true)
	require.Nil(t, err, "Subscribe to book_changes stream should succeed")

	_, exists := conn.Subscriptions[types.SubBookChanges]
	assert.True(t, exists, "book_changes subscription should be recorded")

	// Broadcast to book_changes and verify delivery
	msg := []byte(`{"type":"bookChanges","changes":[]}`)
	sm.BroadcastToStream(types.SubBookChanges, msg, nil)

	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, msg, received)
	default:
		t.Fatal("Expected to receive book_changes broadcast")
	}

	// rippled's doUnsubscribe has no book_changes branch (Unsubscribe.cpp:
	// 61-110), so unsubscribing it is rpcSTREAM_MALFORMED and the stream
	// only drops when the connection closes. Mirror that quirk.
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubBookChanges},
	}, true)
	require.NotNil(t, err, "book_changes is not unsubscribable in rippled")
	assert.Equal(t, types.RpcSTREAM_MALFORMED, err.Code)
	assert.Equal(t, "malformedStream", err.ErrorString)

	_, exists = conn.Subscriptions[types.SubBookChanges]
	assert.True(t, exists, "book_changes subscription should remain")
}

// Concurrent Safety Tests
// Subscription management must be safe for concurrent access since multiple
// WebSocket connections will subscribe/unsubscribe simultaneously.

// TestSubscribeConformanceConcurrentAccess tests that concurrent subscribe and
// unsubscribe operations do not cause data races or panics.
func TestSubscribeConformanceConcurrentAccess(t *testing.T) {
	sm := newTestSubscriptionManager()

	const numConns = 10
	conns := make([]*types.Connection, numConns)
	for i := range numConns {
		conns[i] = newTestConnection(string(rune('A' + i)))
		sm.AddConnection(conns[i])
	}

	var wg sync.WaitGroup

	// Concurrently subscribe all connections to ledger stream
	for i := range numConns {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sm.HandleSubscribe(conns[idx], types.SubscriptionRequest{
				Streams: []types.SubscriptionType{types.SubLedger},
			}, true)
		}(i)
	}
	wg.Wait()

	// Verify all are subscribed
	for i := range numConns {
		_, exists := conns[i].Subscriptions[types.SubLedger]
		assert.True(t, exists, "Connection %d should be subscribed to ledger", i)
	}

	// Concurrently unsubscribe half and broadcast
	for i := range numConns {
		wg.Add(1)
		if i%2 == 0 {
			go func(idx int) {
				defer wg.Done()
				sm.HandleUnsubscribe(conns[idx], types.SubscriptionRequest{
					Streams: []types.SubscriptionType{types.SubLedger},
				}, true)
			}(i)
		} else {
			go func(idx int) {
				defer wg.Done()
				sm.BroadcastToStream(types.SubLedger, []byte(`{"test":true}`), nil)
			}(i)
		}
	}
	wg.Wait()

	// Cleanup
	for i := range numConns {
		sm.RemoveConnection(conns[i].ID)
	}
}

// Unsubscribe From Invalid Stream Tests
// Based on rippled Subscribe_test.cpp testSubErrors(false) - unsubscribe also
// validates stream names the same way subscribe does.

// TestSubscribeConformanceUnsubscribeInvalidStream verifies that unsubscribing
// from an invalid stream name returns rpcSTREAM_MALFORMED, like rippled
// Unsubscribe.cpp:106-109.
func TestSubscribeConformanceUnsubscribeInvalidStream(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// Subscribe to something valid first
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)

	// Unsubscribe from a made-up stream name
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{"not_a_stream"},
	}, true)
	require.NotNil(t, err, "Unsubscribing from an unknown stream should fail")
	assert.Equal(t, types.RpcSTREAM_MALFORMED, err.Code)
	assert.Equal(t, "malformedStream", err.ErrorString)
	assert.Equal(t, "Stream malformed.", err.Message)

	// Original subscription should remain
	_, exists := conn.Subscriptions[types.SubLedger]
	assert.True(t, exists, "Ledger subscription should remain intact")
}

// Connection Removal Cleans Up Subscriptions

// TestSubscribeConformanceConnectionRemovalCleansUp verifies that removing a
// connection cleans up its subscriptions so broadcast no longer targets it.
func TestSubscribeConformanceConnectionRemovalCleansUp(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubLedger))

	// Remove connection
	sm.RemoveConnection(conn.ID)
	assert.Equal(t, 0, sm.GetSubscriberCount(types.SubLedger),
		"Subscriber count should be 0 after connection removal")

	// Broadcast should not panic or send to removed connection
	sm.BroadcastToStream(types.SubLedger, []byte(`{"test":true}`), nil)

	select {
	case <-conn.SendChannel:
		t.Fatal("Should NOT receive broadcast after connection removal")
	default:
		// Expected
	}
}

// Subscribe Re-subscribe After Unsubscribe
// Based on rippled behavior: a connection can re-subscribe after unsubscribing

// TestSubscribeConformanceResubscribeAfterUnsubscribe verifies that a connection
// can subscribe again after unsubscribing.
func TestSubscribeConformanceResubscribeAfterUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	req := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}

	// Subscribe
	err := sm.HandleSubscribe(conn, req, true)
	require.Nil(t, err)
	assert.Contains(t, conn.Subscriptions, types.SubLedger)

	// Unsubscribe
	err = sm.HandleUnsubscribe(conn, req, true)
	require.Nil(t, err)
	assert.NotContains(t, conn.Subscriptions, types.SubLedger)

	// Re-subscribe
	err = sm.HandleSubscribe(conn, req, true)
	require.Nil(t, err)
	assert.Contains(t, conn.Subscriptions, types.SubLedger)

	// Verify messages are delivered again
	msg := []byte(`{"type":"ledgerClosed","ledger_index":300}`)
	sm.BroadcastToStream(types.SubLedger, msg, nil)
	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, msg, received)
	default:
		t.Fatal("Expected to receive message after re-subscribing")
	}
}

// Unsubscribe All Streams At Once

// TestSubscribeConformanceUnsubscribeAllStreams verifies that unsubscribing from
// multiple streams in a single request removes all of them.
func TestSubscribeConformanceUnsubscribeAllStreams(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	// Subscribe to multiple streams
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{
			types.SubLedger,
			types.SubTransactions,
			types.SubValidations,
			types.SubManifests,
		},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 4, len(conn.Subscriptions))

	// Unsubscribe from all at once
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{
			types.SubLedger,
			types.SubTransactions,
			types.SubValidations,
			types.SubManifests,
		},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 0, len(conn.Subscriptions),
		"All subscriptions should be removed")
}

// Mixed Subscribe and Unsubscribe in Single Request
// Based on rippled: unsubscribe from some streams while keeping others

// TestSubscribeConformanceSelectiveUnsubscribe verifies selective unsubscription
// while keeping other subscription types intact.
func TestSubscribeConformanceSelectiveUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Subscribe to streams and accounts
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger, types.SubTransactions},
		Accounts: []string{alice},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 3, len(conn.Subscriptions)) // ledger, transactions, accounts

	// Unsubscribe from transactions stream only
	err = sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}, true)
	require.Nil(t, err)

	// Ledger and accounts should remain
	assert.Contains(t, conn.Subscriptions, types.SubLedger)
	assert.NotContains(t, conn.Subscriptions, types.SubTransactions)
	assert.Contains(t, conn.Subscriptions, types.SubAccounts)

	// Verify ledger broadcast still works
	ledgerMsg := []byte(`{"type":"ledgerClosed"}`)
	sm.BroadcastToStream(types.SubLedger, ledgerMsg, nil)
	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, ledgerMsg, received)
	default:
		t.Fatal("Ledger broadcast should still work")
	}

	// Verify account broadcast still works
	acctMsg := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccounts(acctMsg, []string{alice})
	select {
	case received := <-conn.SendChannel:
		assert.Equal(t, acctMsg, received)
	default:
		t.Fatal("Account broadcast should still work")
	}

	// Verify transactions broadcast does NOT reach conn
	txMsg := []byte(`{"type":"transaction"}`)
	sm.BroadcastToStream(types.SubTransactions, txMsg, nil)
	select {
	case <-conn.SendChannel:
		t.Fatal("Should NOT receive transactions broadcast after unsubscribing")
	default:
	}
}

func mustBook(t *testing.T, pays, gets map[string]any) types.BookRequest {
	t.Helper()
	takerPays, err := json.Marshal(pays)
	require.NoError(t, err)
	takerGets, err := json.Marshal(gets)
	require.NoError(t, err)
	return types.BookRequest{TakerPays: takerPays, TakerGets: takerGets}
}

// TestSubscribeConformanceBadTaker verifies an unparseable book taker is
// rpcBAD_ISSUER (Subscribe.cpp:301-305).
func TestSubscribeConformanceBadTaker(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	book := mustBook(t,
		map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		map[string]any{"currency": "XRP"})
	book.Taker = "not_an_account"

	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Books: []types.BookRequest{book},
	}, true)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcBAD_ISSUER, err.Code)
	assert.Equal(t, "badIssuer", err.ErrorString)
	assert.Equal(t, "Issuer account malformed.", err.Message)
}

// TestSubscribeConformanceDomain verifies the book domain parse
// (Subscribe.cpp:308-315): a non-hex domain is rpcDOMAIN_MALFORMED, a
// valid uint256 hex is accepted and carried onto the stored book (and its
// both:true reverse).
func TestSubscribeConformanceDomain(t *testing.T) {
	const validDomain = "00000000000000000000000000000000000000000000000000000000000000AB"

	t.Run("malformed domain", func(t *testing.T) {
		sm := newTestSubscriptionManager()
		conn := newTestConnection("test-conn-1")
		sm.AddConnection(conn)
		defer sm.RemoveConnection(conn.ID)

		book := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		book.Domain = "not-hex"

		err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcDOMAIN_MALFORMED, err.Code)
		assert.Equal(t, "domainMalformed", err.ErrorString)
		assert.Equal(t, "Domain is malformed.", err.Message)
	})

	t.Run("valid domain accepted and kept on both sides", func(t *testing.T) {
		sm := newTestSubscriptionManager()
		conn := newTestConnection("test-conn-1")
		sm.AddConnection(conn)
		defer sm.RemoveConnection(conn.ID)

		book := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		book.Domain = validDomain
		book.Both = true

		err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.Nil(t, err)

		config := conn.Subscriptions[types.SubBook]
		require.Len(t, config.Books, 2)
		assert.Equal(t, validDomain, config.Books[0].Domain)
		assert.Equal(t, validDomain, config.Books[1].Domain)
	})
}

// TestUnsubscribeConformanceErrorEnvelopes verifies the unsubscribe path
// validates accounts and books the same way subscribe does
// (Unsubscribe.cpp:113-245), minus the taker field it does not carry.
func TestUnsubscribeConformanceErrorEnvelopes(t *testing.T) {
	newConn := func(t *testing.T) (*subscription.Manager, *types.Connection) {
		t.Helper()
		sm := newTestSubscriptionManager()
		conn := newTestConnection("test-conn-1")
		sm.AddConnection(conn)
		t.Cleanup(func() { sm.RemoveConnection(conn.ID) })
		return sm, conn
	}

	t.Run("malformed account", func(t *testing.T) {
		sm, conn := newConn(t)
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			Accounts: []string{"not_an_account"},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
		assert.Equal(t, "actMalformed", err.ErrorString)
		assert.Equal(t, "Account malformed.", err.Message)
	})

	t.Run("malformed accounts_proposed", func(t *testing.T) {
		sm, conn := newConn(t)
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			AccountsProposed: []string{"not_an_account"},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
		assert.Equal(t, "actMalformed", err.ErrorString)
	})

	t.Run("book with bad taker_pays currency", func(t *testing.T) {
		sm, conn := newConn(t)
		book := mustBook(t,
			map[string]any{"currency": "USDX"},
			map[string]any{"currency": "XRP"})
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcSRC_CUR_MALFORMED, err.Code)
		assert.Equal(t, "srcCurMalformed", err.ErrorString)
	})

	t.Run("same-asset book is badMarket", func(t *testing.T) {
		sm, conn := newConn(t)
		book := mustBook(t,
			map[string]any{"currency": "XRP"},
			map[string]any{"currency": "XRP"})
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcBAD_MARKET, err.Code)
		assert.Equal(t, "badMarket", err.ErrorString)
	})

	t.Run("malformed domain", func(t *testing.T) {
		sm, conn := newConn(t)
		book := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		book.Domain = "zz"
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.NotNil(t, err)
		assert.Equal(t, types.RpcDOMAIN_MALFORMED, err.Code)
		assert.Equal(t, "domainMalformed", err.ErrorString)
	})

	t.Run("taker is not validated on unsubscribe", func(t *testing.T) {
		// Unsubscribe.cpp has no taker handling; a malformed taker must
		// not fail the request.
		sm, conn := newConn(t)
		subBook := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{subBook},
		}, true))

		unsubBook := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		unsubBook.Taker = "not_an_account"
		err := sm.HandleUnsubscribe(conn, types.SubscriptionRequest{
			Books: []types.BookRequest{unsubBook},
		}, true)
		require.Nil(t, err)
		assert.NotContains(t, conn.Subscriptions, types.SubBook)
	})
}

// TestSubscribeConformanceStructuralCheckFirst pins rippled's evaluation
// order: both sides' structure is checked before either side's currency
// is parsed (Subscribe.cpp:238-242), so a bad taker_pays currency
// combined with a missing taker_gets reports rpcINVALID_PARAMS, not
// srcCurMalformed.
func TestSubscribeConformanceStructuralCheckFirst(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	takerPays, _ := json.Marshal(map[string]any{"currency": "USDX"})
	err := sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Books: []types.BookRequest{{TakerPays: takerPays}},
	}, true)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
	assert.Equal(t, "Invalid parameters.", err.Message)
}

// TestSubscribeConformanceIncrementalAccounts verifies a second account
// subscribe accumulates onto the existing set rather than replacing it (H1):
// rippled's subAccount inserts into the connection's listener set per call,
// so the first subscribe's account must keep receiving broadcasts and a
// re-subscribe must not duplicate it.
func TestSubscribeConformanceIncrementalAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bob := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Accounts: []string{alice}}, true))
	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Accounts: []string{bob}}, true))

	for _, acc := range []string{alice, bob} {
		msg := []byte(`{"account":"` + acc + `"}`)
		sm.BroadcastToAccounts(msg, []string{acc})
		select {
		case got := <-conn.SendChannel:
			assert.Equal(t, msg, got)
		default:
			t.Fatalf("account %s should still receive broadcasts after an incremental subscribe", acc)
		}
	}

	// Re-subscribing an existing account must not duplicate it.
	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Accounts: []string{alice}}, true))
	assert.ElementsMatch(t, []string{alice, bob}, conn.Subscriptions[types.SubAccounts].Accounts)
}

// TestSubscribeConformanceIncrementalAccountsProposed is the accounts_proposed
// analogue of the accounts merge (H1): the second subscribe previously
// overwrote the first outright.
func TestSubscribeConformanceIncrementalAccountsProposed(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bob := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{AccountsProposed: []string{alice}}, true))
	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{AccountsProposed: []string{bob}}, true))

	for _, acc := range []string{alice, bob} {
		msg := []byte(`{"account":"` + acc + `"}`)
		sm.BroadcastToAccountsProposed(msg, []string{acc})
		select {
		case got := <-conn.SendChannel:
			assert.Equal(t, msg, got)
		default:
			t.Fatalf("accounts_proposed %s should still receive broadcasts after an incremental subscribe", acc)
		}
	}
}

// TestSubscribeConformanceIncrementalBooks verifies a second book subscribe
// accumulates onto the existing set rather than wiping it (H2): rippled calls
// subBook once per entry, so an earlier book must keep matching broadcasts.
func TestSubscribeConformanceIncrementalBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bookA := mustBook(t,
		map[string]any{"currency": "USD", "issuer": issuer},
		map[string]any{"currency": "XRP"})
	bookB := mustBook(t,
		map[string]any{"currency": "EUR", "issuer": issuer},
		map[string]any{"currency": "XRP"})

	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Books: []types.BookRequest{bookA}}, true))
	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Books: []types.BookRequest{bookB}}, true))

	xrp := types.CurrencySpec{Currency: "XRP"}
	for _, pays := range []types.CurrencySpec{
		{Currency: "USD", Issuer: issuer},
		{Currency: "EUR", Issuer: issuer},
	} {
		msg := []byte(`{"book":"` + pays.Currency + `"}`)
		sm.BroadcastToOrderBook(msg, xrp, pays)
		select {
		case got := <-conn.SendChannel:
			assert.Equal(t, msg, got)
		default:
			t.Fatalf("book %s/XRP should still match after an incremental subscribe", pays.Currency)
		}
	}
}

// TestUnsubscribeConformancePerBook verifies unsubscribe removes only the named
// book, leaving the connection's other book subscriptions intact (H2): rippled
// calls unsubBook per entry rather than dropping the whole set.
func TestUnsubscribeConformancePerBook(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	defer sm.RemoveConnection(conn.ID)

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bookA := mustBook(t,
		map[string]any{"currency": "USD", "issuer": issuer},
		map[string]any{"currency": "XRP"})
	bookB := mustBook(t,
		map[string]any{"currency": "EUR", "issuer": issuer},
		map[string]any{"currency": "XRP"})

	require.Nil(t, sm.HandleSubscribe(conn, types.SubscriptionRequest{Books: []types.BookRequest{bookA, bookB}}, true))
	require.Nil(t, sm.HandleUnsubscribe(conn, types.SubscriptionRequest{Books: []types.BookRequest{bookA}}, true))

	xrp := types.CurrencySpec{Currency: "XRP"}

	// Book A no longer matches.
	sm.BroadcastToOrderBook([]byte(`{"book":"USD"}`), xrp, types.CurrencySpec{Currency: "USD", Issuer: issuer})
	select {
	case <-conn.SendChannel:
		t.Fatal("book USD/XRP should not match after unsubscribe")
	default:
	}

	// Book B still matches.
	msg := []byte(`{"book":"EUR"}`)
	sm.BroadcastToOrderBook(msg, xrp, types.CurrencySpec{Currency: "EUR", Issuer: issuer})
	select {
	case got := <-conn.SendChannel:
		assert.Equal(t, msg, got)
	default:
		t.Fatal("book EUR/XRP should still match after unsubscribing only USD/XRP")
	}
}
