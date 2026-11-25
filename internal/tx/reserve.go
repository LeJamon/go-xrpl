package tx

import (
	"math"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
)

// The reserve calculations live on EngineConfig as the single source of truth.
// Both Engine and ApplyContext expose the same helpers as thin delegations so
// callers on either side share one implementation.

// AccountReserve calculates the total reserve required for an account with the
// given owner count: ReserveBase + (ownerCount * ReserveIncrement). This matches
// rippled's accountReserve(ownerCount).
func (c EngineConfig) AccountReserve(ownerCount uint32) uint64 {
	return reserveFloor(c.AccountReserveAmount(ownerCount))
}

func reserveFloor(reserve drops.XRPAmount, ok bool) uint64 {
	if !ok {
		return math.MaxUint64
	}
	return uint64(max(reserve, 0))
}

// AccountReserveAmount returns the signed reserve required for ownerCount.
// Modern XRPFees schedules may contain negative reserve fields; callers doing
// protocol comparisons should use this form rather than AccountReserve.
func (c EngineConfig) AccountReserveAmount(ownerCount uint32) (drops.XRPAmount, bool) {
	return checkedFeeReserve(c.Fees(), ownerCount, 1)
}

// AccountReserveWithCounts calculates the reserve for an account that funds
// accountCount base reserves and ownerCount incremental reserves. Sponsor uses
// this form because a sponsored account funds no base reserve of its own while
// a sponsor may fund several accounts.
func (c EngineConfig) AccountReserveWithCounts(ownerCount, accountCount uint32) uint64 {
	return reserveFloor(c.AccountReserveWithCountsAmount(ownerCount, accountCount))
}

// AccountReserveWithCountsAmount is the signed form of
// AccountReserveWithCounts.
func (c EngineConfig) AccountReserveWithCountsAmount(ownerCount, accountCount uint32) (drops.XRPAmount, bool) {
	return checkedFeeReserve(c.Fees(), ownerCount, accountCount)
}

// ReserveForNewObject calculates the reserve required for creating a new ledger
// object. The first 2 objects don't require extra reserve.
// Reference: rippled SetTrust.cpp:405-407
//
//	XRPAmount const reserveCreate(
//	    (uOwnerCount < 2) ? XRPAmount(beast::zero)
//	                      : view().fees().accountReserve(uOwnerCount + 1));
func (c EngineConfig) ReserveForNewObject(currentOwnerCount uint32) uint64 {
	return reserveFloor(c.ReserveForNewObjectAmount(currentOwnerCount))
}

// ReserveForNewObjectAmount is the signed form of ReserveForNewObject.
func (c EngineConfig) ReserveForNewObjectAmount(currentOwnerCount uint32) (drops.XRPAmount, bool) {
	if currentOwnerCount < 2 {
		return 0, true
	}
	return c.AccountReserveAmount(ConfineOwnerCount(currentOwnerCount, 1))
}

// CanCreateNewObject reports whether priorBalance (balance before fee deduction)
// covers the reserve for creating a new ledger object.
// Reference: rippled SetTrust.cpp:681,710 - mPriorBalance < reserveCreate
func (c EngineConfig) CanCreateNewObject(priorBalance uint64, currentOwnerCount uint32) bool {
	reserve, ok := c.ReserveForNewObjectAmount(currentOwnerCount)
	return ok && BalanceAtLeast(priorBalance, reserve)
}

// CheckReserveIncrease validates that an account can afford the reserve increase
// for a new ledger object, returning TecINSUFFICIENT_RESERVE if not.
func (c EngineConfig) CheckReserveIncrease(priorBalance uint64, currentOwnerCount uint32) ter.Result {
	if !c.CanCreateNewObject(priorBalance, currentOwnerCount) {
		return ter.TecINSUFFICIENT_RESERVE
	}
	return ter.TesSUCCESS
}
