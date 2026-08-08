package service

import (
	"fmt"
	"math"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// TxQConfigFromTuning maps the operator's [transaction_queue] stanza onto
// the built-in txq defaults, mirroring rippled's setup_TxQ
// (TxQ.cpp:1915-1980): every key that is present overrides the default
// (including an explicit 0, matching rippled's BasicConfig::set), the
// consensus percentages are clamped to rippled's ranges, and an explicit
// maximum_txn_in_ledger below the effective minimums is a hard error.
//
// An absent maximum leaves the limit unset; an explicit zero remains present
// and fails the minimum cross-check.
func TxQConfigFromTuning(t config.TransactionQueueConfig, standalone bool) (txq.Config, error) {
	if err := t.Validate(); err != nil {
		return txq.Config{}, err
	}
	cfg := txq.DefaultConfig()
	if standalone {
		cfg = txq.StandaloneConfig()
	}

	var err error
	if cfg.LedgersInQueue, err = optionalUint32("ledgers_in_queue", t.LedgersInQueue, cfg.LedgersInQueue); err != nil {
		return txq.Config{}, err
	}
	if cfg.QueueSizeMin, err = optionalUint32("minimum_queue_size", t.MinimumQueueSize, cfg.QueueSizeMin); err != nil {
		return txq.Config{}, err
	}
	if cfg.RetrySequencePercent, err = optionalUint32("retry_sequence_percent", t.RetrySequencePercent, cfg.RetrySequencePercent); err != nil {
		return txq.Config{}, err
	}
	if t.MinimumEscalationMultiplier != nil {
		cfg.MinimumEscalationMultiplier = uint64(*t.MinimumEscalationMultiplier)
	}
	if cfg.MinimumTxnInLedger, err = optionalUint32("minimum_txn_in_ledger", t.MinimumTxnInLedger, cfg.MinimumTxnInLedger); err != nil {
		return txq.Config{}, err
	}
	if cfg.MinimumTxnInLedgerStandalone, err = optionalUint32("minimum_txn_in_ledger_standalone", t.MinimumTxnInLedgerStandalone, cfg.MinimumTxnInLedgerStandalone); err != nil {
		return txq.Config{}, err
	}
	if cfg.TargetTxnInLedger, err = optionalUint32("target_txn_in_ledger", t.TargetTxnInLedger, cfg.TargetTxnInLedger); err != nil {
		return txq.Config{}, err
	}
	if t.MaximumTxnInLedger != nil {
		if cfg.MaximumTxnInLedger, err = optionalUint32("maximum_txn_in_ledger", t.MaximumTxnInLedger, cfg.MaximumTxnInLedger); err != nil {
			return txq.Config{}, err
		}
		cfg.MaximumTxnInLedgerSet = true
	}
	if t.NormalConsensusIncreasePercent != nil {
		cfg.NormalConsensusIncreasePercent = clampUint32(*t.NormalConsensusIncreasePercent, 0, 1000)
	}
	if t.SlowConsensusDecreasePercent != nil {
		cfg.SlowConsensusDecreasePercent = clampUint32(*t.SlowConsensusDecreasePercent, 0, 100)
	}
	if cfg.MaximumTxnPerAccount, err = optionalUint32("maximum_txn_per_account", t.MaximumTxnPerAccount, cfg.MaximumTxnPerAccount); err != nil {
		return txq.Config{}, err
	}
	if cfg.MinimumLastLedgerBuffer, err = optionalUint32("minimum_last_ledger_buffer", t.MinimumLastLedgerBuffer, cfg.MinimumLastLedgerBuffer); err != nil {
		return txq.Config{}, err
	}

	if cfg.MaximumTxnInLedgerSet {
		if cfg.MinimumTxnInLedger > cfg.MaximumTxnInLedger {
			return txq.Config{}, fmt.Errorf(
				"transaction_queue: minimum_txn_in_ledger (%d) exceeds maximum_txn_in_ledger (%d)",
				cfg.MinimumTxnInLedger, cfg.MaximumTxnInLedger)
		}
		if cfg.MinimumTxnInLedgerStandalone > cfg.MaximumTxnInLedger {
			return txq.Config{}, fmt.Errorf(
				"transaction_queue: minimum_txn_in_ledger_standalone (%d) exceeds maximum_txn_in_ledger (%d)",
				cfg.MinimumTxnInLedgerStandalone, cfg.MaximumTxnInLedger)
		}
	}
	return cfg, nil
}

func optionalUint32(name string, value *int, fallback uint32) (uint32, error) {
	if value == nil {
		return fallback, nil
	}
	if *value < 0 || uint64(*value) > math.MaxUint32 {
		return 0, fmt.Errorf("%s is outside uint32 range: %d", name, *value)
	}
	return uint32(*value), nil
}

func clampUint32(v, lo, hi int) uint32 {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return uint32(v)
}
