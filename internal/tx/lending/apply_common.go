package lending

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending/lmath"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// readLoanBroker reads and parses the LoanBroker entry at brokerKey, returning
// (nil, nil) when absent.
func readLoanBroker(view tx.ReadOnlyLedgerView, brokerKey keylet.Keylet) (*loanBrokerData, error) {
	data, err := view.Read(brokerKey)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return parseLoanBroker(data)
}

// readLoan reads and parses the Loan entry at loanKey, returning (nil, nil) when
// absent.
func readLoan(view tx.ReadOnlyLedgerView, loanKey keylet.Keylet) (*loanData, error) {
	data, err := view.Read(loanKey)
	if err != nil || len(data) == 0 {
		return nil, err
	}
	return parseLoan(data)
}

// createLoanBrokerPseudoAccount derives and inserts the LoanBroker's
// pseudo-account, marked with sfLoanBrokerID.
func createLoanBrokerPseudoAccount(ctx *tx.ApplyContext, brokerKey [32]byte) ([20]byte, ter.Result) {
	id, _, res := tx.CreatePseudoAccount(ctx, brokerKey, tx.PseudoLoanBrokerID)
	return id, res
}

// adjustPseudoOwnerCount applies delta to a pseudo-account's owner count.
func adjustPseudoOwnerCount(ctx *tx.ApplyContext, accountID [20]byte, delta int32) ter.Result {
	if delta == 0 {
		return ter.TesSUCCESS
	}
	ar, err := tx.ReadAccountRoot(ctx.View, accountID)
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

func checkAssetValue(n lmath.N, asset tx.Asset) ter.Result {
	rounded := n.RoundToAsset(assetIntegral(asset))
	if asset.IsNative() {
		maximum := lmath.FromInt(int64(state.MaxNativeDrops))
		if rounded.Cmp(maximum) > 0 || rounded.Cmp(maximum.Negate()) < 0 {
			return ter.TefEXCEPTION
		}
	}
	if !rounded.Equal(n) {
		return ter.TecPRECISION_LOSS
	}
	return ter.TesSUCCESS
}
