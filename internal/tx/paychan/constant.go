package paychan

import "github.com/LeJamon/goXRPLd/internal/tx"

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
	ErrPayChanAmountRequired    = tx.Errorf(tx.TemBAD_AMOUNT, "Amount is required")
	ErrPayChanAmountNotXRP      = tx.Errorf(tx.TemBAD_AMOUNT, "payment channels can only hold XRP")
	ErrPayChanAmountNotPositive = tx.Errorf(tx.TemBAD_AMOUNT, "Amount must be positive")
	ErrPayChanDestRequired      = tx.Errorf(tx.TemDST_NEEDED, "Destination is required")
	ErrPayChanDestIsSrc         = tx.Errorf(tx.TemDST_IS_SRC, "cannot create payment channel to self")
	ErrPayChanPublicKeyRequired = tx.Errorf(tx.TemMALFORMED, "PublicKey is required")
	ErrPayChanPublicKeyInvalid  = tx.Errorf(tx.TemMALFORMED, "PublicKey is not a valid public key")
	ErrPayChanChannelRequired   = tx.Errorf(tx.TemMALFORMED, "Channel is required")
	ErrPayChanBadExpiration     = tx.Errorf(tx.TemBAD_EXPIRATION, "Expiration is invalid")
	ErrPayChanBalanceGTAmount   = tx.Errorf(tx.TemBAD_AMOUNT, "Balance cannot exceed Amount")
	ErrPayChanCloseAndRenew     = tx.Errorf(tx.TemMALFORMED, "cannot set both tfClose and tfRenew")
	ErrPayChanSigNeedsKey       = tx.Errorf(tx.TemMALFORMED, "PublicKey is required with Signature")
	ErrPayChanSigNeedsBalance   = tx.Errorf(tx.TemMALFORMED, "Balance is required with Signature")
	ErrPayChanSigNeedsAmount    = tx.Errorf(tx.TemMALFORMED, "Amount is required with Signature")
)
