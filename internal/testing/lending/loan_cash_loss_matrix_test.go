package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
)

type lossLifecycleFixture struct {
	env                          *jtx.TestEnv
	owner, borrower              *jtx.Account
	vaultKey, brokerKey, loanKey keylet.Keylet
	loanID, brokerID             string
}

func newLegacyLendingEnvWithCleanup(t *testing.T, cleanup bool) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()
	return env
}

func newLossLifecycleFixture(t *testing.T, cash, cleanup bool) *lossLifecycleFixture {
	t.Helper()
	var env *jtx.TestEnv
	if cash {
		env = newCashLendingEnvWithCleanup(t, cleanup)
	} else {
		env = newLegacyLendingEnvWithCleanup(t, cleanup)
	}
	owner := jtx.NewAccount(fmt.Sprintf("loss-%t-%t-owner", cash, cleanup))
	borrower := jtx.NewAccount(fmt.Sprintf("loss-%t-%t-borrower", cash, cleanup))
	env.FundAmount(owner, 10_000_000_000)
	env.FundAmount(borrower, 10_000_000_000)

	vaultSequence := env.Seq(owner)
	vaultID := setupXRPVault(t, env, owner, 10_000_000)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	if !cash {
		env.EnableFeature("LendingProtocolV1_1")
		env.Close()
	}

	brokerSequence := env.Seq(owner)
	brokerSet := lending.NewLoanBrokerSet(owner.Address, vaultID)
	managementFeeRate := uint16(1_000)
	coverRateMinimum := uint32(10_000)
	coverRateLiquidation := uint32(2_500)
	brokerSet.ManagementFeeRate = &managementFeeRate
	brokerSet.CoverRateMinimum = &coverRateMinimum
	brokerSet.CoverRateLiquidation = &coverRateLiquidation
	jtx.RequireTxSuccess(t, env.Submit(brokerSet))
	brokerID := brokerID(owner, brokerSequence)
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerCoverDeposit(
		owner.Address,
		brokerID,
		tx.NewXRPAmount(1_000),
	)))

	loanSet := lending.NewLoanSet(borrower.Address, brokerID, "1000")
	interestRate := uint32(10_000)
	interval := uint32(60)
	payments := uint32(3)
	grace := uint32(60)
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	loanSet.GracePeriod = &grace
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
	return &lossLifecycleFixture{
		env:       env,
		owner:     owner,
		borrower:  borrower,
		vaultKey:  vaultKey,
		brokerKey: brokerKey,
		loanKey:   loanKey,
		loanID:    strings.ToUpper(hex.EncodeToString(loanKey.Key[:])),
		brokerID:  brokerID,
	}
}

func lossNumber(t *testing.T, fields map[string]any, field string) state.XRPLNumber {
	t.Helper()
	raw, _ := fields[field].(string)
	if raw == "" {
		raw = "0"
	}
	number, err := state.ParseXRPLNumber(raw, state.MantissaScaleLarge, state.RoundToNearest)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", field, raw, err)
	}
	return number
}

func requireLossNumberEqual(t *testing.T, name string, got, want state.XRPLNumber) {
	t.Helper()
	if got.Cmp(want) != 0 {
		t.Fatalf("%s = %s, want %s", name, got.String(), want.String())
	}
}

func submitLoanManage(t *testing.T, f *lossLifecycleFixture, flags uint32) {
	t.Helper()
	manage := lending.NewLoanManage(f.owner.Address, f.loanID)
	manage.GetCommon().Flags = &flags
	jtx.RequireTxSuccess(t, f.env.Submit(manage))
}

func TestLoanManageCashAndLegacyImpairmentTransitions(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("cash-%t/cleanup-%t", cash, cleanup), func(t *testing.T) {
				f := newLossLifecycleFixture(t, cash, cleanup)
				loanBefore := decodeLendingEntry(t, f.env, f.loanKey)
				exposure := lossNumber(t, loanBefore, "PrincipalOutstanding")
				if !cash {
					exposure = lossNumber(t, loanBefore, "TotalValueOutstanding").Sub(lossNumber(t, loanBefore, "ManagementFeeOutstanding"))
				}

				submitLoanManage(t, f, lending.TfLoanImpair)
				vaultAfterImpair := decodeLendingEntry(t, f.env, f.vaultKey)
				requireLossNumberEqual(t, "LossUnrealized after impair", lossNumber(t, vaultAfterImpair, "LossUnrealized"), exposure)
				loanAfterImpair := decodeLendingEntry(t, f.env, f.loanKey)
				if flags, ok := loanAfterImpair["Flags"].(uint32); !ok || flags&lending.LsfLoanImpaired == 0 {
					t.Fatalf("Loan Flags after impair = %v, want impaired", loanAfterImpair["Flags"])
				}

				submitLoanManage(t, f, lending.TfLoanUnimpair)
				vaultAfterUnimpair := decodeLendingEntry(t, f.env, f.vaultKey)
				requireLossNumberEqual(t, "LossUnrealized after unimpair", lossNumber(t, vaultAfterUnimpair, "LossUnrealized"), state.NewXRPLNumber(0, 0))

				submitLoanManage(t, f, lending.TfLoanImpair)
				vaultAfterReimpair := decodeLendingEntry(t, f.env, f.vaultKey)
				requireLossNumberEqual(t, "LossUnrealized after reimpair", lossNumber(t, vaultAfterReimpair, "LossUnrealized"), exposure)
				impairedLoan := decodeLendingEntry(t, f.env, f.loanKey)
				nextDue, ok := impairedLoan["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("impaired Loan NextPaymentDueDate = %v, want uint32", impairedLoan["NextPaymentDueDate"])
				}
				f.env.CloseToParentCloseTime(nextDue + 61)
				pay := lending.NewLoanPay(f.borrower.Address, f.loanID, tx.NewXRPAmount(2_000))
				payFlags := lending.TfLoanFullPayment
				pay.GetCommon().Flags = &payFlags
				paymentResult := f.env.Submit(pay)
				if !paymentResult.Success {
					t.Fatalf("LoanPay after impairment: %s (now=%d due=%d)", paymentResult.Code, f.env.NowRipple(), nextDue)
				}
				vaultAfterPayment := decodeLendingEntry(t, f.env, f.vaultKey)
				requireLossNumberEqual(t, "LossUnrealized after payment", lossNumber(t, vaultAfterPayment, "LossUnrealized"), state.NewXRPLNumber(0, 0))
			})
		}
	}
}

func TestLoanManageDefaultUsesExposureAndPartialCover(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("cash-%t/cleanup-%t", cash, cleanup), func(t *testing.T) {
				f := newLossLifecycleFixture(t, cash, cleanup)
				loanBefore := decodeLendingEntry(t, f.env, f.loanKey)
				exposure := lossNumber(t, loanBefore, "PrincipalOutstanding")
				if !cash {
					exposure = lossNumber(t, loanBefore, "TotalValueOutstanding").Sub(lossNumber(t, loanBefore, "ManagementFeeOutstanding"))
				}
				submitLoanManage(t, f, lending.TfLoanImpair)
				loanImpaired := decodeLendingEntry(t, f.env, f.loanKey)
				nextDue, ok := loanImpaired["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("impaired Loan NextPaymentDueDate = %v, want uint32", loanImpaired["NextPaymentDueDate"])
				}
				vaultBefore := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerBefore := decodeLendingEntry(t, f.env, f.brokerKey)
				f.env.CloseToParentCloseTime(nextDue + 61)
				submitLoanManage(t, f, lending.TfLoanDefault)

				vaultAfter := decodeLendingEntry(t, f.env, f.vaultKey)
				brokerAfter := decodeLendingEntry(t, f.env, f.brokerKey)
				covered := lossNumber(t, brokerBefore, "CoverAvailable").Sub(lossNumber(t, brokerAfter, "CoverAvailable"))
				if covered.Signum() <= 0 || covered.Cmp(exposure) >= 0 {
					t.Fatalf("default cover = %s, want positive partial cover below exposure %s", covered.String(), exposure.String())
				}
				requireLossNumberEqual(t, "LoanBroker DebtTotal delta", lossNumber(t, brokerAfter, "DebtTotal").Sub(lossNumber(t, brokerBefore, "DebtTotal")), exposure.Negate())
				requireLossNumberEqual(t, "Vault AssetsTotal delta", lossNumber(t, vaultAfter, "AssetsTotal").Sub(lossNumber(t, vaultBefore, "AssetsTotal")), covered.Sub(exposure))
				requireLossNumberEqual(t, "Vault AssetsAvailable delta", lossNumber(t, vaultAfter, "AssetsAvailable").Sub(lossNumber(t, vaultBefore, "AssetsAvailable")), covered)
				requireLossNumberEqual(t, "LossUnrealized after default", lossNumber(t, vaultAfter, "LossUnrealized"), state.NewXRPLNumber(0, 0))
				loanAfter := decodeLendingEntry(t, f.env, f.loanKey)
				if flags, ok := loanAfter["Flags"].(uint32); !ok || flags&lending.LsfLoanDefault == 0 {
					t.Fatalf("Loan Flags after default = %v, want default", loanAfter["Flags"])
				}

				deleteTx := lending.NewLoanDelete(f.owner.Address, f.loanID)
				jtx.RequireTxSuccess(t, f.env.Submit(deleteTx))
				if f.env.LedgerEntryExists(f.loanKey) {
					t.Fatal("defaulted Loan still exists after LoanDelete")
				}
				if debt := lossNumber(t, decodeLendingEntry(t, f.env, f.brokerKey), "DebtTotal"); debt.Signum() != 0 {
					t.Fatalf("LoanBroker DebtTotal after deleting only loan = %s, want zero", debt.String())
				}
			})
		}
	}
}

func TestLegacyLoanCreatedBeforeLendingProtocolV11KeepsAccrual(t *testing.T) {
	f := newLossLifecycleFixture(t, false, true)
	vault := decodeLendingEntry(t, f.env, f.vaultKey)
	if _, present := vault["LEVersion"]; present {
		t.Fatalf("legacy vault unexpectedly has LEVersion=%v", vault["LEVersion"])
	}
	loan := decodeLendingEntry(t, f.env, f.loanKey)
	if lossNumber(t, vault, "AssetsTotal").Cmp(state.NewXRPLNumberScaled(10_000_000, 0, state.MantissaScaleLarge, state.RoundToNearest)) <= 0 {
		t.Fatalf("legacy origination AssetsTotal = %s, want interest accrual", vault["AssetsTotal"])
	}
	if lossNumber(t, loan, "TotalValueOutstanding").Cmp(lossNumber(t, loan, "PrincipalOutstanding")) <= 0 {
		t.Fatalf("legacy loan TotalValueOutstanding = %s, want above principal %s", loan["TotalValueOutstanding"], loan["PrincipalOutstanding"])
	}
}
