package xchain

import (
	"context"
	"math"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

func transferFunds(
	ctx *tx.ApplyContext,
	source, destination string,
	destinationTag *uint32,
	claimOwner string,
	amount tx.Amount,
	canCreateDestination bool,
	claimOwnerBypassesDepositAuth bool,
	allowFeeDip bool,
) ter.Result {
	sandbox := payment.NewPaymentSandbox(ctx.View)
	sandbox.SetTransactionContext(ctx.TxHash, ctx.Config.LedgerSequence)
	sandbox.SetOpenLedger(ctx.Config.IsViewOpen())
	if sourceID, err := state.DecodeAccountID(source); err == nil && sourceID == ctx.AccountID {
		data, err := state.SerializeAccountRoot(ctx.Account)
		if err != nil {
			return ctx.Internal("XChain.transfer.serializeSource", err)
		}
		if err := sandbox.Update(keylet.Account(sourceID), data); err != nil {
			return ctx.Internal("XChain.transfer.stageSource", err)
		}
	}
	result := transferOnView(
		ctx, sandbox, source, destination, destinationTag, claimOwner, amount,
		canCreateDestination, claimOwnerBypassesDepositAuth, allowFeeDip,
	)
	if result != ter.TesSUCCESS {
		return result
	}
	if err := sandbox.ApplyToView(ctx.View); err != nil {
		return ctx.Internal("XChain.transfer.apply", err)
	}
	syncApplySource(ctx)
	return ter.TesSUCCESS
}

func transferOnView(
	ctx *tx.ApplyContext,
	view tx.LedgerView,
	source, destination string,
	destinationTag *uint32,
	claimOwner string,
	amount tx.Amount,
	canCreateDestination bool,
	claimOwnerBypassesDepositAuth bool,
	allowFeeDip bool,
) ter.Result {
	if source == destination {
		return ter.TesSUCCESS
	}
	sourceID, err := state.DecodeAccountID(source)
	if err != nil {
		return ter.TecINTERNAL
	}
	destinationID, err := state.DecodeAccountID(destination)
	if err != nil {
		return ter.TecNO_DST
	}
	destinationAccount, err := state.ReadAccountRoot(view, destinationID)
	if err != nil {
		return ter.TecINTERNAL
	}
	if destinationAccount != nil {
		if destinationAccount.Flags&state.LsfRequireDestTag != 0 && destinationTag == nil {
			return ter.TecDST_TAG_NEEDED
		}
		bypass := claimOwnerBypassesDepositAuth && destination == claimOwner
		if !bypass && destinationAccount.Flags&state.LsfDepositAuth != 0 {
			exists, err := view.Exists(keylet.DepositPreauth(destinationID, sourceID))
			if err != nil {
				return ter.TecINTERNAL
			}
			if !exists {
				return ter.TecNO_PERMISSION
			}
		}
	} else if !amount.IsNative() || !canCreateDestination {
		return ter.TecNO_DST
	}

	if amount.IsNative() {
		return transferXRPOnView(ctx, view, sourceID, destinationID, destination, amount, canCreateDestination, allowFeeDip)
	}

	sourceAccount, err := state.ReadAccountRoot(view, sourceID)
	if err != nil || sourceAccount == nil {
		return ter.TecINTERNAL
	}
	paymentTx := payment.NewPayment(source, destination, amount)
	paymentCtx := *ctx
	paymentCtx.View = view
	paymentCtx.Account = sourceAccount
	paymentCtx.AccountID = sourceID
	paymentCtx.Common = paymentTx.GetCommon()
	paymentCtx.Metadata = &tx.Metadata{}
	paymentCtx.SourceFeeCharged = 0
	if allowFeeDip && sourceID == ctx.AccountID {
		paymentCtx.SourceFeeCharged = ctx.SourceFeeCharged
	}
	if paymentCtx.Ctx == nil {
		paymentCtx.Ctx = context.Background()
	}
	result := paymentTx.ApplyExactIOUFlow(&paymentCtx, sourceID, destinationID)
	if result == ter.TesSUCCESS || result.IsTec() || result.IsTer() {
		return result
	}
	return ter.TecXCHAIN_PAYMENT_FAILED
}

func transferXRPOnView(
	ctx *tx.ApplyContext,
	view tx.LedgerView,
	sourceID, destinationID [20]byte,
	destination string,
	amount tx.Amount,
	canCreateDestination bool,
	allowFeeDip bool,
) ter.Result {
	drops := amount.Drops()
	source, err := state.ReadAccountRoot(view, sourceID)
	if err != nil || source == nil {
		return ter.TecINTERNAL
	}
	reserve, ok := tx.AccountReserveForView(view, ctx.Config, source, source.OwnerCount)
	if !ok {
		return ter.TecINTERNAL
	}
	available := source.Balance
	if allowFeeDip && sourceID == ctx.AccountID {
		available = ctx.PriorBalance()
	}
	if drops >= 0 {
		value := uint64(drops)
		if value > math.MaxUint64-reserve || available < value+reserve {
			return ter.TecUNFUNDED_PAYMENT
		}
	} else if reserve > uint64(-drops) && available < reserve-uint64(-drops) {
		return ter.TecUNFUNDED_PAYMENT
	}

	destinationAccount, err := state.ReadAccountRoot(view, destinationID)
	if err != nil {
		return ter.TecINTERNAL
	}
	sourceAdjusted := false
	if destinationAccount == nil {
		if !canCreateDestination {
			return ter.TecNO_DST
		}
		if drops < 0 || uint64(drops) < ctx.Config.ReserveBase {
			return ter.TecNO_DST_INSUF_XRP
		}
		destinationAccount = &state.AccountRoot{
			Account: destination, Sequence: ctx.Config.LedgerSequence,
			PreviousTxnID: ctx.TxHash, PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
		}
		destinationAccount.Balance = uint64(drops)
		data, err := state.SerializeAccountRoot(destinationAccount)
		if err != nil {
			return ter.TecINTERNAL
		}
		if err := view.Insert(keylet.Account(destinationID), data); err != nil {
			return ter.TecINTERNAL
		}
	} else {
		if result := adjustXRPBalances(source, destinationAccount, drops); result != ter.TesSUCCESS {
			return result
		}
		sourceAdjusted = true
		destinationAccount.PreviousTxnID = ctx.TxHash
		destinationAccount.PreviousTxnLgrSeq = ctx.Config.LedgerSequence
		data, err := state.SerializeAccountRoot(destinationAccount)
		if err != nil {
			return ter.TecINTERNAL
		}
		if err := view.Update(keylet.Account(destinationID), data); err != nil {
			return ter.TecINTERNAL
		}
	}
	if !sourceAdjusted {
		source.Balance -= uint64(drops)
	}
	data, err := state.SerializeAccountRoot(source)
	if err != nil {
		return ter.TecINTERNAL
	}
	if err := view.Update(keylet.Account(sourceID), data); err != nil {
		return ter.TecINTERNAL
	}
	return ter.TesSUCCESS
}

func adjustXRPBalances(source, destination *state.AccountRoot, drops int64) ter.Result {
	if drops >= 0 {
		value := uint64(drops)
		if destination.Balance > math.MaxUint64-value {
			return ter.TecINVARIANT_FAILED
		}
		source.Balance -= value
		destination.Balance += value
		return ter.TesSUCCESS
	}
	value := uint64(-drops)
	if destination.Balance < value || source.Balance > math.MaxUint64-value {
		return ter.TecINVARIANT_FAILED
	}
	source.Balance += value
	destination.Balance -= value
	return ter.TesSUCCESS
}

func syncApplySource(ctx *tx.ApplyContext) {
	data, err := ctx.View.Read(keylet.Account(ctx.AccountID))
	if err != nil || data == nil {
		return
	}
	account, err := state.ParseAccountRoot(data)
	if err == nil {
		*ctx.Account = *account
	}
}
