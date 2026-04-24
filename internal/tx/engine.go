package tx

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx/invariants"
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

// engineSignerListLookup implements SignerListLookup using the engine's ledger view
type engineSignerListLookup struct {
	view LedgerView
}

// GetSignerList returns the signer list for an account
func (l *engineSignerListLookup) GetSignerList(account string) (*state.SignerListInfo, error) {
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return nil, err
	}

	// Look up the signer list (SignerListID is always 0 currently)
	signerListKey := keylet.SignerList(accountID)
	exists, err := l.view.Exists(signerListKey)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil // No signer list
	}

	// Read and parse the signer list
	signerListData, err := l.view.Read(signerListKey)
	if err != nil {
		return nil, err
	}

	signerList, err := state.ParseSignerList(signerListData)
	if err != nil {
		return nil, err
	}

	return signerList, nil
}

// GetAccountInfo returns account information needed for signer validation
func (l *engineSignerListLookup) GetAccountInfo(account string) (flags uint32, regularKey string, err error) {
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return 0, "", err
	}

	accountKey := keylet.Account(accountID)
	exists, err := l.view.Exists(accountKey)
	if err != nil {
		return 0, "", err
	}
	if !exists {
		return 0, "", errors.New("account not found")
	}

	accountData, err := l.view.Read(accountKey)
	if err != nil {
		return 0, "", err
	}

	accountRoot, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return 0, "", err
	}

	return accountRoot.Flags, accountRoot.RegularKey, nil
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

// ApplyResult contains the result of applying a transaction
type ApplyResult struct {
	// Result is the transaction result code
	Result Result

	// Applied indicates if the transaction was applied to the ledger
	Applied bool

	// Fee is the fee charged (in drops)
	Fee uint64

	// Metadata contains the changes made by the transaction
	Metadata *Metadata

	// Message is a human-readable result message
	Message string
}

// Metadata tracks changes made by a transaction
type Metadata struct {
	// AffectedNodes lists all nodes that were created, modified, or deleted
	AffectedNodes []AffectedNode

	// TransactionIndex is the index in the ledger
	TransactionIndex uint32

	// TransactionResult is the result code
	TransactionResult Result

	// DeliveredAmount is the actual amount delivered (for partial payments)
	DeliveredAmount *Amount

	// ParentBatchID is the hash of the parent batch transaction.
	// Set only for inner transactions within a batch.
	// Reference: rippled TxMeta.h mParentBatchId
	ParentBatchID *[32]byte
}

// AffectedNode is an alias for state.AffectedNode
type AffectedNode = state.AffectedNode

// MarshalJSON implements custom JSON marshaling for Metadata to match rippled format
func (m Metadata) MarshalJSON() ([]byte, error) {
	// Build the output structure matching rippled's format
	output := make(map[string]any)

	// Sort AffectedNodes by LedgerIndex (ascending) to match rippled's ordering
	sortedNodes := make([]AffectedNode, len(m.AffectedNodes))
	copy(sortedNodes, m.AffectedNodes)
	sort.Slice(sortedNodes, func(i, j int) bool {
		return sortedNodes[i].LedgerIndex < sortedNodes[j].LedgerIndex
	})

	// AffectedNodes with nested structure
	affectedNodes := make([]map[string]any, 0, len(sortedNodes))
	for _, node := range sortedNodes {
		nodeJSON, err := affectedNodeToRippledFormat(node)
		if err != nil {
			return nil, err
		}
		affectedNodes = append(affectedNodes, nodeJSON)
	}
	output["AffectedNodes"] = affectedNodes

	// TransactionIndex
	output["TransactionIndex"] = m.TransactionIndex

	// TransactionResult as string
	output["TransactionResult"] = m.TransactionResult.String()

	// delivered_amount (snake_case per rippled format)
	// Use "unavailable" for legacy compatibility when not explicitly set
	if m.DeliveredAmount != nil {
		output["delivered_amount"] = m.DeliveredAmount
	}

	// ParentBatchID for inner batch transactions
	// Reference: rippled TxMeta.cpp getAsObject() lines 257-258
	if m.ParentBatchID != nil {
		output["ParentBatchID"] = strings.ToUpper(hex.EncodeToString(m.ParentBatchID[:]))
	}

	return json.Marshal(output)
}

// toRippledFormat converts an AffectedNode to rippled's nested format
func affectedNodeToRippledFormat(n AffectedNode) (map[string]any, error) {
	// Build the inner node content
	inner := make(map[string]any)

	// FinalFields (for ModifiedNode and DeletedNode)
	if n.FinalFields != nil {
		inner["FinalFields"] = n.FinalFields
	}

	// LedgerEntryType
	inner["LedgerEntryType"] = n.LedgerEntryType

	// LedgerIndex
	inner["LedgerIndex"] = n.LedgerIndex

	// PreviousFields (for ModifiedNode only, omit if nil/empty)
	if n.PreviousFields != nil && len(n.PreviousFields) > 0 {
		inner["PreviousFields"] = n.PreviousFields
	}

	// PreviousTxnID (omit if empty)
	if n.PreviousTxnID != "" {
		inner["PreviousTxnID"] = n.PreviousTxnID
	}

	// PreviousTxnLgrSeq (omit if zero, which means not set)
	if n.PreviousTxnLgrSeq != 0 {
		inner["PreviousTxnLgrSeq"] = n.PreviousTxnLgrSeq
	}

	// NewFields (for CreatedNode only, omit if nil)
	if n.NewFields != nil {
		inner["NewFields"] = n.NewFields
	}

	// Wrap in NodeType (e.g., "ModifiedNode": {...})
	return map[string]any{
		n.NodeType: inner,
	}, nil
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

// Apply processes a transaction and applies it to the ledger.
// Pseudo-transactions (Amendment, SetFee, UNLModify) are rejected here;
// use ApplyPseudo() for pseudo-transaction application (e.g., during block processing).
// Reference: rippled passesLocalChecks() rejects pseudo-transactions submitted by users.
func (e *Engine) Apply(tx Transaction) ApplyResult {
	// Reject pseudo-transactions — they cannot be submitted by users.
	// Reference: rippled passesLocalChecks() in NetworkOPs.cpp
	txType := tx.TxType()
	if txType.IsPseudoTransaction() {
		return ApplyResult{
			Result:  TemINVALID,
			Applied: false,
			Message: "pseudo-transactions cannot be submitted",
		}
	}

	account := tx.GetCommon().Account
	e.logger.Debug("apply",
		"txType", txType.String(),
		"account", account,
		"ledgerSeq", e.config.LedgerSequence,
	)

	// Step 1: Preflight checks (syntax validation)
	result := e.preflight(tx)
	if !result.IsSuccess() {
		e.logger.Debug("preflight failed",
			"txType", txType.String(),
			"account", account,
			"ter", result.String(),
		)
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 2: Compute transaction hash (needed by preclaim for tefALREADY check)
	txHash, err := computeTransactionHash(tx)
	if err != nil {
		return ApplyResult{
			Result:  TefINTERNAL,
			Applied: false,
			Message: "failed to compute transaction hash: " + err.Error(),
		}
	}

	// Step 3: Preclaim checks (validate against ledger state)
	result = e.preclaim(tx, txHash)
	if !result.IsSuccess() && !result.IsTec() {
		e.logger.Debug("preclaim failed",
			"txType", txType.String(),
			"account", account,
			"txHash", hex.EncodeToString(txHash[:]),
			"ter", result.String(),
		)
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// likelyToClaimFee gate: when TapRETRY is set, tec results from
	// preclaim are NOT applied (no fee, no sequence consumed). The tx
	// stays in the retry queue for the next pass where TapRETRY is cleared.
	// Reference: rippled applySteps.h PreclaimResult —
	//   likelyToClaimFee = tesSUCCESS || (isTecClaim && !tapRETRY)
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		return ApplyResult{
			Result:  result,
			Applied: false,
			Message: result.Message(),
		}
	}

	// Step 4: Calculate and apply fee
	fee := e.calculateFee(tx)

	// Step 5: Apply the transaction
	metadata := &Metadata{
		AffectedNodes:     make([]AffectedNode, 0),
		TransactionResult: TesSUCCESS,
	}

	if result.IsSuccess() {
		result = e.doApply(tx, metadata, txHash)
	} else if result.IsTec() {
		// Tec from preclaim: fee must still be deducted and sequence consumed,
		// but doApply() is NOT called — the transaction has no side effects.
		// This mirrors the tec recovery path inside doApply() (lines 1785-2000)
		// but without needing to discard any doApply changes (since doApply never ran).
		// Reference: rippled applySteps.cpp — preclaim tec with likelyToClaimFee=true
		// still enters Transactor::operator() which always applies fee/sequence.
		tecCommon := tx.GetCommon()
		tecAccountID, _ := state.DecodeAccountID(tecCommon.Account)
		tecAccountKey := keylet.Account(tecAccountID)

		tecAccountData, tecReadErr := e.view.Read(tecAccountKey)
		if tecReadErr != nil || tecAccountData == nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to read account",
			}
		}

		tecAccount, tecParseErr := state.ParseAccountRoot(tecAccountData)
		if tecParseErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to parse account",
			}
		}

		tecIsDelegated := tecCommon.Delegate != ""
		tecIsTicket := tecCommon.TicketSequence != nil

		// Deduct fee (unless delegated — delegate pays)
		if !tecIsDelegated {
			tecAccount.Balance -= fee
		}

		// Increment sequence (unless ticket-based)
		if !tecIsTicket && tecCommon.Sequence != nil {
			tecAccount.Sequence = *tecCommon.Sequence + 1
		}

		// Ticket consumption: decrement OwnerCount and TicketCount
		if tecIsTicket && tecAccount.OwnerCount > 0 {
			tecAccount.OwnerCount--
		}
		if tecIsTicket && tecAccount.TicketCount > 0 {
			tecAccount.TicketCount--
		}

		// Thread PreviousTxnID/PreviousTxnLgrSeq
		tecAccount.PreviousTxnID = txHash
		tecAccount.PreviousTxnLgrSeq = e.config.LedgerSequence

		// Update AccountTxnID if tracking is enabled
		{
			var zeroHash [32]byte
			if tecAccount.AccountTxnID != zeroHash {
				tecAccount.AccountTxnID = txHash
			}
		}

		// Create fresh ApplyStateTable for fee-only changes
		tecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

		// Consume ticket through tecTable for proper metadata (DeletedNode + directory changes)
		if tecIsTicket {
			ticketKey := keylet.Ticket(tecAccountID, *tecCommon.TicketSequence)
			ownerDirKey := keylet.OwnerDir(tecAccountID)
			var ticketOwnerNode uint64
			if ticketData, ticketErr := tecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
				ticketOwnerNode = state.GetOwnerNode(ticketData)
			}
			state.DirRemove(tecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
			if err := tecTable.Erase(ticketKey); err != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to erase ticket",
				}
			}
		}

		// Serialize and write updated account to tecTable
		tecUpdatedData, tecSerErr := state.SerializeAccountRoot(tecAccount)
		if tecSerErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to serialize account",
			}
		}
		if err := tecTable.Update(tecAccountKey, tecUpdatedData); err != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to update account",
			}
		}

		// For delegated transactions, deduct fee from delegate's account
		if tecIsDelegated {
			delegateID, _ := state.DecodeAccountID(tecCommon.Delegate)
			delegateAccountKey := keylet.Account(delegateID)
			delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
			if delegateReadErr != nil || delegateAccountData == nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to read delegate account",
				}
			}
			delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
			if delegateParseErr != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to parse delegate account",
				}
			}
			delegateAccount.Balance -= fee
			delegateAccount.PreviousTxnID = txHash
			delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
			if delegateSerErr != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to serialize delegate account",
				}
			}
			if err := tecTable.Update(delegateAccountKey, delegateData); err != nil {
				return ApplyResult{
					Result:  TefINTERNAL,
					Applied: false,
					Message: "tec-from-preclaim: failed to update delegate account",
				}
			}
		}

		// Apply all tracked changes and generate metadata
		generatedMeta, applyErr := tecTable.Apply()
		if applyErr != nil {
			return ApplyResult{
				Result:  TefINTERNAL,
				Applied: false,
				Message: "tec-from-preclaim: failed to apply tecTable",
			}
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes
	}

	metadata.TransactionResult = result

	// Determine if the transaction is applied.
	// In rippled (Transactor.cpp:1108): applied = isTesSuccess(result).
	// For specific tec codes without tapRETRY (tecOVERSIZE, tecKILLED,
	// tecINCOMPLETE, tecEXPIRED, or isTecClaimHardFail), applied is
	// set to true (line 1215). With tapRETRY set, regular tec codes
	// are NOT applied — they return Retry for the next pass.
	// Reference: rippled Transactor.cpp operator() lines 1108-1216
	applied := result.IsApplied()
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		// Retry pass: tec results are NOT applied. The doApply tec
		// recovery already committed fee+sequence to the table, but we
		// DON'T count this as applied so the conformance runner retries.
		// Note: the fee IS consumed (matching rippled where tec from
		// doApply still consumes fee even with tapRETRY, but the tx is
		// returned as Retry, not Success).
		applied = false
	}

	// Record fee as destroyed and assign TransactionIndex
	if applied {
		e.view.AdjustDropsDestroyed(drops.XRPAmount(fee))
		metadata.TransactionIndex = e.txCount
		e.txCount++
	}

	e.logger.Debug("apply result",
		"txHash", hex.EncodeToString(txHash[:]),
		"ter", result.String(),
		"applied", applied,
		"fee", fee,
	)

	return ApplyResult{
		Result:   result,
		Applied:  applied,
		Fee:      fee,
		Metadata: metadata,
		Message:  result.Message(),
	}
}

// ApplyPseudo applies a pseudo-transaction (Amendment, SetFee, UNLModify) to the ledger.
// This is the public entry point for pseudo-transaction application, used by the block
// processor and test environment. Unlike Apply(), this does not reject pseudo-transactions.
// Reference: rippled Change.cpp — pseudo-txs are applied during consensus, not user submission.
func (e *Engine) ApplyPseudo(tx Transaction) ApplyResult {
	return e.applyPseudoTransaction(tx)
}

// applyPseudoTransaction handles pseudo-transactions (Amendment, SetFee, UNLModify).
// These transactions have special handling:
// - No source account (account is zero/empty)
// - No fee (fee is 0)
// - No signature
// - No sequence number checks
// Reference: rippled Change.cpp
func (e *Engine) applyPseudoTransaction(tx Transaction) ApplyResult {
	// Compute transaction hash
	txHash, err := computeTransactionHash(tx)
	if err != nil {
		return ApplyResult{
			Result:  TefINTERNAL,
			Applied: false,
			Message: "failed to compute transaction hash: " + err.Error(),
		}
	}

	// Create metadata
	metadata := &Metadata{
		AffectedNodes:     make([]AffectedNode, 0),
		TransactionResult: TesSUCCESS,
	}

	// Create ApplyStateTable to track changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	// Create a minimal ApplyContext for pseudo-transactions
	ctx := &ApplyContext{
		View:     table,
		Account:  nil, // No account for pseudo-transactions
		Config:   e.config,
		TxHash:   txHash,
		Metadata: metadata,
		Engine:   e,
		Log:      e.logger,
	}

	// Apply the transaction
	var result Result
	if appliable, ok := tx.(Appliable); ok {
		result = appliable.Apply(ctx)
	} else {
		result = TesSUCCESS
	}

	metadata.TransactionResult = result

	// Apply all tracked changes to the base view and generate metadata
	if result.IsSuccess() {
		generatedMeta, err := table.Apply()
		if err != nil {
			return ApplyResult{
				Result:   TefINTERNAL,
				Applied:  false,
				Metadata: metadata,
				Message:  "failed to apply state changes: " + err.Error(),
			}
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes
	}

	// Assign TransactionIndex for applied pseudo-transactions
	if result.IsApplied() {
		metadata.TransactionIndex = e.txCount
		e.txCount++
	}

	return ApplyResult{
		Result:   result,
		Applied:  result.IsApplied(),
		Fee:      0, // Pseudo-transactions have no fee
		Metadata: metadata,
		Message:  result.Message(),
	}
}

// preflight performs initial validation on the transaction
func (e *Engine) preflight(tx Transaction) Result {
	// Validate common fields
	common := tx.GetCommon()

	// Account is required
	if common.Account == "" {
		return TemBAD_SRC_ACCOUNT
	}

	// TransactionType is required
	if common.TransactionType == "" {
		return TemINVALID
	}

	// NetworkID validation (matching rippled's preflight0)
	if result := e.validateNetworkID(common); result != TesSUCCESS {
		return result
	}

	// Amendment check - verify all required amendments are enabled
	// Reference: rippled checks this in each transaction's preflight() method
	for _, featureID := range tx.RequiredAmendments() {
		if !e.rules().Enabled(featureID) {
			return TemDISABLED
		}
	}

	// TicketSequence with disabled TicketBatch feature → temMALFORMED
	// Reference: rippled Transactor.cpp preflight1() line 92
	if common.TicketSequence != nil && !e.rules().Enabled(amendment.FeatureTicketBatch) {
		return TemMALFORMED
	}

	// Delegate field validation
	// Reference: rippled Transactor.cpp preflight1() lines 101-108
	if common.Delegate != "" {
		if !e.rules().Enabled(amendment.FeaturePermissionDelegation) {
			return TemDISABLED
		}
		if common.Delegate == common.Account {
			return TemBAD_SIGNER
		}
	}

	// tfInnerBatchTxn flag validation
	// Reference: rippled Transactor.cpp preflight0() - tfInnerBatchTxn can only be set
	// when processing inner batch transactions, never on directly submitted transactions.
	if common.Flags != nil && *common.Flags&TfInnerBatchTxn != 0 {
		return TemINVALID_FLAG
	}

	// Fee validation
	if result := e.validateFee(common); result != TesSUCCESS {
		return result
	}

	// Sequence must be present (unless using tickets)
	if common.Sequence == nil && common.TicketSequence == nil {
		return TemBAD_SEQUENCE
	}

	// TicketSequence + AccountTxnID is invalid
	// Reference: rippled Transactor.cpp preflight1() line 153
	if common.TicketSequence != nil && common.AccountTxnID != "" {
		return TemINVALID
	}

	// SourceTag validation - if present, it's already a uint32 via JSON parsing
	// No additional validation needed as the type system ensures it's valid

	// Memo validation
	if result := e.validateMemos(common); result != TesSUCCESS {
		return result
	}

	// Multi-sign structural validation: signers must be sorted by binary
	// AccountID, unique, and none may equal the transaction's own Account.
	// These are format checks (not cryptographic) and run regardless of
	// SkipSignatureVerification.
	// Reference: rippled STTx.cpp multiSignHelper() lines 468-485
	if IsMultiSigned(tx) {
		txAccountID, acctErr := state.DecodeAccountID(common.Account)
		if acctErr != nil {
			return TemBAD_SRC_ACCOUNT
		}
		var lastAccountID [20]byte // zero-initialized — less than any real ID
		for _, sw := range common.Signers {
			signerID, decErr := state.DecodeAccountID(sw.Signer.Account)
			if decErr != nil {
				return TemBAD_SIGNATURE
			}
			// The account owner may not multisign for themselves.
			if signerID == txAccountID {
				return TemBAD_SIGNATURE
			}
			// No duplicate signers allowed.
			if signerID == lastAccountID {
				return TemBAD_SIGNATURE
			}
			// Accounts must be in order by binary AccountID.
			if bytes.Compare(lastAccountID[:], signerID[:]) > 0 {
				return TemBAD_SIGNATURE
			}
			lastAccountID = signerID
		}
	}

	// Verify signature (unless skipped for testing)
	if !e.config.SkipSignatureVerification {
		// Check if this is a multi-signed transaction
		if IsMultiSigned(tx) {
			// Multi-signed transactions require signer list lookup
			lookup := &engineSignerListLookup{view: e.view}
			if err := VerifyMultiSignature(tx, lookup); err != nil {
				switch err {
				case ErrNotMultiSigning:
					return TefNOT_MULTI_SIGNING
				case ErrBadQuorum:
					return TefBAD_QUORUM
				case ErrBadSignature:
					return TefBAD_SIGNATURE
				case ErrMasterDisabled:
					return TefMASTER_DISABLED
				case ErrNoSigners:
					return TemBAD_SIGNATURE
				case ErrDuplicateSigner:
					return TemBAD_SIGNATURE
				case ErrSignersNotSorted:
					return TemBAD_SIGNATURE
				default:
					return TefBAD_SIGNATURE
				}
			}
		} else {
			// Single-signed transaction — verify cryptographic signature validity.
			// The signing key authorization (master vs regular key) is checked in preclaim.
			if err := VerifySignature(tx); err != nil {
				return TemBAD_SIGNATURE
			}
		}
	}

	// Transaction-specific validation
	if err := tx.Validate(); err != nil {
		// Try to extract a specific TER code from the error message
		// Many Validate() implementations include the TER code as a prefix (e.g., "temREDUNDANT: message")
		return parseValidationError(err)
	}

	return TesSUCCESS
}

// parseValidationError extracts a TER result code from a validation error message.
// If the error message starts with a valid TER code prefix (e.g., "temREDUNDANT:"),
// it returns the corresponding Result. Otherwise, it returns TemINVALID.
func parseValidationError(err error) Result {
	// Fast path: structured ResultError carries the code directly
	var re *ResultError
	if errors.As(err, &re) {
		return re.Code
	}

	// Legacy fallback: string-prefix matching for unmigrated callers
	msg := err.Error()

	// Check for known TER code prefixes
	// Common tem (malformed) codes
	terCodes := map[string]Result{
		"temMALFORMED":                TemMALFORMED,
		"temBAD_AMOUNT":               TemBAD_AMOUNT,
		"temBAD_CURRENCY":             TemBAD_CURRENCY,
		"temBAD_EXPIRATION":           TemBAD_EXPIRATION,
		"temBAD_FEE":                  TemBAD_FEE,
		"temBAD_ISSUER":               TemBAD_ISSUER,
		"temBAD_LIMIT":                TemBAD_LIMIT,
		"temBAD_OFFER":                TemBAD_OFFER,
		"temBAD_PATH":                 TemBAD_PATH,
		"temBAD_PATH_LOOP":            TemBAD_PATH_LOOP,
		"temBAD_REGKEY":               TemBAD_REGKEY,
		"temBAD_SEQUENCE":             TemBAD_SEQUENCE,
		"temBAD_SIGNATURE":            TemBAD_SIGNATURE,
		"temBAD_SRC_ACCOUNT":          TemBAD_SRC_ACCOUNT,
		"temBAD_TRANSFER_RATE":        TemBAD_TRANSFER_RATE,
		"temDST_IS_SRC":               TemDST_IS_SRC,
		"temDST_NEEDED":               TemDST_NEEDED,
		"temINVALID":                  TemINVALID,
		"temINVALID_FLAG":             TemINVALID_FLAG,
		"temREDUNDANT":                TemREDUNDANT,
		"temRIPPLE_EMPTY":             TemRIPPLE_EMPTY,
		"temDISABLED":                 TemDISABLED,
		"temBAD_SIGNER":               TemBAD_SIGNER,
		"temBAD_QUORUM":               TemBAD_QUORUM,
		"temBAD_WEIGHT":               TemBAD_WEIGHT,
		"temBAD_TICK_SIZE":            TemBAD_TICK_SIZE,
		"temINVALID_ACCOUNT_ID":       TemINVALID_ACCOUNT_ID,
		"temUNCERTAIN":                TemUNCERTAIN,
		"temUNKNOWN":                  TemUNKNOWN,
		"temSEQ_AND_TICKET":           TemSEQ_AND_TICKET,
		"temBAD_SEND_XRP_MAX":         TemBAD_SEND_XRP_MAX,
		"temBAD_SEND_XRP_PARTIAL":     TemBAD_SEND_XRP_PARTIAL,
		"temBAD_SEND_XRP_PATHS":       TemBAD_SEND_XRP_PATHS,
		"temBAD_SEND_XRP_LIMIT":       TemBAD_SEND_XRP_LIMIT,
		"temBAD_SEND_XRP_NO_DIRECT":   TemBAD_SEND_XRP_NO_DIRECT,
		"temCANNOT_PREAUTH_SELF":      TemCAN_NOT_PREAUTH_SELF,
		"temCAN_NOT_PREAUTH_SELF":     TemCAN_NOT_PREAUTH_SELF,
		"temEMPTY_DID":                TemEMPTY_DID,
		"temARRAY_EMPTY":              TemARRAY_EMPTY,
		"temARRAY_TOO_LARGE":          TemARRAY_TOO_LARGE,
		"temBAD_AMM_TOKENS":           TemBAD_AMM_TOKENS,
		"temBAD_TRANSFER_FEE":         TemBAD_TRANSFER_FEE,
		"temBAD_NFTOKEN_TRANSFER_FEE": TemBAD_NFTOKEN_TRANSFER_FEE,
		"temINVALID_COUNT":            TemINVALID_COUNT,
		// tef (failure) codes
		"tefINVALID_LEDGER_FIX_TYPE": TefINVALID_LEDGER_FIX_TYPE,
		// tel (local) codes
		"telBAD_DOMAIN":     TelBAD_DOMAIN,
		"telBAD_PUBLIC_KEY": TelBAD_PUBLIC_KEY,
	}

	// Check if the message starts with any known TER code
	for code, result := range terCodes {
		if len(msg) >= len(code) && msg[:len(code)] == code {
			// Check that it's followed by a colon, space, or is the entire message
			if len(msg) == len(code) || msg[len(code)] == ':' || msg[len(code)] == ' ' {
				return result
			}
		}
	}

	// Default to temINVALID
	return TemINVALID
}

// validateNetworkID validates the NetworkID field according to rippled rules
// - Legacy networks (ID <= 1024) cannot have NetworkID in transactions
// - New networks (ID > 1024) require NetworkID and it must match
func (e *Engine) validateNetworkID(common *Common) Result {
	nodeNetworkID := e.config.NetworkID
	txNetworkID := common.NetworkID

	if nodeNetworkID <= LegacyNetworkIDThreshold {
		// Legacy networks cannot specify NetworkID in transactions
		if txNetworkID != nil {
			return TelNETWORK_ID_MAKES_TX_NON_CANONICAL
		}
	} else {
		// New networks require NetworkID to be present and match
		if txNetworkID == nil {
			return TelREQUIRES_NETWORK_ID
		}
		if *txNetworkID != nodeNetworkID {
			return TelWRONG_NETWORK
		}
	}

	return TesSUCCESS
}

// validateFee validates the Fee field
func (e *Engine) validateFee(common *Common) Result {
	if common.Fee == "" {
		return TesSUCCESS // Fee will be checked later if needed
	}

	// Parse fee as signed int first to detect negative values
	feeInt, err := strconv.ParseInt(common.Fee, 10, 64)
	if err != nil {
		return TemBAD_FEE
	}

	// Fee cannot be negative
	if feeInt < 0 {
		return TemBAD_FEE
	}

	fee := uint64(feeInt)

	// Fee=0 is allowed in preflight — rippled permits it here and checks the
	// minimum fee in preclaim (checkFee). SetRegularKey uses fee=0 for the
	// one-time free "password change". Other tx types that declare fee=0 will
	// be caught later by telINSUF_FEE_P in preclaim.

	// Fee cannot exceed maximum allowed fee
	maxFee := e.config.MaxFee
	if maxFee == 0 {
		maxFee = DefaultMaxFee
	}
	if fee > maxFee {
		return TemBAD_FEE
	}

	return TesSUCCESS
}

// validateMemos validates the Memos array according to rippled rules
func (e *Engine) validateMemos(common *Common) Result {
	if len(common.Memos) == 0 {
		return TesSUCCESS
	}

	// Calculate total serialized size of memos
	totalSize := 0

	for _, memoWrapper := range common.Memos {
		memo := memoWrapper.Memo

		// Validate MemoType if present
		if memo.MemoType != "" {
			// MemoType must be a valid hex string
			memoTypeBytes, err := hex.DecodeString(memo.MemoType)
			if err != nil {
				return TemINVALID
			}
			// MemoType max size is 256 bytes (decoded)
			if len(memoTypeBytes) > MaxMemoTypeSize {
				return TemINVALID
			}
			totalSize += len(memoTypeBytes)

			// MemoType characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoTypeBytes) {
				return TemINVALID
			}
		}

		// Validate MemoData if present
		if memo.MemoData != "" {
			// MemoData must be a valid hex string
			memoDataBytes, err := hex.DecodeString(memo.MemoData)
			if err != nil {
				return TemINVALID
			}
			// MemoData max size is 1024 bytes (decoded)
			if len(memoDataBytes) > MaxMemoDataSize {
				return TemINVALID
			}
			totalSize += len(memoDataBytes)
			// Note: MemoData can contain any data, no character restrictions
		}

		// Validate MemoFormat if present
		if memo.MemoFormat != "" {
			// MemoFormat must be a valid hex string
			memoFormatBytes, err := hex.DecodeString(memo.MemoFormat)
			if err != nil {
				return TemINVALID
			}
			totalSize += len(memoFormatBytes)

			// MemoFormat characters (when decoded) must be valid URL characters per RFC 3986
			if !isValidURLBytes(memoFormatBytes) {
				return TemINVALID
			}
		}
	}

	// Total memo size check
	if totalSize > MaxMemoSize {
		return TemINVALID
	}

	return TesSUCCESS
}

// isValidURLBytes checks if the bytes contain only characters allowed in URLs per RFC 3986
// Allowed: alphanumerics and -._~:/?#[]@!$&'()*+,;=%
func isValidURLBytes(data []byte) bool {
	for _, b := range data {
		if !isURLChar(b) {
			return false
		}
	}
	return true
}

// isURLChar returns true if the byte is a valid URL character per RFC 3986
func isURLChar(c byte) bool {
	// Alphanumerics
	if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
		return true
	}
	// Special characters allowed in URLs: -._~:/?#[]@!$&'()*+,;=%
	switch c {
	case '-', '.', '_', '~', ':', '/', '?', '#', '[', ']', '@', '!', '$', '&', '\'', '(', ')', '*', '+', ',', ';', '=', '%':
		return true
	}
	return false
}

// preclaim validates the transaction against the current ledger state
func (e *Engine) preclaim(tx Transaction, txHash [32]byte) Result {
	common := tx.GetCommon()

	// Check that the source account exists
	accountID, err := state.DecodeAccountID(common.Account)
	if err != nil {
		return TemBAD_SRC_ACCOUNT
	}

	accountKey := keylet.Account(accountID)
	exists, err := e.view.Exists(accountKey)
	if err != nil {
		return TefINTERNAL
	}
	if !exists {
		return TerNO_ACCOUNT
	}

	// Read account data
	accountData, err := e.view.Read(accountKey)
	if err != nil {
		return TefINTERNAL
	}

	// Parse account and check sequence
	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return TefINTERNAL
	}

	// Step 1: checkSeqProxy — sequence/ticket validation
	// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp

	// Check for both Sequence (non-zero) and TicketSequence set → temSEQ_AND_TICKET
	// Reference: rippled Transactor::checkSeqProxy in Transactor.cpp line 375
	if common.Sequence != nil && *common.Sequence != 0 && common.TicketSequence != nil {
		if e.rules().Enabled(amendment.FeatureTicketBatch) {
			return TemSEQ_AND_TICKET
		}
	}

	// Check sequence number or ticket
	if common.TicketSequence != nil {
		// Ticket-based transaction: validate the ticket exists
		if *common.TicketSequence >= account.Sequence {
			// Ticket hasn't been created yet
			return TerPRE_TICKET
		}
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ticketExists, ticketErr := e.view.Exists(ticketKey)
		if ticketErr != nil || !ticketExists {
			return TefNO_TICKET
		}
	} else if common.Sequence != nil {
		if *common.Sequence < account.Sequence {
			return TefPAST_SEQ
		}
		if *common.Sequence > account.Sequence {
			return TerPRE_SEQ
		}
	}

	// Step 2: checkPriorTxAndLastLedger
	// Reference: rippled Transactor::checkPriorTxAndLastLedger in Transactor.cpp

	// AccountTxnID check — if the transaction specifies an AccountTxnID, it must match
	// the account's stored AccountTxnID (the hash of the last tx this account submitted).
	if common.AccountTxnID != "" {
		txAccountTxnID, decErr := hex.DecodeString(common.AccountTxnID)
		if decErr != nil || len(txAccountTxnID) != 32 {
			return TefWRONG_PRIOR
		}
		var txPrior [32]byte
		copy(txPrior[:], txAccountTxnID)
		if txPrior != account.AccountTxnID {
			return TefWRONG_PRIOR
		}
	}

	// LastLedgerSequence check
	if common.LastLedgerSequence != nil {
		if e.config.LedgerSequence > *common.LastLedgerSequence {
			return TefMAX_LEDGER
		}
	}

	// Duplicate transaction detection — if this transaction hash already exists in the
	// view (already applied to this ledger), return tefALREADY.
	// Reference: rippled Transactor::checkPriorTxAndLastLedger — ctx.view.txExists()
	if e.view.TxExists(txHash) {
		return TefALREADY
	}

	// Step 3: checkFee — fee validation and balance check
	// Reference: rippled Transactor::checkFee in Transactor.cpp
	// When a delegate is present, the fee is checked against the delegate's balance.
	fee := e.calculateFee(tx)

	// Calculate the minimum base fee for this transaction type.
	// This is used both for open-ledger fee adequacy (full check) and
	// closed-ledger zero-fee rejection (TxQ minimum).
	// Reference: rippled applySteps.cpp — calculateBaseFee() dispatches to the
	// tx-type-specific override; checkFee() uses that result directly.
	var baseFeeForTx uint64
	if feeCalc, ok := tx.(CustomBaseFeeCalculator); ok {
		baseFeeForTx = feeCalc.CalculateBaseFee(e.view, e.config)
	} else {
		baseFeeForTx = e.config.BaseFee
		if IsMultiSigned(tx) {
			baseFeeForTx = CalculateMultiSigFee(e.config.BaseFee, len(common.Signers))
		}
	}
	// SetRegularKey special case: free password change when lsfPasswordSpent not set.
	// Reference: rippled SetRegularKey.cpp calculateBaseFee
	if tx.TxType() == TypeRegularKeySet {
		signedWithMaster := false
		if spk := common.SigningPubKey; spk != "" {
			sigAddr, sigErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(spk)
			if sigErr == nil && sigAddr == common.Account {
				signedWithMaster = true
			}
		} else if e.config.SkipSignatureVerification && !IsMultiSigned(tx) {
			signedWithMaster = true
		}
		if signedWithMaster && account.Flags&state.LsfPasswordSpent == 0 {
			baseFeeForTx = 0
		}
	}

	// Fee adequacy check: only when the ledger is open.
	// Reference: rippled Transactor::checkFee lines 277-290:
	//   "Only check fee is sufficient when the ledger is open."
	//   When the view is NOT open, fee=0 is accepted (line 292-293).
	if e.config.OpenLedger {
		if fee < baseFeeForTx {
			return TelINSUF_FEE_P
		}
	}

	// When fee is zero, skip batch fee check and balance checks.
	// Reference: rippled Transactor::checkFee line 292-293:
	//   if (feePaid == beast::zero) return tesSUCCESS;
	if fee > 0 {
		if feeCalc, ok := tx.(BatchFeeCalculator); ok {
			batchMinFee := feeCalc.CalculateMinimumFee(e.config.BaseFee)
			if fee < batchMinFee {
				return TelINSUF_FEE_P
			}
		}

		// Determine who pays the fee: delegate (if present) or the source account.
		// Reference: rippled Transactor::checkFee lines 295-297:
		//   auto const id = ctx.tx.isFieldPresent(sfDelegate)
		//       ? ctx.tx.getAccountID(sfDelegate)
		//       : ctx.tx.getAccountID(sfAccount);
		feePayerBalance := account.Balance
		if common.Delegate != "" {
			delegateID, delegateErr := state.DecodeAccountID(common.Delegate)
			if delegateErr != nil {
				return TerNO_ACCOUNT
			}
			delegateAccountKey := keylet.Account(delegateID)
			delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
			if delegateReadErr != nil || delegateAccountData == nil {
				return TerNO_ACCOUNT
			}
			delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
			if delegateParseErr != nil {
				return TefINTERNAL
			}
			feePayerBalance = delegateAccount.Balance
		}

		if feePayerBalance < fee {
			return TerINSUF_FEE_B
		}
	}

	// Step 4: checkPermission — delegation permission check
	// Reference: rippled Transactor::checkPermission in Transactor.cpp lines 213-227
	// and DelegateUtils.cpp checkTxPermission()
	if common.Delegate != "" {
		delegateID, _ := state.DecodeAccountID(common.Delegate)
		delegateKeylet := keylet.DelegateKeylet(accountID, delegateID)
		delegateData, readErr := e.view.Read(delegateKeylet)
		if readErr != nil || delegateData == nil {
			return TecNO_DELEGATE_PERMISSION
		}
		delegateEntry, parseErr := state.ParseDelegate(delegateData)
		if parseErr != nil {
			return TecNO_DELEGATE_PERMISSION
		}
		// Check if the delegate SLE grants permission for this tx type.
		// In rippled: permissionValue == tx.getTxnType() + 1
		txTypeValue := uint32(tx.TxType())
		if !delegateEntry.HasTxPermission(txTypeValue) {
			return TecNO_DELEGATE_PERMISSION
		}
	}

	// Step 5: checkSign — signature verification and multi-sign authorization
	// Reference: rippled Transactor::checkSign in Transactor.cpp
	// When a delegate is present, the idAccount for signature checking is the delegate.
	// Reference: rippled line 602: auto const idAccount = ctx.tx[~sfDelegate].value_or(ctx.tx[sfAccount]);
	if IsMultiSigned(tx) {
		// Multi-signed transaction: always check signer authorization and quorum.
		// This runs regardless of SkipSignatureVerification because quorum and
		// signer authorization (master key disabled, regular key, phantom accounts)
		// are ledger-state checks, not cryptographic checks.
		// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 743-911
		idAccount := common.Account
		if common.Delegate != "" {
			idAccount = common.Delegate
		}
		idAccountID, idErr := state.DecodeAccountID(idAccount)
		if idErr != nil {
			return TefBAD_SIGNATURE
		}
		// Convert tx Signers to SignerInfo for checkBatchMultiSign
		txSigners := make([]SignerInfo, len(common.Signers))
		for i, sw := range common.Signers {
			txSigners[i] = SignerInfo{
				Account:       sw.Signer.Account,
				SigningPubKey: sw.Signer.SigningPubKey,
			}
		}
		if result := e.checkBatchMultiSign(idAccountID, txSigners); result != TesSUCCESS {
			return result
		}
	} else if common.SigningPubKey != "" {
		// Single-signed transaction: check signing key authorization.
		// This runs regardless of SkipSignatureVerification because authorization
		// (master key disabled, regular key) is a ledger-state check, not a
		// cryptographic check. The actual signature verification is done in
		// Validate() and gated by SkipSignatureVerification.
		// Reference: rippled Transactor::checkSingleSign in Transactor.cpp lines 682-740
		signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
		if addrErr != nil {
			return TefBAD_AUTH
		}

		// Determine the idAccount: delegate if present, else source account.
		idAccount := common.Account
		if common.Delegate != "" {
			idAccount = common.Delegate
		}

		// Read the idAccount's data for signature authorization check
		idAccountID, idErr := state.DecodeAccountID(idAccount)
		if idErr != nil {
			return TefBAD_AUTH
		}
		idAccountKey := keylet.Account(idAccountID)
		idAccountData, idReadErr := e.view.Read(idAccountKey)
		if idReadErr != nil || idAccountData == nil {
			return TerNO_ACCOUNT
		}
		idAccountRoot, idParseErr := state.ParseAccountRoot(idAccountData)
		if idParseErr != nil {
			return TefINTERNAL
		}

		isMasterDisabled := (idAccountRoot.Flags & state.LsfDisableMaster) != 0

		if e.rules().Enabled(amendment.FeatureFixMasterKeyAsRegularKey) {
			// With fixMasterKeyAsRegularKey: check regular key first, then master.
			// This allows the master key to serve as a regular key even when
			// master signing is disabled (e.g., regkey(alice, alice) + disable master).
			// Reference: rippled Transactor::checkSingleSign lines 691-713
			if signerAddress == idAccountRoot.RegularKey {
				// Signed with regular key — allowed
			} else if !isMasterDisabled && signerAddress == idAccount {
				// Signed with enabled master key — allowed
			} else if isMasterDisabled && signerAddress == idAccount {
				// Signed with disabled master key
				return TefMASTER_DISABLED
			} else {
				// Signed with an unauthorized key
				return TefBAD_AUTH
			}
		} else {
			// Without fixMasterKeyAsRegularKey: check master key first.
			// If signer == account, it's a master key sign attempt.
			// The regular key is only checked if signer != account.
			// Reference: rippled Transactor::checkSingleSign lines 715-737
			if signerAddress == idAccount {
				// Signing with the master key. Continue if it is not disabled.
				if isMasterDisabled {
					return TefMASTER_DISABLED
				}
			} else if signerAddress == idAccountRoot.RegularKey {
				// Signing with the regular key. Continue.
			} else if idAccountRoot.RegularKey != "" {
				// Signing key does not match master or regular key.
				return TefBAD_AUTH
			} else {
				// No regular key on account and signing key does not match master key.
				return TefBAD_AUTH_MASTER
			}
		}
	}

	// Step 6: checkBatchSign — batch signer authorization
	// Reference: rippled Batch::checkSign -> Transactor::checkBatchSign
	// This checks that each BatchSigner is authorized to act as their account.
	// This runs even when SkipSignatureVerification is true because it checks
	// authorization (account existence, master key, regular key), not crypto.
	if bsp, ok := tx.(BatchSignerProvider); ok {
		if result := e.checkBatchSign(bsp.GetBatchSigners()); result != TesSUCCESS {
			return result
		}
	}

	// Step 7: Transaction-specific preclaim checks.
	// These run after all common preclaim checks and are subject to the
	// TapRETRY gate in Apply(). tec results from preclaim are NOT applied
	// when TapRETRY is set (likelyToClaimFee = false), matching rippled's
	// PreclaimResult semantics.
	// Reference: rippled applySteps.h — invoke_preclaim dispatches to
	// the transaction type's static preclaim() method.
	if preclaimer, ok := tx.(Preclaimer); ok {
		if result := preclaimer.Preclaim(e.config); result != TesSUCCESS {
			return result
		}
	}

	return TesSUCCESS
}

// checkBatchSign verifies that each batch signer is authorized to sign for their account.
// For single-sign signers (SigningPubKey non-empty): derives account from pubkey, checks authorization.
// For multi-sign signers (SigningPubKey empty): checks signer list exists and quorum is met.
// Reference: rippled Transactor::checkBatchSign in Transactor.cpp lines 635-679
func (e *Engine) checkBatchSign(signers []BatchSignerInfo) Result {
	for _, signer := range signers {
		signerAccountID, err := state.DecodeAccountID(signer.Account)
		if err != nil {
			return TefBAD_AUTH
		}

		if signer.SigningPubKey == "" {
			// Multi-sign batch signer: check nested Signers against the account's SignerList.
			// Reference: rippled checkBatchSign -> checkMultiSign
			if result := e.checkBatchMultiSign(signerAccountID, signer.Signers); result != TesSUCCESS {
				return result
			}
			continue
		}

		// Single-sign batch signer: derive account from public key
		signerAddress, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(signer.SigningPubKey)
		if addrErr != nil {
			return TefBAD_AUTH
		}

		signerAccountKey := keylet.Account(signerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)

		if readErr != nil || signerAccountData == nil {
			// Account doesn't exist: only allowed if the signer pubkey derives to this account
			// (phantom account pattern — the signer IS the account)
			if signerAddress != signer.Account {
				return TefBAD_AUTH
			}
			// Phantom account — allowed
			continue
		}

		signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
		if parseErr != nil {
			return TefINTERNAL
		}

		// Check authorization: master key, regular key, or disabled master
		// Reference: rippled Transactor::checkSingleSign
		isMasterDisabled := (signerAccountRoot.Flags & state.LsfDisableMaster) != 0

		if signerAddress == signerAccountRoot.RegularKey {
			// Signed with regular key — allowed
		} else if !isMasterDisabled && signerAddress == signer.Account {
			// Signed with enabled master key — allowed
		} else if isMasterDisabled && signerAddress == signer.Account {
			// Signed with disabled master key
			return TefMASTER_DISABLED
		} else {
			// Signed with an unauthorized key
			return TefBAD_AUTH
		}
	}
	return TesSUCCESS
}

// checkBatchMultiSign verifies a multi-sign batch signer's nested Signers against
// the account's SignerList. This mirrors rippled's checkMultiSign.
// Reference: rippled Transactor::checkMultiSign in Transactor.cpp lines 742-911
func (e *Engine) checkBatchMultiSign(accountID [20]byte, txSigners []SignerInfo) Result {
	signerListKey := keylet.SignerList(accountID)
	signerListData, err := e.view.Read(signerListKey)
	if err != nil || signerListData == nil {
		return TefNOT_MULTI_SIGNING
	}

	signerList, parseErr := state.ParseSignerList(signerListData)
	if parseErr != nil {
		return TefINTERNAL
	}

	// Build a map from r-address to signer entry for O(1) lookup.
	// This avoids ordering issues between binary AccountID sort (rippled/ledger)
	// and r-address string sort (Go's AddMultiSigner).
	authorizedSigners := make(map[string]state.AccountSignerEntry, len(signerList.SignerEntries))
	for _, se := range signerList.SignerEntries {
		authorizedSigners[se.Account] = se
	}

	// Verify each tx signer is authorized and accumulate weights.
	// Reference: rippled checkMultiSign — all signers must be valid.
	var weightSum uint32

	for _, txSigner := range txSigners {
		txSignerAccountID, decErr := state.DecodeAccountID(txSigner.Account)
		if decErr != nil {
			return TefBAD_SIGNATURE
		}

		// Look up the signer in the authorized signers map
		authEntry, found := authorizedSigners[txSigner.Account]
		if !found {
			return TefBAD_SIGNATURE
		}

		// Derive account from the signer's public key
		var signingAcctIDFromPubKey string
		if txSigner.SigningPubKey == "" {
			// In simulation/dry-run mode, empty pubkey maps to the signer account itself
			signingAcctIDFromPubKey = txSigner.Account
		} else {
			addr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(txSigner.SigningPubKey)
			if addrErr != nil {
				return TefBAD_SIGNATURE
			}
			signingAcctIDFromPubKey = addr
		}

		signerAccountKey := keylet.Account(txSignerAccountID)
		signerAccountData, readErr := e.view.Read(signerAccountKey)

		if signingAcctIDFromPubKey == txSigner.Account {
			// Either Phantom or Master key
			if readErr == nil && signerAccountData != nil {
				// Account exists — check master key not disabled
				signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
				if parseErr != nil {
					return TefINTERNAL
				}
				if (signerAccountRoot.Flags & state.LsfDisableMaster) != 0 {
					return TefMASTER_DISABLED
				}
			}
			// Phantom account or master key allowed — continue
		} else {
			// May be a Regular Key
			if readErr != nil || signerAccountData == nil {
				// Non-phantom signer lacks account root
				return TefBAD_SIGNATURE
			}

			signerAccountRoot, parseErr := state.ParseAccountRoot(signerAccountData)
			if parseErr != nil {
				return TefINTERNAL
			}

			if signerAccountRoot.RegularKey == "" {
				// Account lacks RegularKey
				return TefBAD_SIGNATURE
			}

			if signingAcctIDFromPubKey != signerAccountRoot.RegularKey {
				// Wrong RegularKey
				return TefBAD_SIGNATURE
			}
		}

		// Signer is legitimate — add weight
		weightSum += uint32(authEntry.SignerWeight)
	}

	// Check quorum
	if weightSum < signerList.SignerQuorum {
		return TefBAD_QUORUM
	}

	return TesSUCCESS
}

// doApply applies the transaction to the ledger
// For tec results, only fee/sequence changes are applied; transaction effects are discarded.
// Reference: rippled Transactor.cpp - tec results claim fee but don't apply effects
func (e *Engine) doApply(tx Transaction, metadata *Metadata, txHash [32]byte) Result {
	// Store txHash for use by apply functions
	e.currentTxHash = txHash

	// Deduct fee from sender first (this always happens for applied transactions)
	common := tx.GetCommon()
	accountID, _ := state.DecodeAccountID(common.Account)
	accountKey := keylet.Account(accountID)

	// Read sender account directly from view
	accountData, err := e.view.Read(accountKey)
	if err != nil {
		return TefINTERNAL
	}

	account, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return TefINTERNAL
	}

	fee := e.calculateFee(tx)

	// Save original serialized account data for tec recovery.
	// On tec results, we restore the account to its original state
	// and only apply fee deduction + sequence increment.
	// Reference: rippled Transactor.cpp — saves/restores entire SLE on tec.
	originalAccountData := make([]byte, len(accountData))
	copy(originalAccountData, accountData)

	// Deduct fee and handle sequence/ticket
	// Reference: rippled Transactor::payFee + consumeSeqProxy in Transactor.cpp
	isDelegated := common.Delegate != ""
	isTicket := common.TicketSequence != nil

	if isDelegated {
		// Delegated transactions: fee is charged to the delegate account, not the source.
		// The source account's balance is NOT reduced by the fee.
		// Reference: rippled Transactor::payFee() lines 327-337
	} else {
		// Normal transactions: fee is charged to the source account.
		account.Balance -= fee
	}

	if !isTicket && common.Sequence != nil {
		account.Sequence = *common.Sequence + 1
	}

	// Update PreviousTxnID and PreviousTxnLgrSeq (thread the account)
	account.PreviousTxnID = txHash
	account.PreviousTxnLgrSeq = e.config.LedgerSequence

	// Update AccountTxnID if the account has tracking enabled (field is present/non-zero).
	// Reference: rippled Transactor::apply() line 568-569:
	//   if (sle->isFieldPresent(sfAccountTxnID))
	//       sle->setFieldH256(sfAccountTxnID, ctx_.tx.getTransactionID());
	{
		var zeroHash [32]byte
		if account.AccountTxnID != zeroHash {
			account.AccountTxnID = txHash
		}
	}

	// Create ApplyStateTable for transaction-specific changes
	table := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

	// Write the fee-deducted, sequence-incremented account to the table BEFORE Apply().
	// This matches rippled's Transactor::apply() which modifies the account SLE
	// (fee deduction, sequence increment) before calling doApply().
	// Without this, reads during Apply() would see the pre-fee balance.
	{
		preApplyData, preApplyErr := state.SerializeAccountRoot(account)
		if preApplyErr != nil {
			return TefINTERNAL
		}
		if err := table.Update(accountKey, preApplyData); err != nil {
			return TefINTERNAL
		}
	}

	// For delegated transactions, deduct the fee from the delegate's account.
	// Reference: rippled Transactor::payFee() lines 327-337
	if isDelegated {
		delegateID, _ := state.DecodeAccountID(common.Delegate)
		delegateAccountKey := keylet.Account(delegateID)
		delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
		if delegateReadErr != nil || delegateAccountData == nil {
			return TefINTERNAL
		}
		delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
		if delegateParseErr != nil {
			return TefINTERNAL
		}
		delegateAccount.Balance -= fee
		delegateAccount.PreviousTxnID = txHash
		delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
		delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
		if delegateSerErr != nil {
			return TefINTERNAL
		}
		if err := table.Update(delegateAccountKey, delegateData); err != nil {
			return TefINTERNAL
		}
	}

	// Type-specific application - all operations go through the table
	var result Result

	// Determine if the transaction was signed with the master key.
	// Reference: rippled SetAccount.cpp sigWithMaster — compares
	// calcAccountID(SigningPubKey) against the account ID.
	// When signature verification is skipped (test mode), assume master key.
	sigWithMaster := e.config.SkipSignatureVerification
	if common.SigningPubKey != "" {
		signerAddr, addrErr := addresscodec.EncodeClassicAddressFromPublicKeyHex(common.SigningPubKey)
		if addrErr == nil {
			sigWithMaster = signerAddr == common.Account
		}
	}

	// All transaction types implement Appliable
	ctx := &ApplyContext{
		View:             table,
		Account:          account,
		AccountID:        accountID,
		Config:           e.config,
		TxHash:           txHash,
		Metadata:         metadata,
		Engine:           e,
		SignedWithMaster: sigWithMaster,
		Log:              e.logger,
	}

	// Consume ticket BEFORE Apply, matching rippled's Transactor::apply()
	// which calls consumeSeqProxy() before doApply(). This ensures that when
	// doApply() iterates the owner directory (e.g., AccountDelete), the
	// consumed ticket is already gone.
	if isTicket {
		ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
		ownerDirKey := keylet.OwnerDir(accountID)
		var ticketOwnerNode uint64
		if ticketData, ticketErr := table.Read(ticketKey); ticketErr == nil && ticketData != nil {
			ticketOwnerNode = state.GetOwnerNode(ticketData)
		}
		state.DirRemove(table, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
		if err := table.Erase(ticketKey); err != nil {
			return TefINTERNAL
		}
		if account.OwnerCount > 0 {
			account.OwnerCount--
		}
		if account.TicketCount > 0 {
			account.TicketCount--
		}
		preApplyData2, preApplyErr2 := state.SerializeAccountRoot(account)
		if preApplyErr2 != nil {
			return TefINTERNAL
		}
		if err := table.Update(accountKey, preApplyData2); err != nil {
			return TefINTERNAL
		}
	}

	// Set NumberSwitchover based on fixUniversalNumber amendment.
	// When enabled, IOUAmount arithmetic uses Guard-based precision (XRPLNumber).
	// Reference: rippled's setSTNumberSwitchover() in IOUAmount.cpp
	state.SetNumberSwitchover(ctx.Rules().Enabled(amendment.FeatureFixUniversalNumber))

	if appliable, ok := tx.(Appliable); ok {
		result = appliable.Apply(ctx)
	} else {
		result = TesSUCCESS
	}

	// If tx.Apply() returned a non-applied result (tem*/tef*/ter*), discard all changes.
	// This handles transactions like OfferCreate that perform their own preflight/preclaim
	// inside Apply() and may return tem* codes after the engine has already set up the
	// ApplyStateTable. In rippled, these codes are caught before doApply() runs.
	// No fee is charged and no state is modified for non-applied results.
	if !result.IsSuccess() && !result.IsTec() {
		return result
	}

	// Check for oversize metadata: if the transaction touched more than 5200
	// entries, override the result to tecOVERSIZE. This prevents excessively
	// large transactions from being committed.
	// Reference: rippled Transactor.cpp lines 1111-1112:
	//   if (ctx_.size() > oversizeMetaDataCap)
	//       result = tecOVERSIZE;
	const oversizeMetaDataCap = 5200
	if table.Size() > oversizeMetaDataCap {
		result = TecOVERSIZE
	}

	// Ticket was already consumed before Apply (see below). No post-Apply
	// ticket consumption needed for success results.

	// For tec results, only apply fee/sequence changes, not transaction effects.
	// Reference: rippled Transactor.cpp — tec codes claim the fee but discard
	// the apply sandbox, then selectively re-apply specific cleanup operations
	// (offer removal for tecOVERSIZE/tecKILLED, credential deletion for tecEXPIRED).
	//
	// When TapRETRY is set, regular tec results are NOT applied (no fee, no
	// sequence consumed). The tx stays in the retry queue. This matches rippled
	// where applied=isTesSuccess(result)=false with tapRETRY, so ctx_ is never
	// committed. Only isTecClaimHardFail codes (tec without tapRETRY) commit.
	// Reference: rippled Transactor.cpp lines 1108-1216
	if result.IsTec() && (e.config.ApplyFlags&TapRETRY) != 0 {
		// Retry pass: discard all changes, don't commit fee/sequence.
		// The transaction will be retried on the next pass without TapRETRY.
		return result
	}
	if result.IsTec() {
		// For tecOVERSIZE and tecKILLED: collect deleted offers from the table
		// BEFORE discarding, so we can re-remove them from the clean view.
		// Reference: rippled Transactor.cpp lines 1121-1201:
		//   ctx_.visit() collects deleted offer keys, then reset(), then removeUnfundedOffers()
		var removedOfferKeys [][32]byte
		if result == TecOVERSIZE || result == TecKILLED {
			const unfundedOfferRemoveLimit = 1000
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "Offer" {
						removedOfferKeys = append(removedOfferKeys, key)
						if len(removedOfferKeys) >= unfundedOfferRemoveLimit {
							break
						}
					}
				}
			}
		}

		// Collect deleted trust line keys for tecINCOMPLETE (AMMDelete) re-deletion.
		// Reference: rippled Transactor.cpp lines 1139, 1171-1176, 1207-1209:
		//   ctx_.visit() collects deleted RippleState keys, then reset(), then removeDeletedTrustLines()
		var removedTrustLineKeys [][32]byte
		if result == TecINCOMPLETE {
			const maxDeletableAMMTrustLines = 512
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "RippleState" {
						removedTrustLineKeys = append(removedTrustLineKeys, key)
						if len(removedTrustLineKeys) >= maxDeletableAMMTrustLines {
							break
						}
					}
				}
			}
		}

		// Collect expired NFTokenOffer keys for tecEXPIRED re-deletion.
		// Reference: rippled Transactor.cpp lines 1140, 1178-1180, 1203-1205
		var expiredNFTokenOfferKeys [][32]byte
		if result == TecEXPIRED {
			const expiredOfferRemoveLimit = 256
			for key, entry := range table.GetItems() {
				if entry.Action == ActionErase {
					entryType := getLedgerEntryType(entry.Original)
					if entryType == "" && entry.Current != nil {
						entryType = getLedgerEntryType(entry.Current)
					}
					if entryType == "NFTokenOffer" {
						expiredNFTokenOfferKeys = append(expiredNFTokenOfferKeys, key)
						if len(expiredNFTokenOfferKeys) >= expiredOfferRemoveLimit {
							break
						}
					}
				}
			}
		}

		// Discard the transaction table — all doApply() side effects are lost.
		// Reference: rippled Transactor.cpp — reset() discards the sandbox.
		// (We simply don't call table.Apply(), which effectively discards it.)

		// Create a fresh ApplyStateTable to track tec-specific changes
		// (fee, sequence, ticket consumption) for proper metadata generation.
		tecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

		// Consume ticket through tecTable for proper metadata (DeletedNode + directory changes)
		// Reference: rippled Transactor.cpp — tec still consumes the ticket.
		if isTicket {
			ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
			ownerDirKey := keylet.OwnerDir(accountID)
			// Read ticket SLE to get OwnerNode (directory page) for proper removal.
			var ticketOwnerNode uint64
			if ticketData, ticketErr := tecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
				ticketOwnerNode = state.GetOwnerNode(ticketData)
			}
			state.DirRemove(tecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
			if err := tecTable.Erase(ticketKey); err != nil {
				return TefINTERNAL
			}
		}
		// tecINCOMPLETE (AMMDelete): re-delete trust lines that were found during processing.
		// These trust lines were deleted in the (now discarded) sandbox.
		// Reference: rippled Transactor.cpp lines 1207-1209: removeDeletedTrustLines()
		//   which calls deleteAMMTrustLine() for each collected trust line key.
		if len(removedTrustLineKeys) > 0 {
			for _, lineKey := range removedTrustLineKeys {
				lineKL := keylet.Keylet{Key: lineKey}
				lineData, readErr := tecTable.Read(lineKL)
				if readErr != nil || lineData == nil {
					continue
				}
				rs, parseErr := state.ParseRippleState(lineData)
				if parseErr != nil {
					continue
				}
				lowID, decodeErr := state.DecodeAccountID(rs.LowLimit.Issuer)
				if decodeErr != nil {
					continue
				}
				highID, decodeErr := state.DecodeAccountID(rs.HighLimit.Issuer)
				if decodeErr != nil {
					continue
				}
				lowDirKey := keylet.OwnerDir(lowID)
				state.DirRemove(tecTable, lowDirKey, rs.LowNode, lineKey, false)
				highDirKey := keylet.OwnerDir(highID)
				state.DirRemove(tecTable, highDirKey, rs.HighNode, lineKey, false)
				// Erase the trust line
				_ = tecTable.Erase(lineKL)
				// Decrement OwnerCount for the non-AMM side that has a reserve.
				// Reference: rippled View.cpp deleteAMMTrustLine lines 2759-2763
				lowAcctData, _ := tecTable.Read(keylet.Account(lowID))
				highAcctData, _ := tecTable.Read(keylet.Account(highID))
				if lowAcctData != nil && highAcctData != nil {
					lowAcct, _ := state.ParseAccountRoot(lowAcctData)
					highAcct, _ := state.ParseAccountRoot(highAcctData)
					zeroHash := [32]byte{}
					ammLow := lowAcct.AMMID != zeroHash
					ammHigh := highAcct.AMMID != zeroHash
					if rs.Flags&state.LsfLowReserve != 0 && !ammLow {
						adjustOwnerCountOnView(tecTable, lowID, -1, txHash, e.config.LedgerSequence)
					}
					if rs.Flags&state.LsfHighReserve != 0 && !ammHigh {
						adjustOwnerCountOnView(tecTable, highID, -1, txHash, e.config.LedgerSequence)
					}
				}
			}
		}

		// Restore account to original state, then apply only fee/sequence.
		// This discards any changes the transaction made to OwnerCount,
		// MintedNFTokens, BurnedNFTokens, etc.
		// Reference: rippled Transactor.cpp — restores original SLE on tec.
		account, err = state.ParseAccountRoot(originalAccountData)
		if err != nil {
			return TefINTERNAL
		}
		// For delegated transactions, fee is charged to the delegate, not the source.
		// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
		if !isDelegated {
			account.Balance -= fee
		}
		if !isTicket && common.Sequence != nil {
			account.Sequence = *common.Sequence + 1
		}
		// Apply ticket consumption OwnerCount and TicketCount decreases.
		if isTicket && account.OwnerCount > 0 {
			account.OwnerCount--
		}
		if isTicket && account.TicketCount > 0 {
			account.TicketCount--
		}
		// Apply PreviousTxnID/PreviousTxnLgrSeq threading
		account.PreviousTxnID = txHash
		account.PreviousTxnLgrSeq = e.config.LedgerSequence

		// Update AccountTxnID if the account has tracking enabled (field is present/non-zero).
		// On the success path, apply() sets this before doApply(). On the tec path,
		// reset() discards all changes then re-applies fee/sequence. The AccountTxnID
		// must also be updated here so the account tracks the last-applied transaction
		// even when the result is a tec code.
		// Reference: rippled Transactor::apply() lines 568-569.
		{
			var zeroHash [32]byte
			if account.AccountTxnID != zeroHash {
				account.AccountTxnID = txHash
			}
		}

		updatedData, err := state.SerializeAccountRoot(account)
		if err != nil {
			return TefINTERNAL
		}

		// Update account through tecTable for proper metadata diff generation
		if err := tecTable.Update(accountKey, updatedData); err != nil {
			return TefINTERNAL
		}

		// For delegated transactions, deduct the fee from the delegate's account on tec.
		// Reference: rippled Transactor.cpp reset() lines 1011-1013, 1036
		if isDelegated {
			delegateID, _ := state.DecodeAccountID(common.Delegate)
			delegateAccountKey := keylet.Account(delegateID)
			delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
			if delegateReadErr != nil || delegateAccountData == nil {
				return TefINTERNAL
			}
			delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
			if delegateParseErr != nil {
				return TefINTERNAL
			}
			delegateAccount.Balance -= fee
			delegateAccount.PreviousTxnID = txHash
			delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
			if delegateSerErr != nil {
				return TefINTERNAL
			}
			if err := tecTable.Update(delegateAccountKey, delegateData); err != nil {
				return TefINTERNAL
			}
		}

		// tecOVERSIZE/tecKILLED: re-delete offers that were found during processing.
		// These offers were deleted in the (now discarded) sandbox.
		// Reference: rippled Transactor.cpp lines 1198-1201: removeUnfundedOffers()
		if len(removedOfferKeys) > 0 {
			for _, offerKey := range removedOfferKeys {
				offerKL := keylet.Keylet{Key: offerKey}
				offerData, readErr := e.view.Read(offerKL)
				if readErr != nil || offerData == nil {
					continue
				}
				offerObj, parseErr := state.ParseLedgerOffer(offerData)
				if parseErr != nil {
					continue
				}
				ownerID, decodeErr := state.DecodeAccountID(offerObj.Account)
				if decodeErr != nil {
					continue
				}
				ownerDirKey := keylet.OwnerDir(ownerID)
				state.DirRemove(tecTable, ownerDirKey, offerObj.OwnerNode, offerKey, false)
				bookDirKey := keylet.Keylet{Type: 100, Key: offerObj.BookDirectory}
				state.DirRemove(tecTable, bookDirKey, offerObj.BookNode, offerKey, false)
				_ = tecTable.Erase(offerKL)
				adjustOwnerCountOnView(tecTable, ownerID, -1, txHash, e.config.LedgerSequence)
			}
		}

		// tecEXPIRED: re-delete expired NFTokenOffers and credentials.
		// Reference: rippled Transactor.cpp lines 1203-1205: removeExpiredNFTokenOffers()
		if result == TecEXPIRED {
			// Re-delete NFTokenOffers through tecTable
			for _, offerKey := range expiredNFTokenOfferKeys {
				offerKL := keylet.Keylet{Key: offerKey}
				deleteNFTokenOfferOnView(tecTable, offerKL, txHash, e.config.LedgerSequence)
			}

			// Credential deletion via TecApplier
			if tecApplier, ok := tx.(TecApplier); ok {
				tecCtx := &ApplyContext{
					View:      tecTable,
					Account:   account,
					AccountID: accountID,
					Config:    e.config,
					TxHash:    txHash,
					Metadata:  metadata,
					Engine:    e,
					Log:       e.logger,
				}
				tecApplier.ApplyOnTec(tecCtx)
			}
		}

		// Apply all tracked changes and generate proper metadata
		generatedMeta, applyErr := tecTable.Apply()
		if applyErr != nil {
			return TefINTERNAL
		}
		metadata.AffectedNodes = generatedMeta.AffectedNodes

		return result
	}

	// For success, apply all changes through the table
	// Update the source account through the table (unless erased by e.g. AccountDelete)
	if !table.IsErased(accountKey) {
		updatedData, err := state.SerializeAccountRoot(account)
		if err != nil {
			return TefINTERNAL
		}

		if err := table.Update(accountKey, updatedData); err != nil {
			return TefINTERNAL
		}
	}

	// Run invariant checks BEFORE committing — entries are still inspectable in the table.
	// Reference: rippled Transactor::apply() — invariant check runs before ctx_->apply().
	{
		invEntries := table.CollectEntries()
		txDeclaredFee := parseTxDeclaredFee(tx, fee)
		if violation := invariants.CheckInvariants(wrapTxForInvariants(tx), invariants.Result(result), fee, txDeclaredFee, invEntries, table, e.rules()); violation != nil {
			// Invariant violation: discard all doApply() side effects and apply only
			// fee deduction + sequence increment, just like the tec recovery path.
			// Reference: rippled Transactor::apply() lines 1224-1238 — on tecINVARIANT_FAILED,
			// calls reset(fee) which discards the sandbox, then re-applies fee/seq only.
			_ = violation // logged in future via journal

			// Don't call table.Apply() — discard all transaction effects.
			// Create a fresh tecTable for fee-only changes.
			invTecTable := NewApplyStateTable(e.view, txHash, e.config.LedgerSequence, e.rules())

			// Consume ticket through invTecTable if needed.
			if isTicket {
				ticketKey := keylet.Ticket(accountID, *common.TicketSequence)
				ownerDirKey := keylet.OwnerDir(accountID)
				var ticketOwnerNode uint64
				if ticketData, ticketErr := invTecTable.Read(ticketKey); ticketErr == nil && ticketData != nil {
					ticketOwnerNode = state.GetOwnerNode(ticketData)
				}
				state.DirRemove(invTecTable, ownerDirKey, ticketOwnerNode, ticketKey.Key, true)
				if err := invTecTable.Erase(ticketKey); err != nil {
					return TefINTERNAL
				}
			}

			// Restore account to original state, then apply only fee/sequence.
			invAccount, invErr := state.ParseAccountRoot(originalAccountData)
			if invErr != nil {
				return TefINTERNAL
			}
			if !isDelegated {
				invAccount.Balance -= fee
			}
			if !isTicket && common.Sequence != nil {
				invAccount.Sequence = *common.Sequence + 1
			}
			if isTicket && invAccount.OwnerCount > 0 {
				invAccount.OwnerCount--
			}
			if isTicket && invAccount.TicketCount > 0 {
				invAccount.TicketCount--
			}
			invAccount.PreviousTxnID = txHash
			invAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
			{
				var zeroHash [32]byte
				if invAccount.AccountTxnID != zeroHash {
					invAccount.AccountTxnID = txHash
				}
			}

			invUpdatedData, invSerErr := state.SerializeAccountRoot(invAccount)
			if invSerErr != nil {
				return TefINTERNAL
			}
			if err := invTecTable.Update(accountKey, invUpdatedData); err != nil {
				return TefINTERNAL
			}

			// For delegated transactions, deduct the fee from the delegate.
			if isDelegated {
				delegateID, _ := state.DecodeAccountID(common.Delegate)
				delegateAccountKey := keylet.Account(delegateID)
				delegateAccountData, delegateReadErr := e.view.Read(delegateAccountKey)
				if delegateReadErr != nil || delegateAccountData == nil {
					return TefINTERNAL
				}
				delegateAccount, delegateParseErr := state.ParseAccountRoot(delegateAccountData)
				if delegateParseErr != nil {
					return TefINTERNAL
				}
				delegateAccount.Balance -= fee
				delegateAccount.PreviousTxnID = txHash
				delegateAccount.PreviousTxnLgrSeq = e.config.LedgerSequence
				delegateData, delegateSerErr := state.SerializeAccountRoot(delegateAccount)
				if delegateSerErr != nil {
					return TefINTERNAL
				}
				if err := invTecTable.Update(delegateAccountKey, delegateData); err != nil {
					return TefINTERNAL
				}
			}

			// Second invariant check on fee-only state.
			// Reference: rippled Transactor.cpp lines 1234-1238
			// If fee-only state also violates invariants, escalate to tefINVARIANT_FAILED
			// and do NOT apply anything (transaction is completely rejected).
			{
				invEntries2 := invTecTable.CollectEntries()
				if violation2 := invariants.CheckInvariants(wrapTxForInvariants(tx), invariants.Result(TecINVARIANT_FAILED), fee, txDeclaredFee, invEntries2, invTecTable, e.rules()); violation2 != nil {
					return TefINVARIANT_FAILED
				}
			}

			generatedMeta, applyErr := invTecTable.Apply()
			if applyErr != nil {
				return TefINTERNAL
			}
			metadata.AffectedNodes = generatedMeta.AffectedNodes

			return TecINVARIANT_FAILED
		}
	}

	// Apply all tracked changes to the base view and generate metadata automatically
	generatedMeta, err := table.Apply()
	if err != nil {
		return TefINTERNAL
	}

	// Copy generated metadata to the output
	metadata.AffectedNodes = generatedMeta.AffectedNodes

	return result
}

// calculateFee calculates the fee for a transaction
// For multi-signed transactions, the minimum required fee is baseFee * (1 + numSigners)
func (e *Engine) calculateFee(tx Transaction) uint64 {
	common := tx.GetCommon()
	if common.Fee != "" {
		fee, err := strconv.ParseUint(common.Fee, 10, 64)
		if err == nil {
			return fee
		}
	}
	// If no fee specified, use base fee (adjusted for multi-sig if applicable)
	baseFee := e.config.BaseFee
	if IsMultiSigned(tx) {
		numSigners := len(common.Signers)
		return CalculateMultiSigFee(baseFee, numSigners)
	}
	return baseFee
}

// parseTxDeclaredFee extracts the fee declared in the transaction itself.
// This is the fee the user authorized, as opposed to the fee actually charged.
// If the transaction doesn't explicitly set a Fee field (e.g., the test env
// auto-computes it), fallback is returned instead.
// Reference: rippled InvariantCheck.cpp TransactionFeeCheck — tx.getFieldAmount(sfFee).xrp()
func parseTxDeclaredFee(tx Transaction, fallback uint64) uint64 {
	common := tx.GetCommon()
	if common.Fee != "" {
		if fee, err := strconv.ParseUint(common.Fee, 10, 64); err == nil {
			return fee
		}
	}
	// In rippled, sfFee is always present on the transaction. In the Go test env,
	// the fee may be auto-computed by the engine. Use the engine-computed fee as
	// the declared fee in this case, since the engine authorized it on behalf
	// of the test.
	return fallback
}

// AccountReserve calculates the total reserve required for an account with the given owner count.
// This matches rippled's accountReserve(ownerCount) calculation.
// Reserve = ReserveBase + (ownerCount * ReserveIncrement)
func (e *Engine) AccountReserve(ownerCount uint32) uint64 {
	return e.config.ReserveBase + (uint64(ownerCount) * e.config.ReserveIncrement)
}

// ReserveForNewObject calculates the reserve required for creating a new ledger object.
// This matches rippled's logic where the first 2 objects don't require extra reserve.
// Reference: rippled SetTrust.cpp:405-407
//
//	XRPAmount const reserveCreate(
//	    (uOwnerCount < 2) ? XRPAmount(beast::zero)
//	                      : view().fees().accountReserve(uOwnerCount + 1));
func (e *Engine) ReserveForNewObject(currentOwnerCount uint32) uint64 {
	if currentOwnerCount < 2 {
		// First 2 objects are free (no extra reserve needed)
		return 0
	}
	// For 3rd object and beyond, require reserve for (ownerCount + 1) objects
	return e.AccountReserve(currentOwnerCount + 1)
}

// CanCreateNewObject checks if an account has enough balance to create a new ledger object.
// This should be used before creating trust lines, offers, tickets, etc.
// It uses mPriorBalance (balance before fee deduction) to match rippled's behavior.
// Reference: rippled SetTrust.cpp:681,710 - mPriorBalance < reserveCreate
func (e *Engine) CanCreateNewObject(priorBalance uint64, currentOwnerCount uint32) bool {
	reserveNeeded := e.ReserveForNewObject(currentOwnerCount)
	return priorBalance >= reserveNeeded
}

// CheckReserveIncrease validates that an account can afford the reserve increase
// for creating a new ledger object. Returns tecINSUFFICIENT_RESERVE if not enough funds.
func (e *Engine) CheckReserveIncrease(priorBalance uint64, currentOwnerCount uint32) Result {
	if !e.CanCreateNewObject(priorBalance, currentOwnerCount) {
		return TecINSUFFICIENT_RESERVE
	}
	return TesSUCCESS
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
