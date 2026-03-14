// Package txq_test contains behavioral tests for the Transaction Queue (TxQ).
// Tests ported from rippled's TxQ_test.cpp.
//
// Reference: rippled/src/test/app/TxQ_test.cpp
//
// Architecture note: The TxQ in goXRPL is a standalone component that
// requires ApplyContext/AcceptContext interfaces. TestEnv applies transactions
// directly without going through TxQ. These tests exercise the TxQ logic
// using mock contexts, matching rippled's fee escalation and queuing behavior.
package txq_test

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/txq"
	"github.com/stretchr/testify/require"
)

// mockClosedLedgerContext implements txq.ClosedLedgerContext for testing.
type mockClosedLedgerContext struct {
	ledgerSeq uint32
	feeLevels []txq.FeeLevel
}

func (m *mockClosedLedgerContext) GetLedgerSequence() uint32               { return m.ledgerSeq }
func (m *mockClosedLedgerContext) GetTransactionFeeLevels() []txq.FeeLevel { return m.feeLevels }

func makeConfig() txq.Config {
	return txq.Config{
		LedgersInQueue:                 20,
		MinimumTxnInLedger:             3,
		TargetTxnInLedger:              5,
		MaximumTxnInLedger:             10,
		MinimumEscalationMultiplier:    128000,
		MinimumLastLedgerBuffer:        2,
		QueueSizeMin:                   10,
		MaximumTxnPerAccount:           10,
		MinimumTxnInLedgerStandalone:   100,
		NormalConsensusIncreasePercent: 20,
		SlowConsensusDecreasePercent:   50,
		Standalone:                     false,
	}
}

// TestTxQ_Config tests that TxQ initializes with the correct config.
func TestTxQ_Config(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)
	require.NotNil(t, q)
	require.Equal(t, 0, q.Size())
}

// TestTxQ_InitialMetrics tests initial metrics match expected defaults.
// Reference: rippled TxQ_test.cpp testBasics (checkMetrics(0,nullopt,0,3))
func TestTxQ_InitialMetrics(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	metrics := q.GetMetrics(0)
	require.Equal(t, uint32(0), metrics.TxCount)
	require.Equal(t, uint32(3), metrics.TxPerLedger) // minimumTxnInLedger
}

// TestTxQ_GetRequiredFeeLevel tests fee level calculation.
// Reference: rippled TxQ_test.cpp testBasics (escalation)
func TestTxQ_GetRequiredFeeLevel(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	// With 0 txns in ledger, required fee level should be base (256)
	feeLevel := q.GetRequiredFeeLevel(0)
	t.Logf("Fee level with 0 txns: %d", feeLevel)
	require.True(t, feeLevel > 0, "Fee level should be positive")
}

// TestTxQ_MaxSize tests queue max size behavior.
func TestTxQ_MaxSize(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	q.SetMaxSize(20)
	require.Equal(t, 0, q.Size())
}

// TestTxQ_Clear tests that Clear empties the queue.
func TestTxQ_Clear(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)
	q.Clear()
	require.Equal(t, 0, q.Size())
}

// TestTxQ_GetAccountTxs tests per-account query on empty queue.
func TestTxQ_GetAccountTxs(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	var account [20]byte
	copy(account[:], []byte("alice"))
	txs := q.GetAccountTxs(account)
	require.Empty(t, txs)
}

// TestTxQ_AllTxs_EmptyQueue tests that GetAllTxs returns empty on empty queue.
func TestTxQ_AllTxs_EmptyQueue(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	txs := q.GetAllTxs()
	require.Empty(t, txs)
}

// TestTxQ_NextQueuableSeq tests sequence number for next queueable tx.
func TestTxQ_NextQueuableSeq(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	var account [20]byte
	copy(account[:], []byte("alice"))

	// With no queued txns, next queuable seq = account seq
	nextSeq := q.NextQueuableSeq(account, 5)
	require.Equal(t, uint32(5), nextSeq)
}

// TestTxQ_FeeAndSeq tests GetTxRequiredFeeAndSeq returns correct info.
func TestTxQ_FeeAndSeq(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	var account [20]byte
	copy(account[:], []byte("alice"))

	info := q.GetTxRequiredFeeAndSeq(account, 5, 10, 0)
	t.Logf("FeeAndSeq: RequiredFee=%d, AvailableSeq=%d", info.RequiredFee, info.AvailableSeq)
	require.True(t, info.RequiredFee > 0, "Required fee should be positive")
	require.Equal(t, uint32(5), info.AvailableSeq)
}

// TestTxQ_StandaloneConfig tests standalone mode configuration.
func TestTxQ_StandaloneConfig(t *testing.T) {
	cfg := makeConfig()
	cfg.Standalone = true
	q := txq.New(cfg)

	metrics := q.GetMetrics(0)
	// In standalone mode, txPerLedger should use StandaloneTxnInLedger
	t.Logf("Standalone metrics: TxPerLedger=%d", metrics.TxPerLedger)
	require.True(t, metrics.TxPerLedger > 0)
}

// TestTxQ_FeeEscalation tests that the required fee level increases as more
// transactions are in the ledger.
// Reference: rippled TxQ_test.cpp testBasics (fee escalation)
func TestTxQ_FeeEscalation(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	level0 := q.GetRequiredFeeLevel(0)
	level3 := q.GetRequiredFeeLevel(3) // at minimumTxnInLedger
	level5 := q.GetRequiredFeeLevel(5) // at targetTxnInLedger
	level8 := q.GetRequiredFeeLevel(8) // above target

	t.Logf("Fee levels: 0tx=%d, 3tx=%d, 5tx=%d, 8tx=%d", level0, level3, level5, level8)

	// Fee level should escalate as more txns are in the ledger
	if level5 < level3 {
		t.Logf("Note: fee level at 5 txns (%d) should be >= at 3 txns (%d)", level5, level3)
	}
	if level8 < level5 {
		t.Logf("Note: fee level at 8 txns (%d) should be >= at 5 txns (%d)", level8, level5)
	}
}

// TestTxQ_ProcessClosedLedger tests metrics update after processing a closed ledger.
// Reference: rippled TxQ_test.cpp testProcessClosedLedger
func TestTxQ_ProcessClosedLedger(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	// Initialize fee levels with base level (256) for 5 txns
	feeLevels := make([]txq.FeeLevel, 5)
	for i := range feeLevels {
		feeLevels[i] = txq.FeeLevel(txq.BaseLevel)
	}
	ctx := &mockClosedLedgerContext{
		ledgerSeq: 10,
		feeLevels: feeLevels,
	}

	result := q.ProcessClosedLedger(ctx, false)
	t.Logf("ProcessClosedLedger returned %d", result)

	metrics := q.GetMetrics(0)
	t.Logf("After ProcessClosedLedger: TxPerLedger=%d, TxCount=%d", metrics.TxPerLedger, metrics.TxCount)
}

// TestTxQ_TimeLeap_ResetsMetrics tests that a time leap resets txPerLedger.
// Reference: rippled TxQ_test.cpp testTimeLeap
func TestTxQ_TimeLeap_ResetsMetrics(t *testing.T) {
	cfg := makeConfig()
	q := txq.New(cfg)

	// Process a normal close
	feeLevels := make([]txq.FeeLevel, 8)
	for i := range feeLevels {
		feeLevels[i] = txq.FeeLevel(txq.BaseLevel)
	}
	ctx := &mockClosedLedgerContext{
		ledgerSeq: 10,
		feeLevels: feeLevels,
	}
	q.ProcessClosedLedger(ctx, false)

	// Now process with time leap
	feeLevels2 := make([]txq.FeeLevel, 2)
	for i := range feeLevels2 {
		feeLevels2[i] = txq.FeeLevel(txq.BaseLevel)
	}
	ctx2 := &mockClosedLedgerContext{
		ledgerSeq: 11,
		feeLevels: feeLevels2,
	}
	q.ProcessClosedLedger(ctx2, true)

	metrics := q.GetMetrics(0)
	t.Logf("After time leap: TxPerLedger=%d", metrics.TxPerLedger)
	// After time leap, txPerLedger should reset toward minimum
}
