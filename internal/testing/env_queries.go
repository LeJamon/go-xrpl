package testing

import (
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/LeJamon/goXRPLd/keylet"
)

// Balance returns the XRP balance of an account in drops.
func (e *TestEnv) Balance(acc *Account) uint64 {
	e.t.Helper()

	// Get account keylet
	accountKey := keylet.Account(acc.ID)

	// Check if account exists
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return 0
	}
	if !exists {
		return 0 // Account doesn't exist
	}

	// Read account data
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to read account: %v", err)
		return 0
	}

	// Parse account root to get balance
	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		e.t.Fatalf("Failed to parse account data: %v", err)
		return 0
	}

	return accountRoot.Balance
}

// IOUBalance returns the IOU balance of an account for a specific currency and issuer.
// The balance is returned from the perspective of the holder (not the issuer).
// Positive means the holder has tokens, negative means they owe tokens.
func (e *TestEnv) IOUBalance(holder, issuer *Account, currency string) *state.Amount {
	e.t.Helper()

	// Get trust line keylet
	lineKey := keylet.Line(holder.ID, issuer.ID, currency)

	// Check if trust line exists
	exists, err := e.ledger.Exists(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to check trust line existence: %v", err)
		return nil
	}
	if !exists {
		// No trust line = zero balance
		zero := state.NewIssuedAmountFromFloat64(0, currency, issuer.Address)
		return &zero
	}

	// Read trust line data
	data, err := e.ledger.Read(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to read trust line: %v", err)
		return nil
	}

	// Parse RippleState
	rs, err := state.ParseRippleState(data)
	if err != nil {
		e.t.Fatalf("Failed to parse trust line: %v", err)
		return nil
	}

	// Determine if holder is low or high account
	// Balance sign convention: positive means low owes high
	isLow := keylet.IsLowAccount(holder.ID, issuer.ID)

	balance := rs.Balance
	if !isLow {
		// If holder is high account, negate the balance
		// (positive balance means low owes high, so high has positive tokens)
		balance = balance.Negate()
	}

	// RippleState Balance stores zero issuer; override with actual issuer address.
	// This ensures amounts returned here can be used directly in payments.
	balance.Issuer = issuer.Address

	return &balance
}

// BalanceIOU returns the IOU balance of an account for a specific currency and issuer as float64.
// This is a convenience method for tests that need simple numeric comparisons.
func (e *TestEnv) BalanceIOU(holder *Account, currency string, issuer *Account) float64 {
	e.t.Helper()

	balance := e.IOUBalance(holder, issuer, currency)
	if balance == nil {
		return 0.0
	}

	return balance.Float64()
}

// Exists checks if an account exists in the ledger.
func (e *TestEnv) Exists(acc *Account) bool {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return false
	}
	return exists
}

// TrustLineExists checks if a trust line exists between two accounts for a currency.
func (e *TestEnv) TrustLineExists(acc1, acc2 *Account, currency string) bool {
	e.t.Helper()

	lineKey := keylet.Line(acc1.ID, acc2.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to check trust line existence: %v", err)
		return false
	}
	return exists
}

// TrustLineFlags returns the flags on a trust line between two accounts.
// Returns the flags from the perspective of 'account' (which side's flags).
func (e *TestEnv) TrustLineFlags(account, counterparty *Account, currency string) uint32 {
	e.t.Helper()

	lineKey := keylet.Line(account.ID, counterparty.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return 0
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return 0
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return 0
	}

	return rs.Flags
}

// HasNoRipple checks if the account's side of the trust line has NoRipple set.
func (e *TestEnv) HasNoRipple(account, counterparty *Account, currency string) bool {
	e.t.Helper()

	lineKey := keylet.Line(account.ID, counterparty.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return false
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return false
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return false
	}

	// Determine if account is low or high
	isLow := keylet.IsLowAccount(account.ID, counterparty.ID)

	if isLow {
		return (rs.Flags & state.LsfLowNoRipple) != 0
	}
	return (rs.Flags & state.LsfHighNoRipple) != 0
}

// Seq returns the current sequence number for an account.
// It fatals if the account does not exist, matching rippled's Env which throws
// when querying a non-existent account. Use SeqOrDefault for auto-fill paths
// where the account may not exist yet.
func (e *TestEnv) Seq(acc *Account) uint32 {
	e.t.Helper()

	// Get account keylet
	accountKey := keylet.Account(acc.ID)

	// Check if account exists
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return 0
	}
	if !exists {
		e.t.Fatalf("Seq: account %s does not exist", acc.Name)
		return 0 // unreachable
	}

	// Read account data
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to read account: %v", err)
		return 0
	}

	// Parse account root to get sequence
	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		e.t.Fatalf("Failed to parse account data: %v", err)
		return 0
	}

	return accountRoot.Sequence
}

// SeqOrDefault returns the current sequence number for an account, or 1 if
// the account does not exist. This is useful for auto-fill paths where the
// account may not have been created yet (e.g., the Payment that creates it
// is the one being submitted).
func (e *TestEnv) SeqOrDefault(acc *Account) uint32 {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)

	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return 1
	}
	if !exists {
		return 1 // Default sequence for new accounts
	}

	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to read account: %v", err)
		return 1
	}

	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		e.t.Fatalf("Failed to parse account data: %v", err)
		return 1
	}

	return accountRoot.Sequence
}

// Ledger returns the current open ledger.
func (e *TestEnv) Ledger() *ledger.Ledger {
	return e.ledger
}

// LastClosedLedger returns the most recently closed ledger (LCL), the
// parent of the current open ledger. Useful for replay-style tests that
// need to anchor on a real, fully-constructed parent ledger (e.g.,
// post-state derivation tests that replay txs from one closed ledger
// into a verified successor).
func (e *TestEnv) LastClosedLedger() *ledger.Ledger {
	return e.lastClosedLedger
}

// LedgerSeq returns the current ledger sequence number.
func (e *TestEnv) LedgerSeq() uint32 {
	return e.ledger.Sequence()
}

// GetAccount returns a registered account by name.
func (e *TestEnv) GetAccount(name string) *Account {
	return e.accounts[name]
}

// MasterAccount returns the master account for the test environment.
func (e *TestEnv) MasterAccount() *Account {
	return e.accounts["master"]
}

// AccountInfo returns detailed account information.
func (e *TestEnv) AccountInfo(acc *Account) *AccountInfo {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)

	exists, err := e.ledger.Exists(accountKey)
	if err != nil || !exists {
		return nil
	}

	data, err := e.ledger.Read(accountKey)
	if err != nil {
		return nil
	}

	accountRoot, err := state.ParseAccountRootFromBytes(data)
	if err != nil {
		return nil
	}

	// Convert FirstNFTokenSequence from HasFirstNFTSeq/uint32 to *uint32
	var firstNFTSeq *uint32
	if accountRoot.HasFirstNFTSeq {
		v := accountRoot.FirstNFTokenSequence
		firstNFTSeq = &v
	}

	return &AccountInfo{
		Address:              acc.Address,
		Balance:              accountRoot.Balance,
		Sequence:             accountRoot.Sequence,
		OwnerCount:           accountRoot.OwnerCount,
		Flags:                accountRoot.Flags,
		MintedNFTokens:       accountRoot.MintedNFTokens,
		BurnedNFTokens:       accountRoot.BurnedNFTokens,
		FirstNFTokenSequence: firstNFTSeq,
		NFTokenMinter:        accountRoot.NFTokenMinter,
		Domain:               accountRoot.Domain,
		EmailHash:            accountRoot.EmailHash,
		MessageKey:           accountRoot.MessageKey,
		WalletLocator:        accountRoot.WalletLocator,
		AccountTxnID:         accountRoot.AccountTxnID,
		TransferRate:         accountRoot.TransferRate,
		TicketCount:          accountRoot.TicketCount,
	}
}

// AccountInfo contains account information from the ledger.
type AccountInfo struct {
	Address              string
	Balance              uint64
	Sequence             uint32
	OwnerCount           uint32
	Flags                uint32
	MintedNFTokens       uint32
	BurnedNFTokens       uint32
	FirstNFTokenSequence *uint32
	NFTokenMinter        string
	Domain               string
	EmailHash            string
	MessageKey           string
	WalletLocator        string
	AccountTxnID         [32]byte
	TransferRate         uint32
	TicketCount          uint32
}

// MintedCount returns the number of NFTokens minted by this issuer.
// Reference: rippled's mintedCount() test helper.
func (e *TestEnv) MintedCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.MintedNFTokens
}

// BurnedCount returns the number of NFTokens burned for this issuer.
// Reference: rippled's burnedCount() test helper.
func (e *TestEnv) BurnedCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.BurnedNFTokens
}

// BaseFee returns the base fee in drops.
func (e *TestEnv) BaseFee() uint64 {
	return e.baseFee
}

// ReserveBase returns the base reserve in drops.
func (e *TestEnv) ReserveBase() uint64 {
	return e.reserveBase
}

// ReserveIncrement returns the reserve increment in drops.
func (e *TestEnv) ReserveIncrement() uint64 {
	return e.reserveIncrement
}

// TxInLedger returns the number of transactions applied to the current open ledger.
// This is useful for TxQ-related test assertions (e.g., checkMetrics).
func (e *TestEnv) TxInLedger() uint32 {
	return e.txInLedger
}

// EscalatedFee returns the fee (in drops) a transaction should pay to bypass
// the queue and get directly into the current open ledger. This matches
// rippled's auto-fill fee computation in TransactionSign.cpp:
//
//	escalatedFee = toDrops(openLedgerFeeLevel - 1, baseFee) + 1
//
// Reference: rippled getCurrentNetworkFee() in TransactionSign.cpp
func (e *TestEnv) EscalatedFee() uint64 {
	if e.txQueue == nil {
		return e.baseFee
	}
	feeLevel := e.txQueue.GetRequiredFeeLevel(e.txInLedger)
	if uint64(feeLevel) <= txq.BaseLevel {
		return e.baseFee
	}
	// fee = toDrops(feeLevel - 1, baseFee) + 1
	return txq.FeeLevel(uint64(feeLevel)-1).ToDrops(e.baseFee) + 1
}

// OpenLedgerFee returns the fee (in drops) needed to bypass the queue for a
// transaction with the given customBaseFee. This is used for batch transactions
// where the "base fee" is the batch fee (which is higher than the standard base
// fee due to signers and inner transactions).
//
// Reference: rippled Batch_test.cpp openLedgerFee():
//
//	toDrops(metrics.openLedgerFeeLevel, batchFee) + 1
func (e *TestEnv) OpenLedgerFee(customBaseFee uint64) uint64 {
	if e.txQueue == nil {
		return customBaseFee
	}
	feeLevel := e.txQueue.GetRequiredFeeLevel(e.txInLedger)
	if uint64(feeLevel) <= txq.BaseLevel {
		return customBaseFee
	}
	return feeLevel.ToDrops(customBaseFee) + 1
}

// OwnerCount returns the owner count for an account.
// It fatals if the account does not exist, matching rippled's Env which throws
// when querying a non-existent account.
// Reference: rippled's Env::ownerCount(account) in Env.h
func (e *TestEnv) OwnerCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		e.t.Fatalf("OwnerCount: account %s does not exist", acc.Name)
		return 0
	}
	return info.OwnerCount
}

// LedgerEntryExists checks if a ledger entry exists by keylet.
func (e *TestEnv) LedgerEntryExists(key keylet.Keylet) bool {
	e.t.Helper()
	exists, err := e.ledger.Exists(key)
	if err != nil {
		e.t.Fatalf("Failed to check ledger entry existence: %v", err)
		return false
	}
	return exists
}

// LedgerEntry reads a raw ledger entry by keylet.
func (e *TestEnv) LedgerEntry(key keylet.Keylet) ([]byte, error) {
	e.t.Helper()
	return e.ledger.Read(key)
}

// Limit returns the trust line limit for an account/issue.
// Returns 0 if the trust line doesn't exist.
// Reference: rippled's Env::limit(account, issue)
func (e *TestEnv) Limit(holder, issuer *Account, currency string) float64 {
	e.t.Helper()

	lineKey := keylet.Line(holder.ID, issuer.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return 0
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return 0
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return 0
	}

	// Determine which side is the holder's limit
	isLow := keylet.IsLowAccount(holder.ID, issuer.ID)
	if isLow {
		return rs.LowLimit.Float64()
	}
	return rs.HighLimit.Float64()
}

// IncLedgerSeqForAccDel closes enough ledgers so account deletion is allowed.
// rippled requires the account's sequence + 255 to be <= the current ledger sequence.
// Uses addition instead of subtraction to avoid uint32 underflow.
// Reference: rippled's incLgrSeqForAccDel() in acctdelete.h
func (e *TestEnv) IncLedgerSeqForAccDel(acc *Account) {
	e.t.Helper()

	// AccountDelete requires: seq + 255 <= LedgerSeq (i.e., seq + 255 > LedgerSeq fails)
	// Close ledgers until this condition is met.
	for e.Seq(acc)+255 > e.LedgerSeq() {
		e.Close()
	}
}

// GetTxQ returns the test environment's transaction queue, or nil if not configured.
func (e *TestEnv) GetTxQ() *txq.TxQ {
	return e.txQueue
}

// TxQMetrics returns the current TxQ metrics. Panics if TxQ is not configured.
// Reference: rippled TxQ::getMetrics(*env.current())
func (e *TestEnv) TxQMetrics() txq.Metrics {
	if e.txQueue == nil {
		e.t.Fatal("TxQMetrics: TxQ not configured")
	}
	return e.txQueue.GetMetrics(e.txInLedger)
}
