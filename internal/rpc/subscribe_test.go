package rpc

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/subscription"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestSubscriptionManager creates a new subscription.Manager for testing
func newTestSubscriptionManager() *subscription.Manager {
	return subscription.NewManager()
}

// newTestConnection creates a new Connection for testing
func newTestConnection(id string) *subscription.Connection {
	return subscription.NewConnection(id, make(chan []byte, 100))
}

// Stream Subscription Tests
// Based on rippled Subscribe_test.cpp testServer(), testLedger(), testTransactions_APIv1()

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
			streamType:   types.SubTransactionsProposed,
			streamString: "transactions_proposed",
			expectError:  false,
		},
		{
			name:         "server stream - subscribe to server status events",
			streamType:   types.SubServer,
			streamString: "server",
			expectError:  false,
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
			_ = testRegistration(t, sm, conn)

			request := types.SubscriptionRequest{
				Streams: []types.SubscriptionType{tc.streamType},
			}

			err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for stream type: %s", tc.streamString)
				assert.Equal(t, "malformedStream", err.ErrorString)
			} else {
				require.Nil(t, err, "Expected no error for stream type: %s", tc.streamString)

				// Verify subscription was recorded
				assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(tc.streamType), "Expected subscription to be recorded for stream: %s", tc.streamString)
			}

			// Cleanup
		})
	}
}

// TestSubscribeMultipleStreams tests subscribing to multiple streams at once
func TestSubscribeMultipleStreams(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions, types.SubValidations},
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for multiple valid streams")

	// Verify all subscriptions were recorded
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubValidations))
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
			_ = testRegistration(t, sm, conn)

			request := types.SubscriptionRequest{
				Streams: []types.SubscriptionType{types.SubscriptionType(tc.streamName)},
			}

			err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for invalid stream: %s", tc.streamName)
				// rippled Subscribe.cpp:171-174 → rpcSTREAM_MALFORMED.
				assert.Equal(t, types.RpcSTREAM_MALFORMED, err.Code)
				assert.Equal(t, "malformedStream", err.ErrorString)
				assert.Equal(t, "Stream malformed.", err.Message)
			}
		})
	}
}

// Account Subscription Tests
// Based on rippled Subscribe_test.cpp testTransactions_APIv1() account subscription section

// TestSubscribeAccounts tests subscribing to specific accounts
func TestSubscribeAccounts(t *testing.T) {
	validAccounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", // Genesis account
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", // Bob
		"rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW", // Carol
	}

	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		Accounts: validAccounts,
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for valid accounts")

	// Verify subscription was recorded with all accounts
	accounts := testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts)
	assert.Equal(t, len(validAccounts), len(accounts))

	for _, acc := range validAccounts {
		assert.Contains(t, accounts, acc)
	}
}

// TestSubscribeAccountsProposed tests subscribing to proposed transactions for accounts
func TestSubscribeAccountsProposed(t *testing.T) {
	validAccounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		AccountsProposed: validAccounts,
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for valid accounts_proposed")
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
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - very short",
			account:     "rHb9CJA",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - too long",
			account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyThExtraChars",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - wrong prefix",
			account:     "sHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - node public key format",
			account:     "n94JNrQYkDrpt62bbSR7nVEhdyAvcJXRAsjEkFYyqRkh9SUTYEqV",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - numeric string",
			account:     "12345678901234567890123456789012345",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - hex string",
			account:     "0x1234567890ABCDEF1234567890ABCDEF12345678",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - special characters",
			account:     "rHb9CJAWyB4rj91VRWn96DkukG4bwdty!@",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
		{
			name:        "invalid account - contains forbidden char 0",
			account:     "rHb0CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			expectError: true,
			errorMsg:    "Account malformed.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			_ = testRegistration(t, sm, conn)

			request := types.SubscriptionRequest{
				Accounts: []string{tc.account},
			}

			err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)

			if tc.expectError {
				require.NotNil(t, err, "Expected error for invalid account: %s", tc.account)
				// rippled Subscribe.cpp:197-199 → rpcACT_MALFORMED.
				assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
				assert.Equal(t, "actMalformed", err.ErrorString)
				assert.Equal(t, tc.errorMsg, err.Message)
			} else {
				require.Nil(t, err, "Expected no error for valid account: %s", tc.account)
			}
		})
	}
}

// TestSubscribeAccountsProposedInvalidFormat tests invalid accounts_proposed
func TestSubscribeAccountsProposedInvalidFormat(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		AccountsProposed: []string{"invalid_account"},
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "Expected error for invalid accounts_proposed")
	assert.Equal(t, types.RpcACT_MALFORMED, err.Code)
	assert.Equal(t, "actMalformed", err.ErrorString)
	assert.Equal(t, "Account malformed.", err.Message)
}

// Book Subscription Tests
// Based on rippled Subscribe_test.cpp testSubErrors() for books

// TestSubscribeBooks tests subscribing to order books with taker_gets/taker_pays
func TestSubscribeBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Valid book subscription: XRP for USD
	takerPays, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
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
	require.Nil(t, err, "Expected no error for valid book subscription")

	// Verify subscription was recorded
	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().BookCount())
}

// TestSubscribeBooksWithSnapshot tests the snapshot flag for initial order book state
func TestSubscribeBooksWithSnapshot(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]any{
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

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for book subscription with snapshot")

	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().BookCount())
}

// TestSubscribeBooksWithBoth tests the both flag for both sides of order book
func TestSubscribeBooksWithBoth(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]any{
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

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for book subscription with both")

	assert.Equal(t, 2, testRegistration(t, sm, conn).Snapshot().BookCount())
}

// TestSubscribeBooksInvalidCurrency pins the per-site book errors of
// rippled Subscribe.cpp: taker_pays maps to the src* codes (:249-268),
// taker_gets to the dst* codes (:271-290); missing issuer on an IOU and
// a malformed issuer both land on the issuer-malformed code.
func TestSubscribeBooksInvalidCurrency(t *testing.T) {
	tests := []struct {
		name      string
		takerPays map[string]any
		takerGets map[string]any
		wantCode  int
		wantError string
		wantMsg   string
	}{
		{
			name:      "missing taker_pays currency",
			takerPays: map[string]any{},
			takerGets: map[string]any{
				"currency": "XRP",
			},
			wantCode:  types.RpcSRC_CUR_MALFORMED,
			wantError: "srcCurMalformed",
			wantMsg:   "Source currency is malformed.",
		},
		{
			name: "missing taker_gets currency",
			takerPays: map[string]any{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerGets: map[string]any{},
			wantCode:  types.RpcDST_AMT_MALFORMED,
			wantError: "dstAmtMalformed",
			wantMsg:   "Destination amount/currency/issuer is malformed.",
		},
		{
			name: "unparseable taker_pays currency",
			takerPays: map[string]any{
				"currency": "USDX",
			},
			takerGets: map[string]any{
				"currency": "XRP",
			},
			wantCode:  types.RpcSRC_CUR_MALFORMED,
			wantError: "srcCurMalformed",
			wantMsg:   "Source currency is malformed.",
		},
		{
			name: "unparseable taker_gets currency",
			takerPays: map[string]any{
				"currency": "XRP",
			},
			takerGets: map[string]any{
				"currency": "USDX",
			},
			wantCode:  types.RpcDST_AMT_MALFORMED,
			wantError: "dstAmtMalformed",
			wantMsg:   "Destination amount/currency/issuer is malformed.",
		},
		{
			name: "non-XRP taker_pays without issuer",
			takerPays: map[string]any{
				"currency": "USD",
			},
			takerGets: map[string]any{
				"currency": "XRP",
			},
			wantCode:  types.RpcSRC_ISR_MALFORMED,
			wantError: "srcIsrMalformed",
			wantMsg:   "Source issuer is malformed.",
		},
		{
			name: "non-XRP taker_gets without issuer",
			takerPays: map[string]any{
				"currency": "XRP",
			},
			takerGets: map[string]any{
				"currency": "USD",
			},
			wantCode:  types.RpcDST_ISR_MALFORMED,
			wantError: "dstIsrMalformed",
			wantMsg:   "Destination issuer is malformed.",
		},
		{
			name: "invalid issuer in taker_pays",
			takerPays: map[string]any{
				"currency": "USD",
				"issuer":   "invalid_issuer",
			},
			takerGets: map[string]any{
				"currency": "XRP",
			},
			wantCode:  types.RpcSRC_ISR_MALFORMED,
			wantError: "srcIsrMalformed",
			wantMsg:   "Source issuer is malformed.",
		},
		{
			name: "invalid issuer in taker_gets",
			takerPays: map[string]any{
				"currency": "XRP",
			},
			takerGets: map[string]any{
				"currency": "USD",
				"issuer":   "invalid_issuer",
			},
			wantCode:  types.RpcDST_ISR_MALFORMED,
			wantError: "dstIsrMalformed",
			wantMsg:   "Destination issuer is malformed.",
		},
		{
			name: "XRP taker_pays with issuer",
			takerPays: map[string]any{
				"currency": "XRP",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			takerGets: map[string]any{
				"currency": "USD",
				"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
			},
			wantCode:  types.RpcSRC_ISR_MALFORMED,
			wantError: "srcIsrMalformed",
			wantMsg:   "Source issuer is malformed.",
		},
		{
			name: "noAccount sentinel issuer in taker_pays",
			takerPays: map[string]any{
				"currency": "USD",
				"issuer":   "rrrrrrrrrrrrrrrrrrrrBZbvji",
			},
			takerGets: map[string]any{
				"currency": "XRP",
			},
			wantCode:  types.RpcSRC_ISR_MALFORMED,
			wantError: "srcIsrMalformed",
			wantMsg:   "Source issuer is malformed.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			_ = testRegistration(t, sm, conn)

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

			err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
			require.NotNil(t, err, "Expected error for: %s", tc.name)
			assert.Equal(t, tc.wantCode, err.Code)
			assert.Equal(t, tc.wantError, err.ErrorString)
			assert.Equal(t, tc.wantMsg, err.Message)
		})
	}
}

// TestSubscribeBooksCrossConformanceWithBookOffers pins the validation
// surface that subscribe.cpp shares with book_offers (rippled
// Subscribe.cpp:188-225 → makeBookSpec → BookOffers.cpp:51-199). The two RPCs
// must reject the same malformed currency / issuer / market shapes, otherwise
// a subscribe books request can succeed against an order book that book_offers
// would refuse to query.
func TestSubscribeBooksCrossConformanceWithBookOffers(t *testing.T) {
	type bookSpec struct {
		takerPays map[string]any
		takerGets map[string]any
	}
	tests := []struct {
		name      string
		book      bookSpec
		wantError string
	}{
		{
			// Book_test.cpp:1606-1618 — book_offers returns badMarket.
			name: "same currency and issuer is badMarket",
			book: bookSpec{
				takerPays: map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
				takerGets: map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			},
			wantError: "badMarket",
		},
		{
			// Book_test.cpp:1547-1561 — XRP currency must not carry an
			// issuer; Subscribe.cpp's illegal-issuer check (:258-268)
			// reports it as srcIsrMalformed.
			name: "XRP pay with non-XRP issuer is rejected",
			book: bookSpec{
				takerPays: map[string]any{"currency": "XRP", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
				takerGets: map[string]any{"currency": "USD", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			},
			wantError: "srcIsrMalformed",
		},
		{
			// Book_test.cpp:1505-1517 — ACCOUNT_ONE (noAccount sentinel) is
			// rejected with rpcSRC_ISR_MALFORMED.
			name: "ACCOUNT_ONE issuer is rejected",
			book: bookSpec{
				takerPays: map[string]any{"currency": "USD", "issuer": "rrrrrrrrrrrrrrrrrrrrBZbvji"},
				takerGets: map[string]any{"currency": "EUR", "issuer": "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
			},
			wantError: "srcIsrMalformed",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sm := newTestSubscriptionManager()
			conn := newTestConnection("test-conn-1")
			_ = testRegistration(t, sm, conn)

			takerPays, _ := json.Marshal(tc.book.takerPays)
			takerGets, _ := json.Marshal(tc.book.takerGets)

			request := types.SubscriptionRequest{
				Books: []types.BookRequest{
					{TakerPays: takerPays, TakerGets: takerGets},
				},
			}
			err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
			require.NotNil(t, err, "subscribe should reject the same malformed shape book_offers refuses")
			assert.Equal(t, tc.wantError, err.ErrorString)
		})
	}
}

// TestSubscribeBooksMultiple tests subscribing to multiple order books
func TestSubscribeBooksMultiple(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Book 1: XRP for USD
	takerPays1, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets1, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})

	// Book 2: EUR for XRP
	takerPays2, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})
	takerGets2, _ := json.Marshal(map[string]any{
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

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err, "Expected no error for multiple valid books")

	assert.Equal(t, 2, testRegistration(t, sm, conn).Snapshot().BookCount())
}

// Unsubscribe Tests
// Based on rippled Subscribe_test.cpp unsubscribe sections

// TestUnsubscribeFromStreams tests unsubscribing from streams
func TestUnsubscribeFromStreams(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// First subscribe
	subscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions, types.SubValidations},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)
	assert.Equal(t, 3, testRegistration(t, sm, conn).Snapshot().ItemCount())

	// Then unsubscribe from one stream
	unsubscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)
	require.Nil(t, err)

	// Verify ledger subscription was removed
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))

	// Verify other subscriptions remain
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubValidations))
}

// TestUnsubscribeFromAccounts tests unsubscribing from accounts
func TestUnsubscribeFromAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", // Genesis
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK", // Bob
		"rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW", // Carol
	}

	// First subscribe to all accounts
	subscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)

	assert.Len(t, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts), 3)

	// Unsubscribe from one account
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)
	require.Nil(t, err)

	// Verify the account was removed
	remaining := testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts)
	assert.Len(t, remaining, 2)
	assert.NotContains(t, remaining, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	assert.Contains(t, remaining, "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK")
	assert.Contains(t, remaining, "rH4KEcG9dEwGwpn6AyoWK9cZPLL4RLSmWW")
}

// TestUnsubscribeFromAllAccounts tests unsubscribing from all accounts removes the subscription
func TestUnsubscribeFromAllAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	accounts := []string{
		"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK",
	}

	// First subscribe
	subscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)

	// Unsubscribe from all
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: accounts,
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)
	require.Nil(t, err)

	// Verify accounts subscription is completely removed
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubAccounts))
}

// TestUnsubscribeFromBooks tests unsubscribing from order books
// Note: Current implementation removes all book subscriptions when unsubscribing from books
func TestUnsubscribeFromBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays1, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets1, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})

	// Subscribe to a book
	subscribeRequest := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{TakerPays: takerPays1, TakerGets: takerGets1},
		},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)

	require.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBook))

	// Unsubscribe from books
	unsubscribeRequest := types.SubscriptionRequest{
		Books: []types.BookRequest{
			{TakerPays: takerPays1, TakerGets: takerGets1},
		},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)
	require.Nil(t, err)

	// Verify book subscription is removed
	assert.False(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBook))
}

// TestUnsubscribeFromNonSubscribedStream tests that unsubscribing from a non-subscribed stream succeeds silently
// This matches rippled behavior where unsubscribing from something you're not subscribed to is not an error
func TestUnsubscribeFromNonSubscribedStream(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Subscribe to ledger only
	subscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)

	// Unsubscribe from transactions (which we never subscribed to)
	unsubscribeRequest := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubTransactions},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)

	// Should succeed silently
	require.Nil(t, err, "Unsubscribing from non-subscribed stream should succeed silently")

	// Ledger subscription should still exist
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
}

// TestUnsubscribeFromNonSubscribedAccount tests unsubscribing from a non-subscribed account
func TestUnsubscribeFromNonSubscribedAccount(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Subscribe to one account
	subscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), subscribeRequest, true)
	require.Nil(t, err)

	// Unsubscribe from a different account
	unsubscribeRequest := types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	}
	err = sm.HandleUnsubscribe(testRegistration(t, sm, conn), unsubscribeRequest)

	// Should succeed silently
	require.Nil(t, err, "Unsubscribing from non-subscribed account should succeed silently")

	// Original account subscription should still exist
	assert.Contains(t, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts), "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
}

// Additional Error Cases
// Based on rippled Subscribe_test.cpp testSubErrors()

// TestSubscribeMissingTakerPays tests book subscription without taker_pays
func TestSubscribeMissingTakerPays(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerGets, _ := json.Marshal(map[string]any{
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

	// rippled Subscribe.cpp:238-242 → rpcINVALID_PARAMS.
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "Expected error for missing taker_pays")
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
	assert.Equal(t, "Invalid parameters.", err.Message)
}

// TestSubscribeMissingTakerGets tests book subscription without taker_gets
func TestSubscribeMissingTakerGets(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{
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

	// rippled Subscribe.cpp:238-242 → rpcINVALID_PARAMS.
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "Expected error for missing taker_gets")
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
	assert.Equal(t, "Invalid parameters.", err.Message)
}

// TestSubscribeInvalidTakerPaysJSON tests book subscription with invalid JSON in taker_pays
func TestSubscribeInvalidTakerPaysJSON(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerGets, _ := json.Marshal(map[string]any{
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

	// A non-object side fails rippled's structural check
	// (Subscribe.cpp:238-242) → rpcINVALID_PARAMS.
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "Expected error for invalid taker_pays JSON")
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
	assert.Equal(t, "Invalid parameters.", err.Message)
}

// TestSubscribeInvalidTakerGetsJSON tests book subscription with invalid JSON in taker_gets
func TestSubscribeInvalidTakerGetsJSON(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{
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

	// A non-object side fails rippled's structural check
	// (Subscribe.cpp:238-242) → rpcINVALID_PARAMS.
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.NotNil(t, err, "Expected error for invalid taker_gets JSON")
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
	assert.Equal(t, "Invalid parameters.", err.Message)
}

// Subscription Manager State Tests

func TestSubscriptionManagerAttachDetach(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	registration := testRegistration(t, sm, conn)
	assert.Equal(t, uint64(1), sm.Metrics().Connections)
	require.True(t, sm.Detach(registration))
	assert.Zero(t, sm.Metrics().Connections)
}

// TestSubscriptionManagerMultipleConnections tests managing multiple connections
func TestSubscriptionManagerMultipleConnections(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")
	conn3 := newTestConnection("conn-3")

	registration1 := testRegistration(t, sm, conn1)
	registration2 := testRegistration(t, sm, conn2)
	registration3 := testRegistration(t, sm, conn3)
	assert.Equal(t, uint64(3), sm.Metrics().Connections)

	// Subscribe each to different streams
	sm.HandleSubscribe(testRegistration(t, sm, conn1), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true)
	sm.HandleSubscribe(testRegistration(t, sm, conn2), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubTransactions}}, true)
	sm.HandleSubscribe(testRegistration(t, sm, conn3), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger, types.SubTransactions}}, true)

	// Verify subscriber counts
	assert.Equal(t, 2, sm.GetSubscriberCount(types.SubLedger))
	assert.Equal(t, 2, sm.GetSubscriberCount(types.SubTransactions))
	assert.Equal(t, 0, sm.GetSubscriberCount(types.SubValidations))

	// Remove one connection
	require.True(t, sm.Detach(registration3))
	assert.Equal(t, uint64(2), sm.Metrics().Connections)
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubLedger))
	assert.Equal(t, 1, sm.GetSubscriberCount(types.SubTransactions))

	require.True(t, sm.Detach(registration1))
	require.True(t, sm.Detach(registration2))
}

// IsValidXRPLAddress Tests

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

// Subscribe/Unsubscribe Method Tests (RPC Handler level)

// TestSubscribeMethodRequiresWebSocket tests that subscribe returns rippled's
// rpcINVALID_PARAMS over plain JSON-RPC (Subscribe.cpp: "Must be a JSON-RPC
// call." branch when context.infoSub is null and no `url` param is provided).
func TestSubscribeMethodRequiresWebSocket(t *testing.T) {
	method := &handlers.SubscribeMethod{}
	ctx := &types.RpcContext{
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, err := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
}

// TestUnsubscribeMethodRequiresWebSocket tests that unsubscribe returns
// rippled's rpcINVALID_PARAMS over plain JSON-RPC (Unsubscribe.cpp: same
// "Must be a JSON-RPC call." gate as Subscribe.cpp).
func TestUnsubscribeMethodRequiresWebSocket(t *testing.T) {
	method := &handlers.UnsubscribeMethod{}
	ctx := &types.RpcContext{
		Role:       types.RoleGuest,
		ApiVersion: types.ApiVersion1,
	}

	result, err := method.Handle(ctx, nil)
	assert.Nil(t, result)
	require.NotNil(t, err)
	assert.Equal(t, types.RpcINVALID_PARAMS, err.Code)
	assert.Equal(t, "invalidParams", err.ErrorString)
}

// TestSubscribeURLGating tests the url (RPCSub) branch gates over plain
// JSON-RPC: rippled requires Role::ADMIN for url subscriptions
// (Subscribe.cpp / Unsubscribe.cpp → rpcNO_PERMISSION). The admin cases
// here run without a url-subscription service wired (no WebSocket server),
// which reports notSupported; the full url path is covered in
// rpcsub_test.go.
func TestSubscribeURLGating(t *testing.T) {
	params := json.RawMessage(`{"url": "http://localhost:8081/callback", "streams": ["ledger"]}`)

	methods := map[string]types.MethodHandler{
		"subscribe":   &handlers.SubscribeMethod{},
		"unsubscribe": &handlers.UnsubscribeMethod{},
	}

	for name, method := range methods {
		t.Run(name+": url from non-admin is noPermission", func(t *testing.T) {
			ctx := &types.RpcContext{
				Role:       types.RoleGuest,
				ApiVersion: types.ApiVersion1,
			}
			result, err := method.Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, err)
			assert.Equal(t, types.RpcNO_PERMISSION, err.Code)
			assert.Equal(t, "noPermission", err.ErrorString)
		})

		t.Run(name+": url from admin is notSupported", func(t *testing.T) {
			ctx := &types.RpcContext{
				Role:       types.RoleAdmin,
				ApiVersion: types.ApiVersion1,
			}
			result, err := method.Handle(ctx, params)
			assert.Nil(t, result)
			require.NotNil(t, err)
			assert.Equal(t, types.RpcNOT_SUPPORTED, err.Code)
			assert.Equal(t, "notSupported", err.ErrorString)
		})
	}
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

// Broadcast Tests

// TestBroadcastToStream tests broadcasting to stream subscribers
func TestBroadcastToStream(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")
	conn3 := newTestConnection("conn-3")

	_ = testRegistration(t, sm, conn1)
	_ = testRegistration(t, sm, conn2)
	_ = testRegistration(t, sm, conn3)

	// Subscribe conn1 and conn3 to ledger
	sm.HandleSubscribe(testRegistration(t, sm, conn1), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true)
	sm.HandleSubscribe(testRegistration(t, sm, conn2), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubTransactions}}, true)
	sm.HandleSubscribe(testRegistration(t, sm, conn3), types.SubscriptionRequest{Streams: []types.SubscriptionType{types.SubLedger}}, true)

	// Broadcast to ledger stream
	testData := []byte(`{"type":"ledgerClosed","ledger_index":100}`)
	sm.BroadcastToStream(types.SubLedger, testData)

	// conn1 and conn3 should receive the message
	select {
	case msg := <-conn1.Outbound():
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn1 should have received the message")
	}

	select {
	case msg := <-conn3.Outbound():
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn3 should have received the message")
	}

	// conn2 should NOT receive the message
	select {
	case <-conn2.Outbound():
		t.Error("conn2 should NOT have received the message")
	default:
		// Expected - no message
	}
}

// TestBroadcastToAccounts tests broadcasting to account subscribers
func TestBroadcastToAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()

	conn1 := newTestConnection("conn-1")
	conn2 := newTestConnection("conn-2")

	_ = testRegistration(t, sm, conn1)
	_ = testRegistration(t, sm, conn2)

	// Subscribe to different accounts
	sm.HandleSubscribe(testRegistration(t, sm, conn1), types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}, true)
	sm.HandleSubscribe(testRegistration(t, sm, conn2), types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	}, true)

	// Broadcast for first account
	testData := []byte(`{"type":"transaction","account":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`)
	sm.BroadcastToAccountsVersioned(testData, testData, []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"})

	// Only conn1 should receive
	select {
	case msg := <-conn1.Outbound():
		assert.Equal(t, testData, msg)
	default:
		t.Error("conn1 should have received the message")
	}

	select {
	case <-conn2.Outbound():
		t.Error("conn2 should NOT have received the message")
	default:
		// Expected
	}
}

// Duplicate Subscription Tests

// TestSubscribeDuplicateStreamIdempotent tests that subscribing to the same stream twice is idempotent
func TestSubscribeDuplicateStreamIdempotent(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Subscribe once
	request := types.SubscriptionRequest{
		Streams: []types.SubscriptionType{types.SubLedger},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err)
	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().ItemCount())

	// Subscribe again
	err = sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err)
	assert.Equal(t, 1, testRegistration(t, sm, conn).Snapshot().ItemCount()) // Should still be 1
}

// TestSubscribeDuplicateAccountsMerged tests that duplicate accounts are merged
func TestSubscribeDuplicateAccountsMerged(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	// Subscribe to first account
	request1 := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request1, true)
	require.Nil(t, err)

	assert.Len(t, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts), 1)

	// Subscribe to a new account
	request2 := types.SubscriptionRequest{
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
	}
	err = sm.HandleSubscribe(testRegistration(t, sm, conn), request2, true)
	require.Nil(t, err)

	assert.Len(t, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts), 2)

	// Subscribe to an already subscribed account (should not duplicate)
	request3 := types.SubscriptionRequest{
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}
	err = sm.HandleSubscribe(testRegistration(t, sm, conn), request3, true)
	require.Nil(t, err)

	assert.Len(t, testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts), 2)
}

// Mixed Subscription Tests

// TestSubscribeMixedStreamsAndAccounts tests subscribing to both streams and accounts
func TestSubscribeMixedStreamsAndAccounts(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	request := types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger, types.SubTransactions},
		Accounts: []string{"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err)

	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactions))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubAccounts))

	accounts := testRegistration(t, sm, conn).Snapshot().Accounts(types.SubAccounts)
	assert.Len(t, accounts, 1)
	assert.Contains(t, accounts, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
}

// TestSubscribeMixedStreamsAccountsAndBooks tests subscribing to streams, accounts, and books
func TestSubscribeMixedStreamsAccountsAndBooks(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	takerPays, _ := json.Marshal(map[string]any{
		"currency": "USD",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
	})
	takerGets, _ := json.Marshal(map[string]any{
		"currency": "XRP",
	})

	request := types.SubscriptionRequest{
		Streams:  []types.SubscriptionType{types.SubLedger},
		Accounts: []string{"rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"},
		Books: []types.BookRequest{
			{TakerPays: takerPays, TakerGets: takerGets},
		},
	}

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), request, true)
	require.Nil(t, err)

	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubLedger))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubAccounts))
	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubBook))
}

// URL (RPCSub) subscription tests live in rpcsub_test.go: URL requests are
// routed to the URL service before reaching the manager.

// TestSubscribeBookBoth_AutoSubscribesReverse exercises the
// `both:true` shorthand: the subscription manager should register both
// the requested book AND its reversed pair so a broadcast on either
// side reaches the connection. Mirrors rippled Subscribe.cpp:330-337
// (subBook(book) + subBook(reversed(book))).
func TestSubscribeBookBoth_AutoSubscribesReverse(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	gateway := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	takerPays, _ := json.Marshal(map[string]any{"currency": "USD", "issuer": gateway})
	takerGets, _ := json.Marshal(map[string]any{"currency": "XRP"})

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Books: []types.BookRequest{{TakerPays: takerPays, TakerGets: takerGets, Both: true}},
	}, true)
	require.Nil(t, err)

	// Broadcast on the originally-subscribed side.
	msg1 := []byte(`{"type":"transaction","side":"original"}`)
	sm.BroadcastToOrderBooksVersioned(msg1, msg1, []types.OrderBookSpec{{
		TakerGets: types.CurrencySpec{Currency: "XRP"},
		TakerPays: types.CurrencySpec{Currency: "USD", Issuer: gateway},
	}})
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg1, received)
	default:
		t.Fatal("Expected broadcast on original side to reach the connection")
	}

	// Broadcast on the reversed side — should also reach because
	// `both:true` auto-registers the partner pair.
	msg2 := []byte(`{"type":"transaction","side":"reversed"}`)
	sm.BroadcastToOrderBooksVersioned(msg2, msg2, []types.OrderBookSpec{{
		TakerGets: types.CurrencySpec{Currency: "USD", Issuer: gateway},
		TakerPays: types.CurrencySpec{Currency: "XRP"},
	}})
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg2, received)
	default:
		t.Fatal("Expected broadcast on reversed side to reach the connection (both:true)")
	}
}

// snapshotMock is a focused LedgerService stub for snapshot-delivery
// tests. Records the (taker_gets, taker_pays, taker) tuple of every
// GetBookOffers call so the test can assert on dispatch ordering.
type snapshotMock struct {
	*mockLedgerService
	calls []struct {
		Gets, Pays types.Amount
		Taker      string
	}
	offersByGets map[string][]types.BookOffer
}

func (m *snapshotMock) GetBookOffers(_ context.Context, takerGets, takerPays types.Amount, taker, _ string, _ string, _ uint32, _ string, _ bool) (*types.BookOffersResult, error) {
	m.calls = append(m.calls, struct {
		Gets, Pays types.Amount
		Taker      string
	}{takerGets, takerPays, taker})
	key := takerGets.Currency + "/" + takerGets.Issuer
	return &types.BookOffersResult{
		Offers:      m.offersByGets[key],
		LedgerIndex: 100,
		Validated:   true,
	}, nil
}

// TestWebSocketSnapshot_Single verifies snapshot:true delivers a book
// snapshot inline in the subscribe ack via the LedgerService.
// Mirrors rippled Subscribe.cpp:339-394 (single-side: jss::offers).
func TestWebSocketSnapshot_Single(t *testing.T) {
	mock := &snapshotMock{
		mockLedgerService: newMockLedgerService(),
		offersByGets: map[string][]types.BookOffer{
			"XRP/": {types.BookOffer{Account: "rOffer1"}, types.BookOffer{Account: "rOffer2"}},
		},
	}
	services := types.NewTestServiceGraph(types.NewServiceContainer(mock))
	ws := &WebSocketServer{services: services}

	offers, err := ws.snapshotBook(
		&types.RpcContext{Context: context.Background(), Services: services},
		types.Amount{Currency: "XRP"},
		types.Amount{Currency: "USD", Issuer: "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"},
		"",
		"",
	)
	require.NoError(t, err)
	require.Len(t, offers, 2)
	require.Len(t, mock.calls, 1, "single-side snapshot must issue exactly one GetBookOffers call")
}

// TestWebSocketSnapshot_Both verifies snapshot:true + both:true
// produces TWO snapshot calls (one per side) so the response can
// carry bids and asks. Mirrors rippled Subscribe.cpp:362-374.
func TestWebSocketSnapshot_Both(t *testing.T) {
	gateway := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	mock := &snapshotMock{
		mockLedgerService: newMockLedgerService(),
		offersByGets: map[string][]types.BookOffer{
			"XRP/":           {types.BookOffer{Account: "rBid"}},
			"USD/" + gateway: {types.BookOffer{Account: "rAsk"}},
		},
	}
	services := types.NewTestServiceGraph(types.NewServiceContainer(mock))
	ws := &WebSocketServer{services: services}
	ctx := &types.RpcContext{Context: context.Background(), Services: services}

	bids, err := ws.snapshotBook(ctx, types.Amount{Currency: "XRP"}, types.Amount{Currency: "USD", Issuer: gateway}, "", "")
	require.NoError(t, err)
	asks, err := ws.snapshotBook(ctx, types.Amount{Currency: "USD", Issuer: gateway}, types.Amount{Currency: "XRP"}, "", "")
	require.NoError(t, err)
	require.Len(t, mock.calls, 2, "both:true snapshot must issue one GetBookOffers per side")
	require.Equal(t, "rBid", bids[0].Account)
	require.Equal(t, "rAsk", asks[0].Account)
}

// TestComputeServerLoad_TracksTxQ verifies the load-fee snapshot
// reflects the wired TxQ metrics so the server stream emits real
// load_factor_fee_escalation / load_factor_fee_queue numbers rather
// than constant 256s.
func TestComputeServerLoad_TracksTxQ(t *testing.T) {
	mock := newMockLedgerService()
	container := types.NewServiceContainer(mock)
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 512,
			OpenLedgerFeeLevel:    1024,
		}
	}
	services := types.NewTestServiceGraph(container)

	load := handlers.ComputeServerLoad(services)
	assert.Equal(t, uint64(256), load.LoadBase)
	assert.Equal(t, uint64(1024), load.LoadFactorFeeEscalation,
		"escalation must reflect (openLedger * loadBase / reference) = 1024")
	assert.Equal(t, uint64(512), load.LoadFactorFeeQueue,
		"queue must pass through MinProcessingFeeLevel")
	assert.Equal(t, uint64(256), load.LoadFactorFeeReference)
	assert.Equal(t, uint64(1024), load.LoadFactor,
		"server-wide load_factor must rise to escalation when it exceeds loadBase")
}

func TestComputeServerLoad_UsesServerAndOverallMaxima(t *testing.T) {
	mock := newMockLedgerService()
	container := types.NewServiceContainer(mock)
	container.LoadFactorFees = func() types.LoadFactorFees {
		return types.LoadFactorFees{Local: 700, Net: 900, Cluster: 800}
	}
	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 256,
			OpenLedgerFeeLevel:    600,
		}
	}
	services := types.NewTestServiceGraph(container)

	load := handlers.ComputeServerLoad(services)
	assert.Equal(t, uint64(900), load.LoadFactorServer)
	assert.Equal(t, uint64(900), load.LoadFactor)

	container.TxQMetrics = func() types.TxQServerMetrics {
		return types.TxQServerMetrics{
			ReferenceFeeLevel:     256,
			MinProcessingFeeLevel: 256,
			OpenLedgerFeeLevel:    1200,
		}
	}
	services = types.NewTestServiceGraph(container)
	load = handlers.ComputeServerLoad(services)
	assert.Equal(t, uint64(900), load.LoadFactorServer)
	assert.Equal(t, uint64(1200), load.LoadFactor)
}

// TestSubscribeRtTransactionsAlias verifies the deprecated
// "rt_transactions" stream name is accepted as an alias for
// "transactions_proposed" (rippled Subscribe.cpp:151-156).
func TestSubscribeRtTransactionsAlias(t *testing.T) {
	sm := newTestSubscriptionManager()
	conn := newTestConnection("test-conn-1")
	_ = testRegistration(t, sm, conn)

	err := sm.HandleSubscribe(testRegistration(t, sm, conn), types.SubscriptionRequest{
		Streams: []types.SubscriptionType{"rt_transactions"},
	}, true)
	require.Nil(t, err, "rt_transactions must be accepted")

	assert.True(t, testRegistration(t, sm, conn).Snapshot().Has(types.SubTransactionsProposed))

	msg := []byte(`{"type":"transaction","status":"proposed"}`)
	sm.BroadcastToStream(types.SubTransactionsProposed, msg)
	select {
	case received := <-conn.Outbound():
		assert.Equal(t, msg, received)
	default:
		t.Fatal("Expected broadcast on transactions_proposed to reach rt_transactions subscriber")
	}
}
