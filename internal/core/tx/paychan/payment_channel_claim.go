package paychan

import (
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/LeJamon/goXRPLd/internal/core/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/credential"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
)

func init() {
	tx.Register(tx.TypePaymentChannelClaim, func() tx.Transaction {
		return &PaymentChannelClaim{BaseTx: *tx.NewBaseTx(tx.TypePaymentChannelClaim, "")}
	})
}

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

// TxType returns the transaction type
func (p *PaymentChannelClaim) TxType() tx.Type {
	return tx.TypePaymentChannelClaim
}

// Validate validates the PaymentChannelClaim transaction
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
		return errors.New("temMALFORMED: Channel must be a valid 256-bit hash")
	}

	// Validate flags - fix1543
	flags := p.GetFlags()
	validFlags := tfPayChanRenew | tfPayChanClose | tx.TfUniversal
	if flags & ^validFlags != 0 {
		return tx.ErrInvalidFlags
	}

	// Cannot set both tfClose and tfRenew
	if (flags&tfPayChanClose != 0) && (flags&tfPayChanRenew != 0) {
		return ErrPayChanCloseAndRenew
	}

	// Validate Balance if present
	if p.Balance != nil {
		if !p.Balance.IsNative() {
			return errors.New("temBAD_AMOUNT: Balance must be XRP")
		}
		balVal := p.Balance.Drops()
		if balVal <= 0 {
			return errors.New("temBAD_AMOUNT: Balance must be positive")
		}
	}

	// Validate Amount if present
	if p.Amount != nil {
		if !p.Amount.IsNative() {
			return errors.New("temBAD_AMOUNT: Amount must be XRP")
		}
		amtVal := p.Amount.Drops()
		if amtVal <= 0 {
			return errors.New("temBAD_AMOUNT: Amount must be positive")
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
	if p.CredentialIDs != nil {
		if len(p.CredentialIDs) == 0 || len(p.CredentialIDs) > 8 {
			return errors.New("temMALFORMED: CredentialIDs array size is invalid")
		}
		seen := make(map[string]bool, len(p.CredentialIDs))
		for _, id := range p.CredentialIDs {
			if seen[id] {
				return errors.New("temMALFORMED: duplicates in credentials")
			}
			seen[id] = true
		}
	}

	// If Signature is provided, PublicKey and Balance must also be provided
	if p.Signature != "" {
		if p.PublicKey == "" {
			return ErrPayChanSigNeedsKey
		}
		if p.Balance == nil {
			return ErrPayChanSigNeedsBalance
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
		if len(pkBytes) == 33 {
			if pkBytes[0] != 0x02 && pkBytes[0] != 0x03 && pkBytes[0] != 0xED {
				return ErrPayChanPublicKeyInvalid
			}
		} else if len(pkBytes) == 65 {
			if pkBytes[0] != 0x04 {
				return ErrPayChanPublicKeyInvalid
			}
		}
	}

	return nil
}

// Flatten returns a flat map of all transaction fields
func (p *PaymentChannelClaim) Flatten() (map[string]any, error) {
	return tx.ReflectFlatten(p)
}

// RequiredAmendments returns the amendments required for this transaction type
func (p *PaymentChannelClaim) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeaturePayChan}
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

// Apply applies a PaymentChannelClaim transaction
// Reference: rippled PayChan.cpp PayChanClaim::preclaim() + doApply()
func (pcl *PaymentChannelClaim) Apply(ctx *tx.ApplyContext) tx.Result {
	rules := ctx.Rules()

	// --- Preclaim: credential checks ---
	// Reference: rippled PayChan.cpp PayChanClaim::preflight() credential check
	if len(pcl.CredentialIDs) > 0 && !rules.Enabled(amendment.FeatureCredentials) {
		return tx.TemDISABLED
	}

	// Reference: rippled PayChan.cpp PayChanClaim::preclaim() credentials::valid()
	if len(pcl.CredentialIDs) > 0 && rules.Enabled(amendment.FeatureCredentials) {
		if result := validateCredentials(ctx, pcl.CredentialIDs); result != tx.TesSUCCESS {
			return result
		}
	}

	// Parse channel ID
	channelID, err := hex.DecodeString(pcl.Channel)
	if err != nil || len(channelID) != 32 {
		return tx.TemINVALID
	}

	var channelKeyBytes [32]byte
	copy(channelKeyBytes[:], channelID)
	channelKey := keylet.Keylet{Key: channelKeyBytes}

	// Read channel
	channelData, err := ctx.View.Read(channelKey)
	if err != nil || channelData == nil {
		return tx.TecNO_TARGET
	}

	// Parse channel
	channel, err := sle.ParsePayChannel(channelData)
	if err != nil {
		return tx.TefINTERNAL
	}

	// Auto-close on expiration
	// Reference: rippled PayChan.cpp doApply() lines 466-469
	closeTime := ctx.Config.ParentCloseTime
	if (channel.CancelAfter > 0 && closeTime >= channel.CancelAfter) ||
		(channel.Expiration > 0 && closeTime >= channel.Expiration) {
		return closeChannel(ctx, channelKey, channel)
	}

	accountID, _ := sle.DecodeAccountID(pcl.Account)
	isOwner := channel.Account == accountID
	isDest := channel.DestinationID == accountID

	// Permission check: must be owner or destination
	if !isOwner && !isDest {
		return tx.TecNO_PERMISSION
	}

	// --- Handle Balance claim ---
	if pcl.Balance != nil {
		claimBalance := uint64(pcl.Balance.Drops())

		// Destination claiming without signature
		// Reference: rippled PayChan.cpp doApply() lines 480-481
		if isDest && !isOwner && pcl.Signature == "" {
			return tx.TemBAD_SIGNATURE
		}

		// Signature verification
		// Reference: rippled PayChan.cpp doApply() lines 483-501
		if pcl.Signature != "" {
			// Determine authorized amount: use Amount if present, else Balance
			var authAmt uint64
			if pcl.Amount != nil {
				authAmt = uint64(pcl.Amount.Drops())
			} else {
				authAmt = claimBalance
			}

			// Balance must not exceed authorized amount
			if claimBalance > authAmt {
				return tx.TemBAD_AMOUNT
			}

			// PublicKey must match the channel's PublicKey
			if !strings.EqualFold(pcl.PublicKey, channel.PublicKey) {
				return tx.TemBAD_SIGNER
			}

			// Verify the signature
			if !verifyClaimSignature(pcl.Channel, authAmt, pcl.PublicKey, pcl.Signature) {
				return tx.TemBAD_SIGNATURE
			}
		}

		// Claim must not exceed channel funds
		// Reference: rippled PayChan.cpp doApply() lines 503-504
		if claimBalance > channel.Amount {
			return tx.TecUNFUNDED_PAYMENT
		}

		// Must make progress (claim must be > current balance)
		// Reference: rippled PayChan.cpp doApply() lines 506-507
		if claimBalance <= channel.Balance {
			return tx.TecUNFUNDED_PAYMENT
		}

		// Read destination account
		destKey := keylet.Account(channel.DestinationID)
		destData, err := ctx.View.Read(destKey)
		if err != nil || destData == nil {
			return tx.TecNO_DST
		}

		destAccount, err := sle.ParseAccountRoot(destData)
		if err != nil {
			return tx.TefINTERNAL
		}

		// DisallowXRP check — bug compatibility, only when DepositAuth is NOT enabled
		// Reference: rippled PayChan.cpp doApply() lines 546-551
		depositAuth := rules.Enabled(amendment.FeatureDepositAuth)
		if !depositAuth && isOwner && !isDest {
			if destAccount.Flags&sle.LsfDisallowXRP != 0 {
				return tx.TecNO_TARGET
			}
		}

		// DepositAuth check — when DepositAuth IS enabled
		// Reference: rippled PayChan.cpp doApply() lines 553-563
		if depositAuth {
			if result := verifyDepositPreauth(ctx, pcl.CredentialIDs, accountID, channel.DestinationID, destAccount); result != tx.TesSUCCESS {
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
			destUpdatedData, err := sle.SerializeAccountRoot(destAccount)
			if err != nil {
				return tx.TefINTERNAL
			}
			if err := ctx.View.Update(destKey, destUpdatedData); err != nil {
				return tx.TefINTERNAL
			}
		}

		channel.Balance = claimBalance
	}

	// --- Handle tfRenew ---
	// Reference: rippled PayChan.cpp doApply() lines 534-542
	flags := pcl.GetFlags()
	if flags&PaymentChannelClaimFlagRenew != 0 {
		if !isOwner {
			return tx.TecNO_PERMISSION
		}
		// Clear expiration
		channel.Expiration = 0
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
		}
	}

	// Update channel SLE
	updatedChannelData, err := sle.SerializePayChannelFromData(channel)
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
func (pcl *PaymentChannelClaim) ApplyOnTec(ctx *tx.ApplyContext) tx.Result {
	if len(pcl.CredentialIDs) == 0 {
		return tx.TesSUCCESS
	}

	closeTime := ctx.Config.ParentCloseTime
	for _, credIDHex := range pcl.CredentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			continue
		}

		var credKeyBytes [32]byte
		copy(credKeyBytes[:], credHash)
		credKey := keylet.Keylet{Key: credKeyBytes}

		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			continue
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			continue
		}

		// Only delete if actually expired
		if credEntry.Expiration != nil && closeTime > *credEntry.Expiration {
			credential.DeleteSLE(ctx.View, credKey, credEntry)
		}
	}

	return tx.TesSUCCESS
}

// validateCredentials implements rippled's credentials::valid() preclaim check.
// Reference: rippled CredentialHelpers.cpp credentials::valid()
func validateCredentials(ctx *tx.ApplyContext, credentialIDs []string) tx.Result {
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TecBAD_CREDENTIALS
		}

		var credKeyBytes [32]byte
		copy(credKeyBytes[:], credHash)
		credKey := keylet.Keylet{Key: credKeyBytes}

		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TecBAD_CREDENTIALS
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TecBAD_CREDENTIALS
		}

		// Subject must match the transaction sender
		if credEntry.Subject != ctx.AccountID {
			return tx.TecBAD_CREDENTIALS
		}

		// Credential must be accepted
		if (credEntry.Flags & credential.LsfCredentialAccepted) == 0 {
			return tx.TecBAD_CREDENTIALS
		}

		// Check expiration
		if credEntry.Expiration != nil && ctx.Config.ParentCloseTime >= *credEntry.Expiration {
			return tx.TecEXPIRED
		}
	}

	return tx.TesSUCCESS
}

// verifyDepositPreauth implements rippled's verifyDepositPreauth() from CredentialHelpers.cpp.
// Reference: rippled CredentialHelpers.cpp verifyDepositPreauth() lines 357-391
func verifyDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, src [20]byte, dst [20]byte, destAccount *sle.AccountRoot) tx.Result {
	// Only check if destination has lsfDepositAuth set
	if (destAccount.Flags & sle.LsfDepositAuth) == 0 {
		return tx.TesSUCCESS
	}

	// Self-deposits don't need preauth
	if src == dst {
		return tx.TesSUCCESS
	}

	// Check account-based DepositPreauth
	preauthKey := keylet.DepositPreauth(dst, src)
	if exists, _ := ctx.View.Exists(preauthKey); exists {
		return tx.TesSUCCESS
	}

	// No account-based preauth — check credential-based
	if len(credentialIDs) > 0 && ctx.Rules().Enabled(amendment.FeatureCredentials) {
		return authorizedDepositPreauth(ctx, credentialIDs, dst)
	}

	return tx.TecNO_PERMISSION
}

// authorizedDepositPreauth implements rippled's credentials::authorizedDepositPreauth().
// Reference: rippled CredentialHelpers.cpp credentials::authorizedDepositPreauth()
func authorizedDepositPreauth(ctx *tx.ApplyContext, credentialIDs []string, dst [20]byte) tx.Result {
	type credPair struct {
		issuer   [20]byte
		credType []byte
	}

	pairs := make([]credPair, 0, len(credentialIDs))
	for _, credIDHex := range credentialIDs {
		credHash, err := hex.DecodeString(credIDHex)
		if err != nil || len(credHash) != 32 {
			return tx.TefINTERNAL
		}

		var credKeyBytes [32]byte
		copy(credKeyBytes[:], credHash)
		credKey := keylet.Keylet{Key: credKeyBytes}

		credData, err := ctx.View.Read(credKey)
		if err != nil || credData == nil {
			return tx.TefINTERNAL
		}

		credEntry, err := credential.ParseCredentialEntry(credData)
		if err != nil {
			return tx.TefINTERNAL
		}

		pairs = append(pairs, credPair{
			issuer:   credEntry.Issuer,
			credType: credEntry.CredentialType,
		})
	}

	// Sort pairs by (issuer, credType) for deterministic lookup
	sort.Slice(pairs, func(i, j int) bool {
		cmp := strings.Compare(string(pairs[i].issuer[:]), string(pairs[j].issuer[:]))
		if cmp != 0 {
			return cmp < 0
		}
		return strings.Compare(string(pairs[i].credType), string(pairs[j].credType)) < 0
	})

	sortedCreds := make([]keylet.CredentialPair, len(pairs))
	for i, p := range pairs {
		sortedCreds[i] = keylet.CredentialPair{
			Issuer:         p.issuer,
			CredentialType: p.credType,
		}
	}

	// Check if credential-based DepositPreauth exists
	dpKey := keylet.DepositPreauthCredentials(dst, sortedCreds)
	if exists, _ := ctx.View.Exists(dpKey); !exists {
		return tx.TecNO_PERMISSION
	}

	return tx.TesSUCCESS
}
