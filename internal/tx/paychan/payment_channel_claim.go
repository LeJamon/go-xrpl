package paychan

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
)

// PaymentChannelClaim claims XRP from a payment channel.
// Reference: rippled PayChan.cpp PayChanClaim
type PaymentChannelClaim struct {
	tx.BaseTx

	// Channel is the channel ID (required)
	Channel string `json:"Channel" xrpl:"Channel"`

	// Balance is the total amount delivered by this channel (optional)
	Balance *tx.Amount `json:"Balance,omitempty" xrpl:"Balance,omitempty,amount"`

	// Amount is the amount of XRP authorized by the signature (optional)
	Amount *tx.Amount `json:"Amount,omitempty" xrpl:"Amount,omitempty,amount"`

	// Signature is the signature for this claim (optional)
	Signature string `json:"Signature,omitempty" xrpl:"Signature,omitempty"`

	// PublicKey is the public key for verifying the signature (optional)
	PublicKey string `json:"PublicKey,omitempty" xrpl:"PublicKey,omitempty"`

	// CredentialIDs is the list of credential hashes for deposit preauth (optional)
	CredentialIDs []string `json:"CredentialIDs,omitempty" xrpl:"CredentialIDs,omitempty"`
}

// NewPaymentChannelClaim creates a new PaymentChannelClaim transaction
func NewPaymentChannelClaim(account, channel string) *PaymentChannelClaim {
	return &PaymentChannelClaim{
		BaseTx:  *tx.NewBaseTx(tx.TypePaymentChannelClaim, account),
		Channel: channel,
	}
}

func (p *PaymentChannelClaim) TxType() tx.Type {
	return tx.TypePaymentChannelClaim
}

// Reference: rippled PayChan.cpp PayChanClaim::preflight()
func (p *PaymentChannelClaim) Validate() error {
	if err := p.BaseTx.Validate(); err != nil {
		return err
	}

	// Channel is required
	if p.Channel == "" {
		return ErrPayChanChannelRequired
	}

	// Validate Channel is valid hex (256-bit hash)
	channelBytes, err := hex.DecodeString(p.Channel)
	if err != nil || len(channelBytes) != 32 {
		return ter.Errorf(ter.TemMALFORMED, "Channel must be a valid 256-bit hash")
	}

	// Validate Balance if present.
	if p.Balance != nil {
		if !p.Balance.IsNative() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Balance must be XRP")
		}
		if p.Balance.Drops() <= 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Balance must be positive")
		}
	}

	// Validate Amount if present.
	if p.Amount != nil {
		if !p.Amount.IsNative() {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be XRP")
		}
		if p.Amount.Drops() <= 0 {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
		}
	}

	// Balance cannot exceed Amount.
	if p.Balance != nil && p.Amount != nil && p.Balance.Drops() > p.Amount.Drops() {
		return ErrPayChanBalanceGTAmount
	}

	// Cannot set both tfClose and tfRenew. rippled checks this AFTER the
	// Balance/Amount validity, so a bad amount surfaces temBAD_AMOUNT first.
	// Reference: rippled PayChan.cpp PayChanClaim::preflight (flag block).
	flags := p.GetFlags()
	if (flags&tfPayChanClose != 0) && (flags&tfPayChanRenew != 0) {
		return ErrPayChanCloseAndRenew
	}

	// If Signature is provided, PublicKey and Balance must also be provided, and
	// the claim signature is verified here — entirely from tx fields, before any
	// ledger access. rippled runs this whole block before the CredentialIDs shape
	// check. Reference: rippled PayChan.cpp PayChanClaim::preflight (Signature block).
	if p.Signature != "" {
		if p.PublicKey == "" {
			return ErrPayChanSigNeedsKey
		}
		if p.Balance == nil {
			return ErrPayChanSigNeedsBalance
		}

		// Authorized amount: Amount if present, else Balance. Balance may not exceed it.
		authAmt := p.Balance.Drops()
		if p.Amount != nil {
			authAmt = p.Amount.Drops()
		}
		if p.Balance.Drops() > authAmt {
			return ter.Errorf(ter.TemBAD_AMOUNT, "Balance exceeds authorized amount")
		}

		// PublicKey must be a 33-byte key prefixed 0xED / 0x02 / 0x03.
		pkBytes, err := hex.DecodeString(p.PublicKey)
		if err != nil || !tx.IsValidPublicKey(pkBytes) {
			return ErrPayChanPublicKeyInvalid
		}

		if !verifyClaimSignature(p.Channel, uint64(authAmt), p.PublicKey, p.Signature) {
			return ter.Errorf(ter.TemBAD_SIGNATURE, "invalid claim signature")
		}
	}

	// CredentialIDs shape check runs LAST in rippled's PayChanClaim::preflight,
	// after the Signature block. Use HasField to detect an empty array that binary
	// parsing leaves as a nil Go slice. Reference: rippled credentials::checkFields.
	present := p.CredentialIDs != nil || p.HasField("CredentialIDs")
	if err := credential.CheckFields(p.CredentialIDs, present, "duplicates in credentials"); err != nil {
		return err
	}

	return nil
}

func (p *PaymentChannelClaim) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

func (p *PaymentChannelClaim) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
}

// tfPayChanClaimMask is the invalid-flags mask for a claim once fix1543 is
// active: everything except the universal flags plus tfRenew/tfClose.
const tfPayChanClaimMask = ^(tfPayChanRenew | tfPayChanClose | tx.TfUniversal)

// GetFlagsMask returns the invalid-flags mask enforced at preflight0. fix1543
// rejects any flag outside tfPayChanClaimMask; before it, any flags are allowed.
// Reference: rippled PayChan.cpp PayChanClaim::getFlagsMask.
func (p *PaymentChannelClaim) GetFlagsMask(rules *amendment.Rules) uint32 {
	if rules.Enabled(amendment.FeatureFix1543) {
		return tfPayChanClaimMask
	}
	return 0
}

// CheckExtraFeatures gates the CredentialIDs field on the Credentials amendment.
// rippled evaluates this in checkExtraFeatures — before preflight1 and the
// tx-type preflight body — so a CredentialIDs-bearing claim on a network without
// Credentials is temDISABLED ahead of every other TER, keyed on field presence.
// Reference: rippled PayChan.cpp PayChanClaim::checkExtraFeatures.
func (p *PaymentChannelClaim) CheckExtraFeatures(rules *amendment.Rules) error {
	present := p.CredentialIDs != nil || p.HasField("CredentialIDs")
	if present && !rules.Enabled(amendment.FeatureCredentials) {
		return ter.Errorf(ter.TemDISABLED, "Credentials amendment not enabled")
	}
	return nil
}

// SetClose sets the close flag
func (p *PaymentChannelClaim) SetClose() {
	flags := p.GetFlags() | tfPayChanClose
	p.SetFlags(flags)
}

// SetRenew sets the renew flag
func (p *PaymentChannelClaim) SetRenew() {
	flags := p.GetFlags() | tfPayChanRenew
	p.SetFlags(flags)
}

// IsClose returns true if the close flag is set
func (p *PaymentChannelClaim) IsClose() bool {
	return p.GetFlags()&tfPayChanClose != 0
}

// IsRenew returns true if the renew flag is set
func (p *PaymentChannelClaim) IsRenew() bool {
	return p.GetFlags()&tfPayChanRenew != 0
}

// Reference: rippled PayChan.cpp PayChanClaim::preclaim() + doApply()
func (p *PaymentChannelClaim) Apply(ctx *tx.ApplyContext) ter.Result {
	ctx.Log.Trace("payment channel claim apply",
		"account", p.Account,
		"channel", p.Channel,
		"amount", p.Amount,
		"balance", p.Balance,
		"flags", p.GetFlags(),
	)

	rules := ctx.Rules()

	// The CredentialIDs amendment gate (temDISABLED, keyed on field presence) runs
	// in CheckExtraFeatures; the shape check runs last in Validate.

	// Reference: rippled PayChan.cpp PayChanClaim::preclaim() credentials::valid()
	if len(p.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := credential.ValidateCredentialIDs(ctx, p.CredentialIDs); result != ter.TesSUCCESS {
			return result
		}
	}

	// Parse channel ID
	channelID, err := hex.DecodeString(p.Channel)
	if err != nil || len(channelID) != 32 {
		return ter.TemINVALID
	}

	var channelKeyBytes [32]byte
	copy(channelKeyBytes[:], channelID)
	channelKey := keylet.Keylet{Key: channelKeyBytes}

	// Read channel
	channelData, err := ctx.View.Read(channelKey)
	if err != nil || channelData == nil {
		ctx.Log.Warn("payment channel claim: channel not found",
			"channel", p.Channel,
		)
		return ter.TecNO_TARGET
	}

	// Parse channel
	channel, err := state.ParsePayChannel(channelData)
	if err != nil {
		ctx.Log.Error("payment channel claim: failed to parse channel", "error", err)
		return ter.TefINTERNAL
	}

	// Auto-close on expiration
	// Reference: rippled PayChan.cpp doApply() lines 466-469
	closeTime := ctx.Config.ParentCloseTime
	if (channel.CancelAfter > 0 && closeTime >= channel.CancelAfter) ||
		(channel.Expiration > 0 && closeTime >= channel.Expiration) {
		return closeChannel(ctx, channelKey, channel)
	}

	accountID, _ := state.DecodeAccountID(p.Account)
	isOwner := channel.Account == accountID
	isDest := channel.DestinationID == accountID

	// Permission check: must be owner or destination
	if !isOwner && !isDest {
		ctx.Log.Warn("payment channel claim: no permission, not owner or destination")
		return ter.TecNO_PERMISSION
	}

	// Track whether the claim actually mutates the channel SLE. rippled only
	// calls view.update(slep) on a real change (PayChan.cpp PayChanClaim::
	// doApply); a fee-only / no-op claim must leave the channel untouched, so
	// no ModifiedNode is emitted and its PreviousTxnID is not bumped.
	channelChanged := false

	// --- Handle Balance claim ---
	if p.Balance != nil {
		claimBalance := uint64(p.Balance.Drops())

		// Destination claiming without signature
		// Reference: rippled PayChan.cpp doApply() line 529
		if isDest && !isOwner && p.Signature == "" {
			return ter.TemBAD_SIGNATURE
		}

		// The signature itself is verified in Validate(); here we only confirm
		// the supplied PublicKey matches the channel's stored key, which needs
		// ledger state. Reference: rippled PayChan.cpp doApply() lines 532-537.
		if p.Signature != "" {
			if !strings.EqualFold(p.PublicKey, channel.PublicKey) {
				return ter.TemBAD_SIGNER
			}
		}

		// Claim must not exceed channel funds
		// Reference: rippled PayChan.cpp doApply() lines 503-504
		if claimBalance > channel.Amount {
			ctx.Log.Warn("payment channel claim: claim exceeds channel funds",
				"claimBalance", claimBalance,
				"channelAmount", channel.Amount,
			)
			return ter.TecUNFUNDED_PAYMENT
		}

		// Must make progress (claim must be > current balance)
		// Reference: rippled PayChan.cpp doApply() lines 506-507
		if claimBalance <= channel.Balance {
			ctx.Log.Warn("payment channel claim: no progress",
				"claimBalance", claimBalance,
				"channelBalance", channel.Balance,
			)
			return ter.TecUNFUNDED_PAYMENT
		}

		// Read destination account
		destKey := keylet.Account(channel.DestinationID)
		destData, err := ctx.View.Read(destKey)
		if err != nil || destData == nil {
			return ter.TecNO_DST
		}

		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return ter.TefINTERNAL
		}

		// DisallowXRP check — bug compatibility, only when DepositAuth is NOT enabled
		// Reference: rippled PayChan.cpp doApply() lines 546-551
		depositAuth := rules.Enabled(amendment.FeatureDepositAuth)
		if !depositAuth && isOwner && !isDest {
			if destAccount.Flags&state.LsfDisallowXRP != 0 {
				return ter.TecNO_TARGET
			}
		}

		// DepositAuth check — when DepositAuth IS enabled
		// Reference: rippled PayChan.cpp doApply() lines 553-563
		if depositAuth {
			if result := credential.VerifyDepositPreauth(ctx, p.CredentialIDs, accountID, channel.DestinationID, destAccount); result != ter.TesSUCCESS {
				return result
			}
		}

		// Transfer funds to destination
		// Reference: rippled PayChan.cpp doApply() lines 509-510
		transferAmount := claimBalance - channel.Balance
		if channel.DestinationID == ctx.AccountID {
			// Destination is the sender — use ctx.Account (engine writes it back)
			ctx.Account.Balance += transferAmount
		} else {
			// Destination is NOT the sender — update directly
			destAccount.Balance += transferAmount
			destUpdatedData, err := state.SerializeAccountRoot(destAccount)
			if err != nil {
				return ter.TefINTERNAL
			}
			if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
				return ter.TefINTERNAL
			}
		}

		channel.Balance = claimBalance
		channelChanged = true
	}

	// --- Handle tfRenew ---
	// Reference: rippled PayChan.cpp doApply() lines 534-542
	flags := p.GetFlags()
	if flags&PaymentChannelClaimFlagRenew != 0 {
		if !isOwner {
			return ter.TecNO_PERMISSION
		}
		// Clear expiration. rippled always calls view.update(slep) here but
		// relies on its own no-op-modify drop (ApplyStateTable.cpp:156-157)
		// when the expiration was already absent; we update only on a real
		// change for the same net result.
		if channel.Expiration != 0 {
			channel.Expiration = 0
			channelChanged = true
		}
	}

	// --- Handle tfClose ---
	// Reference: rippled PayChan.cpp doApply() lines 544-570
	if flags&PaymentChannelClaimFlagClose != 0 {
		// Destination can close immediately.
		// Channel is dry (Balance == Amount) → close immediately.
		// Otherwise owner must wait settle delay.
		if isDest || channel.Balance == channel.Amount {
			return closeChannel(ctx, channelKey, channel)
		}

		// Owner closing: set expiration to closeTime + SettleDelay
		settleExpiration := closeTime + channel.SettleDelay
		if channel.Expiration == 0 || channel.Expiration > settleExpiration {
			channel.Expiration = settleExpiration
			channelChanged = true
		}
	}

	// Match rippled PayChanClaim::doApply: only write the channel SLE when the
	// claim actually changed it (Balance claim, tfRenew clearing an
	// expiration, or tfClose setting one). A fee-only / no-op claim leaves the
	// channel untouched — no ModifiedNode, no PreviousTxnID bump — so the
	// metadata carries only the submitter's AccountRoot (the fee).
	if !channelChanged {
		return ter.TesSUCCESS
	}

	updatedChannelData, err := state.SerializePayChannelFromData(channel)
	if err != nil {
		return ter.TefINTERNAL
	}

	if err := ctx.View.Update(channelKey, updatedChannelData); err != nil {
		return ter.TefINTERNAL
	}

	return ter.TesSUCCESS
}

// ApplyOnTec implements TecApplier for PaymentChannelClaim.
// When tecEXPIRED is returned, expired credentials must still be deleted from the ledger.
// Reference: rippled CredentialHelpers.cpp removeExpired() — called from verifyDepositPreauth()
func (p *PaymentChannelClaim) ApplyOnTec(ctx *tx.ApplyContext) {
	credential.RemoveExpiredCredentialsOnTec(ctx, p.CredentialIDs)
}
