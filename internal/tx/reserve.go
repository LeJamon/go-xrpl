package tx

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

// The reserve calculations live on EngineConfig as the single source of truth.
// Both Engine and ApplyContext expose the same helpers as thin delegations so
// callers on either side share one implementation.

// AccountReserve calculates the total reserve required for an account with the
// given owner count: ReserveBase + (ownerCount * ReserveIncrement). This matches
// rippled's accountReserve(ownerCount).
func (c EngineConfig) AccountReserve(ownerCount uint32) uint64 {
	return c.ReserveBase + (uint64(ownerCount) * c.ReserveIncrement)
}

// ReserveForNewObject calculates the reserve required for creating a new ledger
// object. The first 2 objects don't require extra reserve.
// Reference: rippled SetTrust.cpp:405-407
//
//	XRPAmount const reserveCreate(
//	    (uOwnerCount < 2) ? XRPAmount(beast::zero)
//	                      : view().fees().accountReserve(uOwnerCount + 1));
func (c EngineConfig) ReserveForNewObject(currentOwnerCount uint32) uint64 {
	if currentOwnerCount < 2 {
		return 0
	}
	return c.AccountReserve(currentOwnerCount + 1)
}

// CanCreateNewObject reports whether priorBalance (balance before fee deduction)
// covers the reserve for creating a new ledger object.
// Reference: rippled SetTrust.cpp:681,710 - mPriorBalance < reserveCreate
func (c EngineConfig) CanCreateNewObject(priorBalance uint64, currentOwnerCount uint32) bool {
	return priorBalance >= c.ReserveForNewObject(currentOwnerCount)
}

// CheckReserveIncrease validates that an account can afford the reserve increase
// for a new ledger object, returning TecINSUFFICIENT_RESERVE if not.
func (c EngineConfig) CheckReserveIncrease(priorBalance uint64, currentOwnerCount uint32) ter.Result {
	if !c.CanCreateNewObject(priorBalance, currentOwnerCount) {
		return ter.TecINSUFFICIENT_RESERVE
	}
	return ter.TesSUCCESS
}
