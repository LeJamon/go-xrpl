package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readLoanBroker reads and parses the LoanBroker entry at brokerKey, returning
// (nil, nil) when absent.
func readLoanBroker(view tx.LedgerView, brokerKey keylet.Keylet) (*loanBrokerData, error) {
	data, err := view.Read(brokerKey)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return parseLoanBroker(data)
}

// readLoan reads and parses the Loan entry at loanKey, returning (nil, nil) when
// absent.
func readLoan(view tx.LedgerView, loanKey keylet.Keylet) (*loanData, error) {
	data, err := view.Read(loanKey)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return parseLoan(data)
}

// createLoanBrokerPseudoAccount derives and inserts the LoanBroker's
// pseudo-account, marked with sfLoanBrokerID (rippled createPseudoAccount). It
// has no key (lsfDisableMaster), ripples by default, and requires deposit auth.
func createLoanBrokerPseudoAccount(ctx *tx.ApplyContext, brokerKey [32]byte) ([20]byte, ter.Result) {
	pseudoID := vault.PseudoAccountAddress(ctx.View, ctx.Config.ParentHash, brokerKey)
	if pseudoID == ([20]byte{}) {
		return pseudoID, ter.TecDUPLICATE
	}
	if exists, _ := ctx.View.Exists(keylet.Account(pseudoID)); exists {
		return pseudoID, ter.TecDUPLICATE
	}
	pseudoAddr, err := state.EncodeAccountID(pseudoID)
	if err != nil {
		return pseudoID, ter.TefINTERNAL
	}
	pseudo := &state.AccountRoot{
		Account:      pseudoAddr,
		Balance:      0,
		Sequence:     0,
		OwnerCount:   0,
		Flags:        state.LsfDisableMaster | state.LsfDefaultRipple | state.LsfDepositAuth,
		LoanBrokerID: brokerKey,
	}
	data, serr := state.SerializeAccountRoot(pseudo)
	if serr != nil {
		return pseudoID, ter.TefINTERNAL
	}
	if ierr := ctx.View.Insert(keylet.Account(pseudoID), data); ierr != nil {
		return pseudoID, ter.TefINTERNAL
	}
	return pseudoID, ter.TesSUCCESS
}

// adjustPseudoOwnerCount applies delta to a pseudo-account's owner count.
func adjustPseudoOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int32) ter.Result {
	if delta == 0 {
		return ter.TesSUCCESS
	}
	ar, err := vault.ReadAccountRoot(ctx.View, accountID)
	if err != nil || ar == nil {
		return ter.TefINTERNAL
	}
	ar.OwnerCount = uint32(int32(ar.OwnerCount) + delta)
	data, serr := state.SerializeAccountRoot(ar)
	if serr != nil {
		return ter.TefINTERNAL
	}
	if uerr := ctx.View.Update(keylet.Account(accountID), data); uerr != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// representableAsAsset reports whether n survives a round-trip through the asset's
// STAmount precision (rippled STAmount{asset, value} == value). Used for the
// tecPRECISION_LOSS guards, relevant to integral (XRP/MPT) assets.
func representableAsAsset(n lmath.N, asset tx.Asset) bool {
	return n.RoundToAsset(assetIntegral(asset)).Equal(n)
}
