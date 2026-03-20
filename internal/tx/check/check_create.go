package check

import (
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

func init() {
	tx.Register(tx.TypeCheckCreate, func() tx.Transaction {
		return &CheckCreate{BaseTx: *tx.NewBaseTx(tx.TypeCheckCreate, "")}
	})
}

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

// TxType returns the transaction type
func (c *CheckCreate) TxType() tx.Type {
	return tx.TypeCheckCreate
}

// Validate implements preflight validation matching rippled's CreateCheck::preflight().
func (c *CheckCreate) Validate() error {
	if err := c.BaseTx.Validate(); err != nil {
		return err
	}

	// No flags allowed except universal flags
	// Reference: CreateCheck.cpp L41-46
	if err := tx.CheckFlags(c.GetFlags(), tx.TfUniversalMask); err != nil {
		return err
	}

	// Cannot create check to self
	// Reference: CreateCheck.cpp L47-52
	if c.Account == c.Destination {
		return tx.Errorf(tx.TemREDUNDANT, "cannot create check to self")
	}

	// SendMax must be positive
	// Reference: CreateCheck.cpp L55-61
	if c.SendMax.Signum() <= 0 {
		return tx.Errorf(tx.TemBAD_AMOUNT, "SendMax must be positive")
	}

	// Cannot use bad currency (XRP as IOU or null currency)
	// Reference: CreateCheck.cpp L63-67
	if !c.SendMax.IsNative() {
		if c.SendMax.Currency == "XRP" || c.SendMax.Currency == "\x00\x00\x00" || c.SendMax.Currency == "" {
			return tx.Errorf(tx.TemBAD_CURRENCY, "invalid currency")
		}
	}

	// Expiration must not be zero if provided
	// Reference: CreateCheck.cpp L70-77
	if c.Expiration != nil && *c.Expiration == 0 {
		return tx.Errorf(tx.TemBAD_EXPIRATION, "expiration must not be zero")
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (c *CheckCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(c)
}

// RequiredAmendments returns the amendments required for this transaction type
func (c *CheckCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureChecks}
}

// Apply implements preclaim + doApply matching rippled's CreateCheck.
func (c *CheckCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("check create apply",
		"account", c.Account,
		"destination", c.Destination,
		"sendMax", c.SendMax,
	)

	// --- Preclaim checks ---

	// Verify destination exists and is not a pseudo-account
	// Reference: CreateCheck.cpp L85-90, L100-105
	destAccount, destID, result := ctx.LookupDestination(c.Destination)
	if result != tx.TesSUCCESS {
		return result
	}

	// Check DisallowIncoming flag on destination
	// Reference: CreateCheck.cpp L93-98
	rules := ctx.Rules()
	if rules.Enabled(amendment.FeatureDisallowIncoming) {
		if destAccount.Flags&state.LsfDisallowIncomingCheck != 0 {
			return tx.TecNO_PERMISSION
		}
	}

	// Check RequireDestTag on destination
	// Reference: CreateCheck.cpp L107-113
	if destAccount.Flags&state.LsfRequireDestTag != 0 && c.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// IOU-specific checks
	// Reference: CreateCheck.cpp L116-161
	if !c.SendMax.IsNative() {
		issuerID, err := state.DecodeAccountID(c.SendMax.Issuer)
		if err != nil {
			return tx.TefINTERNAL
		}

		// Check global freeze on issuer
		// Reference: CreateCheck.cpp L117-125
		issuerKey := keylet.Account(issuerID)
		issuerData, err := ctx.View.Read(issuerKey)
		if err != nil {
			return tx.TefINTERNAL
		}
		issuerAccount, err := state.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if issuerAccount.Flags&state.LsfGlobalFreeze != 0 {
			return tx.TecFROZEN
		}

		accountID := ctx.AccountID

		// Check source trust line freeze (if source is not issuer)
		// Reference: CreateCheck.cpp L131-145
		if accountID != issuerID {
			if isTrustLineFrozen(ctx, accountID, issuerID, c.SendMax.Currency) {
				return tx.TecFROZEN
			}
		}

		// Check destination trust line freeze (if dest is not issuer)
		// For destination, check if DESTINATION froze their own line (not issuer freeze)
		// Reference: CreateCheck.cpp L146-159
		if destID != issuerID {
			if isTrustLineFrozen(ctx, destID, issuerID, c.SendMax.Currency) {
				return tx.TecFROZEN
			}
		}
	}

	// Check expiration
	// Reference: CreateCheck.cpp L162-166
	if c.Expiration != nil && *c.Expiration <= ctx.Config.ParentCloseTime {
		return tx.TecEXPIRED
	}

	// --- doApply ---

	// Reserve check: account must afford owner count + 1
	// Reference: CreateCheck.cpp L181-186
	if result := ctx.CheckReserveWithFee(ctx.Account.OwnerCount+1, c.Fee); result != tx.TesSUCCESS {
		return result
	}

	// Create the check entry
	accountID := ctx.AccountID
	sequence := c.GetCommon().SeqProxy()

	checkKey := keylet.Check(accountID, sequence)

	// Serialize check
	checkData, err := serializeCheck(c, accountID, destID, sequence, c.SendMax)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert check
	if err := ctx.View.Insert(checkKey, checkData); err != nil {
		return tx.TefINTERNAL
	}

	// Increase owner count
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}

// isTrustLineFrozen checks whether the trust line between accountID and issuerID
// for the given currency is frozen on the account's side.
func isTrustLineFrozen(ctx *tx.ApplyContext, accountID, issuerID [20]byte, currency string) bool {
	tlKey := keylet.Line(accountID, issuerID, currency)
	exists, _ := ctx.View.Exists(tlKey)
	if !exists {
		return false
	}
	tlData, err := ctx.View.Read(tlKey)
	if err != nil {
		return false
	}
	tl, err := state.ParseRippleState(tlData)
	if err != nil {
		return false
	}
	isLow := keylet.IsLowAccount(accountID, issuerID)
	if isLow {
		return tl.Flags&state.LsfHighFreeze != 0
	}
	return tl.Flags&state.LsfLowFreeze != 0
}
