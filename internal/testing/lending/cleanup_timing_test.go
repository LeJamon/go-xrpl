package lending_test

import (
	"encoding/hex"
	"fmt"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/lending"
	txsign "github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func newCleanupLoan(t *testing.T, cleanup, cashBasis bool) (*jtx.TestEnv, *jtx.Account, *jtx.Account, keylet.Keylet, keylet.Keylet, uint32) {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("LendingProtocol")
	if cashBasis {
		env.EnableFeature("LendingProtocolV1_1")
	}
	if cleanup {
		env.EnableFeature("fixCleanup3_4_0")
	}
	owner, borrower := jtx.NewAccount("owner"), jtx.NewAccount("borrower")
	env.Fund(owner, borrower)
	env.Close()
	vk := keylet.Vault(owner.ID, env.Seq(owner))
	vid := hex.EncodeToString(vk.Key[:])
	create := vault.NewVaultCreate(owner.Address, tx.Asset{Currency: "XRP"})
	create.Fee = "50000000"
	jtx.RequireTxSuccess(t, env.Submit(create))
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(owner.Address, vid, tx.NewXRPAmount(1000000))))
	bk := keylet.LoanBroker(owner.ID, env.Seq(owner))
	bid := hex.EncodeToString(bk.Key[:])
	broker := lending.NewLoanBrokerSet(owner.Address, vid)
	broker.Fee = "50000000"
	jtx.RequireTxSuccess(t, env.Submit(broker))
	loan := lending.NewLoanSet(borrower.Address, bid, "100000")
	interval, total := uint32(120), uint32(3)
	loan.PaymentInterval = &interval
	loan.PaymentTotal = &total
	loan.Fee = "20"
	loan.Counterparty = owner.Address
	loan.SigningPubKey = borrower.PublicKeyHex()
	signature, err := txsign.SignCounterparty(loan, owner.PublicKeyHex(), "00"+owner.PrivateKeyHex())
	require.NoError(t, err)
	loan.CounterpartySignature = signature
	result := env.Submit(loan)
	jtx.RequireTxSuccess(t, result)
	var lk keylet.Keylet
	for _, node := range result.Metadata.AffectedNodes {
		if node.NodeType == "CreatedNode" && node.LedgerEntryType == "Loan" {
			b, err := hex.DecodeString(node.LedgerIndex)
			require.NoError(t, err)
			copy(lk.Key[:], b)
		}
	}
	require.NotEqual(t, [32]byte{}, lk.Key)
	fields := cleanupLoanFields(t, env, lk)
	due := fields["NextPaymentDueDate"].(uint32)
	return env, owner, borrower, lk, vk, due
}

func cleanupLoanFields(t *testing.T, env *jtx.TestEnv, k keylet.Keylet) map[string]any {
	t.Helper()
	raw, err := env.LedgerEntry(k)
	require.NoError(t, err)
	fields, err := binarycodec.DecodeBytes(raw)
	require.NoError(t, err)
	return fields
}

func testCleanupLoanImpairmentPreservesSchedule(t *testing.T, cashBasis bool) {
	env, owner, borrower, lk, vk, due := newCleanupLoan(t, true, cashBasis)
	id := hex.EncodeToString(lk.Key[:])
	manage := func(flags uint32) *lending.LoanManage {
		m := lending.NewLoanManage(owner.Address, id)
		m.SetFlags(flags)
		return m
	}
	for _, now := range []uint32{due - 1, due} {
		env.CloseToParentCloseTime(now)
		beforeLoan, err := env.LedgerEntry(lk)
		require.NoError(t, err)
		beforeVault, err := env.LedgerEntry(vk)
		require.NoError(t, err)
		balance, seq := env.Balance(owner), env.Seq(owner)
		jtx.RequireTxFail(t, env.Submit(manage(lending.TfLoanImpair)), "tecTOO_SOON")
		afterLoan, err := env.LedgerEntry(lk)
		require.NoError(t, err)
		require.Equal(t, beforeLoan, afterLoan)
		afterVault, err := env.LedgerEntry(vk)
		require.NoError(t, err)
		require.Equal(t, beforeVault, afterVault)
		require.Equal(t, balance-10, env.Balance(owner))
		require.Equal(t, seq+1, env.Seq(owner))
	}
	for _, now := range []uint32{due + 1, due + 121} {
		env.CloseToParentCloseTime(now)
		jtx.RequireTxSuccess(t, env.Submit(manage(lending.TfLoanImpair)))
		require.Equal(t, "100000", fmt.Sprint(cleanupLoanFields(t, env, vk)["LossUnrealized"]))
		require.Equal(t, due, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
		jtx.RequireTxFail(t, env.Submit(manage(lending.TfLoanImpair)), "tecNO_PERMISSION")
		jtx.RequireTxSuccess(t, env.Submit(manage(lending.TfLoanUnimpair)))
		require.Equal(t, due, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
		jtx.RequireTxFail(t, env.Submit(manage(lending.TfLoanUnimpair)), "tecNO_PERMISSION")
	}
	jtx.RequireTxSuccess(t, env.Submit(manage(lending.TfLoanImpair)))
	payment := lending.NewLoanPay(borrower.Address, id, tx.NewXRPAmount(40000))
	payment.SetFlags(lending.TfLoanLatePayment)
	jtx.RequireTxSuccess(t, env.Submit(payment))
	require.Equal(t, due+120, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
	require.Empty(t, cleanupLoanFields(t, env, vk)["LossUnrealized"])
}

func testCleanupLoanDefaultGraceBoundary(t *testing.T, cashBasis bool) {
	for _, cleanup := range []bool{false, true} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("cleanup=%t/delta=%d", cleanup, delta), func(t *testing.T) {
				env, owner, _, lk, vk, due := newCleanupLoan(t, cleanup, cashBasis)
				grace := cleanupLoanFields(t, env, lk)["GracePeriod"].(uint32)
				env.CloseToParentCloseTime(uint32(int64(due+grace) + delta))
				beforeLoan, err := env.LedgerEntry(lk)
				require.NoError(t, err)
				beforeVault, err := env.LedgerEntry(vk)
				require.NoError(t, err)
				balance, seq := env.Balance(owner), env.Seq(owner)
				m := lending.NewLoanManage(owner.Address, hex.EncodeToString(lk.Key[:]))
				m.SetFlags(lending.TfLoanDefault)
				result := env.Submit(m)
				if delta < 0 || (cleanup && delta == 0) {
					jtx.RequireTxFail(t, result, "tecTOO_SOON")
					afterLoan, err := env.LedgerEntry(lk)
					require.NoError(t, err)
					require.Equal(t, beforeLoan, afterLoan)
					afterVault, err := env.LedgerEntry(vk)
					require.NoError(t, err)
					require.Equal(t, beforeVault, afterVault)
				} else {
					jtx.RequireTxSuccess(t, result)
					foundLoan := false
					for _, node := range result.Metadata.AffectedNodes {
						if node.LedgerEntryType == "Loan" {
							foundLoan = true
							require.Equal(t, "ModifiedNode", node.NodeType)
							require.Equal(t, due, node.PreviousFields["NextPaymentDueDate"])
							require.Equal(t, "100000", node.PreviousFields["PrincipalOutstanding"])
						}
					}
					require.True(t, foundLoan)
					require.Empty(t, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
					require.Empty(t, cleanupLoanFields(t, env, lk)["PrincipalOutstanding"])
					m = lending.NewLoanManage(owner.Address, hex.EncodeToString(lk.Key[:]))
					m.SetFlags(lending.TfLoanDefault)
					jtx.RequireTxFail(t, env.Submit(m), "tecNO_PERMISSION")
					balance -= 10
					seq++
				}
				require.Equal(t, balance-10, env.Balance(owner))
				require.Equal(t, seq+1, env.Seq(owner))
			})
		}
	}
}

func testCleanupLoanPaymentDueBoundary(t *testing.T, cashBasis bool) {
	for _, cleanup := range []bool{false, true} {
		for _, late := range []bool{false, true} {
			for _, delta := range []int64{-1, 0, 1} {
				t.Run(fmt.Sprintf("cleanup=%t/late=%t/delta=%d", cleanup, late, delta), func(t *testing.T) {
					env, _, borrower, lk, vk, due := newCleanupLoan(t, cleanup, cashBasis)
					env.CloseToParentCloseTime(uint32(int64(due) + delta))
					beforeLoan, err := env.LedgerEntry(lk)
					require.NoError(t, err)
					beforeVault, err := env.LedgerEntry(vk)
					require.NoError(t, err)
					balance, seq := env.Balance(borrower), env.Seq(borrower)
					payment := lending.NewLoanPay(borrower.Address, hex.EncodeToString(lk.Key[:]), tx.NewXRPAmount(40000))
					if late {
						payment.SetFlags(lending.TfLoanLatePayment)
					}
					result := env.Submit(payment)
					isLate := delta > 0 || (!cleanup && delta == 0)
					if late != isLate {
						expected := "tecEXPIRED"
						if late {
							expected = "tecTOO_SOON"
						}
						jtx.RequireTxFail(t, result, expected)
						afterLoan, err := env.LedgerEntry(lk)
						require.NoError(t, err)
						require.Equal(t, beforeLoan, afterLoan)
						afterVault, err := env.LedgerEntry(vk)
						require.NoError(t, err)
						require.Equal(t, beforeVault, afterVault)
						require.Equal(t, balance-10, env.Balance(borrower))
					} else {
						jtx.RequireTxSuccess(t, result)
						require.Equal(t, uint32(2), cleanupLoanFields(t, env, lk)["PaymentRemaining"])
						require.Equal(t, due+120, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
					}
					require.Equal(t, seq+1, env.Seq(borrower))
				})
			}
		}
	}
}

func testCleanupLoanDefaultFrozenCover(t *testing.T, cashBasis bool) {
	for _, kind := range []string{"individual", "deep", "global", "holder lock", "issuance lock"} {
		for _, cleanup := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/cleanup=%t", kind, cleanup), func(t *testing.T) {
				asset := "IOU"
				if kind == "holder lock" || kind == "issuance lock" {
					asset = "MPT"
				}
				f := newLoanSetAssetFixture(t, asset, mpttest.TfMPTCanLock)
				if cashBasis {
					f.env.EnableFeature("LendingProtocolV1_1")
				}
				if cleanup {
					f.env.EnableFeature("fixCleanup3_4_0")
				}
				f.env.EnableFeature("MPTokensV2")
				f.createHolding(f.owner)
				f.fundHolding(f.owner, 1000)
				broker := decodeLendingEntry(t, f.env, keylet.LoanBrokerByID(f.brokerKey))
				set := lending.NewLoanBrokerSet(f.owner.Address, broker["VaultID"].(string))
				brokerSeq := f.env.Seq(f.owner)
				rate := uint32(100000)
				set.CoverRateMinimum = &rate
				set.CoverRateLiquidation = &rate
				jtx.RequireTxSuccess(t, f.env.Submit(set))
				f.brokerKey = keylet.LoanBroker(f.owner.ID, brokerSeq).Key
				f.brokerID = hex.EncodeToString(f.brokerKey[:])
				broker = decodeLendingEntry(t, f.env, keylet.LoanBrokerByID(f.brokerKey))
				jtx.RequireTxSuccess(t, f.env.Submit(lending.NewLoanBrokerCoverDeposit(f.owner.Address, f.brokerID, f.amount(1000))))
				jtx.RequireTxSuccess(t, submitLoanSet(t, f, f.borrower, f.owner, false))
				lk := keylet.Loan(f.brokerKey, 1)
				loan := cleanupLoanFields(t, f.env, lk)
				due, grace := loan["NextPaymentDueDate"].(uint32), loan["GracePeriod"].(uint32)
				brokerAccount := jtx.NewAccountWithAddress("broker-pseudo", broker["Account"].(string))
				vaultBytes, err := hex.DecodeString(broker["VaultID"].(string))
				require.NoError(t, err)
				var vaultID [32]byte
				copy(vaultID[:], vaultBytes)
				vk := keylet.VaultByID(vaultID)
				vaultAccount := jtx.NewAccountWithAddress("vault-pseudo", cleanupLoanFields(t, f.env, vk)["Account"].(string))
				switch kind {
				case "individual":
					f.env.FreezeTrustLine(f.issuer, brokerAccount, "USD")
					f.env.FreezeTrustLine(f.issuer, vaultAccount, "USD")
					f.env.FreezeTrustLine(f.issuer, f.borrower, "USD")
				case "deep":
					deepFreezeLoanSetTrustLine(t, f, brokerAccount.ID)
					deepFreezeLoanSetTrustLine(t, f, vaultAccount.ID)
				case "global":
					f.env.EnableGlobalFreeze(f.issuer)
				case "holder lock":
					f.token.Set(mpttest.SetOpts{Holder: brokerAccount, Flags: mpttest.TfMPTLock})
					f.token.Set(mpttest.SetOpts{Holder: vaultAccount, Flags: mpttest.TfMPTLock})
				case "issuance lock":
					f.token.Set(mpttest.SetOpts{Flags: mpttest.TfMPTLock})
				}
				f.env.CloseToParentCloseTime(due + grace + 1)
				snapshots := map[keylet.Keylet][]byte{}
				for _, k := range []keylet.Keylet{lk, vk, keylet.LoanBrokerByID(f.brokerKey), f.holdingKey(brokerAccount), f.holdingKey(vaultAccount)} {
					raw, err := f.env.LedgerEntry(k)
					require.NoError(t, err)
					snapshots[k] = raw
				}
				repayment := lending.NewLoanPay(f.borrower.Address, hex.EncodeToString(lk.Key[:]), f.amount(1000))
				repayment.SetFlags(lending.TfLoanLatePayment)
				repaymentCode := "tecINVARIANT_FAILED"
				if kind == "global" || kind == "individual" || kind == "deep" {
					repaymentCode = "tecFROZEN"
				}
				if kind == "issuance lock" || kind == "holder lock" {
					repaymentCode = "tecLOCKED"
				}
				borrowerBalance, borrowerSeq := f.env.Balance(f.borrower), f.env.Seq(f.borrower)
				jtx.RequireTxFail(t, f.env.Submit(repayment), repaymentCode)
				require.Equal(t, borrowerBalance-10, f.env.Balance(f.borrower))
				require.Equal(t, borrowerSeq+1, f.env.Seq(f.borrower))
				for k, before := range snapshots {
					after, err := f.env.LedgerEntry(k)
					require.NoError(t, err)
					require.Equal(t, before, after)
				}
				for _, account := range []*jtx.Account{f.borrower, f.issuer, brokerAccount, vaultAccount} {
					k := keylet.Account(account.ID)
					raw, err := f.env.LedgerEntry(k)
					require.NoError(t, err)
					snapshots[k] = raw
				}
				if asset == "MPT" {
					rawID, err := hex.DecodeString(f.asset.MPTIssuanceID)
					require.NoError(t, err)
					var issuance [24]byte
					copy(issuance[:], rawID)
					k := keylet.MPTIssuance(issuance)
					raw, err := f.env.LedgerEntry(k)
					require.NoError(t, err)
					snapshots[k] = raw
				}
				ownerCount := f.env.OwnerCount(f.owner)
				balance, seq := f.env.Balance(f.owner), f.env.Seq(f.owner)
				m := lending.NewLoanManage(f.owner.Address, hex.EncodeToString(lk.Key[:]))
				m.SetFlags(lending.TfLoanDefault)
				result := f.env.Submit(m)
				if cleanup {
					jtx.RequireTxSuccess(t, result)
					require.Empty(t, cleanupLoanFields(t, f.env, lk)["PrincipalOutstanding"])
					require.Empty(t, cleanupLoanFields(t, f.env, keylet.LoanBrokerByID(f.brokerKey))["CoverAvailable"])
					require.Equal(t, "10000", cleanupLoanFields(t, f.env, vk)["AssetsAvailable"])
					require.Equal(t, "10000", cleanupLoanFields(t, f.env, vk)["AssetsTotal"])
					require.Empty(t, cleanupLoanFields(t, f.env, vk)["LossUnrealized"])
					require.Empty(t, cleanupLoanFields(t, f.env, keylet.LoanBrokerByID(f.brokerKey))["DebtTotal"])
					require.Empty(t, cleanupLoanFields(t, f.env, lk)["PaymentRemaining"])
					require.EqualValues(t, lending.LsfLoanDefault, cleanupLoanFields(t, f.env, lk)["Flags"])
					found := map[string]bool{}
					for _, node := range result.Metadata.AffectedNodes {
						switch node.LedgerEntryType {
						case "Loan":
							found["Loan"] = true
							require.Equal(t, "ModifiedNode", node.NodeType)
							require.Equal(t, "1000", node.PreviousFields["PrincipalOutstanding"])
							require.Equal(t, uint32(1), node.PreviousFields["PaymentRemaining"])
							require.EqualValues(t, lending.LsfLoanDefault, node.FinalFields["Flags"])
						case "LoanBroker":
							found["LoanBroker"] = true
							require.Equal(t, "1000", node.PreviousFields["DebtTotal"])
							require.Equal(t, "1000", node.PreviousFields["CoverAvailable"])
						case "Vault":
							found["Vault"] = true
							require.Equal(t, "9000", node.PreviousFields["AssetsAvailable"])
							require.Equal(t, "10000", node.FinalFields["AssetsAvailable"])
						}
					}
					require.Equal(t, map[string]bool{"Loan": true, "LoanBroker": true, "Vault": true}, found)

					if asset == "MPT" {
						f.token.RequireMPTokenAmount(brokerAccount, 0)
						f.token.RequireMPTokenAmount(vaultAccount, 10000)
					} else {
						raw, err := f.env.LedgerEntry(f.holdingKey(brokerAccount))
						require.NoError(t, err)
						line, err := state.ParseRippleState(raw)
						require.NoError(t, err)
						require.True(t, line.Balance.IsZero())
					}
				} else {
					jtx.RequireTxFail(t, result, "tecINVARIANT_FAILED")
					for k, before := range snapshots {
						after, err := f.env.LedgerEntry(k)
						require.NoError(t, err)
						require.Equal(t, before, after)
					}
				}
				require.Equal(t, ownerCount, f.env.OwnerCount(f.owner))
				require.Equal(t, balance-10, f.env.Balance(f.owner))
				require.Equal(t, seq+1, f.env.Seq(f.owner))
			})
		}
	}
}

func TestCleanupLoanImpairmentPreservesSchedule(t *testing.T) {
	for _, cashBasis := range []bool{false, true} {
		t.Run(fmt.Sprintf("LP1.1=%t", cashBasis), func(t *testing.T) { testCleanupLoanImpairmentPreservesSchedule(t, cashBasis) })
	}
}

func TestCleanupLoanDefaultGraceBoundary(t *testing.T) {
	for _, cashBasis := range []bool{false, true} {
		t.Run(fmt.Sprintf("LP1.1=%t", cashBasis), func(t *testing.T) { testCleanupLoanDefaultGraceBoundary(t, cashBasis) })
	}
}

func TestCleanupLoanPaymentDueBoundary(t *testing.T) {
	for _, cashBasis := range []bool{false, true} {
		t.Run(fmt.Sprintf("LP1.1=%t", cashBasis), func(t *testing.T) { testCleanupLoanPaymentDueBoundary(t, cashBasis) })
	}
}

func TestCleanupLoanDefaultFrozenCover(t *testing.T) {
	for _, cashBasis := range []bool{false, true} {
		t.Run(fmt.Sprintf("LP1.1=%t", cashBasis), func(t *testing.T) { testCleanupLoanDefaultFrozenCover(t, cashBasis) })
	}
}

func TestCleanupLoanImpairmentBoundaryModes(t *testing.T) {
	for _, cleanup := range []bool{false, true} {
		for _, delta := range []int64{-1, 0, 1} {
			t.Run(fmt.Sprintf("cleanup=%t/delta=%d", cleanup, delta), func(t *testing.T) {
				env, owner, _, lk, _, due := newCleanupLoan(t, cleanup, false)
				now := uint32(int64(due) + delta)
				env.CloseToParentCloseTime(now)
				manage := lending.NewLoanManage(owner.Address, hex.EncodeToString(lk.Key[:]))
				manage.SetFlags(lending.TfLoanImpair)
				result := env.Submit(manage)
				if cleanup && delta <= 0 {
					jtx.RequireTxFail(t, result, "tecTOO_SOON")
					require.Equal(t, due, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
					return
				}
				jtx.RequireTxSuccess(t, result)
				expected := due
				if !cleanup && delta < 0 {
					expected = now
				}
				require.Equal(t, expected, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
				manage = lending.NewLoanManage(owner.Address, hex.EncodeToString(lk.Key[:]))
				manage.SetFlags(lending.TfLoanUnimpair)
				jtx.RequireTxSuccess(t, env.Submit(manage))
				if !cleanup && delta >= 0 {
					expected = now + 120
				} else {
					expected = due
				}
				require.Equal(t, expected, cleanupLoanFields(t, env, lk)["NextPaymentDueDate"])
			})
		}
	}
}
