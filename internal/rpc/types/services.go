package types

import (
	"context"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/keylet"
)

// MethodDispatcher allows forwarding RPC calls to the method registry.
// Used by the 'json' RPC method to proxy calls.
type MethodDispatcher interface {
	ExecuteMethod(method string, params []byte) (interface{}, *RpcError)
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

	// PeerCount returns the number of connected peers (nil when not in consensus mode)
	PeerCount func() int

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
}

// LedgerNavigator provides ledger index navigation and mode queries.
type LedgerNavigator interface {
	GetCurrentLedgerIndex() uint32
	GetClosedLedgerIndex() uint32
	GetValidatedLedgerIndex() uint32
	AcceptLedger(ctx context.Context) (uint32, error)
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
	GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error)
	GetLedgerData(ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error)
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
}

// AccountQuerier provides account-related read operations.
type AccountQuerier interface {
	GetAccountInfo(account string, ledgerIndex string) (*AccountInfo, error)
	GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*AccountLinesResult, error)
	GetAccountOffers(account string, ledgerIndex string, limit uint32) (*AccountOffersResult, error)
	GetAccountTransactions(ctx context.Context, account string, ledgerMin, ledgerMax int64, limit uint32, marker *AccountTxMarker, forward bool) (*AccountTxResult, error)
	GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*AccountChannelsResult, error)
	GetAccountCurrencies(account string, ledgerIndex string) (*AccountCurrenciesResult, error)
	GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*AccountObjectsResult, error)
	GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*AccountNFTsResult, error)
}

// LedgerService is the full interface for ledger operations.
// It composes the sub-interfaces and includes remaining methods.
type LedgerService interface {
	LedgerNavigator
	LedgerAccessor
	TransactionSubmitter
	AccountQuerier

	// Book and market data
	GetBookOffers(takerGets, takerPays Amount, ledgerIndex string, limit uint32) (*BookOffersResult, error)

	// Gateway operations
	GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error)
	GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error)
	GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string, credentials []string) (*DepositAuthorizedResult, error)

	// NFT operations
	GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
	GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
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
	Standalone          bool
	ServerState         string
	OpenLedgerSeq       uint32
	ClosedLedgerSeq     uint32
	ClosedLedgerHash    [32]byte
	ValidatedLedgerSeq  uint32
	ValidatedLedgerHash [32]byte
	CompleteLedgers     string
	NetworkID           uint32
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

// BookOffer represents an offer in an order book
type BookOffer struct {
	Account         string      `json:"Account"`
	BookDirectory   string      `json:"BookDirectory"`
	BookNode        string      `json:"BookNode"`
	Flags           uint32      `json:"Flags"`
	LedgerEntryType string      `json:"LedgerEntryType"`
	OwnerNode       string      `json:"OwnerNode"`
	Sequence        uint32      `json:"Sequence"`
	TakerGets       interface{} `json:"TakerGets"`
	TakerPays       interface{} `json:"TakerPays"`
	Index           string      `json:"index"`
	Quality         string      `json:"quality"`
	OwnerFunds      string      `json:"owner_funds,omitempty"`
	TakerGetsFunded interface{} `json:"taker_gets_funded,omitempty"`
	TakerPaysFunded interface{} `json:"taker_pays_funded,omitempty"`
}

// BookOffersResult contains the result of book_offers RPC
type BookOffersResult struct {
	LedgerIndex uint32      `json:"ledger_index"`
	LedgerHash  [32]byte    `json:"ledger_hash"`
	Offers      []BookOffer `json:"offers"`
	Validated   bool        `json:"validated"`
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
