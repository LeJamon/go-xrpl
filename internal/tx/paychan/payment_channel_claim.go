package paychan

import (
	"encoding/hex"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
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
		return tx.Errorf(tx.TemMALFORMED, "Channel must be a valid 256-bit hash")
	}

	// The tfPayChanClaimMask flag check is gated on fix1543 and runs in
	// Preclaim, where the amendment rules are available. The tfClose/tfRenew
	// conflict check below is NOT gated. Reference: rippled PayChan.cpp:443-447.
	flags := p.GetFlags()

	// Cannot set both tfClose and tfRenew
	if (flags&tfPayChanClose != 0) && (flags&tfPayChanRenew != 0) {
		return ErrPayChanCloseAndRenew
	}

	// Validate Balance if present
	if p.Balance != nil {
		if !p.Balance.IsNative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Balance must be XRP")
		}
		balVal := p.Balance.Drops()
		if balVal <= 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Balance must be positive")
		}
	}

	// Validate Amount if present
	if p.Amount != nil {
		if !p.Amount.IsNative() {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be XRP")
		}
		amtVal := p.Amount.Drops()
		if amtVal <= 0 {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
		}
	}

	// Balance cannot exceed Amount
	if p.Balance != nil && p.Amount != nil {
		balVal := p.Balance.Drops()
		amtVal := p.Amount.Drops()
		if balVal > amtVal {
			return ErrPayChanBalanceGTAmount
		}
	}

	// Validate CredentialIDs if present
	// Reference: rippled credentials::checkFields()
	// Use HasField to detect empty arrays from binary parsing where omitempty
	// causes the Go struct field to be nil even though the field was present.
	if p.CredentialIDs != nil || p.HasField("CredentialIDs") {
		if len(p.CredentialIDs) == 0 || len(p.CredentialIDs) > 8 {
			return tx.Errorf(tx.TemMALFORMED, "CredentialIDs array size is invalid")
		}
		seen := make(map[string]bool, len(p.CredentialIDs))
		for _, id := range p.CredentialIDs {
			if seen[id] {
				return tx.Errorf(tx.TemMALFORMED, "duplicates in credentials")
			}
			seen[id] = true
		}
	}

	// If Signature is provided, PublicKey and Balance must also be provided,
	// and the signature is verified here — entirely from tx fields, before any
	// ledger access. Reference: rippled PayChan.cpp PayChanClaim::preflight()
	// lines 450-474.
	if p.Signature != "" {
		if p.PublicKey == "" {
			return ErrPayChanSigNeedsKey
		}
		if p.Balance == nil {
			return ErrPayChanSigNeedsBalance
		}

		// Authorized amount: Amount if present, else Balance. Balance may not
		// exceed it. Reference: PayChan.cpp lines 459-463.
		authAmt := p.Balance.Drops()
		if p.Amount != nil {
			authAmt = p.Amount.Drops()
		}
		if p.Balance.Drops() > authAmt {
			return tx.Errorf(tx.TemBAD_AMOUNT, "Balance exceeds authorized amount")
		}

		// Validate PublicKey is valid hex with the type rippled's
		// publicKeyType() accepts: 33 bytes prefixed 0xED / 0x02 / 0x03.
		// Reference: rippled PayChan.cpp preflight() publicKeyType()
		pkBytes, err := hex.DecodeString(p.PublicKey)
		if err != nil || !tx.IsValidPublicKey(pkBytes) {
			return ErrPayChanPublicKeyInvalid
		}

		// Verify the claim signature over the authorized amount.
		// Reference: PayChan.cpp lines 469-473 serializePayChanAuthorization.
		if !verifyClaimSignature(p.Channel, uint64(authAmt), p.PublicKey, p.Signature) {
			return tx.Errorf(tx.TemBAD_SIGNATURE, "invalid claim signature")
		}
	}

	return nil
}

func (p *PaymentChannelClaim) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

func (p *PaymentChannelClaim) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
}

// Preclaim performs the rules-aware fix1543 flag check. Only the
// tfPayChanClaimMask check is gated; the tfClose/tfRenew conflict check runs
// unconditionally in Validate. Reference: rippled PayChan.cpp:443-447. rippled
// runs the mask check first in preflight; the gate is rules-aware and go-xrpl
// exposes rules only at Preclaim, so it runs after the common preflight/preclaim
// steps. For a tx malformed in two ways this can surface a different tem code
// than rippled; the result is tem-only (never enters a ledger) so there is no
// consensus divergence.
func (p *PaymentChannelClaim) Preclaim(_ tx.LedgerView, config tx.EngineConfig) tx.Result {
	const tfPayChanClaimMask = ^(tfPayChanRenew | tfPayChanClose | tx.TfUniversal)
	if config.GetRules().Enabled(amendment.FeatureFix1543) && (p.GetFlags()&tfPayChanClaimMask) != 0 {
		return tx.TemINVALID_FLAG
	}
	return tx.TesSUCCESS
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
func (p *PaymentChannelClaim) Apply(ctx *tx.ApplyContext) tx.Result {
	ctx.Log.Trace("payment channel claim apply",
		"account", p.Account,
		"channel", p.Channel,
		"amount", p.Amount,
		"balance", p.Balance,
		"flags", p.GetFlags(),
	)

	rules := ctx.Rules()

	// --- Preclaim: credential checks ---
	// Reference: rippled PayChan.cpp PayChanClaim::preflight() credential check
	if len(p.CredentialIDs) > 0 && !rules.Enabled(amendment.FeatureCredentials) {
		return tx.TemDISABLED
	}

	// Reference: rippled PayChan.cpp PayChanClaim::preclaim() credentials::valid()
	if len(p.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := credential.ValidateCredentialIDs(ctx, p.CredentialIDs); result != tx.TesSUCCESS {
			return result
		}
	}

	// Parse channel ID
	channelID, err := hex.DecodeString(p.Channel)
	if err != nil || len(channelID) != 32 {
		return tx.TemINVALID
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
		return tx.TecNO_TARGET
	}

	// Parse channel
	channel, err := state.ParsePayChannel(channelData)
	if err != nil {
		ctx.Log.Error("payment channel claim: failed to parse channel", "error", err)
		return tx.TefINTERNAL
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
		return tx.TecNO_PERMISSION
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
			return tx.TemBAD_SIGNATURE
		}

		// The signature itself is verified in Validate(); here we only confirm
		// the supplied PublicKey matches the channel's stored key, which needs
		// ledger state. Reference: rippled PayChan.cpp doApply() lines 532-537.
		if p.Signature != "" {
			if !strings.EqualFold(p.PublicKey, channel.PublicKey) {
				return tx.TemBAD_SIGNER
			}
		}

		// Claim must not exceed channel funds
		// Reference: rippled PayChan.cpp doApply() lines 503-504
		if claimBalance > channel.Amount {
			ctx.Log.Warn("payment channel claim: claim exceeds channel funds",
				"claimBalance", claimBalance,
				"channelAmount", channel.Amount,
			)
			return tx.TecUNFUNDED_PAYMENT
		}

		// Must make progress (claim must be > current balance)
		// Reference: rippled PayChan.cpp doApply() lines 506-507
		if claimBalance <= channel.Balance {
			ctx.Log.Warn("payment channel claim: no progress",
				"claimBalance", claimBalance,
				"channelBalance", channel.Balance,
			)
			return tx.TecUNFUNDED_PAYMENT
		}

		// Read destination account
		destKey := keylet.Account(channel.DestinationID)
		destData, err := ctx.View.Read(destKey)
		if err != nil || destData == nil {
			return tx.TecNO_DST
		}

		destAccount, err := state.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// DisallowXRP check — bug compatibility, only when DepositAuth is NOT enabled
		// Reference: rippled PayChan.cpp doApply() lines 546-551
		depositAuth := rules.Enabled(amendment.FeatureDepositAuth)
		if !depositAuth && isOwner && !isDest {
			if destAccount.Flags&state.LsfDisallowXRP != 0 {
				return tx.TecNO_TARGET
			}
		}

		// DepositAuth check — when DepositAuth IS enabled
		// Reference: rippled PayChan.cpp doApply() lines 553-563
		if depositAuth {
			if result := credential.VerifyDepositPreauth(ctx, p.CredentialIDs, accountID, channel.DestinationID, destAccount); result != tx.TesSUCCESS {
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
				return tx.TefINTERNAL
			}
			if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
				return tx.TefINTERNAL
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
			return tx.TecNO_PERMISSION
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
		return tx.TesSUCCESS
	}

	updatedChannelData, err := state.SerializePayChannelFromData(channel)
	if err != nil {
		return tx.TefINTERNAL
	}

	if err := ctx.View.Update(channelKey, updatedChannelData); err != nil {
		return tx.TefINTERNAL
	}

	return tx.TesSUCCESS
}

// ApplyOnTec implements TecApplier for PaymentChannelClaim.
// When tecEXPIRED is returned, expired credentials must still be deleted from the ledger.
// Reference: rippled CredentialHelpers.cpp removeExpired() — called from verifyDepositPreauth()
func (p *PaymentChannelClaim) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	credential.RemoveExpiredCredentials(ctx, p.CredentialIDs)
	return tx.TesSUCCESS
}
