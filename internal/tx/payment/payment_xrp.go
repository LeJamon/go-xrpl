package payment

import (
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
	// deducted (rippled's mPriorBalance). For a delegated payment the fee is
	// charged to the delegate, so the source balance is untouched.
	priorBalance := ctx.PriorBalance()

	// Calculate reserve as: ReserveBase + (ownerCount * ReserveIncrement)
	// This matches rippled's accountReserve(ownerCount) calculation
	reserve := ctx.Config.ReserveBase + (uint64(ctx.Account.OwnerCount) * ctx.Config.ReserveIncrement)

	// The final spend may dip into the reserve to cover its own fee — but only
	// when the source account is the fee payer. In a delegated payment the fee
	// payer is the delegate, so the source need only keep its plain reserve.
	// Reference: rippled Payment.cpp doApply() (fix: decouple reserve from fee).
	minRequired := reserve
	if p.GetCommon().Delegate == "" {
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

		// Update PreviousTxnID and PreviousTxnLgrSeq on destination (thread the account)
		destAccount.PreviousTxnID = ctx.TxHash
		destAccount.PreviousTxnLgrSeq = ctx.Config.LedgerSequence

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
	// Check minimum amount for account creation
	if amountDrops < ctx.Config.ReserveBase {
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
