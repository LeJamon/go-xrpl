package types

import (
	"context"
	"fmt"
	"reflect"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/keylet"
)

// MethodDispatcher allows forwarding RPC calls to the method registry.
// Used by the 'json' RPC method to proxy calls. The caller's RpcContext is
// threaded through so the forwarded method keeps the request's timeout,
// role, client IP and api version — without it a guest could wrap a heavy
// method in `json` to escape per-IP load charging.
type MethodDispatcher interface {
	ExecuteMethod(ctx *RpcContext, method string, params []byte) (any, *RpcError)
}

// Shutdowner requests an orderly node shutdown. The capability is wired before
// RPC transports are constructed and remains stable for every request.
type Shutdowner interface {
	RequestShutdown()
}

// ShutdownFunc adapts a function to Shutdowner for construction and test
// fixtures. Production wiring should use a controller that owns the channel.
type ShutdownFunc func()

func (f ShutdownFunc) RequestShutdown() {
	if f != nil {
		f()
	}
}

// ValidatorListPublisherInfo is the per-publisher snapshot the
// `validators` RPC surfaces. Expressed as a value type (not the
// internal/validator/list.PublisherState struct) so internal/rpc/types
// doesn't import internal/validator/list — same anti-cycle pattern as
// ManifestLookup below.
type ValidatorListPublisherInfo struct {
	// PublicKeyHex is the 33-byte master pubkey, hex-encoded uppercase.
	// Emitted as `pubkey_publisher` in the validators RPC to match
	// rippled's getJson at ValidatorList.cpp:1669 (`strHex(publicKey)`).
	PublicKeyHex string
	// Available is true when the publisher's current list is fresh
	// (matches rippled's `pubCollection.status == available`).
	Available bool
	// Status is one of "unavailable" / "available" / "expired" / "revoked".
	Status string
	// Sequence is the version of the currently-effective list. Zero
	// before the first accepted list.
	Sequence uint32
	// Version is the protocol version of the most recently applied
	// list (rippled `pubCollection.rawVersion`).
	Version uint32
	// EffectiveUnix is the Unix-epoch second at which the current list
	// became effective. Zero when unset.
	EffectiveUnix int64
	// ExpirationUnix is the Unix-epoch second after which the current
	// list is treated as expired. Zero when unset.
	ExpirationUnix int64
	// EffectiveISO is the same time formatted RFC3339-UTC. Empty when
	// EffectiveUnix is zero.
	EffectiveISO string
	// ExpirationISO is the same time formatted RFC3339-UTC. Empty when
	// ExpirationUnix is zero.
	ExpirationISO string
	// SiteURI is the source URL (or "peer:<id>") of the most recent
	// list. Emitted as `uri` to match rippled.
	SiteURI string
	// ValidatorsBase58 is the per-publisher list of validator NodePublic
	// keys (base58, NodePublicKey prefix), sorted lexicographically.
	// Matches rippled's `list` array at ValidatorList.cpp:1684-1688.
	ValidatorsBase58 []string
	// EffectiveSet records whether the accepted blob carried an
	// `effective` field. Rippled gates the JSON `effective` emit on
	// `validFrom != TimeKeeper::time_point{}` at
	// ValidatorList.cpp:1682-1683; without this sentinel a missing
	// blob field would be flattened to a synthetic 2000-Jan-01 stamp
	// by the ripple-epoch offset.
	EffectiveSet bool
	// Remaining holds the per-publisher future-dated rotation queue.
	// Mirrors rippled's `remaining` JSON array emitted under each
	// publisher entry at ValidatorList.cpp:1699-1713.
	Remaining []ValidatorListRemainingInfo
}

// ValidatorListRemainingInfo is one entry in a publisher's
// `remaining` array — a future-dated list that has not yet been
// promoted into the current slot. Mirrors rippled's PublisherList
// shape inside `pubCollection.remaining`.
type ValidatorListRemainingInfo struct {
	Sequence         uint32
	Version          uint32
	SiteURI          string
	EffectiveUnix    int64
	ExpirationUnix   int64
	EffectiveISO     string
	ExpirationISO    string
	EffectiveSet     bool
	ValidatorsBase58 []string
}

// ValidatorListSiteInfo is the per-URL snapshot the
// `validator_list_sites` RPC surfaces. Field names track rippled's
// ValidatorSite::getJson at ValidatorSite.cpp:683-702.
type ValidatorListSiteInfo struct {
	URI             string
	LastRefreshUnix int64
	LastSuccessUnix int64
	NextRefreshUnix int64
	LastRefreshISO  string
	NextRefreshISO  string
	LastError       string
	LastDisposition string
	// LastDispositionSet mirrors rippled's
	// std::optional<Site::Status>::has_value() at
	// ValidatorSite.cpp:690: the handler must omit
	// `last_refresh_status` from the RPC response until the first
	// poll attempt completes. Without this flag the zero-value
	// disposition string would surface as a false "accepted" status.
	LastDispositionSet bool
	RefreshIntervalSec int
	RefreshIntervalMin int
}

// ValidatorListReader is the read-only facet of the publisher-trust
// aggregator that the validators / validator_list_sites RPCs need.
// Expressed as an interface so internal/rpc/types doesn't import
// internal/validator/list.
type ValidatorListReader interface {
	// PublisherCount returns the number of configured publishers in the
	// trust set. Zero means the publisher-trust subsystem is inert and
	// the RPC will report an empty publisher list.
	PublisherCount() int
	// Threshold returns the configured publisher threshold (minimum
	// number of publishers whose lists must agree on a validator
	// before it enters the effective UNL).
	Threshold() int
	// IsUNLBlocked reports whether publisher-list expiry or an empty trusted
	// union has locked the node out of consensus participation.
	IsUNLBlocked() bool
	// Publishers returns a snapshot of per-publisher state for the
	// `validators` RPC.
	Publishers() []ValidatorListPublisherInfo
	// Sites returns a snapshot of per-URL polling state for the
	// `validator_list_sites` RPC.
	Sites() []ValidatorListSiteInfo
	// TrustedMasterKeys returns the master pubkeys currently in the
	// effective trusted UNL contributed by publishers.
	TrustedMasterKeys() [][33]byte
	// ListedValidators returns every validator master key that appears in
	// any publisher's list, each tagged with whether it is currently
	// trusted. Mirrors rippled ValidatorList::for_each_listed
	// (ValidatorList.cpp:1750), which iterates keyListings_ (the union of
	// all listed keys) and reports trusted = membership in trustedMasterKeys_.
	ListedValidators() []ListedValidator
}

// ListedValidator is one entry from the union of all publisher-listed
// validators.
type ListedValidator struct {
	MasterKey [33]byte
	Trusted   bool
}

// ServerInfoPortSnapshot is the immutable portion of a configured listener
// surfaced by server_info and server_state.
type ServerInfoPortSnapshot struct {
	Port     int
	Protocol string
	Admin    bool
}

// ServerInfoConfigSnapshot holds startup configuration used by the server
// status RPCs. The node builds it once before serving requests.
type ServerInfoConfigSnapshot struct {
	Ports        []ServerInfoPortSnapshot
	ServerDomain string
	NodeSize     string
	GitHash      string
}

// RPCCapabilities is the immutable startup policy used by RPC handlers.
type RPCCapabilities struct {
	SigningEnabled bool
	PathSearchMax  int
}

// ManifestLookup is the read-only facet of the validator-manifest cache
// that the `manifest` RPC needs. Expressed as an interface (not a
// concrete type) so internal/rpc/types doesn't import
// internal/manifest, avoiding a cycle once the handler grows.
type ManifestLookup interface {
	// GetMasterKey resolves an ephemeral signing key to its master
	// key via the cached manifest. Returns the input unchanged if no
	// manifest maps it — matches rippled ManifestCache::getMasterKey.
	GetMasterKey(signingKey [33]byte) [33]byte
	// GetSigningKey returns the current ephemeral signing key for a
	// master key, or false if unknown / revoked.
	GetSigningKey(masterKey [33]byte) ([33]byte, bool)
	// GetManifest returns the raw serialized manifest bytes for a
	// master key, or false if unknown / revoked.
	GetManifest(masterKey [33]byte) ([]byte, bool)
	// GetSequence returns the stored manifest's sequence number.
	GetSequence(masterKey [33]byte) (uint32, bool)
	// GetDomain returns the stored manifest's domain.
	GetDomain(masterKey [33]byte) (string, bool)
}

// ServiceContainer holds references to all services needed by RPC handlers
type ServiceContainer struct {
	// LedgerService provides ledger operations
	Ledger LedgerService

	// Shutdown requests an orderly node shutdown. It is supplied to the
	// construction-only builder and copied into the immutable graph.
	Shutdown Shutdowner

	// NodePublicKey is the base58-encoded node identity public key (e.g. "n9...")
	NodePublicKey string
	// SystemTime supplies the wall clock used in time-bearing RPC projections.
	SystemTime func() time.Time

	// ServerInfoConfig is the immutable startup configuration and build
	// metadata surfaced by server_info and server_state.
	ServerInfoConfig ServerInfoConfigSnapshot

	// Capabilities freezes operator-controlled RPC policy before listeners serve.
	Capabilities RPCCapabilities

	// LastCloseInfo returns proposer count and convergence time (ms) from the last consensus round
	LastCloseInfo func() (proposers int, convergeTimeMs int)

	// ConsensusInfo returns the live consensus-round state used by the
	// consensus_info RPC (rippled NetworkOPs::getConsensusInfo →
	// RCLConsensus::getJson). full requests the detailed view. Nil in
	// standalone / RPC-only mode (no consensus engine) — the handler then
	// returns an empty info object, matching rippled's standalone behavior.
	ConsensusInfo func(full bool) map[string]any

	// Manifests is the validator-manifest lookup used by the
	// `manifest` RPC method. Nil until the consensus components are
	// built (e.g. in standalone mode without p2p); handlers must
	// nil-check before use.
	Manifests ManifestLookup

	// ValidatorPublicKey is the local validator's signing public key
	// (33-byte compressed). Empty when the server is not configured
	// as a validator. Mirrors rippled's Application::getValidationPublicKey
	// — validator_info uses emptiness to gate the notValidator response.
	ValidatorPublicKey []byte

	// ValidationQuorum returns the live consensus quorum (number of
	// trusted-validator signatures required to fully validate a ledger).
	// Computed by the adaptor from the current UNL minus the negative-UNL.
	// Nil in standalone mode (server_info falls back to 1).
	ValidationQuorum func() int

	// ValidatorList is the publisher-trust subsystem's read facet for
	// the `validators` and `validator_list_sites` RPC methods. Nil when
	// no validator_list_keys are configured — handlers must nil-check.
	ValidatorList ValidatorListReader

	// LocalStaticTrustedKeysBase58 returns the operator's static
	// `[validators]` config entries, base58-encoded with the NodePublic
	// prefix. Surfaced by the `validators` RPC as `local_static_keys`
	// (rippled getJson at ValidatorList.cpp:1657-1661). Nil-safe — a nil
	// func means "no static keys".
	LocalStaticTrustedKeysBase58 func() []string

	// TrustedValidatorKeysBase58 returns the current effective trusted UNL,
	// including configured static validators, the local identity, and validators
	// selected from publisher lists. Surfaced as `trusted_validator_keys`.
	TrustedValidatorKeysBase58 func() []string

	// SigningKeysBase58 returns the master→signing key map projected as
	// base58 strings. Surfaced by the `validators` RPC as `signing_keys`
	// (rippled getJson at ValidatorList.cpp:1725-1734). Nil-safe.
	SigningKeysBase58 func() map[string]string

	// NegativeUNLBase58 returns the current negative-UNL set, base58-
	// encoded. Surfaced by the `validators` RPC as `NegativeUNL`
	// (rippled getJson at ValidatorList.cpp:1737-1744). Nil-safe.
	NegativeUNLBase58 func() []string

	// BetaRPCAPI reports whether the operator enabled the beta RPC API
	// (beta_rpc_api config knob, rippled Config::BETA_RPC_API). When set,
	// requests may use api_version up to BetaApiVersion; otherwise the
	// accepted range is capped at MaxSupportedApiVersion. The `version`
	// method reports `last` accordingly.
	BetaRPCAPI bool

	// TxQMetrics returns the current transaction-queue metrics used by
	// server_info for the load_factor_fee_* triple. Nil until the
	// ledger service is wired (standalone tests, pre-startup) —
	// server_info falls back to baseline values.
	TxQMetrics func() TxQServerMetrics

	// TxQFeeMetrics returns the full TxQ snapshot consumed by the
	// `fee` RPC handler. Nil until the ledger service is wired
	// (standalone tests, pre-startup) — handler then falls back to
	// rippled's idle-state defaults.
	TxQFeeMetrics func() TxQFeeMetrics

	// JqTransOverflow returns the cumulative inbound transactions shed
	// under saturation, summed across the two sequential drop stages: the
	// overlay ingress gate (the in-flight tx ceiling) and the consensus
	// worker pool. A frame is shed by at most one stage, so the sum does not
	// double-count. This is goxrpl's analog of rippled's single
	// jq_trans_overflow counter and drives server_info.jq_trans_overflow.
	// Nil in standalone / RPC-only configurations (no overlay) — handler
	// reads zero.
	JqTransOverflow func() uint64

	// PeerDisconnects returns cumulative peer-disconnect counters
	// surfaced by server_info: (total, resources-driven). Nil when
	// the overlay isn't wired (standalone, RPC-only tests).
	PeerDisconnects func() (total, resources uint64)

	// PeerReservationAdd inserts or replaces a peer reservation keyed by
	// base58 NodePublic, returning the previous description, whether one
	// existed, and any persistence error. Backs peer_reservations_add (rippled
	// Reservations.cpp, whose insert_or_assign may throw on a failed DB write).
	PeerReservationAdd func(nodePublic, description string) (previous string, replaced bool, err error)

	// PeerReservationDel removes a peer reservation by base58 NodePublic,
	// returning the previous description, whether one existed, and any
	// persistence error. Backs peer_reservations_del.
	PeerReservationDel func(nodePublic string) (previous string, existed bool, err error)

	// PeerReservationList returns all peer reservations. Backs
	// peer_reservations_list. All three are nil when the overlay isn't wired
	// (standalone / RPC-only) — handlers then report empty results.
	PeerReservationList func() []PeerReservationEntry

	// PeerConnect admits an outbound peer connection to a host:port for the
	// runtime-owned bounded scheduler backing the admin `connect` RPC. The
	// function is non-blocking and returns admission errors synchronously;
	// duplicate queued/running addresses are idempotent. Nil in standalone /
	// RPC-only configurations (no live overlay).
	PeerConnect func(addr string) error

	// ResourceBlacklist returns the overlay resource manager's per-endpoint
	// reputation table filtered by an optional threshold (nil applies the
	// WarningThreshold default), backing the admin `black_list` RPC
	// (rippled BlackList.cpp → ResourceManager::getJson). Keyed by endpoint
	// address with {local, remote, type} values.
	ResourceBlacklist func(threshold *int) map[string]any

	// StateAccounting returns the operating-mode state-machine
	// snapshot surfaced by server_info: per-mode counts/durations
	// plus the current-state and initial-sync durations. The Modes
	// map is empty until consensus is wired.
	StateAccounting func() StateAccountingSnapshot

	FastSyncMetrics func() FastSyncMetrics

	// CloseTimeOffset returns the consensus-derived close-time offset
	// from the adaptor. Surfaced as close_time_offset on the ledger
	// object in human mode when |offset| >= 60s
	// (NetworkOPs.cpp:2946-2949). Nil before consensus is wired.
	CloseTimeOffset func() time.Duration

	// FetchPackCacheSize returns the number of inbound fetch-pack nodes
	// currently cached by the consensus router. Nil in standalone mode.
	FetchPackCacheSize func() uint32

	// LoadFactorFees returns the LoadFeeTrack local/net/cluster fees
	// driving the admin-only human-mode load_factor_local/net/cluster
	// emits (NetworkOPs.cpp:2887-2901). Nil until a LoadFeeTrack
	// subsystem lands — handler suppresses the fields when nil.
	LoadFactorFees func() LoadFactorFees

	// Nil in RPC-only test contexts, which handlers treat as unloaded.
	IsLoadedCluster func() bool

	// IsLoadedLocal reports whether local fee pressure is elevated. Nil in
	// RPC-only test contexts, which handlers treat as unloaded.
	IsLoadedLocal func() bool

	// ClientLoad is the shared in-flight client-RPC counter that drives
	// the rpcTOO_BUSY load-shedding gates. Approximates rippled's
	// jtCLIENT backpressure via in-flight RPC count: rippled measures
	// JobQueue.getJobCountGE(jtCLIENT) (queued *waiting* jobs); go-xrpl
	// has no unified job queue and instead measures concurrent RPCs
	// bracketed by Begin()/End() at the HTTP/WS dispatchers. The two
	// signals are correlated but not identical — see handlers.RequireNotBusyClient
	// and friends for the per-tier thresholds (500 generic, 200 book_offers,
	// 50 path-find) mirroring rippled's Tuning.h constants.
	//
	// Nil in standalone / RPC-only test contexts — every gate treats
	// nil as "never shed".
	ClientLoad *ClientLoadShedder

	// RPCDiagnostics records completed and currently-running RPC handlers for
	// server_info/server_state. It is shared by every transport so the snapshot
	// represents the node rather than one listener.
	RPCDiagnostics      RPCDiagnostics
	SubscriptionMetrics func() SubscriptionMetrics

	// GetCounts returns the runtime counters surfaced by the get_counts RPC
	// (node-store I/O counters and locally-held transactions). Nil until the
	// ledger service is wired — the handler then reports only the standalone
	// flag.
	//
	// CountsResult / NodeStoreCounts intentionally mirror the structs of the
	// same shape in internal/ledger/service: this RPC-types package must not
	// import the ledger service, so the wiring in cmd/server translates between
	// the two. The duplication is the layering boundary, not an oversight.
	GetCounts func() CountsResult

	// TxReduceRelayMetrics returns the transaction reduce-relay rolling
	// averages surfaced by the tx_reduce_relay RPC. Nil when the overlay
	// isn't wired (standalone / RPC-only) — the handler then reports zeros.
	TxReduceRelayMetrics func() TxReduceRelayMetrics

	// FetchInfo returns the inbound-ledger acquisition snapshot served by
	// fetch_info (rippled InboundLedgers::getInfo): a map keyed by ledger
	// sequence (or hash) whose values report have_header/have_state/
	// have_transactions/peers and the needed_state_hashes/needed_transaction_hashes
	// for in-flight acquisitions, and the same fields with failed:true for recent
	// failures. Nil in standalone / RPC-only mode (no acquisition subsystem) —
	// the handler then returns an empty object, matching rippled's empty result
	// on a node that isn't acquiring.
	FetchInfo func() map[string]any

	// FetchInfoClear resets the inbound-ledger acquisition counters and
	// failure history, backing fetch_info's `clear` param (rippled
	// NetworkOPs::clearLedgerFetch → InboundLedgers::clearFailures). Nil-safe.
	FetchInfoClear func()

	// RequestLedger triggers (or joins) a generic acquisition of a ledger from
	// peers, backing the ledger_request RPC (rippled
	// InboundLedgers::acquire(..., Reason::GENERIC)). The target is identified
	// by hash, or by seq when hash is zero (resolved against the validated
	// ledger). It returns the per-acquisition progress snapshot (rippled
	// InboundLedger::getJson shape) and started=true while an acquisition is in
	// flight, or (nil,false,false) when the target can't be resolved or no peer
	// is available. reference is true when the snapshot describes a 256-aligned
	// reference ledger being fetched only to resolve a deep target's hash —
	// rippled wraps that case as lgrNotFound + acquiring, versus the bare
	// snapshot it returns when acquiring the target itself. Nil in standalone /
	// RPC-only mode (no acquisition subsystem) — the handler then reports the
	// ledger as not found without acquiring.
	RequestLedger func(ledgerHash [32]byte, ledgerSeq uint32) (acquiring map[string]any, started, reference bool)

	// LedgerCleanerConfigure configures and starts the background
	// ledger-integrity verifier, returning its resulting status. Backs the
	// admin ledger_cleaner RPC. Nil when no cleaner is wired — the handler
	// then reports the service unavailable.
	//
	// LedgerCleanerParams / LedgerCleanerStatus mirror the structs of the same
	// shape in internal/ledger/cleaner: this RPC-types package must not import
	// the cleaner, so the wiring in cmd/server translates between the two.
	LedgerCleanerConfigure func(LedgerCleanerParams) LedgerCleanerStatus

	// UNLBlocked reports whether the node's UNL is blocked (the configured
	// validator list has expired), driving the rpcEXPIRED_VALIDATOR_LIST
	// branch of conditionMet (mirrors rippled NetworkOPs::isUNLBlocked).
	// Nil in standalone / RPC-only configurations (no validator list) — the
	// gate then treats the node as not blocked.
	UNLBlocked func() bool

	// AdvisoryDeleteState backs the can_delete RPC (rippled SHAMapStore
	// advisory-delete state). Nil when no online-delete state subsystem is
	// wired — the handler then returns notEnabled, matching rippled's
	// advisoryDelete() gate.
	AdvisoryDeleteState AdvisoryDeleteStore

	// AccountHistorySubscriptions is the optional provider for rippled's
	// experimental account_history_tx_stream. Nil means the node cannot perform
	// the historical replay/live handoff and subscribe requests return notEnabled.
	AccountHistorySubscriptions AccountHistorySubscriptionService

	// QueueAccountTxs returns the transactions currently queued in the TxQ
	// for one account, sorted by SeqProxy. Backs account_info's queue_data
	// (rippled TxQ::getAccountTxs → AccountInfo.cpp:193-283). Nil in
	// standalone / RPC-only configurations (no TxQ) — the handler then
	// reports an empty queue.
	QueueAccountTxs func(account [20]byte) []QueuedTxInfo

	// QueueAllTxs returns every transaction currently in the TxQ, ordered by
	// fee level. Backs the ledger method's queue_data dump (rippled
	// TxQ::getTxs → LedgerToJson.cpp fillJsonQueue). Nil-safe like
	// QueueAccountTxs.
	QueueAllTxs func() []QueuedTxInfo
}

// URLSubscriptionService is the url-keyed subscription registry mirroring
// rippled's RPCSub/mRPCSubMap: each url maps to one long-lived subscriber
// whose events are delivered as outbound JSON-RPC "event" calls with per-url
// sequence numbers and basic auth. Callers gate on role before invoking —
// both methods are admin-only in rippled's handlers.
type URLSubscriptionService interface {
	// Subscribe registers (or extends) the url subscription and returns the
	// same ack payload a WebSocket subscriber gets (current ledger info for
	// the ledger stream, book snapshots).
	Subscribe(ctx *RpcContext, request SubscriptionRequest) (map[string]any, *RpcError)
	// Unsubscribe removes the listed streams/accounts/books from the url
	// subscription and drops the registry entry once no stream
	// subscriptions remain. An unknown url is silent success.
	Unsubscribe(ctx *RpcContext, request SubscriptionRequest) (map[string]any, *RpcError)
}

// AccountHistorySubscriptionService owns the historical replay and its live
// continuation for account_history_tx_stream. Subscribe is called only after
// request, transaction-table, and provider validation; implementations derive
// asynchronous work from conn.Context(). Unsubscribe is an idempotent no-op for
// unknown accounts.
type AccountHistorySubscriptionService interface {
	ValidateSubscribe(conn AccountHistorySubscriptionSink, account string) *RpcError
	Subscribe(conn AccountHistorySubscriptionSink, account string)
	Unsubscribe(conn AccountHistorySubscriptionSink, account string, historyOnly bool)
	RemoveConnection(conn AccountHistorySubscriptionSink)
	HasSubscriptions(conn AccountHistorySubscriptionSink) bool
}

type AccountHistorySubscriptionSink interface {
	ID() string
	Context() context.Context
	Done() <-chan struct{}
	TrySend([]byte) bool
}

// QueuedTxInfo is the per-transaction view of a TxQ candidate surfaced by
// the queue_data sections of account_info and the ledger method. It mirrors
// the fields rippled reads off TxQ::TxDetails (AccountInfo.cpp:218-261,
// LedgerToJson.cpp:292-316). Fee and MaxSpendDrops are drop amounts;
// FeeLevel is the queue fee level on the same scale server_info reports.
type QueuedTxInfo struct {
	Account          [20]byte
	TxID             [32]byte
	SeqValue         uint32
	IsTicket         bool
	FeeLevel         uint64
	LastValid        uint32
	Fee              uint64
	MaxSpendDrops    uint64
	AuthChange       bool
	RetriesRemaining int
	PreflightResult  string
	LastResult       string
	HasLastResult    bool
	// TxJSON is the flattened transaction, included verbatim in the ledger
	// queue dump's per-entry tx / tx_json field. Nil for account_info, which
	// does not echo the transaction body.
	TxJSON map[string]any
}

// AdvisoryDeleteStore is the advisory-delete state facet backing the
// can_delete RPC, mirroring the subset of rippled's SHAMapStore that
// CanDelete.cpp uses: advisoryDelete() / getCanDelete() / setCanDelete() /
// getLastRotated(). Satisfied by *internal/ledger/shamapstore.Store.
type AdvisoryDeleteStore interface {
	AdvisoryDelete() bool
	GetCanDelete() uint32
	SetCanDelete(seq uint32) (uint32, error)
	GetLastRotated() uint32
}

// LedgerCleanerParams mirrors internal/ledger/cleaner.Params (layering
// boundary — see LedgerCleanerConfigure). Pointer fields are nil when the
// caller omits the corresponding JSON parameter.
type LedgerCleanerParams struct {
	Ledger     *uint32
	MinLedger  *uint32
	MaxLedger  *uint32
	Full       *bool
	CheckNodes *bool
	FixTxns    *bool
	Stop       bool
}

// LedgerCleanerStatus mirrors internal/ledger/cleaner.Status (layering
// boundary). State is "idle", "running", or "stopped".
type LedgerCleanerStatus struct {
	State          string
	MinLedger      uint32
	MaxLedger      uint32
	CheckNodes     bool
	FixTxns        bool
	Failures       int
	LedgersChecked uint64
	NodesChecked   uint64
	MissingNodes   uint64
	LastError      string
}

// CountsResult is the subset of rippled's get_counts that go-xrpl has real data
// for. NodeStore is nil when no persistent node store is configured.
type CountsResult struct {
	Standalone bool
	LocalTxs   int
	NodeStore  *NodeStoreCounts
	FullBelow  *FullBelowCounts
}

// NodeStoreCounts holds node-store I/O counters for get_counts. Fields map 1:1
// onto the node_* keys rippled emits from NodeStore::Database::getCountsJson.
type NodeStoreCounts struct {
	Reads      uint64 // node_reads_total
	FetchHits  uint64 // node_reads_hit
	Writes     uint64 // node_writes
	ReadBytes  uint64 // node_read_bytes
	WriteBytes uint64 // node_written_bytes
}

// RPCDiagnostics records handler execution after admission and exposes an
// immutable point-in-time snapshot. The finish callback receives true only
// when the handler panicked; ordinary RPC error results are completed calls.
type RPCDiagnostics interface {
	Start(method string) (finish func(panicked bool))
	Snapshot() RPCDiagnosticsSnapshot
}

type RPCDiagnosticsSnapshot struct {
	Methods map[string]RPCMethodDiagnostics
	Current []RPCActivity
}

type SubscriptionMetrics struct {
	Connections               uint64
	Items                     uint64
	RequestLimitRejections    uint64
	ConnectionLimitRejections uint64
	GlobalLimitRejections     uint64
	DeliveriesQueued          uint64
	DeliveriesDropped         uint64
	DeliveryDisconnects       uint64
}

type RPCMethodDiagnostics struct {
	Started    uint64
	Finished   uint64
	Errored    uint64
	DurationUs uint64
}

type RPCActivity struct {
	Method     string
	DurationUs uint64
}

// FullBelowCounts holds the shared SHAMap completeness-cache metrics.
type FullBelowCounts struct {
	Size       int
	TargetSize int
	Hits       uint64
	Misses     uint64
	Inserts    uint64
	Evictions  uint64
	Sweeps     uint64
}

// TxReduceRelayMetrics holds the transaction reduce-relay rolling averages for
// the tx_reduce_relay RPC. Mirrors rippled metrics::TxMetrics — each value is
// a 30-sample rolling average (per-second for message counts/sizes, per-sample
// for the peer-selection averages) emitted as a decimal string, matching
// OverlayImpl::txMetrics() (TxMetrics.cpp:117-148).
type TxReduceRelayMetrics struct {
	TxCnt           uint64
	TxSz            uint64
	HaveTxCnt       uint64
	HaveTxSz        uint64
	GetLedgerCnt    uint64
	GetLedgerSz     uint64
	LedgerDataCnt   uint64
	LedgerDataSz    uint64
	TransactionsCnt uint64
	TransactionsSz  uint64
	SelectedCnt     uint64
	SuppressedCnt   uint64
	NotEnabledCnt   uint64
	MissingTxFreq   uint64
}

// JSON renders the metrics in rippled's tx_reduce_relay wire shape: the txr_*
// keys with decimal-string values (rippled uses std::to_string), matching
// TxMetrics::json() (TxMetrics.cpp:117-148).
func (m TxReduceRelayMetrics) JSON() map[string]any {
	s := func(v uint64) string { return strconv.FormatUint(v, 10) }
	return map[string]any{
		"txr_tx_cnt":           s(m.TxCnt),
		"txr_tx_sz":            s(m.TxSz),
		"txr_have_txs_cnt":     s(m.HaveTxCnt),
		"txr_have_txs_sz":      s(m.HaveTxSz),
		"txr_get_ledger_cnt":   s(m.GetLedgerCnt),
		"txr_get_ledger_sz":    s(m.GetLedgerSz),
		"txr_ledger_data_cnt":  s(m.LedgerDataCnt),
		"txr_ledger_data_sz":   s(m.LedgerDataSz),
		"txr_transactions_cnt": s(m.TransactionsCnt),
		"txr_transactions_sz":  s(m.TransactionsSz),
		"txr_selected_cnt":     s(m.SelectedCnt),
		"txr_suppressed_cnt":   s(m.SuppressedCnt),
		"txr_not_enabled_cnt":  s(m.NotEnabledCnt),
		"txr_missing_tx_freq":  s(m.MissingTxFreq),
	}
}

// PeerReservationEntry is one peer reservation surfaced by
// peer_reservations_list: a base58 NodePublic key and its operator
// description.
type PeerReservationEntry struct {
	NodePublic  string
	Description string
}

// Rippled rpc::Tuning thresholds (Tuning.h:62-64).
const (
	// MaxJobQueueClients is the generic-RPC shedding ceiling used by
	// rippled's fillHandler (RPCHandler.cpp:135). Strict-greater.
	MaxJobQueueClients int64 = 500
	// MaxBookOffersClients is the book_offers-specific ceiling
	// (BookOffers.cpp:42). Strict-greater.
	MaxBookOffersClients int64 = 200
	// MaxPathfindClients is the per-attempt path-finding ceiling
	// (LegacyPathFind.cpp:39 + Tuning.h:63 maxPathfindJobCount).
	// Strict-greater.
	MaxPathfindClients int64 = 50
	// MaxPathfindsInProgress is the hard cap on concurrently-running
	// path-finds (LegacyPathFind.cpp:47 + Tuning.h:62). Strict-less.
	MaxPathfindsInProgress int64 = 2
)

// ClientLoadShedder is the shared in-flight RPC counter plus a
// dedicated concurrent-path-find counter. See ServiceContainer.ClientLoad.
//
// Begin returns an ownership-bound release function for each RPC dispatch
// (HTTP and WebSocket). Handlers
// consult InFlight() against the rippled-faithful tier constants
// (MaxJobQueueClients, MaxBookOffersClients, MaxPathfindClients).
//
// AcquirePathfind and AcquirePathfindUnlimited return ownership-bound
// release functions for the second counter, which is capped at
// MaxPathfindsInProgress for bounded callers. A release function is safe to
// invoke more than once.
type ClientLoadShedder struct {
	inFlight       atomic.Int64
	pathfindActive atomic.Int64
	pathfindOnce   sync.Once
	pathfindReady  chan struct{}
}

func NewClientLoadShedder() *ClientLoadShedder {
	return &ClientLoadShedder{}
}

func (s *ClientLoadShedder) pathfindSignal() chan struct{} {
	s.pathfindOnce.Do(func() {
		s.pathfindReady = make(chan struct{}, int(MaxPathfindsInProgress))
	})
	return s.pathfindReady
}

func noOpLoadShedRelease() {}

func (s *ClientLoadShedder) release(counter *atomic.Int64, signal bool) func() {
	if s == nil {
		return noOpLoadShedRelease
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			counter.Add(-1)
			if signal {
				select {
				case s.pathfindSignal() <- struct{}{}:
				default:
				}
			}
		})
	}
}

func (s *ClientLoadShedder) Begin() func() {
	if s == nil {
		return noOpLoadShedRelease
	}
	s.inFlight.Add(1)
	return s.release(&s.inFlight, false)
}

// InFlight returns the current in-flight RPC count. Gates compare it
// against the rippled-faithful Max* tier constants.
func (s *ClientLoadShedder) InFlight() int64 {
	if s == nil {
		return 0
	}
	return s.inFlight.Load()
}

// AcquirePathfind attempts to enter the path-finding critical section.
// It returns an ownership-bound release function and true on success, or a
// nil release function and false when already at MaxPathfindsInProgress.
// CAS-loop matches rippled's LegacyPathFind ctor at LegacyPathFind.cpp:44-58.
func (s *ClientLoadShedder) AcquirePathfind() (func(), bool) {
	if s == nil {
		return noOpLoadShedRelease, true
	}
	for {
		prev := s.pathfindActive.Load()
		if prev >= MaxPathfindsInProgress {
			return nil, false
		}
		if s.pathfindActive.CompareAndSwap(prev, prev+1) {
			return s.release(&s.pathfindActive, true), true
		}
	}
}

// WaitPathfind blocks until a bounded path-finding slot is available or the
// request is canceled. It returns the acquired slot's release function and
// true on success, or a nil release function and false on cancellation.
func (s *ClientLoadShedder) WaitPathfind(ctx context.Context) (func(), bool) {
	if s == nil {
		return noOpLoadShedRelease, true
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		release, acquired := s.AcquirePathfind()
		if acquired {
			return release, true
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-s.pathfindSignal():
		}
	}
}

// AcquirePathfindUnlimited enters the path-finding critical section without
// enforcing the non-admin concurrency cap and returns its release function.
func (s *ClientLoadShedder) AcquirePathfindUnlimited() func() {
	if s == nil {
		return noOpLoadShedRelease
	}
	s.pathfindActive.Add(1)
	return s.release(&s.pathfindActive, true)
}

// PathfindActive returns the current concurrent-path-find count.
func (s *ClientLoadShedder) PathfindActive() int64 {
	if s == nil {
		return 0
	}
	return s.pathfindActive.Load()
}

// LedgerSelectionReader provides read-only ledger index and mode queries.
type LedgerSelectionReader interface {
	GetCurrentLedgerIndex() uint32
	GetClosedLedgerIndex() uint32
	GetValidatedLedgerIndex() uint32
	IsStandalone() bool
}

type LedgerAcceptor interface {
	AcceptLedger(ctx context.Context) (uint32, error)
}

// LedgerNavigator is the construction compatibility aggregate.
type LedgerNavigator interface {
	LedgerSelectionReader
	LedgerAcceptor
}

// LedgerDataReader provides ledger retrieval and server metadata.
type LedgerDataReader interface {
	GetLedgerBySequence(seq uint32) (LedgerReader, error)
	GetLedgerByHash(hash [32]byte) (LedgerReader, error)
	GetServerInfo() LedgerServerInfo
	GetGenesisAccount() (string, error)
	GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64)
	GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*LedgerRangeResult, error)
	GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error)
	GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error)
	IsAmendmentBlocked() bool
}

type ClosedLedgerViewSource interface {
	GetClosedLedgerView() (LedgerStateView, error)
}

// LedgerAccessor is the construction compatibility aggregate.
type LedgerAccessor interface {
	LedgerDataReader
	ClosedLedgerViewSource
}

// TxTablesProvider reports whether the node maintains the transaction
// tables backing tx-history RPCs. Mirrors rippled config().useTxTables():
// tx, account_tx and tx_history must return rpcNOT_ENABLED before any
// parameter validation when the tables are unavailable (AccountTx.cpp,
// TxHistory.cpp, Tx.cpp all gate as their first statement). A ledger
// service that does not implement it is assumed to have history
// available.
type TxTablesProvider interface {
	UseTxTables() bool
}

// TxSearchResult reports how completely a requested ledger range was searched
// when a transaction hash is absent.
type TxSearchResult int

const (
	TxSearchUnknown TxSearchResult = iota
	TxSearchSome
	TxSearchAll
)

// RangedTransactionLookup is the optional transaction-table lookup used by the
// tx RPC when both ledger range bounds are present. Keeping it separate from
// TransactionSubmitter lets lightweight ledger mocks omit relational search.
type RangedTransactionLookup interface {
	GetTransactionWithRange(ctx context.Context, txHash [32]byte, minLedger, maxLedger uint32) (*TransactionInfo, TxSearchResult, error)
}

// TransactionSubmitter handles transaction submission and retrieval.
// FailHardSubmitter is the optional rippled-faithful surface for
// submitting a transaction with tapFAIL_HARD semantics (TxQ.cpp:393-399,
// NetworkOPs.cpp:1685-1689): on non-apply the blob is NOT held in the
// localTxs pool, NOT pushed onto the canonical pendingTxs slice, and
// NOT relayed. Production LedgerServiceAdapter implements it; test
// mocks may omit it — submit handlers fall back to SubmitTransaction
// when the interface is not satisfied.
type FailHardSubmitter interface {
	SubmitTransactionFailHard(txJSON []byte, txBlobHex string) (*SubmitResult, error)
}

type TransactionQuerier interface {
	GetTransaction(txHash [32]byte) (*TransactionInfo, error)
	GetTransactionHistory(ctx context.Context, startIndex uint32) (*TxHistoryResult, error)
}

type TransactionSubmission interface {
	// txBlobHex is the signed transaction blob, or an empty string when the
	// caller has only transaction JSON.
	SubmitTransaction(txJSON []byte, txBlobHex string) (*SubmitResult, error)
	SimulateTransaction(txJSON []byte) (*SubmitResult, error)

	// GetAutofillFee returns the Fee a transaction should carry to enter
	// the open ledger. Mirrors rippled getCurrentNetworkFee
	// (TransactionSign.cpp:839-877): max(scaleFeeLoad(feeDefault),
	// escalatedFee) with a feeDefault * mult / div ceiling, where mult /
	// div are the caller's fee_mult_max / fee_div_max (default 10 / 1).
	// On ceiling overflow handlers map to rpcINTERNAL; on exceedance the
	// returned error is a *svcerr.HighFeeError (errors.Is(svcerr.ErrHighFee)
	// also matches). Includes per-tx-type adjustments (multisign,
	// AccountDelete, AMMCreate, LedgerStateFix). Never reads the source
	// account.
	//
	// unlimited mirrors rippled's isUnlimited(role) carve-out: admin /
	// identified callers skip local-only load below 4x remote. The
	// ceiling check still applies (rippled enforces it post-scale).
	GetAutofillFee(txJSON []byte, unlimited bool, mult, div int) (fee uint64, err error)

	// GetAutofillSequence returns the Sequence a transaction should
	// carry. Mirrors rippled getAutofillSequence (Simulate.cpp:37-69):
	// returns 0 when hasTicketSequence is true; otherwise reads the
	// account SLE and consults TxQ.NextQueuableSeq. Returns
	// svcerr.ErrAccountNotFound when the account is absent and no ticket
	// supersedes the requirement.
	GetAutofillSequence(account string, hasTicketSequence bool) (sequence uint32, err error)
}

// TransactionSubmitter is the construction compatibility aggregate.
type TransactionSubmitter interface {
	TransactionQuerier
	TransactionSubmission
}

// LedgerContext contains the durable ledger fields needed to decorate
// historical transaction rows.
type LedgerContext struct {
	Hash      [32]byte
	CloseTime int64
}

// LedgerContextReader resolves ledger header fields without requiring the full
// ledger to remain in the in-memory history window.
type LedgerContextReader interface {
	GetLedgerContext(ctx context.Context, sequence uint32) (*LedgerContext, error)
}

type ContextLedgerHashReader interface {
	GetLedgerByHashContext(ctx context.Context, hash [32]byte) (LedgerReader, error)
}

// TransactionRulesSource provides the amendment rules used to admit a
// transaction to the current open ledger. Handlers that validate before
// submission or simulation use this optional facet to match the engine.
type TransactionRulesSource interface {
	TransactionRules() *amendment.Rules
}

// AccountQuerier provides account-related read operations.
type AccountQuerier interface {
	GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*AccountInfo, error)
	GetAccountLines(ctx context.Context, account string, ledgerIndex string, peer string, limit uint32, marker string) (*AccountLinesResult, error)
	GetAccountOffers(ctx context.Context, account string, ledgerIndex string, limit uint32, marker string) (*AccountOffersResult, error)
	GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *AccountTxMarker, forward bool) (*AccountTxResult, error)
	GetAccountChannels(ctx context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32, marker string) (*AccountChannelsResult, error)
	GetAccountCurrencies(ctx context.Context, account string, ledgerIndex string) (*AccountCurrenciesResult, error)
	GetAccountObjects(ctx context.Context, account string, ledgerIndex string, objType string, limit uint32, marker string) (*AccountObjectsResult, error)
	GetAccountNFTs(ctx context.Context, account string, ledgerIndex string, limit uint32, marker string) (*AccountNFTsResult, error)
}

// AccountTxDelegateRole identifies the queried account's role in a delegated
// transaction.
type AccountTxDelegateRole uint8

const (
	AccountTxDelegateActor AccountTxDelegateRole = iota
	AccountTxDelegateAuthorizer
)

// AccountTxDelegateFilter selects delegated account transactions and an
// optional counterparty.
type AccountTxDelegateFilter struct {
	Role         AccountTxDelegateRole
	Counterparty string
}

// AccountTxDelegateQuerier extends account_tx with delegation filtering.
type AccountTxDelegateQuerier interface {
	GetAccountTransactionsWithDelegate(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *AccountTxMarker, forward bool, delegate *AccountTxDelegateFilter) (*AccountTxResult, error)
}

type BookReader interface {
	GetBookOffers(ctx context.Context, takerGets, takerPays Amount, taker, domain string, ledgerIndex string, limit uint32, marker string, withProofs bool) (*BookOffersResult, error)
}

type GatewayReader interface {
	GetGatewayBalances(ctx context.Context, account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error)
	GetNoRippleCheck(ctx context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error)
	GetDepositAuthorized(ctx context.Context, sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*DepositAuthorizedResult, error)
}

type NFTReader interface {
	GetNFTBuyOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
	GetNFTSellOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
}

// LedgerReadService is the read-only ledger capability published to ordinary
// RPC consumers.
type LedgerReadService interface {
	LedgerSelectionReader
	LedgerDataReader
	TransactionQuerier
	AccountQuerier
	BookReader
	GatewayReader
	NFTReader
}

// LedgerMutationService is reserved for RPCs that close ledgers or submit and
// simulate transactions.
type LedgerMutationService interface {
	LedgerAcceptor
	TransactionSubmission
}

// LedgerService is the construction-time aggregate implemented by the ledger
// adapter. ServiceGraph publishes its read and mutation facets separately.
type LedgerService interface {
	LedgerReadService
	LedgerMutationService
	ClosedLedgerViewSource
}

// LedgerViewSource is an optional LedgerService facet for handlers that need
// direct state-view access to a specific ledger (e.g. ripple_path_find with
// an explicit ledger_index/ledger_hash). The returned LedgerReader carries
// the metadata (sequence, hash, validated) merged into the RPC response.
// Production and rpcenv adapters implement it; mocks may omit it, in which
// case handlers report lgrNotFound for explicit ledger selectors.
type LedgerViewSource interface {
	GetLedgerViewBySeq(seq uint32) (LedgerStateView, LedgerReader, error)
	GetLedgerViewByHash(hash [32]byte) (LedgerStateView, LedgerReader, error)
}

// OpenLedgerViewSource provides an immutable snapshot of the current
// open-ledger state for operations such as transaction path construction.
// It is optional so lightweight RPC mocks can continue to omit the view.
type OpenLedgerViewSource interface {
	GetOpenLedgerView() (LedgerStateView, error)
}

// LedgerStateReader provides low-level read access to ledger state.
type LedgerStateReader interface {
	Read(k keylet.Keylet) ([]byte, error)
	Exists(k keylet.Keylet) (bool, error)
	ForEach(fn func(key [32]byte, data []byte) bool) error
	Succ(key [32]byte) ([32]byte, []byte, bool, error)
	TxExists(txID [32]byte) (bool, error)
	Rules() *amendment.Rules
	LedgerSeq() uint32
}

type LedgerStateWriter interface {
	Insert(k keylet.Keylet, data []byte) error
	Update(k keylet.Keylet, data []byte) error
	Erase(k keylet.Keylet) error
	AdjustDropsDestroyed(d drops.XRPAmount) error
}

// LedgerStateView is the full engine-facing state interface.
type LedgerStateView interface {
	LedgerStateReader
	LedgerStateWriter
}

// DepositAuthorizedResult contains the result of deposit_authorized RPC
type DepositAuthorizedResult struct {
	SourceAccount      string   `json:"source_account"`
	DestinationAccount string   `json:"destination_account"`
	DepositAuthorized  bool     `json:"deposit_authorized"`
	LedgerIndex        uint32   `json:"ledger_index"`
	LedgerHash         [32]byte `json:"ledger_hash"`
	Validated          bool     `json:"validated"`
}

// AccountInfo contains account information from the ledger
type AccountInfo struct {
	Account           string
	Balance           string
	Flags             uint32
	OwnerCount        uint32
	Sequence          uint32
	RegularKey        string
	Domain            string
	EmailHash         string
	TransferRate      uint32
	TickSize          uint8
	PreviousTxnID     string
	PreviousTxnLgrSeq uint32
	LedgerIndex       uint32
	LedgerHash        string
	Validated         bool
	RawData           []byte // Raw SLE binary for full deserialization via binarycodec
	Index             string // SLE key/hash (hex string)
}

// LedgerReader provides read access to a ledger
type LedgerReader interface {
	Sequence() uint32
	Hash() [32]byte
	ParentHash() [32]byte
	IsClosed() bool
	IsValidated() bool
	TotalDrops() uint64
	CloseTime() int64 // Ripple epoch seconds
	CloseTimeResolution() uint32
	CloseFlags() uint8
	ParentCloseTime() int64 // Ripple epoch seconds
	TxMapHash() [32]byte    // Transaction tree root hash
	StateMapHash() [32]byte // Account state tree root hash
	ForEachTransaction(fn func(txHash [32]byte, txData []byte) bool) error
}

// LedgerTransactionSource is implemented by ledger readers that can query the
// transaction tree of that exact ledger.
type LedgerTransactionSource interface {
	GetLedgerTransaction(txHash [32]byte) ([]byte, bool, error)
}

type ContextLedgerTransactionSource interface {
	GetLedgerTransactionContext(ctx context.Context, txHash [32]byte) ([]byte, bool, error)
}

type ContextLedgerStateSource interface {
	ForEachLedgerStateContext(ctx context.Context, fn func(key [32]byte, data []byte) bool) error
}

type LedgerAmendmentRulesSource interface {
	LedgerAmendmentRules() *amendment.Rules
}

// LedgerAmendmentRulesErrorSource is the error-aware amendment-rules facet.
// It is optional to preserve the existing LedgerReader mock contract while
// allowing production adapters to surface rules-loading failures.
type LedgerAmendmentRulesErrorSource interface {
	LedgerAmendmentRulesWithError() (*amendment.Rules, error)
}

// LedgerServerInfo contains server status information from the ledger service
type LedgerServerInfo struct {
	Standalone            bool
	ServerState           string
	NeedsNetworkLedger    bool
	OpenLedgerSeq         uint32
	ClosedLedgerSeq       uint32
	ClosedLedgerHash      [32]byte
	ClosedLedgerCloseTime int64 // Ripple-epoch seconds; 0 when unknown.
	// HaveValidated is true when the service has a validated ledger.
	// Mirrors rippled LedgerMaster::haveValidated() — drives the
	// validated_ledger vs closed_ledger emit gate at NetworkOPs.cpp:2918.
	HaveValidated            bool
	ValidatedLedgerSeq       uint32
	ValidatedLedgerHash      [32]byte
	ValidatedLedgerCloseTime int64 // Ripple-epoch seconds; 0 when unknown.
	CompleteLedgers          string
	HavePublished            bool
	PublishedLedgerSeq       uint32
	NetworkID                uint32
}

// LoadFactorFees carries rippled's per-source LoadFeeTrack fees used
// for the admin-only human-mode load_factor_local/net/cluster emits
// at NetworkOPs.cpp:2887-2901. Each field is a fee level on the same
// scale as loadBase; values equal to loadBase suppress emission.
type LoadFactorFees struct {
	Local   uint32
	Net     uint32
	Cluster uint32
}

// TxQServerMetrics is the subset of TxQ metrics surfaced by server_info.
// The TxQ admission-control saturation counter (txq.Metrics.TxQFull)
// is intentionally not exposed here: rippled has no analogous public
// field, and conflating it with jq_trans_overflow misled operators
// pre-#494. The counter remains internal for logs / diagnostics.
type TxQServerMetrics struct {
	ReferenceFeeLevel     uint64
	MinProcessingFeeLevel uint64
	OpenLedgerFeeLevel    uint64
}

// TxQFeeMetrics is the full TxQ snapshot surfaced by the `fee` RPC,
// mirroring the fields read by rippled's TxQ::doRPC
// (TxQ.cpp:1860-1909). It is a superset of TxQServerMetrics — the
// `fee` handler needs txCount / txPerLedger / txInLedger / median /
// queue-max in addition to the load_factor levels.
type TxQFeeMetrics struct {
	TxCount               uint64
	TxQMaxSize            *uint64 // nil → no limit, omits max_queue_size
	TxInLedger            uint64
	TxPerLedger           uint64
	ReferenceFeeLevel     uint64
	MinProcessingFeeLevel uint64
	MedFeeLevel           uint64
	OpenLedgerFeeLevel    uint64
}

// StateAccountingEntry is one row of server_info.state_accounting:
// the cumulative time spent in an operating mode and the number of
// times the node entered it.
type StateAccountingEntry struct {
	Transitions uint64
	DurationUs  uint64
}

// StateAccountingSnapshot bundles everything server_info needs from
// the state-accounting tracker. Mirrors the data emitted by rippled's
// NetworkOPsImp::StateAccounting::json (NetworkOPs.cpp:4828-4849):
// per-mode rows plus the two top-level companion fields.
type StateAccountingSnapshot struct {
	// Modes is the per-mode counts/durations table.
	Modes map[string]StateAccountingEntry
	// CurrentDurationUs is the time spent in the current operating
	// mode since the last transition. Surfaced as the top-level
	// server_state_duration_us field.
	CurrentDurationUs uint64
	// InitialSyncUs is the duration from process start to the first
	// transition into Full. Zero before that transition. Surfaced as
	// initial_sync_duration_us; rippled emits it only when non-zero.
	InitialSyncUs uint64
}

// FastSyncMetrics is the server_info representation of fast-sync outcomes.
type FastSyncMetrics struct {
	CompletionRecheckAccepted            uint64
	CompletionRecheckRejectedNoEvidence  uint64
	CompletionRecheckRejectedBelowQuorum uint64
	CompletionRecheckRejectedUnavailable uint64
	TargetSuperseded                     uint64
	ObsoleteAcquisitionCompleted         uint64
	ReplayPipelineRequested              uint64
	ReplayPipelineReady                  uint64
	ReplayPipelineApplied                uint64
	ReplayPipelineDiscarded              uint64
	ReplayPipelineRetried                uint64
	ReplayPipelineFallbacks              uint64
	ReplayPipelineCapacityRetargets      uint64
	ReplayPipelineBackpressureEvents     uint64
	ReplayPipelineRetargetFailures       uint64
	ReplayPipelineAcquireUs              uint64
	ReplayPipelineReadyWaitUs            uint64
	ReplayPipelineApplyUs                uint64
	ReplayPipelinePersistUs              uint64
	ReplayPipelineWindow                 uint32
	ReplayPipelineDepth                  uint32
	ReplayPipelineReadyDepth             uint32
	ReplayPipelineHeadSeq                uint32
	ReplayPipelineTargetSeq              uint32
	ReplayPipelinePreparedLimit          uint32
	ReplayPipelinePivotSeq               uint32
	ReplayPipelinePreparedTailSeq        uint32
	ReplayPipelineTrustedHeadSeq         uint32
	ReplayPipelineGeneration             uint64
	ReplayPipelinePivotStateNodesPerSec  uint64
	ReplayPipelineHeadBlockedUs          uint64
}

// SubmitResult contains the result of submitting a transaction.
// The boolean fields match rippled's Transaction::SubmitResult struct:
// applied, broadcast, queued, kept are independent pipeline states.
// "accepted" in rippled is derived as: applied || broadcast || queued || kept.
type SubmitResult struct {
	// EngineResult is the result code string (e.g., "tesSUCCESS")
	EngineResult string

	// EngineResultCode is the numeric result code
	EngineResultCode int

	// EngineResultMessage is a human-readable result message
	EngineResultMessage string

	// Applied indicates if the transaction was applied to the open ledger
	Applied bool

	// Broadcast indicates if the transaction was broadcast to peers
	Broadcast bool

	// Queued indicates if the transaction was placed in the transaction queue
	Queued bool

	// Kept indicates if the transaction was kept for retry
	Kept bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// CurrentLedger is the current open ledger sequence
	CurrentLedger uint32
	// CurrentLedgerCloseTime is the open-ledger close time in Ripple-epoch seconds.
	CurrentLedgerCloseTime int64

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32

	// CurrentLedgerState is the immutable state snapshot captured at submit
	// time. It is nil when no validated ledger exists or the authoritative
	// account/fee state could not be derived.
	CurrentLedgerState *SubmitLedgerState

	// Metadata is nil when the transaction produced no metadata.
	Metadata *SubmitMetadata
}

// SubmitLedgerState contains the four current-ledger values returned by
// submit. The pointer on SubmitResult distinguishes an unavailable snapshot
// from valid zero-valued fields.
type SubmitLedgerState struct {
	ValidatedLedgerIndex     uint32
	OpenLedgerCost           uint64
	AccountSequenceNext      uint32
	AccountSequenceAvailable uint32
}

// SubmitMetadata carries simulation metadata in JSON and binary form
// so the simulate handler can render either `meta` or `meta_blob`.
type SubmitMetadata struct {
	JSON any
	Blob []byte
}

// Accepted returns true if any submission state is true, matching
// rippled's SubmitResult::any() method.
func (r *SubmitResult) Accepted() bool {
	return r.Applied || r.Broadcast || r.Queued || r.Kept
}

// TransactionInfo contains transaction data and metadata
type TransactionInfo struct {
	// TxData is the raw transaction data with metadata
	TxData []byte

	// LedgerIndex is the ledger sequence containing this transaction
	LedgerIndex uint32

	// LedgerHash is the hash of the containing ledger
	LedgerHash string

	// Validated indicates if the transaction is in a validated ledger
	Validated bool

	// TxIndex is the transaction's index within the ledger
	TxIndex uint32

	// CloseTime is the containing ledger's close time in Ripple epoch seconds.
	CloseTime int64
}

// Amount identifies an XRP, issued-currency, or MPT amount.
type Amount struct {
	Value         string `json:"value,omitempty"`
	Currency      string `json:"currency,omitempty"`
	Issuer        string `json:"issuer,omitempty"`
	MPTIssuanceID string `json:"mpt_issuance_id,omitempty"`
}

func (a Amount) IsNative() bool {
	return a.MPTIssuanceID == "" && a.Currency == "" && a.Issuer == ""
}

func (a Amount) IsMPT() bool { return a.MPTIssuanceID != "" }

// TrustLine represents a trust line from account_lines RPC
type TrustLine struct {
	Account        string `json:"account"`
	Balance        string `json:"balance"`
	Currency       string `json:"currency"`
	Limit          string `json:"limit"`
	LimitPeer      string `json:"limit_peer"`
	QualityIn      uint32 `json:"quality_in,omitempty"`
	QualityOut     uint32 `json:"quality_out,omitempty"`
	NoRipple       bool   `json:"no_ripple,omitempty"`
	NoRipplePeer   bool   `json:"no_ripple_peer,omitempty"`
	Authorized     bool   `json:"authorized,omitempty"`
	PeerAuthorized bool   `json:"peer_authorized,omitempty"`
	Freeze         bool   `json:"freeze,omitempty"`
	FreezePeer     bool   `json:"freeze_peer,omitempty"`
	DeepFreeze     bool   `json:"deep_freeze,omitempty"`
	DeepFreezePeer bool   `json:"deep_freeze_peer,omitempty"`
	HasReserve     bool   `json:"-"`
}

// AccountLinesResult contains the result of account_lines RPC
type AccountLinesResult struct {
	Account     string      `json:"account"`
	Lines       []TrustLine `json:"lines"`
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Validated   bool        `json:"validated"`
	Marker      string      `json:"marker,omitempty"`
}

// AccountOffer represents an offer from account_offers RPC
type AccountOffer struct {
	Flags      uint32 `json:"flags"`
	Seq        uint32 `json:"seq"`
	TakerGets  any    `json:"taker_gets"`
	TakerPays  any    `json:"taker_pays"`
	Quality    string `json:"quality"`
	Expiration uint32 `json:"expiration,omitempty"`
}

// AccountOffersResult contains the result of account_offers RPC
type AccountOffersResult struct {
	Account     string         `json:"account"`
	Offers      []AccountOffer `json:"offers"`
	LedgerIndex uint32         `json:"ledger_index"`
	LedgerHash  [32]byte       `json:"ledger_hash"`
	Validated   bool           `json:"validated"`
	Marker      string         `json:"marker,omitempty"`
}

// BookOffer represents an offer in an order book. The wire shape mirrors
// rippled's sleOffer->getJson(JsonOptions::none) output plus the per-offer
// fields (quality, owner_funds, taker_gets_funded, taker_pays_funded) that
// NetworkOPsImp::getBookPage layers on top.
type BookOffer struct {
	Account           string           `json:"Account"`
	BookDirectory     string           `json:"BookDirectory"`
	BookNode          string           `json:"BookNode"`
	Expiration        uint32           `json:"Expiration,omitempty"`
	Flags             uint32           `json:"Flags"`
	LedgerEntryType   string           `json:"LedgerEntryType"`
	OwnerNode         string           `json:"OwnerNode"`
	PreviousTxnID     string           `json:"PreviousTxnID"`
	PreviousTxnLgrSeq uint32           `json:"PreviousTxnLgrSeq"`
	Sequence          uint32           `json:"Sequence"`
	TakerGets         any              `json:"TakerGets"`
	TakerPays         any              `json:"TakerPays"`
	DomainID          string           `json:"DomainID,omitempty"`
	AdditionalBooks   []map[string]any `json:"AdditionalBooks,omitempty"`
	Index             string           `json:"index"`
	Quality           string           `json:"quality"`
	OwnerFunds        string           `json:"owner_funds,omitempty"`
	TakerGetsFunded   any              `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded   any              `json:"taker_pays_funded,omitempty"`
	// Proof carries the SHAMap state-tree proof (leaf-to-root, upper-case
	// hex) for the offer's ledger entry when the request set proof=true.
	// Verify against ledger.account_hash with shamap.VerifyProofPath.
	Proof []string `json:"proof,omitempty"`
}

// BookOffersResult contains the result of book_offers RPC
type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
	// Marker is the resume token for the next page (64-hex offer index).
	// Empty when the book has been fully walked. go-xrpl extension —
	// rippled's BookOffers handler accepts a marker parameter but never
	// emits one.
	//
	// To paginate safely, callers should pin the ledger by passing
	// ledger_index or ledger_hash on every follow-up call. The default
	// "current" ledger advances between calls, so the offer indexed by
	// the marker can be consumed by a concurrent transaction; the next
	// call then returns rpcINVALID_PARAMS ("object pointed to by marker
	// is gone").
	Marker string `json:"marker,omitempty"`
}

// AccountTxMarker is used for pagination in account_tx
type AccountTxMarker struct {
	LedgerSeq uint32 `json:"ledger"`
	TxnSeq    uint32 `json:"seq"`
}

// AccountTransaction contains transaction data for account_tx
type AccountTransaction struct {
	Hash        [32]byte `json:"hash"`
	LedgerIndex uint32   `json:"ledger_index"`
	TxnSeq      uint32   `json:"txn_seq"`
	TxBlob      []byte   `json:"tx_blob,omitempty"`
	Meta        []byte   `json:"meta,omitempty"`
}

// AccountTxResult contains the result of account_tx query
type AccountTxResult struct {
	Account      string               `json:"account"`
	LedgerMin    uint32               `json:"ledger_index_min"`
	LedgerMax    uint32               `json:"ledger_index_max"`
	Limit        uint32               `json:"limit"`
	Marker       *AccountTxMarker     `json:"marker,omitempty"`
	Transactions []AccountTransaction `json:"transactions"`
	Validated    bool                 `json:"validated"`
}

// TxHistoryResult contains the result of tx_history query
type TxHistoryResult struct {
	Index        uint32               `json:"index"`
	Transactions []AccountTransaction `json:"txs"`
}

// LedgerRangeResult contains ledger hashes for a range
type LedgerRangeResult struct {
	LedgerFirst uint32              `json:"ledger_first"`
	LedgerLast  uint32              `json:"ledger_last"`
	Hashes      map[uint32][32]byte `json:"hashes"`
}

// LedgerEntryResult contains a single ledger entry
type LedgerEntryResult struct {
	Index       string   `json:"index"`
	LedgerIndex uint32   `json:"ledger_index"`
	LedgerHash  [32]byte `json:"ledger_hash"`
	Node        []byte   `json:"node"`
	NodeBinary  string   `json:"node_binary,omitempty"`
	Validated   bool     `json:"validated"`
}

// LedgerDataItem represents a single state entry
type LedgerDataItem struct {
	Index string `json:"index"`
	Data  []byte `json:"data"`
}

// LedgerHeaderInfo contains complete ledger header data for responses
type LedgerHeaderInfo struct {
	AccountHash         [32]byte `json:"account_hash"`
	CloseFlags          uint8    `json:"close_flags"`
	CloseTime           int64    `json:"close_time"`
	CloseTimeHuman      string   `json:"close_time_human"`
	CloseTimeISO        string   `json:"close_time_iso"`
	CloseTimeResolution uint32   `json:"close_time_resolution"`
	Closed              bool     `json:"closed"`
	LedgerHash          [32]byte `json:"ledger_hash"`
	LedgerIndex         uint32   `json:"ledger_index"`
	ParentCloseTime     int64    `json:"parent_close_time"`
	ParentHash          [32]byte `json:"parent_hash"`
	TotalCoins          uint64   `json:"total_coins"`
	TransactionHash     [32]byte `json:"transaction_hash"`
}

// LedgerDataResult contains ledger state data
type LedgerDataResult struct {
	LedgerIndex  uint32            `json:"ledger_index"`
	LedgerHash   [32]byte          `json:"ledger_hash"`
	State        []LedgerDataItem  `json:"state"`
	Marker       string            `json:"marker,omitempty"`
	Validated    bool              `json:"validated"`
	LedgerHeader *LedgerHeaderInfo `json:"ledger,omitempty"`
}

// AccountObjectItem represents an account object
type AccountObjectItem struct {
	Index           string `json:"index"`
	LedgerEntryType string `json:"LedgerEntryType"`
	Data            []byte `json:"data"`
}

// OwnerInfoResult groups an account's owner-directory contents the way
// rippled's NetworkOPsImp::getOwnerInfo does (NetworkOPs.cpp:1753): offers
// and trust lines only.
type OwnerInfoResult struct {
	Offers      []AccountObjectItem
	RippleLines []AccountObjectItem
	LedgerIndex uint32
	LedgerHash  [32]byte
	Validated   bool
}

// OwnerDirectoryReader is the capability owner_info needs beyond the base
// LedgerService surface: a faithful owner-directory walk (every page, no
// object-count cap), mirroring rippled's NetworkOPsImp::getOwnerInfo. The
// production LedgerServiceAdapter implements it; handlers reach it by
// type-asserting ctx.Services.Ledger, like types.FailHardSubmitter.
type OwnerDirectoryReader interface {
	GetOwnerInfo(ctx context.Context, account string, ledgerIndex string) (*OwnerInfoResult, error)
}

// AccountObjectsResult contains account objects
type AccountObjectsResult struct {
	Account        string              `json:"account"`
	AccountObjects []AccountObjectItem `json:"account_objects"`
	LedgerIndex    uint32              `json:"ledger_index"`
	LedgerHash     [32]byte            `json:"ledger_hash"`
	Validated      bool                `json:"validated"`
	Marker         string              `json:"marker,omitempty"`
}

// AccountChannel represents a payment channel for account_channels RPC
type AccountChannel struct {
	ChannelID          string `json:"channel_id"`
	Account            string `json:"account"`
	DestinationAccount string `json:"destination_account"`
	Amount             string `json:"amount"`
	Balance            string `json:"balance"`
	SettleDelay        uint32 `json:"settle_delay"`
	PublicKey          string `json:"public_key,omitempty"`
	PublicKeyHex       string `json:"public_key_hex,omitempty"`
	Expiration         uint32 `json:"expiration,omitempty"`
	CancelAfter        uint32 `json:"cancel_after,omitempty"`
	SourceTag          uint32 `json:"source_tag,omitempty"`
	DestinationTag     uint32 `json:"destination_tag,omitempty"`
	HasSourceTag       bool   `json:"-"` // Internal flag, not serialized
	HasDestTag         bool   `json:"-"` // Internal flag, not serialized
}

// AccountChannelsResult contains the result of account_channels RPC
type AccountChannelsResult struct {
	Account     string           `json:"account"`
	Channels    []AccountChannel `json:"channels"`
	LedgerIndex uint32           `json:"ledger_index"`
	LedgerHash  [32]byte         `json:"ledger_hash"`
	Validated   bool             `json:"validated"`
	Marker      string           `json:"marker,omitempty"`
	Limit       uint32           `json:"limit,omitempty"`
}

// AccountCurrenciesResult contains the result of account_currencies RPC
type AccountCurrenciesResult struct {
	ReceiveCurrencies []string `json:"receive_currencies"`
	SendCurrencies    []string `json:"send_currencies"`
	LedgerIndex       uint32   `json:"ledger_index"`
	LedgerHash        [32]byte `json:"ledger_hash"`
	Validated         bool     `json:"validated"`
}

// NFTInfo represents an individual NFT for account_nfts RPC
type NFTInfo struct {
	Flags        uint16 `json:"Flags"`
	Issuer       string `json:"Issuer"`
	NFTokenID    string `json:"NFTokenID"`
	NFTokenTaxon uint32 `json:"NFTokenTaxon"`
	URI          string `json:"URI,omitempty"`
	NFTSerial    uint32 `json:"nft_serial"`
	TransferFee  uint16 `json:"transfer_fee,omitempty"`
}

// AccountNFTsResult contains the result of account_nfts RPC
type AccountNFTsResult struct {
	Account     string    `json:"account"`
	AccountNFTs []NFTInfo `json:"account_nfts"`
	LedgerIndex uint32    `json:"ledger_index"`
	LedgerHash  [32]byte  `json:"ledger_hash"`
	Validated   bool      `json:"validated"`
	Marker      string    `json:"marker,omitempty"`
}

// CurrencyBalance represents a currency balance for gateway_balances
type CurrencyBalance struct {
	Currency string `json:"currency"`
	Value    string `json:"value"`
}

// GatewayBalancesResult contains the result of gateway_balances RPC
type GatewayBalancesResult struct {
	Account        string                       `json:"account"`
	Obligations    map[string]string            `json:"obligations,omitempty"`     // currency -> value
	Balances       map[string][]CurrencyBalance `json:"balances,omitempty"`        // account -> []balance
	FrozenBalances map[string][]CurrencyBalance `json:"frozen_balances,omitempty"` // account -> []balance
	Assets         map[string][]CurrencyBalance `json:"assets,omitempty"`          // account -> []balance
	Locked         map[string]string            `json:"locked,omitempty"`          // currency -> value (escrows)
	LedgerIndex    uint32                       `json:"ledger_index"`
	LedgerHash     [32]byte                     `json:"ledger_hash"`
	Validated      bool                         `json:"validated"`
}

// SuggestedTransaction represents a suggested transaction to fix NoRipple issues
type SuggestedTransaction struct {
	TransactionType string         `json:"TransactionType"`
	Account         string         `json:"Account"`
	Fee             string         `json:"Fee"`
	Sequence        uint32         `json:"Sequence"`
	SetFlag         uint32         `json:"SetFlag,omitempty"`
	Flags           uint32         `json:"Flags,omitempty"`
	LimitAmount     map[string]any `json:"LimitAmount,omitempty"`
}

// NoRippleCheckResult contains the result of noripple_check RPC
type NoRippleCheckResult struct {
	Problems     []string               `json:"problems"`
	Transactions []SuggestedTransaction `json:"transactions,omitempty"`
	LedgerIndex  uint32                 `json:"ledger_index"`
	LedgerHash   [32]byte               `json:"ledger_hash"`
	Validated    bool                   `json:"validated"`
}

// NFTOfferInfo represents an individual NFToken offer for nft_buy_offers/nft_sell_offers RPC
type NFTOfferInfo struct {
	NFTOfferIndex string `json:"nft_offer_index"`
	Flags         uint32 `json:"flags"`
	Owner         string `json:"owner"`
	Amount        any    `json:"amount"`                // Can be string (XRP drops) or object (IOU)
	Destination   string `json:"destination,omitempty"` // Optional
	Expiration    uint32 `json:"expiration,omitempty"`  // Optional
}

// NFTOffersResult contains the result of nft_buy_offers/nft_sell_offers RPC
type NFTOffersResult struct {
	NFTID       string         `json:"nft_id"`
	Offers      []NFTOfferInfo `json:"offers"`
	LedgerIndex uint32         `json:"ledger_index"`
	LedgerHash  [32]byte       `json:"ledger_hash"`
	Validated   bool           `json:"validated"`
	Limit       uint32         `json:"limit,omitempty"`  // Only present when paginating
	Marker      string         `json:"marker,omitempty"` // Only present when more results available
}

type serviceGraphProfile struct {
	RequireLedger             bool
	RequireShutdown           bool
	RequireClientLoad         bool
	RequireDiagnostics        bool
	RequireConfig             bool
	RequireSubscriptionMetric bool
}

// ServiceGraphBuilder is mutable construction state. It is never passed to a
// request context or transport; Build returns the only published graph.
type ServiceGraphBuilder struct {
	ServiceContainer
}

// NewServiceContainer constructs the legacy mutable wiring state. New node
// code should use NewServiceGraphBuilder; this function remains for small
// fixture setup before NewTestServiceGraph is called.
func NewServiceContainer(ledger LedgerService) *ServiceContainer {
	return &ServiceContainer{Ledger: ledger}
}

// NewServiceGraphBuilder starts construction with the mandatory ledger
// dependency. Additional capabilities are attached while startup stages run.
func NewServiceGraphBuilder(ledger LedgerService) *ServiceGraphBuilder {
	return &ServiceGraphBuilder{ServiceContainer: ServiceContainer{Ledger: ledger}}
}

// Build validates the strict production dependency profile and publishes an
// immutable graph.
func (b *ServiceGraphBuilder) Build() (*ServiceGraph, error) {
	return b.buildProfile(serviceGraphProfile{
		RequireLedger:             true,
		RequireShutdown:           true,
		RequireClientLoad:         true,
		RequireDiagnostics:        true,
		RequireConfig:             true,
		RequireSubscriptionMetric: true,
	})
}

func (b *ServiceGraphBuilder) buildProfile(profile serviceGraphProfile) (*ServiceGraph, error) {
	if b == nil {
		return nil, fmt.Errorf("service graph builder is nil")
	}
	if profile.RequireLedger && serviceCapabilityNil(b.Ledger) {
		return nil, fmt.Errorf("service graph requires a ledger service")
	}
	if profile.RequireShutdown && serviceCapabilityNil(b.Shutdown) {
		return nil, fmt.Errorf("service graph requires a shutdown capability")
	}
	if profile.RequireClientLoad && b.ClientLoad == nil {
		return nil, fmt.Errorf("service graph requires a client-load shedder")
	}
	if profile.RequireDiagnostics && serviceCapabilityNil(b.RPCDiagnostics) {
		return nil, fmt.Errorf("service graph requires RPC diagnostics")
	}
	if profile.RequireConfig && b.ServerInfoConfig.Ports == nil {
		return nil, fmt.Errorf("service graph requires a server configuration snapshot")
	}
	if profile.RequireSubscriptionMetric && b.SubscriptionMetrics == nil {
		return nil, fmt.Errorf("service graph requires subscription metrics")
	}

	snapshot := cloneServiceContainer(b.ServiceContainer)
	// These dependencies belong to the transport/request layer, not the
	// application graph. They are deliberately absent from ServiceContainer.
	return &ServiceGraph{snapshot: snapshot}, nil
}

func serviceCapabilityNil(capability any) bool {
	if capability == nil {
		return true
	}
	value := reflect.ValueOf(capability)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// NewTestServiceGraph publishes a sparse graph for handler and transport
// fixtures. Production validation is exercised through ServiceGraphBuilder.Build.
func NewTestServiceGraph(services *ServiceContainer) *ServiceGraph {
	if services == nil {
		services = &ServiceContainer{}
	}
	builder := &ServiceGraphBuilder{ServiceContainer: *services}
	graph, err := builder.buildProfile(serviceGraphProfile{})
	if err != nil {
		panic(err)
	}
	return graph
}

// NewTestServiceGraphFrom copies a published graph and applies fixture-only
// overrides before publishing a new snapshot.
func NewTestServiceGraphFrom(graph *ServiceGraph, configure func(*ServiceContainer)) *ServiceGraph {
	services := ServiceContainer{}
	if graph != nil && graph.services() != nil {
		services = cloneServiceContainer(*graph.services())
	}
	if configure != nil {
		configure(&services)
	}
	return NewTestServiceGraph(&services)
}

// ServiceGraph is the immutable application capability graph published to
// RPC contexts and transports. Its state is private; live capabilities may
// expose their own synchronized operations, but the graph topology cannot be
// changed after Build returns.
type ServiceGraph struct {
	snapshot ServiceContainer
}

func (g *ServiceGraph) services() *ServiceContainer {
	if g == nil {
		return nil
	}
	return &g.snapshot
}

func (g *ServiceGraph) Ledger() LedgerReadService {
	if s := g.services(); s != nil {
		return s.Ledger
	}
	return nil
}

func (g *ServiceGraph) LedgerMutation() LedgerMutationService {
	if s := g.services(); s != nil {
		return s.Ledger
	}
	return nil
}

func (g *ServiceGraph) LedgerViews() ClosedLedgerViewSource {
	if s := g.services(); s != nil {
		return s.Ledger
	}
	return nil
}

func (g *ServiceGraph) ClosedLedgerState() (LedgerStateReader, error) {
	views := g.LedgerViews()
	if views == nil {
		return nil, fmt.Errorf("closed ledger view is unavailable")
	}
	return views.GetClosedLedgerView()
}

func (g *ServiceGraph) Shutdowner() Shutdowner {
	if s := g.services(); s != nil {
		return s.Shutdown
	}
	return nil
}

func (g *ServiceGraph) NodePublicKey() string {
	if s := g.services(); s != nil {
		return s.NodePublicKey
	}
	return ""
}

func (g *ServiceGraph) SystemTime() func() time.Time {
	if s := g.services(); s != nil {
		return s.SystemTime
	}
	return nil
}

func (g *ServiceGraph) ServerInfoConfig() ServerInfoConfigSnapshot {
	if s := g.services(); s != nil {
		return cloneServerInfoConfig(s.ServerInfoConfig)
	}
	return ServerInfoConfigSnapshot{}
}

func (g *ServiceGraph) Capabilities() RPCCapabilities {
	if s := g.services(); s != nil {
		return s.Capabilities
	}
	return RPCCapabilities{}
}

func (g *ServiceGraph) LastCloseInfo() func() (int, int) {
	if s := g.services(); s != nil {
		return s.LastCloseInfo
	}
	return nil
}

func (g *ServiceGraph) ConsensusInfo() func(bool) map[string]any {
	if s := g.services(); s != nil {
		return s.ConsensusInfo
	}
	return nil
}

func (g *ServiceGraph) Manifests() ManifestLookup {
	if s := g.services(); s != nil {
		return s.Manifests
	}
	return nil
}

func (g *ServiceGraph) ValidatorPublicKey() []byte {
	if s := g.services(); s != nil {
		return append([]byte(nil), s.ValidatorPublicKey...)
	}
	return nil
}

func (g *ServiceGraph) ValidationQuorum() func() int {
	if s := g.services(); s != nil {
		return s.ValidationQuorum
	}
	return nil
}

func (g *ServiceGraph) ValidatorList() ValidatorListReader {
	if s := g.services(); s != nil {
		return s.ValidatorList
	}
	return nil
}

func (g *ServiceGraph) LocalStaticTrustedKeysBase58() func() []string {
	if s := g.services(); s != nil {
		return s.LocalStaticTrustedKeysBase58
	}
	return nil
}

func (g *ServiceGraph) TrustedValidatorKeysBase58() func() []string {
	if s := g.services(); s != nil {
		return s.TrustedValidatorKeysBase58
	}
	return nil
}

func (g *ServiceGraph) SigningKeysBase58() func() map[string]string {
	if s := g.services(); s != nil {
		return s.SigningKeysBase58
	}
	return nil
}

func (g *ServiceGraph) NegativeUNLBase58() func() []string {
	if s := g.services(); s != nil {
		return s.NegativeUNLBase58
	}
	return nil
}

func (g *ServiceGraph) BetaRPCAPI() bool {
	if s := g.services(); s != nil {
		return s.BetaRPCAPI
	}
	return false
}

func (g *ServiceGraph) TxQMetrics() func() TxQServerMetrics {
	if s := g.services(); s != nil {
		return s.TxQMetrics
	}
	return nil
}

func (g *ServiceGraph) TxQFeeMetrics() func() TxQFeeMetrics {
	if s := g.services(); s != nil {
		return s.TxQFeeMetrics
	}
	return nil
}

func (g *ServiceGraph) JqTransOverflow() func() uint64 {
	if s := g.services(); s != nil {
		return s.JqTransOverflow
	}
	return nil
}

func (g *ServiceGraph) PeerDisconnects() func() (uint64, uint64) {
	if s := g.services(); s != nil {
		return s.PeerDisconnects
	}
	return nil
}

func (g *ServiceGraph) PeerReservationAdd() func(string, string) (string, bool, error) {
	if s := g.services(); s != nil {
		return s.PeerReservationAdd
	}
	return nil
}

func (g *ServiceGraph) PeerReservationDel() func(string) (string, bool, error) {
	if s := g.services(); s != nil {
		return s.PeerReservationDel
	}
	return nil
}

func (g *ServiceGraph) PeerReservationList() func() []PeerReservationEntry {
	if s := g.services(); s != nil {
		return s.PeerReservationList
	}
	return nil
}

func (g *ServiceGraph) PeerConnect() func(string) error {
	if s := g.services(); s != nil {
		return s.PeerConnect
	}
	return nil
}

func (g *ServiceGraph) ResourceBlacklist() func(*int) map[string]any {
	if s := g.services(); s != nil {
		return s.ResourceBlacklist
	}
	return nil
}

func (g *ServiceGraph) StateAccounting() func() StateAccountingSnapshot {
	if s := g.services(); s != nil {
		return s.StateAccounting
	}
	return nil
}

func (g *ServiceGraph) FastSyncMetrics() func() FastSyncMetrics {
	if s := g.services(); s != nil {
		return s.FastSyncMetrics
	}
	return nil
}

func (g *ServiceGraph) CloseTimeOffset() func() time.Duration {
	if s := g.services(); s != nil {
		return s.CloseTimeOffset
	}
	return nil
}

func (g *ServiceGraph) FetchPackCacheSize() func() uint32 {
	if s := g.services(); s != nil {
		return s.FetchPackCacheSize
	}
	return nil
}

func (g *ServiceGraph) LoadFactorFees() func() LoadFactorFees {
	if s := g.services(); s != nil {
		return s.LoadFactorFees
	}
	return nil
}

func (g *ServiceGraph) IsLoadedCluster() func() bool {
	if s := g.services(); s != nil {
		return s.IsLoadedCluster
	}
	return nil
}

func (g *ServiceGraph) IsLoadedLocal() func() bool {
	if s := g.services(); s != nil {
		return s.IsLoadedLocal
	}
	return nil
}

func (g *ServiceGraph) ClientLoad() *ClientLoadShedder {
	if s := g.services(); s != nil {
		return s.ClientLoad
	}
	return nil
}

func (g *ServiceGraph) RPCDiagnostics() RPCDiagnostics {
	if s := g.services(); s != nil {
		return s.RPCDiagnostics
	}
	return nil
}

func (g *ServiceGraph) SubscriptionMetrics() func() SubscriptionMetrics {
	if s := g.services(); s != nil {
		return s.SubscriptionMetrics
	}
	return nil
}

func (g *ServiceGraph) GetCounts() func() CountsResult {
	if s := g.services(); s != nil {
		return s.GetCounts
	}
	return nil
}

func (g *ServiceGraph) TxReduceRelayMetrics() func() TxReduceRelayMetrics {
	if s := g.services(); s != nil {
		return s.TxReduceRelayMetrics
	}
	return nil
}

func (g *ServiceGraph) FetchInfo() func() map[string]any {
	if s := g.services(); s != nil {
		return s.FetchInfo
	}
	return nil
}

func (g *ServiceGraph) FetchInfoClear() func() {
	if s := g.services(); s != nil {
		return s.FetchInfoClear
	}
	return nil
}

func (g *ServiceGraph) RequestLedger() func([32]byte, uint32) (map[string]any, bool, bool) {
	if s := g.services(); s != nil {
		return s.RequestLedger
	}
	return nil
}

func (g *ServiceGraph) LedgerCleanerConfigure() func(LedgerCleanerParams) LedgerCleanerStatus {
	if s := g.services(); s != nil {
		return s.LedgerCleanerConfigure
	}
	return nil
}

func (g *ServiceGraph) UNLBlocked() func() bool {
	if s := g.services(); s != nil {
		return s.UNLBlocked
	}
	return nil
}

func (g *ServiceGraph) AdvisoryDeleteState() AdvisoryDeleteStore {
	if s := g.services(); s != nil {
		return s.AdvisoryDeleteState
	}
	return nil
}

func (g *ServiceGraph) AccountHistorySubscriptions() AccountHistorySubscriptionService {
	if s := g.services(); s != nil {
		return s.AccountHistorySubscriptions
	}
	return nil
}

func (g *ServiceGraph) QueueAccountTxs() func([20]byte) []QueuedTxInfo {
	if s := g.services(); s != nil {
		return s.QueueAccountTxs
	}
	return nil
}

func (g *ServiceGraph) QueueAllTxs() func() []QueuedTxInfo {
	if s := g.services(); s != nil {
		return s.QueueAllTxs
	}
	return nil
}

func cloneServiceContainer(input ServiceContainer) ServiceContainer {
	if serviceCapabilityNil(input.Manifests) {
		input.Manifests = nil
	}
	if serviceCapabilityNil(input.ValidatorList) {
		input.ValidatorList = nil
	}
	if serviceCapabilityNil(input.AdvisoryDeleteState) {
		input.AdvisoryDeleteState = nil
	}
	if serviceCapabilityNil(input.AccountHistorySubscriptions) {
		input.AccountHistorySubscriptions = nil
	}
	input.ValidatorPublicKey = append([]byte(nil), input.ValidatorPublicKey...)
	input.ServerInfoConfig = cloneServerInfoConfig(input.ServerInfoConfig)
	return input
}

func cloneServerInfoConfig(input ServerInfoConfigSnapshot) ServerInfoConfigSnapshot {
	input.Ports = append([]ServerInfoPortSnapshot(nil), input.Ports...)
	return input
}
