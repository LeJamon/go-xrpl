// TODO missing sle method related to payment chanel
package paychan

import (
	"encoding/hex"
	"errors"
	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypePaymentChannelFund, func() tx.Transaction {
		return &PaymentChannelFund{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelFund, "")}
	})
}

// PaymentChannelFund adds more XRP to a payment channel.
// Reference: rippled PayChan.cpp PayChanFund
type PaymentChannelFund struct {
	tx.BaseTx

	// Channel is the channel ID (required)
	Channel string `json:"Channel" xrpl:"Channel"`

	// Amount is the amount of XRP to add (required)
	Amount tx.Amount `json:"Amount" xrpl:"Amount,amount"`

	// Expiration is the new expiration time (optional)
	Expiration *uint32 `json:"Expiration,omitempty" xrpl:"Expiration,omitempty"`
}

// NewPaymentChannelFund creates a new PaymentChannelFund transaction
func NewPaymentChannelFund(account, channel string, amount tx.Amount) *PaymentChannelFund {
	return &PaymentChannelFund{
		BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelFund, account),
		Channel: channel,
		Amount:  amount,
	}
}

// TxType returns the transaction type
func (p *PaymentChannelFund) TxType() tx.Type {
	return tx.TypePaymentChannelFund
}

// Validate validates the PaymentChannelFund transaction
// Reference: rippled PayChan.cpp PayChanFund::preflight()
func (p *PaymentChannelFund) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask) - fix1543
	if p.Common.Flags != nil && *p.Common.Flags&tx.TfUniversal != 0 {
		return tx.ErrInvalidFlags
	}

	// Channel is required
	if p.Channel == "" {
		return ErrPayChanChannelRequired
	}

	// Validate Channel is valid hex (256-bit hash)
	channelBytes, err := hex.DecodeString(p.Channel)
	if err != nil || len(channelBytes) != 32 {
		return errors.New("temMALFORMED: Channel must be a valid 256-bit hash")
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

	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *PaymentChannelFund) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

// RequiredAmendments returns the amendments required for this transaction type
func (p *PaymentChannelFund) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
}

// Apply applies a PaymentChannelFund transaction
// Reference: rippled PayChan.cpp PayChanFund::doApply()
func (pf *PaymentChannelFund) Apply(ctx *tx.ApplyContext) tx.Result {
	// Parse channel ID
	channelID, err := hex.DecodeString(pf.Channel)
	if err != nil || len(channelID) != 32 {
		return tx.TemINVALID
	}

	var channelKeyBytes [32]byte
	copy(channelKeyBytes[:], channelID)
	channelKey := keylet.Keylet{Key: channelKeyBytes}

	// Read channel
	channelData, err := ctx.View.Read(channelKey)
	if err != nil || channelData == nil {
		return tx.TecNO_ENTRY
	}

	// Parse channel
	channel, err := sle.ParsePayChannel(channelData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Auto-close check: if CancelAfter or Expiration has passed
	// Reference: rippled PayChan.cpp doApply() lines 345-360
	closeTime := ctx.Config.ParentCloseTime
	if (channel.CancelAfter > 0 && closeTime >= channel.CancelAfter) ||
		(channel.Expiration > 0 && closeTime >= channel.Expiration) {
		return closeChannel(ctx, channelKey, channel)
	}

	// Verify sender is the channel owner
	accountID, _ := sle.DecodeAccountID(pf.Account)
	if channel.Account != accountID {
		return tx.TecNO_PERMISSION
	}

	// Handle Expiration extension
	// Reference: rippled PayChan.cpp doApply() lines 370-381
	if pf.Expiration != nil {
		// minExpiration = closeTime + settleDelay
		minExpiration := closeTime + channel.SettleDelay

		// If channel already has expiration and it's less than minExpiration, use it
		if channel.Expiration > 0 && channel.Expiration < minExpiration {
			minExpiration = channel.Expiration
		}

		// New expiration must be >= minExpiration
		if *pf.Expiration < minExpiration {
			return tx.TemBAD_EXPIRATION
		}

		channel.Expiration = *pf.Expiration
	}

	// Reserve check
	// Reference: rippled PayChan.cpp doApply() lines 383-387
	amount := uint64(pf.Amount.Drops())
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}
	if ctx.Account.Balance-reserve < amount {
		return tx.TecUNFUNDED
	}

	// Destination must still exist
	// Reference: rippled PayChan.cpp doApply() lines 389-390
	destKey := keylet.Account(channel.DestinationID)
	if exists, _ := ctx.View.Exists(destKey); !exists {
		return tx.TecNO_DST
	}

	// Deduct from account and add to channel
	ctx.Account.Balance -= amount
	channel.Amount += amount

	// Serialize updated channel
	updatedChannelData, err := sle.SerializePayChannelFromData(channel)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(channelKey, updatedChannelData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}
