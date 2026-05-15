package subscription

import (
	"encoding/json"
	"fmt"
	"sync"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// validStreams contains the set of valid stream types
var validStreams = map[types.SubscriptionType]bool{
	types.SubLedger:               true,
	types.SubTransactions:         true,
	types.SubTransactionsProposed: true,
	types.SubAccounts:             true,
	types.SubOrderBooks:           true,
	types.SubValidations:          true,
	types.SubManifests:            true,
	types.SubPeerStatus:           true,
	types.SubServer:               true,
	types.SubConsensus:            true,
	types.SubPath:                 true,
}

// Manager manages WebSocket subscriptions
type Manager struct {
	Connections map[string]*types.Connection
	mu          sync.RWMutex
}

// NewManager creates a new Manager
func NewManager() *Manager {
	return &Manager{
		Connections: make(map[string]*types.Connection),
	}
}

// AddConnection adds a connection to the subscription manager
func (sm *Manager) AddConnection(conn *types.Connection) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.Connections == nil {
		sm.Connections = make(map[string]*types.Connection)
	}
	sm.Connections[conn.ID] = conn
}

// RemoveConnection removes a connection from the subscription manager
func (sm *Manager) RemoveConnection(connID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.Connections, connID)
}

// HandleSubscribe handles a subscribe request for a connection
func (sm *Manager) HandleSubscribe(conn *types.Connection, request types.SubscriptionRequest) *types.RpcError {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Validate and add stream subscriptions
	for _, stream := range request.Streams {
		if !validStreams[stream] {
			return &types.RpcError{
				Code:    types.RpcINVALID_PARAMS,
				Message: "Unknown stream type: " + string(stream),
			}
		}
		conn.Subscriptions[stream] = types.SubscriptionConfig{}
	}

	if len(request.Accounts) > 0 {
		// Validate all accounts first
		for _, acc := range request.Accounts {
			if !isValidXRPLAddress(acc) {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "Invalid account address: " + acc,
				}
			}
		}

		// Merge with existing accounts if already subscribed
		existing, ok := conn.Subscriptions[types.SubAccounts]
		accounts := request.Accounts
		if ok {
			// Append new accounts avoiding duplicates
			existingMap := make(map[string]bool)
			for _, acc := range existing.Accounts {
				existingMap[acc] = true
			}
			for _, acc := range request.Accounts {
				if !existingMap[acc] {
					accounts = append(accounts, acc)
				}
			}
		}
		conn.Subscriptions[types.SubAccounts] = types.SubscriptionConfig{
			Accounts: accounts,
		}
	}

	if len(request.AccountsProposed) > 0 {
		// Validate all accounts first
		for _, acc := range request.AccountsProposed {
			if !isValidXRPLAddress(acc) {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "Invalid account address: " + acc,
				}
			}
		}
		// Store in a separate subscription type (using accounts for now)
		conn.Subscriptions["accounts_proposed"] = types.SubscriptionConfig{
			Accounts: request.AccountsProposed,
		}
	}

	if len(request.Books) > 0 {
		for _, book := range request.Books {
			// Validate taker_gets
			if book.TakerGets == nil {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "Missing taker_gets in book subscription",
				}
			}
			// Validate taker_pays
			if book.TakerPays == nil {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "Missing taker_pays in book subscription",
				}
			}

			// Parse and validate currency specs
			var takerGets, takerPays types.CurrencySpec
			if err := json.Unmarshal(book.TakerGets, &takerGets); err != nil {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: fmt.Sprintf("Invalid taker_gets: %v", err),
				}
			}
			if err := json.Unmarshal(book.TakerPays, &takerPays); err != nil {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: fmt.Sprintf("Invalid taker_pays: %v", err),
				}
			}

			// Validate issuer for non-XRP currencies
			if takerGets.Currency != "XRP" && takerGets.Issuer == "" {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "taker_gets: issuer required for non-XRP currency",
				}
			}
			if takerPays.Currency != "XRP" && takerPays.Issuer == "" {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "taker_pays: issuer required for non-XRP currency",
				}
			}

			// Validate issuer format if provided
			if takerGets.Issuer != "" && !isValidXRPLAddress(takerGets.Issuer) {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "taker_gets: invalid issuer address",
				}
			}
			if takerPays.Issuer != "" && !isValidXRPLAddress(takerPays.Issuer) {
				return &types.RpcError{
					Code:    types.RpcINVALID_PARAMS,
					Message: "taker_pays: invalid issuer address",
				}
			}

			conn.Subscriptions[types.SubOrderBooks] = types.SubscriptionConfig{
				Books:     request.Books,
				TakerGets: &takerGets,
				TakerPays: &takerPays,
				Snapshot:  book.Snapshot,
				Both:      book.Both,
			}
		}
	}

	// Handle URL subscriptions
	if request.URL != "" {
		conn.URLSubscription = request.URL
	}

	return nil
}

// isValidXRPLAddress checks if a string is a valid XRPL address
func isValidXRPLAddress(addr string) bool {
	return addresscodec.IsValidClassicAddress(addr)
}

// HandleUnsubscribe handles an unsubscribe request for a connection
func (sm *Manager) HandleUnsubscribe(conn *types.Connection, request types.SubscriptionRequest) *types.RpcError {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, stream := range request.Streams {
		delete(conn.Subscriptions, stream)
	}

	if len(request.Accounts) > 0 {
		if existing, ok := conn.Subscriptions[types.SubAccounts]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range request.Accounts {
				accountsToRemove[acc] = true
			}
			var remainingAccounts []string
			for _, acc := range existing.Accounts {
				if !accountsToRemove[acc] {
					remainingAccounts = append(remainingAccounts, acc)
				}
			}
			if len(remainingAccounts) > 0 {
				conn.Subscriptions[types.SubAccounts] = types.SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, types.SubAccounts)
			}
		}
	}

	// Remove specific accounts_proposed subscriptions
	if len(request.AccountsProposed) > 0 {
		if existing, ok := conn.Subscriptions["accounts_proposed"]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range request.AccountsProposed {
				accountsToRemove[acc] = true
			}
			var remainingAccounts []string
			for _, acc := range existing.Accounts {
				if !accountsToRemove[acc] {
					remainingAccounts = append(remainingAccounts, acc)
				}
			}
			if len(remainingAccounts) > 0 {
				conn.Subscriptions["accounts_proposed"] = types.SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, "accounts_proposed")
			}
		}
	}

	if len(request.Books) > 0 {
		delete(conn.Subscriptions, types.SubOrderBooks)
	}

	// Handle URL unsubscription
	if request.URL != "" {
		conn.URLSubscription = ""
	}

	return nil
}

// BroadcastToStream sends a message to every connection subscribed to a
// stream. Broadcasts snapshot subscriber connections under sm.mu, then
// send after the lock is released — a slow consumer never stalls
// HandleSubscribe / HandleUnsubscribe / RemoveConnection or other
// broadcasts (#428 race fix). Delivery uses types.Connection.TrySend so
// the per-connection consecutive-drop counter is updated and the
// connection is disconnected after MaxConsecutiveDrops back-to-back
// failures — unifies the slow-consumer policy across all outbound paths.
func (sm *Manager) BroadcastToStream(streamType types.SubscriptionType, data []byte, _ interface{}) {
	deliver(sm.collectStreamTargets(streamType), data)
}

func (sm *Manager) collectStreamTargets(streamType types.SubscriptionType) []*types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	targets := make([]*types.Connection, 0, len(sm.Connections))
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			targets = append(targets, conn)
		}
	}
	return targets
}

func deliver(targets []*types.Connection, data []byte) {
	for _, c := range targets {
		c.TrySend(data)
	}
}

// BroadcastToAccounts sends a message to every connection subscribed to
// any of the named accounts on the SubAccounts stream.
func (sm *Manager) BroadcastToAccounts(data []byte, accounts []string) {
	deliver(sm.collectAccountTargets(types.SubAccounts, accounts), data)
}

// BroadcastToAccountsProposed sends a message to accounts_proposed
// subscribers.
func (sm *Manager) BroadcastToAccountsProposed(data []byte, accounts []string) {
	deliver(sm.collectAccountTargets("accounts_proposed", accounts), data)
}

func (sm *Manager) collectAccountTargets(stream types.SubscriptionType, accounts []string) []*types.Connection {
	if len(accounts) == 0 {
		return nil
	}
	accountSet := make(map[string]bool, len(accounts))
	for _, acc := range accounts {
		accountSet[acc] = true
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	var targets []*types.Connection
	for _, conn := range sm.Connections {
		cfg, ok := conn.Subscriptions[stream]
		if !ok {
			continue
		}
		for _, subAcc := range cfg.Accounts {
			if accountSet[subAcc] {
				targets = append(targets, conn)
				break
			}
		}
	}
	return targets
}

// BroadcastToOrderBook sends a message to order book subscribers whose
// configured TakerGets/TakerPays match the broadcast's currency pair.
func (sm *Manager) BroadcastToOrderBook(data []byte, takerGets, takerPays types.CurrencySpec) {
	deliver(sm.collectOrderBookTargets(takerGets, takerPays), data)
}

func (sm *Manager) collectOrderBookTargets(takerGets, takerPays types.CurrencySpec) []*types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	var targets []*types.Connection
	for _, conn := range sm.Connections {
		cfg, ok := conn.Subscriptions[types.SubOrderBooks]
		if !ok || cfg.TakerGets == nil || cfg.TakerPays == nil {
			continue
		}
		if cfg.TakerGets.Currency == takerGets.Currency &&
			cfg.TakerGets.Issuer == takerGets.Issuer &&
			cfg.TakerPays.Currency == takerPays.Currency &&
			cfg.TakerPays.Issuer == takerPays.Issuer {
			targets = append(targets, conn)
		}
	}
	return targets
}

// GetSubscriberCount returns the number of subscribers for a stream type
func (sm *Manager) GetSubscriberCount(streamType types.SubscriptionType) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	count := 0
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			count++
		}
	}
	return count
}

// ConnectionCount returns the number of active connections
func (sm *Manager) ConnectionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.Connections)
}

// GetConnection returns a connection by ID
func (sm *Manager) GetConnection(connID string) *types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.Connections[connID]
}

// IsSubscribed checks if a connection is subscribed to a stream type
func (sm *Manager) IsSubscribed(connID string, streamType types.SubscriptionType) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return false
	}
	_, ok := conn.Subscriptions[streamType]
	return ok
}

// GetConnectionSubscriptions returns the subscriptions for a connection
func (sm *Manager) GetConnectionSubscriptions(connID string) map[types.SubscriptionType]types.SubscriptionConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return nil
	}
	return conn.Subscriptions
}

// GetSubscribeResponse creates a subscribe confirmation response
func (sm *Manager) GetSubscribeResponse(ledgerIndex uint32, ledgerHash string, ledgerTime uint32, feeBase uint64, reserveBase uint64, reserveInc uint64) types.SubscribeResponse {
	return types.SubscribeResponse{
		Status:      "success",
		LedgerIndex: ledgerIndex,
		LedgerHash:  ledgerHash,
		LedgerTime:  ledgerTime,
		FeeBase:     feeBase,
		ReserveBase: reserveBase,
		ReserveInc:  reserveInc,
	}
}
