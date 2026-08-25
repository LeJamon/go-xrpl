package payment

import (
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// applyXRPPayment applies an XRP-to-XRP payment
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp doApply() for XRP direct payments
func (p *Payment) applyXRPPayment(ctx *tx.ApplyContext) ter.Result {
	// Get the amount in drops
	drops := p.Amount.Drops()
	if drops <= 0 {
		return ter.TemBAD_AMOUNT
	}
	amountDrops := uint64(drops)

	// Parse the fee from the transaction
	feeDrops, err := strconv.ParseUint(p.Fee, 10, 64)
	if err != nil {
		feeDrops = ctx.Config.BaseFee // fallback to base fee if not specified
	}

	// PriorBalance is the source account's balance before its own fee was
	// deducted (rippled's mPriorBalance). When a delegate or sponsor pays the
	// fee, the source balance is untouched.
	priorBalance := ctx.PriorBalance()

	accountCountDelta := uint32(0)
	if p.GetFlags()&PaymentFlagSponsorCreatedAccount != 0 {
		accountCountDelta = 1
	}
	reserve, ok := effectiveAccountReserve(ctx.Config, ctx.Account, accountCountDelta)
	if !ok {
		return ter.TecINTERNAL
	}

	// The final spend may dip into the reserve to cover its own fee — but only
	// when the source account is the fee payer. With an external delegate or
	// sponsor, the source need only keep its plain reserve.
	// Reference: rippled Payment.cpp doApply() (fix: decouple reserve from fee).
	minRequired := reserve
	if ctx.SourceFeeCharged != 0 {
		minRequired = max(feeDrops, reserve)
	}

	if priorBalance < amountDrops+minRequired {
		return ter.TecUNFUNDED_PAYMENT
	}

	// Get destination account
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return ter.TemDST_NEEDED
	}
	destKey := keylet.Account(destAccountID)

	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return ter.TefINTERNAL
	}

	if destExists {
		// Destination exists - just credit the amount
		destData, err := ctx.View.Read(destKey)
		if err != nil {
			return ter.TefINTERNAL
		}

		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Check for pseudo-account (AMM/Vault cannot receive direct payments).
		// See rippled Payment.cpp:636-637: if (isPseudoAccount(sleDst)) return tecNO_PERMISSION.
		if destAccount.IsPseudoAccount() {
			return ter.TecNO_PERMISSION
		}

		// Check deposit authorization
		// Reference: rippled Payment.cpp:641-678
		// XRP payments have a wedge-prevention exemption: if BOTH the payment amount
		// AND destination balance are <= base reserve, deposit preauth is NOT
		// checked at all (expired credentials are left untouched too).
		dstReserve := ctx.Config.ReserveBase
		if amountDrops > dstReserve || destAccount.Balance > dstReserve {
			if result := credential.VerifyDepositPreauth(ctx, p.CredentialIDs, ctx.AccountID, destAccountID, destAccount); result != ter.TesSUCCESS {
				return result
			}
		}

		// Credit destination
		destAccount.Balance += amountDrops

		// Clear PasswordSpent flag if set (lsfPasswordSpent = 0x00010000)
		// Per rippled Payment.cpp:686-687, receiving XRP clears this flag
		if (destAccount.Flags & state.LsfPasswordSpent) != 0 {
			destAccount.Flags &^= state.LsfPasswordSpent
		}

		// Debit sender
		ctx.Account.Balance -= amountDrops

		// Update destination
		updatedDestData, err := state.SerializeAccountRoot(destAccount)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Update tracked automatically by ApplyStateTable
		if err := ctx.View.Update(destKey, updatedDestData); err != nil {
			return ter.TefINTERNAL
		}

		return ter.TesSUCCESS
	}

	// Destination doesn't exist - need to create it
	sponsorCreated := p.GetFlags()&PaymentFlagSponsorCreatedAccount != 0

	// A normally-created account must fund its own base reserve. A sponsored
	// account may start with a single drop because the source funds that reserve.
	if amountDrops < ctx.Config.ReserveBase && !sponsorCreated {
		return ter.TecNO_DST_INSUF_XRP
	}

	// Create new account. New accounts start with sequence equal to the current
	// ledger sequence. Reference: rippled Payment.cpp:433 (setFieldU32(sfSequence,
	// view().seq())).
	newAccount := &state.AccountRoot{
		Account:           p.Destination,
		Balance:           amountDrops,
		Sequence:          ctx.Config.LedgerSequence,
		Flags:             0,
		PreviousTxnID:     ctx.TxHash,
		PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
	}
	if sponsorCreated {
		if ctx.Account.SponsoringAccountCount == math.MaxUint32 {
			return ter.TecINTERNAL
		}
		ctx.Account.SponsoringAccountCount++
		newAccount.Sponsor = p.Account
		newAccount.HasSponsor = true
	}

	// Debit sender
	ctx.Account.Balance -= amountDrops

	// Serialize and insert new account
	newAccountData, err := state.SerializeAccountRoot(newAccount)
	if err != nil {
		return ter.TefINTERNAL
	}

	// Insert tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(destKey, newAccountData); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// effectiveAccountReserve mirrors accountReserve(AccountRoot): owner increments
// paid by another sponsor are removed, owner/account units paid by this account
// are added, and a sponsored account does not fund its own base reserve.
func effectiveAccountReserve(config tx.EngineConfig, account *state.AccountRoot, accountDelta uint32) (uint64, bool) {
	if account == nil || account.SponsoredOwnerCount > account.OwnerCount {
		return 0, false
	}
	owners := uint64(account.OwnerCount-account.SponsoredOwnerCount) +
		uint64(account.SponsoringOwnerCount)
	accounts := uint64(account.SponsoringAccountCount) + uint64(accountDelta)
	if !account.HasSponsor {
		accounts++
	}
	if owners > math.MaxUint32 || accounts > math.MaxUint32 {
		return 0, false
	}
	return config.AccountReserveWithCounts(uint32(owners), uint32(accounts)), true
}
