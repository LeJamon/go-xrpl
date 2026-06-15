package engine

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// The reserve calculations live on EngineConfig as the single source of truth.
// Engine exposes the same helpers as thin delegations so callers on either side
// share one implementation.

// AccountReserve calculates the total reserve required for an account with the
// given owner count.
func (e *Engine) AccountReserve(ownerCount uint32) uint64 {
	return e.config.AccountReserve(ownerCount)
}

// ReserveForNewObject calculates the reserve required for creating a new ledger
// object (the first 2 objects are free).
func (e *Engine) ReserveForNewObject(currentOwnerCount uint32) uint64 {
	return e.config.ReserveForNewObject(currentOwnerCount)
}

// CanCreateNewObject reports whether the prior balance covers the reserve for a
// new ledger object.
func (e *Engine) CanCreateNewObject(priorBalance uint64, currentOwnerCount uint32) bool {
	return e.config.CanCreateNewObject(priorBalance, currentOwnerCount)
}

// CheckReserveIncrease validates that an account can afford the reserve increase
// for a new ledger object. Returns TecINSUFFICIENT_RESERVE if not enough funds.
func (e *Engine) CheckReserveIncrease(priorBalance uint64, currentOwnerCount uint32) ter.Result {
	return e.config.CheckReserveIncrease(priorBalance, currentOwnerCount)
}
