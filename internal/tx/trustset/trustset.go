package trustset

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/amm"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func init() {
	tx.Register(tx.TypeTrustSet, func() tx.Transaction {
		return &TrustSet{BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "")}
	})
}

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

	// tfTrustSetMask is the mask for valid TrustSet transaction flags
	TrustSetFlagMask uint32 = ^(TrustSetFlagSetfAuth |
		TrustSetFlagSetNoRipple |
		TrustSetFlagClearNoRipple |
		TrustSetFlagSetFreeze |
		TrustSetFlagClearFreeze |
		TrustSetFlagSetDeepFreeze |
		TrustSetFlagClearDeepFreeze |
		tx.TfFullyCanonicalSig)
)

// NewTrustSet creates a new TrustSet transaction
func NewTrustSet(account string, limitAmount tx.Amount) *TrustSet {
	return &TrustSet{
		BaseTx:      *tx.NewBaseTx(tx.TypeTrustSet, account),
		LimitAmount: limitAmount,
	}
}

// TxType returns the transaction type
func (t *TrustSet) TxType() tx.Type {
	return tx.TypeTrustSet
}

// Validate validates the TrustSet transaction
// Reference: rippled SetTrust.cpp preflight()
func (t *TrustSet) Validate() error {
	if err := t.BaseTx.Validate(); err != nil {
		return err
	}

	txFlags := t.GetFlags()

	// Check for invalid transaction flags
	if txFlags&TrustSetFlagMask != 0 {
		return tx.Errorf(tx.TemINVALID_FLAG, "invalid transaction flags")
	}

	// LimitAmount must be an issued currency, not XRP
	if t.LimitAmount.IsNative() {
		return tx.Errorf(tx.TemBAD_LIMIT, "cannot create trust line for XRP")
	}

	if t.LimitAmount.Currency == "" {
		return tx.Errorf(tx.TemBAD_CURRENCY, "currency is required")
	}

	// Check for XRP currency code
	if t.LimitAmount.Currency == "XRP" {
		return tx.Errorf(tx.TemBAD_CURRENCY, "cannot use XRP as IOU currency")
	}

	// Negative limit is not allowed
	if t.LimitAmount.IsNegative() {
		return tx.Errorf(tx.TemBAD_LIMIT, "negative credit limit")
	}

	// Check if destination makes sense
	if t.LimitAmount.Issuer == "" {
		return tx.Errorf(tx.TemDST_NEEDED, "issuer is required")
	}

	// Cannot create trust line to self
	if t.LimitAmount.Issuer == t.Account {
		return tx.Errorf(tx.TemDST_IS_SRC, "cannot create trust line to self")
	}

	// Check for contradictory NoRipple flags
	setNoRipple := txFlags&TrustSetFlagSetNoRipple != 0
	clearNoRipple := txFlags&TrustSetFlagClearNoRipple != 0
	if setNoRipple && clearNoRipple {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set and clear NoRipple")
	}

	// Check for contradictory Freeze flags
	setFreeze := txFlags&TrustSetFlagSetFreeze != 0
	clearFreeze := txFlags&TrustSetFlagClearFreeze != 0
	setDeepFreeze := txFlags&TrustSetFlagSetDeepFreeze != 0
	clearDeepFreeze := txFlags&TrustSetFlagClearDeepFreeze != 0

	if (setFreeze || setDeepFreeze) && (clearFreeze || clearDeepFreeze) {
		return tx.Errorf(tx.TemINVALID_FLAG, "cannot set and clear freeze in same transaction")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
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

// Apply applies a TrustSet transaction to the ledger state.
// Reference: rippled SetTrust.cpp doApply
func (t *TrustSet) Apply(ctx *tx.ApplyContext) tx.Result {
	// Cannot create trust line to self
	if t.LimitAmount.Issuer == ctx.Account.Account {
		return tx.TemDST_IS_SRC
	}

	// Get the issuer account ID
	issuerAccountID, err := state.DecodeAccountID(t.LimitAmount.Issuer)
	if err != nil {
		return tx.TemBAD_ISSUER
	}
	issuerKey := keylet.Account(issuerAccountID)

	// Check issuer exists and get issuer account for flag checks
	// Per rippled SetTrust.cpp: returns tecNO_DST when destination (issuer) doesn't exist
	issuerData, err := ctx.View.Read(issuerKey)
	if err != nil || issuerData == nil {
		return tx.TecNO_DST
	}
	issuerAccount, err := state.ParseAccountRoot(issuerData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Get the account ID
	accountID, _ := state.DecodeAccountID(ctx.Account.Account)

	// Determine low/high accounts (for consistent trust line ordering)
	bHigh := state.CompareAccountIDsForLine(accountID, issuerAccountID) > 0

	// Get or create the trust line
	trustLineKey := keylet.Line(accountID, issuerAccountID, t.LimitAmount.Currency)

	trustLineExists, err := ctx.View.Exists(trustLineKey)
	if err != nil {
		return tx.TefINTERNAL
	}

	// If the destination has opted to disallow incoming trustlines, honour that flag.
	// Reference: rippled SetTrust.cpp lines 254-271
	if ctx.Rules().DisallowIncomingEnabled() {
		if issuerAccount.Flags&state.LsfDisallowIncomingTrustline != 0 {
			// fixDisallowIncomingV1: if the trust line already exists, allow the TrustSet
			if ctx.Rules().Enabled(amendment.FeatureFixDisallowIncomingV1) && trustLineExists {
				// pass — existing trust lines are allowed
			} else {
				return tx.TecNO_PERMISSION
			}
		}
	}

	// In general, trust lines to pseudo-accounts (AMM) are not permitted
	// unless the trust line already exists or it's an LP token trust line
	// for a non-empty AMM.
	// Reference: rippled SetTrust.cpp lines 273-309
	var zeroHash [32]byte
	if (issuerAccount.Flags & state.LsfAMM) != 0 {
		if issuerAccount.AMMID != zeroHash {
			if trustLineExists {
				// Allow modification of existing trust lines to AMM accounts.
			} else {
				// Read the AMM SLE to check LP token balance and currency.
				ammKey := keylet.AMMByID(issuerAccount.AMMID)
				ammRawData, err := ctx.View.Read(ammKey)
				if err != nil || ammRawData == nil {
					return tx.TecINTERNAL
				}
				ammData, err := amm.ParseAMMData(ammRawData)
				if err != nil {
					return tx.TecINTERNAL
				}
				if amm.IsAMMEmpty(ammData) {
					return tx.TecAMM_EMPTY
				}
				// Compute LP token currency from the AMM's asset pair
				lptCurrency := amm.GenerateAMMLPTCurrency(ammData.Asset.Currency, ammData.Asset2.Currency)
				if lptCurrency != t.LimitAmount.Currency {
					return tx.TecNO_PERMISSION
				}
				// LP token trust line to non-empty AMM — allow creation
			}
		} else {
			return tx.TecPSEUDO_ACCOUNT
		}
	}

	// Parse transaction flags
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

	// Validate tfSetfAuth - requires issuer to have lsfRequireAuth set
	if bSetAuth && (ctx.Account.Flags&state.LsfRequireAuth) == 0 {
		return tx.TefNO_AUTH_REQUIRED
	}

	// Validate freeze flags - cannot freeze if account has lsfNoFreeze set
	bNoFreeze := (ctx.Account.Flags & state.LsfNoFreeze) != 0
	if bNoFreeze && (bSetFreeze || bSetDeepFreeze) {
		return tx.TecNO_PERMISSION
	}

	// Parse quality values from transaction
	const qualityOne uint32 = 1000000000
	var uQualityIn, uQualityOut uint32
	bQualityIn := t.QualityIn != nil
	bQualityOut := t.QualityOut != nil

	if bQualityIn {
		uQualityIn = *t.QualityIn
		if uQualityIn == qualityOne {
			uQualityIn = 0
		}
	}
	if bQualityOut {
		uQualityOut = *t.QualityOut
		if uQualityOut == qualityOne {
			uQualityOut = 0
		}
	}

	// Use the limit amount directly (it's already a tx.Amount)
	limitAmount := t.LimitAmount

	if !trustLineExists {
		// Check if setting zero limit without existing trust line
		if limitAmount.IsZero() && !bSetAuth && (!bQualityIn || uQualityIn == 0) && (!bQualityOut || uQualityOut == 0) {
			return tx.TesSUCCESS
		}

		// Check account has reserve for new trust line
		// Reference: rippled SetTrust.cpp line 710: tecNO_LINE_INSUF_RESERVE for new lines
		reserveCreate := ctx.ReserveForNewObject(ctx.Account.OwnerCount)
		if ctx.Account.Balance < reserveCreate {
			return tx.TecNO_LINE_INSUF_RESERVE
		}

		// Determine the LOW and HIGH account IDs
		var lowAccountID, highAccountID [20]byte
		if !bHigh {
			lowAccountID = accountID
			highAccountID = issuerAccountID
		} else {
			lowAccountID = issuerAccountID
			highAccountID = accountID
		}

		// Create new RippleState
		// Note: In RippleState, Balance.Issuer is a special "no account" address (ACCOUNT_ONE)
		rs := &state.RippleState{
			Balance:           tx.NewIssuedAmount(0, -100, t.LimitAmount.Currency, state.AccountOneAddress),
			Flags:             0,
			LowNode:           0,
			HighNode:          0,
			PreviousTxnID:     ctx.TxHash,
			PreviousTxnLgrSeq: ctx.Config.LedgerSequence,
		}

		// Set the limit based on which side this account is
		// Note: In RippleState, LowLimit.Issuer = LOW account, HighLimit.Issuer = HIGH account
		// The "issuer" in these Amount fields refers to which account owns that limit
		lowAccountStr, _ := state.EncodeAccountID(lowAccountID)
		highAccountStr, _ := state.EncodeAccountID(highAccountID)

		if !bHigh {
			// Transaction sender is LOW account
			rs.LowLimit = tx.NewIssuedAmount(limitAmount.IOU().Mantissa(), limitAmount.IOU().Exponent(), t.LimitAmount.Currency, lowAccountStr)
			rs.HighLimit = tx.NewIssuedAmount(0, -100, t.LimitAmount.Currency, highAccountStr)
			rs.Flags |= state.LsfLowReserve
		} else {
			// Transaction sender is HIGH account
			rs.LowLimit = tx.NewIssuedAmount(0, -100, t.LimitAmount.Currency, lowAccountStr)
			rs.HighLimit = tx.NewIssuedAmount(limitAmount.IOU().Mantissa(), limitAmount.IOU().Exponent(), t.LimitAmount.Currency, highAccountStr)
			rs.Flags |= state.LsfHighReserve
		}

		// Handle Auth flag for new trust line
		if bSetAuth {
			if bHigh {
				rs.Flags |= state.LsfHighAuth
			} else {
				rs.Flags |= state.LsfLowAuth
			}
		}

		// Handle NoRipple flag from transaction
		if bSetNoRipple && !bClearNoRipple {
			if bHigh {
				rs.Flags |= state.LsfHighNoRipple
			} else {
				rs.Flags |= state.LsfLowNoRipple
			}
		}

		// If the peer (destination/issuer) does not have DefaultRipple,
		// set NoRipple on the peer's side of the trust line.
		// Reference: rippled trustCreate() in View.cpp lines 1428-1432
		if (issuerAccount.Flags & state.LsfDefaultRipple) == 0 {
			if bHigh {
				// Sender is high, peer is low
				rs.Flags |= state.LsfLowNoRipple
			} else {
				// Sender is low, peer is high
				rs.Flags |= state.LsfHighNoRipple
			}
		}

		// Handle Freeze flag for new trust line
		if bSetFreeze && !bClearFreeze && !bNoFreeze {
			if bHigh {
				rs.Flags |= state.LsfHighFreeze
			} else {
				rs.Flags |= state.LsfLowFreeze
			}
		}

		// Handle DeepFreeze flag for new trust line
		if bSetDeepFreeze && !bClearDeepFreeze && !bNoFreeze {
			if bHigh {
				rs.Flags |= state.LsfHighDeepFreeze
			} else {
				rs.Flags |= state.LsfLowDeepFreeze
			}
		}

		// Handle QualityIn/QualityOut for new trust line
		if bQualityIn && uQualityIn != 0 {
			if bHigh {
				rs.HighQualityIn = uQualityIn
			} else {
				rs.LowQualityIn = uQualityIn
			}
		}
		if bQualityOut && uQualityOut != 0 {
			if bHigh {
				rs.HighQualityOut = uQualityOut
			} else {
				rs.LowQualityOut = uQualityOut
			}
		}

		// Add trust line to LOW account's owner directory
		lowDirKey := keylet.OwnerDir(lowAccountID)
		lowDirResult, err := state.DirInsert(ctx.View, lowDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
			dir.Owner = lowAccountID
		})
		if err != nil {
			return tx.TefINTERNAL
		}

		// Add trust line to HIGH account's owner directory
		highDirKey := keylet.OwnerDir(highAccountID)
		highDirResult, err := state.DirInsert(ctx.View, highDirKey, trustLineKey.Key, func(dir *state.DirectoryNode) {
			dir.Owner = highAccountID
		})
		if err != nil {
			return tx.TefINTERNAL
		}

		// Set LowNode and HighNode on the RippleState (deletion hints)
		rs.LowNode = lowDirResult.Page
		rs.HighNode = highDirResult.Page

		// Serialize and insert the trust line
		trustLineData, err := state.SerializeRippleState(rs)
		if err != nil {
			return tx.TefINTERNAL
		}

		if err := ctx.View.Insert(trustLineKey, trustLineData); err != nil {
			return tx.TefINTERNAL
		}

		// Increment owner count for the transaction sender
		ctx.Account.OwnerCount++

	} else {
		// Modify existing trust line
		trustLineData, err := ctx.View.Read(trustLineKey)
		if err != nil {
			return tx.TefINTERNAL
		}

		rs, err := state.ParseRippleState(trustLineData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Update the limit.
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
			if uQualityIn != 0 {
				if bHigh {
					rs.HighQualityIn = uQualityIn
				} else {
					rs.LowQualityIn = uQualityIn
				}
			} else {
				if bHigh {
					rs.HighQualityIn = 0
				} else {
					rs.LowQualityIn = 0
				}
			}
		}

		// Handle QualityOut
		if bQualityOut {
			if uQualityOut != 0 {
				if bHigh {
					rs.HighQualityOut = uQualityOut
				} else {
					rs.LowQualityOut = uQualityOut
				}
			} else {
				if bHigh {
					rs.HighQualityOut = 0
				} else {
					rs.LowQualityOut = 0
				}
			}
		}

		// Normalize quality values
		if rs.LowQualityIn == qualityOne {
			rs.LowQualityIn = 0
		}
		if rs.LowQualityOut == qualityOne {
			rs.LowQualityOut = 0
		}
		if rs.HighQualityIn == qualityOne {
			rs.HighQualityIn = 0
		}
		if rs.HighQualityOut == qualityOne {
			rs.HighQualityOut = 0
		}

		// Check if trust line should be deleted
		bLowDefRipple := (issuerAccount.Flags & state.LsfDefaultRipple) != 0
		bHighDefRipple := (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		if bHigh {
			bLowDefRipple = (issuerAccount.Flags & state.LsfDefaultRipple) != 0
			bHighDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
		} else {
			bLowDefRipple = (ctx.Account.Flags & state.LsfDefaultRipple) != 0
			bHighDefRipple = (issuerAccount.Flags & state.LsfDefaultRipple) != 0
		}

		bLowReserveSet := rs.LowQualityIn != 0 || rs.LowQualityOut != 0 ||
			((rs.Flags&state.LsfLowNoRipple) == 0) != bLowDefRipple ||
			(rs.Flags&state.LsfLowFreeze) != 0 || !rs.LowLimit.IsZero() ||
			rs.Balance.Signum() > 0

		bHighReserveSet := rs.HighQualityIn != 0 || rs.HighQualityOut != 0 ||
			((rs.Flags&state.LsfHighNoRipple) == 0) != bHighDefRipple ||
			(rs.Flags&state.LsfHighFreeze) != 0 || !rs.HighLimit.IsZero() ||
			rs.Balance.Signum() < 0

		// Record previous reserve state before modifying
		// Reference: rippled SetTrust.cpp lines 636-668
		bLowReserved := (rs.Flags & state.LsfLowReserve) != 0
		bHighReserved := (rs.Flags & state.LsfHighReserve) != 0

		bDefault := !bLowReserveSet && !bHighReserveSet

		if bDefault {
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
			state.DirRemove(ctx.View, lowDirKey, rs.LowNode, trustLineKey.Key, false)
			highDirKey := keylet.OwnerDir(highAccountID)
			state.DirRemove(ctx.View, highDirKey, rs.HighNode, trustLineKey.Key, false)

			// Delete the trust line
			if err := ctx.View.Erase(trustLineKey); err != nil {
				return tx.TefINTERNAL
			}

			// Decrement owner count for both sides that had reserve set
			// Reference: rippled trustDelete() decrements both sides
			if bLowReserved {
				if !bHigh {
					// Low is ctx.Account (transaction sender)
					if ctx.Account.OwnerCount > 0 {
						ctx.Account.OwnerCount--
					}
				} else {
					// Low is the issuer (peer account)
					if issuerAccount.OwnerCount > 0 {
						issuerAccount.OwnerCount--
					}
				}
			}
			if bHighReserved {
				if bHigh {
					// High is ctx.Account (transaction sender)
					if ctx.Account.OwnerCount > 0 {
						ctx.Account.OwnerCount--
					}
				} else {
					// High is the issuer (peer account)
					if issuerAccount.OwnerCount > 0 {
						issuerAccount.OwnerCount--
					}
				}
			}

			// Write issuer account back if its OwnerCount changed
			if (bLowReserved && bHigh) || (bHighReserved && !bHigh) {
				issuerUpdatedData, serErr := state.SerializeAccountRoot(issuerAccount)
				if serErr != nil {
					return tx.TefINTERNAL
				}
				if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
					return tx.TefINTERNAL
				}
			}
		} else {
			// Adjust OwnerCount when reserve flags change
			// Reference: rippled SetTrust.cpp lines 636-668
			bReserveIncrease := false

			// Low account reserve changes
			if bLowReserveSet && !bLowReserved {
				rs.Flags |= state.LsfLowReserve
				if !bHigh {
					// Low is ctx.Account
					ctx.Account.OwnerCount++
					bReserveIncrease = true
				} else {
					// Low is the issuer (peer account)
					issuerAccount.OwnerCount++
				}
			} else if !bLowReserveSet && bLowReserved {
				rs.Flags &^= state.LsfLowReserve
				if !bHigh {
					if ctx.Account.OwnerCount > 0 {
						ctx.Account.OwnerCount--
					}
				} else {
					if issuerAccount.OwnerCount > 0 {
						issuerAccount.OwnerCount--
					}
				}
			}

			// High account reserve changes
			if bHighReserveSet && !bHighReserved {
				rs.Flags |= state.LsfHighReserve
				if bHigh {
					// High is ctx.Account
					ctx.Account.OwnerCount++
					bReserveIncrease = true
				} else {
					// High is the issuer (peer account)
					issuerAccount.OwnerCount++
				}
			} else if !bHighReserveSet && bHighReserved {
				rs.Flags &^= state.LsfHighReserve
				if bHigh {
					if ctx.Account.OwnerCount > 0 {
						ctx.Account.OwnerCount--
					}
				} else {
					if issuerAccount.OwnerCount > 0 {
						issuerAccount.OwnerCount--
					}
				}
			}

			// Check reserve increase affordability
			// Reference: rippled SetTrust.cpp lines 681-688
			if bReserveIncrease {
				reserveCreate := ctx.ReserveForNewObject(ctx.Account.OwnerCount)
				if ctx.Account.Balance < reserveCreate {
					return tx.TecINSUF_RESERVE_LINE
				}
			}

			// Write issuer account back if its OwnerCount changed
			issuerChanged := (bLowReserveSet && !bLowReserved && bHigh) ||
				(!bLowReserveSet && bLowReserved && bHigh) ||
				(bHighReserveSet && !bHighReserved && !bHigh) ||
				(!bHighReserveSet && bHighReserved && !bHigh)
			if issuerChanged {
				issuerUpdatedData, serErr := state.SerializeAccountRoot(issuerAccount)
				if serErr != nil {
					return tx.TefINTERNAL
				}
				if err := ctx.View.Update(issuerKey, issuerUpdatedData); err != nil {
					return tx.TefINTERNAL
				}
			}

			// Update the trust line
			updatedData, err := state.SerializeRippleState(rs)
			if err != nil {
				return tx.TefINTERNAL
			}

			if err := ctx.View.Update(trustLineKey, updatedData); err != nil {
				return tx.TefINTERNAL
			}
		}
	}

	return tx.TesSUCCESS
}
