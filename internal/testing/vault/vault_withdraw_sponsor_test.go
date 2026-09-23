package vault_test

import (
	"encoding/hex"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/applystate"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	signtx "github.com/LeJamon/go-xrpl/internal/tx/sign"
	sponsortx "github.com/LeJamon/go-xrpl/internal/tx/sponsor"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const (
	vaultWithdrawDepositDrops int64 = 100_000_000
	vaultWithdrawAmountDrops  int64 = 10_000_000
)

type vaultWithdrawFixture struct {
	env       *jtx.TestEnv
	owner     *jtx.Account
	depositor *jtx.Account
	sponsor   *jtx.Account
	delegate  *jtx.Account
	vaultID   string
}

func newVaultWithdrawFixture(t *testing.T, cleanup340 bool) *vaultWithdrawFixture {
	t.Helper()

	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("Sponsor")
	env.EnableFeature("PermissionDelegationV1_1")
	env.EnableFeature("fixCleanup3_4_0")
	if !cleanup340 {
		env.DisableFeature("fixCleanup3_4_0")
	}
	env.Close()

	fixture := &vaultWithdrawFixture{
		env:       env,
		owner:     jtx.NewAccount("vault-withdraw-owner"),
		depositor: jtx.NewAccount("vault-withdraw-depositor"),
		sponsor:   jtx.NewAccount("vault-withdraw-sponsor"),
		delegate:  jtx.NewAccount("vault-withdraw-delegate"),
	}
	env.Fund(fixture.owner, fixture.depositor, fixture.sponsor, fixture.delegate)
	env.Close()

	createSequence := env.Seq(fixture.owner)
	create := vault.NewVaultCreate(fixture.owner.Address, tx.Asset{Currency: "XRP"})
	create.Common.Fee = createFee
	jtx.RequireTxSuccess(t, env.Submit(create))
	fixture.vaultID = vaultID(fixture.owner, createSequence)

	deposit := vault.NewVaultDeposit(
		fixture.depositor.Address,
		fixture.vaultID,
		tx.NewXRPAmount(vaultWithdrawDepositDrops),
	)
	jtx.RequireTxSuccess(t, env.Submit(deposit))
	env.Close()

	return fixture
}

func (f *vaultWithdrawFixture) withdraw(destination string) *vault.VaultWithdraw {
	withdraw := vault.NewVaultWithdraw(
		f.depositor.Address,
		f.vaultID,
		tx.NewXRPAmount(vaultWithdrawAmountDrops),
	)
	withdraw.Destination = destination
	withdraw.Common.Fee = strconv.FormatUint(f.env.BaseFee(), 10)
	return withdraw
}

func readVaultWithdrawState(t *testing.T, f *vaultWithdrawFixture) *vault.VaultLending {
	t.Helper()
	rawID, err := hex.DecodeString(f.vaultID)
	require.NoError(t, err)
	var vaultKey [32]byte
	copy(vaultKey[:], rawID)
	vaultState, err := vault.ReadVaultLending(f.env.Ledger(), keylet.VaultByID(vaultKey))
	require.NoError(t, err)
	require.NotNil(t, vaultState)
	return vaultState
}

func assertVaultWithdrawPoolChanged(t *testing.T, before, after *vault.VaultLending) {
	t.Helper()
	want := state.NewXRPLNumberScaled(
		vaultWithdrawDepositDrops-vaultWithdrawAmountDrops,
		0,
		state.MantissaScaleLarge,
		state.RoundToNearest,
	)
	for name, got := range map[string]string{
		"AssetsTotal":     after.AssetsTotal,
		"AssetsAvailable": after.AssetsAvailable,
	} {
		number, err := state.ParseXRPLNumber(got, state.MantissaScaleLarge, state.RoundToNearest)
		require.NoError(t, err, name)
		require.True(t, number.Equal(want), "%s = %s, want %s", name, got, want)
	}
	require.Equal(t, before.LossUnrealized, after.LossUnrealized)
}

func assertVaultWithdrawPoolUnchanged(t *testing.T, before, after *vault.VaultLending) {
	t.Helper()
	require.Equal(t, before.AssetsTotal, after.AssetsTotal)
	require.Equal(t, before.AssetsAvailable, after.AssetsAvailable)
	require.Equal(t, before.AssetsMaximum, after.AssetsMaximum)
	require.Equal(t, before.LossUnrealized, after.LossUnrealized)
}

func signingPrivateKey(account *jtx.Account) string {
	prefix := "00"
	if account.IsEd25519() {
		prefix = "ED"
	}
	return prefix + account.PrivateKeyHex()
}

func attachVaultSponsorSignature(
	t *testing.T,
	env *jtx.TestEnv,
	transaction tx.Transaction,
	source, sponsor *jtx.Account,
) {
	t.Helper()
	common := transaction.GetCommon()
	sequence := env.Seq(source)
	common.Sequence = &sequence
	common.Fee = strconv.FormatUint(env.BaseFee(), 10)
	common.SigningPubKey = source.PublicKeyHex()
	signature, err := signtx.SignSponsorWithRules(
		transaction,
		sponsor.PublicKeyHex(),
		signingPrivateKey(sponsor),
		env.Rules(),
	)
	require.NoError(t, err)
	common.SponsorSignature = signature
}

func setVaultFeeSponsorship(t *testing.T, f *vaultWithdrawFixture) {
	t.Helper()
	set := sponsortx.NewSponsorshipSet(f.sponsor.Address)
	set.Sponsee = f.depositor.Address
	feeAmount := tx.NewXRPAmount(100)
	set.FeeAmountDelta = &feeAmount
	maxFee := tx.NewXRPAmount(int64(f.env.BaseFee()))
	set.MaxFee = &maxFee
	jtx.RequireTxSuccess(t, f.env.Submit(set))
	f.env.Close()
}

func readVaultFeeSponsorship(t *testing.T, f *vaultWithdrawFixture) *state.SponsorshipData {
	t.Helper()
	data, err := f.env.LedgerEntry(keylet.Sponsorship(f.sponsor.ID, f.depositor.ID))
	require.NoError(t, err)
	require.NotNil(t, data)
	sponsorship, err := state.ParseSponsorship(data)
	require.NoError(t, err)
	require.NotNil(t, sponsorship)
	return sponsorship
}

func TestVaultWithdraw_OrdinaryFeePayerFullEngine(t *testing.T) {
	for _, cleanup340 := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup340 {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultWithdrawFixture(t, cleanup340)
			fee := f.env.BaseFee()
			sourceBalance := f.env.Balance(f.depositor)
			sourceSequence := f.env.Seq(f.depositor)
			poolBefore := readVaultWithdrawState(t, f)

			result := f.env.Submit(f.withdraw(f.depositor.Address))
			require.Equal(t, "tesSUCCESS", result.Code)
			require.True(t, result.Applied)
			require.Equal(t, fee, result.Fee)
			require.Equal(t, sourceBalance+uint64(vaultWithdrawAmountDrops)-fee, f.env.Balance(f.depositor))
			require.Equal(t, sourceSequence+1, f.env.Seq(f.depositor))
			assertVaultWithdrawPoolChanged(t, poolBefore, readVaultWithdrawState(t, f))
		})
	}
}

func TestVaultWithdraw_DelegatedFeePayerRejectedByProtocol(t *testing.T) {
	for _, cleanup340 := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup340 {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultWithdrawFixture(t, cleanup340)
			fee := f.env.BaseFee()
			sourceBalance := f.env.Balance(f.depositor)
			sourceSequence := f.env.Seq(f.depositor)
			delegateBalance := f.env.Balance(f.delegate)
			delegateSequence := f.env.Seq(f.delegate)
			poolBefore := readVaultWithdrawState(t, f)

			withdraw := f.withdraw(f.delegate.Address)
			withdraw.Delegate = f.delegate.Address
			result := f.env.SubmitSignedWith(withdraw, f.delegate)
			require.Equal(t, "temINVALID", result.Code)
			require.False(t, result.Applied)
			require.Zero(t, result.Fee)
			require.Equal(t, sourceBalance, f.env.Balance(f.depositor))
			require.Equal(t, sourceSequence, f.env.Seq(f.depositor))
			require.Equal(t, delegateBalance, f.env.Balance(f.delegate))
			require.Equal(t, delegateSequence, f.env.Seq(f.delegate))
			require.Equal(t, fee, f.env.BaseFee())
			assertVaultWithdrawPoolUnchanged(t, poolBefore, readVaultWithdrawState(t, f))
		})
	}
}

func TestVaultWithdraw_CosignedSponsorFeePayerDestination(t *testing.T) {
	for _, cleanup340 := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup340 {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultWithdrawFixture(t, cleanup340)
			fee := f.env.BaseFee()
			sourceBalance := f.env.Balance(f.depositor)
			sourceSequence := f.env.Seq(f.depositor)
			sponsorBalance := f.env.Balance(f.sponsor)
			sponsorSequence := f.env.Seq(f.sponsor)
			poolBefore := readVaultWithdrawState(t, f)

			withdraw := f.withdraw(f.sponsor.Address)
			withdraw.Sponsor = f.sponsor.Address
			flags := tx.SpfSponsorFee
			withdraw.SponsorFlags = &flags
			attachVaultSponsorSignature(t, f.env, withdraw, f.depositor, f.sponsor)
			result := f.env.SubmitSigned(withdraw)

			if cleanup340 {
				require.Equal(t, "tesSUCCESS", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, fee, result.Fee)
				require.Equal(t, sponsorBalance+uint64(vaultWithdrawAmountDrops)-fee, f.env.Balance(f.sponsor))
				assertVaultWithdrawPoolChanged(t, poolBefore, readVaultWithdrawState(t, f))
			} else {
				require.Equal(t, "tecINVARIANT_FAILED", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, fee, result.Fee)
				require.Equal(t, sponsorBalance-fee, f.env.Balance(f.sponsor))
				assertVaultWithdrawPoolUnchanged(t, poolBefore, readVaultWithdrawState(t, f))
			}
			require.Equal(t, sourceBalance, f.env.Balance(f.depositor))
			require.Equal(t, sourceSequence+1, f.env.Seq(f.depositor))
			require.Equal(t, sponsorSequence, f.env.Seq(f.sponsor))
		})
	}
}

func TestVaultWithdraw_PrefundedSponsorFeePayerDestination(t *testing.T) {
	for _, cleanup340 := range []bool{false, true} {
		name := "cleanup-off"
		if cleanup340 {
			name = "cleanup-on"
		}
		t.Run(name, func(t *testing.T) {
			f := newVaultWithdrawFixture(t, cleanup340)
			setVaultFeeSponsorship(t, f)
			fee := f.env.BaseFee()
			sourceBalance := f.env.Balance(f.depositor)
			sourceSequence := f.env.Seq(f.depositor)
			sponsorBalance := f.env.Balance(f.sponsor)
			sponsorSequence := f.env.Seq(f.sponsor)
			poolBefore := readVaultWithdrawState(t, f)
			sponsorshipBefore := readVaultFeeSponsorship(t, f)
			require.Equal(t, uint64(100), sponsorshipBefore.FeeAmount)

			withdraw := f.withdraw(f.sponsor.Address)
			withdraw.Sponsor = f.sponsor.Address
			flags := tx.SpfSponsorFee
			withdraw.SponsorFlags = &flags
			result := f.env.Submit(withdraw)

			if cleanup340 {
				require.Equal(t, "tesSUCCESS", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, fee, result.Fee)
				require.Equal(t, sponsorBalance+uint64(vaultWithdrawAmountDrops), f.env.Balance(f.sponsor))
				assertVaultWithdrawPoolChanged(t, poolBefore, readVaultWithdrawState(t, f))
			} else {
				require.Equal(t, "tecINVARIANT_FAILED", result.Code)
				require.True(t, result.Applied)
				require.Equal(t, fee, result.Fee)
				require.Equal(t, sponsorBalance, f.env.Balance(f.sponsor))
				assertVaultWithdrawPoolUnchanged(t, poolBefore, readVaultWithdrawState(t, f))
			}
			require.Equal(t, sourceBalance, f.env.Balance(f.depositor))
			require.Equal(t, sourceSequence+1, f.env.Seq(f.depositor))
			require.Equal(t, sponsorSequence, f.env.Seq(f.sponsor))
			sponsorshipAfter := readVaultFeeSponsorship(t, f)
			require.True(t, sponsorshipAfter.HasFeeAmount)
			require.Equal(t, uint64(100)-fee, sponsorshipAfter.FeeAmount)
		})
	}
}

func TestVaultWithdraw_InvariantFailureRollsBackMovementAndClaimsFee(t *testing.T) {
	f := newVaultWithdrawFixture(t, true)
	fee := f.env.BaseFee()
	sourceBalance := f.env.Balance(f.depositor)
	sourceSequence := f.env.Seq(f.depositor)
	sponsorBalance := f.env.Balance(f.sponsor)
	sponsorSequence := f.env.Seq(f.sponsor)
	poolBefore := readVaultWithdrawState(t, f)

	withdraw := f.withdraw(f.sponsor.Address)
	withdraw.Sponsor = f.sponsor.Address
	flags := tx.SpfSponsorFee
	withdraw.SponsorFlags = &flags
	attachVaultSponsorSignature(t, f.env, withdraw, f.depositor, f.sponsor)
	f.env.SetInvariantViolationHook(func(result ter.Result, _ *applystate.ApplyStateTable) *txengine.InvariantViolationValue {
		if result == ter.TesSUCCESS {
			return txengine.NewInvariantViolation("VaultWithdrawTest", "rollback vault movement")
		}
		return nil
	})
	defer f.env.SetInvariantViolationHook(nil)

	result := f.env.SubmitSigned(withdraw)
	require.Equal(t, "tecINVARIANT_FAILED", result.Code)
	require.True(t, result.Applied)
	require.Equal(t, fee, result.Fee)
	require.Equal(t, sourceBalance, f.env.Balance(f.depositor))
	require.Equal(t, sourceSequence+1, f.env.Seq(f.depositor))
	require.Equal(t, sponsorBalance-fee, f.env.Balance(f.sponsor))
	require.Equal(t, sponsorSequence, f.env.Seq(f.sponsor))
	assertVaultWithdrawPoolUnchanged(t, poolBefore, readVaultWithdrawState(t, f))
}
