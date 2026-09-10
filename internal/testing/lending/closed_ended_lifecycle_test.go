package lending_test

import (
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/stretchr/testify/require"
)

type closedEndedLoanFixture struct {
	env        *jtx.TestEnv
	owner      *jtx.Account
	borrower   *jtx.Account
	brokerID   string
	submission uint32
	redemption uint32
}

func newClosedEndedLoanFixture(t *testing.T) closedEndedLoanFixture {
	t.Helper()
	env := newLendingEnv(t)
	env.EnableFeature("LendingProtocolV1_1")
	env.Close()
	owner := jtx.NewAccount("closed-ended-owner")
	borrower := jtx.NewAccount("closed-ended-borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSequence := env.Seq(owner)
	subscription := env.NowRipple() + 60
	redemption := subscription + 180
	kind := vault.VaultKindClosedEnded
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = reserveIncrement
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, vaultSequence)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vaultID, tx.NewXRPAmount(2_000_000_000))))

	brokerSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
	return closedEndedLoanFixture{
		env:        env,
		owner:      owner,
		borrower:   borrower,
		brokerID:   brokerID(owner, brokerSequence),
		submission: subscription,
		redemption: redemption,
	}
}

func closedEndedLoanSet(t *testing.T, f closedEndedLoanFixture, interval uint32) *lending.LoanSet {
	t.Helper()
	loan := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000000")
	total := uint32(1)
	loan.PaymentInterval = &interval
	loan.PaymentTotal = &total
	loan.Counterparty = f.owner.Address
	loan.GetCommon().Fee = "20"
	loan.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loan,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
		f.env.Rules(),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loan.GetCommon().CounterpartySignature = signature
	return loan
}

func TestLoanSetClosedEndedPhaseBoundariesIntegration(t *testing.T) {
	f := newClosedEndedLoanFixture(t)
	f.env.CloseToParentCloseTime(f.submission)
	sequence := f.env.Seq(f.borrower)
	result := f.env.Submit(closedEndedLoanSet(t, f, 60))
	jtx.RequireTxClaimed(t, result, "tecTOO_SOON")
	require.Equal(t, 2*f.env.BaseFee(), result.Fee)
	require.Equal(t, sequence+1, f.env.Seq(f.borrower))

	f.env.CloseToParentCloseTime(f.redemption)
	sequence = f.env.Seq(f.borrower)
	result = f.env.Submit(closedEndedLoanSet(t, f, 60))
	jtx.RequireTxClaimed(t, result, jtx.TecEXPIRED)
	require.Equal(t, 2*f.env.BaseFee(), result.Fee)
	require.Equal(t, sequence+1, f.env.Seq(f.borrower))
}

func TestLoanSetClosedEndedRedemptionBufferIntegration(t *testing.T) {
	f := newClosedEndedLoanFixture(t)
	f.env.CloseToParentCloseTime(f.submission + 1)
	start := f.env.NowRipple()
	interval := f.redemption - uint32(vault.LoanRedemptionBuffer) - start
	if interval < 60 {
		t.Fatalf("exact-buffer interval %d is below the protocol minimum", interval)
	}
	sequence := f.env.Seq(f.borrower)
	result := f.env.Submit(closedEndedLoanSet(t, f, interval))
	jtx.RequireTxSuccess(t, result)
	require.Equal(t, 2*f.env.BaseFee(), result.Fee)
	require.Equal(t, sequence+1, f.env.Seq(f.borrower))

	f = newClosedEndedLoanFixture(t)
	f.env.CloseToParentCloseTime(f.submission + 1)
	start = f.env.NowRipple()
	interval = f.redemption - uint32(vault.LoanRedemptionBuffer) - start + 1
	sequence = f.env.Seq(f.borrower)
	result = f.env.Submit(closedEndedLoanSet(t, f, interval))
	jtx.RequireTxClaimed(t, result, "tecNO_PERMISSION")
	require.Equal(t, 2*f.env.BaseFee(), result.Fee)
	require.Equal(t, sequence+1, f.env.Seq(f.borrower))
}
