package check

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

type CheckCreate struct {
	tx.BaseTx

	// Destination is the account that can cash the check (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// SendMax is the maximum amount that can be debited from the sender (required)
	SendMax tx.Amount `json:"SendMax" xrpl:"SendMax,amount"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// Expiration is the time when the check expires (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`

	// InvoiceID is a 256-bit hash for identifying this check (optional)
	InvoiceID string `json:"InvoiceID,omitempty" xrpl:"InvoiceID,omitempty"`
}

// NewCheckCreate creates a new CheckCreate transaction
func NewCheckCreate(account, destination string, sendMax tx.Amount) *CheckCreate {
	return &CheckCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, account),
		Destination: destination,
		SendMax:     sendMax,
	}
}

func (c *CheckCreate) TxType() tx.Type {
	return tx.TypeCheckCreate
}

// Validate implements preflight validation matching rippled's CreateCheck::preflight().
// GetFlagsMask adopts the engine FlagsMasker seam. CreateCheck defines no
// type-specific flags, so it uses the base universal mask, checked at preflight0.
func (c *CheckCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

func (c *CheckCreate) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	if c.Destination == "" {
		return ter.Errorf(ter.TemDST_NEEDED, "Destination is required")
	}

	// Cannot create check to self
	// Reference: CreateCheck.cpp L47-52
	if c.Account == c.Destination {
		return ter.Errorf(ter.TemREDUNDANT, "cannot create check to self")
	}

	// SendMax must be positive
	// Reference: CreateCheck.cpp L55-61
	if c.SendMax.Signum() <= 0 {
		return ter.Errorf(ter.TemBAD_AMOUNT, "SendMax must be positive")
	}

	if badMPTAsset(c.SendMax) {
		return ter.Errorf(ter.TemBAD_CURRENCY, "invalid MPT issuance")
	}

	// Cannot use bad currency (XRP as IOU or null currency)
	// Reference: CreateCheck.cpp L63-67
	if !c.SendMax.IsNative() && !c.SendMax.IsMPT() {
		if c.SendMax.Currency == "XRP" || c.SendMax.Currency == "\x00\x00\x00" || c.SendMax.Currency == "" {
			return ter.Errorf(ter.TemBAD_CURRENCY, "invalid currency")
		}
	}

	// Expiration must not be zero if provided
	// Reference: CreateCheck.cpp L70-77
	if c.Expiration != nil && *c.Expiration == 0 {
		return ter.Errorf(ter.TemBAD_EXPIRATION, "expiration must not be zero")
	}

	return nil
}

func badMPTAsset(amount tx.Amount) bool {
	if !amount.IsMPT() {
		return false
	}
	id, err := mptutil.DecodeID(amount.MPTIssuanceID())
	return err != nil || mptutil.Issuer(id) == ([20]byte{})
}

func (c *CheckCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

func (c *CheckCreate) RequiredAmendments() [][32]byte {
	return nil
}

// CheckExtraFeatures gates an MPT-denominated SendMax on the MPTokensV2
// amendment, mirroring rippled CheckCreate::checkExtraFeatures. Runs before the
// common preflight, so an MPT SendMax without the amendment surfaces temDISABLED.
func (c *CheckCreate) CheckExtraFeatures(rules *amendment.Rules) error {
	if !rules.MPTokensV2Enabled() && c.SendMax.IsMPT() {
		return ter.Errorf(ter.TemDISABLED, "MPT SendMax requires MPTokensV2 amendment")
	}
	return nil
}

// Apply implements preclaim + doApply matching rippled's CreateCheck.
func (c *CheckCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("check create apply",
		"account", c.Account,
		"destination", c.Destination,
		"sendMax", c.SendMax,
	)

	// --- Preclaim checks ---

	// Verify destination exists and is not a pseudo-account
	// Reference: CreateCheck.cpp L85-90, L100-105
	destAccount, destID, result := ctx.LookupDestination(c.Destination)
	if result != ter.TesSUCCESS {
		return result
	}

	// Check DisallowIncoming flag on destination
	// Reference: CreateCheck.cpp L93-98
	if destAccount.Flags&state.LsfDisallowIncomingCheck != 0 {
		return ter.TecNO_PERMISSION
	}

	// Check RequireDestTag on destination
	// Reference: CreateCheck.cpp L107-113
	if destAccount.Flags&state.LsfRequireDestTag != 0 && c.DestinationTag == nil {
		return ter.TecDST_TAG_NEEDED
	}

	if c.SendMax.IsMPT() {
		mptID, err := mptutil.DecodeID(c.SendMax.MPTIssuanceID())
		if err != nil {
			return ter.TecOBJECT_NOT_FOUND
		}
		accountID := ctx.AccountID
		issuerID := mptutil.Issuer(mptID)

		for _, holderID := range [][20]byte{accountID, destID} {
			if holderID == issuerID {
				continue
			}
			if _, _, result := mptutil.ReadHolding(ctx.View, mptID, holderID); result != ter.TesSUCCESS && result != ter.TecNO_AUTH {
				return result
			}
		}
		if mptutil.IsGlobalFrozen(ctx.View, mptID) ||
			(accountID != issuerID && mptutil.IsFrozen(ctx.View, mptID, accountID)) ||
			(destID != issuerID && mptutil.IsFrozen(ctx.View, mptID, destID)) {
			return ter.TecLOCKED
		}
		if result := mptutil.CanTransfer(ctx.View, mptID, accountID, destID); result != ter.TesSUCCESS {
			return result
		}
	} else if !c.SendMax.IsNative() {
		// Reference: CreateCheck.cpp L116-161
		issuerID, err := state.DecodeAccountID(c.SendMax.Issuer)
		if err != nil {
			return ter.TefINTERNAL
		}

		// Check global freeze on issuer
		// Reference: CreateCheck.cpp L117-125
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil {
			return ter.TefINTERNAL
		}
		if issuerData != nil {
			issuerAccount, err := state.ParseAccountRoot(issuerData)
			if err != nil {
				return ter.TefINTERNAL
			}
			if issuerAccount.Flags&state.LsfGlobalFreeze != 0 {
				return ter.TecFROZEN
			}
		}

		accountID := ctx.AccountID

		// Check source trust line freeze (if source is not issuer): the issuer's
		// freeze of the source side blocks the source from sending. This is the
		// shared issuer-side individual freeze check.
		// Reference: CreateCheck.cpp L131-145
		frozen, err := isTrustLineFrozenByIssuer(ctx.View, accountID, issuerID, c.SendMax.Currency)
		if err != nil {
			return ter.TefINTERNAL
		}
		if frozen {
			return ter.TecFROZEN
		}

		// Check destination trust line freeze (if dest is not issuer): check if
		// the destination froze their own side (not issuer freeze).
		// Reference: CreateCheck.cpp L146-159
		frozen, err = isTrustLineFrozenBySelf(ctx.View, destID, issuerID, c.SendMax.Currency)
		if err != nil {
			return ter.TefINTERNAL
		}
		if frozen {
			return ter.TecFROZEN
		}
	}

	// Check expiration
	// Reference: CreateCheck.cpp L162-166
	if tx.HasExpired(c.Expiration, ctx.Config.ParentCloseTime) {
		return ter.TecEXPIRED
	}

	// --- doApply ---

	// Reserve check: account must afford owner count + 1
	// Reference: CreateCheck.cpp L181-186
	if result := ctx.CheckReserveWithFee(ctx.Account.OwnerCount + 1); result != ter.TesSUCCESS {
		return result
	}

	// Create the check entry
	accountID := ctx.AccountID
	sequence := c.GetCommon().SeqProxy()

	checkKey := keylet.Check(accountID, sequence)

	// Build the check SLE and insert its initial (pre-directory) form; the
	// directory page fields are filled in and the entry re-serialized below.
	checkSLE := newCheckData(c, accountID, destID, sequence, c.SendMax)
	checkData, err := state.SerializeCheckFromData(checkSLE)
	if err != nil {
		return ctx.Internal("SerializeCheckFromData", err)
	}

	// Insert check
	if err := ctx.View.Insert(checkKey, checkData); err != nil {
		return ctx.Internal("insert check", err)
	}

	// Insert check into destination's owner directory (not self-send).
	// Reference: CreateCheck.cpp L213-228
	if destID != accountID {
		destDirKey := keylet.OwnerDir(destID)
		destResult, err := state.DirInsert(ctx.View, destDirKey, checkKey.Key, false, func(dir *state.DirectoryNode) {
			dir.Owner = destID
		})
		if err != nil {
			ctx.Log.Error("check create: destination directory full", "error", err)
			return ter.TecDIR_FULL
		}
		checkSLE.DestinationNode = destResult.Page
		checkSLE.HasDestNode = true
	}

	// Insert check into owner's owner directory.
	// Reference: CreateCheck.cpp L230-244
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerResult, err := state.DirInsert(ctx.View, ownerDirKey, checkKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		ctx.Log.Error("check create: owner directory full", "error", err)
		return ter.TecDIR_FULL
	}
	checkSLE.OwnerNode = ownerResult.Page

	// Re-serialize check with updated OwnerNode/DestinationNode
	updatedData, err := state.SerializeCheckFromData(checkSLE)
	if err != nil {
		return ctx.Internal("SerializeCheckFromData", err)
	}
	if err := ctx.View.Update(checkKey, updatedData); err != nil {
		return ctx.Internal("update check", err)
	}

	// Increase owner count
	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}

// isTrustLineFrozenBySelf reports whether the trust line between account and
// issuer is frozen on the account's own side. Returns false when account ==
// issuer, the line does not exist, or the line cannot be read or parsed.
func isTrustLineFrozenByIssuer(view tx.LedgerView, accountID, issuerID [20]byte, currency string) (bool, error) {
	if accountID == issuerID {
		return false, nil
	}
	tl, err := tx.ReadRippleState(view, accountID, issuerID, currency)
	if err != nil || tl == nil {
		return false, err
	}
	freezeFlag := state.LsfLowFreeze
	if keylet.IsLowAccount(accountID, issuerID) {
		freezeFlag = state.LsfHighFreeze
	}
	return tl.Flags&freezeFlag != 0, nil
}

func isTrustLineFrozenBySelf(view tx.LedgerView, accountID, issuerID [20]byte, currency string) (bool, error) {
	if accountID == issuerID {
		return false, nil
	}
	tl, err := tx.ReadRippleState(view, accountID, issuerID, currency)
	if err != nil || tl == nil {
		return false, err
	}
	freezeFlag := state.LsfLowFreeze
	if !keylet.IsLowAccount(accountID, issuerID) {
		freezeFlag = state.LsfHighFreeze
	}
	return tl.Flags&freezeFlag != 0, nil
}
