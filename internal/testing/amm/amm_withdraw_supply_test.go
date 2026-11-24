package amm_test

import (
	"bytes"
	"strconv"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/internal/testing/amm"
	"github.com/LeJamon/go-xrpl/internal/testing/metadata"
	"github.com/LeJamon/go-xrpl/internal/tx"
	coreamm "github.com/LeJamon/go-xrpl/internal/tx/amm"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/stretchr/testify/require"
)

type withdrawSupplyFixture struct {
	env       *amm.AMMTestEnv
	asset1    tx.Asset
	asset2    tx.Asset
	amm       *jtx.Account
	lptIssue  tx.Amount
	ammKey    keylet.Keylet
	holding   tx.Amount
	stored    tx.Amount
	stateKeys []keylet.Keylet
}

func newWithdrawSupplyFixture(t *testing.T, extraLP bool, recordedSupply string) withdrawSupplyFixture {
	t.Helper()
	env := amm.NewAMMTestEnv(t)
	env.DisableFeature("SingleAssetVault")
	env.DisableFeature("LendingProtocol")
	env.DisableFeature("fixAMMv1_3")

	for _, account := range []*jtx.Account{env.GW, env.Alice, env.Carol} {
		env.TestEnv.FundAmount(account, uint64(jtx.XRP(30_000)))
	}
	env.Close()

	asset1 := tx.Asset{Currency: "ABC", Issuer: env.GW.Address}
	asset2 := tx.Asset{Currency: "USD", Issuer: env.GW.Address}
	result := env.Submit(amm.AMMCreate(env.GW,
		amm.IOUAmount(env.GW, "ABC", 2_000_000),
		amm.IOUAmount(env.GW, "USD", 1),
	).Build())
	if !result.Success {
		t.Fatalf("AMMCreate: %s - %s", result.Code, result.Message)
	}
	env.Close()

	ammAccount := amm.AMMAccount(t, env, asset1, asset2)
	lptIssue := env.LPTokenAmountFromLedger(asset1, asset2, 0)
	if extraLP {
		env.Trust(env.Alice, ammAccount, lptIssue.Currency, 1_000_000)
		env.Close()
		lineKey := keylet.Line(ammAccount.ID, env.Alice.ID, lptIssue.Currency)
		require.True(t, env.LedgerEntryExists(lineKey))
		lineBalance, found := env.LookupIOUBalance(env.Alice, ammAccount, lptIssue.Currency)
		require.True(t, found)
		require.NotNil(t, lineBalance)
		require.True(t, lineBalance.IsZero())
	}

	ammData := env.ReadAMMData(asset1, asset2)
	require.NotNil(t, ammData)
	holdingPtr, found := env.LookupIOUBalance(env.GW, ammAccount, lptIssue.Currency)
	require.True(t, found)
	require.NotNil(t, holdingPtr)
	holding := *holdingPtr

	// Keep the actual pool and trust lines intact while reproducing the stale
	// recorded supply from the incident parent state.
	staleSupply := holding
	if recordedSupply != "" {
		var err error
		staleSupply, err = state.NewIssuedAmountFromDecimalString(recordedSupply, lptIssue.Currency, ammAccount.Address)
		require.NoError(t, err)
	}
	ammKey := setRecordedAMMSupply(t, env, asset1, asset2, lptIssue, staleSupply)
	return withdrawSupplyFixture{
		env:       env,
		asset1:    asset1,
		asset2:    asset2,
		amm:       ammAccount,
		lptIssue:  lptIssue,
		ammKey:    ammKey,
		holding:   holding,
		stored:    staleSupply,
		stateKeys: withdrawSupplyStateKeys(ammKey, ammAccount, env.GW, env.Alice, asset1, asset2, lptIssue, extraLP),
	}
}

func setRecordedAMMSupply(t *testing.T, env *amm.AMMTestEnv, asset1, asset2 tx.Asset, lptIssue, recorded tx.Amount) keylet.Keylet {
	t.Helper()
	ammKey := coreamm.ComputeAMMKeylet(asset1, asset2)
	raw, err := env.LedgerEntry(ammKey)
	require.NoError(t, err)
	var decoded entry.AMM
	require.NoError(t, decoded.Decode(raw))
	require.NoError(t, decoded.SetLPTokenBalanceValue(entry.AmountValue{
		Value:    recorded.Value(),
		Currency: lptIssue.Currency,
		Issuer:   lptIssue.Issuer,
	}))
	updated, err := decoded.Encode()
	require.NoError(t, err)
	require.NoError(t, env.Ledger().Update(ammKey, updated))
	got := env.ReadAMMData(asset1, asset2)
	require.NotNil(t, got)
	require.Equal(t, 0, got.LPTokenBalance.Compare(recorded))
	return ammKey
}

func withdrawSupplyStateKeys(ammKey keylet.Keylet, ammAccount, issuer, extraHolder *jtx.Account, asset1, asset2 tx.Asset, lptIssue tx.Amount, extraLP bool) []keylet.Keylet {
	keys := []keylet.Keylet{
		ammKey,
		keylet.Line(ammAccount.ID, issuer.ID, asset1.Currency),
		keylet.Line(ammAccount.ID, issuer.ID, asset2.Currency),
		keylet.Line(issuer.ID, ammAccount.ID, lptIssue.Currency),
	}
	if extraLP {
		keys = append(keys, keylet.Line(ammAccount.ID, extraHolder.ID, lptIssue.Currency))
	}
	return keys
}

func snapshotLedgerState(t *testing.T, env *jtx.TestEnv, keys []keylet.Keylet) map[keylet.Keylet][]byte {
	t.Helper()
	snapshot := make(map[keylet.Keylet][]byte, len(keys))
	for _, key := range keys {
		if !env.LedgerEntryExists(key) {
			snapshot[key] = nil
			continue
		}
		data, err := env.LedgerEntry(key)
		require.NoError(t, err)
		snapshot[key] = append([]byte(nil), data...)
	}
	return snapshot
}

func requireLedgerStateUnchanged(t *testing.T, env *jtx.TestEnv, before map[keylet.Keylet][]byte) {
	t.Helper()
	for key, want := range before {
		if want == nil {
			require.False(t, env.LedgerEntryExists(key), "ledger entry %x was created", key.Key)
			continue
		}
		got, err := env.LedgerEntry(key)
		require.NoError(t, err)
		require.True(t, bytes.Equal(want, got), "ledger entry %x changed", key.Key)
	}
}

func requireClaimedSupplyFailure(t *testing.T, env *jtx.TestEnv, account *jtx.Account, result jtx.TxResult, wantCode string, before map[keylet.Keylet][]byte, sequence uint32, balance uint64) {
	t.Helper()
	amm.ExpectTER(t, result, wantCode)
	require.True(t, result.Applied)
	require.Equal(t, env.BaseFee(), result.Fee)
	require.Equal(t, sequence+1, env.Seq(account))
	require.Equal(t, balance-result.Fee, env.Balance(account))
	requireLedgerStateUnchanged(t, env, before)

	require.NotNil(t, result.Metadata)
	require.Len(t, result.Metadata.AffectedNodes, 1)
	node := metadata.FindNodeByAccount(result.Metadata, account.Address)
	require.NotNil(t, node)
	require.Equal(t, "ModifiedNode", node.NodeType)
	require.Equal(t, strconv.FormatUint(balance-result.Fee, 10), metadata.GetFinalField(node, "Balance"))
	require.Equal(t, strconv.FormatUint(balance, 10), metadata.GetPreviousField(node, "Balance"))
	require.Equal(t, sequence+1, metadata.ToUint32(metadata.GetFinalField(node, "Sequence")))
	require.Equal(t, sequence, metadata.ToUint32(metadata.GetPreviousField(node, "Sequence")))
}

func TestAMMWithdrawSupplyGuard(t *testing.T) {
	cases := []struct {
		name              string
		recordedSupply    string
		holdingVsRecorded int
		wantCode          string
	}{
		{name: "AboveRecorded", recordedSupply: "1414.213562373", holdingVsRecorded: 1, wantCode: "tecINTERNAL"},
		{name: "EqualRecorded", recordedSupply: "", holdingVsRecorded: 0, wantCode: "tesSUCCESS"},
		{name: "BelowRecorded", recordedSupply: "1414.213562373096", holdingVsRecorded: -1, wantCode: "tesSUCCESS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newWithdrawSupplyFixture(t, true, tc.recordedSupply)
			require.Equal(t, tc.holdingVsRecorded, fixture.holding.Compare(fixture.stored))
			sequence := fixture.env.Seq(fixture.env.GW)
			balance := fixture.env.Balance(fixture.env.GW)
			before := snapshotLedgerState(t, fixture.env.TestEnv, fixture.stateKeys)
			result := fixture.env.Submit(amm.AMMWithdraw(fixture.env.GW, fixture.asset1, fixture.asset2).WithdrawAll().Build())
			if tc.wantCode != "tesSUCCESS" {
				requireClaimedSupplyFailure(t, fixture.env.TestEnv, fixture.env.GW, result, tc.wantCode, before, sequence, balance)
				return
			}
			amm.ExpectTER(t, result, tc.wantCode)
		})
	}
}

func TestAMMWithdrawSupplyGuardLPTokenAndHoldingsPrecedence(t *testing.T) {
	t.Run("LPTokenAboveRecorded", func(t *testing.T) {
		fixture := newWithdrawSupplyFixture(t, true, "1414.213562373")
		sequence := fixture.env.Seq(fixture.env.GW)
		balance := fixture.env.Balance(fixture.env.GW)
		before := snapshotLedgerState(t, fixture.env.TestEnv, fixture.stateKeys)
		result := fixture.env.Submit(amm.AMMWithdraw(fixture.env.GW, fixture.asset1, fixture.asset2).
			LPTokenIn(fixture.holding).LPToken().Build())
		requireClaimedSupplyFailure(t, fixture.env.TestEnv, fixture.env.GW, result, "tecINTERNAL", before, sequence, balance)
	})

	t.Run("LPTokenAboveOwnHoldings", func(t *testing.T) {
		fixture := newWithdrawSupplyFixture(t, true, "1414.213562373")
		requested, err := state.NewIssuedAmountFromDecimalString("1414.213562373096", fixture.lptIssue.Currency, fixture.amm.Address)
		require.NoError(t, err)
		sequence := fixture.env.Seq(fixture.env.GW)
		balance := fixture.env.Balance(fixture.env.GW)
		before := snapshotLedgerState(t, fixture.env.TestEnv, fixture.stateKeys)
		result := fixture.env.Submit(amm.AMMWithdraw(fixture.env.GW, fixture.asset1, fixture.asset2).
			LPTokenIn(requested).LPToken().Build())
		requireClaimedSupplyFailure(t, fixture.env.TestEnv, fixture.env.GW, result, "tecAMM_INVALID_TOKENS", before, sequence, balance)
	})
}

func TestAMMWithdrawSupplyGuardSingleProviderReconciliation(t *testing.T) {
	fixture := newWithdrawSupplyFixture(t, false, "1414.213562373")
	result := fixture.env.Submit(amm.AMMWithdraw(fixture.env.GW, fixture.asset1, fixture.asset2).WithdrawAll().Build())
	amm.ExpectTER(t, result, "tesSUCCESS")
	require.False(t, fixture.env.LedgerEntryExists(fixture.ammKey))
}
