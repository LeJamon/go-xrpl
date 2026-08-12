package amm_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	ammtest "github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	ammtx "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/tx/vault"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

const ammCreateFee = uint64(50_000_000)

type vaultShareFixture struct {
	env        *jtx.TestEnv
	creator    *jtx.Account
	shareID    [24]byte
	share      tx.Amount
	xrp        tx.Amount
	shareAsset tx.Asset
	xrpAsset   tx.Asset
}

func newVaultShareFixture(t *testing.T, creatorIsIssuer bool) vaultShareFixture {
	t.Helper()
	env := jtx.NewTestEnv(t)
	env.EnableFeature("SingleAssetVault")
	env.EnableFeature("MPTokensV2")
	env.Close()
	issuer := jtx.NewAccount("issuer")
	holder := jtx.NewAccount("holder")
	creator := holder
	if creatorIsIssuer {
		creator = issuer
	}
	env.Fund(issuer, holder)
	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(issuer).DefaultRipple().Build()))

	const currency = "USD"
	if !creatorIsIssuer {
		env.Trust(holder, tx.NewIssuedAmountFromFloat64(1_000, currency, issuer.Address))
		env.PayIOU(issuer, holder, issuer, currency, 500)
	}

	vaultSequence := env.Seq(creator)
	create := vault.NewVaultCreate(creator.Address, tx.Asset{Currency: currency, Issuer: issuer.Address})
	create.Fee = "50000000"
	jtx.RequireTxSuccess(t, env.Submit(create))
	vaultKey := keylet.Vault(creator.AccountID(), vaultSequence)
	vaultID := strings.ToUpper(hex.EncodeToString(vaultKey.Key[:]))
	jtx.RequireTxSuccess(t, env.Submit(vault.NewVaultDeposit(
		creator.Address,
		vaultID,
		tx.NewIssuedAmountFromFloat64(200, currency, issuer.Address),
	)))

	info, err := vault.ReadVaultInfo(env.Ledger(), vaultKey)
	require.NoError(t, err)
	shareID := info.ShareMPTID
	shareIDHex := mptutil.EncodeID(shareID)
	shareIssuer := state.EncodeAccountIDSafe(mptutil.Issuer(shareID))
	share := state.NewMPTAmountWithIssuanceID(100_000_000, shareIssuer, shareIDHex)

	return vaultShareFixture{
		env:        env,
		creator:    creator,
		shareID:    shareID,
		share:      share,
		xrp:        tx.NewXRPAmount(100_000_000),
		shareAsset: tx.Asset{MPTIssuanceID: shareIDHex},
		xrpAsset:   tx.Asset{Currency: "XRP"},
	}
}

func vaultShareBalance(t *testing.T, fixture vaultShareFixture) uint64 {
	t.Helper()
	holding, _, result := mptutil.ReadHolding(fixture.env.Ledger(), fixture.shareID, fixture.creator.ID)
	require.Equal(t, ter.TesSUCCESS, result)
	return holding.MPTAmount
}

func TestAMMCreateRejectsVaultShares(t *testing.T) {
	tests := []struct {
		name            string
		creatorIsIssuer bool
		shareFirst      bool
	}{
		{name: "holder_amount", shareFirst: true},
		{name: "holder_amount2"},
		{name: "underlying_issuer_amount", creatorIsIssuer: true, shareFirst: true},
		{name: "underlying_issuer_amount2", creatorIsIssuer: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newVaultShareFixture(t, test.creatorIsIssuer)
			amount, amount2 := fixture.xrp, fixture.share
			if test.shareFirst {
				amount, amount2 = fixture.share, fixture.xrp
			}

			balanceBefore := fixture.env.Balance(fixture.creator)
			sequenceBefore := fixture.env.Seq(fixture.creator)
			sharesBefore := vaultShareBalance(t, fixture)
			result := fixture.env.Submit(ammtest.AMMCreate(fixture.creator, amount, amount2).Build())

			jtx.RequireTxFail(t, result, "tecWRONG_ASSET")
			require.True(t, result.Applied)
			require.Equal(t, ammCreateFee, result.Fee)
			require.NotNil(t, result.Metadata)
			require.Equal(t, sequenceBefore+1, fixture.env.Seq(fixture.creator))
			require.Equal(t, balanceBefore-ammCreateFee, fixture.env.Balance(fixture.creator))
			require.Equal(t, sharesBefore, vaultShareBalance(t, fixture))
			require.False(t, fixture.env.LedgerEntryExists(
				ammtx.ComputeAMMKeylet(fixture.shareAsset, fixture.xrpAsset),
			))
		})
	}
}

func TestAMMCreateRejectsNestedVaultShareAmounts(t *testing.T) {
	fixture := newVaultShareFixture(t, false)
	tests := []struct {
		name       string
		shareFirst bool
	}{
		{name: "Amount", shareFirst: true},
		{name: "Amount2"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			amount, amount2 := fixture.xrp, fixture.share
			if test.shareFirst {
				amount, amount2 = fixture.share, fixture.xrp
			}
			create := ammtest.AMMCreate(fixture.creator, amount, amount2).Build()
			create.SetSequence(fixture.env.Seq(fixture.creator))
			blob, err := tx.SerializeTransaction(create)
			require.NoError(t, err)
			parsedTx, err := tx.ParseFromBinary(blob)
			require.NoError(t, err)
			parsed, ok := parsedTx.(*ammtx.AMMCreate)
			require.True(t, ok)
			if test.shareFirst {
				require.Equal(t, fixture.share.MPTIssuanceID(), parsed.Amount.MPTIssuanceID())
			} else {
				require.Equal(t, fixture.share.MPTIssuanceID(), parsed.Amount2.MPTIssuanceID())
			}

			result := parsed.Preclaim(fixture.env.Ledger(), tx.EngineConfig{
				ReserveBase:      10_000_000,
				ReserveIncrement: ammCreateFee,
				Rules:            amendment.AllSupportedRules(),
			})
			require.Equal(t, ter.TecWRONG_ASSET, result)
		})
	}
}

func TestAMMCreateVaultShareGateOffPreservesLegacyBehavior(t *testing.T) {
	fixture := newVaultShareFixture(t, false)
	rules := amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Disable(amendment.FeatureSingleAssetVault).
		Build()
	create := ammtest.AMMCreate(fixture.creator, fixture.xrp, fixture.share).Build()
	result := create.Preclaim(fixture.env.Ledger(), tx.EngineConfig{
		ReserveBase:      10_000_000,
		ReserveIncrement: ammCreateFee,
		Rules:            rules,
	})

	require.Equal(t, ter.TesSUCCESS, result)
}

func TestAMMCreateVaultShareAddressCollisionPrecedesAssetCheck(t *testing.T) {
	fixture := newVaultShareFixture(t, false)
	ammKey := ammtx.ComputeAMMKeylet(fixture.shareAsset, fixture.xrpAsset)
	parentHash := fixture.env.Ledger().ParentHash()
	for range 256 {
		accountID := ammtx.PseudoAccountAddress(fixture.env.Ledger(), parentHash, ammKey.Key)
		require.NotZero(t, accountID)
		account := &state.AccountRoot{
			Account:  state.EncodeAccountIDSafe(accountID),
			Balance:  1,
			Sequence: 1,
		}
		data, err := state.SerializeAccountRoot(account)
		require.NoError(t, err)
		require.NoError(t, fixture.env.Ledger().Insert(keylet.Account(accountID), data))
	}

	create := ammtest.AMMCreate(fixture.creator, fixture.share, fixture.xrp).Build()
	result := create.Preclaim(fixture.env.Ledger(), tx.EngineConfig{
		ReserveBase:      10_000_000,
		ReserveIncrement: ammCreateFee,
		ParentHash:       parentHash,
		Rules:            amendment.AllSupportedRules(),
	})

	require.Equal(t, ter.TerADDRESS_COLLISION, result)
}
