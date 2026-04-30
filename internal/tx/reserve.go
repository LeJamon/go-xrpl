package tx

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
