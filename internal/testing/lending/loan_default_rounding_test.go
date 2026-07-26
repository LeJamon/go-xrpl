package lending_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestLoanManageDefaultRoundsLiquidationCoverUpward(t *testing.T) {
	env := newLendingEnv(t)
	owner := jtx.NewAccount("owner")
	borrower := jtx.NewAccount("borrower")
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSeq := env.Seq(owner)
	vaultID := setupXRPVault(t, env, owner, 50_325_985)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSeq)

	brokerSeq := env.Seq(owner)
	brokerSet := lending.NewLoanBrokerSet(owner.Address, vaultID)
	managementFeeRate := uint16(1_000)
	coverRateMinimum := uint32(10_000)
	coverRateLiquidation := uint32(5_000)
	brokerSet.ManagementFeeRate = &managementFeeRate
	brokerSet.CoverRateMinimum = &coverRateMinimum
	brokerSet.CoverRateLiquidation = &coverRateLiquidation
	jtx.RequireTxSuccess(t, env.Submit(brokerSet))

	brokerID := brokerID(owner, brokerSeq)
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSeq)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(
		owner.Address,
		brokerID,
		tx.NewXRPAmount(10_000_000),
	)))

	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "20000000")
	loanOriginationFee := "1000000"
	loanServiceFee := "500000"
	interestRate := uint32(5_000)
	paymentInterval := uint32(61)
	paymentTotal := uint32(3)
	gracePeriod := uint32(60)
	loanSet.LoanOriginationFee = &loanOriginationFee
	loanSet.LoanServiceFee = &loanServiceFee
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &paymentInterval
	loanSet.PaymentTotal = &paymentTotal
	loanSet.GracePeriod = &gracePeriod
	loanSet.Counterparty = owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
	signature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(owner.PublicKeyHex()),
		"00"+strings.ToUpper(owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign LoanSet: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, env.Submit(loanSet))

	loanKey := keylet.Loan(brokerKey.Key, 1)
	loan := decodeLendingEntry(t, env, loanKey)
	assertLendingField(t, "Loan", loan, "PrincipalOutstanding", "20000000")
	assertLendingField(t, "Loan", loan, "TotalValueOutstanding", "20000004")
	assertLendingField(t, "Loan", loan, "PaymentRemaining", uint32(3))

	broker := decodeLendingEntry(t, env, brokerKey)
	assertLendingField(t, "LoanBroker", broker, "DebtTotal", "20000004")
	assertLendingField(t, "LoanBroker", broker, "CoverAvailable", "10000000")

	vault := decodeLendingEntry(t, env, vaultKey)
	assertLendingField(t, "Vault", vault, "AssetsTotal", "50325989")
	assertLendingField(t, "Vault", vault, "AssetsAvailable", "30325985")

	brokerAccount := lendingAccountID(t, broker, "LoanBroker")
	vaultAccount := lendingAccountID(t, vault, "Vault")
	assertLendingAccountBalance(t, env, brokerAccount, 10_000_000)
	assertLendingAccountBalance(t, env, vaultAccount, 30_325_985)

	nextPaymentDueDate, ok := loan["NextPaymentDueDate"].(uint32)
	if !ok {
		t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
	}
	env.CloseToParentCloseTime(nextPaymentDueDate + gracePeriod)

	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	manage := lending.NewLoanManage(owner.Address, loanID)
	flags := lending.TfLoanDefault
	manage.GetCommon().Flags = &flags
	jtx.RequireTxSuccess(t, env.Submit(manage))

	broker = decodeLendingEntry(t, env, brokerKey)
	assertLendingField(t, "LoanBroker", broker, "CoverAvailable", "9899999")
	vault = decodeLendingEntry(t, env, vaultKey)
	assertLendingField(t, "Vault", vault, "AssetsTotal", "30425986")
	assertLendingField(t, "Vault", vault, "AssetsAvailable", "30425986")
	assertLendingAccountBalance(t, env, brokerAccount, 9_899_999)
	assertLendingAccountBalance(t, env, vaultAccount, 30_425_986)
}

func decodeLendingEntry(t *testing.T, env *jtx.TestEnv, key keylet.Keylet) map[string]any {
	t.Helper()
	data, err := env.LedgerEntry(key)
	if err != nil {
		t.Fatalf("read ledger entry: %v", err)
	}
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		t.Fatalf("decode ledger entry: %v", err)
	}
	return fields
}

func assertLendingField(t *testing.T, name string, fields map[string]any, field string, want any) {
	t.Helper()
	if got := fields[field]; got != want {
		t.Fatalf("%s %s = %v, want %v", name, field, got, want)
	}
}

func lendingAccountID(t *testing.T, fields map[string]any, name string) [20]byte {
	t.Helper()
	address, ok := fields["Account"].(string)
	if !ok {
		t.Fatalf("%s Account = %v, want address", name, fields["Account"])
	}
	id, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("decode %s Account: %v", name, err)
	}
	return id
}

func assertLendingAccountBalance(t *testing.T, env *jtx.TestEnv, accountID [20]byte, want uint64) {
	t.Helper()
	data, err := env.LedgerEntry(keylet.Account(accountID))
	if err != nil {
		t.Fatalf("read AccountRoot: %v", err)
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("decode AccountRoot: %v", err)
	}
	if account.Balance != want {
		t.Fatalf("AccountRoot Balance = %d, want %d", account.Balance, want)
	}
}
