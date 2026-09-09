package service

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/pseudo"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

type observingAutofillRulesTx struct {
	rules *amendment.Rules
}

func (t *observingAutofillRulesTx) TxType() tx.Type                  { return tx.TypeAccountSet }
func (t *observingAutofillRulesTx) GetCommon() *tx.Common            { return &tx.Common{} }
func (t *observingAutofillRulesTx) Validate() error                  { return nil }
func (t *observingAutofillRulesTx) Flatten() (map[string]any, error) { return nil, nil }
func (t *observingAutofillRulesTx) GetRawBytes() []byte              { return nil }
func (t *observingAutofillRulesTx) SetRawBytes([]byte)               {}
func (t *observingAutofillRulesTx) RequiredAmendments() [][32]byte   { return nil }
func (t *observingAutofillRulesTx) CalculateBaseFee(_ tx.LedgerView, cfg tx.EngineConfig) uint64 {
	t.rules = cfg.Rules
	return cfg.BaseFee
}

type amendmentRequiredSimulationTx struct {
	*tx.BaseTx
}

func (t *amendmentRequiredSimulationTx) RequiredAmendments() [][32]byte {
	return [][32]byte{amendment.FeatureBatchV1_1}
}

func TestOpenLedgerAcceptanceUsesLastValidatedRules(t *testing.T) {
	svc, err := New(DefaultConfig())
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	validated := svc.GetValidatedLedger()
	parent := svc.GetClosedLedger()
	require.NotNil(t, validated)
	require.NotNil(t, parent)
	require.False(t, validated.Rules().Enabled(amendment.FeatureBatchV1_1))

	localClosed, err := ledger.NewOpen(parent, parent.CloseTime().Add(time.Second))
	require.NoError(t, err)
	amendments, err := pseudo.SerializeAmendmentsSLE(&pseudo.AmendmentsSLE{
		Amendments: [][32]byte{amendment.FeatureBatchV1_1},
	})
	require.NoError(t, err)
	require.NoError(t, localClosed.Insert(keylet.Amendments(), amendments))
	require.NoError(t, localClosed.Close(parent.CloseTime().Add(time.Second), 0))
	require.True(t, localClosed.Rules().Enabled(amendment.FeatureBatchV1_1))

	svc.mu.RLock()
	applyCfg, err := svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)
	require.False(t, applyCfg.Rules.Enabled(amendment.FeatureBatchV1_1),
		"ingress must use the last validated rules, not a locally built closed ledger")
	require.False(t, svc.TransactionRules().Enabled(amendment.FeatureBatchV1_1),
		"TransactionRules must match the open-ledger admission rules")

	svc.openLedgerMu.Lock()
	svc.mu.Lock()
	accept := svc.openLedgerAcceptanceLocked(nil, nil)
	svc.mu.Unlock()
	err = accept(localClosed, nil, false, nil)
	svc.openLedgerMu.Unlock()
	require.NoError(t, err)

	current := svc.openLedgerView.Current()
	require.NotNil(t, current)
	require.False(t, current.Rules().Enabled(amendment.FeatureBatchV1_1),
		"the published open view must carry the same rules as its ApplyConfig")
	svc.mu.RLock()
	publishedCfg, err := svc.applyConfigLocked()
	svc.mu.RUnlock()
	require.NoError(t, err)
	require.Same(t, current.Rules(), publishedCfg.Rules,
		"ingress must reuse the published open view rule snapshot")
	require.Same(t, current.Rules(), svc.TransactionRules(),
		"TransactionRules must expose the published open view rule snapshot")

	// Validation can advance independently of open-view publication. Keep the
	// published rules snapshot authoritative until another accept publishes a
	// replacement view, including for autofill fee calculation.
	require.NoError(t, localClosed.SetValidated())
	legacyOpen, err := ledger.NewOpen(localClosed, time.Now())
	require.NoError(t, err)
	svc.mu.Lock()
	previousClosed, previousOpen, previousValidated := svc.closedLedger, svc.openLedger, svc.validatedLedger
	svc.closedLedger, svc.openLedger, svc.validatedLedger = localClosed, legacyOpen, localClosed
	svc.mu.Unlock()
	defer func() {
		svc.mu.Lock()
		svc.closedLedger, svc.openLedger, svc.validatedLedger = previousClosed, previousOpen, previousValidated
		svc.mu.Unlock()
	}()
	require.False(t, svc.TransactionRules().Enabled(amendment.FeatureBatchV1_1),
		"validation advancement must not alter an already-published open view")
	observed := &observingAutofillRulesTx{}
	_, err = svc.GetAutofillFee(observed, false, 10, 1)
	require.NoError(t, err)
	require.Same(t, current.Rules(), observed.rules,
		"autofill must use the published open view rules")

	simulationTx := &amendmentRequiredSimulationTx{
		BaseTx: tx.NewBaseTx(tx.TypeAccountSet, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"),
	}
	simulationTx.Fee = "10"
	simulationTx.SetSequence(1)
	simulated, err := svc.SimulateTransaction(simulationTx)
	require.NoError(t, err)
	require.Equal(t, ter.TemDISABLED, simulated.Result,
		"simulation must gate the transaction with the published rules, even when the closed ledger has advanced")
}
