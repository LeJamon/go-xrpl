package vault

import (
	"math"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// AssetPayment is one ordered destination in a lending disbursement.
type AssetPayment struct {
	Account [20]byte
	Amount  state.XRPLNumber
}

// SendAssets sends to the ordered recipients with transfer fees waived. Transit
// credits precede the aggregate sender debit; the caller must discard its
// transaction sandbox on failure.
func SendAssets(ctx *tx.ApplyContext, from [20]byte, asset tx.Asset, payments []AssetPayment) ter.Result {
	if asset.IsNative() {
		return sendXRPAssets(ctx, from, payments)
	}
	if asset.IsMPT() {
		id, ok := assetMPTID(asset)
		if !ok {
			return ter.TefINTERNAL
		}
		return sendMPTAssets(ctx, id, from, payments)
	}
	return sendIOUAssets(ctx, from, asset, payments)
}

func sendXRPAccount(ctx *tx.ApplyContext, id [20]byte) (*state.AccountRoot, error) {
	if id == [20]byte{} {
		return nil, nil
	}
	if id == ctx.AccountID {
		return ctx.Account, nil
	}
	return tx.ReadAccountRoot(ctx.View, id)
}

func writeSendXRPAccount(ctx *tx.ApplyContext, id [20]byte, account *state.AccountRoot) ter.Result {
	if id == ctx.AccountID {
		return ter.TesSUCCESS
	}
	data, err := state.SerializeAccountRoot(account)
	if err != nil || ctx.View.Update(keylet.Account(id), data) != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func sendXRPAssets(ctx *tx.ApplyContext, from [20]byte, payments []AssetPayment) ter.Result {
	sender, err := sendXRPAccount(ctx, from)
	if err != nil {
		return ter.TefINTERNAL
	}
	var debit int64
	for _, payment := range payments {
		amount := ctx.NumberContext().ToAmount(payment.Amount, state.NewXRPAmountFromInt(0), state.RoundToNearest).Drops()
		if amount < 0 {
			return ter.TecINTERNAL
		}
		if amount == 0 || from == payment.Account {
			continue
		}
		receiver, err := sendXRPAccount(ctx, payment.Account)
		if err != nil {
			return ter.TefINTERNAL
		}
		if receiver == nil {
			continue
		}
		if debit > math.MaxInt64-amount {
			return ter.TecINTERNAL
		}
		receiver.Balance += uint64(amount)
		if r := writeSendXRPAccount(ctx, payment.Account, receiver); r != ter.TesSUCCESS {
			return r
		}
		debit += amount
	}
	if sender != nil {
		if sender.Balance < uint64(debit) {
			return ter.TecFAILED_PROCESSING
		}
		sender.Balance -= uint64(debit)
		return writeSendXRPAccount(ctx, from, sender)
	}
	return ter.TesSUCCESS
}

func sendIOUAssets(ctx *tx.ApplyContext, from [20]byte, asset tx.Asset, payments []AssetPayment) ter.Result {
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	debit := state.NewIssuedAmountFromValue(0, 0, asset.Currency, asset.Issuer)
	for _, payment := range payments {
		amount := ctx.NumberContext().ToAmount(payment.Amount, debit, state.RoundToNearest)
		if amount.IsZero() || from == payment.Account {
			continue
		}
		if from == issuer || payment.Account == issuer {
			if r := tx.RippleCreditWithNumberContext(ctx.View, from, payment.Account, amount, ctx.NumberContext()); r != ter.TesSUCCESS {
				return r
			}
			continue
		}
		debit, err = debit.AddWithNumberContext(amount, ctx.NumberContext(), state.RoundToNearest)
		if err != nil {
			return ter.TefINTERNAL
		}
		if r := tx.RippleCreditWithNumberContext(ctx.View, issuer, payment.Account, amount, ctx.NumberContext()); r != ter.TesSUCCESS {
			return r
		}
	}
	if from != issuer && !debit.IsZero() {
		return tx.RippleCreditWithNumberContext(ctx.View, from, issuer, debit, ctx.NumberContext())
	}
	return ter.TesSUCCESS
}

func sendMPTAssets(ctx *tx.ApplyContext, id [24]byte, from [20]byte, payments []AssetPayment) ter.Result {
	issuance, _, r := mptutil.ReadIssuance(ctx.View, id)
	if r != ter.TesSUCCESS {
		return r
	}
	issuer := mptIDIssuer(id)
	maximum := mptutil.MaximumAmount(issuance)
	outstanding := issuance.OutstandingAmount
	var issued uint64
	var actual, debit int64
	for _, payment := range payments {
		amount := payment.Amount.ToInt64WithMode(state.RoundToNearest)
		if amount < 0 {
			return ter.TecINTERNAL
		}
		if amount == 0 || from == payment.Account {
			continue
		}
		if from == issuer || payment.Account == issuer {
			if from == issuer {
				value := uint64(amount)
				if ctx.Rules().Enabled(amendment.FeatureFixCleanup3_1_3) {
					if value > maximum || issued > maximum-value || outstanding > maximum-value-issued {
						return ter.TecPATH_DRY
					}
					issued += value
				} else if value > maximum || outstanding > maximum-value {
					return ter.TecPATH_DRY
				}
			}
			if actual > math.MaxInt64-amount {
				return ter.TecINTERNAL
			}
			if r := sendMPTDirect(ctx, id, from, payment.Account, uint64(amount)); r != ter.TesSUCCESS {
				return r
			}
			actual += amount
			continue
		}
		if actual > math.MaxInt64-amount {
			return ter.TecINTERNAL
		}
		actual += amount
		debit += amount
		if r := sendMPTDirect(ctx, id, issuer, payment.Account, uint64(amount)); r != ter.TesSUCCESS {
			return r
		}
	}
	if from != issuer && debit != 0 {
		return sendMPTDirect(ctx, id, from, issuer, uint64(debit))
	}
	return ter.TesSUCCESS
}

func sendMPTDirect(ctx *tx.ApplyContext, id [24]byte, from, to [20]byte, amount uint64) ter.Result {
	issuance, issuanceKey, r := mptutil.ReadIssuance(ctx.View, id)
	if r != ter.TesSUCCESS {
		return r
	}
	issuer := mptIDIssuer(id)
	if from == issuer {
		if ctx.Rules().MPTokensV2Enabled() &&
			(amount > mptutil.MaximumAmount(issuance) || issuance.OutstandingAmount > math.MaxUint64-amount) {
			return ter.TecPATH_DRY
		}
		issuance.OutstandingAmount += amount
		data, err := state.SerializeMPTokenIssuance(issuance)
		if err != nil || ctx.View.Update(issuanceKey, data) != nil {
			return ter.TefINTERNAL
		}
	} else {
		token, tokenKey, r := mptutil.ReadHolding(ctx.View, id, from)
		if r != ter.TesSUCCESS {
			return r
		}
		if token.MPTAmount < amount {
			return ter.TecINSUFFICIENT_FUNDS
		}
		token.MPTAmount -= amount
		data, err := state.SerializeMPToken(token)
		if err != nil || ctx.View.Update(tokenKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	if to == issuer {
		if issuance.OutstandingAmount < amount {
			return ter.TecINTERNAL
		}
		issuance.OutstandingAmount -= amount
		data, err := state.SerializeMPTokenIssuance(issuance)
		if err != nil || ctx.View.Update(issuanceKey, data) != nil {
			return ter.TefINTERNAL
		}
	} else {
		token, tokenKey, r := mptutil.ReadHolding(ctx.View, id, to)
		if r != ter.TesSUCCESS {
			return r
		}
		if ctx.Rules().MPTokensV2Enabled() && token.MPTAmount > math.MaxUint64-amount {
			return ter.TecINTERNAL
		}
		token.MPTAmount += amount
		data, err := state.SerializeMPToken(token)
		if err != nil || ctx.View.Update(tokenKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	return ter.TesSUCCESS
}
