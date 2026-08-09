package types

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
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

func (r Role) IsAdmin() bool {
	return r == RoleAdmin
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
	ClientIP   string
	ResourceIP string
	PeerSource PeerSource
	// Services is the per-request service container handlers read to
	// reach the ledger service, dispatcher, manifest cache, etc. The
	// HTTP/WebSocket dispatchers populate this from the server's wired
	// container; tests construct RpcContext directly with whatever
	// fixtures they need. Replaces the former package-level
	// types.Services global.
	Services *ServiceContainer
	// LoadCost is the resource charge selected while handling the request.
	// Dispatch initializes it to the reference cost; handlers raise it only
	// after reaching the equivalent rippled work boundary.
	LoadCost uint32
	// ResourceConsumer is the connection-scoped consumer used by WebSocket
	// requests. HTTP requests leave it nil and acquire a short-lived consumer.
	ResourceConsumer *resource.Consumer
	// ResourceAdmission owns the request's atomic in-flight reservation.
	ResourceAdmission *resource.Admission
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

// MethodRegistry is the immutable, published RPC method catalogue. Use a
// MethodRegistryBuilder to construct one before handing it to a transport.
type MethodRegistry struct {
	methods map[string]MethodHandler
	names   []string
}

// MethodRegistryBuilder collects RPC methods before publication. Its zero
// value is ready for use.
type MethodRegistryBuilder struct {
	methods map[string]MethodHandler
	built   bool
}

func NewMethodRegistryBuilder() *MethodRegistryBuilder {
	return &MethodRegistryBuilder{}
}

// Register adds a method to the builder. Names must be non-empty and have no
// surrounding whitespace. Handlers must be non-nil, including when an
// interface contains a typed nil value. Registration is rejected after Build.
func (b *MethodRegistryBuilder) Register(name string, handler MethodHandler) error {
	if b == nil {
		return fmt.Errorf("method registry builder is nil")
	}
	if b.built {
		return fmt.Errorf("method registry is already built")
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" || trimmed != name {
		return fmt.Errorf("invalid RPC method name %q", name)
	}
	if methodHandlerIsNil(handler) {
		return fmt.Errorf("RPC method %q has a nil handler", name)
	}
	if b.methods == nil {
		b.methods = make(map[string]MethodHandler)
	}
	if _, exists := b.methods[name]; exists {
		return fmt.Errorf("RPC method %q is already registered", name)
	}
	b.methods[name] = handler
	return nil
}

// Build publishes an immutable method registry. The builder cannot be reused
// for further registration after publication.
func (b *MethodRegistryBuilder) Build() (*MethodRegistry, error) {
	if b == nil {
		return nil, fmt.Errorf("method registry builder is nil")
	}
	if b.built {
		return nil, fmt.Errorf("method registry is already built")
	}
	methods := make(map[string]MethodHandler, len(b.methods))
	names := make([]string, 0, len(b.methods))
	for name, handler := range b.methods {
		methods[name] = handler
		names = append(names, name)
	}
	sort.Strings(names)
	registry := &MethodRegistry{methods: methods, names: names}
	b.built = true
	return registry, nil
}

func methodHandlerIsNil(handler MethodHandler) bool {
	if handler == nil {
		return true
	}
	value := reflect.ValueOf(handler)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func (r *MethodRegistry) Get(name string) (MethodHandler, bool) {
	if r == nil {
		return nil, false
	}
	handler, exists := r.methods[name]
	return handler, exists
}

func (r *MethodRegistry) List() []string {
	if r == nil {
		return nil
	}
	methods := make([]string, len(r.names))
	copy(methods, r.names)
	return methods
}

// LedgerIndex is a custom type that can unmarshal from either a JSON number or string
// This matches XRPL API behavior where ledger_index can be: 12345, "12345", "validated", "current", "closed"
type LedgerIndex string

// UnmarshalJSON implements custom unmarshaling for LedgerIndex
func (li *LedgerIndex) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*li = ""
		return nil
	}
	if string(data) == "-0" {
		*li = "0"
		return nil
	}
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

	// Ledger is rippled's legacy combined selector. Exactly 64 characters are
	// treated as a ledger hash; every other string is treated as an index.
	Ledger LedgerIndex `json:"ledger,omitempty"`
}

func (*LedgerSpecifier) UsesLedgerSpecifier() {}

// API Warning IDs as defined in XRPL documentation
const (
	WarningUnsupportedAmendmentsMajority = 1001 // Unsupported amendments have reached majority
	WarningAmendmentBlocked              = 1002 // This server is amendment blocked
	WarningExpiredValidatorList          = 1003 // This server has an expired validator list
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
	Request map[string]any
}

// WebSocketResponse represents an XRPL WebSocket API response.
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
	Request      any             `json:"request,omitempty"`
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
	SubAccountsProposed     SubscriptionType = "accounts_proposed"
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
	Streams          []SubscriptionType                 `json:"streams,omitempty"`
	Accounts         []string                           `json:"accounts,omitempty"`
	AccountsProposed []string                           `json:"accounts_proposed,omitempty"`
	RTAccounts       []string                           `json:"rt_accounts,omitempty"`
	AccountHistory   *AccountHistorySubscriptionRequest `json:"account_history_tx_stream,omitempty"`
	Books            []BookRequest                      `json:"books,omitempty"`
	URL              string                             `json:"url,omitempty"`
	URLUsername      string                             `json:"url_username,omitempty"`
	URLPassword      string                             `json:"url_password,omitempty"`
	// Username / Password are the deprecated aliases rippled still accepts
	// for url_username / url_password. When present they take precedence,
	// and they alone trigger credential updates on an already-registered
	// url subscription (doSubscribe's reuse branch only checks them).
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	// ApiVersion is resolved by the transport and is not part of the decoded
	// subscription payload. A zero value uses DefaultApiVersion.
	ApiVersion int `json:"-"`

	// wire holds the as-received JSON of shape-sensitive fields, captured at
	// decode time. Typed values collapse the shapes rippled
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
	rtAccounts       json.RawMessage
	accountHistory   json.RawMessage
	books            json.RawMessage
	url              json.RawMessage
	username         json.RawMessage
	password         json.RawMessage
}

// WireSubscriptionArrays exposes the raw JSON the wire carried for the
// shape-sensitive subscription fields. Present is false when the request was not
// decoded from JSON, in which case the manager falls back to the typed slices.
type WireSubscriptionArrays struct {
	Present          bool
	Streams          json.RawMessage
	Accounts         json.RawMessage
	AccountsProposed json.RawMessage
	RTAccounts       json.RawMessage
	AccountHistory   json.RawMessage
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
		RTAccounts:       r.wire.rtAccounts,
		AccountHistory:   r.wire.accountHistory,
		Books:            r.wire.books,
	}
}

// WithoutAccountHistory returns a copy that preserves every wire field except
// account_history_tx_stream. The transport handles that field through the
// optional history-stream capability rather than the ordinary stream manager.
func (r SubscriptionRequest) WithoutAccountHistory() SubscriptionRequest {
	r.AccountHistory = nil
	if r.wire != nil {
		wire := *r.wire
		wire.accountHistory = nil
		r.wire = &wire
	}
	return r
}

// WithoutBooks returns a copy that preserves every wire field except books.
func (r SubscriptionRequest) WithoutBooks() SubscriptionRequest {
	r.Books = nil
	if r.wire != nil {
		wire := *r.wire
		wire.books = nil
		r.wire = &wire
	}
	return r
}

// UnmarshalJSON captures the raw JSON of shape-sensitive fields before
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
		rtAccounts:       m["rt_accounts"],
		accountHistory:   m["account_history_tx_stream"],
		books:            m["books"],
		url:              m["url"],
		username:         m["username"],
		password:         m["password"],
	}
	_ = json.Unmarshal(m["streams"], &r.Streams)
	_ = json.Unmarshal(m["accounts"], &r.Accounts)
	_ = json.Unmarshal(m["accounts_proposed"], &r.AccountsProposed)
	_ = json.Unmarshal(m["rt_accounts"], &r.RTAccounts)
	_ = json.Unmarshal(m["account_history_tx_stream"], &r.AccountHistory)
	_ = json.Unmarshal(m["books"], &r.Books)
	_ = json.Unmarshal(m["url"], &r.URL)
	_ = json.Unmarshal(m["url_username"], &r.URLUsername)
	_ = json.Unmarshal(m["url_password"], &r.URLPassword)
	_ = json.Unmarshal(m["username"], &r.Username)
	_ = json.Unmarshal(m["password"], &r.Password)
	return nil
}

// AccountHistorySubscriptionRequest is the nested request carried by
// account_history_tx_stream. StopHistoryTxOnly is meaningful only for
// unsubscribe; its exact wire type and presence are validated from WireArrays.
type AccountHistorySubscriptionRequest struct {
	Account           string `json:"account"`
	StopHistoryTxOnly bool   `json:"stop_history_tx_only,omitempty"`
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

type BookRequest struct {
	TakerPays json.RawMessage `json:"taker_pays"`
	TakerGets json.RawMessage `json:"taker_gets"`
	Snapshot  bool            `json:"snapshot,omitempty"`
	StateNow  bool            `json:"state_now,omitempty"`
	Both      bool            `json:"both,omitempty"`
	BothSides bool            `json:"both_sides,omitempty"`
	Taker     string          `json:"taker,omitempty"`
	Domain    string          `json:"domain,omitempty"`
	wire      *BookRequestWire
}

type BookRequestWire struct {
	Taker     json.RawMessage
	Domain    json.RawMessage
	Both      json.RawMessage
	BothSides json.RawMessage
	Snapshot  json.RawMessage
	StateNow  json.RawMessage
}

func (b *BookRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	b.TakerPays = append(json.RawMessage(nil), raw["taker_pays"]...)
	b.TakerGets = append(json.RawMessage(nil), raw["taker_gets"]...)
	b.Taker = ""
	b.Domain = ""
	b.wire = &BookRequestWire{
		Taker:     append(json.RawMessage(nil), raw["taker"]...),
		Domain:    append(json.RawMessage(nil), raw["domain"]...),
		Both:      append(json.RawMessage(nil), raw["both"]...),
		BothSides: append(json.RawMessage(nil), raw["both_sides"]...),
		Snapshot:  append(json.RawMessage(nil), raw["snapshot"]...),
		StateNow:  append(json.RawMessage(nil), raw["state_now"]...),
	}
	_ = json.Unmarshal(raw["taker"], &b.Taker)
	_ = json.Unmarshal(raw["domain"], &b.Domain)
	b.Both = jsonCppBool(raw["both"])
	b.BothSides = jsonCppBool(raw["both_sides"])
	b.Snapshot = jsonCppBool(raw["snapshot"])
	b.StateNow = jsonCppBool(raw["state_now"])
	return nil
}

func (b BookRequest) Wire() (BookRequestWire, bool) {
	if b.wire == nil {
		return BookRequestWire{}, false
	}
	return *b.wire, true
}

func jsonCppBool(raw json.RawMessage) bool {
	if raw == nil {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0
	case string:
		return v != ""
	case []any:
		return len(v) != 0
	case map[string]any:
		return len(v) != 0
	default:
		return false
	}
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
	Marker json.RawMessage `json:"marker,omitempty"`
}

// Currency specification
type Currency struct {
	Currency string `json:"currency"`
	Issuer   string `json:"issuer,omitempty"`
}

// Path specification for path finding
type Path []PathStep

type PathStep struct {
	Account       string `json:"account,omitempty"`
	Currency      string `json:"currency"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
	Type          uint8  `json:"type,omitempty"`
	TypeHex       string `json:"type_hex,omitempty"`
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
	Currency      string `json:"currency,omitempty"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
}

type OrderBookSpec struct {
	TakerGets CurrencySpec
	TakerPays CurrencySpec
	Domain    string
}

// WebSocketResponseOptions contains optional fields for WebSocket responses
type WebSocketResponseOptions struct {
	Warning   string          // "load" when approaching rate limit
	Warnings  []WarningObject // Array of warning objects
	Forwarded bool            // True if forwarded from Clio to P2P server
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

// LedgerInfoProvider provides current ledger info for subscribe responses
type LedgerInfoProvider interface {
	GetCurrentLedgerInfo() *LedgerSubscribeInfo
}

// LedgerSubscribeInfo contains ledger info returned in the subscribe
// response for the `ledger` stream. Field set mirrors rippled's
// subLedger ack (NetworkOPs::subLedger): fee_ref is emitted only when
// the XRPFees amendment is disabled, and network_id is present when a
// validated ledger is available.
// The per-ledger streamed event uses LedgerCloseEvent and carries
// additional fields (txn_count, etc.).
type LedgerSubscribeInfo struct {
	// LedgerAvailable is false while the server has no validated ledger. In
	// that state subLedger still emits validated_ledgers when its independent
	// operating-mode gate allows it, but must omit all fields derived from a
	// particular validated ledger.
	LedgerAvailable         bool   `json:"-"`
	LedgerIndex             uint32 `json:"ledger_index"`
	LedgerHash              string `json:"ledger_hash"`
	LedgerTime              uint32 `json:"ledger_time"`
	FeeBase                 int32  `json:"fee_base"`
	FeeRef                  uint64 `json:"fee_ref"`
	ReserveBase             int32  `json:"reserve_base"`
	ReserveInc              int32  `json:"reserve_inc"`
	ValidatedLedgers        string `json:"validated_ledgers,omitempty"`
	ValidatedLedgersPresent bool   `json:"-"`
	NetworkID               uint32 `json:"network_id"`
	// XRPFeesEnabled gates fee_ref: rippled emits the deprecated fee_ref
	// only while the XRPFees amendment is disabled.
	XRPFeesEnabled bool `json:"-"`
}
