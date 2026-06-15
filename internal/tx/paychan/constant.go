package paychan

import "github.com/LeJamon/go-xrpl/internal/tx/ter"

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
	ErrPayChanAmountRequired    = ter.Errorf(ter.TemBAD_AMOUNT, "Amount is required")
	ErrPayChanAmountNotXRP      = ter.Errorf(ter.TemBAD_AMOUNT, "payment channels can only hold XRP")
	ErrPayChanAmountNotPositive = ter.Errorf(ter.TemBAD_AMOUNT, "Amount must be positive")
	ErrPayChanPublicKeyRequired = ter.Errorf(ter.TemMALFORMED, "PublicKey is required")
	ErrPayChanPublicKeyInvalid  = ter.Errorf(ter.TemMALFORMED, "PublicKey is not a valid public key")
	ErrPayChanChannelRequired   = ter.Errorf(ter.TemMALFORMED, "Channel is required")
	ErrPayChanBalanceGTAmount   = ter.Errorf(ter.TemBAD_AMOUNT, "Balance cannot exceed Amount")
	ErrPayChanCloseAndRenew     = ter.Errorf(ter.TemMALFORMED, "cannot set both tfClose and tfRenew")
	ErrPayChanSigNeedsKey       = ter.Errorf(ter.TemMALFORMED, "PublicKey is required with Signature")
	ErrPayChanSigNeedsBalance   = ter.Errorf(ter.TemMALFORMED, "Balance is required with Signature")
)
