package invariants

import "github.com/LeJamon/go-xrpl/amendment"

var (
	CheckValidVault = checkValidVault
	TouchesVault    = touchesVault
)

// ValidVaultVisitingAll is checkValidVault without the no-vault shortcut: the
// per-entry visit always runs.
func ValidVaultVisitingAll(tx Transaction, result Result, fee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	return validVault(tx, result, fee, entries, view, rules, true)
}
