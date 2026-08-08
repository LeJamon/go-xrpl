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
	_ = testRegistration(t, sm, conn)

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

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "same asset on both sides must be rejected")
	assert.Equal(t, types.RpcBAD_MARKET, err.Code)
	assert.Equal(t, "badMarket", err.ErrorString)
	assert.Equal(t, "No such market.", err.Message)
}

// TestSubscribeConformanceBadMarketXRP tests badMarket with XRP on both sides.
func TestSubscribeConformanceBadMarketXRP(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

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

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
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
	_ = testRegistration(t, sm, conn)

	// Subscribe to ledger stream
	subscribeReq := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeReq, true)
	require.Nil(t, err)

	// Broadcast should reach the connection
	msg1 := []byte(`{"type":"ledgerClosed","ledger_index":100}`)
	sm.BroadcastToStream(types.SubLedger, msg1)

	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg1, received, "Should receive message while subscribed")
	default:
		t.Fatal("Expected to receive broadcast message while subscribed")
	}

	// Now unsubscribe
	unsubscribeReq := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeReq)
	require.Nil(t, err)

	// Broadcast again - should NOT be received
	msg2 := []byte(`{"type":"ledgerClosed","ledger_index":101}`)
	sm.BroadcastToStream(types.SubLedger, msg2)

	select {
	case <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Subscribe to alice's account
	subscribeReq := types.SubscriptionRequest{
		Accounts: []string{alice},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeReq, true)
	require.Nil(t, err)

	// Broadcast for alice - should reach connection
	msg1 := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccountsVersioned(msg1, msg1, []string{alice})

	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg1, received)
	default:
		t.Fatal("Expected to receive message for subscribed account")
	}

	// Unsubscribe from alice
	unsubscribeReq := types.SubscriptionRequest{
		Accounts: []string{alice},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeReq)
	require.Nil(t, err)

	// Broadcast for alice again - should NOT be received
	msg2 := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh","seq":2}`)
	sm.BroadcastToAccountsVersioned(msg2, msg2, []string{alice})

	select {
	case <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn1)
	_ = testRegistration(t, sm, conn2)

	// Both subscribe to ledger
	req := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn1), req, true))
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn2), req, true))

	// conn1 unsubscribes
	require.Nil(t, sm.HandleUnsubscribe(testRegistration(t, sm, conn1), req))

	// Broadcast
	msg := []byte(`{"type":"ledgerClosed","ledger_index":200}`)
	sm.BroadcastToStream(types.SubLedger, msg)

	// conn1 should NOT receive
	select {
	case <-conn1.Outbound():
		t.Fatal("conn1 should NOT receive after unsubscribing")
	default:
	}

	// conn2 should still receive
	select {
	case received := <-conn2.Outbound():
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
	_ = testRegistration(t, sm, conn)

	// Step 1: Subscribe to transactions
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}, true)
	require.Nil(t, err)
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))

	// Step 2: Receive a transaction broadcast
	txMsg := []byte(`{"type":"transaction","tx":{"TransactionType":"Payment"}}`)
	sm.BroadcastToStream(types.SubTransactions, txMsg)
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, txMsg, received)
	default:
		t.Fatal("Expected transaction message")
	}

	// Step 3: Unsubscribe from transactions
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	})
	require.Nil(t, err)
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))

	// Step 4: Subscribe to accounts
	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	err = sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Accounts: []string{alice},
	}, true)
	require.Nil(t, err)
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubAccounts))

	// Step 5: Transaction for a different account should NOT be received
	otherMsg := []byte(`{"type":"transaction","account":"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"}`)
	sm.BroadcastToAccountsVersioned(otherMsg, otherMsg, []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"})
	select {
	case <-conn.Outbound():
		t.Fatal("Should not receive message for unsubscribed account")
	default:
	}

	// Step 6: Transaction for alice should be received
	aliceMsg := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccountsVersioned(aliceMsg, aliceMsg, []string{alice})
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, aliceMsg, received)
	default:
		t.Fatal("Expected message for subscribed account")
	}

	// Step 7: Unsubscribe from accounts
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Accounts: []string{alice},
	})
	require.Nil(t, err)
}

// Accounts Proposed Unsubscribe Tests
// Based on rippled Subscribe_test.cpp testSubErrors() for accounts_proposed

// TestSubscribeConformanceAccountsProposedUnsubscribe tests the full lifecycle
// of subscribing and unsubscribing from accounts_proposed.
func TestSubscribeConformanceAccountsProposedUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	// Subscribe to accounts_proposed
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		AccountsProposed: accounts,
	}, true)
	require.Nil(t, err)

	// Verify subscription was recorded
	registration := testRegistration(t, sm, conn)
	require.True(t, registration.Snapshot().Has(types.SubAccountsProposed))
	assert.Len(t, registration.Snapshot().Accounts(types.SubAccountsProposed), 2)
	require.Nil(t, sm.HandleUnsubscribe(registration, types.SubscriptionRequest{AccountsProposed: accounts}))
	assert.False(t, registration.Snapshot().Has(types.SubAccountsProposed))
}

// Empty Subscription Request Tests
// Based on rippled: sending subscribe with no params still returns success

// TestSubscribeConformanceEmptyRequest verifies that subscribing with an empty
// request (no streams, accounts, or books) succeeds.
func TestSubscribeConformanceEmptyRequest(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{}, true)
	require.Nil(t, err, "Empty subscribe request should succeed")
	assert.Equal(t, 0, testRegistration(t, sm, conn).Snapshot().ItemCount(), "No subscriptions should be added")
}

// TestSubscribeConformanceEmptyUnsubscribeRequest verifies that unsubscribing
// with an empty request succeeds.
func TestSubscribeConformanceEmptyUnsubscribeRequest(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// First subscribe to something
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().ItemCount())

	// Empty unsubscribe should not remove anything
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{})
	require.Nil(t, err, "Empty unsubscribe request should succeed")
	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().ItemCount(), "Existing subscriptions should remain")
}

// book_changes Stream Tests
// Based on rippled Subscribe_test.cpp testSubBookChanges()

// TestSubscribeConformanceBookChangesStream verifies that subscribing to the
// per-ledger book_changes aggregate stream works correctly.
func TestSubscribeConformanceBookChangesStream(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubBookChanges},
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Subscribe to book_changes stream should succeed")

	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBookChanges))

	// Broadcast to book_changes and verify delivery
	msg := []byte(`{"type":"bookChanges","changes":[]}`)
	sm.BroadcastToStream(types.SubBookChanges, msg)

	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg, received)
	default:
		t.Fatal("Expected to receive book_changes broadcast")
	}

	// rippled's doUnsubscribe has no book_changes branch (Unsubscribe.cpp:
	// 61-110), so unsubscribing it is rpcSTREAM_MALFORMED and the stream
	// only drops when the connection closes. Mirror that quirk.
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubBookChanges},
	})
	require.NotNil(t, err, "book_changes is not unsubscribable in rippled")
	assert.Equal(t, types.RpcSTREAM_MALFORMED, err.Code)
	assert.Equal(t, "malformedStream", err.ErrorString)

	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBookChanges))
}

// Concurrent Safety Tests
// Subscription management must be safe for concurrent access since multiple
// WebSocket connections will subscribe/unsubscribe simultaneously.

// TestSubscribeConformanceConcurrentAccess tests that concurrent subscribe and
// unsubscribe operations do not cause data races or panics.
func TestSubscribeConformanceConcurrentAccess(t *testing.T) {
	sm := newTestSubscriptionManager()

	const numConns = 10
	conns := make([]*subscription.Connection, numConns)
	registrations := make([]*subscription.Registration, numConns)
	for i := range numConns {
		conns[i] = newTestConnection(string(rune('A' + i)))
		registrations[i] = testRegistration(t, sm, conns[i])
	}

	var wg sync.WaitGroup

	// Concurrently subscribe all connections to ledger stream
	for i := range numConns {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			sm.HandleSubscribe(registrations[idx], types.SubscriptionRequest{
				Streams: []types.SubscriptionType{types.SubLedger},
			}, true)
		}(i)
	}
	wg.Wait()

	// Verify all are subscribed
	for i := range numConns {
		assert.True(t, registrations[i].Snapshot().Has(types.SubLedger), "Connection %d should be subscribed to ledger", i)
	}

	// Concurrently unsubscribe half and broadcast
	for i := range numConns {
		wg.Add(1)
		if i%2 == 0 {
			go func(idx int) {
				defer wg.Done()
				sm.HandleUnsubscribe(registrations[idx], types.SubscriptionRequest{
					Streams: []types.SubscriptionType{types.SubLedger},
				})
			}(i)
		} else {
			go func(idx int) {
				defer wg.Done()
				sm.BroadcastToStream(types.SubLedger, []byte(`{"test":true}`))
			}(i)
		}
	}
	wg.Wait()

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
	_ = testRegistration(t, sm, conn)

	// Subscribe to something valid first
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)

	// Unsubscribe from a made-up stream name
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{"not_a_stream"},
	})
	require.NotNil(t, err, "Unsubscribing from an unknown stream should fail")
	assert.Equal(t, types.RpcSTREAM_MALFORMED, err.Code)
	assert.Equal(t, "malformedStream", err.ErrorString)
	assert.Equal(t, "Stream malformed.", err.Message)

	// Original subscription should remain
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
}

// Connection Removal Cleans Up Subscriptions

// TestSubscribeConformanceConnectionRemovalCleansUp verifies that removing a
// connection cleans up its subscriptions so broadcast no longer targets it.
func TestSubscribeConformanceConnectionRemovalCleansUp(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	registration := testRegistration(t, sm, conn)

	// Subscribe
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubLedger))

	// Remove connection
	require.True(t, sm.Detach(registration))
	assert.Equal(t, 0, sm.GetSubscriberCount(types.SubLedger),
		"Subscriber count should be 0 after connection removal")

	// Broadcast should not panic or send to removed connection
	sm.BroadcastToStream(types.SubLedger, []byte(`{"test":true}`))

	select {
	case <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn)

	req := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}

	// Subscribe
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), req, true)
	require.Nil(t, err)
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))

	// Unsubscribe
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), req)
	require.Nil(t, err)
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))

	// Re-subscribe
	err = sm.HandleSubscribe(testRegistration(t, sm, conn), req, true)
	require.Nil(t, err)
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))

	// Verify messages are delivered again
	msg := []byte(`{"type":"ledgerClosed","ledger_index":300}`)
	sm.BroadcastToStream(types.SubLedger, msg)
	select {
	case received := <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn)

	// Subscribe to multiple streams
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{
			types.SubLedger,
			types.SubTransactions,
			types.SubValidations,
			types.SubManifests,
		},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 4, testRegistration(t, sm, conn).Snapshot().ItemCount())

	// Unsubscribe from all at once
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{
			types.SubLedger,
			types.SubTransactions,
			types.SubValidations,
			types.SubManifests,
		},
	})
	require.Nil(t, err)
	assert.Equal(t, 0, testRegistration(t, sm, conn).Snapshot().ItemCount(),
		"All subscriptions should be removed")
}

// Mixed Subscribe and Unsubscribe in Single Request
// Based on rippled: unsubscribe from some streams while keeping others

// TestSubscribeConformanceSelectiveUnsubscribe verifies selective unsubscription
// while keeping other subscription types intact.
func TestSubscribeConformanceSelectiveUnsubscribe(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"

	// Subscribe to streams and accounts
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger, types.SubTransactions},
		Accounts: []string{alice},
	}, true)
	require.Nil(t, err)
	assert.Equal(t, 3, testRegistration(t, sm, conn).Snapshot().ItemCount()) // ledger, transactions, accounts

	// Unsubscribe from transactions stream only
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	})
	require.Nil(t, err)

	// Ledger and accounts should remain
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubAccounts))

	// Verify ledger broadcast still works
	ledgerMsg := []byte(`{"type":"ledgerClosed"}`)
	sm.BroadcastToStream(types.SubLedger, ledgerMsg)
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, ledgerMsg, received)
	default:
		t.Fatal("Ledger broadcast should still work")
	}

	// Verify account broadcast still works
	acctMsg := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccountsVersioned(acctMsg, acctMsg, []string{alice})
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, acctMsg, received)
	default:
		t.Fatal("Account broadcast should still work")
	}

	// Verify transactions broadcast does NOT reach conn
	txMsg := []byte(`{"type":"transaction"}`)
	sm.BroadcastToStream(types.SubTransactions, txMsg)
	select {
	case <-conn.Outbound():
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
// rpcACT_MALFORMED (rippled 3.2.0 #6529 changed this from rpcBAD_ISSUER).
func TestSubscribeConformanceBadTaker(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	book := mustBook(t,
		map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		map[string]any{"currency": "XRP"})
	book.Taker = "not_an_account"

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Books: []types.BookRequest{book},
	}, true)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
	assert.Equal(t, "actMalformed", err.ErrorString)
	assert.Equal(t, "Account malformed.", err.Message)
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
		_ = testRegistration(t, sm, conn)

		book := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		book.Domain = "not-hex"

		err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
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
		_ = testRegistration(t, sm, conn)

		book := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		book.Domain = validDomain
		book.Both = true

		err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		}, true)
		require.Nil(t, err)

		require.Equal(t, 2, testRegistration(t, sm, conn).Snapshot().BookCount())
	})
}

// TestUnsubscribeConformanceErrorEnvelopes verifies the unsubscribe path
// validates accounts and books the same way subscribe does
// (Unsubscribe.cpp:113-245), minus the taker field it does not carry.
func TestUnsubscribeConformanceErrorEnvelopes(t *testing.T) {
	newConn := func(t *testing.T) (*subscription.Manager, *subscription.Connection) {
		t.Helper()
		sm := newTestSubscriptionManager()
		conn := newTestConnection("test-conn-1")
		_ = testRegistration(t, sm, conn)
		return sm, conn
	}

	t.Run("malformed account", func(t *testing.T) {
		sm, conn := newConn(t)
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Accounts: []string{"not_an_account"},
		})
		require.NotNil(t, err)
		assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
		assert.Equal(t, "actMalformed", err.ErrorString)
		assert.Equal(t, "Account malformed.", err.Message)
	})

	t.Run("malformed accounts_proposed", func(t *testing.T) {
		sm, conn := newConn(t)
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			AccountsProposed: []string{"not_an_account"},
		})
		require.NotNil(t, err)
		assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
		assert.Equal(t, "actMalformed", err.ErrorString)
	})

	t.Run("book with bad taker_pays currency", func(t *testing.T) {
		sm, conn := newConn(t)
		book := mustBook(t,
			map[string]any{"currency": "USDX"},
			map[string]any{"currency": "XRP"})
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		})
		require.NotNil(t, err)
		assert.Equal(t, types.RpcSRC_CUR_MALFORMED, err.Code)
		assert.Equal(t, "srcCurMalformed", err.ErrorString)
	})

	t.Run("same-asset book is badMarket", func(t *testing.T) {
		sm, conn := newConn(t)
		book := mustBook(t,
			map[string]any{"currency": "XRP"},
			map[string]any{"currency": "XRP"})
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		})
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
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{book},
		})
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
		require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{subBook},
		}, true))

		unsubBook := mustBook(t,
			map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			map[string]any{"currency": "XRP"})
		unsubBook.Taker = "not_an_account"
		err := sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
			Books: []types.BookRequest{unsubBook},
		})
		require.Nil(t, err)
		assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBook))
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
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{"currency": "USDX"})
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
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
	_ = testRegistration(t, sm, conn)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bob := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Accounts: []string{alice}}, true))
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Accounts: []string{bob}}, true))

	for _, acc := range []string{alice, bob} {
		msg := []byte(`{"account":"` + acc + `"}`)
		sm.BroadcastToAccountsVersioned(msg, msg, []string{acc})
		select {
		case got := <-conn.Outbound():
			assert.Equal(t, msg, got)
		default:
			t.Fatalf("account %s should still receive broadcasts after an incremental subscribe", acc)
		}
	}

	// Re-subscribing an existing account must not duplicate it.
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Accounts: []string{alice}}, true))
	assert.ElementsMatch(t, []string{alice, bob}, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts))
}

// TestSubscribeConformanceIncrementalAccountsProposed is the accounts_proposed
// analogue of the accounts merge (H1): a second subscribe must accumulate onto
// the existing set rather than overwrite it.
func TestSubscribeConformanceIncrementalAccountsProposed(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	alice := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bob := "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"

	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{AccountsProposed: []string{alice}}, true))
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{AccountsProposed: []string{bob}}, true))

	for _, acc := range []string{alice, bob} {
		msg := []byte(`{"account":"` + acc + `"}`)
		sm.BroadcastToAccountsProposedVersioned(msg, msg, []string{acc})
		select {
		case got := <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn)

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bookA := mustBook(t,
		map[string]any{"currency": "USD", "issuer": issuer},
		map[string]any{"currency": "XRP"})
	bookB := mustBook(t,
		map[string]any{"currency": "EUR", "issuer": issuer},
		map[string]any{"currency": "XRP"})

	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Books: []types.BookRequest{bookA}}, true))
	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Books: []types.BookRequest{bookB}}, true))

	xrp := types.CurrencySpec{Currency: "XRP"}
	for _, pays := range []types.CurrencySpec{
		{Currency: "USD", Issuer: issuer},
		{Currency: "EUR", Issuer: issuer},
	} {
		msg := []byte(`{"book":"` + pays.Currency + `"}`)
		sm.BroadcastToOrderBooksVersioned(msg, msg, []types.OrderBookSpec{{TakerGets: xrp, TakerPays: pays}})
		select {
		case got := <-conn.Outbound():
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
	_ = testRegistration(t, sm, conn)

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	bookA := mustBook(t,
		map[string]any{"currency": "USD", "issuer": issuer},
		map[string]any{"currency": "XRP"})
	bookB := mustBook(t,
		map[string]any{"currency": "EUR", "issuer": issuer},
		map[string]any{"currency": "XRP"})

	require.Nil(t, sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Books: []types.BookRequest{bookA, bookB}}, true))
	require.Nil(t, sm.HandleUnsubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{Books: []types.BookRequest{bookA}}))

	xrp := types.CurrencySpec{Currency: "XRP"}

	// Book A no longer matches.
	usdMsg := []byte(`{"book":"USD"}`)
	sm.BroadcastToOrderBooksVersioned(usdMsg, usdMsg, []types.OrderBookSpec{{TakerGets: xrp, TakerPays: types.CurrencySpec{Currency: "USD", Issuer: issuer}}})
	select {
	case <-conn.Outbound():
		t.Fatal("book USD/XRP should not match after unsubscribe")
	default:
	}

	// Book B still matches.
	msg := []byte(`{"book":"EUR"}`)
	sm.BroadcastToOrderBooksVersioned(msg, msg, []types.OrderBookSpec{{TakerGets: xrp, TakerPays: types.CurrencySpec{Currency: "EUR", Issuer: issuer}}})
	select {
	case got := <-conn.Outbound():
		assert.Equal(t, msg, got)
	default:
		t.Fatal("book EUR/XRP should still match after unsubscribing only USD/XRP")
	}
}
