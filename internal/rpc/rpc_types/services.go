package rpc_types

// Services provides access to core services from RPC handlers
// This is a singleton that holds references to the services
// needed by RPC method handlers.
var Services *ServiceContainer

// MethodDispatcher allows forwarding RPC calls to the method registry.
// Used by the 'json' RPC method to proxy calls.
type MethodDispatcher interface {
	ExecuteMethod(method string, params []byte) (interface{}, *RpcError)
}

// ServiceContainer holds references to all services needed by RPC handlers
type ServiceContainer struct {
	// LedgerService provides ledger operations
	Ledger LedgerService

	// Dispatcher forwards RPC calls (used by 'json' method)
	Dispatcher MethodDispatcher

	// ShutdownFunc gracefully stops the server (used by 'stop' method)
	ShutdownFunc func()
}

// LedgerService interface for ledger operations
type LedgerService interface {
	// GetCurrentLedgerIndex returns the current open ledger index
	GetCurrentLedgerIndex() uint32

	// GetClosedLedgerIndex returns the last closed ledger index
	GetClosedLedgerIndex() uint32

	// GetValidatedLedgerIndex returns the highest validated ledger index
	GetValidatedLedgerIndex() uint32

	// AcceptLedger closes the current open ledger (standalone mode only)
	AcceptLedger() (uint32, error)

	// IsStandalone returns true if running in standalone mode
	IsStandalone() bool

	// GetServerInfo returns server status information
	GetServerInfo() LedgerServerInfo

	// GetLedgerBySequence returns a ledger by its sequence number
	GetLedgerBySequence(seq uint32) (LedgerReader, error)

	// GetLedgerByHash returns a ledger by its hash
	GetLedgerByHash(hash [32]byte) (LedgerReader, error)

	// GetGenesisAccount returns the genesis account address
	GetGenesisAccount() (string, error)

	// SubmitTransaction submits a transaction to the open ledger
	SubmitTransaction(txJSON []byte) (*SubmitResult, error)

	// GetCurrentFees returns the current fee settings
	GetCurrentFees() (baseFee, reserveBase, reserveIncrement uint64)

	// GetAccountInfo retrieves account information from the ledger
	GetAccountInfo(account string, ledgerIndex string) (*AccountInfo, error)

	// GetTransaction retrieves a transaction by its hash
	GetTransaction(txHash [32]byte) (*TransactionInfo, error)

	// StoreTransaction stores a transaction in the current ledger
	StoreTransaction(txHash [32]byte, txData []byte) error

	// GetAccountLines retrieves trust lines for an account
	GetAccountLines(account string, ledgerIndex string, peer string, limit uint32) (*AccountLinesResult, error)

	// GetAccountOffers retrieves offers for an account
	GetAccountOffers(account string, ledgerIndex string, limit uint32) (*AccountOffersResult, error)

	// GetBookOffers retrieves offers from an order book
	GetBookOffers(takerGets, takerPays Amount, ledgerIndex string, limit uint32) (*BookOffersResult, error)

	// GetAccountTransactions retrieves transaction history for an account
	GetAccountTransactions(account string, ledgerMin, ledgerMax int64, limit uint32, marker *AccountTxMarker, forward bool) (*AccountTxResult, error)

	// GetTransactionHistory retrieves recent transactions
	GetTransactionHistory(startIndex uint32) (*TxHistoryResult, error)

	// GetLedgerRange retrieves ledger hashes for a range of sequences
	GetLedgerRange(minSeq, maxSeq uint32) (*LedgerRangeResult, error)

	// GetLedgerEntry retrieves a specific ledger entry by its index/key
	GetLedgerEntry(entryKey [32]byte, ledgerIndex string) (*LedgerEntryResult, error)

	// GetLedgerData retrieves all ledger state entries with pagination
	GetLedgerData(ledgerIndex string, limit uint32, marker string) (*LedgerDataResult, error)

	// GetAccountObjects retrieves all objects owned by an account
	GetAccountObjects(account string, ledgerIndex string, objType string, limit uint32) (*AccountObjectsResult, error)

	// GetAccountChannels retrieves payment channels for an account
	GetAccountChannels(account string, destinationAccount string, ledgerIndex string, limit uint32) (*AccountChannelsResult, error)

	// GetAccountCurrencies retrieves currencies an account can send and receive
	GetAccountCurrencies(account string, ledgerIndex string) (*AccountCurrenciesResult, error)

	// GetAccountNFTs retrieves NFTs owned by an account
	GetAccountNFTs(account string, ledgerIndex string, limit uint32) (*AccountNFTsResult, error)

	// GetGatewayBalances retrieves obligations and balances for a gateway account
	GetGatewayBalances(account string, hotWallets []string, ledgerIndex string) (*GatewayBalancesResult, error)

	// GetNoRippleCheck checks trust lines for proper NoRipple flag settings
	GetNoRippleCheck(account string, role string, ledgerIndex string, limit uint32, transactions bool) (*NoRippleCheckResult, error)

	// GetDepositAuthorized checks if a source account is authorized to deposit to a destination account
	GetDepositAuthorized(sourceAccount string, destinationAccount string, ledgerIndex string) (*DepositAuthorizedResult, error)

	// GetNFTBuyOffers retrieves buy offers for an NFToken
	GetNFTBuyOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)

	// GetNFTSellOffers retrieves sell offers for an NFToken
	GetNFTSellOffers(nftID [32]byte, ledgerIndex string, limit uint32, marker string) (*NFTOffersResult, error)
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
	Account      string
	Balance      string
	Flags        uint32
	OwnerCount   uint32
	Sequence     uint32
	RegularKey   string
	Domain       string
	EmailHash    string
	TransferRate uint32
	TickSize     uint8
	LedgerIndex  uint32
	LedgerHash   string
	Validated    bool
}

// LedgerReader provides read access to a ledger
type LedgerReader interface {
	Sequence() uint32
	Hash() [32]byte
	ParentHash() [32]byte
	IsClosed() bool
	IsValidated() bool
	TotalDrops() uint64
}

// LedgerServerInfo contains server status information from the ledger service
type LedgerServerInfo struct {
	Standalone          bool
	OpenLedgerSeq       uint32
	ClosedLedgerSeq     uint32
	ClosedLedgerHash    [32]byte
	ValidatedLedgerSeq  uint32
	ValidatedLedgerHash [32]byte
	CompleteLedgers     string
}

// SubmitResult contains the result of submitting a transaction
type SubmitResult struct {
	// EngineResult is the result code string (e.g., "tesSUCCESS")
	EngineResult string

	// EngineResultCode is the numeric result code
	EngineResultCode int

	// EngineResultMessage is a human-readable result message
	EngineResultMessage string

	// Applied indicates if the transaction was applied to the ledger
	Applied bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// CurrentLedger is the current open ledger sequence
	CurrentLedger uint32

	// ValidatedLedger is the highest validated ledger sequence
	ValidatedLedger uint32
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
	Flags         uint16 `json:"Flags"`
	Issuer        string `json:"Issuer"`
	NFTokenID     string `json:"NFTokenID"`
	NFTokenTaxon  uint32 `json:"NFTokenTaxon"`
	URI           string `json:"URI,omitempty"`
	NFTSerial     uint32 `json:"nft_serial"`
	TransferFee   uint16 `json:"transfer_fee,omitempty"`
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
	Account        string                         `json:"account"`
	Obligations    map[string]string              `json:"obligations,omitempty"`    // currency -> value
	Balances       map[string][]CurrencyBalance   `json:"balances,omitempty"`       // account -> []balance
	FrozenBalances map[string][]CurrencyBalance   `json:"frozen_balances,omitempty"` // account -> []balance
	Assets         map[string][]CurrencyBalance   `json:"assets,omitempty"`         // account -> []balance
	Locked         map[string]string              `json:"locked,omitempty"`         // currency -> value (escrows)
	LedgerIndex    uint32                         `json:"ledger_index"`
	LedgerHash     [32]byte                       `json:"ledger_hash"`
	Validated      bool                           `json:"validated"`
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
	Amount        interface{} `json:"amount"`               // Can be string (XRP drops) or object (IOU)
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

// InitServices initializes the service container
func InitServices(ledger LedgerService) {
	Services = &ServiceContainer{
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
