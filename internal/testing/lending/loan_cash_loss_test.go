package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestCashBasisLoanManageUsesPrincipalForLossAndDefault(t *testing.T) {
	env := newCashLendingEnv(t)
	owner := jtx.NewAccount("cash-loss-owner")
	borrower := jtx.NewAccount("cash-loss-borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSequence := env.Seq(owner)
	vaultID := setupCashXRPVault(t, env, owner, 10_000_000)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	brokerSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
	brokerID := brokerID(owner, brokerSequence)
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "1000")
	interestRate := uint32(10_000)
	interval := uint32(86_400)
	payments := uint32(2)
	grace := uint32(60)
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	loanSet.GracePeriod = &grace
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
		env.Rules(),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	loanKey := keylet.Loan(brokerKey.Key, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	loan := decodeLendingEntry(t, env, loanKey)
	principal, ok := loan["PrincipalOutstanding"].(string)
	if !ok {
		t.Fatalf("Loan PrincipalOutstanding = %v, want NUMBER string", loan["PrincipalOutstanding"])
	}
	total, ok := loan["TotalValueOutstanding"].(string)
	if !ok {
		t.Fatalf("Loan TotalValueOutstanding = %v, want NUMBER string", loan["TotalValueOutstanding"])
	}
	if total == principal {
		t.Fatalf("cash loss test needs interest, got TotalValueOutstanding=%s PrincipalOutstanding=%s", total, principal)
	}

	impair := lending.NewLoanManage(owner.Address, loanID)
	impairFlags := lending.TfLoanImpair
	impair.GetCommon().Flags = &impairFlags
	jtx.RequireTxSuccess(t, env.Submit(impair))
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "LossUnrealized", principal)

	loan = decodeLendingEntry(t, env, loanKey)
	nextDue, ok := loan["NextPaymentDueDate"].(uint32)
	if !ok {
		t.Fatalf("impaired Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
	}
	env.CloseToParentCloseTime(nextDue + grace)
	defaultTx := lending.NewLoanManage(owner.Address, loanID)
	defaultFlags := lending.TfLoanDefault
	defaultTx.GetCommon().Flags = &defaultFlags
	jtx.RequireTxSuccess(t, env.Submit(defaultTx))

	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "9999000")
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9999000")
	vaultAfter := decodeLendingEntry(t, env, vaultKey)
	if loss, present := vaultAfter["LossUnrealized"]; present && loss != "0" {
		t.Fatalf("Vault LossUnrealized after default = %v, want zero", loss)
	}
	brokerAfter := decodeLendingEntry(t, env, brokerKey)
	if debt, present := brokerAfter["DebtTotal"]; present && debt != "0" {
		t.Fatalf("LoanBroker DebtTotal after default = %v, want zero", debt)
	}
}
