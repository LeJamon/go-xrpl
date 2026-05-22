package types

import (
	"context"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/keylet"
)

// MethodDispatcher allows forwarding RPC calls to the method registry.
// Used by the 'json' RPC method to proxy calls.
type MethodDispatcher interface {
	ExecuteMethod(method string, params []byte) (interface{}, *RpcError)
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
	// Publishers returns a snapshot of per-publisher state for the
	// `validators` RPC.
	Publishers() []ValidatorListPublisherInfo
	// Sites returns a snapshot of per-URL polling state for the
	// `validator_list_sites` RPC.
	Sites() []ValidatorListSiteInfo
	// TrustedMasterKeys returns the master pubkeys currently in the
	// effective trusted UNL contributed by publishers.
	TrustedMasterKeys() [][33]byte
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

	// Dispatcher forwards RPC calls (used by 'json' method)
	Dispatcher MethodDispatcher

	// ShutdownFunc gracefully stops the server (used by 'stop' method)
	ShutdownFunc func()

	// NodePublicKey is the base58-encoded node identity public key (e.g. "n9...")
	NodePublicKey string

	// LastCloseInfo returns proposer count and convergence time (ms) from the last consensus round
	LastCloseInfo func() (proposers int, convergeTimeMs int)

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

	// SigningKeysBase58 returns the master→signing key map projected as
	// base58 strings. Surfaced by the `validators` RPC as `signing_keys`
	// (rippled getJson at ValidatorList.cpp:1725-1734). Nil-safe.
	SigningKeysBase58 func() map[string]string

	// NegativeUNLBase58 returns the current negative-UNL set, base58-
	// encoded. Surfaced by the `validators` RPC as `NegativeUNL`
	// (rippled getJson at ValidatorList.cpp:1737-1744). Nil-safe.
	NegativeUNLBase58 func() []string

	// TxQMetrics returns the current transaction-queue metrics used by
	// server_info for the load_factor_fee_* triple. Nil until the
	// ledger service is wired (standalone tests, pre-startup) —
	// server_info falls back to baseline values.
	TxQMetrics func() TxQServerMetrics

	// JqTransOverflow returns the cumulative inbound TMTransaction
	// frames the overlay refused at the router-dispatch boundary
	// because the in-flight tx ceiling was already met. This is
	// goxrpl's analog of rippled's OverlayImpl::getJqTransOverflow
	// (bumped at PeerImp.cpp:1353 when
	// `getJobCount(jtTRANSACTION) > MAX_TRANSACTIONS`) and drives
	// server_info.jq_trans_overflow. Nil in standalone / RPC-only
	// configurations (no overlay) — handler reads zero.
	JqTransOverflow func() uint64

	// PeerDisconnects returns cumulative peer-disconnect counters
	// surfaced by server_info: (total, resources-driven). Nil when
	// the overlay isn't wired (standalone, RPC-only tests).
	PeerDisconnects func() (total, resources uint64)

	// StateAccounting returns the operating-mode state-machine
	// snapshot surfaced by server_info: per-mode counts/durations
	// plus the current-state and initial-sync durations. The Modes
	// map is empty until consensus is wired.
	StateAccounting func() StateAccountingSnapshot

	// CloseTimeOffset returns the consensus-derived close-time offset
	// from the adaptor. Surfaced as close_time_offset on the ledger
	// object in human mode when |offset| >= 60s
	// (NetworkOPs.cpp:2946-2949). Nil before consensus is wired.
	CloseTimeOffset func() time.Duration

	// LoadFactorFees returns the LoadFeeTrack local/net/cluster fees
	// driving the admin-only human-mode load_factor_local/net/cluster
	// emits (NetworkOPs.cpp:2887-2901). Nil until a LoadFeeTrack
	// subsystem lands — handler suppresses the fields when nil.
	LoadFactorFees func() LoadFactorFees
}

// LedgerNavigator provides ledger index navigation and mode queries.
type LedgerNavigator interface {
	GetCurrentLedgerIndex() uint32
	GetClosedLedgerIndex() uint32
	GetValidatedLedgerIndex() uint32
	AcceptLedger(ctx context.Context) (uint32, error)
	AcceptLedgerAt(ctx context.Context, closeTime time.Time) (uint32, error)
	IsStandalone() bool
}

// LedgerAccessor provides ledger retrieval and server metadata.
type LedgerAccessor interface {
	GetLedgerBySequence(seq uint32) (LedgerReader, error)
	GetLedgerByHash(hash [32]byte) (LedgerReader, error)
	GetServerInfo() LedgerServerInfo
	GetGenesisAccount() (string, error)
	GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64)
	GetLedgerRange(ctx context.Context, minSeq, maxSeq uint32) (*LedgerRangeResult, error)
	GetLedgerEntry(ctx context.Context, entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error)
	GetLedgerData(ctx context.Context, ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error)
	GetClosedLedgerView() (LedgerStateView, error)
	IsAmendmentBlocked() bool
}

// TransactionSubmitter handles transaction submission and retrieval.
type TransactionSubmitter interface {
	SubmitTransaction(txJSON []byte, txBlobHex ...string) (*SubmitResult, error)
	SimulateTransaction(txJSON []byte) (*SubmitResult, error)
	GetTransaction(txHash [32]byte) (*TransactionInfo, error)
	StoreTransaction(txHash [32]byte, txData []byte) error
	GetTransactionHistory(ctx context.Context, startIndex uint32) (*TxHistoryResult, error)

	// GetAutofillFee returns the Fee a transaction should carry to enter
	// the open ledger. Mirrors rippled getCurrentNetworkFee
	// (TransactionSign.cpp:839-877): max(scaleFeeLoad(feeDefault),
	// escalatedFee) with a feeDefault * mult / div ceiling. On ceiling
	// overflow handlers map to rpcINTERNAL; on exceedance the returned
	// error is a *svcerr.HighFeeError (errors.Is(svcerr.ErrHighFee) also
	// matches). Includes per-tx-type adjustments (multisign, AccountDelete,
	// AMMCreate, LedgerStateFix). Never reads the source account.
	//
	// unlimited mirrors rippled's isUnlimited(role) carve-out: admin /
	// identified callers skip local-only load below 4x remote. The
	// ceiling check still applies (rippled enforces it post-scale).
	GetAutofillFee(txJSON []byte, unlimited bool) (fee uint64, err error)

	// GetAutofillSequence returns the Sequence a transaction should
	// carry. Mirrors rippled getAutofillSequence (Simulate.cpp:37-69):
	// returns 0 when hasTicketSequence is true; otherwise reads the
	// account SLE and consults TxQ.NextQueuableSeq. Returns
	// svcerr.ErrAccountNotFound when the account is absent and no ticket
	// supersedes the requirement.
	GetAutofillSequence(account string, hasTicketSequence bool) (sequence uint32, err error)
}

// AccountQuerier provides account-related read operations.
type AccountQuerier interface {
	GetAccountInfo(ctx context.Context, account string, ledgerIndex string) (*AccountInfo, error)
	GetAccountLines(ctx context.Context, account string, ledgerIndex string, peer string, limit uint32) (*AccountLinesResult, error)
	GetAccountOffers(ctx context.Context, account string, ledgerIndex string, limit uint32) (*AccountOffersResult, error)
	GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *AccountTxMarker, forward bool) (*AccountTxResult, error)
	GetAccountChannels(ctx context.Context, account string, destinationAccount string, ledgerIndex string, limit uint32) (*AccountChannelsResult, error)
	GetAccountCurrencies(ctx context.Context, account string, ledgerIndex string) (*AccountCurrenciesResult, error)
	GetAccountObjects(ctx context.Context, account string, ledgerIndex string, objType string, limit uint32) (*AccountObjectsResult, error)
	GetAccountNFTs(ctx context.Context, account string, ledgerIndex string, limit uint32) (*AccountNFTsResult, error)
}

// LedgerService is the full interface for ledger operations.
// It composes the sub-interfaces and includes remaining methods.
type LedgerService interface {
	LedgerNavigator
	LedgerAccessor
	TransactionSubmitter
	AccountQuerier

	// Book and market data
	GetBookOffers(ctx context.Context, takerGets, takerPays Amount, taker, domain string, ledgerIndex string, limit uint32, marker string) (*BookOffersResult, error)

	// Gateway operations
	GetGatewayBalances(ctx context.Context, account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error)
	GetNoRippleCheck(ctx context.Context, account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error)
	GetDepositAuthorized(ctx context.Context, sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*DepositAuthorizedResult, error)

	// NFT operations
	GetNFTBuyOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
	GetNFTSellOffers(ctx context.Context, nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
}

// LedgerStateView provides low-level read access to ledger state.
// This interface matches tx.LedgerView for pathfinding and other operations
// that need direct state access. Any *ledger.Ledger satisfies this.
type LedgerStateView interface {
	Read(k keylet.Keylet) ([]byte, error)
	Exists(k keylet.Keylet) (bool, error)
	Insert(k keylet.Keylet, data []byte) error
	Update(k keylet.Keylet, data []byte) error
	Erase(k keylet.Keylet) error
	ForEach(fn func(key [32]byte, data []byte) bool) error
	Succ(key [32]byte) ([32]byte, []byte, bool, error)
	AdjustDropsDestroyed(d drops.XRPAmount)
	TxExists(txID [32]byte) bool
	Rules() *amendment.Rules
	LedgerSeq() uint32
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

// LedgerServerInfo contains server status information from the ledger service
type LedgerServerInfo struct {
	Standalone            bool
	ServerState           string
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

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32

	// Metadata is nil when the transaction produced no metadata.
	Metadata *SubmitMetadata
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
}

// Amount represents a currency amount (XRP or IOU)
type Amount struct {
	Value    string `json:"value,omitempty"`
	Currency string `json:"currency,omitempty"`
	Issuer   string `json:"issuer,omitempty"`
}

// IsNative returns true if this is an XRP amount (not an IOU)
func (a Amount) IsNative() bool {
	return a.Currency == "" && a.Issuer == ""
}

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
	Flags      uint32      `json:"flags"`
	Seq        uint32      `json:"seq"`
	TakerGets  interface{} `json:"taker_gets"`
	TakerPays  interface{} `json:"taker_pays"`
	Quality    string      `json:"quality"`
	Expiration uint32      `json:"expiration,omitempty"`
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
	Account           string                   `json:"Account"`
	BookDirectory     string                   `json:"BookDirectory"`
	BookNode          string                   `json:"BookNode"`
	Expiration        uint32                   `json:"Expiration,omitempty"`
	Flags             uint32                   `json:"Flags"`
	LedgerEntryType   string                   `json:"LedgerEntryType"`
	OwnerNode         string                   `json:"OwnerNode"`
	PreviousTxnID     string                   `json:"PreviousTxnID"`
	PreviousTxnLgrSeq uint32                   `json:"PreviousTxnLgrSeq"`
	Sequence          uint32                   `json:"Sequence"`
	TakerGets         interface{}              `json:"TakerGets"`
	TakerPays         interface{}              `json:"TakerPays"`
	DomainID          string                   `json:"DomainID,omitempty"`
	AdditionalBooks   []map[string]interface{} `json:"AdditionalBooks,omitempty"`
	Index             string                   `json:"index"`
	Quality           string                   `json:"quality"`
	OwnerFunds        string                   `json:"owner_funds,omitempty"`
	TakerGetsFunded   interface{}              `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded   interface{}              `json:"taker_pays_funded,omitempty"`
}

// BookOffersResult contains the result of book_offers RPC
type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
	// Marker is the resume token for the next page (64-hex offer index).
	// Empty when the book has been fully walked. goXRPL extension —
	// rippled's BookOffers handler accepts a marker parameter but never
	// emits one.
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

// NoRippleProblem describes a trust line with incorrect NoRipple settings
type NoRippleProblem struct {
	Message  string `json:"message"`
	Currency string `json:"currency"`
	Peer     string `json:"peer"`
}

// SuggestedTransaction represents a suggested transaction to fix NoRipple issues
type SuggestedTransaction struct {
	TransactionType string                 `json:"TransactionType"`
	Account         string                 `json:"Account"`
	Fee             string                 `json:"Fee"`
	Sequence        uint32                 `json:"Sequence"`
	SetFlag         uint32                 `json:"SetFlag,omitempty"`
	Flags           uint32                 `json:"Flags,omitempty"`
	LimitAmount     map[string]interface{} `json:"LimitAmount,omitempty"`
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
	NFTOfferIndex string      `json:"nft_offer_index"`
	Flags         uint32      `json:"flags"`
	Owner         string      `json:"owner"`
	Amount        interface{} `json:"amount"`                // Can be string (XRP drops) or object (IOU)
	Destination   string      `json:"destination,omitempty"` // Optional
	Expiration    uint32      `json:"expiration,omitempty"`  // Optional
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

// NewServiceContainer constructs a ServiceContainer wired to the given
// ledger service. Callers attach the dispatcher, peer hooks, manifest
// cache, etc. afterwards as components come online.
func NewServiceContainer(ledger LedgerService) *ServiceContainer {
	return &ServiceContainer{
		Ledger: ledger,
	}
}

// SetDispatcher sets the method dispatcher on the service container.
func (sc *ServiceContainer) SetDispatcher(d MethodDispatcher) {
	sc.Dispatcher = d
}

// SetShutdownFunc sets the shutdown function on the service container.
func (sc *ServiceContainer) SetShutdownFunc(f func()) {
	sc.ShutdownFunc = f
}
