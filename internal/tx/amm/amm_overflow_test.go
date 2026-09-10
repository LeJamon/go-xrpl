package amm

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

type unrelatedAMMPanic struct{}

func (unrelatedAMMPanic) Error() string { return "index out of range" }
func (unrelatedAMMPanic) RuntimeError() {}

type panicAMMLogger struct{ xrpllog.Logger }

func (panicAMMLogger) Trace(string, ...any) { panic(unrelatedAMMPanic{}) }

func TestAMMDepositIntegralOverflowCleanupGate(t *testing.T) {
	for _, test := range []struct {
		name       string
		cleanup    bool
		wantResult ter.Result
		wantPanic  bool
	}{
		{name: "before cleanup", wantPanic: true},
		{name: "after cleanup", cleanup: true, wantResult: ter.TecAMM_FAILED},
	} {
		t.Run(test.name, func(t *testing.T) {
			view, account, deposit := overflowDepositFixture(t, test.cleanup)
			ctx := ammMPTContext(view, account, accountIDForFixture(account))

			var recovered any
			var result ter.Result
			func() {
				defer func() { recovered = recover() }()
				result = deposit.Apply(ctx)
			}()

			if test.wantPanic {
				require.NotNil(t, recovered)
				return
			}
			require.Nil(t, recovered)
			require.Equal(t, test.wantResult, result)
		})
	}
}

func TestAMMOverflowCleanupPropagatesUnrelatedPanic(t *testing.T) {
	view, account, deposit := overflowDepositFixture(t, true)
	ctx := ammMPTContext(view, account, accountIDForFixture(account))
	ctx.Log = panicAMMLogger{Logger: xrpllog.Discard()}

	require.PanicsWithValue(t, unrelatedAMMPanic{}, func() {
		deposit.Apply(ctx)
	})
}

func accountIDForFixture(account *state.AccountRoot) [20]byte {
	id, _ := state.DecodeAccountID(account.Account)
	return id
}

func overflowDepositFixture(t *testing.T, cleanup bool) (*ammMPTView, *state.AccountRoot, *AMMDeposit) {
	t.Helper()
	view := newAMMMPTView()
	rules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported)
	if cleanup {
		rules.Enable(amendment.FeatureFixCleanup3_4_0)
	} else {
		rules.Disable(amendment.FeatureFixCleanup3_4_0)
	}
	view.rules = rules.Build()

	var issuerID, depositorID, ammID [20]byte
	copy(issuerID[:], []byte("overflow-mpt-issuer"))
	copy(depositorID[:], []byte("overflow-mpt-deposit"))
	copy(ammID[:], []byte("overflow-mpt-amm----"))
	issuerAddr := state.EncodeAccountIDSafe(issuerID)
	insertAMMMPTAccount(t, view, issuerID, 100_000_000, 0)
	depositor := insertAMMMPTAccount(t, view, depositorID, 100_000_000, 0)
	insertAMMMPTAccount(t, view, ammID, 10_000_000, 0)

	id := ammMPTID(11, issuerID)
	idHex := mptutil.EncodeID(id)
	flags := entry.LsfMPTCanTrade | entry.LsfMPTCanTransfer
	insertAMMMPTIssuance(t, view, id, flags, 0)
	issuance, issuanceKey, result := mptutil.ReadIssuance(view, id)
	require.Equal(t, ter.TesSUCCESS, result)
	maximum := protocol.MaxMPTokenAmount
	issuance.MaximumAmount = &maximum
	raw, err := state.SerializeMPTokenIssuance(issuance)
	require.NoError(t, err)
	require.NoError(t, view.Update(issuanceKey, raw))
	require.Equal(t, ter.TesSUCCESS, mptutil.EnsureHolding(view, id, depositorID, entry.LsfMPTAuthorized, false))
	require.Equal(t, ter.TesSUCCESS, mptutil.Credit(view, id, issuerID, depositorID, 10_000_000_000_000, false))
	require.Equal(t, ter.TesSUCCESS, mptutil.EnsureHolding(view, id, ammID, entry.LsfMPTAMM|entry.LsfMPTAuthorized, false))
	require.Equal(t, ter.TesSUCCESS, mptutil.Credit(view, id, issuerID, ammID, 1, false))

	mptAsset := tx.Asset{MPTIssuanceID: idHex}
	xrpAsset := tx.Asset{Currency: "XRP"}
	ammAddr := state.EncodeAccountIDSafe(ammID)
	amm := &AMMData{
		Account:        ammID,
		Asset:          mptAsset,
		Asset2:         xrpAsset,
		LPTokenBalance: state.NewIssuedAmountFromValue(1, 0, GenerateAMMLPTCurrencyForAssets(mptAsset, xrpAsset), ammAddr),
	}
	raw, err = serializeAMMData(amm)
	require.NoError(t, err)
	require.NoError(t, view.Insert(computeAMMKeylet(mptAsset, xrpAsset), raw))

	depositAmount := state.NewMPTAmountWithIssuanceID(10_000_000_000_000, issuerAddr, idHex)
	depositAmount2 := state.NewXRPAmountFromInt(1)
	deposit := NewAMMDeposit(depositor.Account, mptAsset, xrpAsset)
	deposit.Amount = &depositAmount
	deposit.Amount2 = &depositAmount2
	deposit.SetFlags(tfTwoAsset)
	return view, depositor, deposit
}
