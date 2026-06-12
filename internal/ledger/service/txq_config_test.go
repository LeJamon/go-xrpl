package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func intPtr(v int) *int { return &v }

func TestTxQConfigFromTuning_DefaultsWhenUnset(t *testing.T) {
	got, err := TxQConfigFromTuning(config.TransactionQueueConfig{}, false)
	require.NoError(t, err)
	assert.Equal(t, txq.DefaultConfig(), got)

	got, err = TxQConfigFromTuning(config.TransactionQueueConfig{}, true)
	require.NoError(t, err)
	assert.Equal(t, txq.StandaloneConfig(), got)
}

// TestTxQConfigFromTuning_OverridesReachQueueConfig is the wiring test
// for the [transaction_queue] stanza: every configured key must land on
// the corresponding txq.Config field.
func TestTxQConfigFromTuning_OverridesReachQueueConfig(t *testing.T) {
	tuning := config.TransactionQueueConfig{
		LedgersInQueue:                 intPtr(30),
		MinimumQueueSize:               intPtr(3000),
		RetrySequencePercent:           intPtr(50),
		MinimumEscalationMultiplier:    intPtr(64000),
		MinimumTxnInLedger:             intPtr(16),
		MinimumTxnInLedgerStandalone:   intPtr(500),
		TargetTxnInLedger:              intPtr(128),
		MaximumTxnInLedger:             intPtr(4096),
		NormalConsensusIncreasePercent: intPtr(30),
		SlowConsensusDecreasePercent:   intPtr(60),
		MaximumTxnPerAccount:           intPtr(50),
		MinimumLastLedgerBuffer:        intPtr(4),
	}

	got, err := TxQConfigFromTuning(tuning, false)
	require.NoError(t, err)

	want := txq.Config{
		LedgersInQueue:                 30,
		QueueSizeMin:                   3000,
		RetrySequencePercent:           50,
		MinimumEscalationMultiplier:    64000,
		MinimumTxnInLedger:             16,
		MinimumTxnInLedgerStandalone:   500,
		TargetTxnInLedger:              128,
		MaximumTxnInLedger:             4096,
		NormalConsensusIncreasePercent: 30,
		SlowConsensusDecreasePercent:   60,
		MaximumTxnPerAccount:           50,
		MinimumLastLedgerBuffer:        4,
		Standalone:                     false,
	}
	assert.Equal(t, want, got)
}

// TestTxQConfigFromTuning_ClampsPercentages mirrors rippled's
// std::clamp calls in setup_TxQ (TxQ.cpp:1959-1974).
func TestTxQConfigFromTuning_ClampsPercentages(t *testing.T) {
	got, err := TxQConfigFromTuning(config.TransactionQueueConfig{
		NormalConsensusIncreasePercent: intPtr(5000),
		SlowConsensusDecreasePercent:   intPtr(500),
	}, false)
	require.NoError(t, err)
	assert.Equal(t, uint32(1000), got.NormalConsensusIncreasePercent)
	assert.Equal(t, uint32(100), got.SlowConsensusDecreasePercent)
}

// TestTxQConfigFromTuning_ExplicitZeroOverridesDefault verifies rippled's
// presence-based override (BasicConfig::set): an explicit 0 must replace
// the default, not be treated as "unset". TestTxQConfigFromTuning_DefaultsWhenUnset
// covers the complementary nil case, so the two together prove 0 ≠ absent.
func TestTxQConfigFromTuning_ExplicitZeroOverridesDefault(t *testing.T) {
	got, err := TxQConfigFromTuning(config.TransactionQueueConfig{
		MinimumLastLedgerBuffer:        intPtr(0),
		NormalConsensusIncreasePercent: intPtr(0),
		SlowConsensusDecreasePercent:   intPtr(0),
	}, false)
	require.NoError(t, err)
	assert.Equal(t, uint32(0), got.MinimumLastLedgerBuffer)
	assert.Equal(t, uint32(0), got.NormalConsensusIncreasePercent)
	assert.Equal(t, uint32(0), got.SlowConsensusDecreasePercent)
}

// TestTxQConfigFromTuning_MaximumBelowEffectiveMinimum mirrors rippled's
// hard error when maximum_txn_in_ledger is below the (defaulted)
// minimums (TxQ.cpp:1930-1951).
func TestTxQConfigFromTuning_MaximumBelowEffectiveMinimum(t *testing.T) {
	// Default MinimumTxnInLedger is 32; an explicit maximum of 10 must fail
	// even though minimum_txn_in_ledger is unset.
	_, err := TxQConfigFromTuning(config.TransactionQueueConfig{MaximumTxnInLedger: intPtr(10)}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minimum_txn_in_ledger")

	// Default MinimumTxnInLedgerStandalone is 1000.
	_, err = TxQConfigFromTuning(config.TransactionQueueConfig{
		MinimumTxnInLedger: intPtr(5),
		MaximumTxnInLedger: intPtr(100),
	}, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minimum_txn_in_ledger_standalone")
}

// TestServiceNew_UsesTxQOverride proves the configured values actually
// reach the running queue, not just the conversion helper.
func TestServiceNew_UsesTxQOverride(t *testing.T) {
	tuning := config.TransactionQueueConfig{MaximumTxnPerAccount: intPtr(77)}
	txqCfg, err := TxQConfigFromTuning(tuning, true)
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.TxQ = &txqCfg
	svc, err := New(cfg)
	require.NoError(t, err)

	assert.Equal(t, uint32(77), svc.txQueue.Config().MaximumTxnPerAccount)
}
