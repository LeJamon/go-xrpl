package check

import (
	"errors"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
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
	if c.GetFlags()&tx.TfUniversalMask != 0 {
		return errors.New("temINVALID_FLAG: invalid flags")
	}

	// Cannot create check to self
	// Reference: CreateCheck.cpp L47-52
	if c.Account == c.Destination {
		return errors.New("temREDUNDANT: cannot create check to self")
	}

	// SendMax must be positive
	// Reference: CreateCheck.cpp L55-61
	if c.SendMax.Signum() <= 0 {
		return errors.New("temBAD_AMOUNT: SendMax must be positive")
	}

	// Cannot use bad currency (XRP as IOU or null currency)
	// Reference: CreateCheck.cpp L63-67
	if !c.SendMax.IsNative() {
		if c.SendMax.Currency == "XRP" || c.SendMax.Currency == "\x00\x00\x00" || c.SendMax.Currency == "" {
			return errors.New("temBAD_CURRENCY: invalid currency")
		}
	}

	// Expiration must not be zero if provided
	// Reference: CreateCheck.cpp L70-77
	if c.Expiration != nil && *c.Expiration == 0 {
		return errors.New("temBAD_EXPIRATION: expiration must not be zero")
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
	// --- Preclaim checks ---

	// Verify destination exists
	// Reference: CreateCheck.cpp L85-90
	destID, err := sle.DecodeAccountID(c.Destination)
	if err != nil {
		return tx.TemINVALID
	}

	destKey := keylet.Account(destID)
	destData, err := ctx.View.Read(destKey)
	if err != nil || destData == nil {
		return tx.TecNO_DST
	}

	destAccount, err := sle.ParseAccountRoot(destData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Check DisallowIncoming flag on destination
	// Reference: CreateCheck.cpp L93-98
	rules := ctx.Rules()
	if rules.Enabled(amendment.FeatureDisallowIncoming) {
		if destAccount.Flags&sle.LsfDisallowIncomingCheck != 0 {
			return tx.TecNO_PERMISSION
		}
	}

	// Check RequireDestTag on destination
	// Reference: CreateCheck.cpp L107-113
	if destAccount.Flags&sle.LsfRequireDestTag != 0 && c.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// IOU-specific checks
	// Reference: CreateCheck.cpp L116-161
	if !c.SendMax.IsNative() {
		issuerID, err := sle.DecodeAccountID(c.SendMax.Issuer)
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
		issuerAccount, err := sle.ParseAccountRoot(issuerData)
		if err != nil {
			return tx.TefINTERNAL
		}
		if issuerAccount.Flags&sle.LsfGlobalFreeze != 0 {
			return tx.TecFROZEN
		}

		accountID := ctx.AccountID

		// Check source trust line freeze (if source is not issuer)
		// Reference: CreateCheck.cpp L131-145
		if accountID != issuerID {
			srcTLKey := keylet.Line(accountID, issuerID, c.SendMax.Currency)
			srcTLExists, _ := ctx.View.Exists(srcTLKey)
			if srcTLExists {
				srcTLData, err := ctx.View.Read(srcTLKey)
				if err == nil {
					srcTL, err := sle.ParseRippleState(srcTLData)
					if err == nil {
						srcIsLow := keylet.IsLowAccount(accountID, issuerID)
						if srcIsLow {
							if srcTL.Flags&sle.LsfHighFreeze != 0 {
								return tx.TecFROZEN
							}
						} else {
							if srcTL.Flags&sle.LsfLowFreeze != 0 {
								return tx.TecFROZEN
							}
						}
					}
				}
			}
		}

		// Check destination trust line freeze (if dest is not issuer)
		// For destination, check if DESTINATION froze their own line (not issuer freeze)
		// Reference: CreateCheck.cpp L146-159
		if destID != issuerID {
			dstTLKey := keylet.Line(destID, issuerID, c.SendMax.Currency)
			dstTLExists, _ := ctx.View.Exists(dstTLKey)
			if dstTLExists {
				dstTLData, err := ctx.View.Read(dstTLKey)
				if err == nil {
					dstTL, err := sle.ParseRippleState(dstTLData)
					if err == nil {
						dstIsLow := keylet.IsLowAccount(destID, issuerID)
						// Check if the destination froze their own side
						if dstIsLow {
							if dstTL.Flags&sle.LsfLowFreeze != 0 {
								return tx.TecFROZEN
							}
						} else {
							if dstTL.Flags&sle.LsfHighFreeze != 0 {
								return tx.TecFROZEN
							}
						}
					}
				}
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
	// Use prior balance (before fee deduction) as rippled uses mPriorBalance
	feeDrops := parseFee(c.Fee)
	priorBalance := ctx.Account.Balance + feeDrops
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
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
