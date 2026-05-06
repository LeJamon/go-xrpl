// Package amm provides test builders for AMM transactions.
// Reference: rippled/src/test/jtx/AMM.h
package amm

import (
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/amm"
)

// AMMCreateBuilder provides a fluent interface for building AMMCreate transactions.
type AMMCreateBuilder struct {
	account    *jtx.Account
	amount1    tx.Amount
	amount2    tx.Amount
	tradingFee uint16
	fee        string
	flags      uint32
}

// AMMCreate creates a new AMMCreateBuilder.
// The default fee matches rippled's AMMCreate::calculateBaseFee — one owner
// reserve increment (50,000,000 drops).
func AMMCreate(account *jtx.Account, amount1, amount2 tx.Amount) *AMMCreateBuilder {
	return &AMMCreateBuilder{
		account:    account,
		amount1:    amount1,
		amount2:    amount2,
		tradingFee: 0,
		fee:        "50000000",
	}
}

// TradingFee sets the trading fee (0-1000, where 1000 = 1%).
func (b *AMMCreateBuilder) TradingFee(fee uint16) *AMMCreateBuilder {
	b.tradingFee = fee
	return b
}

// Fee sets the transaction fee.
func (b *AMMCreateBuilder) Fee(fee string) *AMMCreateBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMCreateBuilder) Flags(flags uint32) *AMMCreateBuilder {
	b.flags = flags
	return b
}

// Build creates the AMMCreate transaction.
func (b *AMMCreateBuilder) Build() *amm.AMMCreate {
	ammTx := amm.NewAMMCreate(b.account.Address, b.amount1, b.amount2, b.tradingFee)
	ammTx.Fee = b.fee
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMDepositBuilder provides a fluent interface for building AMMDeposit transactions.
type AMMDepositBuilder struct {
	account    *jtx.Account
	asset      tx.Asset
	asset2     tx.Asset
	amount     *tx.Amount
	amount2    *tx.Amount
	lpTokenOut *tx.Amount
	ePrice     *tx.Amount
	tradingFee uint16
	fee        string
	flags      uint32
}

// AMMDeposit creates a new AMMDepositBuilder.
func AMMDeposit(account *jtx.Account, asset, asset2 tx.Asset) *AMMDepositBuilder {
	return &AMMDepositBuilder{
		account: account,
		asset:   asset,
		asset2:  asset2,
		fee:     "10",
	}
}

// Amount sets the first deposit amount.
func (b *AMMDepositBuilder) Amount(amt tx.Amount) *AMMDepositBuilder {
	b.amount = &amt
	return b
}

// Amount2 sets the second deposit amount.
func (b *AMMDepositBuilder) Amount2(amt tx.Amount) *AMMDepositBuilder {
	b.amount2 = &amt
	return b
}

// LPTokenOut sets the desired LP tokens to receive.
func (b *AMMDepositBuilder) LPTokenOut(amt tx.Amount) *AMMDepositBuilder {
	b.lpTokenOut = &amt
	return b
}

// EPrice sets the effective price limit.
func (b *AMMDepositBuilder) EPrice(amt tx.Amount) *AMMDepositBuilder {
	b.ePrice = &amt
	return b
}

// TradingFee sets the trading fee for tfTwoAssetIfEmpty.
func (b *AMMDepositBuilder) TradingFee(fee uint16) *AMMDepositBuilder {
	b.tradingFee = fee
	return b
}

// Fee sets the transaction fee.
func (b *AMMDepositBuilder) Fee(fee string) *AMMDepositBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMDepositBuilder) Flags(flags uint32) *AMMDepositBuilder {
	b.flags = flags
	return b
}

// LPToken sets the tfLPToken flag.
func (b *AMMDepositBuilder) LPToken() *AMMDepositBuilder {
	b.flags |= TfLPToken
	return b
}

// SingleAsset sets the tfSingleAsset flag.
func (b *AMMDepositBuilder) SingleAsset() *AMMDepositBuilder {
	b.flags |= TfSingleAsset
	return b
}

// TwoAsset sets the tfTwoAsset flag.
func (b *AMMDepositBuilder) TwoAsset() *AMMDepositBuilder {
	b.flags |= TfTwoAsset
	return b
}

// OneAssetLPToken sets the tfOneAssetLPToken flag.
func (b *AMMDepositBuilder) OneAssetLPToken() *AMMDepositBuilder {
	b.flags |= TfOneAssetLPToken
	return b
}

// LimitLPToken sets the tfLimitLPToken flag.
func (b *AMMDepositBuilder) LimitLPToken() *AMMDepositBuilder {
	b.flags |= TfLimitLPToken
	return b
}

// TwoAssetIfEmpty sets the tfTwoAssetIfEmpty flag.
func (b *AMMDepositBuilder) TwoAssetIfEmpty() *AMMDepositBuilder {
	b.flags |= TfTwoAssetIfEmpty
	return b
}

// Build creates the AMMDeposit transaction.
func (b *AMMDepositBuilder) Build() *amm.AMMDeposit {
	ammTx := amm.NewAMMDeposit(b.account.Address, b.asset, b.asset2)
	ammTx.Fee = b.fee
	ammTx.Amount = b.amount
	ammTx.Amount2 = b.amount2
	ammTx.LPTokenOut = b.lpTokenOut
	ammTx.EPrice = b.ePrice
	ammTx.TradingFee = b.tradingFee
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMWithdrawBuilder provides a fluent interface for building AMMWithdraw transactions.
type AMMWithdrawBuilder struct {
	account   *jtx.Account
	asset     tx.Asset
	asset2    tx.Asset
	amount    *tx.Amount
	amount2   *tx.Amount
	lpTokenIn *tx.Amount
	ePrice    *tx.Amount
	fee       string
	flags     uint32
}

// AMMWithdraw creates a new AMMWithdrawBuilder.
func AMMWithdraw(account *jtx.Account, asset, asset2 tx.Asset) *AMMWithdrawBuilder {
	return &AMMWithdrawBuilder{
		account: account,
		asset:   asset,
		asset2:  asset2,
		fee:     "10",
	}
}

// Amount sets the first withdrawal amount.
func (b *AMMWithdrawBuilder) Amount(amt tx.Amount) *AMMWithdrawBuilder {
	b.amount = &amt
	return b
}

// Amount2 sets the second withdrawal amount.
func (b *AMMWithdrawBuilder) Amount2(amt tx.Amount) *AMMWithdrawBuilder {
	b.amount2 = &amt
	return b
}

// LPTokenIn sets the LP tokens to burn.
func (b *AMMWithdrawBuilder) LPTokenIn(amt tx.Amount) *AMMWithdrawBuilder {
	b.lpTokenIn = &amt
	return b
}

// EPrice sets the effective price limit.
func (b *AMMWithdrawBuilder) EPrice(amt tx.Amount) *AMMWithdrawBuilder {
	b.ePrice = &amt
	return b
}

// Fee sets the transaction fee.
func (b *AMMWithdrawBuilder) Fee(fee string) *AMMWithdrawBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMWithdrawBuilder) Flags(flags uint32) *AMMWithdrawBuilder {
	b.flags = flags
	return b
}

// LPToken sets the tfLPToken flag.
func (b *AMMWithdrawBuilder) LPToken() *AMMWithdrawBuilder {
	b.flags |= TfLPToken
	return b
}

// WithdrawAll sets the tfWithdrawAll flag.
func (b *AMMWithdrawBuilder) WithdrawAll() *AMMWithdrawBuilder {
	b.flags |= TfWithdrawAll
	return b
}

// OneAssetWithdrawAll sets the tfOneAssetWithdrawAll flag.
func (b *AMMWithdrawBuilder) OneAssetWithdrawAll() *AMMWithdrawBuilder {
	b.flags |= TfOneAssetWithdrawAll
	return b
}

// SingleAsset sets the tfSingleAsset flag.
func (b *AMMWithdrawBuilder) SingleAsset() *AMMWithdrawBuilder {
	b.flags |= TfSingleAsset
	return b
}

// TwoAsset sets the tfTwoAsset flag.
func (b *AMMWithdrawBuilder) TwoAsset() *AMMWithdrawBuilder {
	b.flags |= TfTwoAsset
	return b
}

// OneAssetLPToken sets the tfOneAssetLPToken flag.
func (b *AMMWithdrawBuilder) OneAssetLPToken() *AMMWithdrawBuilder {
	b.flags |= TfOneAssetLPToken
	return b
}

// LimitLPToken sets the tfLimitLPToken flag.
func (b *AMMWithdrawBuilder) LimitLPToken() *AMMWithdrawBuilder {
	b.flags |= TfLimitLPToken
	return b
}

// Build creates the AMMWithdraw transaction.
func (b *AMMWithdrawBuilder) Build() *amm.AMMWithdraw {
	ammTx := amm.NewAMMWithdraw(b.account.Address, b.asset, b.asset2)
	ammTx.Fee = b.fee
	ammTx.Amount = b.amount
	ammTx.Amount2 = b.amount2
	ammTx.LPTokenIn = b.lpTokenIn
	ammTx.EPrice = b.ePrice
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMVoteBuilder provides a fluent interface for building AMMVote transactions.
type AMMVoteBuilder struct {
	account    *jtx.Account
	asset      tx.Asset
	asset2     tx.Asset
	tradingFee uint16
	fee        string
	flags      uint32
}

// AMMVote creates a new AMMVoteBuilder.
func AMMVote(account *jtx.Account, asset, asset2 tx.Asset, tradingFee uint16) *AMMVoteBuilder {
	return &AMMVoteBuilder{
		account:    account,
		asset:      asset,
		asset2:     asset2,
		tradingFee: tradingFee,
		fee:        "10",
	}
}

// Fee sets the transaction fee.
func (b *AMMVoteBuilder) Fee(fee string) *AMMVoteBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMVoteBuilder) Flags(flags uint32) *AMMVoteBuilder {
	b.flags = flags
	return b
}

// Build creates the AMMVote transaction.
func (b *AMMVoteBuilder) Build() *amm.AMMVote {
	ammTx := amm.NewAMMVote(b.account.Address, b.asset, b.asset2, b.tradingFee)
	ammTx.Fee = b.fee
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMBidBuilder provides a fluent interface for building AMMBid transactions.
type AMMBidBuilder struct {
	account      *jtx.Account
	asset        tx.Asset
	asset2       tx.Asset
	bidMin       *tx.Amount
	bidMax       *tx.Amount
	authAccounts []amm.AuthAccount
	fee          string
	flags        uint32
}

// AMMBid creates a new AMMBidBuilder.
func AMMBid(account *jtx.Account, asset, asset2 tx.Asset) *AMMBidBuilder {
	return &AMMBidBuilder{
		account: account,
		asset:   asset,
		asset2:  asset2,
		fee:     "10",
	}
}

// BidMin sets the minimum bid amount.
func (b *AMMBidBuilder) BidMin(amt tx.Amount) *AMMBidBuilder {
	b.bidMin = &amt
	return b
}

// BidMax sets the maximum bid amount.
func (b *AMMBidBuilder) BidMax(amt tx.Amount) *AMMBidBuilder {
	b.bidMax = &amt
	return b
}

// AuthAccounts sets the authorized accounts for discounted trading.
func (b *AMMBidBuilder) AuthAccounts(accounts ...string) *AMMBidBuilder {
	b.authAccounts = make([]amm.AuthAccount, 0, len(accounts))
	for _, addr := range accounts {
		b.authAccounts = append(b.authAccounts, amm.AuthAccount{
			AuthAccount: amm.AuthAccountData{Account: addr},
		})
	}
	return b
}

// Fee sets the transaction fee.
func (b *AMMBidBuilder) Fee(fee string) *AMMBidBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMBidBuilder) Flags(flags uint32) *AMMBidBuilder {
	b.flags = flags
	return b
}

// Build creates the AMMBid transaction.
func (b *AMMBidBuilder) Build() *amm.AMMBid {
	ammTx := amm.NewAMMBid(b.account.Address, b.asset, b.asset2)
	ammTx.Fee = b.fee
	ammTx.BidMin = b.bidMin
	ammTx.BidMax = b.bidMax
	ammTx.AuthAccounts = b.authAccounts
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMDeleteBuilder provides a fluent interface for building AMMDelete transactions.
type AMMDeleteBuilder struct {
	account *jtx.Account
	asset   tx.Asset
	asset2  tx.Asset
	fee     string
	flags   uint32
}

// AMMDelete creates a new AMMDeleteBuilder.
func AMMDelete(account *jtx.Account, asset, asset2 tx.Asset) *AMMDeleteBuilder {
	return &AMMDeleteBuilder{
		account: account,
		asset:   asset,
		asset2:  asset2,
		fee:     "10",
	}
}

// Fee sets the transaction fee.
func (b *AMMDeleteBuilder) Fee(fee string) *AMMDeleteBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMDeleteBuilder) Flags(flags uint32) *AMMDeleteBuilder {
	b.flags = flags
	return b
}

// Build creates the AMMDelete transaction.
func (b *AMMDeleteBuilder) Build() *amm.AMMDelete {
	ammTx := amm.NewAMMDelete(b.account.Address, b.asset, b.asset2)
	ammTx.Fee = b.fee
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// AMMClawbackBuilder provides a fluent interface for building AMMClawback transactions.
type AMMClawbackBuilder struct {
	account *jtx.Account
	holder  string
	asset   tx.Asset
	asset2  tx.Asset
	amount  *tx.Amount
	fee     string
	flags   uint32
}

// AMMClawback creates a new AMMClawbackBuilder.
func AMMClawback(account *jtx.Account, holder string, asset, asset2 tx.Asset) *AMMClawbackBuilder {
	return &AMMClawbackBuilder{
		account: account,
		holder:  holder,
		asset:   asset,
		asset2:  asset2,
		fee:     "10",
	}
}

// Amount sets the clawback amount.
func (b *AMMClawbackBuilder) Amount(amt tx.Amount) *AMMClawbackBuilder {
	b.amount = &amt
	return b
}

// Fee sets the transaction fee.
func (b *AMMClawbackBuilder) Fee(fee string) *AMMClawbackBuilder {
	b.fee = fee
	return b
}

// Flags sets the transaction flags.
func (b *AMMClawbackBuilder) Flags(flags uint32) *AMMClawbackBuilder {
	b.flags = flags
	return b
}

// ClawTwoAssets sets the tfClawTwoAssets flag.
func (b *AMMClawbackBuilder) ClawTwoAssets() *AMMClawbackBuilder {
	b.flags |= TfClawTwoAssets
	return b
}

// Build creates the AMMClawback transaction.
func (b *AMMClawbackBuilder) Build() *amm.AMMClawback {
	ammTx := amm.NewAMMClawback(b.account.Address, b.holder, b.asset, b.asset2)
	ammTx.Fee = b.fee
	ammTx.Amount = b.amount
	if b.flags != 0 {
		ammTx.SetFlags(b.flags)
	}
	return ammTx
}

// Transaction flags for AMM operations
const (
	// AMMDeposit flags
	TfLPToken         uint32 = 0x00010000
	TfSingleAsset     uint32 = 0x00080000
	TfTwoAsset        uint32 = 0x00100000
	TfOneAssetLPToken uint32 = 0x00200000
	TfLimitLPToken    uint32 = 0x00400000
	TfTwoAssetIfEmpty uint32 = 0x00800000

	// AMMWithdraw flags
	TfWithdrawAll         uint32 = 0x00020000
	TfOneAssetWithdrawAll uint32 = 0x00040000

	// AMMClawback flags
	TfClawTwoAssets uint32 = 0x00000001
)
