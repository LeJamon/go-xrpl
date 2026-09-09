package vault

const (
	VaultKindOpenEnded   uint8 = 0
	VaultKindClosedEnded uint8 = 1
)

// VaultPhase is the lifecycle phase of a closed-ended vault at a ledger's
// parent close time.
type VaultPhase uint8

const (
	VaultPhaseNoPhase VaultPhase = iota
	VaultPhaseSubscription
	VaultPhaseInvestment
	VaultPhaseRedemption
)

const (
	minClosedEndedInvestmentPeriod uint64 = 180
	maxClosedEndedInvestmentPeriod uint64 = 946_708_560
	// LoanRedemptionBuffer is the minimum interval between a loan's final
	// scheduled payment and a closed-ended vault's redemption date.
	LoanRedemptionBuffer uint64 = 60
)

// IsValidClosedEndedGap reports whether the redemption date is in the allowed
// half-open interval after the subscription date. The subtraction is widened
// before it is performed so uint32 wraparound cannot make an invalid gap pass.
func IsValidClosedEndedGap(subscriptionDate, redemptionDate uint32) bool {
	if redemptionDate < subscriptionDate {
		return false
	}
	gap := uint64(redemptionDate) - uint64(subscriptionDate)
	return gap >= minClosedEndedInvestmentPeriod && gap < maxClosedEndedInvestmentPeriod
}

// GetVaultPhase derives the phase from persisted lifecycle fields. Legacy and
// open-ended vaults have no phase. Subscription includes its boundary; the
// redemption boundary belongs to Redemption.
func GetVaultPhase(kind uint8, subscriptionDate, redemptionDate *uint32, now uint32) VaultPhase {
	if kind != VaultKindClosedEnded {
		return VaultPhaseNoPhase
	}
	if subscriptionDate == nil || now <= *subscriptionDate {
		return VaultPhaseSubscription
	}
	if redemptionDate == nil || now < *redemptionDate {
		return VaultPhaseInvestment
	}
	return VaultPhaseRedemption
}
