package vault

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type sharedHelperView struct {
	*mptArmsView
	rules *amendment.Rules
}

func newSharedHelperView(rules *amendment.Rules) *sharedHelperView {
	return &sharedHelperView{mptArmsView: newMPTArmsView(), rules: rules}
}

func (v *sharedHelperView) Rules() *amendment.Rules { return v.rules }

func TestSpendableAssetIncludesOppositeTrustLimit(t *testing.T) {
	rules := amendment.NewRules([][32]byte{amendment.FeatureSingleAssetVault})
	view := newSharedHelperView(rules)
	var account, issuer [20]byte
	account[19] = 0x10
	issuer[19] = 0x20
	accountAddress := state.EncodeAccountIDSafe(account)
	issuerAddress := state.EncodeAccountIDSafe(issuer)
	asset := tx.Asset{Currency: "USD", Issuer: issuerAddress}

	line := &state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(3_000_000_000_000_000, -14, "USD", issuerAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, state.MinExponent, "USD", accountAddress),
		HighLimit: state.NewIssuedAmountFromValue(7_000_000_000_000_000, -14, "USD", issuerAddress),
	}
	raw, err := state.SerializeRippleState(line)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Line(account, issuer, "USD"), raw))

	spendable, err := spendableAsset(view, tx.EngineConfig{Rules: rules}, account, asset)
	require.NoError(t, err)
	want := state.NewXRPLNumberScaled(100, 0, state.MantissaScaleLargeLegacy, state.RoundToNearest)
	require.Zero(t, spendable.Cmp(want))

	holding, err := actualAssetHolding(view, account, asset, rules)
	require.NoError(t, err)
	want = state.NewXRPLNumberScaled(30, 0, state.MantissaScaleLargeLegacy, state.RoundToNearest)
	require.Zero(t, holding.Cmp(want))
}

func TestMintSharesEnforcesMaximumAmount(t *testing.T) {
	view := newMPTArmsView()
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	ctx := buildArmsCtx(t, view, holder, rulesWithFix(true))
	id := keylet.MakeMPTID(7, issuer)
	maximum := uint64(100)
	issuance := &state.MPTokenIssuanceData{
		Issuer:            issuer,
		Sequence:          7,
		MaximumAmount:     &maximum,
		OutstandingAmount: 90,
	}
	issuanceRaw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), issuanceRaw))
	tokenRaw, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		MPTAmount:         90,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTokenByID(id, holder), tokenRaw))

	require.Equal(t, ter.TecPATH_DRY, mintShares(ctx, id, holder, 11))
	updated, err := readMPTIssuance(view, id)
	require.NoError(t, err)
	require.Equal(t, uint64(90), updated.OutstandingAmount)
}

func TestWithdrawSelfMPTReserveAndOwnerCount(t *testing.T) {
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(9, issuer)
	asset := tx.Asset{MPTIssuanceID: hex.EncodeToString(id[:])}

	build := func(t *testing.T, ownerCount uint32, balance uint64) (*tx.ApplyContext, *mptArmsView) {
		t.Helper()
		view := newMPTArmsView()
		ctx := buildArmsCtx(t, view, holder, rulesWithFix(true))
		ctx.Account.OwnerCount = ownerCount
		ctx.Account.Balance = balance
		ctx.Config.ReserveBase = 20
		ctx.Config.ReserveIncrement = 10
		issuanceRaw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
			Issuer: issuer,
			Flags:  entry.LsfMPTCanTransfer,
		})
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.MPTIssuance(id), issuanceRaw))
		return ctx, view
	}

	t.Run("third object requires reserve", func(t *testing.T) {
		ctx, view := build(t, 2, 49)
		require.Equal(t, ter.TecINSUFFICIENT_RESERVE, addWithdrawDestinationHolding(ctx, asset))
		exists, err := view.Exists(keylet.MPTokenByID(id, holder))
		require.NoError(t, err)
		require.False(t, exists)
		require.Equal(t, uint32(2), ctx.Account.OwnerCount)
	})

	t.Run("second object is reserve free and increments owner count", func(t *testing.T) {
		ctx, view := build(t, 1, 0)
		require.Equal(t, ter.TesSUCCESS, addWithdrawDestinationHolding(ctx, asset))
		exists, err := view.Exists(keylet.MPTokenByID(id, holder))
		require.NoError(t, err)
		require.True(t, exists)
		require.Equal(t, uint32(2), ctx.Account.OwnerCount)
	})

	t.Run("existing holding does not recheck reserve", func(t *testing.T) {
		ctx, view := build(t, 2, 0)
		raw, err := state.SerializeMPToken(&state.MPTokenData{
			Account:           holder,
			MPTokenIssuanceID: id,
		})
		require.NoError(t, err)
		require.NoError(t, view.Insert(keylet.MPTokenByID(id, holder), raw))
		require.Equal(t, ter.TesSUCCESS, addWithdrawDestinationHolding(ctx, asset))
		require.Equal(t, uint32(2), ctx.Account.OwnerCount)
	})
}

func TestWithdrawSelfIOUReserveUsesUpdatedOwnerCount(t *testing.T) {
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	view := newMPTArmsView()
	ctx := buildArmsCtx(t, view, holder, rulesWithFix(true))
	ctx.Account.OwnerCount = 2
	ctx.Account.Balance = 40
	ctx.Config.ReserveBase = 20
	ctx.Config.ReserveIncrement = 10
	require.Equal(t, ter.TesSUCCESS, ctx.UpdateAccountRoot(holder, ctx.Account))

	shareID := keylet.MakeMPTID(7, issuer)
	tokenKey := keylet.MPTokenByID(shareID, holder)
	dir, err := state.DirInsert(view, keylet.OwnerDir(holder), tokenKey.Key, false, func(node *state.DirectoryNode) {
		node.Owner = holder
	})
	require.NoError(t, err)
	token, err := state.SerializeMPToken(&state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: shareID,
		OwnerNode:         dir.Page,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(tokenKey, token))
	require.Equal(t, ter.TesSUCCESS, removeEmptyShareMPToken(ctx, holder, shareID))
	require.Equal(t, uint32(1), ctx.Account.OwnerCount)

	issuerAddress := state.EncodeAccountIDSafe(issuer)
	issuerData, err := state.SerializeAccountRoot(&state.AccountRoot{
		Account: issuerAddress,
		Flags:   state.LsfDefaultRipple,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(issuer), issuerData))
	delta, result := addEmptyHolding(ctx, holder, tx.Asset{Currency: "USD", Issuer: issuerAddress})
	require.Equal(t, ter.TesSUCCESS, result)
	require.Equal(t, int32(1), delta)
}

func TestAssetDispatchPreservesMPTPrecedenceAndFreezeTER(t *testing.T) {
	rules := amendment.NewRulesBuilder().
		Enable(amendment.FeatureSingleAssetVault).
		Enable(amendment.FeatureFixCleanup3_2_0).
		Build()
	view := newSharedHelperView(rules)
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(7, issuer)
	asset := tx.Asset{MPTIssuanceID: hex.EncodeToString(id[:])}

	// Issuance lookup precedes the waiver and issuer-involvement checks.
	require.Equal(t, ter.TecOBJECT_NOT_FOUND, canTransfer(view, asset, issuer, holder, true))

	issuanceRaw, err := state.SerializeMPTokenIssuance(&state.MPTokenIssuanceData{
		Issuer:   issuer,
		Sequence: 7,
		Flags:    entry.LsfMPTLocked,
	})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.MPTIssuance(id), issuanceRaw))
	require.Equal(t, ter.TesSUCCESS, canTransfer(view, asset, issuer, holder, false))
	require.Equal(t, ter.TecLOCKED, checkFrozen(view, asset, holder))
	require.Equal(t, ter.TesSUCCESS, checkFrozen(view, tx.Asset{Currency: "XRP"}, holder))
}

func TestRemoveVaultAssetMPTHoldingHonorsLockedAmountAmendment(t *testing.T) {
	var issuer, holder [20]byte
	issuer[19] = 1
	holder[19] = 2
	id := keylet.MakeMPTID(9, issuer)
	asset := tx.Asset{MPTIssuanceID: hex.EncodeToString(id[:])}
	locked := uint64(5)

	build := func(t *testing.T, fixEnabled bool) (*tx.ApplyContext, *sharedHelperView) {
		builder := amendment.NewRulesBuilder().Enable(amendment.FeatureSingleAssetVault)
		if fixEnabled {
			builder.Enable(amendment.FeatureFixCleanup3_1_3)
		}
		view := newSharedHelperView(builder.Build())
		tokenKey := keylet.MPTokenByID(id, holder)
		dir, err := state.DirInsert(view, keylet.OwnerDir(holder), tokenKey.Key, false, func(node *state.DirectoryNode) {
			node.Owner = holder
		})
		require.NoError(t, err)
		raw, err := state.SerializeMPToken(&state.MPTokenData{
			Account:           holder,
			MPTokenIssuanceID: id,
			LockedAmount:      &locked,
			OwnerNode:         dir.Page,
		})
		require.NoError(t, err)
		require.NoError(t, view.Insert(tokenKey, raw))
		return &tx.ApplyContext{View: view, Config: tx.EngineConfig{Rules: view.rules}}, view
	}

	t.Run("fix disabled removes holding", func(t *testing.T) {
		ctx, view := build(t, false)
		delta, result := removeVaultAssetHolding(ctx, holder, asset)
		require.Equal(t, ter.TesSUCCESS, result)
		require.Equal(t, int32(-1), delta)
		exists, err := view.Exists(keylet.MPTokenByID(id, holder))
		require.NoError(t, err)
		require.False(t, exists)
	})

	t.Run("fix enabled preserves obligation", func(t *testing.T) {
		ctx, view := build(t, true)
		delta, result := removeVaultAssetHolding(ctx, holder, asset)
		require.Equal(t, ter.TecHAS_OBLIGATIONS, result)
		require.Zero(t, delta)
		exists, err := view.Exists(keylet.MPTokenByID(id, holder))
		require.NoError(t, err)
		require.True(t, exists)
	})
}
