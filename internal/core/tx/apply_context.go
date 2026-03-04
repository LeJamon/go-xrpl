package tx

import (
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

// ApplyContext provides all the state and helpers needed to apply a transaction.
// It is passed to Appliable.Apply() instead of individual parameters.
type ApplyContext struct {
	// View provides read/write access to ledger state (the ApplyStateTable)
	View LedgerView

	// Account is the source account (mutable, will be written back by the engine)
	Account *sle.AccountRoot

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
