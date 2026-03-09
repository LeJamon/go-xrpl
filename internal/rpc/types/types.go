package types

import (
	"context"
	"encoding/json"
	"fmt"

	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
)

// XRPL API Version constants
const (
	ApiVersion1       = 1
	ApiVersion2       = 2
	ApiVersion3       = 3
	DefaultApiVersion = ApiVersion1
)

// Role-based access control matching rippled
type Role int

const (
	RoleGuest Role = iota
	RoleUser
	RoleAdmin
	RoleIdentified
)

// RPC Context contains request-specific information
type RpcContext struct {
	Context    context.Context
	Role       Role
	ApiVersion int
	IsAdmin    bool
	ClientIP   string
}

// Method handler interface - all RPC methods implement this
type MethodHandler interface {
	Handle(ctx *RpcContext, params json.RawMessage) (interface{}, *RpcError)
	RequiredRole() Role
	SupportedApiVersions() []int
}

// Method registry for dynamic method registration
type MethodRegistry struct {
	methods map[string]MethodHandler
}

func NewMethodRegistry() *MethodRegistry {
	return &MethodRegistry{
		methods: make(map[string]MethodHandler),
	}
}

func (r *MethodRegistry) Register(name string, handler MethodHandler) {
	r.methods[name] = handler
}

func (r *MethodRegistry) Get(name string) (MethodHandler, bool) {
	handler, exists := r.methods[name]
	return handler, exists
}

func (r *MethodRegistry) List() []string {
	methods := make([]string, 0, len(r.methods))
	for name := range r.methods {
		methods = append(methods, name)
	}
	return methods
}

// LedgerIndex is a custom type that can unmarshal from either a JSON number or string
// This matches XRPL API behavior where ledger_index can be: 12345, "12345", "validated", "current", "closed"
type LedgerIndex string

// UnmarshalJSON implements custom unmarshaling for LedgerIndex
func (li *LedgerIndex) UnmarshalJSON(data []byte) error {
	// First try to unmarshal as a string (handles "validated", "current", "closed", "12345")
	var strVal string
	if err := json.Unmarshal(data, &strVal); err == nil {
		*li = LedgerIndex(strVal)
		return nil
	}

	// Try to unmarshal as a number
	var numVal uint64
	if err := json.Unmarshal(data, &numVal); err == nil {
		*li = LedgerIndex(fmt.Sprintf("%d", numVal))
		return nil
	}

	// If both fail, return an error
	return fmt.Errorf("ledger_index must be a number or string, got: %s", string(data))
}

// String returns the string representation of the LedgerIndex
func (li LedgerIndex) String() string {
	return string(li)
}

// LedgerSpecifier - used to specify which ledger to query
type LedgerSpecifier struct {
	LedgerHash  string      `json:"ledger_hash,omitempty"`
	LedgerIndex LedgerIndex `json:"ledger_index,omitempty"` // can be number or "validated", "current", "closed"
}

// JSON-RPC 2.0 Request
type JsonRpcRequest struct {
	JsonRpc string          `json:"jsonrpc"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
	ID      interface{}     `json:"id,omitempty"`
}

// JSON-RPC 2.0 Response
type JsonRpcResponse struct {
	JsonRpc string      `json:"jsonrpc"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RpcError   `json:"error,omitempty"`
	ID      interface{} `json:"id,omitempty"`
}

// Base response structure for XRPL RPC responses
type BaseResponse struct {
	// Standard fields present in most responses
	Status        string `json:"status,omitempty"`
	Type          string `json:"type,omitempty"`
	Validated     *bool  `json:"validated,omitempty"`
	LedgerHash    string `json:"ledger_hash,omitempty"`
	LedgerIndex   uint32 `json:"ledger_index,omitempty"`
	LedgerCurrent uint32 `json:"ledger_current_index,omitempty"`
}

// API Warning IDs as defined in XRPL documentation
const (
	WarningUnsupportedAmendmentsMajority = 1001 // Unsupported amendments have reached majority
	WarningAmendmentBlocked              = 1002 // This server is amendment blocked
	WarningClioServer                    = 2001 // This is a clio server
)

// WarningObject represents an API warning in responses
type WarningObject struct {
	ID      int                    `json:"id"`                // Unique numeric code for this warning
	Message string                 `json:"message"`           // Human-readable description
	Details map[string]interface{} `json:"details,omitempty"` // Additional warning-specific information
}

// WebSocket specific structures
type WebSocketCommand struct {
	Command    string          `json:"command"`
	ID         interface{}     `json:"id,omitempty"`
	ApiVersion *int            `json:"api_version,omitempty"`
	Params     json.RawMessage `json:",inline,omitempty"`
}

// WebSocketResponse represents an XRPL WebSocket API response
type WebSocketResponse struct {
	Status     string          `json:"status"`
	Type       string          `json:"type"`
	Result     interface{}     `json:"result,omitempty"`
	ID         interface{}     `json:"id,omitempty"`
	Warning    string          `json:"warning,omitempty"`
	Warnings   []WarningObject `json:"warnings,omitempty"`
	Forwarded  bool            `json:"forwarded,omitempty"`
	ApiVersion int             `json:"api_version,omitempty"`
	Error      string          `json:"error,omitempty"`
	ErrorCode  int             `json:"error_code,omitempty"`
	ErrorMessage string        `json:"error_message,omitempty"`
}

// Subscription types for WebSocket streams
type SubscriptionType string

const (
	SubLedger       SubscriptionType = "ledger"
	SubTransactions SubscriptionType = "transactions"
	SubAccounts     SubscriptionType = "accounts"
	SubOrderBooks   SubscriptionType = "book_changes"
	SubValidations  SubscriptionType = "validations"
	SubManifests    SubscriptionType = "manifests"
	SubPeerStatus   SubscriptionType = "peer_status"
	SubConsensus    SubscriptionType = "consensus"
	SubPath         SubscriptionType = "path_find"
)

// Subscription request structure
type SubscriptionRequest struct {
	Streams          []SubscriptionType `json:"streams,omitempty"`
	Accounts         []string           `json:"accounts,omitempty"`
	AccountsProposed []string           `json:"accounts_proposed,omitempty"`
	Books            []BookRequest      `json:"books,omitempty"`
	URL              string             `json:"url,omitempty"`
	URLUsername      string             `json:"url_username,omitempty"`
	URLPassword      string             `json:"url_password,omitempty"`
}

// Book request for order book subscriptions
type BookRequest struct {
	TakerPays json.RawMessage `json:"taker_pays"`
	TakerGets json.RawMessage `json:"taker_gets"`
	Snapshot  bool            `json:"snapshot,omitempty"`
	Both      bool            `json:"both,omitempty"`
}

// Stream message types
type StreamMessage struct {
	Type        string          `json:"type"`
	LedgerIndex uint32          `json:"ledger_index,omitempty"`
	LedgerHash  string          `json:"ledger_hash,omitempty"`
	LedgerTime  uint32          `json:"ledger_time,omitempty"`
	FeeBase     uint32          `json:"fee_base,omitempty"`
	FeeRef      uint32          `json:"fee_ref,omitempty"`
	ReserveBase uint32          `json:"reserve_base,omitempty"`
	ReserveInc  uint32          `json:"reserve_inc,omitempty"`
	Validated   bool            `json:"validated,omitempty"`
	Transaction json.RawMessage `json:"transaction,omitempty"`
	Meta        json.RawMessage `json:"meta,omitempty"`
	Account     string          `json:"account,omitempty"`
}

// Common parameter structures

// Account parameter
type AccountParam struct {
	Account string `json:"account"`
}

// Transaction identifier
type TransactionParam struct {
	Transaction string `json:"transaction"`
	Binary      bool   `json:"binary,omitempty"`
}

// Pagination parameters
type PaginationParams struct {
	Limit  uint32      `json:"limit,omitempty"`
	Marker interface{} `json:"marker,omitempty"`
}

// Currency specification
type Currency struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}

// RawAmount can be drops (string) or IOU object (used for JSON parsing)
type RawAmount json.RawMessage

// Path specification for path finding
type Path []PathStep

type PathStep struct {
	Account  string `json:"account,omitempty"`
	Currency string `json:"currency,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
	Type     uint8  `json:"type,omitempty"`
	TypeHex  string `json:"type_hex,omitempty"`
}

// Quality specification
type Quality struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
	Value    string `json:"value"`
}

// Memo structure
type Memo struct {
	MemoData   string `json:"MemoData,omitempty"`
	MemoFormat string `json:"MemoFormat,omitempty"`
	MemoType   string `json:"MemoType,omitempty"`
}

// Signer structure
type Signer struct {
	Signer struct {
		Account       string `json:"Account"`
		TxnSignature  string `json:"TxnSignature"`
		SigningPubKey string `json:"SigningPubKey"`
	} `json:"Signer"`
}

// CurrencySpec represents a currency specification for order book subscriptions
type CurrencySpec struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}

// SubscriptionConfig holds configuration for a specific subscription
type SubscriptionConfig struct {
	// For account subscriptions
	Accounts []string `json:"accounts,omitempty"`
	// For book subscriptions (multiple books)
	Books []BookRequest `json:"books,omitempty"`
	// For single book subscription (legacy)
	TakerGets *CurrencySpec `json:"taker_gets,omitempty"`
	TakerPays *CurrencySpec `json:"taker_pays,omitempty"`
	Snapshot  bool          `json:"snapshot,omitempty"`
	Both      bool          `json:"both,omitempty"`
	// For URL subscriptions
	URL      string `json:"url,omitempty"`
	Username string `json:"url_username,omitempty"`
	Password string `json:"url_password,omitempty"`
}

// Connection represents a WebSocket connection for subscription management
type Connection struct {
	ID              string
	Subscriptions   map[SubscriptionType]SubscriptionConfig
	SendChannel     chan []byte
	CloseChannel    chan struct{}
	URLSubscription string // URL for server-to-server subscriptions
}

// WebSocketResponseOptions contains optional fields for WebSocket responses
type WebSocketResponseOptions struct {
	Warning   string          // "load" when approaching rate limit
	Warnings  []WarningObject // Array of warning objects
	Forwarded bool            // True if forwarded from Clio to P2P server
}

// SubscriptionManager manages WebSocket subscriptions
type SubscriptionManager struct {
	Connections map[string]*Connection
}

// NewSubscriptionManager creates a new SubscriptionManager
func NewSubscriptionManager() *SubscriptionManager {
	return &SubscriptionManager{
		Connections: make(map[string]*Connection),
	}
}

// AddConnection adds a connection to the subscription manager
func (sm *SubscriptionManager) AddConnection(conn *Connection) {
	if sm.Connections == nil {
		sm.Connections = make(map[string]*Connection)
	}
	sm.Connections[conn.ID] = conn
}

// RemoveConnection removes a connection from the subscription manager
func (sm *SubscriptionManager) RemoveConnection(connID string) {
	delete(sm.Connections, connID)
}

// validStreams contains the set of valid stream types
var validStreams = map[SubscriptionType]bool{
	SubLedger:       true,
	SubTransactions: true,
	SubAccounts:     true,
	SubOrderBooks:   true,
	SubValidations:  true,
	SubManifests:    true,
	SubPeerStatus:   true,
	SubConsensus:    true,
	SubPath:         true,
}

// HandleSubscribe handles a subscribe request for a connection
func (sm *SubscriptionManager) HandleSubscribe(conn *Connection, request SubscriptionRequest) *RpcError {
	// Validate and add stream subscriptions
	for _, stream := range request.Streams {
		if !validStreams[stream] {
			return &RpcError{
				Code:    RpcINVALID_PARAMS,
				Message: "Unknown stream type: " + string(stream),
			}
		}
		conn.Subscriptions[stream] = SubscriptionConfig{}
	}

	// Add account subscriptions
	if len(request.Accounts) > 0 {
		// Validate all accounts first
		for _, acc := range request.Accounts {
			if !isValidXRPLAddress(acc) {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Invalid account address: " + acc,
				}
			}
		}

		// Merge with existing accounts if already subscribed
		existing, ok := conn.Subscriptions[SubAccounts]
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
		conn.Subscriptions[SubAccounts] = SubscriptionConfig{
			Accounts: accounts,
		}
	}

	// Add accounts_proposed subscriptions
	if len(request.AccountsProposed) > 0 {
		// Validate all accounts first
		for _, acc := range request.AccountsProposed {
			if !isValidXRPLAddress(acc) {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Invalid account address: " + acc,
				}
			}
		}
		// Store in a separate subscription type (using accounts for now)
		conn.Subscriptions["accounts_proposed"] = SubscriptionConfig{
			Accounts: request.AccountsProposed,
		}
	}

	// Add book subscriptions
	if len(request.Books) > 0 {
		for _, book := range request.Books {
			// Validate taker_gets
			if book.TakerGets == nil {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Missing taker_gets in book subscription",
				}
			}
			// Validate taker_pays
			if book.TakerPays == nil {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Missing taker_pays in book subscription",
				}
			}

			// Parse and validate currency specs
			var takerGets, takerPays CurrencySpec
			if err := json.Unmarshal(book.TakerGets, &takerGets); err != nil {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Invalid taker_gets: " + err.Error(),
				}
			}
			if err := json.Unmarshal(book.TakerPays, &takerPays); err != nil {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "Invalid taker_pays: " + err.Error(),
				}
			}

			// Validate issuer for non-XRP currencies
			if takerGets.Currency != "XRP" && takerGets.Issuer == "" {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "taker_gets: issuer required for non-XRP currency",
				}
			}
			if takerPays.Currency != "XRP" && takerPays.Issuer == "" {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "taker_pays: issuer required for non-XRP currency",
				}
			}

			// Validate issuer format if provided
			if takerGets.Issuer != "" && !isValidXRPLAddress(takerGets.Issuer) {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "taker_gets: invalid issuer address",
				}
			}
			if takerPays.Issuer != "" && !isValidXRPLAddress(takerPays.Issuer) {
				return &RpcError{
					Code:    RpcINVALID_PARAMS,
					Message: "taker_pays: invalid issuer address",
				}
			}

			conn.Subscriptions[SubOrderBooks] = SubscriptionConfig{
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
func (sm *SubscriptionManager) HandleUnsubscribe(conn *Connection, request SubscriptionRequest) *RpcError {
	// Remove stream subscriptions
	for _, stream := range request.Streams {
		delete(conn.Subscriptions, stream)
	}

	// Remove specific account subscriptions
	if len(request.Accounts) > 0 {
		if existing, ok := conn.Subscriptions[SubAccounts]; ok {
			// Remove specific accounts from the subscription
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
				conn.Subscriptions[SubAccounts] = SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, SubAccounts)
			}
		}
	}

	// Remove book subscriptions
	if len(request.Books) > 0 {
		delete(conn.Subscriptions, SubOrderBooks)
	}

	// Handle URL unsubscription
	if request.URL != "" {
		conn.URLSubscription = ""
	}

	return nil
}

// BroadcastToStream sends a message to all connections subscribed to a stream
func (sm *SubscriptionManager) BroadcastToStream(streamType SubscriptionType, data []byte, _ interface{}) {
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			select {
			case conn.SendChannel <- data:
			default:
				// Channel full, skip
			}
		}
	}
}

// BroadcastToAccounts sends a message to all connections subscribed to any of the accounts
func (sm *SubscriptionManager) BroadcastToAccounts(data []byte, accounts []string) {
	accountSet := make(map[string]bool)
	for _, acc := range accounts {
		accountSet[acc] = true
	}

	for _, conn := range sm.Connections {
		if config, ok := conn.Subscriptions[SubAccounts]; ok {
			for _, subAcc := range config.Accounts {
				if accountSet[subAcc] {
					select {
					case conn.SendChannel <- data:
					default:
						// Channel full, skip
					}
					break
				}
			}
		}
	}
}

// BroadcastToAccountsProposed sends a message to accounts_proposed subscribers
func (sm *SubscriptionManager) BroadcastToAccountsProposed(data []byte, accounts []string) {
	// Similar to BroadcastToAccounts but for proposed transactions
	sm.BroadcastToAccounts(data, accounts)
}

// BroadcastToOrderBook sends a message to order book subscribers
func (sm *SubscriptionManager) BroadcastToOrderBook(data []byte, takerGets, takerPays CurrencySpec) {
	for _, conn := range sm.Connections {
		if config, ok := conn.Subscriptions[SubOrderBooks]; ok {
			if config.TakerGets != nil && config.TakerPays != nil {
				if config.TakerGets.Currency == takerGets.Currency &&
					config.TakerGets.Issuer == takerGets.Issuer &&
					config.TakerPays.Currency == takerPays.Currency &&
					config.TakerPays.Issuer == takerPays.Issuer {
					select {
					case conn.SendChannel <- data:
					default:
						// Channel full, skip
					}
				}
			}
		}
	}
}

// GetSubscriberCount returns the number of subscribers for a stream type
func (sm *SubscriptionManager) GetSubscriberCount(streamType SubscriptionType) int {
	count := 0
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			count++
		}
	}
	return count
}

// ConnectionCount returns the number of active connections
func (sm *SubscriptionManager) ConnectionCount() int {
	return len(sm.Connections)
}

// GetConnection returns a connection by ID
func (sm *SubscriptionManager) GetConnection(connID string) *Connection {
	return sm.Connections[connID]
}

// IsSubscribed checks if a connection is subscribed to a stream type
func (sm *SubscriptionManager) IsSubscribed(connID string, streamType SubscriptionType) bool {
	conn := sm.Connections[connID]
	if conn == nil {
		return false
	}
	_, ok := conn.Subscriptions[streamType]
	return ok
}

// GetConnectionSubscriptions returns the subscriptions for a connection
func (sm *SubscriptionManager) GetConnectionSubscriptions(connID string) map[SubscriptionType]SubscriptionConfig {
	conn := sm.Connections[connID]
	if conn == nil {
		return nil
	}
	return conn.Subscriptions
}

// SubscribeResponse represents the response to a subscribe request
type SubscribeResponse struct {
	Status      string `json:"status"`
	LedgerIndex uint32 `json:"ledger_index"`
	LedgerHash  string `json:"ledger_hash"`
	LedgerTime  uint32 `json:"ledger_time"`
	FeeBase     uint64 `json:"fee_base"`
	ReserveBase uint64 `json:"reserve_base"`
	ReserveInc  uint64 `json:"reserve_inc"`
}

// GetSubscribeResponse creates a subscribe confirmation response
func (sm *SubscriptionManager) GetSubscribeResponse(ledgerIndex uint32, ledgerHash string, ledgerTime uint32, feeBase uint64, reserveBase uint64, reserveInc uint64) SubscribeResponse {
	return SubscribeResponse{
		Status:      "success",
		LedgerIndex: ledgerIndex,
		LedgerHash:  ledgerHash,
		LedgerTime:  ledgerTime,
		FeeBase:     feeBase,
		ReserveBase: reserveBase,
		ReserveInc:  reserveInc,
	}
}

// IsValidXRPLAddress validates an XRPL address using the address codec
func IsValidXRPLAddress(address string) bool {
	return addresscodec.IsValidAddress(address)
}

// BookMatchesCurrency checks if a book request matches the given currency specs
func BookMatchesCurrency(book BookRequest, specGets, specPays CurrencySpec) bool {
	// Parse book's taker_gets and taker_pays
	var bookGets, bookPays struct {
		Currency string `json:"currency"`
		Issuer   string `json:"issuer"`
	}
	if err := json.Unmarshal(book.TakerGets, &bookGets); err != nil {
		return false
	}
	if err := json.Unmarshal(book.TakerPays, &bookPays); err != nil {
		return false
	}

	// Compare currencies and issuers
	if bookGets.Currency != specGets.Currency || bookGets.Issuer != specGets.Issuer {
		return false
	}
	if bookPays.Currency != specPays.Currency || bookPays.Issuer != specPays.Issuer {
		return false
	}

	return true
}
