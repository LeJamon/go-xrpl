package amm_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/accountset"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type clawbackSupplyFixture struct {
	env       *amm.AMMTestEnv
	asset1    tx.Asset
	asset2    tx.Asset
	ammKey    keylet.Keylet
	stateKeys []keylet.Keylet
}

func newClawbackSupplyFixture(t *testing.T, fixAMMv1_1, fixClawbackRounding bool, recordedSupply string) clawbackSupplyFixture {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.DisableFeature("fixAMMv1_3")
	if fixAMMv1_1 {
		env.EnableFeature("fixAMMv1_1")
	} else {
		env.DisableFeature("fixAMMv1_1")
	}
	if fixClawbackRounding {
		env.EnableFeature("fixAMMClawbackRounding")
	} else {
		env.DisableFeature("fixAMMClawbackRounding")
	}

	for _, account := range []*jtx.Account{env.GW, env.Alice} {
		env.TestEnv.FundAmount(account, uint64(jtx.XRP(100_000)))
	}
	env.Close()
	if env.Rules().Enabled(amendment.FeatureFixAMMv1_1) != fixAMMv1_1 {
		t.Fatalf("fixAMMv1_1 state: got %t, want %t", env.Rules().Enabled(amendment.FeatureFixAMMv1_1), fixAMMv1_1)
	}
	if env.Rules().Enabled(amendment.FeatureFixAMMClawbackRounding) != fixClawbackRounding {
		t.Fatalf("fixAMMClawbackRounding state: got %t, want %t", env.Rules().Enabled(amendment.FeatureFixAMMClawbackRounding), fixClawbackRounding)
	}

	jtx.RequireTxSuccess(t, env.Submit(accountset.AccountSet(env.GW).AllowClawback().Build()))
	env.Close()
	for _, currency := range []string{"USD", "EUR"} {
		env.Trust(env.Alice, env.GW, currency, 100_000)
		env.PayIOU(env.GW, env.Alice, currency, 50_000)
	}
	env.Close()

	asset1 := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
	asset2 := tx.Asset{Currency: "EUR", Issuer: env.GW.Address}
	jtx.RequireTxSuccess(t, env.Submit(amm.AMMCreate(env.Alice,
		amm.IOUAmount(env.GW, "USD", 2),
		amm.IOUAmount(env.GW, "EUR", 1),
	).Build()))
	env.Close()

	ammAccount := amm.AMMAccount(t, env, asset1, asset2)
	lptIssue := env.LPTokenAmountFromLedger(asset1, asset2, 0)
	holdingPtr, found := env.LookupIOUBalance(env.Alice, ammAccount, lptIssue.Currency)
	require.True(t, found)
	require.NotNil(t, holdingPtr)
	holding := *holdingPtr
	recorded, err := state.NewIssuedAmountFromDecimalString(recordedSupply, lptIssue.Currency, ammAccount.Address)
	require.NoError(t, err)
	require.Greater(t, holding.Compare(recorded), 0)
	ammKey := setRecordedAMMSupply(t, env, asset1, asset2, lptIssue, recorded)

	return clawbackSupplyFixture{
		env:    env,
		asset1: asset1,
		asset2: asset2,
		ammKey: ammKey,
		stateKeys: []keylet.Keylet{
			ammKey,
			keylet.Line(ammAccount.ID, env.GW.ID, asset1.Currency),
			keylet.Line(ammAccount.ID, env.GW.ID, asset2.Currency),
			keylet.Line(env.Alice.ID, ammAccount.ID, lptIssue.Currency),
			keylet.Line(env.Alice.ID, env.GW.ID, asset1.Currency),
			keylet.Line(env.Alice.ID, env.GW.ID, asset2.Currency),
		},
	}
}

func TestAMMClawbackSupplyGuard(t *testing.T) {
	cases := []struct {
		name             string
		fixAMMv1_1       bool
		fixClawbackRound bool
		wantCode         string
	}{
		{name: "AmendmentOn/RoundingOff", fixAMMv1_1: true, fixClawbackRound: false, wantCode: "tecINTERNAL"},
		{name: "AmendmentOff/RoundingOff", fixAMMv1_1: false, fixClawbackRound: false, wantCode: "tecAMM_BALANCE"},
		{name: "AmendmentOn/RoundingOn", fixAMMv1_1: true, fixClawbackRound: true, wantCode: "tesSUCCESS"},
		{name: "AmendmentOff/RoundingOn", fixAMMv1_1: false, fixClawbackRound: true, wantCode: "tesSUCCESS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newClawbackSupplyFixture(t, tc.fixAMMv1_1, tc.fixClawbackRound, "1.414213562373")
			sequence := fixture.env.Seq(fixture.env.GW)
			balance := fixture.env.Balance(fixture.env.GW)
			before := snapshotLedgerState(t, fixture.env.TestEnv, fixture.stateKeys)
			result := fixture.env.Submit(amm.AMMClawback(fixture.env.GW, fixture.env.Alice.Address, fixture.asset1, fixture.asset2).
				ClawTwoAssets().Build())
			if tc.wantCode != "tesSUCCESS" {
				requireClaimedSupplyFailure(t, fixture.env.TestEnv, fixture.env.GW, result, tc.wantCode, before, sequence, balance)
				return
			}
			amm.ExpectTER(t, result, tc.wantCode)
			require.False(t, fixture.env.LedgerEntryExists(fixture.ammKey))
		})
	}
}
