package paychan

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// PaymentChannelCreate creates a payment channel.
// Reference: rippled PayChan.cpp PayChanCreate
type PaymentChannelCreate struct {
	tx.BaseTx

	// Amount is the amount of XRP to lock in the channel (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Destination is the account to receive channel payments (required)
	Destination string `json:"Destination" xrpl:"Destination"`

	// SettleDelay is the time in seconds to wait after close (required)
	SettleDelay uint32 `json:"SettleDelay" xrpl:"SettleDelay"`

	// PublicKey is the public key for verifying claims (required)
	PublicKey string `json:"PublicKey" xrpl:"PublicKey"`

	// CancelAfter is the time when the channel expires (optional)
	CancelAfter *uint32 `json:"CancelAfter,omitempty" xrpl:"CancelAfter,omitempty"`

	// DestinationTag is an arbitrary tag for the destination (optional)
	DestinationTag *uint32 `json:"DestinationTag,omitempty" xrpl:"DestinationTag,omitempty"`

	// SourceTag is an optional tag for the source (optional)
	SourceTag *uint32 `json:"SourceTag,omitempty" xrpl:"SourceTag,omitempty"`
}

// NewPaymentChannelCreate creates a new PaymentChannelCreate transaction
func NewPaymentChannelCreate(account, destination string, amount tx.Amount, settleDelay uint32, publicKey string) *PaymentChannelCreate {
	return &PaymentChannelCreate{
		BaseTx:      *tx.NewBaseTx(tx.TypePaymentChannelCreate, account),
		Amount:      amount,
		Destination: destination,
		SettleDelay: settleDelay,
		PublicKey:   publicKey,
	}
}

func (p *PaymentChannelCreate) TxType() tx.Type {
	return tx.TypePaymentChannelCreate
}

// Reference: rippled PayChan.cpp PayChanCreate::preflight()
func (p *PaymentChannelCreate) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// The tfUniversalMask flag check is gated on fix1543 and runs in Preclaim,
	// where the amendment rules are available.

	// Destination is required
	if err := tx.CheckDestRequired(p.Destination); err != nil {
		return err
	}

	// Amount is required and must be XRP
	if p.Amount.IsZero() {
		return ErrPayChanAmountRequired
	}

	if !p.Amount.IsNative() {
		return ErrPayChanAmountNotXRP
	}

	// Amount must be positive
	if p.Amount.Drops() <= 0 {
		return ErrPayChanAmountNotPositive
	}

	// Cannot create channel to self
	if err := tx.CheckDestNotSrc(p.Account, p.Destination); err != nil {
		return err
	}

	// PublicKey is required and must be valid
	if p.PublicKey == "" {
		return ErrPayChanPublicKeyRequired
	}

	// Validate PublicKey is valid hex with the type rippled's publicKeyType()
	// accepts: 33 bytes prefixed 0xED / 0x02 / 0x03.
	// Reference: rippled PayChan.cpp preflight() publicKeyType()
	pkBytes, err := hex.DecodeString(p.PublicKey)
	if err != nil || !tx.IsValidPublicKey(pkBytes) {
		return ErrPayChanPublicKeyInvalid
	}

	return nil
}

func (p *PaymentChannelCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

func (p *PaymentChannelCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
}

// GetFlagsMask returns the invalid-flags mask enforced at preflight0: any
// non-universal flag is rejected.
// Reference: rippled PayChan.cpp PayChanCreate::getFlagsMask.
func (p *PaymentChannelCreate) GetFlagsMask(rules *amendment.Rules) uint32 {
	return tx.TfUniversalMask
}

// Reference: rippled PayChan.cpp PayChanCreate::doApply()
func (p *PaymentChannelCreate) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("payment channel create apply",
		"account", p.Account,
		"destination", p.Destination,
		"amount", p.Amount,
		"settleDelay", p.SettleDelay,
	)

	amount := uint64(p.Amount.Drops())

	// Reserve and funding checks run before the destination checks, matching
	// rippled's preclaim order, which reads the PRE-fee balance — so use
	// PriorBalance, else a fee straddling reserve(OwnerCount+1) flips the TER.
	// Reference: rippled PayChan.cpp preclaim() lines 204-214.
	priorBalance := ctx.PriorBalance()
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if priorBalance < reserve {
		ctx.Log.Warn("payment channel create: insufficient reserve",
			"balance", priorBalance,
			"reserve", reserve,
		)
		return ter.TecINSUFFICIENT_RESERVE
	}
	if priorBalance-reserve < amount {
		ctx.Log.Warn("payment channel create: unfunded",
			"balance", priorBalance,
			"needed", reserve+amount,
		)
		return ter.TecUNFUNDED
	}

	// Verify destination exists and is not a pseudo-account (AMM)
	// Reference: rippled PayChan.cpp preclaim() lines 216-248
	destAccount, destID, result := ctx.LookupDestination(p.Destination)
	if result != ter.TesSUCCESS {
		ctx.Log.Warn("payment channel create: destination lookup failed",
			"destination", p.Destination,
			"result", result,
		)
		return result
	}

	// DisallowIncoming check
	// Reference: rippled PayChan.cpp preclaim() lsfDisallowIncomingPayChan
	if destAccount.Flags&state.LsfDisallowIncomingPayChan != 0 {
		ctx.Log.Warn("payment channel create: destination disallows incoming pay channels",
			"destination", p.Destination,
		)
		return ter.TecNO_PERMISSION
	}

	// RequireDestTag check
	// Reference: rippled PayChan.cpp preclaim() lsfRequireDestTag
	if (destAccount.Flags&state.LsfRequireDestTag) != 0 && p.DestinationTag == nil {
		ctx.Log.Warn("payment channel create: destination tag required",
			"destination", p.Destination,
		)
		return ter.TecDST_TAG_NEEDED
	}

	// fixPayChanCancelAfter: CancelAfter must be in the future
	// Reference: rippled PayChan.cpp doApply() fixPayChanCancelAfter
	if ctx.Rules().Enabled(amendment.FeatureFixPayChanCancelAfter) {
		if p.CancelAfter != nil {
			closeTime := ctx.Config.ParentCloseTime
			if closeTime > *p.CancelAfter {
				return ter.TecEXPIRED
			}
		}
	}

	accountID, _ := state.DecodeAccountID(p.Account)
	sequence := p.GetCommon().SeqProxy()
	channelKey := keylet.PayChannel(accountID, destID, sequence)

	// Build the channel SLE and insert its initial (pre-directory) form; the
	// directory page fields are filled in and the entry re-serialized below.
	channelSLE := newPayChannelData(p, accountID, destID, amount)
	channelData, err := state.SerializePayChannelFromData(channelSLE)
	if err != nil {
		return ctx.Internal("SerializePayChannelFromData", err)
	}

	// Insert channel
	if err := ctx.View.Insert(channelKey, channelData); err != nil {
		return ctx.Internal("insert pay channel", err)
	}

	// DirInsert into owner directory
	// Reference: rippled PayChan.cpp doApply() dirAdd(ownerDir)
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerResult, err := state.DirInsert(ctx.View, ownerDirKey, channelKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		ctx.Log.Error("payment channel create: owner directory full", "error", err)
		return ter.TecDIR_FULL
	}

	channelSLE.OwnerNode = ownerResult.Page

	// fixIncludeKeyletFields: store the creating sequence (tx or ticket) used
	// to derive the channel keylet.
	if ctx.Rules().Enabled(amendment.FeatureFixIncludeKeyletFields) {
		channelSLE.Sequence = sequence
		channelSLE.HasSequence = true
	}

	// DirInsert into the recipient's owner directory.
	// Reference: rippled PayChan.cpp doApply() — recipient owner directory.
	destDirKey := keylet.OwnerDir(destID)
	destResult, err := state.DirInsert(ctx.View, destDirKey, channelKey.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = destID
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	channelSLE.DestinationNode = destResult.Page
	channelSLE.HasDestNode = true

	// Re-serialize with updated OwnerNode/DestinationNode
	updatedData, err := state.SerializePayChannelFromData(channelSLE)
	if err != nil {
		return ctx.Internal("SerializePayChannelFromData", err)
	}
	if err := ctx.View.Update(channelKey, updatedData); err != nil {
		return ctx.Internal("update pay channel", err)
	}

	// Deduct amount from account and increment OwnerCount
	ctx.Account.Balance -= amount
	ctx.Account.OwnerCount++

	return ter.TesSUCCESS
}
