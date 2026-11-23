package invariants

import "github.com/LeJamon/go-xrpl/amendment"

// CheckValidVault reports how many ledger images the checker decoded.
func CheckValidVault(tx Transaction, result Result, fee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules) (*InvariantViolation, int) {
	entries = withImageCaches(entries)
	violation := checkValidVault(tx, result, fee, entries, view, rules)
	var decoded int
	for _, e := range entries {
		for _, image := range e.images {
			if image.model.done {
				decoded++
			}
		}
	}
	return violation, decoded
}

// ValidVaultVisitingAll is checkValidVault without the no-vault shortcut: the
// per-entry visit always runs.
func ValidVaultVisitingAll(tx Transaction, result Result, fee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	return validVault(tx, result, fee, entries, view, rules, true)
}
