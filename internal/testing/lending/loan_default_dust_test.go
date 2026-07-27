package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanManageDefaultReconcilesIOUDust(t *testing.T) {
	env := newLendingEnv(t)
	issuer := jtx.NewAccount("dust-issuer")
	lender := jtx.NewAccount("dust-lender")
	borrower := jtx.NewAccount("dust-borrower")
	for _, account := range []*jtx.Account{issuer, lender, borrower} {
		env.FundAmount(account, 10_000_000_000)
	}

	limit := tx.NewIssuedAmountFromFloat64(100_000, "USD", issuer.Address)
	env.Trust(lender, limit)
	env.Trust(borrower, limit)
	env.PayIOU(issuer, lender, issuer, "USD", 50_000)
	env.PayIOU(issuer, borrower, issuer, "USD", 50_000)

	vaultSeq := env.Seq(lender)
	create := vault.NewVaultCreate(
		lender.Address,
		tx.Asset{Currency: "USD", Issuer: issuer.Address},
	)
	create.Common.Fee = reserveIncrement
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(lender, vaultSeq)
	vaultKey := keylet.Vault(lender.AccountID(), vaultSeq)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(
		lender.Address,
		vaultID,
		tx.NewIssuedAmountFromFloat64(10_000, "USD", issuer.Address),
	)))

	brokerSeq := env.Seq(lender)
	brokerSet := lending.NewLoanBrokerSet(lender.Address, vaultID)
	debtMaximum := "0"
	managementFeeRate := uint16(100)
	coverRateMinimum := uint32(1_000)
	coverRateLiquidation := uint32(2_500)
	brokerSet.DebtMaximum = &debtMaximum
	brokerSet.ManagementFeeRate = &managementFeeRate
	brokerSet.CoverRateMinimum = &coverRateMinimum
	brokerSet.CoverRateLiquidation = &coverRateLiquidation
	jtx.RequireTxSuccess(t, env.Submit(brokerSet))

	brokerID := brokerID(lender, brokerSeq)
	brokerKey := keylet.LoanBroker(lender.AccountID(), brokerSeq)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(
		lender.Address,
		brokerID,
		tx.NewIssuedAmountFromFloat64(1_000, "USD", issuer.Address),
	)))

	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "100")
	interestRate := uint32(1_922)
	paymentTotal := uint32(5_816)
	paymentInterval := uint32(86400 * 6)
	gracePeriod := uint32(86400 * 5)
	loanSet.InterestRate = &interestRate
	loanSet.PaymentTotal = &paymentTotal
	loanSet.PaymentInterval = &paymentInterval
	loanSet.GracePeriod = &gracePeriod
	loanSet.Counterparty = lender.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	signature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(lender.PublicKeyHex()),
		"00"+strings.ToUpper(lender.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign LoanSet: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	loanKey := keylet.Loan(brokerKey.Key, 1)
	loan := decodeLendingEntry(t, env, loanKey)
	nextPaymentDueDate, ok := loan["NextPaymentDueDate"].(uint32)
	if !ok {
		t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
	}
	env.CloseToParentCloseTime(nextPaymentDueDate + gracePeriod)

	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	manage := lending.NewLoanManage(lender.Address, loanID)
	flags := lending.TfLoanDefault
	manage.GetCommon().Flags = &flags
	jtx.RequireTxSuccess(t, env.Submit(manage))

	vaultEntry := decodeLendingEntry(t, env, vaultKey)
	assetsTotal, ok := vaultEntry["AssetsTotal"].(string)
	if !ok {
		t.Fatalf("Vault AssetsTotal = %v, want issued amount", vaultEntry["AssetsTotal"])
	}
	assetsAvailable, ok := vaultEntry["AssetsAvailable"].(string)
	if !ok {
		t.Fatalf("Vault AssetsAvailable = %v, want issued amount", vaultEntry["AssetsAvailable"])
	}
	if assetsAvailable != assetsTotal {
		t.Fatalf(
			"Vault AssetsAvailable = %s, AssetsTotal = %s after default",
			assetsAvailable,
			assetsTotal,
		)
	}
}
