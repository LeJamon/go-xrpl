package engine

import (
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type feePayerType uint8

const (
	feePayerAccount feePayerType = iota
	feePayerDelegate
	feePayerSponsorCoSigned
	feePayerSponsorPreFunded
)

type feePayer struct {
	keylet  keylet.Keylet
	payerTy feePayerType
}

func isFeeSponsored(common *txcore.Common) bool {
	return common != nil && common.Sponsor != "" && common.SponsorFlags != nil &&
		*common.SponsorFlags&txcore.SpfSponsorFee != 0
}

// getFeePayer mirrors Transactor::getFeePayer. A relationship SLE is preferred
// whenever it exists, even when the transaction also carries SponsorSignature.
func (e *Engine) getFeePayer(common *txcore.Common) (feePayer, ter.Result) {
	if isFeeSponsored(common) {
		sponsorID, err := state.DecodeAccountID(common.Sponsor)
		if err != nil {
			return feePayer{}, ter.TerNO_ACCOUNT
		}
		initiator := common.Account
		if common.Delegate != "" {
			initiator = common.Delegate
		}
		initiatorID, err := state.DecodeAccountID(initiator)
		if err != nil {
			return feePayer{}, ter.TerNO_ACCOUNT
		}
		sponsorshipKey := keylet.Sponsorship(sponsorID, initiatorID)
		exists, err := e.view.Exists(sponsorshipKey)
		if err != nil {
			return feePayer{}, ter.TefINTERNAL
		}
		if exists {
			return feePayer{
				keylet:  sponsorshipKey,
				payerTy: feePayerSponsorPreFunded,
			}, ter.TesSUCCESS
		}
		return feePayer{
			keylet:  keylet.Account(sponsorID),
			payerTy: feePayerSponsorCoSigned,
		}, ter.TesSUCCESS
	}

	if common.Delegate == "" {
		return feePayer{payerTy: feePayerAccount}, ter.TesSUCCESS
	}
	payerID, err := state.DecodeAccountID(common.Delegate)
	if err != nil {
		return feePayer{}, ter.TerNO_ACCOUNT
	}
	return feePayer{
		keylet:  keylet.Account(payerID),
		payerTy: feePayerDelegate,
	}, ter.TesSUCCESS
}

func (e *Engine) accountReserve(account *state.AccountRoot) (uint64, bool) {
	if account == nil || account.SponsoredOwnerCount > account.OwnerCount {
		return 0, false
	}
	owners := uint64(account.OwnerCount-account.SponsoredOwnerCount) +
		uint64(account.SponsoringOwnerCount)
	accounts := uint64(account.SponsoringAccountCount)
	if !account.HasSponsor {
		accounts++
	}
	if owners > math.MaxUint32 || accounts > math.MaxUint32 {
		return 0, false
	}
	return e.config.AccountReserveWithCounts(uint32(owners), uint32(accounts)), true
}

// feePayerBalanceAndSpendable returns the payer's stored balance and the
// maximum amount this transaction may consume. For pre-funded sponsorships the
// stored balance is FeeAmount; for a co-signed sponsor spendable excludes the
// account reserve.
func (e *Engine) feePayerBalanceAndSpendable(payer feePayer, source *state.AccountRoot) (uint64, uint64, ter.Result) {
	if payer.payerTy == feePayerAccount {
		return source.Balance, source.Balance, ter.TesSUCCESS
	}

	data, err := e.view.Read(payer.keylet)
	if err != nil {
		return 0, 0, ter.TefINTERNAL
	}
	if data == nil {
		if payer.payerTy == feePayerSponsorPreFunded {
			return 0, 0, ter.TefINTERNAL
		}
		return 0, 0, ter.TerNO_ACCOUNT
	}

	if payer.payerTy == feePayerSponsorPreFunded {
		sponsorship, err := state.ParseSponsorship(data)
		if err != nil {
			return 0, 0, ter.TefINTERNAL
		}
		balance := uint64(0)
		if sponsorship.HasFeeAmount {
			balance = sponsorship.FeeAmount
		}
		spendable := balance
		if sponsorship.HasMaxFee {
			spendable = min(spendable, sponsorship.MaxFee)
		}
		return balance, spendable, ter.TesSUCCESS
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return 0, 0, ter.TefINTERNAL
	}
	spendable := account.Balance
	if payer.payerTy == feePayerSponsorCoSigned {
		reserve, ok := e.accountReserve(account)
		if !ok {
			return 0, 0, ter.TefINTERNAL
		}
		if spendable > reserve {
			spendable -= reserve
		} else {
			spendable = 0
		}
	}
	return account.Balance, spendable, ter.TesSUCCESS
}

func (st *applyState) hasExternalFeePayer() bool {
	return st.isDelegated || st.feePayer.payerTy != feePayerAccount
}

// payExternalFeeOnTable charges Delegate, SponsorCoSigned, and
// SponsorPreFunded payers. On a normal apply sponsor limits are rechecked
// against the mutable view and insufficient funds reject the transaction. On a
// recovery path the charge is clamped to the authorized spendable amount.
func (e *Engine) payExternalFeeOnTable(
	st *applyState,
	table *applystate.ApplyStateTable,
	recovery bool,
) ter.Result {
	if !st.hasExternalFeePayer() {
		return ter.TesSUCCESS
	}
	balance, spendable, result := e.feePayerBalanceAndSpendable(st.feePayer, st.account)
	if result != ter.TesSUCCESS {
		return result
	}

	fee := st.fee
	if fee > spendable {
		if !recovery &&
			(st.feePayer.payerTy == feePayerSponsorPreFunded ||
				st.feePayer.payerTy == feePayerSponsorCoSigned) {
			if spendable > 0 && !e.config.IsViewOpen() {
				return ter.TecINSUFF_FEE
			}
			return ter.TerINSUF_FEE_B
		}
		fee = spendable
	}
	st.chargedFee = fee
	if fee == 0 {
		return ter.TesSUCCESS
	}

	if st.feePayer.payerTy == feePayerSponsorPreFunded {
		data, err := e.view.Read(st.feePayer.keylet)
		if err != nil || data == nil {
			return ter.TefINTERNAL
		}
		sponsorship, err := state.ParseSponsorship(data)
		if err != nil {
			return ter.TefINTERNAL
		}
		after := balance - fee
		sponsorship.FeeAmount = after
		sponsorship.HasFeeAmount = after != 0
		updated, err := state.SerializeSponsorship(sponsorship)
		if err != nil {
			return ter.TefINTERNAL
		}
		if err := table.Update(st.feePayer.keylet, updated); err != nil {
			return ter.TefINTERNAL
		}
		return ter.TesSUCCESS
	}

	data, err := e.view.Read(st.feePayer.keylet)
	if err != nil || data == nil {
		return ter.TefINTERNAL
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return ter.TefINTERNAL
	}
	account.Balance = balance - fee
	updated, err := state.SerializeAccountRoot(account)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := table.Update(st.feePayer.keylet, updated); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
