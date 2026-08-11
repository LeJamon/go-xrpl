package trustset

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// TrustSet creates or modifies a trust line between two accounts.
type TrustSet struct {
	tx.BaseTx

	// LimitAmount defines the trust line (required)
	// The issuer field is the account to trust
	LimitAmount tx.Amount `json:"LimitAmount" xrpl:"LimitAmount,amount"`

	// QualityIn is the quality in (1e9 = 1:1) - optional
	QualityIn *uint32 `json:"QualityIn,omitempty" xrpl:"QualityIn,omitempty"`

	// QualityOut is the quality out (1e9 = 1:1) - optional
	QualityOut *uint32 `json:"QualityOut,omitempty" xrpl:"QualityOut,omitempty"`
}

// TrustSet transaction flags
// Reference: rippled SetTrust.cpp
const (
	// tfSetfAuth authorizes the other party to hold currency
	TrustSetFlagSetfAuth uint32 = 0x00010000
	// tfSetNoRipple blocks rippling on this trust line
	TrustSetFlagSetNoRipple uint32 = 0x00020000
	// tfClearNoRipple clears the no ripple flag
	TrustSetFlagClearNoRipple uint32 = 0x00040000
	// tfSetFreeze freezes the trust line
	TrustSetFlagSetFreeze uint32 = 0x00100000
	// tfClearFreeze clears the freeze flag
	TrustSetFlagClearFreeze uint32 = 0x00200000
	// tfSetDeepFreeze deep freezes the trust line (requires featureDeepFreeze)
	TrustSetFlagSetDeepFreeze uint32 = 0x00400000
	// tfClearDeepFreeze clears the deep freeze flag
	TrustSetFlagClearDeepFreeze uint32 = 0x00800000

	// tfTrustSetMask is the mask for valid TrustSet transaction flags.
	// Reference: rippled TxFlags.h tfTrustSetMask — carves out tfUniversal
	// (tfFullyCanonicalSig | tfInnerBatchTxn) so inner Batch txs aren't rejected.
	TrustSetFlagMask uint32 = ^(TrustSetFlagSetfAuth |
		TrustSetFlagSetNoRipple |
		TrustSetFlagClearNoRipple |
		TrustSetFlagSetFreeze |
		TrustSetFlagClearFreeze |
		TrustSetFlagSetDeepFreeze |
		TrustSetFlagClearDeepFreeze |
		tx.TfUniversal)
)

// NewTrustSet creates a new TrustSet transaction
func NewTrustSet(account string, limitAmount tx.Amount) *TrustSet {
	return &TrustSet{
		BaseTx:      *tx.NewBaseTx(tx.TypeTrustSet, account),
		LimitAmount: limitAmount,
	}
}

func (t *TrustSet) TxType() tx.Type {
	return tx.TypeTrustSet
}

// cMaxNativeN is the largest legal native (XRP) mantissa: the total XRP supply
// in drops. A native LimitAmount whose magnitude exceeds it is temBAD_AMOUNT.
// Reference: rippled STAmount::cMaxNativeN / isLegalNet.
const cMaxNativeN int64 = 100_000_000_000_000_000

// Validate runs the rules-independent structural checks of rippled's
// SetTrust::preflight body. The flag mask lives in GetFlagsMask (preflight0),
// the amendment-gated deep-freeze rejection in PreflightRules, and the
// self-issuer check is a ledger-stage preclaim check (see Apply) — not preflight.
// Reference: rippled SetTrust.cpp preflight().
func (t *TrustSet) Validate() error {
	if err := t.BaseTx.Validate(); err != nil {
		return err
	}

	// isLegalNet: a native limit whose magnitude exceeds the total XRP supply is
	// temBAD_AMOUNT, checked before the native -> temBAD_LIMIT rejection.
	if t.LimitAmount.IsNative() {
		mag := t.LimitAmount.Drops()
		if mag < 0 {
			mag = -mag
		}
		if mag > cMaxNativeN {
			return ter.Errorf(ter.TemBAD_AMOUNT, "limit amount exceeds maximum")
		}
	}

	// LimitAmount must be an issued currency, not XRP
	if t.LimitAmount.IsNative() {
		return ter.Errorf(ter.TemBAD_LIMIT, "cannot create trust line for XRP")
	}

	if t.LimitAmount.Currency == "" {
		return ter.Errorf(ter.TemBAD_CURRENCY, "currency is required")
	}

	// Check for XRP currency code
	if t.LimitAmount.Currency == "XRP" {
		return ter.Errorf(ter.TemBAD_CURRENCY, "cannot use XRP as IOU currency")
	}

	// Negative limit is not allowed
	if t.LimitAmount.IsNegative() {
		return ter.Errorf(ter.TemBAD_LIMIT, "negative credit limit")
	}

	// Check if destination makes sense
	// In rippled, preflight checks: if (!issuer || issuer == noAccount())
	// noAccount() is ACCOUNT_ONE = rrrrrrrrrrrrrrrrrrrrBZbvji
	if t.LimitAmount.Issuer == "" || t.LimitAmount.Issuer == "rrrrrrrrrrrrrrrrrrrrBZbvji" {
		return ter.Errorf(ter.TemDST_NEEDED, "issuer is required")
	}

	return nil
}

// GetFlagsMask reports the invalid-flag mask (rippled SetTrust::getFlagsMask =
// tfTrustSetMask). The deep-freeze bits are valid in this mask; their amendment
// gating is the separate preflight-body check in PreflightRules, so the mask is
// unconditional. The engine rejects flags intersecting it at preflight0.
func (t *TrustSet) GetFlagsMask(rules *amendment.Rules) uint32 {
	return TrustSetFlagMask
}

// PreflightRules runs the amendment-gated deep-freeze rejection at rippled's
// position: the first statement of SetTrust::preflight's body, after preflight1's
// fee/account/key checks. The deep-freeze flag bits are valid within
// tfTrustSetMask, so the flag mask never rejects them; only this
// amendment-conditional check does.
// Reference: rippled SetTrust.cpp preflight() (featureDeepFreeze gate).
func (t *TrustSet) PreflightRules(rules *amendment.Rules) error {
	if !rules.DeepFreezeEnabled() &&
		t.GetFlags()&(TrustSetFlagSetDeepFreeze|TrustSetFlagClearDeepFreeze) != 0 {
		return ter.Errorf(ter.TemINVALID_FLAG, "deep freeze flags require the DeepFreeze amendment")
	}
	return nil
}

func (t *TrustSet) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(t)
}

// SetNoRipple sets the no ripple flag on this trust line
func (t *TrustSet) SetNoRipple() {
	flags := t.GetFlags() | TrustSetFlagSetNoRipple
	t.SetFlags(flags)
}

// ClearNoRipple clears the no ripple flag on this trust line
func (t *TrustSet) ClearNoRipple() {
	flags := t.GetFlags() | TrustSetFlagClearNoRipple
	t.SetFlags(flags)
}

// SetFreeze freezes this trust line
func (t *TrustSet) SetFreeze() {
	flags := t.GetFlags() | TrustSetFlagSetFreeze
	t.SetFlags(flags)
}

// computeFreezeFlags computes the resulting trust line flags after applying
// freeze/deep-freeze flag changes. Matches rippled's computeFreezeFlags() exactly.
// Reference: rippled SetTrust.cpp lines 34-64
func computeFreezeFlags(
	uFlags uint32,
	bHigh bool,
	bNoFreeze bool,
	bSetFreeze bool,
	bClearFreeze bool,
	bSetDeepFreeze bool,
	bClearDeepFreeze bool,
) uint32 {
	if bSetFreeze && !bClearFreeze && !bNoFreeze {
		if bHigh {
			uFlags |= state.LsfHighFreeze
		} else {
			uFlags |= state.LsfLowFreeze
		}
	} else if bClearFreeze && !bSetFreeze {
		if bHigh {
			uFlags &^= state.LsfHighFreeze
		} else {
			uFlags &^= state.LsfLowFreeze
		}
	}
	if bSetDeepFreeze && !bClearDeepFreeze && !bNoFreeze {
		if bHigh {
			uFlags |= state.LsfHighDeepFreeze
		} else {
			uFlags |= state.LsfLowDeepFreeze
		}
	} else if bClearDeepFreeze && !bSetDeepFreeze {
		if bHigh {
			uFlags &^= state.LsfHighDeepFreeze
		} else {
			uFlags &^= state.LsfLowDeepFreeze
		}
	}
	return uFlags
}

// Apply applies a TrustSet transaction to the ledger state.
// Reference: rippled SetTrust.cpp doApply
func (t *TrustSet) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("trust set apply",
		"account", t.Account,
		"currency", t.LimitAmount.Currency,
		"issuer", t.LimitAmount.Issuer,
		"value", t.LimitAmount.Value,
		"qualityIn", t.QualityIn,
		"qualityOut", t.QualityOut,
		"flags", t.GetFlags(),
	)

	accountID, err := state.DecodeAccountID(ctx.Account.Account)
	if err != nil {
		return ter.TefINTERNAL
	}
	issuerAccountID, err := state.DecodeAccountID(t.LimitAmount.Issuer)
	if err != nil {
		return ter.TemBAD_ISSUER
	}
	issuerKey := keylet.Account(issuerAccountID)

	// Parse transaction flags up front — tfSetfAuth gates the first preclaim check.
	txFlags := uint32(0)
	if t.Flags != nil {
		txFlags = *t.Flags
	}
	bSetAuth := (txFlags & TrustSetFlagSetfAuth) != 0
	bSetNoRipple := (txFlags & TrustSetFlagSetNoRipple) != 0
	bClearNoRipple := (txFlags & TrustSetFlagClearNoRipple) != 0
	bSetFreeze := (txFlags & TrustSetFlagSetFreeze) != 0
	bClearFreeze := (txFlags & TrustSetFlagClearFreeze) != 0
	bSetDeepFreeze := (txFlags & TrustSetFlagSetDeepFreeze) != 0
	bClearDeepFreeze := (txFlags & TrustSetFlagClearDeepFreeze) != 0

	// tefNO_AUTH_REQUIRED — tfSetfAuth requires the sender to have lsfRequireAuth.
	// rippled SetTrust::preclaim evaluates this right after loading the sender
	// account, before the self-issuer and destination-existence checks.
	if bSetAuth && (ctx.Account.Flags&state.LsfRequireAuth) == 0 {
		return ter.TefNO_AUTH_REQUIRED
	}

	// Get or create the trust line.
	trustLineKey := keylet.Line(accountID, issuerAccountID, t.LimitAmount.Currency)
	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return ter.TefINTERNAL
	}

	// temDST_IS_SRC — a trust line to self is always rejected. rippled
	// SetTrust::preclaim orders this after the tfSetfAuth check and before the
	// destination read.
	if accountID == issuerAccountID {
		return ter.TemDST_IS_SRC
	}

	// Check issuer (destination) exists and load it for the flag checks below.
	// Per rippled SetTrust.cpp: returns tecNO_DST when the destination doesn't exist.
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil || issuerData == nil {
		ctx.Log.Warn("trust set: issuer account does not exist",
			"issuer", t.LimitAmount.Issuer,
		)
		return ter.TecNO_DST
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return ter.TefINTERNAL
	}

	usesReserveSponsor, result := ctx.UsesReserveSponsorFor(ctx.AccountID, ctx.Account)
	if result != ter.TesSUCCESS {
		return result
	}
	effectiveOwners, ok := tx.EffectiveOwnerCount(ctx.Account, 0)
	if !ok {
		return ter.TefINTERNAL
	}
	requiresReserve := usesReserveSponsor || effectiveOwners >= 2
	// mPriorBalance is the balance BEFORE fee deduction, matching rippled's
	// Transactor::mPriorBalance (set before doApply is called).
	mPriorBalance := ctx.PriorBalance()

	// Determine low/high accounts (for consistent trust line ordering)
	bHigh := state.CompareAccountIDs(accountID, issuerAccountID) > 0

	// If the destination has opted to disallow incoming trustlines, honour that flag.
	if issuerAccount.Flags&state.LsfDisallowIncomingTrustline != 0 {
		// fixDisallowIncomingV1: if the trust line already exists, allow the TrustSet
		if ctx.Rules().Enabled(amendment.FeatureFixDisallowIncomingV1) && trustLineExists {
			// pass — existing trust lines are allowed
		} else {
			return ter.TecNO_PERMISSION
		}
	}

	// In general, trust lines to pseudo-accounts (AMM) are not permitted
	// unless the trust line already exists or it's an LP token trust line
	// for a non-empty AMM.
	// Reference: rippled SetTrust.cpp lines 273-309
	if issuerAccount.IsPseudoAccount() {
		if issuerAccount.AMMID != [32]byte{} {
			if trustLineExists {
				// Allow modification of existing trust lines to AMM accounts.
			} else {
				// Read the AMM SLE to check LP token balance and currency.
				ammKey := keylet.AMMByID(issuerAccount.AMMID)
				ammRawData, err := ctx.View.Read(ammKey)
				if err != nil || ammRawData == nil {
					return ter.TecINTERNAL
				}
				ammData, err := amm.ParseAMMData(ammRawData)
				if err != nil {
					return ter.TecINTERNAL
				}
				if amm.IsAMMEmpty(ammData) {
					return ter.TecAMM_EMPTY
				}
				// Compute LP token currency from the AMM's asset pair
				lptCurrency := amm.GenerateAMMLPTCurrency(ammData.Asset.Currency, ammData.Asset2.Currency)
				if lptCurrency != t.LimitAmount.Currency {
					return ter.TecNO_PERMISSION
				}
				// LP token trust line to non-empty AMM — allow creation
			}
		} else if issuerAccount.VaultID != [32]byte{} || issuerAccount.LoanBrokerID != [32]byte{} {
			if !trustLineExists {
				return ter.TecNO_PERMISSION
			}
		} else {
			return ter.TecPSEUDO_ACCOUNT
		}
	}

	bNoFreeze := (ctx.Account.Flags & state.LsfNoFreeze) != 0

	// Deep freeze preclaim invariants. The amendment-disabled flag rejection is a
	// preflight check (PreflightRules); only these ledger-state invariants remain.
	// Reference: rippled SetTrust.cpp preclaim() freeze/deep-freeze checks.
	if ctx.Rules().DeepFreezeEnabled() {
		// Check #1: Cannot freeze if account has lsfNoFreeze set.
		// Reference: rippled preclaim() lines 318-322
		if bNoFreeze && (bSetFreeze || bSetDeepFreeze) {
			return ter.TecNO_PERMISSION
		}

		// Check #2: Cannot set and clear freeze in same transaction.
		// Reference: rippled preclaim() lines 326-332
		if (bSetFreeze || bSetDeepFreeze) && (bClearFreeze || bClearDeepFreeze) {
			return ter.TecNO_PERMISSION
		}

		// Check #3: Compute what the trust line flags WOULD be after applying,
		// and reject if deep frozen without being frozen.
		// Reference: rippled preclaim() lines 334-360
		var currentFlags uint32
		if trustLineExists {
			trustLineData, readErr := ctx.View.Read(trustLineKey)
			if readErr != nil {
				return ter.TefINTERNAL
			}
			if trustLineData != nil {
				rs, parseErr := state.ParseRippleState(trustLineData)
				if parseErr != nil {
					return ter.TefINTERNAL
				}
				currentFlags = rs.Flags
			}
		}

		resultFlags := computeFreezeFlags(
			currentFlags, bHigh, bNoFreeze,
			bSetFreeze, bClearFreeze,
			bSetDeepFreeze, bClearDeepFreeze,
		)

		var frozen, deepFrozen bool
		if bHigh {
			frozen = resultFlags&state.LsfHighFreeze != 0
			deepFrozen = resultFlags&state.LsfHighDeepFreeze != 0
		} else {
			frozen = resultFlags&state.LsfLowFreeze != 0
			deepFrozen = resultFlags&state.LsfLowDeepFreeze != 0
		}

		if deepFrozen && !frozen {
			return ter.TecNO_PERMISSION
		}
	}

	// Parse quality values from transaction
	var uQualityIn, uQualityOut uint32
	bQualityIn := t.QualityIn != nil
	bQualityOut := t.QualityOut != nil

	if bQualityIn {
		uQualityIn = *t.QualityIn
	}
	if bQualityOut {
		uQualityOut = *t.QualityOut
		if uQualityOut == protocol.QualityOne {
			uQualityOut = 0
		}
	}

	// Use the limit amount directly (it's already a tx.Amount)
	limitAmount := t.LimitAmount

	if !trustLineExists {
		// Setting a non-existent line to defaults is redundant.
		// Reference: rippled SetTrust.cpp lines 698-708
		if limitAmount.IsZero() && !bSetAuth && (!bQualityIn || uQualityIn == 0) && (!bQualityOut || uQualityOut == 0) {
			return ter.TecNO_LINE_REDUNDANT
		}

		// Check account has reserve for new trust line
		// Reference: rippled SetTrust.cpp line 710: mPriorBalance < reserveCreate
		if requiresReserve {
			result = ctx.CheckReserveFor(ctx.AccountID, ctx.Account, mPriorBalance, 1, 0, ter.TecNO_LINE_INSUF_RESERVE)
		}
		if result != ter.TesSUCCESS {
			ctx.Log.Warn("trust set: insufficient reserve for new trust line",
				"balance", mPriorBalance,
			)
			return result
		}
		sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
		if result != ter.TesSUCCESS {
			return result
		}

		// Create the trust line through the shared canonical creator. The sender
		// is rippled's "account being set" (it owns the limit), so LimitIssuer ==
		// Src; tx.TrustCreate derives low/high ordering, reserve/flag placement,
		// the peer-DefaultRipple noRipple rule, both owner-dir inserts, and the
		// LowNode/HighNode deletion hints. OwnerCount stays a caller concern and
		// PreviousTxnID/PreviousTxnLgrSeq are stamped by the apply threading pass.
		accountStr, err := state.EncodeAccountID(accountID)
		if err != nil {
			return ter.TefINTERNAL
		}
		result = tx.TrustCreate(ctx.View, tx.TrustCreateParams{
			SrcHigh:     bHigh,
			Src:         accountID,
			Dst:         issuerAccountID,
			LineKey:     trustLineKey,
			LimitIssuer: accountID,
			Auth:        bSetAuth,
			NoRipple:    bSetNoRipple && !bClearNoRipple,
			Freeze:      bSetFreeze && !bClearFreeze && !bNoFreeze,
			DeepFreeze:  bSetDeepFreeze && !bClearDeepFreeze && !bNoFreeze,
			Balance:     tx.NewIssuedAmount(0, -100, t.LimitAmount.Currency, state.AccountOneAddress),
			Limit:       tx.NewIssuedAmount(limitAmount.IOU().Mantissa(), limitAmount.IOU().Exponent(), t.LimitAmount.Currency, accountStr),
			QualityIn:   uQualityIn,
			QualityOut:  uQualityOut,
			Sponsor:     sponsorAddress,
		})
		if result != ter.TesSUCCESS {
			return result
		}
	} else {
		// Modify existing trust line
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil {
			return ter.TefINTERNAL
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Per rippled: saLimitAllow = saLimitAmount; saLimitAllow.setIssuer(account_);
		// The limit's issuer must be set to the sender's account, not the counterparty.
		// In a RippleState, LowLimit.Issuer = lowAccount, HighLimit.Issuer = highAccount.
		saLimitAllow := tx.NewIssuedAmount(limitAmount.IOU().Mantissa(), limitAmount.IOU().Exponent(), limitAmount.Currency, ctx.Account.Account)
		if !bHigh {
			rs.LowLimit = saLimitAllow
		} else {
			rs.HighLimit = saLimitAllow
		}

		// Handle Auth flag (can only be set, not cleared per rippled)
		if bSetAuth {
			if bHigh {
				rs.Flags |= state.LsfHighAuth
			} else {
				rs.Flags |= state.LsfLowAuth
			}
		}

		// Handle NoRipple flag
		if bSetNoRipple && !bClearNoRipple {
			var balanceFromPerspective bool
			if bHigh {
				balanceFromPerspective = rs.Balance.Signum() <= 0
			} else {
				balanceFromPerspective = rs.Balance.Signum() >= 0
			}
			if balanceFromPerspective {
				if bHigh {
					rs.Flags |= state.LsfHighNoRipple
				} else {
					rs.Flags |= state.LsfLowNoRipple
				}
			} else {
				// Cannot set noRipple on a negative balance.
				return ter.TecNO_PERMISSION
			}
		} else if bClearNoRipple && !bSetNoRipple {
			if bHigh {
				rs.Flags &^= state.LsfHighNoRipple
			} else {
				rs.Flags &^= state.LsfLowNoRipple
			}
		}

		// Handle Freeze flag
		if bSetFreeze && !bClearFreeze && !bNoFreeze {
			if bHigh {
				rs.Flags |= state.LsfHighFreeze
			} else {
				rs.Flags |= state.LsfLowFreeze
			}
		} else if bClearFreeze && !bSetFreeze {
			if bHigh {
				rs.Flags &^= state.LsfHighFreeze
			} else {
				rs.Flags &^= state.LsfLowFreeze
			}
		}

		// Handle DeepFreeze flag
		if bSetDeepFreeze && !bClearDeepFreeze && !bNoFreeze {
			if bHigh {
				rs.Flags |= state.LsfHighDeepFreeze
			} else {
				rs.Flags |= state.LsfLowDeepFreeze
			}
		} else if bClearDeepFreeze && !bSetDeepFreeze {
			if bHigh {
				rs.Flags &^= state.LsfHighDeepFreeze
			} else {
				rs.Flags &^= state.LsfLowDeepFreeze
			}
		}

		// Handle QualityIn
		if bQualityIn {
			if bHigh {
				rs.HighQualityIn = uQualityIn
				rs.HasHighQualityIn = uQualityIn != 0
			} else {
				rs.LowQualityIn = uQualityIn
				rs.HasLowQualityIn = uQualityIn != 0
			}
		}

		// Handle QualityOut
		if bQualityOut {
			if bHigh {
				rs.HighQualityOut = uQualityOut
				rs.HasHighQualityOut = uQualityOut != 0
			} else {
				rs.LowQualityOut = uQualityOut
				rs.HasLowQualityOut = uQualityOut != 0
			}
		}

		lowQualityIn := rs.LowQualityIn
		if lowQualityIn == protocol.QualityOne {
			lowQualityIn = 0
		}
		lowQualityOut := rs.LowQualityOut
		if lowQualityOut == protocol.QualityOne {
			lowQualityOut = 0
		}
		highQualityIn := rs.HighQualityIn
		if highQualityIn == protocol.QualityOne {
			highQualityIn = 0
		}
		highQualityOut := rs.HighQualityOut
		if highQualityOut == protocol.QualityOne {
			highQualityOut = 0
		}

		// Check if trust line should be deleted
		var bLowDefRipple, bHighDefRipple bool
		if bHigh {
			bLowDefRipple = (issuerAccount.Flags & state.LsfDefaultRipple) != 0
			bHighDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		} else {
			bLowDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
			bHighDefRipple = (issuerAccount.Flags & state.LsfDefaultRipple) != 0
		}

		bLowReserveSet := lowQualityIn != 0 || lowQualityOut != 0 ||
			((rs.Flags&state.LsfLowNoRipple) == 0) != bLowDefRipple ||
			(rs.Flags&state.LsfLowFreeze) != 0 || !rs.LowLimit.IsZero() ||
			rs.Balance.Signum() > 0

		bHighReserveSet := highQualityIn != 0 || highQualityOut != 0 ||
			((rs.Flags&state.LsfHighNoRipple) == 0) != bHighDefRipple ||
			(rs.Flags&state.LsfHighFreeze) != 0 || !rs.HighLimit.IsZero() ||
			rs.Balance.Signum() < 0

		// Record previous reserve state before modifying
		// Reference: rippled SetTrust.cpp lines 636-668
		bLowReserved := (rs.Flags & state.LsfLowReserve) != 0
		bHighReserved := (rs.Flags & state.LsfHighReserve) != 0

		bDefault := !bLowReserveSet && !bHighReserveSet
		badCurrency := keylet.CurrencyBytes(t.LimitAmount.Currency) == keylet.BadCurrency()

		if bLowReserveSet && !bLowReserved {
			if !bHigh && requiresReserve {
				if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, mPriorBalance, 1, 0, ter.TecINSUF_RESERVE_LINE); result != ter.TesSUCCESS {
					return result
				}
			}
			rs.Flags |= state.LsfLowReserve
			if !bHigh {
				sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
				if result != ter.TesSUCCESS {
					return result
				}
				rs.LowSponsor = sponsorAddress
			} else {
				issuerAccount.OwnerCount = tx.ConfineOwnerCount(issuerAccount.OwnerCount, 1)
			}
		} else if !bLowReserveSet && bLowReserved {
			rs.Flags &^= state.LsfLowReserve
			if !bHigh {
				if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, rs.LowSponsor, 1); err != nil {
					return ctx.Internal("TrustSet.LowOwnerCount", err)
				}
			} else {
				if err := tx.DecreaseOwnerCount(ctx.View, issuerAccount, rs.LowSponsor, 1); err != nil {
					return ctx.Internal("TrustSet.LowOwnerCount", err)
				}
				ctx.SyncSenderSponsorCounts(rs.LowSponsor)
			}
			rs.LowSponsor = ""
		}

		if bHighReserveSet && !bHighReserved {
			if bHigh && requiresReserve {
				if result := ctx.CheckReserveFor(ctx.AccountID, ctx.Account, mPriorBalance, 1, 0, ter.TecINSUF_RESERVE_LINE); result != ter.TesSUCCESS {
					return result
				}
			}
			rs.Flags |= state.LsfHighReserve
			if bHigh {
				sponsorAddress, result := tx.IncreaseOwnerCount(ctx, ctx.AccountID, ctx.Account, 1)
				if result != ter.TesSUCCESS {
					return result
				}
				rs.HighSponsor = sponsorAddress
			} else {
				issuerAccount.OwnerCount = tx.ConfineOwnerCount(issuerAccount.OwnerCount, 1)
			}
		} else if !bHighReserveSet && bHighReserved {
			rs.Flags &^= state.LsfHighReserve
			if bHigh {
				if err := tx.DecreaseOwnerCount(ctx.View, ctx.Account, rs.HighSponsor, 1); err != nil {
					return ctx.Internal("TrustSet.HighOwnerCount", err)
				}
			} else {
				if err := tx.DecreaseOwnerCount(ctx.View, issuerAccount, rs.HighSponsor, 1); err != nil {
					return ctx.Internal("TrustSet.HighOwnerCount", err)
				}
				ctx.SyncSenderSponsorCounts(rs.HighSponsor)
			}
			rs.HighSponsor = ""
		}

		issuerChanged := (bLowReserveSet && !bLowReserved && bHigh) ||
			(!bLowReserveSet && bLowReserved && bHigh) ||
			(bHighReserveSet && !bHighReserved && !bHigh) ||
			(!bHighReserveSet && bHighReserved && !bHigh)

		if bDefault || badCurrency {
			// Remove from both owner directories before erasing
			// Reference: rippled trustDelete() in View.cpp
			var lowAccountID, highAccountID [20]byte
			if !bHigh {
				lowAccountID = accountID
				highAccountID = issuerAccountID
			} else {
				lowAccountID = issuerAccountID
				highAccountID = accountID
			}
			lowDirKey := keylet.OwnerDir(lowAccountID)
			if res, err := state.DirRemove(ctx.View, lowDirKey, rs.LowNode, trustLineKey.Key, false); err != nil || !res.Success {
				return ter.TefBAD_LEDGER
			}
			highDirKey := keylet.OwnerDir(highAccountID)
			if res, err := state.DirRemove(ctx.View, highDirKey, rs.HighNode, trustLineKey.Key, false); err != nil || !res.Success {
				return ter.TefBAD_LEDGER
			}

			// Persist the trust line's field changes (limit/flags/quality) before
			// erasing it, so the DeletedNode metadata carries them (FinalFields and
			// PreviousFields). Matches rippled SetTrust, which sets the fields on the
			// SLE before trustDelete().
			modifiedData, serErr := state.SerializeRippleState(rs)
			if serErr != nil {
				return ter.TefINTERNAL
			}
			if err := ctx.View.Update(trustLineKey, modifiedData); err != nil {
				return ter.TefINTERNAL
			}

			if err := ctx.View.Erase(trustLineKey); err != nil {
				return ter.TefINTERNAL
			}

			if issuerChanged {
				if latest, readErr := tx.ReadAccountRoot(ctx.View, issuerAccountID); readErr != nil || latest == nil {
					return ter.TefINTERNAL
				} else {
					issuerAccount.SponsoringOwnerCount = latest.SponsoringOwnerCount
				}
				issuerUpdatedData, serErr := state.SerializeAccountRoot(issuerAccount)
				if serErr != nil {
					return ter.TefINTERNAL
				}
				if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
					return ter.TefINTERNAL
				}
			}
		} else {
			// Write issuer account back if its OwnerCount changed
			if issuerChanged {
				if latest, readErr := tx.ReadAccountRoot(ctx.View, issuerAccountID); readErr != nil || latest == nil {
					return ter.TefINTERNAL
				} else {
					issuerAccount.SponsoringOwnerCount = latest.SponsoringOwnerCount
				}
				issuerUpdatedData, serErr := state.SerializeAccountRoot(issuerAccount)
				if serErr != nil {
					return ter.TefINTERNAL
				}
				if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
					return ter.TefINTERNAL
				}
			}

			updatedData, err := state.SerializeRippleState(rs)
			if err != nil {
				return ter.TefINTERNAL
			}

			if err := ctx.View.Update(trustLineKey, updatedData); err != nil {
				return ter.TefINTERNAL
			}
		}
	}

	return ter.TesSUCCESS
}
