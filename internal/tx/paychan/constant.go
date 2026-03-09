package paychan

import "errors"

// Payment channel constants
const (
	// MaxPayChanPublicKeyLength is the maximum length of a public key (33 bytes compressed)
	MaxPayChanPublicKeyLength = 66 // 33 bytes * 2 hex chars
)

// Payment channel flags
const (
	// tfPayChanRenew resets the settle delay
	tfPayChanRenew uint32 = 0x00010000
	// tfPayChanClose requests to close the channel
	tfPayChanClose uint32 = 0x00020000
)

// Exported flag constants for backwards compatibility
const (
	PaymentChannelClaimFlagRenew = tfPayChanRenew
	PaymentChannelClaimFlagClose = tfPayChanClose
)

// Payment channel errors
var (
	ErrPayChanAmountRequired    = errors.New("temBAD_AMOUNT: Amount is required")
	ErrPayChanAmountNotXRP      = errors.New("temBAD_AMOUNT: payment channels can only hold XRP")
	ErrPayChanAmountNotPositive = errors.New("temBAD_AMOUNT: Amount must be positive")
	ErrPayChanDestRequired      = errors.New("temDST_NEEDED: Destination is required")
	ErrPayChanDestIsSrc         = errors.New("temDST_IS_SRC: cannot create payment channel to self")
	ErrPayChanPublicKeyRequired = errors.New("temMALFORMED: PublicKey is required")
	ErrPayChanPublicKeyInvalid  = errors.New("temMALFORMED: PublicKey is not a valid public key")
	ErrPayChanChannelRequired   = errors.New("temMALFORMED: Channel is required")
	ErrPayChanBadExpiration     = errors.New("temBAD_EXPIRATION: Expiration is invalid")
	ErrPayChanBalanceGTAmount   = errors.New("temBAD_AMOUNT: Balance cannot exceed Amount")
	ErrPayChanCloseAndRenew     = errors.New("temMALFORMED: cannot set both tfClose and tfRenew")
	ErrPayChanSigNeedsKey       = errors.New("temMALFORMED: PublicKey is required with Signature")
	ErrPayChanSigNeedsBalance   = errors.New("temMALFORMED: Balance is required with Signature")
	ErrPayChanSigNeedsAmount    = errors.New("temMALFORMED: Amount is required with Signature")
)
