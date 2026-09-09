package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	paytest "github.com/LeJamon/go-xrpl/internal/testing/payment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
)

type cashPaymentObservation struct {
	assetsDelta       state.XRPLNumber
	debtDelta         state.XRPLNumber
	principalPaid     state.XRPLNumber
	loanValuePaid     state.XRPLNumber
	managementFeePaid state.XRPLNumber
	loanPaymentRemain uint32
}

func matrixNumber(t *testing.T, fields map[string]any, field string) state.XRPLNumber {
	t.Helper()
	raw, ok := fields[field].(string)
	if !ok || raw == "" {
		raw = "0"
	}
	number, err := state.ParseXRPLNumber(raw, state.MantissaScaleLarge, state.RoundToNearest)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", field, raw, err)
	}
	return number
}

func matrixAmount(t *testing.T, f *loanSetAssetFixture, number state.XRPLNumber) tx.Amount {
	t.Helper()
	context := state.NewNumberContext(state.MantissaScaleLarge, true)
	var prototype state.Amount
	if f.asset.IsMPT() {
		prototype = state.NewMPTAmountWithIssuanceID(1, "", f.asset.MPTIssuanceID)
	} else {
		prototype = state.NewIssuedAmountFromFloat64(1, f.asset.Currency, f.asset.Issuer)
	}
	return context.ToAmount(number, prototype, state.RoundToNearest)
}

func createMatrixLoan(t *testing.T, f *loanSetAssetFixture, overpayment bool) (string, keylet.Keylet) {
	t.Helper()
	f.createHolding(f.owner)
	loanSet := lending.NewLoanSet(f.borrower.Address, f.brokerID, "1000")
	interestRate := uint32(10_000)
	interval := uint32(600)
	payments := uint32(4)
	grace := uint32(300)
	loanSet.InterestRate = &interestRate
	loanSet.PaymentInterval = &interval
	loanSet.PaymentTotal = &payments
	loanSet.GracePeriod = &grace
	if overpayment {
		flags := lending.TfLoanOverpayment
		loanSet.GetCommon().Flags = &flags
	}
	loanSet.Counterparty = f.owner.Address
	loanSet.GetCommon().Fee = "20"
	loanSet.GetCommon().SigningPubKey = strings.ToUpper(f.borrower.PublicKeyHex())
	signature, err := txsign.SignCounterparty(
		loanSet,
		strings.ToUpper(f.owner.PublicKeyHex()),
		"00"+strings.ToUpper(f.owner.PrivateKeyHex()),
	)
	if err != nil {
		t.Fatalf("sign LoanSet counterparty: %v", err)
	}
	loanSet.GetCommon().CounterpartySignature = signature
	jtx.RequireTxSuccess(t, f.env.Submit(loanSet))

	loanKey := keylet.Loan(f.brokerKey, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	return loanID, loanKey
}

func runCashPaymentScenario(t *testing.T, f *loanSetAssetFixture, scenario string) cashPaymentObservation {
	t.Helper()
	loanID, loanKey := createMatrixLoan(t, f, scenario == "overpayment")
	vaultKey := f.vaultKey
	brokerKey := keylet.LoanBrokerByID(f.brokerKey)
	loanBefore := decodeLendingEntry(t, f.env, loanKey)
	vaultBefore := decodeLendingEntry(t, f.env, vaultKey)
	brokerBefore := decodeLendingEntry(t, f.env, brokerKey)
	periodic := matrixNumber(t, loanBefore, "PeriodicPayment")
	context := state.NewNumberContext(state.MantissaScaleLarge, true)
	var payment state.XRPLNumber
	flags := uint32(0)
	switch scenario {
	case "regular":
		payment = periodic.Add(context.Int(1))
	case "late":
		nextDue, ok := loanBefore["NextPaymentDueDate"].(uint32)
		if !ok {
			t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loanBefore["NextPaymentDueDate"])
		}
		f.env.CloseToParentCloseTime(nextDue + 1)
		payment = periodic.Mul(context.Int(3)).Add(context.Int(10))
		flags = lending.TfLoanLatePayment
	case "full":
		payment = matrixNumber(t, loanBefore, "PrincipalOutstanding").Mul(context.Int(3))
		flags = lending.TfLoanFullPayment
	case "overpayment":
		payment = periodic.Mul(context.Int(2)).Add(context.Int(10))
		flags = lending.TfLoanOverpayment
	default:
		t.Fatalf("unknown payment scenario %q", scenario)
	}
	if scenario == "full" {
		f.fundHolding(f.borrower, 3_000)
	}
	paymentTx := lending.NewLoanPay(f.borrower.Address, loanID, matrixAmount(t, f, payment))
	if flags != 0 {
		paymentTx.GetCommon().Flags = &flags
	}
	jtx.RequireTxSuccess(t, f.env.Submit(paymentTx))

	loanAfter := decodeLendingEntry(t, f.env, loanKey)
	vaultAfter := decodeLendingEntry(t, f.env, vaultKey)
	brokerAfter := decodeLendingEntry(t, f.env, brokerKey)
	loanValuePaid := matrixNumber(t, loanBefore, "TotalValueOutstanding").Sub(matrixNumber(t, loanAfter, "TotalValueOutstanding"))
	principalPaid := matrixNumber(t, loanBefore, "PrincipalOutstanding").Sub(matrixNumber(t, loanAfter, "PrincipalOutstanding"))
	managementFeePaid := matrixNumber(t, loanBefore, "ManagementFeeOutstanding").Sub(matrixNumber(t, loanAfter, "ManagementFeeOutstanding"))
	remaining := uint32(0)
	if value, present := loanAfter["PaymentRemaining"]; present {
		var ok bool
		remaining, ok = value.(uint32)
		if !ok {
			t.Fatalf("Loan PaymentRemaining = %v, want uint32", value)
		}
	}
	return cashPaymentObservation{
		assetsDelta:       matrixNumber(t, vaultAfter, "AssetsTotal").Sub(matrixNumber(t, vaultBefore, "AssetsTotal")),
		debtDelta:         matrixNumber(t, brokerAfter, "DebtTotal").Sub(matrixNumber(t, brokerBefore, "DebtTotal")),
		principalPaid:     principalPaid,
		loanValuePaid:     loanValuePaid,
		managementFeePaid: managementFeePaid,
		loanPaymentRemain: remaining,
	}
}

func requireMatrixNumberEqual(t *testing.T, name string, got, want state.XRPLNumber) {
	t.Helper()
	if got.Cmp(want) != 0 {
		t.Fatalf("%s = %s, want %s", name, got.String(), want.String())
	}
}

func TestLoanPaymentAccountingMatchesCashAndLegacyModels(t *testing.T) {
	for _, kind := range []string{"IOU", "MPT"} {
		for _, scenario := range []string{"regular", "late", "full", "overpayment"} {
			t.Run(fmt.Sprintf("%s/%s", kind, scenario), func(t *testing.T) {
				legacy := runCashPaymentScenario(t, newLoanSetAssetFixture(t, kind), scenario)
				cash := runCashPaymentScenario(t, newCashLoanSetAssetFixture(t, kind), scenario)
				expectedRemaining := map[string]uint32{
					"regular":     3,
					"late":        3,
					"full":        0,
					"overpayment": 2,
				}[scenario]
				for model, observation := range map[string]cashPaymentObservation{"legacy": legacy, "cash": cash} {
					if observation.principalPaid.Signum() <= 0 {
						t.Fatalf("%s %s principal paid = %s, want positive", model, scenario, observation.principalPaid.String())
					}
					if observation.loanPaymentRemain != expectedRemaining {
						t.Fatalf("%s %s PaymentRemaining = %d, want %d", model, scenario, observation.loanPaymentRemain, expectedRemaining)
					}
				}

				requireMatrixNumberEqual(t, "principal paid parity", cash.principalPaid, legacy.principalPaid)
				requireMatrixNumberEqual(t, "loan value paid parity", cash.loanValuePaid, legacy.loanValuePaid)
				requireMatrixNumberEqual(t, "management fee paid parity", cash.managementFeePaid, legacy.managementFeePaid)
				if cash.loanPaymentRemain != legacy.loanPaymentRemain {
					t.Fatalf("PaymentRemaining parity = %d/%d", cash.loanPaymentRemain, legacy.loanPaymentRemain)
				}

				trackedInterest := legacy.loanValuePaid.Sub(legacy.principalPaid).Sub(legacy.managementFeePaid)
				if trackedInterest.Signum() <= 0 {
					t.Fatalf("%s recognized interest = %s, want positive", scenario, trackedInterest.String())
				}
				requireMatrixNumberEqual(t, "legacy DebtTotal delta", legacy.debtDelta, legacy.principalPaid.Add(trackedInterest).Negate())
				requireMatrixNumberEqual(t, "cash DebtTotal delta", cash.debtDelta, cash.principalPaid.Negate())
				requireMatrixNumberEqual(t, "cash AssetsTotal delta", cash.assetsDelta, legacy.assetsDelta.Add(trackedInterest))
			})
		}
	}
}

func newCashLendingEnvWithCleanup(t *testing.T, cleanup bool) *jtx.TestEnv {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV1")
	env.EnableFeature("LendingProtocol")
	env.EnableFeature("LendingProtocolV1_1")
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()
	return env
}

func newPaymentLegacyLendingEnvWithCleanup(t *testing.T, cleanup bool) *jtx.TestEnv {
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

func setupClosedEndedXRPVaultForAccounting(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, deposit uint64) string {
	t.Helper()
	sequence := env.Seq(owner)
	subscription := env.NowRipple() + 60
	redemption := subscription + 100_000
	kind := vault.VaultKindClosedEnded
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = reserveIncrement
	create.VaultKind = &kind
	create.SubscriptionDate = &subscription
	create.RedemptionDate = &redemption
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultID := vaultID(owner, sequence)
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vaultID, tx.NewXRPAmount(int64(deposit)))))
	// Deposit during subscription, then exercise the lending transactions in
	// investment. The redemption date leaves enough room for every test loan.
	env.CloseToParentCloseTime(subscription + 1)
	return vaultID
}

func TestCashBasisVaultSetDataUpdatePreservesExistingCap(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		t.Run(fmt.Sprintf("cleanup-%t", cleanup), func(t *testing.T) {
			env := newCashLendingEnvWithCleanup(t, cleanup)
			owner := jtx.NewAccount("cap-owner")
			borrower := jtx.NewAccount("cap-borrower")
			env.FundAmount(owner, 10_000_000_000)
			env.FundAmount(borrower, 10_000_000_000)

			vaultSequence := env.Seq(owner)
			vaultID := setupClosedEndedXRPVaultForAccounting(t, env, owner, 10_000_000)
			vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
			maximum := "10000000"
			setMaximum := vault.NewVaultSet(owner.Address, vaultID)
			setMaximum.AssetsMaximum = &maximum
			jtx.RequireTxSuccess(t, env.Submit(setMaximum))

			brokerSequence := env.Seq(owner)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
			brokerID := brokerID(owner, brokerSequence)
			brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

			loanSet := lending.NewLoanSet(borrower.Address, brokerID, "10000")
			interestRate := uint32(10_000)
			interval := uint32(60)
			payments := uint32(2)
			loanSet.InterestRate = &interestRate
			loanSet.PaymentInterval = &interval
			loanSet.PaymentTotal = &payments
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
			loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
			loan := decodeLendingEntry(t, env, loanKey)
			nextDue, ok := loan["NextPaymentDueDate"].(uint32)
			if !ok {
				t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
			}
			env.CloseToParentCloseTime(nextDue - 10)
			jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanPay(
				borrower.Address,
				loanID,
				tx.NewXRPAmount(6_000),
			)))
			vaultAfter := decodeLendingEntry(t, env, vaultKey)
			if matrixNumber(t, vaultAfter, "AssetsTotal").Cmp(matrixNumber(t, vaultAfter, "AssetsMaximum")) <= 0 {
				t.Fatalf("cash payment did not exceed AssetsMaximum: total=%s maximum=%s", vaultAfter["AssetsTotal"], vaultAfter["AssetsMaximum"])
			}

			data := "AA"
			dataUpdate := vault.NewVaultSet(owner.Address, vaultID)
			dataUpdate.Data = data
			dataResult := env.Submit(dataUpdate)
			if cleanup {
				jtx.RequireTxSuccess(t, dataResult)
			} else {
				jtx.RequireTxClaimed(t, dataResult, jtx.TecINVARIANT_FAILED)
			}

			explicitCap := vault.NewVaultSet(owner.Address, vaultID)
			explicitCap.AssetsMaximum = &maximum
			jtx.RequireTxClaimed(t, env.Submit(explicitCap), "tecLIMIT_EXCEEDED")
		})
	}
}

func TestLoanPayConservesXRPWhenFeePayeeIsBelowReserve(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("cash-%t/cleanup-%t", cash, cleanup), func(t *testing.T) {
				var env *jtx.TestEnv
				if cash {
					env = newCashLendingEnvWithCleanup(t, cleanup)
				} else {
					env = newPaymentLegacyLendingEnvWithCleanup(t, cleanup)
				}
				owner := jtx.NewAccount(fmt.Sprintf("conservation-%t-%t-owner", cash, cleanup))
				borrower := jtx.NewAccount(fmt.Sprintf("conservation-%t-%t-borrower", cash, cleanup))
				issuer := jtx.NewAccount(fmt.Sprintf("conservation-%t-%t-issuer", cash, cleanup))
				env.FundAmount(owner, 10_000_000_000)
				env.FundAmount(borrower, 10_000_000_000)
				env.FundAmount(issuer, 10_000_000_000)

				vaultSequence := env.Seq(owner)
				var vaultID string
				if cash {
					vaultID = setupClosedEndedXRPVaultForAccounting(t, env, owner, 10_000_000)
				} else {
					vaultID = setupXRPVault(t, env, owner, 10_000_000)
				}
				vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
				vaultFields := decodeLendingEntry(t, env, vaultKey)
				vaultAccount := jtx.NewAccountWithAddress("conservation-vault", state.EncodeAccountIDSafe(lendingAccountID(t, vaultFields, "Vault")))

				brokerSequence := env.Seq(owner)
				jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
				brokerID := brokerID(owner, brokerSequence)
				brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)

				loanSet := lending.NewLoanSet(borrower.Address, brokerID, "1000")
				serviceFee := "2"
				interestRate := uint32(12_000)
				interval := uint32(3_600)
				payments := uint32(12)
				loanSet.LoanServiceFee = &serviceFee
				loanSet.InterestRate = &interestRate
				loanSet.PaymentInterval = &interval
				loanSet.PaymentTotal = &payments
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
				loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
				loan := decodeLendingEntry(t, env, loanKey)
				nextDue, ok := loan["NextPaymentDueDate"].(uint32)
				if !ok {
					t.Fatalf("Loan NextPaymentDueDate = %v, want uint32", loan["NextPaymentDueDate"])
				}
				reserve := env.ReserveBase() + uint64(env.OwnerCount(owner))*env.ReserveIncrement()
				ownerBalance := env.Balance(owner)
				if ownerBalance <= reserve+env.BaseFee() {
					t.Fatalf("owner balance %d is too small for reserve %d", ownerBalance, reserve)
				}
				jtx.RequireTxSuccess(t, env.Submit(paytest.Pay(owner, issuer, ownerBalance-reserve-env.BaseFee()).Build()))
				env.NoopWithFee(owner, 100)
				env.Close()
				if got := env.Balance(owner); got >= reserve {
					t.Fatalf("fee payee balance = %d, want below reserve %d", got, reserve)
				}

				env.CloseToParentCloseTime(nextDue - 10)
				borrowerBefore := env.Balance(borrower)
				vaultBefore := env.Balance(vaultAccount)
				ownerBefore := env.Balance(owner)
				paymentResult := env.Submit(lending.NewLoanPay(borrower.Address, loanID, tx.NewXRPAmount(100)))
				jtx.RequireTxSuccess(t, paymentResult)
				env.Close()

				borrowerAfter := env.Balance(borrower)
				vaultAfter := env.Balance(vaultAccount)
				ownerAfter := env.Balance(owner)
				if ownerAfter <= ownerBefore {
					t.Fatalf("fee payee balance = %d, want greater than %d", ownerAfter, ownerBefore)
				}
				if ownerAfter >= reserve {
					t.Fatalf("fee payee balance = %d, want below reserve %d", ownerAfter, reserve)
				}
				if got, want := borrowerBefore-paymentResult.Fee+vaultBefore+ownerBefore, borrowerAfter+vaultAfter+ownerAfter; got != want {
					t.Fatalf("XRP funds after LoanPay = %d, want %d", want, got)
				}
			})
		}
	}
}
