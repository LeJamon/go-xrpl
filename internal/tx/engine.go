package tx

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/amendment"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
	xrpllog "github.com/LeJamon/goXRPLd/log"
	"github.com/LeJamon/goXRPLd/protocol"
)

// Validation constants matching rippled
const (
	// MaxMemoSize is the maximum total serialized size of memos (in bytes)
	MaxMemoSize = 1024

	// MaxMemoTypeSize is the maximum size of MemoType field (in bytes)
	MaxMemoTypeSize = 256

	// MaxMemoDataSize is the maximum size of MemoData field (in bytes)
	MaxMemoDataSize = 1024

	// LegacyNetworkIDThreshold is the threshold for legacy network IDs
	// Networks with ID <= this value are legacy networks
	LegacyNetworkIDThreshold = 1024

	// DefaultMaxFee is the maximum legal fee amount matching rippled's INITIAL_XRP.
	// Reference: rippled SystemParameters.h isLegalAmount() — fee <= INITIAL_XRP
	DefaultMaxFee = 100_000_000_000_000_000 // 100 billion XRP in drops

	// QualityOne is the identity transfer rate (1e9). Alias for protocol.QualityOne.
	QualityOne = protocol.QualityOne
)

// Engine processes transactions against a ledger
type Engine struct {
	// View provides access to ledger state
	view LedgerView

	// Config holds engine configuration
	config EngineConfig

	// logger is the scoped logger for the Tx partition.
	// Always non-nil; falls back to xrpllog.Discard() when not configured.
	logger xrpllog.Logger

	// currentTxHash is the hash of the transaction currently being applied
	// Used to set PreviousTxnID on modified ledger entries
	currentTxHash [32]byte

	// txCount tracks the number of applied transactions for TransactionIndex.
	// Each applied transaction (tesSUCCESS or tec) gets the current count as
	// its TransactionIndex, then the counter increments.
	// Reference: rippled OpenView::txCount() = baseTxCount_ + txs_.size()
	txCount uint32
}

// ApplyFlags controls transaction application behavior during consensus.
// Reference: rippled ApplyView.h ApplyFlags enum
type ApplyFlags uint32

const (
	TapNONE      ApplyFlags = 0x00
	TapFAIL_HARD ApplyFlags = 0x10  // Local tx with fail_hard flag
	TapRETRY     ApplyFlags = 0x20  // Not the tx's last pass — tec from preclaim is not applied
	TapUNLIMITED ApplyFlags = 0x400 // Privileged source
)

// EngineConfig holds configuration for the transaction engine
type EngineConfig struct {
	// BaseFee is the current base fee in drops
	BaseFee uint64

	// ReserveBase is the base reserve in drops
	ReserveBase uint64

	// ReserveIncrement is the owner reserve increment in drops
	ReserveIncrement uint64

	// LedgerSequence is the current ledger sequence
	LedgerSequence uint32

	// SkipSignatureVerification skips signature checks (for testing/standalone)
	SkipSignatureVerification bool

	// Standalone indicates if running in standalone mode (relaxes some validation)
	Standalone bool

	// NetworkID is the network identifier for this node
	// Networks with ID > 1024 require NetworkID in transactions
	// Networks with ID <= 1024 are legacy networks and cannot have NetworkID in transactions
	NetworkID uint32

	// MaxFee is the maximum allowed fee in drops (default 1 XRP = 1000000 drops)
	// Transactions with fees exceeding this will be rejected in preflight
	MaxFee uint64

	// ParentCloseTime is the close time of the parent ledger (in Ripple epoch seconds)
	// This is used for checking offer/escrow expiration
	ParentCloseTime uint32

	// ParentHash is the hash of the parent ledger.
	// Used by pseudoAccountAddress for deterministic AMM account derivation.
	// Reference: rippled View.cpp pseudoAccountAddress uses view.info().parentHash
	ParentHash [32]byte

	// Rules contains the amendment rules for this ledger.
	// If nil, defaults to all amendments enabled (for backwards compatibility).
	Rules *amendment.Rules

	// OpenLedger controls whether fee adequacy is checked.
	// When true, the engine verifies that the transaction fee meets the
	// minimum required fee (including tx-type-specific overrides like
	// AccountDelete's owner reserve). When false, fee adequacy is
	// skipped — only basic fee validity (non-negative, legal amount,
	// sufficient balance) is checked.
	// Reference: rippled Transactor.cpp checkFee — "Only check fee is
	// sufficient when the ledger is open."
	OpenLedger bool

	// ApplyFlags controls transaction application behavior.
	// TapRETRY means this is not the tx's last pass: tec results from
	// preclaim are not applied (likelyToClaimFee = false), allowing the
	// tx to be retried on the next pass.
	// Reference: rippled Transactor.cpp / BuildLedger.cpp
	ApplyFlags ApplyFlags

	// Logger is the logger to use for this engine instance.
	// If nil, xrpllog.Discard() is used — safe for tests and zero-value construction.
	Logger xrpllog.Logger
}

// GetRules returns the amendment rules, falling back to AllSupportedRules if nil.
// This is the same fallback used by Engine.rules() and ApplyContext.Rules().
func (c EngineConfig) GetRules() *amendment.Rules {
	if c.Rules != nil {
		return c.Rules
	}
	return amendment.AllSupportedRules()
}

// LedgerView provides read/write access to ledger state
type LedgerView interface {
	// Read reads a ledger entry
	Read(k keylet.Keylet) ([]byte, error)

	// Exists checks if an entry exists
	Exists(k keylet.Keylet) (bool, error)

	// Insert adds a new entry
	Insert(k keylet.Keylet, data []byte) error

	// Update modifies an existing entry
	Update(k keylet.Keylet, data []byte) error

	// Erase removes an entry
	Erase(k keylet.Keylet) error

	// AdjustDropsDestroyed records destroyed XRP
	AdjustDropsDestroyed(drops drops.XRPAmount)

	// ForEach iterates over all state entries
	// If fn returns false, iteration stops early
	ForEach(fn func(key [32]byte, data []byte) bool) error

	// Succ returns the first entry with key > the given key.
	// Returns (key, data, true, nil) if found, or ([32]byte{}, nil, false, nil) if not.
	// Reference: rippled ReadView::succ() used for efficient ordered traversal.
	Succ(key [32]byte) ([32]byte, []byte, bool, error)

	// TxExists returns true if a transaction with the given hash has already been
	// applied to the current open ledger. Used by invariant checkers and duplicate
	// transaction detection.
	// Reference: rippled ReadView::txExists()
	TxExists(txID [32]byte) bool

	// Rules returns the amendment rules for this view.
	// Returns nil if rules are not available.
	Rules() *amendment.Rules
}

// NewEngine creates a new transaction engine
func NewEngine(view LedgerView, config EngineConfig) *Engine {
	logger := config.Logger
	if logger == nil {
		logger = xrpllog.Discard()
	}
	return &Engine{
		view:   view,
		config: config,
		logger: logger.Named(xrpllog.PartitionTx),
	}
}

// rules returns the amendment rules, defaulting to all amendments enabled if nil.
// This provides backwards compatibility for code that doesn't set Rules.
func (e *Engine) rules() *amendment.Rules {
	if e.config.Rules != nil {
		return e.config.Rules
	}
	// Default to all supported amendments enabled for backwards compatibility
	return amendment.AllSupportedRules()
}

// TxCount returns the current transaction count (for batch baseTxCount).
// Reference: rippled OpenView::txCount()
func (e *Engine) TxCount() uint32 {
	return e.txCount
}

// SetBaseTxCount sets the base transaction count for batch inner transactions.
// Inner transactions start numbering from this value.
// Reference: rippled OpenView::baseTxCount_ initialized from parent view
func (e *Engine) SetBaseTxCount(count uint32) {
	e.txCount = count
}

// ComputeTransactionHash computes the hash of a transaction.
// The hash is SHA512Half of the "TXN\x00" prefix + serialized transaction.
func ComputeTransactionHash(tx Transaction) ([32]byte, error) {
	return computeTransactionHash(tx)
}

// computeTransactionHash computes the hash of a transaction
// The hash is SHA512Half of the "TXN\x00" prefix + serialized transaction
func computeTransactionHash(tx Transaction) ([32]byte, error) {
	var hash [32]byte
	var txBytes []byte

	// Use raw bytes if available (from parsing), otherwise re-serialize
	if rawBytes := tx.GetRawBytes(); len(rawBytes) > 0 {
		txBytes = rawBytes
	} else {
		// Serialize the transaction using Flatten
		txMap, err := tx.Flatten()
		if err != nil {
			return hash, err
		}

		// Encode to binary using the binary codec
		hexStr, err := binarycodec.Encode(txMap)
		if err != nil {
			return hash, err
		}

		txBytes, err = hex.DecodeString(hexStr)
		if err != nil {
			return hash, err
		}
	}

	// Prefix is "TXN\x00" = 0x54584E00
	prefix := []byte{0x54, 0x58, 0x4E, 0x00}
	data := append(prefix, txBytes...)

	hash = common.Sha512Half(data)
	return hash, nil
}

// adjustOwnerCountOnView modifies an account's OwnerCount on a LedgerView.
// Used by the engine for tecOVERSIZE offer cleanup after the sandbox is discarded.
// Reference: rippled removeUnfundedOffers() adjusts owner count on the base view.
func adjustOwnerCountOnView(view LedgerView, account [20]byte, delta int, txHash [32]byte, ledgerSeq uint32) {
	_ = AdjustOwnerCountWithTx(view, account, delta, txHash, ledgerSeq)
}

// deleteNFTokenOfferOnView deletes an NFTokenOffer from the ledger view,
// removing it from owner directory, NFTBuys/NFTSells directory, and erasing the SLE.
// Used for tecEXPIRED re-deletion of expired NFToken offers.
// Reference: rippled NFTokenUtils.cpp deleteTokenOffer
func deleteNFTokenOfferOnView(view LedgerView, offerKL keylet.Keylet, txHash [32]byte, ledgerSeq uint32) {
	offerData, err := view.Read(offerKL)
	if err != nil || offerData == nil {
		return
	}

	offer, err := state.ParseNFTokenOffer(offerData)
	if err != nil {
		return
	}

	ownerDirKey := keylet.OwnerDir(offer.Owner)
	state.DirRemove(view, ownerDirKey, offer.OwnerNode, offerKL.Key, false)

	// Remove from NFTBuys or NFTSells directory
	const lsfSellNFToken = 0x00000001
	isSellOffer := offer.Flags&lsfSellNFToken != 0
	var tokenDirKey keylet.Keylet
	if isSellOffer {
		tokenDirKey = keylet.NFTSells(offer.NFTokenID)
	} else {
		tokenDirKey = keylet.NFTBuys(offer.NFTokenID)
	}
	state.DirRemove(view, tokenDirKey, offer.NFTokenOfferNode, offerKL.Key, false)

	_ = view.Erase(offerKL)
	adjustOwnerCountOnView(view, offer.Owner, -1, txHash, ledgerSeq)
}
