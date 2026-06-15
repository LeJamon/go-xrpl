package tx

import (
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// creditHookView is the optional creditHook a view may implement to track
// deferred credits during multi-step payment flow. It mirrors rippled's virtual
// ApplyView::creditHook — a no-op on the base view, overridden by the payment
// sandbox. RippleCredit invokes it only when the view implements it, so
// non-payment callers are unaffected.
type creditHookView interface {
	CreditHook(sender, receiver [20]byte, amount Amount, preCreditSenderBalance Amount)
}

// ownerCountHookView is the optional owner-count hook a view may implement to
// record reserve changes for in-flight payment reserve accounting. It mirrors
// rippled's virtual ApplyView::adjustOwnerCountHook (no-op on the base view).
type ownerCountHookView interface {
	AdjustOwnerCount(account [20]byte, cur, next uint32)
}

// adjustTrustLineOwnerCount adjusts accountID's OwnerCount by delta, first
// firing the view's owner-count hook when present so payment reserve accounting
// stays accurate. Mirrors rippled's adjustOwnerCount, which always calls
// adjustOwnerCountHook before mutating the count.
func adjustTrustLineOwnerCount(view LedgerView, accountID [20]byte, delta int) ter.Result {
	if h, ok := view.(ownerCountHookView); ok {
		if data, err := view.Read(keylet.Account(accountID)); err == nil && data != nil {
			if acct, perr := state.ParseAccountRoot(data); perr == nil {
				h.AdjustOwnerCount(accountID, acct.OwnerCount, confineOwnerCount(acct.OwnerCount, delta))
			}
		}
	}
	if err := AdjustOwnerCount(view, accountID, delta); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

// RippleCredit moves `amount` of an IOU from sender to receiver along their
// trust line — the canonical primitive every credit path shares. When the line
// is absent it is auto-created (NoRipple derived from the receiver's
// DefaultRipple). When it exists, the credit is recorded via the view's
// creditHook (when present) and rippled's deletion tail runs: if the sender's
// holding falls from positive to non-positive on a default, limitless, freeze-
// and quality-free line carrying the sender's reserve, that reserve and owner
// count are released and the emptied line is deleted.
//
// Reference: rippled View.cpp rippleCreditIOU (lines 1635-1782).
func RippleCredit(view LedgerView, sender, receiver [20]byte, amount Amount) ter.Result {
	if amount.IsZero() {
		return ter.TesSUCCESS
	}

	lineKey := keylet.Line(sender, receiver, amount.Currency)
	data, err := view.Read(lineKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if data == nil {
		return rippleCreditCreate(view, sender, receiver, amount, lineKey)
	}

	rs, err := state.ParseRippleState(data)
	if err != nil {
		return ter.TefINTERNAL
	}

	senderIsLow := state.CompareAccountIDs(sender, receiver) < 0

	// Express the balance from the sender's perspective (negate the stored
	// low-perspective balance when the sender is the high account).
	saBefore := rs.Balance
	if !senderIsLow {
		saBefore = saBefore.Negate()
	}

	if h, ok := view.(creditHookView); ok {
		h.CreditHook(sender, receiver, amount, saBefore)
	}

	// Sending lowers the sender's holding: subtract from the stored
	// low-perspective balance when the sender is low, add when it is high.
	if senderIsLow {
		rs.Balance, err = rs.Balance.Sub(amount)
	} else {
		rs.Balance, err = rs.Balance.Add(amount)
	}
	if err != nil {
		return ter.TefINTERNAL
	}

	saAfter := rs.Balance
	if !senderIsLow {
		saAfter = saAfter.Negate()
	}

	bDelete := false
	if saBefore.Signum() > 0 && saAfter.Signum() <= 0 {
		senderReserve, senderNoRipple, senderFreeze := state.LsfHighReserve, state.LsfHighNoRipple, state.LsfHighFreeze
		senderLimit, senderQIn, senderQOut := rs.HighLimit, rs.HighQualityIn, rs.HighQualityOut
		receiverReserve := state.LsfLowReserve
		if senderIsLow {
			senderReserve, senderNoRipple, senderFreeze = state.LsfLowReserve, state.LsfLowNoRipple, state.LsfLowFreeze
			senderLimit, senderQIn, senderQOut = rs.LowLimit, rs.LowQualityIn, rs.LowQualityOut
			receiverReserve = state.LsfHighReserve
		}

		senderDefaultRipple := false
		if sd, rerr := view.Read(keylet.Account(sender)); rerr == nil && sd != nil {
			if sa, perr := state.ParseAccountRoot(sd); perr == nil {
				senderDefaultRipple = sa.Flags&state.LsfDefaultRipple != 0
			}
		}

		if rs.Flags&senderReserve != 0 &&
			(rs.Flags&senderNoRipple != 0) != senderDefaultRipple &&
			rs.Flags&senderFreeze == 0 &&
			senderLimit.IsZero() &&
			senderQIn == 0 && senderQOut == 0 {
			if r := adjustTrustLineOwnerCount(view, sender, -1); r != ter.TesSUCCESS {
				return r
			}
			rs.Flags &^= senderReserve
			bDelete = saAfter.Signum() == 0 && rs.Flags&receiverReserve == 0
		}
	}

	// Persist the post-credit line first so the metadata reflects the final
	// balance and flags even when the line is about to be deleted.
	updated, err := state.SerializeRippleState(rs)
	if err != nil {
		return ter.TefINTERNAL
	}
	if err := view.Update(lineKey, updated); err != nil {
		return ter.TefINTERNAL
	}

	if bDelete {
		lowID, highID := receiver, sender
		if senderIsLow {
			lowID, highID = sender, receiver
		}
		return TrustDelete(view, lineKey, lowID, highID, rs.LowNode, rs.HighNode)
	}

	return ter.TesSUCCESS
}

// rippleCreditCreate is RippleCredit's missing-line branch: it auto-creates the
// trust line carrying the credited balance and bumps the receiver's owner count,
// mirroring rippled's rippleCreditIOU create path.
func rippleCreditCreate(view LedgerView, sender, receiver [20]byte, amount Amount, lineKey keylet.Keylet) ter.Result {
	receiverData, err := view.Read(keylet.Account(receiver))
	if err != nil || receiverData == nil {
		return ter.TefINTERNAL
	}
	receiverAcct, err := state.ParseAccountRoot(receiverData)
	if err != nil {
		return ter.TefINTERNAL
	}
	receiverStr, err := state.EncodeAccountID(receiver)
	if err != nil {
		return ter.TefINTERNAL
	}

	r := TrustCreate(view, TrustCreateParams{
		SrcHigh:     state.CompareAccountIDs(sender, receiver) > 0,
		Src:         sender,
		Dst:         receiver,
		LineKey:     lineKey,
		LimitIssuer: receiver,
		NoRipple:    receiverAcct.Flags&state.LsfDefaultRipple == 0,
		Balance:     state.NewIssuedAmountFromValue(amount.Mantissa(), amount.Exponent(), amount.Currency, state.AccountOneAddress),
		Limit:       NewIssuedAmount(0, state.MinExponent, amount.Currency, receiverStr),
	})
	if r != ter.TesSUCCESS {
		return r
	}

	if r := adjustTrustLineOwnerCount(view, receiver, 1); r != ter.TesSUCCESS {
		return r
	}

	// A line auto-created mid-payment must record its credit in the sandbox's
	// deferred-credit table so the freshly minted balance can't be rippled through
	// twice in the same transaction. The pre-credit balance is zero — the line did
	// not previously exist. Mirrors rippled trustCreate's terminal creditHook.
	if h, ok := view.(creditHookView); ok {
		h.CreditHook(sender, receiver, amount, NewIssuedAmount(0, state.MinExponent, amount.Currency, amount.Issuer))
	}
	return ter.TesSUCCESS
}

// TrustDelete removes a trust line from the low and high owner directories and
// erases it, mirroring rippled's trustDelete (View.cpp lines 1532-1570). Owner
// count adjustments are the caller's responsibility.
func TrustDelete(view LedgerView, lineKey keylet.Keylet, lowID, highID [20]byte, lowNode, highNode uint64) ter.Result {
	lowResult, err := state.DirRemove(view, keylet.OwnerDir(lowID), lowNode, lineKey.Key, false)
	if err != nil || !lowResult.Success {
		return ter.TefBAD_LEDGER
	}
	highResult, err := state.DirRemove(view, keylet.OwnerDir(highID), highNode, lineKey.Key, false)
	if err != nil || !highResult.Success {
		return ter.TefBAD_LEDGER
	}
	if err := view.Erase(lineKey); err != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}
