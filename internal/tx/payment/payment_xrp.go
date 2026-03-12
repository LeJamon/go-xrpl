package payment

import (
	"strconv"

	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/keylet"
)

// applyXRPPayment applies an XRP-to-XRP payment
// Reference: rippled/src/xrpld/app/tx/detail/Payment.cpp doApply() for XRP direct payments
func (p *Payment) applyXRPPayment(ctx *tx.ApplyContext) tx.Result {
	// Get the amount in drops
	drops := p.Amount.Drops()
	if drops <= 0 {
		return tx.TemBAD_AMOUNT
	}
	amountDrops := uint64(drops)

	// Parse the fee from the transaction
	feeDrops, err := strconv.ParseUint(p.Fee, 10, 64)
	if err != nil {
		feeDrops = ctx.Config.BaseFee // fallback to base fee if not specified
	}

	// IMPORTANT: sender.Balance has already had fee deducted (in doApply).
	// Rippled checks against mPriorBalance (balance BEFORE fee deduction).
	// We reconstruct the pre-fee balance for the check.
	// Reference: rippled Payment.cpp:619 - if (mPriorBalance < dstAmount.xrp() + mmm)
	priorBalance := ctx.Account.Balance + feeDrops

	// Calculate reserve as: ReserveBase + (ownerCount * ReserveIncrement)
	// This matches rippled's accountReserve(ownerCount) calculation
	reserve := ctx.Config.ReserveBase + (uint64(ctx.Account.OwnerCount) * ctx.Config.ReserveIncrement)

	// Use max(reserve, fee) as the minimum balance that must remain
	// This matches rippled's behavior: auto const mmm = std::max(reserve, ctx_.tx.getFieldAmount(sfFee).xrp())
	// Reference: rippled Payment.cpp:617
	mmm := reserve
	if feeDrops > mmm {
		mmm = feeDrops
	}

	// Check sender has enough balance using PRE-FEE balance
	// Reference: rippled Payment.cpp:619 - if (mPriorBalance < dstAmount.xrp() + mmm)
	if priorBalance < amountDrops+mmm {
		return tx.TecUNFUNDED_PAYMENT
	}

	// Get destination account
	destAccountID, err := state.DecodeAccountID(p.Destination)
	if err != nil {
		return tx.TemDST_NEEDED
	}
	destKey := keylet.Account(destAccountID)

	destExists, err := ctx.View.Exists(destKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	if destExists {
		// Destination exists - just credit the amount
		destData, err := ctx.View.Read(destKey)
		if err != nil {
			return tx.TefINTERNAL
		}

		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check for pseudo-account (AMM accounts cannot receive direct payments)
		// See rippled Payment.cpp:636-637: if (isPseudoAccount(sleDst)) return tecNO_PERMISSION
		if (destAccount.Flags & state.LsfAMM) != 0 {
			return tx.TecNO_PERMISSION
		}

		// Check destination's lsfDisallowXRP flag
		// Per rippled, if lsfDisallowXRP is set and sender != destination, return tecNO_TARGET
		// This allows accounts to indicate they don't want to receive XRP
		// Reference: this matches rippled behavior for direct XRP payments
		if (destAccount.Flags & state.LsfDisallowXRP) != 0 {
			senderAccountID, err := state.DecodeAccountID(ctx.Account.Account)
			if err != nil {
				return tx.TefINTERNAL
			}
			// Only reject if sender is not the destination (self-payments are allowed)
			if senderAccountID != destAccountID {
				return tx.TecNO_TARGET
			}
		}

		// Check if destination requires a tag
		if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
			return tx.TecDST_TAG_NEEDED
		}

		// Validate credentials (preclaim)
		if result := p.validateCredentials(ctx); result != tx.TesSUCCESS {
			return result
		}

		// Check deposit authorization
		// Reference: rippled Payment.cpp:641-677
		// XRP payments have a wedge-prevention exemption: if BOTH the payment amount
		// AND destination balance are <= base reserve, deposit preauth is NOT required.
		if (destAccount.Flags & state.LsfDepositAuth) != 0 {
			dstReserve := ctx.Config.ReserveBase

			if amountDrops > dstReserve || destAccount.Balance > dstReserve {
				if result := p.verifyDepositPreauth(ctx, ctx.AccountID, destAccountID, destAccount); result != tx.TesSUCCESS {
					return result
				}
			}
		} else if len(p.CredentialIDs) > 0 {
			// Even without lsfDepositAuth, remove expired credentials if present
			if p.removeExpiredCredentials(ctx) {
				return tx.TecEXPIRED
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
			return tx.TefINTERNAL
		}

		// Update tracked automatically by ApplyStateTable
		if err := ctx.View.Update(destKey, updatedDestData); err != nil {
			return tx.TefINTERNAL
		}

		return tx.TesSUCCESS
	}

	// Destination doesn't exist - need to create it
	// Check minimum amount for account creation
	if amountDrops < ctx.Config.ReserveBase {
		return tx.TecNO_DST_INSUF_XRP
	}

	// Create new account
	// With featureDeletableAccounts enabled, new accounts start with sequence
	// equal to the current ledger sequence. Otherwise, sequence starts at 1.
	// (see rippled Payment.cpp:409-411)
	var accountSequence uint32
	if ctx.Rules().DeletableAccountsEnabled() {
		accountSequence = ctx.Config.LedgerSequence
	} else {
		accountSequence = 1
	}
	newAccount := &state.AccountRoot{
		Account:           p.Destination,
		Balance:           amountDrops,
		Sequence:          accountSequence,
		Flags:             0,
		PreviousTxnID:     ctx.TxHash,
		PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
	}

	// Debit sender
	ctx.Account.Balance -= amountDrops

	// Serialize and insert new account
	newAccountData, err := state.SerializeAccountRoot(newAccount)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert tracked automatically by ApplyStateTable
	if err := ctx.View.Insert(destKey, newAccountData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
