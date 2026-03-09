package rpc

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/goXRPLd/internal/rpc/handlers"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSubscriptionManager creates a new SubscriptionManager for testing
func newTestSubscriptionManager() *types.SubscriptionManager {
	return &types.SubscriptionManager{
		Connections: make(map[string]*types.Connection),
	}
}

// newTestConnection creates a new Connection for testing
func newTestConnection(id string) *types.Connection {
	return &types.Connection{
		ID:            id,
		Subscriptions: make(map[types.SubscriptionType]types.SubscriptionConfig),
		SendChannel:   make(chan []byte, 100),
		CloseChannel:  make(chan struct{}),
	}
}

// =============================================================================
// Stream Subscription Tests
// Based on rippled Subscribe_test.cpp testServer(), testLedger(), testTransactions_APIv1()
// =============================================================================

// TestSubscribeStreamTypes tests subscribing to various stream types
func TestSubscribeStreamTypes(t *testing.T) {
	tests := []struct {
		name         string
		streamType   types.SubscriptionType
		streamString string
		expectError  bool
	}{
		{
			name:         "ledger stream - subscribe to ledger close events",
			streamType:   types.SubLedger,
			streamString: "ledger",
			expectError:  false,
		},
		{
			name:         "transactions stream - subscribe to all transactions",
			streamType:   types.SubTransactions,
			streamString: "transactions",
			expectError:  false,
		},
		{
			name:         "transactions_proposed stream - subscribe to proposed transactions",
			streamType:   types.SubscriptionType("transactions_proposed"),
			streamString: "transactions_proposed",
			expectError:  true, // Not in validStreams map
		},
		{
			name:         "validations stream - subscribe to validation messages",
			streamType:   types.SubValidations,
			streamString: "validations",
			expectError:  false,
		},
		{
			name:         "manifests stream - subscribe to manifest updates",
			streamType:   types.SubManifests,
			streamString: "manifests",
			expectError:  false,
		},
		{
			name:         "peer_status stream - subscribe to peer status changes",
			streamType:   types.SubPeerStatus,
			streamString: "peer_status",
			expectError:  false,
		},
		{
			name:         "consensus stream - subscribe to consensus events",
			streamType:   types.SubConsensus,
			streamString: "consensus",
			expectError:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			sm.AddConnection(conn)

			request := types.SubscriptionRequest{
				Streams: []types.SubscriptionType{tc.streamType},
			}

			err := sm.HandleSubscribe(conn, request)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for stream type: %s", tc.streamString)
				assert.Contains(t, err.Message, "Unknown stream type")
			} else {
				require.Nil(t, err, "Expected no error for stream type: %s", tc.streamString)

				// Verify subscription was recorded
				_, exists := conn.Subscriptions[tc.streamType]
				assert.True(t, exists, "Expected subscription to be recorded for stream: %s", tc.streamString)
			}

			// Cleanup
			sm.RemoveConnection(conn.ID)
		})
	}
}

// TestSubscribeMultipleStreams tests subscribing to multiple streams at once
func TestSubscribeMultipleStreams(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions, types.SubValidations},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for multiple valid streams")

	// Verify all subscriptions were recorded
	assert.Contains(t, conn.Subscriptions, types.SubLedger)
	assert.Contains(t, conn.Subscriptions, types.SubTransactions)
	assert.Contains(t, conn.Subscriptions, types.SubValidations)

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeInvalidStreamName tests subscribing to an invalid stream name
// Based on rippled Subscribe_test.cpp testSubErrors() for streams
func TestSubscribeInvalidStreamName(t *testing.T) {
	tests := []struct {
		name        string
		streamName  string
		expectError bool
	}{
		{
			name:        "invalid stream name - random string",
			streamName:  "not_a_stream",
			expectError: true,
		},
		{
			name:        "invalid stream name - empty",
			streamName:  "",
			expectError: true,
		},
		{
			name:        "invalid stream name - typo",
			streamName:  "ledgers", // should be "ledger"
			expectError: true,
		},
		{
			name:        "invalid stream name - uppercase",
			streamName:  "LEDGER",
			expectError: true,
		},
		{
			name:        "invalid stream name - mixed case",
			streamName:  "Ledger",
			expectError: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			sm.AddConnection(conn)

			request := types.SubscriptionRequest{
				Streams: []types.SubscriptionType{types.SubscriptionType(tc.streamName)},
			}

			err := sm.HandleSubscribe(conn, request)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for invalid stream: %s", tc.streamName)
				assert.Contains(t, err.Message, "Unknown stream type")
			}

			sm.RemoveConnection(conn.ID)
		})
	}
}

// =============================================================================
// Account Subscription Tests
// Based on rippled Subscribe_test.cpp testTransactions_APIv1() account subscription section
// =============================================================================

// TestSubscribeAccounts tests subscribing to specific accounts
func TestSubscribeAccounts(t *testing.T) {
	validAccounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", // Genesis account
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", // Bob
		"rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW", // Carol
	}

	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		Accounts: validAccounts,
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for valid accounts")

	// Verify subscription was recorded with all accounts
	config, exists := conn.Subscriptions[types.SubAccounts]
	require.True(t, exists, "Expected accounts subscription to be recorded")
	assert.Equal(t, len(validAccounts), len(config.Accounts))

	for _, acc := range validAccounts {
		assert.Contains(t, config.Accounts, acc)
	}

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeAccountsProposed tests subscribing to proposed transactions for accounts
func TestSubscribeAccountsProposed(t *testing.T) {
	validAccounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		AccountsProposed: validAccounts,
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for valid accounts_proposed")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeAccountInvalidFormat tests subscribing with invalid account formats
// Based on rippled Subscribe_test.cpp testSubErrors() for accounts
// Note: The current implementation uses a regex that allows 25-34 characters after 'r'
// and includes both uppercase and lowercase letters (except 0, O, I, l per base58)
func TestSubscribeAccountInvalidFormat(t *testing.T) {
	tests := []struct {
		name        string
		account     string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "invalid account - empty string",
			account:     "",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - very short",
			account:     "rHb9CJA",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - too long",
			account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyThExtraChars",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - wrong prefix",
			account:     "sHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - node public key format",
			account:     "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - numeric string",
			account:     "12345678901234567890123456789012345",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - hex string",
			account:     "0x1234567890ABCDEF1234567890ABCDEF12345678",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - special characters",
			account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdty!@",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
		{
			name:        "invalid account - contains forbidden char 0",
			account:     "rHb0CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expectError: true,
			errorMsg:    "Invalid account address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			sm.AddConnection(conn)

			request := types.SubscriptionRequest{
				Accounts: []string{tc.account},
			}

			err := sm.HandleSubscribe(conn, request)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for invalid account: %s", tc.account)
				assert.Contains(t, err.Message, tc.errorMsg)
			} else {
				require.Nil(t, err, "Expected no error for valid account: %s", tc.account)
			}

			sm.RemoveConnection(conn.ID)
		})
	}
}

// TestSubscribeAccountsProposedInvalidFormat tests invalid accounts_proposed
func TestSubscribeAccountsProposedInvalidFormat(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		AccountsProposed: []string{"invalid_account"},
	}

	err := sm.HandleSubscribe(conn, request)
	require.NotNil(t, err, "Expected error for invalid accounts_proposed")
	assert.Contains(t, err.Message, "Invalid account address")

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// Book Subscription Tests
// Based on rippled Subscribe_test.cpp testSubErrors() for books
// =============================================================================

// TestSubscribeBooks tests subscribing to order books with taker_gets/taker_pays
func TestSubscribeBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Valid book subscription: XRP for USD
	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]interface{}{
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

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for valid book subscription")

	// Verify subscription was recorded
	config, exists := conn.Subscriptions[types.SubOrderBooks]
	require.True(t, exists, "Expected book subscription to be recorded")
	assert.Equal(t, 1, len(config.Books))

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeBooksWithSnapshot tests the snapshot flag for initial order book state
func TestSubscribeBooksWithSnapshot(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				TakerGets: takerGets,
				Snapshot:  true,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for book subscription with snapshot")

	config := conn.Subscriptions[types.SubOrderBooks]
	assert.True(t, config.Books[0].Snapshot, "Snapshot flag should be true")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeBooksWithBoth tests the both flag for both sides of order book
func TestSubscribeBooksWithBoth(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				TakerGets: takerGets,
				Both:      true,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for book subscription with both")

	config := conn.Subscriptions[types.SubOrderBooks]
	assert.True(t, config.Books[0].Both, "Both flag should be true")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeBooksInvalidCurrency tests invalid currency in book specification
// Based on rippled Subscribe_test.cpp srcCurMalformed error
func TestSubscribeBooksInvalidCurrency(t *testing.T) {
	tests := []struct {
		name      string
		takerPays map[string]interface{}
		takerGets map[string]interface{}
		errorMsg  string
	}{
		{
			name:      "missing taker_pays currency",
			takerPays: map[string]interface{}{},
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			errorMsg: "taker_pays: issuer required for non-XRP currency",
		},
		{
			name: "missing taker_gets currency",
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerGets: map[string]interface{}{},
			errorMsg:  "taker_gets: issuer required for non-XRP currency",
		},
		{
			name: "non-XRP taker_pays without issuer",
			takerPays: map[string]interface{}{
				"currency": "USD",
			},
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			errorMsg: "taker_pays: issuer required for non-XRP currency",
		},
		{
			name: "non-XRP taker_gets without issuer",
			takerPays: map[string]interface{}{
				"currency": "XRP",
			},
			takerGets: map[string]interface{}{
				"currency": "USD",
			},
			errorMsg: "taker_gets: issuer required for non-XRP currency",
		},
		{
			name: "invalid issuer in taker_pays",
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "invalid_issuer",
			},
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			errorMsg: "taker_pays: invalid issuer address",
		},
		{
			name: "invalid issuer in taker_gets",
			takerPays: map[string]interface{}{
				"currency": "XRP",
			},
			takerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   "invalid_issuer",
			},
			errorMsg: "taker_gets: invalid issuer address",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			sm.AddConnection(conn)

			takerPays, _ := json.Marshal(tc.takerPays)
			takerGets, _ := json.Marshal(tc.takerGets)

			request := types.SubscriptionRequest{
				Books: []types.BookRequest{
					{
						TakerPays: takerPays,
						TakerGets: takerGets,
					},
				},
			}

			err := sm.HandleSubscribe(conn, request)
			require.NotNil(t, err, "Expected error for: %s", tc.name)
			assert.Contains(t, err.Message, tc.errorMsg)

			sm.RemoveConnection(conn.ID)
		})
	}
}

// TestSubscribeBooksMultiple tests subscribing to multiple order books
func TestSubscribeBooksMultiple(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Book 1: XRP for USD
	takerPays1, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets1, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	// Book 2: EUR for XRP
	takerPays2, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})
	takerGets2, _ := json.Marshal(map[string]interface{}{
		"currency": "EUR",
		"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays1,
				TakerGets: takerGets1,
			},
			{
				TakerPays: takerPays2,
				TakerGets: takerGets2,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err, "Expected no error for multiple valid books")

	config := conn.Subscriptions[types.SubOrderBooks]
	assert.Equal(t, 2, len(config.Books))

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// Unsubscribe Tests
// Based on rippled Subscribe_test.cpp unsubscribe sections
// =============================================================================

// TestUnsubscribeFromStreams tests unsubscribing from streams
func TestUnsubscribeFromStreams(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// First subscribe
	subscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions, types.SubValidations},
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)
	assert.Equal(t, 3, len(conn.Subscriptions))

	// Then unsubscribe from one stream
	unsubscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)
	require.Nil(t, err)

	// Verify ledger subscription was removed
	_, exists := conn.Subscriptions[types.SubLedger]
	assert.False(t, exists, "Ledger subscription should be removed")

	// Verify other subscriptions remain
	assert.Contains(t, conn.Subscriptions, types.SubTransactions)
	assert.Contains(t, conn.Subscriptions, types.SubValidations)

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeFromAccounts tests unsubscribing from accounts
func TestUnsubscribeFromAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", // Genesis
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", // Bob
		"rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW", // Carol
	}

	// First subscribe to all accounts
	subscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	config := conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 3, len(config.Accounts))

	// Unsubscribe from one account
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)
	require.Nil(t, err)

	// Verify the account was removed
	config = conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 2, len(config.Accounts))
	assert.NotContains(t, config.Accounts, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	assert.Contains(t, config.Accounts, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK")
	assert.Contains(t, config.Accounts, "rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW")

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeFromAllAccounts tests unsubscribing from all accounts removes the subscription
func TestUnsubscribeFromAllAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	// First subscribe
	subscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	// Unsubscribe from all
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)
	require.Nil(t, err)

	// Verify accounts subscription is completely removed
	_, exists := conn.Subscriptions[types.SubAccounts]
	assert.False(t, exists, "Accounts subscription should be removed when all accounts are unsubscribed")

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeFromBooks tests unsubscribing from order books
// Note: Current implementation removes all book subscriptions when unsubscribing from books
func TestUnsubscribeFromBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays1, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets1, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	// Subscribe to a book
	subscribeRequest := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{TakerPays: takerPays1, TakerGets: takerGets1},
		},
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	_, exists := conn.Subscriptions[types.SubOrderBooks]
	require.True(t, exists, "Book subscription should exist")

	// Unsubscribe from books
	unsubscribeRequest := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{TakerPays: takerPays1, TakerGets: takerGets1},
		},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)
	require.Nil(t, err)

	// Verify book subscription is removed
	_, exists = conn.Subscriptions[types.SubOrderBooks]
	assert.False(t, exists, "Book subscription should be removed after unsubscribing")

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeFromNonSubscribedStream tests that unsubscribing from a non-subscribed stream succeeds silently
// This matches rippled behavior where unsubscribing from something you're not subscribed to is not an error
func TestUnsubscribeFromNonSubscribedStream(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe to ledger only
	subscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	// Unsubscribe from transactions (which we never subscribed to)
	unsubscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)

	// Should succeed silently
	require.Nil(t, err, "Unsubscribing from non-subscribed stream should succeed silently")

	// Ledger subscription should still exist
	assert.Contains(t, conn.Subscriptions, types.SubLedger)

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeFromNonSubscribedAccount tests unsubscribing from a non-subscribed account
func TestUnsubscribeFromNonSubscribedAccount(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe to one account
	subscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	// Unsubscribe from a different account
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)

	// Should succeed silently
	require.Nil(t, err, "Unsubscribing from non-subscribed account should succeed silently")

	// Original account subscription should still exist
	config := conn.Subscriptions[types.SubAccounts]
	assert.Contains(t, config.Accounts, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// Additional Error Cases
// Based on rippled Subscribe_test.cpp testSubErrors()
// =============================================================================

// TestSubscribeMissingTakerPays tests book subscription without taker_pays
func TestSubscribeMissingTakerPays(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerGets, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerGets: takerGets,
				// Missing TakerPays
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.NotNil(t, err, "Expected error for missing taker_pays")
	assert.Contains(t, err.Message, "taker_pays")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeMissingTakerGets tests book subscription without taker_gets
func TestSubscribeMissingTakerGets(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				// Missing TakerGets
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.NotNil(t, err, "Expected error for missing taker_gets")
	assert.Contains(t, err.Message, "taker_gets")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeInvalidTakerPaysJSON tests book subscription with invalid JSON in taker_pays
func TestSubscribeInvalidTakerPaysJSON(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerGets, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: json.RawMessage(`{invalid json}`),
				TakerGets: takerGets,
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.NotNil(t, err, "Expected error for invalid taker_pays JSON")
	assert.Contains(t, err.Message, "Invalid taker_pays")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeInvalidTakerGetsJSON tests book subscription with invalid JSON in taker_gets
func TestSubscribeInvalidTakerGetsJSON(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})

	request := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{
				TakerPays: takerPays,
				TakerGets: json.RawMessage(`{invalid json}`),
			},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.NotNil(t, err, "Expected error for invalid taker_gets JSON")
	assert.Contains(t, err.Message, "Invalid taker_gets")

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// Subscription Manager State Tests
// =============================================================================

// TestSubscriptionManagerAddRemoveConnection tests connection management
func TestSubscriptionManagerAddRemoveConnection(t *testing.T) {
	sm := newTestSubscriptionManager()

	// Add connection
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)
	assert.Equal(t, 1, sm.ConnectionCount())

	// Verify connection exists
	retrievedConn := sm.GetConnection("test-conn-1")
	assert.NotNil(t, retrievedConn)
	assert.Equal(t, "test-conn-1", retrievedConn.ID)

	// Remove connection
	sm.RemoveConnection("test-conn-1")
	assert.Equal(t, 0, sm.ConnectionCount())

	// Verify connection no longer exists
	retrievedConn = sm.GetConnection("test-conn-1")
	assert.Nil(t, retrievedConn)
}

// TestSubscriptionManagerMultipleConnections tests managing multiple connections
func TestSubscriptionManagerMultipleConnections(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")
	conn3 := newTestConnection("conn-3")

	sm.AddConnection(conn1)
	sm.AddConnection(conn2)
	sm.AddConnection(conn3)
	assert.Equal(t, 3, sm.ConnectionCount())

	// Subscribe each to different streams
	sm.HandleSubscribe(conn1, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})
	sm.HandleSubscribe(conn2, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubTransactions}})
	sm.HandleSubscribe(conn3, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions}})

	// Verify subscriber counts
	assert.Equal(t, 2, sm.GetSubscriberCount(types.SubLedger))
	assert.Equal(t, 2, sm.GetSubscriberCount(types.SubTransactions))
	assert.Equal(t, 0, sm.GetSubscriberCount(types.SubValidations))

	// Remove one connection
	sm.RemoveConnection("conn-3")
	assert.Equal(t, 2, sm.ConnectionCount())
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubLedger))
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubTransactions))

	// Cleanup
	sm.RemoveConnection("conn-1")
	sm.RemoveConnection("conn-2")
}

// TestIsSubscribed tests the IsSubscribed helper method
func TestIsSubscribed(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Initially not subscribed
	assert.False(t, sm.IsSubscribed("test-conn-1", "ledger"))

	// Subscribe
	sm.HandleSubscribe(conn, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})

	// Now subscribed
	assert.True(t, sm.IsSubscribed("test-conn-1", "ledger"))
	assert.False(t, sm.IsSubscribed("test-conn-1", "transactions"))

	// Non-existent connection
	assert.False(t, sm.IsSubscribed("non-existent", "ledger"))

	sm.RemoveConnection(conn.ID)
}

// TestGetConnectionSubscriptions tests getting all subscriptions for a connection
func TestGetConnectionSubscriptions(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe to multiple things
	sm.HandleSubscribe(conn, types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger, types.SubTransactions},
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	})

	subs := sm.GetConnectionSubscriptions("test-conn-1")
	require.NotNil(t, subs)
	assert.Contains(t, subs, types.SubLedger)
	assert.Contains(t, subs, types.SubTransactions)
	assert.Contains(t, subs, types.SubAccounts)

	// Non-existent connection
	subs = sm.GetConnectionSubscriptions("non-existent")
	assert.Nil(t, subs)

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// IsValidXRPLAddress Tests
// =============================================================================

// TestIsValidXRPLAddress tests the address validation function
func TestIsValidXRPLAddress(t *testing.T) {
	tests := []struct {
		name     string
		address  string
		expected bool
	}{
		{
			name:     "valid genesis account",
			address:  "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: true,
		},
		{
			name:     "valid account 2",
			address:  "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			expected: true,
		},
		{
			name:     "invalid - bad checksum",
			address:  "rN7n3473SaZBCG4dFL83w7a1RXtXtbk2D9",
			expected: false,
		},
		{
			name:     "valid short account",
			address:  "rLDYrujdKUfVx28T9vRDAbyJ7G2WVXKo4K",
			expected: true,
		},
		{
			name:     "invalid - empty string",
			address:  "",
			expected: false,
		},
		{
			name:     "invalid - too short",
			address:  "rHb9CJAWyB4rj91VRWn96Dk",
			expected: false,
		},
		{
			name:     "invalid - too long",
			address:  "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyThExtraChars",
			expected: false,
		},
		{
			name:     "invalid - wrong prefix",
			address:  "sHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - numeric prefix",
			address:  "0Hb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - contains 0",
			address:  "rHb0CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - contains O",
			address:  "rHbOCJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - contains I",
			address:  "rHbICJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - contains l",
			address:  "rHblCJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expected: false,
		},
		{
			name:     "invalid - special characters",
			address:  "rHb9CJAWyB4rj91VRWn96DkukG4bwdty!@",
			expected: false,
		},
		{
			name:     "invalid - node public key",
			address:  "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV",
			expected: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := types.IsValidXRPLAddress(tc.address)
			assert.Equal(t, tc.expected, result, "IsValidXRPLAddress(%q) = %v, want %v", tc.address, result, tc.expected)
		})
	}
}

// =============================================================================
// Subscribe Response Tests
// =============================================================================

// TestGetSubscribeResponse tests generating a subscribe confirmation response
func TestGetSubscribeResponse(t *testing.T) {
	sm := newTestSubscriptionManager()

	response := sm.GetSubscribeResponse(
		100,                                                              // ledgerIndex
		"4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652", // ledgerHash
		735000000,                                                        // ledgerTime
		10,                                                               // feeBase
		10000000,                                                         // reserveBase
		2000000,                                                          // reserveInc
	)

	assert.Equal(t, "success", response.Status)
	assert.Equal(t, uint32(100), response.LedgerIndex)
	assert.Equal(t, "4BC50C9B0D8515D3EAAE1E74B29A95804346C491EE1A95BF25E4AAB854A6A652", response.LedgerHash)
	assert.Equal(t, uint32(735000000), response.LedgerTime)
	assert.Equal(t, uint64(10), response.FeeBase)
	assert.Equal(t, uint64(10000000), response.ReserveBase)
	assert.Equal(t, uint64(2000000), response.ReserveInc)
}

// =============================================================================
// Subscribe/Unsubscribe Method Tests (RPC Handler level)
// =============================================================================

// TestSubscribeMethodRequiresWebSocket tests that subscribe returns error via HTTP
func TestSubscribeMethodRequiresWebSocket(t *testing.T) {
	method := &handlers.SubscribeMethod{}
	ctx := &types.RpcContext{
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, err := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcNOT_SUPPORTED, err.Code)
	assert.Contains(t, err.Message, "WebSocket")
}

// TestUnsubscribeMethodRequiresWebSocket tests that unsubscribe returns error via HTTP
func TestUnsubscribeMethodRequiresWebSocket(t *testing.T) {
	method := &handlers.UnsubscribeMethod{}
	ctx := &types.RpcContext{
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, err := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcNOT_SUPPORTED, err.Code)
	assert.Contains(t, err.Message, "WebSocket")
}

// TestSubscribeMethodMetadata tests method metadata
func TestSubscribeMethodMetadata(t *testing.T) {
	method := &handlers.SubscribeMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"subscribe should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// TestUnsubscribeMethodMetadata tests method metadata
func TestUnsubscribeMethodMetadata(t *testing.T) {
	method := &handlers.UnsubscribeMethod{}

	t.Run("RequiredRole", func(t *testing.T) {
		assert.Equal(t, types.RoleGuest, method.RequiredRole(),
			"unsubscribe should be accessible to guests")
	})

	t.Run("SupportedApiVersions", func(t *testing.T) {
		versions := method.SupportedApiVersions()
		assert.Contains(t, versions, types.ApiVersion1)
		assert.Contains(t, versions, types.ApiVersion2)
		assert.Contains(t, versions, types.ApiVersion3)
	})
}

// =============================================================================
// Broadcast Tests
// =============================================================================

// TestBroadcastToStream tests broadcasting to stream subscribers
func TestBroadcastToStream(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")
	conn3 := newTestConnection("conn-3")

	sm.AddConnection(conn1)
	sm.AddConnection(conn2)
	sm.AddConnection(conn3)

	// Subscribe conn1 and conn3 to ledger
	sm.HandleSubscribe(conn1, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})
	sm.HandleSubscribe(conn2, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubTransactions}})
	sm.HandleSubscribe(conn3, types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}})

	// Broadcast to ledger stream
	testData := []byte(`{"type":"ledgerClosed","ledger_index":100}`)
	sm.BroadcastToStream(types.SubLedger, testData, nil)

	// conn1 and conn3 should receive the message
	select {
	case msg := <-conn1.SendChannel:
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn1 should have received the message")
	}

	select {
	case msg := <-conn3.SendChannel:
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn3 should have received the message")
	}

	// conn2 should NOT receive the message
	select {
	case <-conn2.SendChannel:
		t.Error("conn2 should NOT have received the message")
	default:
		// Expected - no message
	}

	sm.RemoveConnection("conn-1")
	sm.RemoveConnection("conn-2")
	sm.RemoveConnection("conn-3")
}

// TestBroadcastToAccounts tests broadcasting to account subscribers
func TestBroadcastToAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")

	sm.AddConnection(conn1)
	sm.AddConnection(conn2)

	// Subscribe to different accounts
	sm.HandleSubscribe(conn1, types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	})
	sm.HandleSubscribe(conn2, types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	})

	// Broadcast for first account
	testData := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccounts(testData, []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"})

	// Only conn1 should receive
	select {
	case msg := <-conn1.SendChannel:
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn1 should have received the message")
	}

	select {
	case <-conn2.SendChannel:
		t.Error("conn2 should NOT have received the message")
	default:
		// Expected
	}

	sm.RemoveConnection("conn-1")
	sm.RemoveConnection("conn-2")
}

// TestBookMatchesCurrency tests the order book matching logic
func TestBookMatchesCurrency(t *testing.T) {
	tests := []struct {
		name       string
		takerGets  map[string]interface{}
		takerPays  map[string]interface{}
		specGets   types.CurrencySpec
		specPays   types.CurrencySpec
		shouldMatch bool
	}{
		{
			name: "XRP for USD - match",
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			specGets:    types.CurrencySpec{Currency: "XRP", Issuer: ""},
			specPays:    types.CurrencySpec{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			shouldMatch: true,
		},
		{
			name: "XRP for USD - no match (different issuer)",
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			specGets:    types.CurrencySpec{Currency: "XRP", Issuer: ""},
			specPays:    types.CurrencySpec{Currency: "USD", Issuer: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
			shouldMatch: false,
		},
		{
			name: "XRP for USD - no match (different currency)",
			takerGets: map[string]interface{}{
				"currency": "XRP",
			},
			takerPays: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			specGets:    types.CurrencySpec{Currency: "XRP", Issuer: ""},
			specPays:    types.CurrencySpec{Currency: "EUR", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			shouldMatch: false,
		},
		{
			name: "USD for EUR - match (both IOUs)",
			takerGets: map[string]interface{}{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerPays: map[string]interface{}{
				"currency": "EUR",
				"issuer":   "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
			},
			specGets:    types.CurrencySpec{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			specPays:    types.CurrencySpec{Currency: "EUR", Issuer: "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
			shouldMatch: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			takerGets, _ := json.Marshal(tc.takerGets)
			takerPays, _ := json.Marshal(tc.takerPays)

			book := types.BookRequest{
				TakerGets: takerGets,
				TakerPays: takerPays,
			}

			result := types.BookMatchesCurrency(book, tc.specGets, tc.specPays)
			assert.Equal(t, tc.shouldMatch, result)
		})
	}
}

// =============================================================================
// Duplicate Subscription Tests
// =============================================================================

// TestSubscribeDuplicateStreamIdempotent tests that subscribing to the same stream twice is idempotent
func TestSubscribeDuplicateStreamIdempotent(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe once
	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err)
	assert.Equal(t, 1, len(conn.Subscriptions))

	// Subscribe again
	err = sm.HandleSubscribe(conn, request)
	require.Nil(t, err)
	assert.Equal(t, 1, len(conn.Subscriptions)) // Should still be 1

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeDuplicateAccountsMerged tests that duplicate accounts are merged
func TestSubscribeDuplicateAccountsMerged(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe to first account
	request1 := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err := sm.HandleSubscribe(conn, request1)
	require.Nil(t, err)

	config := conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 1, len(config.Accounts))

	// Subscribe to a new account
	request2 := types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	}
	err = sm.HandleSubscribe(conn, request2)
	require.Nil(t, err)

	config = conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 2, len(config.Accounts))

	// Subscribe to an already subscribed account (should not duplicate)
	request3 := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err = sm.HandleSubscribe(conn, request3)
	require.Nil(t, err)

	config = conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 2, len(config.Accounts)) // Should still be 2

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// Mixed Subscription Tests
// =============================================================================

// TestSubscribeMixedStreamsAndAccounts tests subscribing to both streams and accounts
func TestSubscribeMixedStreamsAndAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger, types.SubTransactions},
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err)

	assert.Contains(t, conn.Subscriptions, types.SubLedger)
	assert.Contains(t, conn.Subscriptions, types.SubTransactions)
	assert.Contains(t, conn.Subscriptions, types.SubAccounts)

	accountConfig := conn.Subscriptions[types.SubAccounts]
	assert.Equal(t, 1, len(accountConfig.Accounts))
	assert.Contains(t, accountConfig.Accounts, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")

	sm.RemoveConnection(conn.ID)
}

// TestSubscribeMixedStreamsAccountsAndBooks tests subscribing to streams, accounts, and books
func TestSubscribeMixedStreamsAccountsAndBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	takerPays, _ := json.Marshal(map[string]interface{}{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]interface{}{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger},
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		Books: []types.BookRequest{
			{TakerPays: takerPays, TakerGets: takerGets},
		},
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err)

	assert.Contains(t, conn.Subscriptions, types.SubLedger)
	assert.Contains(t, conn.Subscriptions, types.SubAccounts)
	assert.Contains(t, conn.Subscriptions, types.SubOrderBooks)

	sm.RemoveConnection(conn.ID)
}

// =============================================================================
// URL Subscription Tests
// =============================================================================

// TestSubscribeWithURL tests subscribing with URL callback
func TestSubscribeWithURL(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	request := types.SubscriptionRequest{
		Streams:     []types.SubscriptionType{types.SubLedger},
		URL:         "http://localhost/events",
		URLUsername: "admin",
		URLPassword: "password",
	}

	err := sm.HandleSubscribe(conn, request)
	require.Nil(t, err)

	// Verify URL subscription is stored in the URLSubscription field
	assert.Equal(t, "http://localhost/events", conn.URLSubscription, "URL subscription should be stored")

	sm.RemoveConnection(conn.ID)
}

// TestUnsubscribeWithURL tests unsubscribing a URL callback
func TestUnsubscribeWithURL(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	sm.AddConnection(conn)

	// Subscribe with URL
	subscribeRequest := types.SubscriptionRequest{
		URL: "http://localhost/events",
	}
	err := sm.HandleSubscribe(conn, subscribeRequest)
	require.Nil(t, err)

	require.Equal(t, "http://localhost/events", conn.URLSubscription)

	// Unsubscribe URL
	unsubscribeRequest := types.SubscriptionRequest{
		URL: "http://localhost/events",
	}
	err = sm.HandleUnsubscribe(conn, unsubscribeRequest)
	require.Nil(t, err)

	assert.Equal(t, "", conn.URLSubscription, "URL subscription should be removed")

	sm.RemoveConnection(conn.ID)
}
