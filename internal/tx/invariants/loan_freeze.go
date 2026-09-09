package invariants

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
)

const tfLoanDefault uint32 = 0x00010000

func loanDefaultFreezeExemptionFor(tx Transaction, view ReadView, rules *amendment.Rules) *loanDefaultFreezeExemption {
	return findLoanDefaultFreezeExemption(tx, view, rules)
}

func (exemption *loanDefaultFreezeExemption) mptHolderExempt(issuance [24]byte, holder [20]byte) bool {
	if exemption == nil || !exemption.hasMPT || exemption.mptID != issuance {
		return false
	}
	account := state.EncodeAccountIDSafe(holder)
	return account == exemption.broker || account == exemption.vault
}
