package types

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
)

// XRPL API Version constants
const (
	ApiVersion1 = 1
	ApiVersion2 = 2
	ApiVersion3 = 3
	// DefaultApiVersion is the version assumed when a request omits
	// api_version. Matches rippled's apiVersionIfUnspecified = 1
	// (ApiVersion.h): every request expecting a v2+ response shape must
	// set api_version explicitly.
	DefaultApiVersion = ApiVersion1
	// MaxSupportedApiVersion is the highest non-beta version a request may
	// reach (rippled apiMaximumSupportedVersion).
	MaxSupportedApiVersion = ApiVersion2
	// BetaApiVersion is the highest version accepted only when the
	// beta_rpc_api config knob is set (rippled apiBetaVersion).
	BetaApiVersion = ApiVersion3
)

// Role-based access control matching rippled's Role enum (Role.h).
// Numeric ordering is not meaningful — callers must compare roles by
// name (e.g. `role == RoleAdmin`) rather than `<` / `>`.
type Role int

const (
	RoleGuest Role = iota
	RoleUser
	RoleAdmin
	// RoleIdentified is granted to requests arriving from a configured
	// secure_gateway peer carrying an X-User header. Identified callers
	// have unlimited resources (matches rippled isUnlimited / Role.cpp).
	RoleIdentified
	// RoleProxy is granted to requests arriving from a secure_gateway
	// peer with no X-User header. Used for client-IP attribution; not
	// resource-unlimited.
	RoleProxy
)

// IsUnlimited reports whether the role exempts the request from
// resource limits. Mirrors rippled isUnlimited() in Role.cpp:124-128:
// only ADMIN and IDENTIFIED qualify.
func (r Role) IsUnlimited() bool {
	return r == RoleAdmin || r == RoleIdentified
}

// Condition represents the preconditions required by an RPC method.
// Matches rippled's Condition enum in Handler.h.
// When the server is amendment-blocked, methods with any condition
// other than NoCondition are blocked with rpcAMENDMENT_BLOCKED.
type Condition int

const (
	// NoCondition - method has no preconditions, always available even when amendment blocked
	NoCondition Condition = iota
	// NeedsNetworkConnection - method requires network sync
	NeedsNetworkConnection
	// NeedsCurrentLedger - method requires access to the current open ledger
	NeedsCurrentLedger
	// NeedsClosedLedger - method requires access to the last closed ledger
	NeedsClosedLedger
)

// PeerSource produces the data the `peers` RPC returns. PeersJSON
// emits one entry per connected peer; ClusterJSON populates the
// top-level cluster object (rippled doPeers Peers.cpp:59-80) with
// each [cluster_nodes] member except the local node. PeerCount feeds
// server_info.peers from the same underlying source.
type PeerSource interface {
	PeersJSON() []map[string]any
	ClusterJSON() map[string]any
	PeerCount() int
}

// RPC Context contains request-specific information
type RpcContext struct {
	Context    context.Context
	Role       Role
	ApiVersion int
	// IsAdmin gates admin-only commands. True iff Role == RoleAdmin.
	IsAdmin bool
	// Unlimited skips per-request resource limits (page sizes, etc.).
	// True for RoleAdmin and RoleIdentified, matching rippled
	// isUnlimited() in Role.cpp.
	Unlimited  bool
	ClientIP   string
	PeerSource PeerSource
	// Services is the per-request service container handlers read to
	// reach the ledger service, dispatcher, manifest cache, etc. The
	// HTTP/WebSocket dispatchers populate this from the server's wired
	// container; tests construct RpcContext directly with whatever
	// fixtures they need. Replaces the former package-level
	// types.Services global.
	Services *ServiceContainer
	// LoadWarning is set by the post-dispatch load charge when the caller
	// crosses the resource warn threshold. Transport writers surface it as
	// the top-level warning:"load" field, mirroring rippled's
	// `if (consumer.warn()) jr[warning] = load`.
	LoadWarning bool
}

// Method handler interface - all RPC methods implement this
type MethodHandler interface {
	Handle(ctx *RpcContext, params json.RawMessage) (any, *RpcError)
	RequiredRole() Role
	SupportedApiVersions() []int
	RequiredCondition() Condition
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

// API Warning IDs as defined in XRPL documentation
const (
	WarningUnsupportedAmendmentsMajority = 1001 // Unsupported amendments have reached majority
	WarningAmendmentBlocked              = 1002 // This server is amendment blocked
	WarningClioServer                    = 2001 // This is a clio server
)

// WarningObject represents an API warning in responses
type WarningObject struct {
	ID      int            `json:"id"`                // Unique numeric code for this warning
	Message string         `json:"message"`           // Human-readable description
	Details map[string]any `json:"details,omitempty"` // Additional warning-specific information
}

// WebSocketCommand is assembled by the WS read loop from the decoded
// message: Command/ID are lifted from the top level and Params holds the
// remaining fields. It is never JSON-(un)marshalled directly, so Params
// carries no wire tag.
type WebSocketCommand struct {
	Command string
	ID      any
	Params  json.RawMessage
}

// WebSocketResponse represents an XRPL WebSocket API response
type WebSocketResponse struct {
	Status       string          `json:"status"`
	Type         string          `json:"type"`
	Result       any             `json:"result,omitempty"`
	ID           any             `json:"id,omitempty"`
	Warning      string          `json:"warning,omitempty"`
	Warnings     []WarningObject `json:"warnings,omitempty"`
	Forwarded    bool            `json:"forwarded,omitempty"`
	ApiVersion   int             `json:"api_version,omitempty"`
	Error        string          `json:"error,omitempty"`
	ErrorCode    int             `json:"error_code,omitempty"`
	ErrorMessage string          `json:"error_message,omitempty"`
	// Request echoes the original command back on an error reply. rippled's
	// WS missingCommand path returns the unparsed request alongside the
	// error token (ServerHandler.cpp:457).
	Request any `json:"request,omitempty"`
}

// Subscription types for WebSocket streams. Rippled's per-book stream
// is keyed "book" (Subscribe.cpp:231-356); the per-ledger aggregate
// stream is keyed "book_changes" (Subscribe.cpp:139-142). Earlier
// revisions of this package collapsed both into SubOrderBooks="book_changes",
// which meant per-book subscriptions silently landed on the wrong key
// and the aggregate stream was unreachable. Split them into two
// distinct constants.
type SubscriptionType string

const (
	SubLedger               SubscriptionType = "ledger"
	SubTransactions         SubscriptionType = "transactions"
	SubTransactionsProposed SubscriptionType = "transactions_proposed"
	SubAccounts             SubscriptionType = "accounts"
	SubBook                 SubscriptionType = "book"
	SubBookChanges          SubscriptionType = "book_changes"
	SubValidations          SubscriptionType = "validations"
	SubManifests            SubscriptionType = "manifests"
	SubPeerStatus           SubscriptionType = "peer_status"
	SubServer               SubscriptionType = "server"
	SubConsensus            SubscriptionType = "consensus"
	SubPath                 SubscriptionType = "path_find"
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
	// Username / Password are the deprecated aliases rippled still accepts
	// for url_username / url_password. When present they take precedence,
	// and they alone trigger credential updates on an already-registered
	// url subscription (doSubscribe's reuse branch only checks them).
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`

	// wire holds the as-received JSON of the array-valued fields, captured at
	// decode time. The typed slices above collapse the four shapes rippled
	// distinguishes via isMember/isArray — absent, null, non-array, and empty
	// array — into a single nil-or-empty slice, so the subscription manager
	// reads wire to reproduce rippled's per-shape error codes. nil when the
	// request was built directly in Go rather than decoded from the wire.
	// url/username/password presence is captured too: rippled branches on
	// isMember, so an empty-string url still selects the url branch.
	wire *wireSubscriptionArrays
}

type wireSubscriptionArrays struct {
	streams          json.RawMessage
	accounts         json.RawMessage
	accountsProposed json.RawMessage
	books            json.RawMessage
	url              json.RawMessage
	username         json.RawMessage
	password         json.RawMessage
}

// WireSubscriptionArrays exposes the raw JSON the wire carried for the
// array-valued subscription fields. Present is false when the request was not
// decoded from JSON, in which case the manager falls back to the typed slices.
type WireSubscriptionArrays struct {
	Present          bool
	Streams          json.RawMessage
	Accounts         json.RawMessage
	AccountsProposed json.RawMessage
	Books            json.RawMessage
}

// WireArrays returns the raw array-field JSON captured by UnmarshalJSON.
func (r *SubscriptionRequest) WireArrays() WireSubscriptionArrays {
	if r.wire == nil {
		return WireSubscriptionArrays{}
	}
	return WireSubscriptionArrays{
		Present:          true,
		Streams:          r.wire.streams,
		Accounts:         r.wire.accounts,
		AccountsProposed: r.wire.accountsProposed,
		Books:            r.wire.books,
	}
}

// UnmarshalJSON captures the raw JSON of the array-valued fields before
// decoding the typed fields, so the subscription manager can apply rippled's
// per-field, per-shape error codes — a typed slice cannot tell an absent field
// from a null, an empty array, or a non-array value. Shape mismatches on the
// array fields are tolerated here (left for the manager to report with the
// correct code) rather than failing the whole decode; scalars decode normally.
func (r *SubscriptionRequest) UnmarshalJSON(data []byte) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	r.wire = &wireSubscriptionArrays{
		streams:          m["streams"],
		accounts:         m["accounts"],
		accountsProposed: m["accounts_proposed"],
		books:            m["books"],
		url:              m["url"],
		username:         m["username"],
		password:         m["password"],
	}
	_ = json.Unmarshal(m["streams"], &r.Streams)
	_ = json.Unmarshal(m["accounts"], &r.Accounts)
	_ = json.Unmarshal(m["accounts_proposed"], &r.AccountsProposed)
	_ = json.Unmarshal(m["books"], &r.Books)
	_ = json.Unmarshal(m["url"], &r.URL)
	_ = json.Unmarshal(m["url_username"], &r.URLUsername)
	_ = json.Unmarshal(m["url_password"], &r.URLPassword)
	_ = json.Unmarshal(m["username"], &r.Username)
	_ = json.Unmarshal(m["password"], &r.Password)
	return nil
}

// HasURL reports whether the request selects rippled's url (RPCSub) branch.
// For wire-decoded requests this is member presence — an empty-string url
// still takes the branch (and then fails url parsing); for Go-built requests
// a non-empty URL stands in for presence.
func (r *SubscriptionRequest) HasURL() bool {
	if r.wire != nil {
		return r.wire.url != nil
	}
	return r.URL != ""
}

// URLCredentials resolves the basic-auth credentials for a url subscription
// the way doSubscribe does: url_username / url_password, overridden by the
// deprecated username / password members when present. usernameSet and
// passwordSet report the deprecated members' presence — on an existing url
// subscription only those trigger credential updates.
func (r *SubscriptionRequest) URLCredentials() (username, password string, usernameSet, passwordSet bool) {
	username, password = r.URLUsername, r.URLPassword
	if r.wire != nil {
		usernameSet = r.wire.username != nil
		passwordSet = r.wire.password != nil
	} else {
		usernameSet = r.Username != ""
		passwordSet = r.Password != ""
	}
	if usernameSet {
		username = r.Username
	}
	if passwordSet {
		password = r.Password
	}
	return username, password, usernameSet, passwordSet
}

// Book request for order book subscriptions
type BookRequest struct {
	TakerPays json.RawMessage `json:"taker_pays"`
	TakerGets json.RawMessage `json:"taker_gets"`
	Snapshot  bool            `json:"snapshot,omitempty"`
	Both      bool            `json:"both,omitempty"`
	// Taker is the perspective account used when computing snapshot
	// quality (sfTaker on rippled Subscribe.cpp:282-299). Optional; an
	// empty value means "anonymous" — the same default rippled uses
	// when the field is absent. Validated against XRPL-address format
	// in subscription.HandleSubscribe.
	Taker string `json:"taker,omitempty"`
	// Domain optionally scopes the book to a permissioned domain
	// (Subscribe.cpp:308-320). Carried as the uint256 hex string the
	// client sent; parse-validated in subscription.HandleSubscribe.
	Domain string `json:"domain,omitempty"`
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
	Limit  uint32 `json:"limit,omitempty"`
	Marker any    `json:"marker,omitempty"`
}

// Currency specification
type Currency struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}

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
	Taker     string        `json:"taker,omitempty"`
	// For URL subscriptions
	URL      string `json:"url,omitempty"`
	Username string `json:"url_username,omitempty"`
	Password string `json:"url_password,omitempty"`
}

// MaxConsecutiveDrops is the number of back-to-back send failures
// after which a subscriber is considered terminally slow and is
// disconnected via Connection.Disconnect. Mirrors rippled's
// approach in Resource::Manager: warn-then-drop, but applied per
// outbound queue rather than per inbound charge balance.
const MaxConsecutiveDrops = 8

// Connection represents a WebSocket connection for subscription
// management. The struct is shared between subscription.Manager and
// the WebSocket server so both observe the same drop counter and
// disconnect callback — eliminates the double-bookkeeping pattern
// flagged in the #428 audit.
type Connection struct {
	ID            string
	Subscriptions map[SubscriptionType]SubscriptionConfig
	SendChannel   chan []byte
	CloseChannel  chan struct{}
	// Disconnect is invoked when MaxConsecutiveDrops is reached. The
	// WS layer populates this with its per-conn cancel func so a
	// persistently slow client gets torn down once, in one place.
	Disconnect func()

	// EncodeOutbound, when set, transforms each event at the single
	// enqueue point before it is queued. url (RPCSub) subscriptions use
	// it to stamp the per-url sequence number at enqueue, mirroring
	// rippled's mSeq++ in send(): a number is consumed even when the
	// bounded queue then drops the event, so the remote sees a gap.
	EncodeOutbound func([]byte) []byte

	// consecutiveDrops counts back-to-back send failures. Reset to 0
	// on every successful TrySend.
	consecutiveDrops atomic.Int32
}

// TrySend pushes data onto SendChannel without blocking. Returns true
// when delivered; on failure increments the consecutive-drop counter
// and, if the counter reaches MaxConsecutiveDrops, invokes Disconnect
// exactly once. Same policy is used by every outbound path (broadcasts,
// per-request responses) so HTTP and WebSocket no longer have
// inconsistent slow-consumer handling.
func (c *Connection) TrySend(data []byte) bool {
	if c == nil || c.SendChannel == nil {
		return false
	}
	if c.EncodeOutbound != nil {
		data = c.EncodeOutbound(data)
	}
	select {
	case c.SendChannel <- data:
		c.consecutiveDrops.Store(0)
		return true
	default:
		if c.consecutiveDrops.Add(1) >= MaxConsecutiveDrops {
			if c.Disconnect != nil {
				c.Disconnect()
			}
		}
		return false
	}
}

// WebSocketResponseOptions contains optional fields for WebSocket responses
type WebSocketResponseOptions struct {
	Warning   string          // "load" when approaching rate limit
	Warnings  []WarningObject // Array of warning objects
	Forwarded bool            // True if forwarded from Clio to P2P server
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

// IsValidXRPLAddress validates an XRPL address using the address codec
func IsValidXRPLAddress(address string) bool {
	return addresscodec.IsValidAddress(address)
}

// IsValidClassicAddress reports whether address is a valid classic
// (base58check AccountID) address, rejecting X-addresses. Matches the set
// rippled's parseBase58<AccountID> accepts.
func IsValidClassicAddress(address string) bool {
	return addresscodec.IsValidClassicAddress(address)
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

// LedgerInfoProvider provides current ledger info for subscribe responses
type LedgerInfoProvider interface {
	GetCurrentLedgerInfo() *LedgerSubscribeInfo
}

// LedgerSubscribeInfo contains ledger info returned in the subscribe
// response for the `ledger` stream. Field set mirrors rippled's
// subLedger ack (NetworkOPs::subLedger): fee_ref is emitted only when
// the XRPFees amendment is disabled, and network_id is always present.
// The per-ledger streamed event uses LedgerCloseEvent and carries
// additional fields (txn_count, etc.).
type LedgerSubscribeInfo struct {
	LedgerIndex      uint32 `json:"ledger_index"`
	LedgerHash       string `json:"ledger_hash"`
	LedgerTime       uint32 `json:"ledger_time"`
	FeeBase          uint64 `json:"fee_base"`
	FeeRef           uint64 `json:"fee_ref"`
	ReserveBase      uint64 `json:"reserve_base"`
	ReserveInc       uint64 `json:"reserve_inc"`
	ValidatedLedgers string `json:"validated_ledgers,omitempty"`
	NetworkID        uint32 `json:"network_id"`
	// XRPFeesEnabled gates fee_ref: rippled emits the deprecated fee_ref
	// only while the XRPFees amendment is disabled.
	XRPFeesEnabled bool `json:"-"`
}
