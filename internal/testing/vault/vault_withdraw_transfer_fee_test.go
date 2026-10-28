package vault_test

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	mpttest "github.com/LeJamon/go-xrpl/internal/testing/mpt"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

// A 25% transfer fee: 100 gross delivers 80 net.
const (
	transferFee125 uint16 = 25_000
	rate125        uint32 = 1_250_000_000
)

type feeVaultFixture struct {
	t      *testing.T
	env    *jtx.TestEnv
	issuer *jtx.Account
	owner  *jtx.Account
	// other is the MPT depositor or the IOU third party (charlie).
	other *jtx.Account
	token *mpttest.MPTTester
	asset tx.Asset
}

type fundedFeeVault struct {
	id      string
	key     keylet.Keylet
	account [20]byte
	shareID string
}

func enableFeeFeatures(env *jtx.TestEnv, fix350, mptV2 bool) {
	for name, enabled := range map[string]bool{"fixCleanup3_5_0": fix350, "MPTokensV2": mptV2} {
		if enabled {
			env.EnableFeature(name)
		} else {
			env.DisableFeature(name)
		}
	}
	env.Close()
}

// newMPTFeeFixture mirrors the MPT testCase setup of rippled
// VaultLifecycle_test.cpp: a RequireAuth, CanTransfer MPT with the given
// transfer fee, and a depositor holding 1000.
func newMPTFeeFixture(t *testing.T, transferFee uint16, fix350, mptV2 bool) *feeVaultFixture {
	t.Helper()
	env := newVaultEnv(t)
	enableFeeFeatures(env, fix350, mptV2)
	issuer := jtx.NewAccount("issuer")
	owner := jtx.NewAccount("owner")
	depositor := jtx.NewAccount("depositor")

	token := mpttest.NewMPTTester(t, env, issuer, mpttest.MPTInit{Holders: []*jtx.Account{owner, depositor}})
	token.Create(mpttest.CreateOpts{
		TransferFee: &transferFee,
		Flags: mpttest.TfMPTCanTransfer | mpttest.TfMPTCanLock | mpttest.TfMPTCanClawback |
			mpttest.TfMPTRequireAuth,
	})
	for _, holder := range []*jtx.Account{owner, depositor} {
		token.Authorize(mpttest.AuthorizeOpts{Account: holder})
		token.Authorize(mpttest.AuthorizeOpts{Holder: holder})
	}
	token.Pay(issuer, depositor, 1_000)

	return &feeVaultFixture{
		t: t, env: env, issuer: issuer, owner: owner, other: depositor, token: token,
		asset: tx.Asset{MPTIssuanceID: token.IssuanceID()},
	}
}

// newIOUFeeFixture mirrors the IOU testCase setup of rippled
// VaultLifecycle_test.cpp: the owner holds 200 IOU, charlie has an empty trust
// line, and the issuer charges rate.
func newIOUFeeFixture(t *testing.T, rate uint32, fix350 bool) *feeVaultFixture {
	t.Helper()
	env := newVaultEnv(t)
	enableFeeFeatures(env, fix350, true)
	owner := jtx.NewAccount("owner")
	issuer := jtx.NewAccount("issuer")
	charlie := jtx.NewAccount("charlie")
	env.Fund(issuer, owner, charlie)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).AllowClawback().Build()))

	asset := tx.Asset{Currency: "IOU", Issuer: issuer.Address}
	env.Trust(owner, tx.NewIssuedAmountFromFloat64(1_000, "IOU", issuer.Address))
	env.PayIOU(issuer, owner, issuer, "IOU", 200)
	env.Trust(charlie, tx.NewIssuedAmountFromFloat64(1_000, "IOU", issuer.Address))
	env.SetTransferRate(issuer, rate)
	env.Close()

	return &feeVaultFixture{t: t, env: env, issuer: issuer, owner: owner, other: charlie, asset: asset}
}

func (f *feeVaultFixture) amount(value int64) tx.Amount {
	if f.token != nil {
		return f.token.MPTAmount(value)
	}
	return tx.NewIssuedAmountFromFloat64(float64(value), "IOU", f.issuer.Address)
}

func (f *feeVaultFixture) iouAmount(value string) tx.Amount {
	f.t.Helper()
	amount, err := state.NewIssuedAmountFromDecimalString(value, "IOU", f.issuer.Address)
	require.NoError(f.t, err)
	return amount
}

func (f *feeVaultFixture) createFundedVault(depositor *jtx.Account, value int64) fundedFeeVault {
	f.t.Helper()
	seq := f.env.Seq(f.owner)
	create := vault.NewVaultCreate(f.owner.Address, f.asset)
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(f.t, f.env.Submit(create))
	id := vaultID(f.owner, seq)
	jtx.RequireTxSuccess(f.t, f.env.Submit(vault.NewVaultDeposit(depositor.Address, id, f.amount(value))))

	key := keylet.Vault(f.owner.AccountID(), seq)
	info, err := vault.ReadVaultInfo(f.env.Ledger(), key)
	require.NoError(f.t, err)
	require.NotNil(f.t, info)
	return fundedFeeVault{
		id:      id,
		key:     key,
		account: info.Account,
		shareID: strings.ToUpper(hex.EncodeToString(info.ShareMPTID[:])),
	}
}

func (v fundedFeeVault) shares(value int64) tx.Amount {
	return state.NewMPTAmountWithIssuanceID(value, "", v.shareID)
}

func (f *feeVaultFixture) withdraw(depositor *jtx.Account, v fundedFeeVault, amount tx.Amount, destination *jtx.Account) jtx.TxResult {
	f.t.Helper()
	withdraw := vault.NewVaultWithdraw(depositor.Address, v.id, amount)
	if destination != nil {
		withdraw.Destination = destination.Address
	}
	return f.env.Submit(withdraw)
}

func (f *feeVaultFixture) deleteVault(v fundedFeeVault) {
	f.t.Helper()
	jtx.RequireTxSuccess(f.t, f.env.Submit(vault.NewVaultDelete(f.owner.Address, v.id)))
}

func (f *feeVaultFixture) mptBalance(holder [20]byte, issuanceID string) int64 {
	f.t.Helper()
	raw, err := hex.DecodeString(issuanceID)
	require.NoError(f.t, err)
	var id [24]byte
	copy(id[:], raw)
	key := keylet.MPTokenByID(id, holder)
	if !f.env.LedgerEntryExists(key) {
		return 0
	}
	data, err := f.env.LedgerEntry(key)
	require.NoError(f.t, err)
	token, err := state.ParseMPToken(data)
	require.NoError(f.t, err)
	return int64(token.MPTAmount)
}

func (f *feeVaultFixture) requireVaultAssets(v fundedFeeVault, want float64) {
	f.t.Helper()
	if f.token != nil {
		require.Equal(f.t, int64(want), f.mptBalance(v.account, f.token.IssuanceID()))
		return
	}
	got, _ := iouLineBalance(f.t, f.env, v.account, f.issuer.AccountID(), "IOU")
	require.True(f.t, approxEq(got, want), "vault holds %v, want %v", got, want)
}

func (f *feeVaultFixture) requireShares(holder *jtx.Account, v fundedFeeVault, want int64) {
	f.t.Helper()
	require.Equal(f.t, want, f.mptBalance(holder.AccountID(), v.shareID))
}

func (f *feeVaultFixture) requireIOU(holder *jtx.Account, want float64) {
	f.t.Helper()
	got := f.env.BalanceIOU(holder, "IOU", f.issuer)
	require.True(f.t, approxEq(got, want), "%s holds %v IOU, want %v", holder.Name, got, want)
}

func (f *feeVaultFixture) requireOutstanding(want int64) {
	f.t.Helper()
	require.True(f.t, f.token.CheckMPTokenOutstandingAmount(want), "outstanding = %d, want %d",
		f.token.IssuanceOutstandingAmount(), want)
}

func (f *feeVaultFixture) vaultNumbers(v fundedFeeVault) (total, available float64) {
	f.t.Helper()
	lending, err := vault.ReadVaultLending(f.env.Ledger(), v.key)
	require.NoError(f.t, err)
	require.NotNil(f.t, lending)
	parse := func(s string) float64 {
		if s == "" {
			return 0
		}
		value, err := strconv.ParseFloat(s, 64)
		require.NoError(f.t, err)
		return value
	}
	return parse(lending.AssetsTotal), parse(lending.AssetsAvailable)
}

// Reference: rippled VaultLifecycle_test.cpp testWithMPT transfer fee cases.
func TestVaultWithdrawMPTTransferFee(t *testing.T) {
	for _, fix350 := range []bool{false, true} {
		name := "fee_waived_without_fixCleanup3_5_0"
		if fix350 {
			name = "fee_on_third_party_withdrawal"
		}
		t.Run(name, func(t *testing.T) {
			f := newMPTFeeFixture(t, transferFee125, fix350, true)
			depositor := f.other
			v := f.createFundedVault(depositor, 100)
			jtx.RequireTxSuccess(t, f.withdraw(depositor, v, v.shares(100), f.owner))

			expected := int64(100)
			if fix350 {
				expected = 80
			}
			f.token.RequireMPTokenAmount(depositor, 900)
			f.token.RequireMPTokenAmount(f.owner, expected)
			f.requireOutstanding(900 + expected)
			f.requireVaultAssets(v, 0)
			f.deleteVault(v)
		})
	}

	t.Run("fee_on_fixed_asset_third_party_withdrawal", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(depositor, 100)
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(40), f.owner))

		// The destination receives the requested 40; the gross 50 leaves the vault.
		f.token.RequireMPTokenAmount(f.owner, 40)
		f.requireVaultAssets(v, 50)
		f.requireShares(depositor, v, 50)
		f.requireOutstanding(990)
	})

	t.Run("fee_on_fixed_share_third_party_withdrawal", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(depositor, 100)
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, v.shares(25), f.owner))

		// The 25 shares redeem 25 assets; the destination receives 20 net.
		f.token.RequireMPTokenAmount(f.owner, 20)
		f.requireVaultAssets(v, 75)
		f.requireShares(depositor, v, 75)
		f.requireOutstanding(995)
	})

	// The fee on a fixed-asset withdrawal rounds the gross up to a whole unit,
	// and the delivered amount back down: 1 costs ceil(1.25) = 2 and delivers
	// floor(2 / 1.25) = 1; 3 costs ceil(3.75) = 4 and delivers floor(3.2) = 3.
	for _, c := range []struct {
		request, gross int64
	}{
		{1, 2},
		{3, 4},
	} {
		t.Run(fmt.Sprintf("fee_rounds_fixed_asset_gross_up_%d", c.request), func(t *testing.T) {
			f := newMPTFeeFixture(t, transferFee125, true, true)
			depositor := f.other
			v := f.createFundedVault(depositor, 100)
			jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(c.request), f.owner))

			f.token.RequireMPTokenAmount(f.owner, c.request)
			f.requireVaultAssets(v, float64(100-c.gross))
			f.requireShares(depositor, v, 100-c.gross)
			f.requireOutstanding(1_000 - (c.gross - c.request))
		})
	}

	t.Run("no_fee_on_withdrawal_to_issuer", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(depositor, 100)
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(10), f.issuer))

		f.requireVaultAssets(v, 90)
		f.requireShares(depositor, v, 90)
		f.requireOutstanding(990)
	})

	t.Run("no_fee_on_self_withdrawal", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(depositor, 100)
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(10), nil))

		f.token.RequireMPTokenAmount(depositor, 910)
		f.requireVaultAssets(v, 90)
		f.requireOutstanding(1_000)
	})

	t.Run("fee_must_fit_in_the_vault_position", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(depositor, 100)

		// Delivering 81 costs 102 gross, more than the 100 shares held.
		jtx.RequireTxFail(t, f.withdraw(depositor, v, f.amount(81), f.owner), ter.TecINSUFFICIENT_FUNDS.String())

		// Delivering 80 costs exactly the 100 shares held.
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(80), f.owner))
		f.token.RequireMPTokenAmount(f.owner, 80)
		f.requireVaultAssets(v, 0)
		f.requireShares(depositor, v, 0)
		f.requireOutstanding(980)
		f.deleteVault(v)
	})

	// The payout rounds down whether or not MPTokensV2 is enabled.
	for _, mptV2 := range []bool{true, false} {
		name := "fee_rounds_payout_down_to_zero"
		if !mptV2 {
			name += "_without_MPTokensV2"
		}
		t.Run(name, func(t *testing.T) {
			f := newMPTFeeFixture(t, transferFee125, true, mptV2)
			depositor := f.other
			v := f.createFundedVault(depositor, 1)

			jtx.RequireTxFail(t, f.withdraw(depositor, v, v.shares(1), f.owner), ter.TecPRECISION_LOSS.String())

			// A self-withdrawal pays no fee, so the same share still redeems.
			jtx.RequireTxSuccess(t, f.withdraw(depositor, v, v.shares(1), nil))
			f.token.RequireMPTokenAmount(depositor, 1_000)
			f.requireVaultAssets(v, 0)
			f.deleteVault(v)
		})
	}

	t.Run("fee_leaves_other_depositors_whole", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		charlie := jtx.NewAccount("charlie")
		f.env.Fund(charlie)
		f.token.Authorize(mpttest.AuthorizeOpts{Account: charlie})
		f.token.Authorize(mpttest.AuthorizeOpts{Holder: charlie})
		f.token.Pay(f.issuer, f.owner, 100)

		v := f.createFundedVault(depositor, 100)
		jtx.RequireTxSuccess(t, f.env.Submit(vault.NewVaultDeposit(f.owner.Address, v.id, f.amount(100))))
		totalBefore, availableBefore := f.vaultNumbers(v)

		// Delivering 40 to charlie costs the depositor 50 gross.
		jtx.RequireTxSuccess(t, f.withdraw(depositor, v, f.amount(40), charlie))
		f.token.RequireMPTokenAmount(charlie, 40)
		totalAfter, availableAfter := f.vaultNumbers(v)
		require.Equal(t, totalBefore-50, totalAfter)
		require.Equal(t, availableBefore-50, availableAfter)

		// The owner's 100 shares still redeem for the 100 it deposited.
		f.requireShares(f.owner, v, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, v.shares(100), nil))
		f.token.RequireMPTokenAmount(f.owner, 100)
	})

	t.Run("no_fee_on_issuer_withdrawal_to_a_third_party", func(t *testing.T) {
		f := newMPTFeeFixture(t, transferFee125, true, true)
		depositor := f.other
		v := f.createFundedVault(f.issuer, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.issuer, v, f.amount(40), depositor))

		f.token.RequireMPTokenAmount(depositor, 1_040)
		f.requireVaultAssets(v, 60)
		f.requireShares(f.issuer, v, 60)
		f.requireOutstanding(1_100)
	})
}

// Reference: rippled VaultLifecycle_test.cpp testWithIOU transfer fee cases.
func TestVaultWithdrawIOUTransferFee(t *testing.T) {
	for _, fix350 := range []bool{false, true} {
		name := "fee_waived_without_fixCleanup3_5_0"
		if fix350 {
			name = "fee_on_third_party_withdrawal"
		}
		t.Run(name, func(t *testing.T) {
			f := newIOUFeeFixture(t, rate125, fix350)
			charlie := f.other
			v := f.createFundedVault(f.owner, 100)

			// Deposits are fee-free.
			f.requireIOU(f.owner, 100)
			f.requireVaultAssets(v, 100)

			claw := vault.NewVaultClawback(f.issuer.Address, v.id, f.owner.Address)
			amount := f.amount(50)
			claw.Amount = &amount
			jtx.RequireTxSuccess(t, f.env.Submit(claw))

			// Clawbacks are fee-free.
			f.requireIOU(f.owner, 100)
			f.requireVaultAssets(v, 50)

			jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, v.shares(20_000_000), nil))

			// Self-withdrawals are fee-free.
			f.requireIOU(f.owner, 120)
			f.requireVaultAssets(v, 30)

			jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, v.shares(30_000_000), charlie))
			expected := 30.0
			if fix350 {
				expected = 24
			}
			f.requireIOU(f.owner, 120)
			f.requireIOU(charlie, expected)
			f.requireVaultAssets(v, 0)
			f.deleteVault(v)
		})
	}

	t.Run("fee_on_fixed_asset_third_party_withdrawal", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other
		v := f.createFundedVault(f.owner, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, f.amount(40), charlie))

		// The destination receives the requested 40; the gross 50 leaves the vault.
		f.requireIOU(charlie, 40)
		f.requireVaultAssets(v, 50)
		f.requireShares(f.owner, v, 50_000_000)
		f.requireIOU(f.owner, 100)
	})

	t.Run("fee_on_fixed_share_third_party_withdrawal", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other
		v := f.createFundedVault(f.owner, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, v.shares(25_000_000), charlie))

		// The shares redeem 25 assets; the destination receives 20 net.
		f.requireIOU(charlie, 20)
		f.requireVaultAssets(v, 75)
		f.requireShares(f.owner, v, 75_000_000)
	})

	t.Run("no_fee_on_withdrawal_to_issuer", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		v := f.createFundedVault(f.owner, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, f.amount(10), f.issuer))

		f.requireVaultAssets(v, 90)
		f.requireIOU(f.owner, 100)
	})

	t.Run("no_fee_on_self_withdrawal", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		v := f.createFundedVault(f.owner, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, f.amount(10), nil))

		f.requireIOU(f.owner, 110)
		f.requireVaultAssets(v, 90)
	})

	t.Run("fee_must_fit_in_the_vault_position", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other
		v := f.createFundedVault(f.owner, 100)

		// Delivering 81 costs 101.25 gross, more than the vault position.
		jtx.RequireTxFail(t, f.withdraw(f.owner, v, f.amount(81), charlie), ter.TecINSUFFICIENT_FUNDS.String())

		// Delivering 80 costs exactly the 100 held.
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, f.amount(80), charlie))
		f.requireIOU(charlie, 80)
		f.requireVaultAssets(v, 0)
		f.deleteVault(v)
	})

	t.Run("no_fee_on_issuer_withdrawal_to_a_third_party", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other
		v := f.createFundedVault(f.issuer, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.issuer, v, f.amount(40), charlie))

		f.requireIOU(charlie, 40)
		f.requireVaultAssets(v, 60)
	})

	t.Run("receiver_limit_applies_to_the_net_amount", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other

		// The limit fits the 40 delivered but not the 50 gross.
		f.env.Trust(charlie, f.amount(40))
		v := f.createFundedVault(f.owner, 100)
		jtx.RequireTxSuccess(t, f.withdraw(f.owner, v, f.amount(40), charlie))

		f.requireIOU(charlie, 40)
		f.requireVaultAssets(v, 50)
	})

	// Requesting 51 redeems a gross 63.75 from the vault and issues 51 to a
	// destination holding 9999999999999995, whose balance rounds to
	// 10000000000000050 (a delta of 55 at a scale of ten). At that scale the
	// vault delta rounds to 60, which nets 48 after the fee, more than one unit
	// from the destination's rounded 60. The 8.75 the issuer keeps is below the
	// destination's unit, but that tolerance does not apply to a fee-bearing
	// withdrawal, so ValidVault rejects it.
	t.Run("fee_withdrawal_at_coarse_destination_scale", func(t *testing.T) {
		f := newIOUFeeFixture(t, rate125, true)
		charlie := f.other
		f.env.Trust(charlie, f.iouAmount("100000000000000000"))
		jtx.RequireTxSuccess(t, f.env.Submit(payment.NewPayment(
			f.issuer.Address, charlie.Address, f.iouAmount("9999999999999995"))))
		v := f.createFundedVault(f.owner, 100)

		result := f.withdraw(f.owner, v, f.amount(51), charlie)
		require.Equal(t, ter.TecINVARIANT_FAILED.String(), result.Code)
		f.requireVaultAssets(v, 100)
		balance := f.env.IOUBalance(charlie, f.issuer, "IOU")
		require.NotNil(t, balance)
		require.Equal(t, f.iouAmount("9999999999999995").Value(), balance.Value())
	})

	// A share-denominated withdrawal checks the receiver limit against the
	// assets the shares redeem, net of the fee once it applies.
	for _, fix350 := range []bool{false, true} {
		name := "share_withdrawal_limit_without_fixCleanup3_5_0"
		if fix350 {
			name = "share_withdrawal_limit_applies_to_the_net_amount"
		}
		t.Run(name, func(t *testing.T) {
			f := newIOUFeeFixture(t, rate125, fix350)
			charlie := f.other

			// The limit fits the 20 delivered but not the 25 gross.
			f.env.Trust(charlie, f.amount(20))
			v := f.createFundedVault(f.owner, 100)
			result := f.withdraw(f.owner, v, v.shares(25_000_000), charlie)
			if !fix350 {
				jtx.RequireTxFail(t, result, ter.TecNO_LINE.String())
				f.requireVaultAssets(v, 100)
				return
			}
			jtx.RequireTxSuccess(t, result)
			f.requireIOU(charlie, 20)
			f.requireVaultAssets(v, 75)
		})
	}
}
