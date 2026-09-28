package lending_test

import (
	"encoding/hex"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanPayIntegerScalePostconditions(t *testing.T) {
	for _, model := range []struct {
		name string
		v11  bool
		cash bool
	}{
		{name: "legacy"},
		{name: "upgraded-legacy", v11: true},
		{name: "cash", v11: true, cash: true},
	} {
		for _, cleanup := range []bool{false, true} {
			for _, scenario := range []struct {
				name      string
				principal int64
				interest  uint32
				payment   int64
				full      bool
			}{
				{name: "rounded-principal", principal: 1, interest: 50_000, payment: 1},
				{name: "reducing-principal", principal: 6, payment: 2},
				{name: "full-repayment", principal: 6, payment: 6, full: true},
			} {
				t.Run(fmt.Sprintf("%s/cleanup=%t/%s", model.name, cleanup, scenario.name), func(t *testing.T) {
					env := newTwoHoldingLoanEnv(t, model.cash, cleanup)
					env.EnableFeature("fixCleanup3_2_0")
					env.Close()
					issuer := jtx.NewAccount("rounding-issuer")
					owner := jtx.NewAccount("rounding-owner")
					borrower := jtx.NewAccount("rounding-borrower")
					token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner, borrower}})
					token.Create(mpttest.CreateOpts{Flags: mpttest.TfMPTCanTransfer})
					for _, account := range []*jtx.Account{owner, borrower} {
						token.Authorize(mpttest.AuthorizeOpts{Account: account})
						token.Pay(issuer, account, 10_000)
					}

					const interval = uint32(31_536_000)
					vaultSequence := env.Seq(owner)
					create := vault.NewVaultCreate(owner.Address, tx.Asset{MPTIssuanceID: token.IssuanceID()})
					create.Fee = reserveIncrement
					if model.cash {
						kind := vault.VaultKindClosedEnded
						subscription := env.NowRipple() + 60
						redemption := subscription + 4*interval
						create.VaultKind = &kind
						create.SubscriptionDate = &subscription
						create.RedemptionDate = &redemption
					}
					jtx.RequireTxSuccess(t, env.Submit(create))
					vaultID := vaultID(owner, vaultSequence)
					vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
					jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vaultID, token.MPTAmount(5_000))))
					if model.cash {
						env.CloseToParentCloseTime(*create.SubscriptionDate + 1)
					}
					brokerSequence := env.Seq(owner)
					jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vaultID)))
					brokerID := brokerID(owner, brokerSequence)
					brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)
					if model.v11 {
						env.EnableFeature("LendingProtocolV1_1")
					}
					env.Close()
					require.Equal(t, model.v11, env.FeatureEnabled("LendingProtocolV1_1"))
					require.Equal(t, cleanup, env.FeatureEnabled("fixCleanup3_4_0"))
					require.True(t, env.FeatureEnabled("fixCleanup3_2_0"))

					loanSet := lending.NewLoanSet(borrower.Address, brokerID, fmt.Sprint(scenario.principal))
					payments := uint32(3)
					paymentInterval := interval
					loanSet.InterestRate = &scenario.interest
					loanSet.PaymentInterval = &paymentInterval
					loanSet.PaymentTotal = &payments
					loanSet.Counterparty = owner.Address
					loanSet.Fee = "20"
					loanSet.SigningPubKey = strings.ToUpper(borrower.PublicKeyHex())
					signature, err := txsign.SignCounterpartyWithRules(loanSet, strings.ToUpper(owner.PublicKeyHex()), "00"+strings.ToUpper(owner.PrivateKeyHex()), env.Rules())
					require.NoError(t, err)
					loanSet.CounterpartySignature = signature
					jtx.RequireTxSuccess(t, env.Submit(loanSet))
					env.Close()

					loanKey := keylet.Loan(brokerKey.Key, 1)
					loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
					loan := decodeLendingEntry(t, env, loanKey)
					initialDue := loan["NextPaymentDueDate"].(uint32)
					initialTotal := scenario.principal
					if scenario.interest != 0 {
						initialTotal = 3
					}
					require.Equal(t, fmt.Sprint(scenario.principal), loan["PrincipalOutstanding"])
					require.Equal(t, fmt.Sprint(initialTotal), loan["TotalValueOutstanding"])
					require.Equal(t, uint32(3), loan["PaymentRemaining"])
					initialVault := decodeLendingEntry(t, env, vaultKey)
					initialAssets, initialDebt := 5_000+initialTotal-scenario.principal, initialTotal
					if model.cash {
						initialAssets, initialDebt = 5_000, scenario.principal
						require.Equal(t, int(vault.VaultVersionCashBasis), initialVault["LEVersion"])
					} else {
						require.NotContains(t, initialVault, "LEVersion")
					}
					require.Equal(t, fmt.Sprint(initialAssets), initialVault["AssetsTotal"])
					require.Equal(t, fmt.Sprint(5_000-scenario.principal), initialVault["AssetsAvailable"])
					require.Equal(t, fmt.Sprint(initialDebt), decodeLendingEntry(t, env, brokerKey)["DebtTotal"])
					vaultAccount := jtx.NewAccountWithAddress("rounding-vault", initialVault["Account"].(string))
					count := 3
					if scenario.full {
						count = 1
					}
					for i := 0; i < count; i++ {
						beforeLoan := decodeLendingEntry(t, env, loanKey)
						beforeVault := decodeLendingEntry(t, env, vaultKey)
						beforeBroker := decodeLendingEntry(t, env, brokerKey)
						balance, sequence := env.Balance(borrower), env.Seq(borrower)
						pay := lending.NewLoanPay(borrower.Address, loanID, token.MPTAmount(scenario.payment))
						if scenario.full {
							flags := lending.TfLoanFullPayment
							pay.Flags = &flags
						}
						result := env.Submit(pay)
						jtx.RequireTxSuccess(t, result)
						require.Equal(t, env.BaseFee(), result.Fee)
						require.Equal(t, balance-result.Fee, env.Balance(borrower))
						require.Equal(t, sequence+1, env.Seq(borrower))
						require.NotNil(t, result.Metadata)
						require.Equal(t, jtx.TesSUCCESS, result.Metadata.TransactionResult.String())

						paid := int64(i+1) * scenario.payment
						total := initialTotal - paid
						principal := total
						if scenario.interest != 0 && total > 0 {
							principal = 1
						}
						remaining := uint32(2 - i)
						due := initialDue + uint32(i+1)*interval
						if i == count-1 {
							remaining, due = 0, 0
						}
						loan = decodeLendingEntry(t, env, loanKey)
						for field, want := range map[string]int64{"PrincipalOutstanding": principal, "TotalValueOutstanding": total, "ManagementFeeOutstanding": 0} {
							requireLoanPayNumber(t, result, loanKey, beforeLoan, loan, field, want)
						}
						if remaining == 0 {
							require.NotContains(t, loan, "PaymentRemaining")
							require.NotContains(t, loan, "NextPaymentDueDate")
						} else {
							require.Equal(t, remaining, loan["PaymentRemaining"])
							require.Equal(t, due, loan["NextPaymentDueDate"])
						}
						loanNode := loanSetMetadataNode(result, loanKey)
						require.Equal(t, beforeLoan["PaymentRemaining"], loanNode.PreviousFields["PaymentRemaining"])
						require.Equal(t, beforeLoan["NextPaymentDueDate"], loanNode.PreviousFields["NextPaymentDueDate"])
						require.Equal(t, loan["PaymentRemaining"], loanNode.FinalFields["PaymentRemaining"])
						require.Equal(t, loan["NextPaymentDueDate"], loanNode.FinalFields["NextPaymentDueDate"])

						assets, debt := int64(5_000)+initialTotal-scenario.principal, total
						if model.cash {
							assets = 5_000 + paid - (scenario.principal - principal)
							debt = principal
						}
						afterVault := decodeLendingEntry(t, env, vaultKey)
						requireLoanPayNumber(t, result, vaultKey, beforeVault, afterVault, "AssetsTotal", assets)
						requireLoanPayNumber(t, result, vaultKey, beforeVault, afterVault, "AssetsAvailable", 5_000-scenario.principal+paid)
						requireLoanPayNumber(t, result, brokerKey, beforeBroker, decodeLendingEntry(t, env, brokerKey), "DebtTotal", debt)
						token.RequireMPTokenAmount(borrower, 10_000+scenario.principal-paid)
						token.RequireMPTokenAmount(vaultAccount, 5_000-scenario.principal+paid)
						token.RequireMPTokenAmount(owner, 5_000)
						env.Close()
					}
				})
			}
		}
	}
}

func requireLoanPayNumber(t *testing.T, result jtx.TxResult, key keylet.Keylet, before, after map[string]any, field string, want int64) {
	t.Helper()
	require.Equal(t, fmt.Sprint(want), matrixNumber(t, after, field).String(), field)
	node := loanSetMetadataNode(result, key)
	if before[field] == after[field] {
		if node != nil {
			require.NotContains(t, node.PreviousFields, field)
			require.Equal(t, after[field], node.FinalFields[field], field)
		}
		return
	}
	require.NotNil(t, node, field)
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.Equal(t, before[field], node.PreviousFields[field], field)
	require.Equal(t, after[field], node.FinalFields[field], field)
}
