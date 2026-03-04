package paychan

import (
	"fmt"
	"time"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
	paychan "github.com/LeJamon/goXRPLd/internal/core/tx/paychan"
	"github.com/LeJamon/goXRPLd/internal/testing"
)

const RippleEpoch = 946684800

func ToRippleTime(t time.Time) uint32 {
	return uint32(t.Unix() - RippleEpoch)
}

type ChannelCreateBuilder struct {
	from           *testing.Account
	to             *testing.Account
	amount         int64
	settleDelay    uint32
	publicKey      string
	cancelAfter    *uint32
	destinationTag *uint32
	sourceTag      *uint32
	fee            int64
	sequence       *uint32
	ticketSeq      *uint32
}

func ChannelCreate(from, to *testing.Account, amount int64, settleDelay uint32, publicKey string) *ChannelCreateBuilder {
	return &ChannelCreateBuilder{
		from:        from,
		to:          to,
		amount:      amount,
		settleDelay: settleDelay,
		publicKey:   publicKey,
		fee:         10,
	}
}

func (b *ChannelCreateBuilder) CancelAfter(t time.Time) *ChannelCreateBuilder {
	v := ToRippleTime(t)
	b.cancelAfter = &v
	return b
}

func (b *ChannelCreateBuilder) CancelAfterRipple(rippleTime uint32) *ChannelCreateBuilder {
	b.cancelAfter = &rippleTime
	return b
}

func (b *ChannelCreateBuilder) DestTag(tag uint32) *ChannelCreateBuilder {
	b.destinationTag = &tag
	return b
}

func (b *ChannelCreateBuilder) SourceTag(tag uint32) *ChannelCreateBuilder {
	b.sourceTag = &tag
	return b
}

func (b *ChannelCreateBuilder) Fee(f uint64) *ChannelCreateBuilder {
	b.fee = int64(f)
	return b
}

func (b *ChannelCreateBuilder) Sequence(seq uint32) *ChannelCreateBuilder {
	b.sequence = &seq
	return b
}

func (b *ChannelCreateBuilder) Ticket(seq uint32) *ChannelCreateBuilder {
	b.ticketSeq = &seq
	return b
}

func (b *ChannelCreateBuilder) Build() tx.Transaction {
	amount := tx.NewXRPAmount(b.amount)
	c := paychan.NewPaymentChannelCreate(b.from.Address, b.to.Address, amount, b.settleDelay, b.publicKey)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.cancelAfter != nil {
		c.CancelAfter = b.cancelAfter
	}
	if b.destinationTag != nil {
		c.DestinationTag = b.destinationTag
	}
	if b.sourceTag != nil {
		c.SourceTag = b.sourceTag
	}
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.ticketSeq != nil {
		c.GetCommon().TicketSequence = b.ticketSeq
	}

	return c
}

type ChannelFundBuilder struct {
	funder     *testing.Account
	channelID  string
	amount     int64
	expiration *uint32
	fee        int64
	sequence   *uint32
	ticketSeq  *uint32
}

func ChannelFund(funder *testing.Account, channelID string, amount int64) *ChannelFundBuilder {
	return &ChannelFundBuilder{
		funder:    funder,
		channelID: channelID,
		amount:    amount,
		fee:       10,
	}
}

func (b *ChannelFundBuilder) Expiration(t time.Time) *ChannelFundBuilder {
	v := ToRippleTime(t)
	b.expiration = &v
	return b
}

func (b *ChannelFundBuilder) ExpirationRipple(rippleTime uint32) *ChannelFundBuilder {
	b.expiration = &rippleTime
	return b
}

func (b *ChannelFundBuilder) Fee(f uint64) *ChannelFundBuilder {
	b.fee = int64(f)
	return b
}

func (b *ChannelFundBuilder) Sequence(seq uint32) *ChannelFundBuilder {
	b.sequence = &seq
	return b
}

func (b *ChannelFundBuilder) Ticket(seq uint32) *ChannelFundBuilder {
	b.ticketSeq = &seq
	return b
}

func (b *ChannelFundBuilder) Build() tx.Transaction {
	amount := tx.NewXRPAmount(b.amount)
	f := paychan.NewPaymentChannelFund(b.funder.Address, b.channelID, amount)
	f.Fee = fmt.Sprintf("%d", b.fee)

	if b.expiration != nil {
		f.Expiration = b.expiration
	}
	if b.sequence != nil {
		f.SetSequence(*b.sequence)
	}
	if b.ticketSeq != nil {
		f.GetCommon().TicketSequence = b.ticketSeq
	}

	return f
}

type ChannelClaimBuilder struct {
	claimer       *testing.Account
	channelID     string
	balance       *int64
	amount        *int64
	signature     string
	publicKey     string
	fee           int64
	sequence      *uint32
	ticketSeq     *uint32
	close         bool
	renew         bool
	credentialIDs []string
}

func ChannelClaim(claimer *testing.Account, channelID string) *ChannelClaimBuilder {
	return &ChannelClaimBuilder{
		claimer:   claimer,
		channelID: channelID,
		fee:       10,
	}
}

func (b *ChannelClaimBuilder) Balance(drops int64) *ChannelClaimBuilder {
	b.balance = &drops
	return b
}

func (b *ChannelClaimBuilder) Amount(drops int64) *ChannelClaimBuilder {
	b.amount = &drops
	return b
}

func (b *ChannelClaimBuilder) Signature(sig string) *ChannelClaimBuilder {
	b.signature = sig
	return b
}

func (b *ChannelClaimBuilder) PublicKey(pk string) *ChannelClaimBuilder {
	b.publicKey = pk
	return b
}

func (b *ChannelClaimBuilder) Fee(f uint64) *ChannelClaimBuilder {
	b.fee = int64(f)
	return b
}

func (b *ChannelClaimBuilder) Sequence(seq uint32) *ChannelClaimBuilder {
	b.sequence = &seq
	return b
}

func (b *ChannelClaimBuilder) Ticket(seq uint32) *ChannelClaimBuilder {
	b.ticketSeq = &seq
	return b
}

func (b *ChannelClaimBuilder) CredentialIDs(ids []string) *ChannelClaimBuilder {
	b.credentialIDs = ids
	return b
}

func (b *ChannelClaimBuilder) Close() *ChannelClaimBuilder {
	b.close = true
	return b
}

func (b *ChannelClaimBuilder) Renew() *ChannelClaimBuilder {
	b.renew = true
	return b
}

func (b *ChannelClaimBuilder) Build() tx.Transaction {
	c := paychan.NewPaymentChannelClaim(b.claimer.Address, b.channelID)
	c.Fee = fmt.Sprintf("%d", b.fee)

	if b.balance != nil {
		v := tx.NewXRPAmount(*b.balance)
		c.Balance = &v
	}
	if b.amount != nil {
		v := tx.NewXRPAmount(*b.amount)
		c.Amount = &v
	}
	if b.signature != "" {
		c.Signature = b.signature
	}
	if b.publicKey != "" {
		c.PublicKey = b.publicKey
	}
	if b.close {
		c.SetClose()
	}
	if b.renew {
		c.SetRenew()
	}
	if b.sequence != nil {
		c.SetSequence(*b.sequence)
	}
	if b.ticketSeq != nil {
		c.GetCommon().TicketSequence = b.ticketSeq
	}
	if b.credentialIDs != nil {
		c.CredentialIDs = b.credentialIDs
	}

	return c
}
