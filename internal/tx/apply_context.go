package tx

import (
	"strconv"

	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	xrpllog "github.com/LeJamon/goXRPLd/log"
)

// ApplyContext provides all the state and helpers needed to apply a transaction.
// It is passed to Appliable.Apply() instead of individual parameters.
type ApplyContext struct {
	// View provides read/write access to ledger state (the ApplyStateTable)
	View LedgerView

	// Account is the source account (mutable, will be written back by the engine)
	Account *state.AccountRoot

	// AccountID is the decoded source account ID
	AccountID [20]byte

	// Config holds engine configuration (reserves, ledger sequence, etc.)
	Config EngineConfig

	// TxHash is the hash of the current transaction
	TxHash [32]byte

	// Metadata allows transactions to set DeliveredAmount (used by Payment)
	Metadata *Metadata

	// Engine provides access to shared helper methods (dirInsert, dirRemove, etc.)
	Engine *Engine

	// SignedWithMaster is true when the transaction was signed with the account's master key.
	// Reference: rippled SetAccount.cpp sigWithMaster — derived from SigningPubKey.
	SignedWithMaster bool

	// Log is the logger scoped to the Tx partition for this transaction.
	// Transaction handlers (Apply methods) use this for Trace/Debug logging.
	// Always non-nil — falls back to xrpllog.Discard() when not set by the engine.
	Log xrpllog.Logger
}

// AccountReserve calculates the total reserve required for an account with the given owner count.
// Reserve = ReserveBase + (ownerCount * ReserveIncrement)
func (ctx *ApplyContext) AccountReserve(ownerCount uint32) uint64 {
	return ctx.Config.ReserveBase + (uint64(ownerCount) * ctx.Config.ReserveIncrement)
}

// ReserveForNewObject calculates the reserve required for creating a new ledger object.
// The first 2 objects don't require extra reserve.
func (ctx *ApplyContext) ReserveForNewObject(currentOwnerCount uint32) uint64 {
	if currentOwnerCount < 2 {
		return 0
	}
	return ctx.AccountReserve(currentOwnerCount + 1)
}

// CanCreateNewObject checks if an account has enough balance to create a new ledger object.
func (ctx *ApplyContext) CanCreateNewObject(priorBalance uint64, currentOwnerCount uint32) bool {
	return priorBalance >= ctx.ReserveForNewObject(currentOwnerCount)
}

// CheckReserveIncrease validates that an account can afford the reserve increase
// for creating a new ledger object. Returns TecINSUFFICIENT_RESERVE if not enough funds.
func (ctx *ApplyContext) CheckReserveIncrease(priorBalance uint64, currentOwnerCount uint32) Result {
	if !ctx.CanCreateNewObject(priorBalance, currentOwnerCount) {
		return TecINSUFFICIENT_RESERVE
	}
	return TesSUCCESS
}

// Rules returns the amendment rules, defaulting to all amendments enabled if nil.
func (ctx *ApplyContext) Rules() *amendment.Rules {
	if ctx.Config.Rules != nil {
		return ctx.Config.Rules
	}
	return amendment.AllSupportedRules()
}

// LookupAccount loads and parses an AccountRoot by account address string.
// Returns the parsed AccountRoot, decoded account ID, and TesSUCCESS on success.
// On failure returns nil, zero ID, and the appropriate TER code:
//   - TemINVALID if the address cannot be decoded
//   - TecNO_DST if the account does not exist
//   - TefINTERNAL if the account data cannot be parsed
func (ctx *ApplyContext) LookupAccount(account string) (*state.AccountRoot, [20]byte, Result) {
	var zeroID [20]byte
	accountID, err := state.DecodeAccountID(account)
	if err != nil {
		return nil, zeroID, TemINVALID
	}

	accountKey := keylet.Account(accountID)
	accountData, err := ctx.View.Read(accountKey)
	if err != nil || accountData == nil {
		return nil, zeroID, TecNO_DST
	}

	accountRoot, err := state.ParseAccountRoot(accountData)
	if err != nil {
		return nil, zeroID, TefINTERNAL
	}

	return accountRoot, accountID, TesSUCCESS
}

// LookupDestination loads and parses a destination account.
// In addition to LookupAccount checks, it also rejects pseudo-accounts (LsfAMM).
// Reference: rippled's common preclaim pattern for destination accounts.
func (ctx *ApplyContext) LookupDestination(account string) (*state.AccountRoot, [20]byte, Result) {
	dest, destID, result := ctx.LookupAccount(account)
	if result != TesSUCCESS {
		return nil, destID, result
	}

	// Pseudo-accounts (AMM) cannot be destinations
	if (dest.Flags & state.LsfAMM) != 0 {
		return nil, destID, TecNO_PERMISSION
	}

	return dest, destID, TesSUCCESS
}

// PriorBalance returns the sender's balance before fee deduction.
// Equivalent to rippled's mPriorBalance = account.Balance + fee.
func (ctx *ApplyContext) PriorBalance(fee string) uint64 {
	return ctx.Account.Balance + parseFeeDrops(fee)
}

// CheckReserveWithFee validates that the sender can afford the reserve
// for the given owner count using prior balance (before fee deduction).
// This is the "prior balance vs reserve" pattern used in 12+ Apply() methods.
// Returns TecINSUFFICIENT_RESERVE if the prior balance is below the reserve.
func (ctx *ApplyContext) CheckReserveWithFee(ownerCountAfter uint32, fee string) Result {
	priorBalance := ctx.PriorBalance(fee)
	reserve := ctx.AccountReserve(ownerCountAfter)
	if priorBalance < reserve {
		return TecINSUFFICIENT_RESERVE
	}
	return TesSUCCESS
}

// UpdateAccountRoot serializes an AccountRoot and writes it back to the ledger view.
// Encapsulates the serialize + view.Update pattern repeated across Apply() methods.
// Returns TefINTERNAL on serialization or update failure, TesSUCCESS otherwise.
func (ctx *ApplyContext) UpdateAccountRoot(accountID [20]byte, account *state.AccountRoot) Result {
	data, err := state.SerializeAccountRoot(account)
	if err != nil {
		return TefINTERNAL
	}
	accountKey := keylet.Account(accountID)
	if err := ctx.View.Update(accountKey, data); err != nil {
		return TefINTERNAL
	}
	return TesSUCCESS
}

// parseFeeDrops parses a fee string (in drops) to uint64.
// Returns 0 if the fee is empty or invalid.
func parseFeeDrops(fee string) uint64 {
	if fee == "" {
		return 0
	}
	v, err := strconv.ParseUint(fee, 10, 64)
	if err != nil {
		return 0
	}
	return v
}
