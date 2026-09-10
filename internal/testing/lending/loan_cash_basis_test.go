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

func newCashLendingEnv(t *testing.T) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	env.EnableFeature("LendingProtocolV1_1")
	env.Close()
	return env
}

func setupCashXRPVault(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, deposit uint64) string {
	t.Helper()
	sequence := env.Seq(owner)
	kind := vault.VaultKindClosedEnded
	subscription := env.NowRipple() + 1
	redemption := subscription + 7*24*60*60
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Fee = reserveIncrement
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	jtx.RequireTxSuccess(t, env.Submit(create))
	id := vaultID(owner, sequence)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, id, tx.NewXRPAmount(int64(deposit)))))
	env.CloseToParentCloseTime(subscription + 1)
	return id
}

func TestCashBasisLoanSetAndLoanPayXRP(t *testing.T) {
	env := newCashLendingEnv(t)
	owner := jtx.NewAccount("cash-owner")
	borrower := jtx.NewAccount("cash-borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSequence := env.Seq(owner)
	vaultID := setupCashXRPVault(t, env, owner, 10_000_000)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	createdVault := decodeLendingEntry(t, env, vaultKey)
	assertLendingField(t, "Vault", createdVault, "LEVersion", int(vault.VaultVersionCashBasis))

	brokerSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
	brokerID := brokerID(owner, brokerSequence)
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "1000")
	interval := uint32(60)
	payments := uint32(2)
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	signature, err := txsign.SignCounterpartyWithRules(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
		env.Ledger().Rules(),
	)
	if err != nil {
		t.Fatalf("sign counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	loanKey := keylet.Loan(brokerKey.Key, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "10000000")
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9999000")
	assertLendingField(t, "LoanBroker", decodeLendingEntry(t, env, brokerKey), "DebtTotal", "1000")

	loan := decodeLendingEntry(t, env, loanKey)
	assertLendingField(t, "Loan", loan, "TotalValueOutstanding", "1000")
	nextPaymentDue, ok := loan["NextPaymentDueDate"].(uint32)
	if !ok {
		t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
	}
	env.CloseToParentCloseTime(nextPaymentDue - 10)

	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanPay(
		borrower.Address,
		loanID,
		tx.NewXRPAmount(500),
	)))
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "10000000")
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9999500")
	assertLendingField(t, "LoanBroker", decodeLendingEntry(t, env, brokerKey), "DebtTotal", "500")
}
