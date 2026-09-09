package invariants

import "github.com/LeJamon/go-xrpl/amendment"

const tfLoanDefault uint32 = 0x00010000

func loanDefaultFreezeExemptionFor(tx Transaction, view ReadView, rules *amendment.Rules) *loanDefaultFreezeExemption {
	return findLoanDefaultFreezeExemption(tx, view, rules)
}
