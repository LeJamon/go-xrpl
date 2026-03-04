package paychan

import (
	"encoding/hex"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypePaymentChannelCreate, func() tx.Transaction {
		return &PaymentChannelCreate{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelCreate, "")}
	})
}

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

// TxType returns the transaction type
func (p *PaymentChannelCreate) TxType() tx.Type {
	return tx.TypePaymentChannelCreate
}

// Validate validates the PaymentChannelCreate transaction
// Reference: rippled PayChan.cpp PayChanCreate::preflight()
func (p *PaymentChannelCreate) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// Check for invalid flags (tfUniversalMask) - fix1543
	if p.Common.Flags != nil && *p.Common.Flags&tx.TfUniversal != 0 {
		return tx.ErrInvalidFlags
	}

	// Destination is required
	if p.Destination == "" {
		return ErrPayChanDestRequired
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
	if p.Account == p.Destination {
		return ErrPayChanDestIsSrc
	}

	// PublicKey is required and must be valid
	if p.PublicKey == "" {
		return ErrPayChanPublicKeyRequired
	}

	// Validate PublicKey is valid hex, proper length, and valid prefix
	// Reference: rippled PayChan.cpp preflight() publicKeyType()
	pkBytes, err := hex.DecodeString(p.PublicKey)
	if err != nil {
		return ErrPayChanPublicKeyInvalid
	}
	if len(pkBytes) != 33 && len(pkBytes) != 65 {
		return ErrPayChanPublicKeyInvalid
	}
	// Check prefix byte: 0x02 or 0x03 for secp256k1, 0xED for ed25519
	if len(pkBytes) == 33 {
		if pkBytes[0] != 0x02 && pkBytes[0] != 0x03 && pkBytes[0] != 0xED {
			return ErrPayChanPublicKeyInvalid
		}
	} else if len(pkBytes) == 65 {
		if pkBytes[0] != 0x04 {
			return ErrPayChanPublicKeyInvalid
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *PaymentChannelCreate) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

// RequiredAmendments returns the amendments required for this transaction type
func (p *PaymentChannelCreate) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
}

// Apply applies a PaymentChannelCreate transaction
// Reference: rippled PayChan.cpp PayChanCreate::doApply()
func (pc *PaymentChannelCreate) Apply(ctx *tx.ApplyContext) tx.Result {
	amount := uint64(pc.Amount.Drops())

	// Verify destination exists
	destID, err := sle.DecodeAccountID(pc.Destination)
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

	// DisallowIncoming check
	// Reference: rippled PayChan.cpp preclaim() featureDisallowIncoming
	if ctx.Rules().Enabled(amendment.FeatureDisallowIncoming) {
		if destAccount.Flags&sle.LsfDisallowIncomingPayChan != 0 {
			return tx.TecNO_PERMISSION
		}
	}

	// RequireDestTag check
	// Reference: rippled PayChan.cpp preclaim() lsfRequireDestTag
	if (destAccount.Flags&sle.LsfRequireDestTag) != 0 && pc.DestinationTag == nil {
		return tx.TecDST_TAG_NEEDED
	}

	// DisallowXRP check (only when DepositAuth amendment is NOT enabled — bug compat)
	// Reference: rippled PayChan.cpp preclaim() lsfDisallowXRP
	if !ctx.Rules().Enabled(amendment.FeatureDepositAuth) {
		if destAccount.Flags&sle.LsfDisallowXRP != 0 {
			return tx.TecNO_TARGET
		}
	}

	// Reserve check
	// Reference: rippled PayChan.cpp preclaim() balance < reserve, balance - reserve < amount
	reserve := ctx.AccountReserve(ctx.Account.OwnerCount + 1)
	if ctx.Account.Balance < reserve {
		return tx.TecINSUFFICIENT_RESERVE
	}
	if ctx.Account.Balance-reserve < amount {
		return tx.TecUNFUNDED
	}

	// fixPayChanCancelAfter: CancelAfter must be in the future
	// Reference: rippled PayChan.cpp doApply() fixPayChanCancelAfter
	if ctx.Rules().Enabled(amendment.FeatureFixPayChanCancelAfter) {
		if pc.CancelAfter != nil {
			closeTime := ctx.Config.ParentCloseTime
			if closeTime > *pc.CancelAfter {
				return tx.TecEXPIRED
			}
		}
	}

	// Create pay channel
	accountID, _ := sle.DecodeAccountID(pc.Account)
	sequence := pc.GetCommon().SeqProxy()
	channelKey := keylet.PayChannel(accountID, destID, sequence)

	// Serialize pay channel SLE
	channelData, err := serializePayChannel(pc, accountID, destID, amount)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Insert channel
	if err := ctx.View.Insert(channelKey, channelData); err != nil {
		return tx.TefINTERNAL
	}

	// DirInsert into owner directory
	// Reference: rippled PayChan.cpp doApply() dirAdd(ownerDir)
	ownerDirKey := keylet.OwnerDir(accountID)
	ownerResult, err := sle.DirInsert(ctx.View, ownerDirKey, channelKey.Key, func(dir *sle.DirectoryNode) {
		dir.Owner = accountID
	})
	if err != nil {
		return tx.TecDIR_FULL
	}

	// Re-read and update channel with OwnerNode from DirInsert
	channelSLE, err := sle.ParsePayChannel(channelData)
	if err != nil {
		return tx.TefINTERNAL
	}
	channelSLE.OwnerNode = ownerResult.Page

	// DirInsert into destination directory (if fixPayChanRecipientOwnerDir enabled)
	// Reference: rippled PayChan.cpp doApply() fixPayChanRecipientOwnerDir
	if ctx.Rules().Enabled(amendment.FeatureFixPayChanRecipientOwnerDir) {
		destDirKey := keylet.OwnerDir(destID)
		destResult, err := sle.DirInsert(ctx.View, destDirKey, channelKey.Key, func(dir *sle.DirectoryNode) {
			dir.Owner = destID
		})
		if err != nil {
			return tx.TecDIR_FULL
		}
		channelSLE.DestinationNode = destResult.Page
		channelSLE.HasDestNode = true
	}

	// Re-serialize with updated OwnerNode/DestinationNode
	updatedData, err := sle.SerializePayChannelFromData(channelSLE)
	if err != nil {
		return tx.TefINTERNAL
	}
	if err := ctx.View.Update(channelKey, updatedData); err != nil {
		return tx.TefINTERNAL
	}

	// Deduct amount from account and increment OwnerCount
	ctx.Account.Balance -= amount
	ctx.Account.OwnerCount++

	return tx.TesSUCCESS
}
