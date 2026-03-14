package relationaldb

import (
	"context"
	"encoding/hex"
	"fmt"
	"time"
)

// LedgerIndex represents a ledger sequence number
type LedgerIndex uint32

// Hash represents a 256-bit hash
type Hash [32]byte

// AccountID represents an XRPL account identifier
type AccountID [20]byte

// Amount represents an XRPL amount value
type Amount int64

// LedgerInfo contains basic information about a ledger
type LedgerInfo struct {
	Hash            Hash        `json:"hash"`
	Sequence        LedgerIndex `json:"sequence"`
	ParentHash      Hash        `json:"parent_hash"`
	AccountHash     Hash        `json:"account_hash"`
	TransactionHash Hash        `json:"transaction_hash"`
	TotalCoins      Amount      `json:"total_coins"`
	CloseTime       time.Time   `json:"close_time"`
	ParentCloseTime time.Time   `json:"parent_close_time"`
	CloseTimeRes    int32       `json:"close_time_res"`
	CloseFlags      uint32      `json:"close_flags"`
}

// LedgerHashPair contains a ledger hash and its parent hash
type LedgerHashPair struct {
	LedgerHash Hash `json:"ledger_hash"`
	ParentHash Hash `json:"parent_hash"`
}

// LedgerRange represents a range of ledger sequences
type LedgerRange struct {
	Min LedgerIndex `json:"min"`
	Max LedgerIndex `json:"max"`
}

// TransactionInfo contains information about a transaction
type TransactionInfo struct {
	Hash      Hash        `json:"hash"`
	LedgerSeq LedgerIndex `json:"ledger_seq"`
	TxnSeq    uint32      `json:"txn_seq"`
	Status    string      `json:"status"`
	RawTxn    []byte      `json:"raw_txn"`
	TxnMeta   []byte      `json:"txn_meta"`
	Account   AccountID   `json:"account"`
}

// AccountTxOptions contains criteria for account transaction queries
type AccountTxOptions struct {
	Account   AccountID   `json:"account"`
	MinLedger LedgerIndex `json:"min_ledger"`
	MaxLedger LedgerIndex `json:"max_ledger"`
	Offset    uint32      `json:"offset"`
	Limit     uint32      `json:"limit"`
	Unlimited bool        `json:"unlimited"`
}

// AccountTxMarker represents pagination marker for account transactions
type AccountTxMarker struct {
	LedgerSeq LedgerIndex `json:"ledger_seq"`
	TxnSeq    uint32      `json:"txn_seq"`
}

// AccountTxPageOptions contains criteria for paginated account transaction queries
type AccountTxPageOptions struct {
	Account   AccountID        `json:"account"`
	MinLedger LedgerIndex      `json:"min_ledger"`
	MaxLedger LedgerIndex      `json:"max_ledger"`
	Marker    *AccountTxMarker `json:"marker,omitempty"`
	Limit     uint32           `json:"limit"`
	Admin     bool             `json:"admin"`
}

// AccountTxResult contains the result of an account transaction query
type AccountTxResult struct {
	Transactions []TransactionInfo `json:"transactions"`
	LedgerRange  LedgerRange       `json:"ledger_range"`
	Limit        uint32            `json:"limit"`
	Marker       *AccountTxMarker  `json:"marker,omitempty"`
}

// CountMinMax contains count and range information
type CountMinMax struct {
	Count        int64       `json:"count"`
	MinLedgerSeq LedgerIndex `json:"min_ledger_seq"`
	MaxLedgerSeq LedgerIndex `json:"max_ledger_seq"`
}

// TxSearchResult represents the result of a transaction search
type TxSearchResult int

const (
	TxSearchUnknown TxSearchResult = iota
	TxSearchSome
	TxSearchAll
)

// LedgerRepository handles ledger-related database operations
type LedgerRepository interface {
	GetMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetMaxLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetLedgerInfoBySeq(ctx context.Context, seq LedgerIndex) (*LedgerInfo, error)
	GetLedgerInfoByHash(ctx context.Context, hash Hash) (*LedgerInfo, error)
	GetNewestLedgerInfo(ctx context.Context) (*LedgerInfo, error)
	GetLimitedOldestLedgerInfo(ctx context.Context, minSeq LedgerIndex) (*LedgerInfo, error)
	GetLimitedNewestLedgerInfo(ctx context.Context, minSeq LedgerIndex) (*LedgerInfo, error)
	GetHashByIndex(ctx context.Context, seq LedgerIndex) (*Hash, error)
	GetHashesByIndex(ctx context.Context, seq LedgerIndex) (*LedgerHashPair, error)
	GetHashesByRange(ctx context.Context, minSeq, maxSeq LedgerIndex) (map[LedgerIndex]LedgerHashPair, error)
	SaveValidatedLedger(ctx context.Context, ledger *LedgerInfo, current bool) error
	DeleteLedgersBySeq(ctx context.Context, maxSeq LedgerIndex) error
	GetLedgerCountMinMax(ctx context.Context) (*CountMinMax, error)
	GetKBUsedLedger(ctx context.Context) (uint32, error)
	HasLedgerSpace(ctx context.Context) (bool, error)
}

// TransactionRepository handles transaction-related database operations
type TransactionRepository interface {
	GetTransactionsMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetTransactionCount(ctx context.Context) (int64, error)
	GetTransaction(ctx context.Context, hash Hash, ledgerRange *LedgerRange) (*TransactionInfo, TxSearchResult, error)
	GetTxHistory(ctx context.Context, startIndex LedgerIndex, limit int) ([]TransactionInfo, error)
	SaveTransaction(ctx context.Context, txInfo *TransactionInfo) error
	DeleteTransactionsByLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
	DeleteTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
	GetKBUsedTransaction(ctx context.Context) (uint32, error)
	HasTransactionSpace(ctx context.Context) (bool, error)
}

// AccountTransactionRepository handles account transaction-related database operations
type AccountTransactionRepository interface {
	GetAccountTransactionsMinLedgerSeq(ctx context.Context) (*LedgerIndex, error)
	GetAccountTransactionCount(ctx context.Context) (int64, error)
	GetOldestAccountTxs(ctx context.Context, options AccountTxOptions) ([]TransactionInfo, error)
	GetNewestAccountTxs(ctx context.Context, options AccountTxOptions) ([]TransactionInfo, error)
	GetOldestAccountTxsPage(ctx context.Context, options AccountTxPageOptions) (*AccountTxResult, error)
	GetNewestAccountTxsPage(ctx context.Context, options AccountTxPageOptions) (*AccountTxResult, error)
	SaveAccountTransaction(ctx context.Context, accountID AccountID, txInfo *TransactionInfo) error
	DeleteAccountTransactionsBeforeLedgerSeq(ctx context.Context, ledgerSeq LedgerIndex) error
}

// SystemRepository handles system-level database operations
type SystemRepository interface {
	GetKBUsedAll(ctx context.Context) (uint32, error)
	Ping(ctx context.Context) error
	Begin(ctx context.Context) (TransactionContext, error)
	CloseLedgerDB(ctx context.Context) error
	CloseTransactionDB(ctx context.Context) error
}

// TransactionContext represents a database transaction context with repository access
type TransactionContext interface {
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error

	// Repository access within transaction
	Ledger() LedgerRepository
	Transaction() TransactionRepository
	AccountTransaction() AccountTransactionRepository
}

// RepositoryManager provides access to all repositories and transaction management
type RepositoryManager interface {
	// Repository access
	Ledger() LedgerRepository
	Transaction() TransactionRepository
	AccountTransaction() AccountTransactionRepository
	System() SystemRepository

	// Connection management
	Open(ctx context.Context) error
	Close(ctx context.Context) error

	// Transaction management
	WithTransaction(ctx context.Context, fn func(TransactionContext) error) error
}

// Helper methods for Hash type
func (h Hash) String() string {
	return fmt.Sprintf("%x", h[:])
}

func (h Hash) IsZero() bool {
	for _, b := range h {
		if b != 0 {
			return false
		}
	}
	return true
}

// ParseHash parses a hex string into a Hash
func ParseHash(s string) (Hash, error) {
	var h Hash
	if len(s) != 64 {
		return h, fmt.Errorf("invalid hash length: expected 64, got %d", len(s))
	}

	decoded, err := hex.DecodeString(s)
	if err != nil {
		return h, fmt.Errorf("invalid hex string: %w", err)
	}

	copy(h[:], decoded)
	return h, nil
}

// Helper methods for AccountID type
func (a AccountID) String() string {
	return fmt.Sprintf("%x", a[:])
}

func (a AccountID) IsZero() bool {
	for _, b := range a {
		if b != 0 {
			return false
		}
	}
	return true
}

// ParseAccountID parses a hex string into an AccountID
func ParseAccountID(s string) (AccountID, error) {
	var a AccountID
	if len(s) != 40 {
		return a, fmt.Errorf("invalid account ID length: expected 40, got %d", len(s))
	}

	decoded, err := hex.DecodeString(s)
	if err != nil {
		return a, fmt.Errorf("invalid hex string: %w", err)
	}

	copy(a[:], decoded)
	return a, nil
}
