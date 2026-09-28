package lending_test

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestLoanAccountingWithSponsorship(t *testing.T) {
	for _, cash := range []bool{false, true} {
		for _, cleanup := range []bool{false, true} {
			for _, mode := range []string{"ordinary", "cosigned fee", "prefunded fee", "reserve", "fee and reserve"} {
				t.Run(fmt.Sprintf("cash=%t/cleanup=%t/%s", cash, cleanup, mode), func(t *testing.T) {
					testLoanAccountingWithSponsor(t, cash, cleanup, mode)
				})
			}
		}
	}
}

func testLoanAccountingWithSponsor(t *testing.T, cash, cleanup bool, mode string) {
	t.Helper()
	env := newLendingEnv(t)
	env.EnableFeature("Sponsor")
	if cash {
		env.EnableFeature("LendingProtocolV1_1")
	}
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	} else {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()
	owner := jtx.NewAccount("accounting-owner")
	borrower := jtx.NewAccount("accounting-borrower")
	feeSponsor := jtx.NewAccount("accounting-sponsor")
	for _, account := range []*jtx.Account{owner, borrower, feeSponsor} {
		env.FundAmount(account, 10_000_000_000)
	}

	subscription := env.NowRipple() + 100
	createFields := map[string]any{
		"TransactionType": "VaultCreate", "Account": owner.Address,
		"Asset": map[string]any{"currency": "XRP"}, "Fee": reserveIncrement,
	}
	if cash {
		createFields["VaultKind"] = 1
		createFields["SubscriptionDate"] = subscription
		createFields["RedemptionDate"] = subscription + 1000
	}
	createJSON, err := json.Marshal(createFields)
	require.NoError(t, err)
	create, err := tx.ParseJSON(createJSON)
	require.NoError(t, err)
	vaultSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(create))
	vid := vaultID(owner, vaultSequence)
	vaultKey := keylet.Vault(owner.AccountID(), vaultSequence)
	createdVault := decodeLendingEntry(t, env, vaultKey)
	if cash {
		require.Equal(t, int(vault.VaultVersionCashBasis), createdVault["LEVersion"])
	} else {
		require.NotContains(t, createdVault, "LEVersion")
	}
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vid, tx.NewXRPAmount(10_000))))
	brokerSequence := env.Seq(owner)
	jtx.RequireTxSuccess(t, env.Submit(lending.NewLoanBrokerSet(owner.Address, vid)))
	brokerKey := keylet.LoanBroker(owner.AccountID(), brokerSequence)
	if cash {
		env.CloseToParentCloseTime(subscription + 1)
	}

	prefunded := mode == "prefunded fee"
	feeSponsored := mode == "cosigned fee" || prefunded || mode == "fee and reserve"
	reserveSponsored := mode == "reserve" || mode == "fee and reserve"
	if prefunded {
		set := sponsor.NewSponsorshipSet(feeSponsor.Address)
		set.Sponsee = borrower.Address
		amount := tx.NewXRPAmount(100)
		set.FeeAmountDelta = &amount
		jtx.RequireTxSuccess(t, env.Submit(set))
	}
	prepare := func(transaction tx.Transaction, fee string, reserve bool) {
		common := transaction.GetCommon()
		sequence := env.Seq(borrower)
		common.Sequence = &sequence
		common.Fee = fee
		common.SigningPubKey = borrower.PublicKeyHex()
		flags := uint32(0)
		if feeSponsored {
			flags |= tx.SpfSponsorFee
		}
		if reserve && reserveSponsored {
			flags |= tx.SpfSponsorReserve
		}
		if flags != 0 {
			common.Sponsor = feeSponsor.Address
			common.SponsorFlags = &flags
			if !prefunded {
				signature, err := txsign.SignSponsorWithRules(transaction, feeSponsor.PublicKeyHex(), "00"+feeSponsor.PrivateKeyHex(), env.Rules())
				require.NoError(t, err)
				common.SponsorSignature = signature
			}
		}
	}
	checkFee := func(sponsorBefore, borrowerAfterMovement, fee uint64) {
		t.Helper()
		if feeSponsored {
			require.Equal(t, borrowerAfterMovement, env.Balance(borrower))
			if prefunded {
				require.Equal(t, sponsorBefore, env.Balance(feeSponsor))
			} else {
				require.Equal(t, sponsorBefore-fee, env.Balance(feeSponsor))
			}
		} else {
			require.Equal(t, borrowerAfterMovement-fee, env.Balance(borrower))
			require.Equal(t, sponsorBefore, env.Balance(feeSponsor))
		}
	}

	loanSet := lending.NewLoanSet(borrower.Address, brokerID(owner, brokerSequence), "1000")
	interval, payments := uint32(60), uint32(2)
	loanSet.PaymentInterval, loanSet.PaymentTotal = &interval, &payments
	loanSet.Counterparty = owner.Address
	originationFee, serviceFee := "20", "10"
	loanSet.LoanOriginationFee, loanSet.LoanServiceFee = &originationFee, &serviceFee
	prepare(loanSet, "20", true)
	counterpartySignature, err := txsign.SignCounterpartyWithRules(loanSet, owner.PublicKeyHex(), "00"+owner.PrivateKeyHex(), env.Rules())
	require.NoError(t, err)
	loanSet.CounterpartySignature = counterpartySignature
	borrowerBefore, sponsorBefore, ownerBefore := env.Balance(borrower), env.Balance(feeSponsor), env.Balance(owner)
	sequence := env.Seq(borrower)
	brokerBefore := decodeLendingEntry(t, env, brokerKey)
	result := env.Submit(loanSet)
	if reserveSponsored {
		jtx.RequireTxFail(t, result, jtx.TemINVALID_FLAG)
		require.Equal(t, borrowerBefore, env.Balance(borrower))
		require.Equal(t, sponsorBefore, env.Balance(feeSponsor))
		require.Equal(t, ownerBefore, env.Balance(owner))
		require.Equal(t, sequence, env.Seq(borrower))
		require.False(t, env.LedgerEntryExists(keylet.Loan(brokerKey.Key, 1)))
		assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "10000")
		assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "10000")
		require.Equal(t, brokerBefore, decodeLendingEntry(t, env, brokerKey))
		return
	}
	jtx.RequireTxSuccess(t, result)
	checkFee(sponsorBefore, borrowerBefore+980, 20)
	require.Equal(t, ownerBefore+20, env.Balance(owner))
	require.Equal(t, sequence+1, env.Seq(borrower))
	loanKey := keylet.Loan(brokerKey.Key, 1)
	loanID := strings.ToUpper(hex.EncodeToString(loanKey.Key[:]))
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "10000")
	assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9000")
	assertLendingField(t, "LoanBroker", decodeLendingEntry(t, env, brokerKey), "DebtTotal", "1000")
	loanBefore := decodeLendingEntry(t, env, loanKey)

	for _, amount := range []int64{509, 510} {
		pay := lending.NewLoanPay(borrower.Address, loanID, tx.NewXRPAmount(amount))
		prepare(pay, "10", false)
		borrowerBefore, sponsorBefore, ownerBefore = env.Balance(borrower), env.Balance(feeSponsor), env.Balance(owner)
		sequence = env.Seq(borrower)
		result := env.Submit(pay)
		if amount == 509 {
			jtx.RequireTxClaimed(t, result, "tecINSUFFICIENT_PAYMENT")
			checkFee(sponsorBefore, borrowerBefore, 10)
			require.Equal(t, ownerBefore, env.Balance(owner))
			require.Equal(t, loanBefore, decodeLendingEntry(t, env, loanKey))
			assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9000")
			assertLendingField(t, "LoanBroker", decodeLendingEntry(t, env, brokerKey), "DebtTotal", "1000")
		} else {
			jtx.RequireTxSuccess(t, result)
			checkFee(sponsorBefore, borrowerBefore-510, 10)
			require.Equal(t, ownerBefore+10, env.Balance(owner))
			assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsAvailable", "9500")
			assertLendingField(t, "LoanBroker", decodeLendingEntry(t, env, brokerKey), "DebtTotal", "500")
		}
		require.Equal(t, sequence+1, env.Seq(borrower))
		assertLendingField(t, "Vault", decodeLendingEntry(t, env, vaultKey), "AssetsTotal", "10000")
	}
	if prefunded {
		relationship := decodeLendingEntry(t, env, keylet.Sponsorship(feeSponsor.AccountID(), borrower.AccountID()))
		require.Equal(t, "60", relationship["FeeAmount"])
	}
}
