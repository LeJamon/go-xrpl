package service

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// TxQConfigFromTuning maps the operator's [transaction_queue] stanza onto
// the built-in txq defaults, mirroring rippled's setup_TxQ
// (TxQ.cpp:1915-1980): only explicitly-set (non-zero) keys override the
// defaults, the consensus percentages are clamped to rippled's ranges,
// and an explicit maximum_txn_in_ledger below the effective minimums is
// a hard error.
//
// maximum_txn_in_ledger keeps its "0 = no maximum" meaning, which is
// also the default.
func TxQConfigFromTuning(t config.TransactionQueueConfig, standalone bool) (txq.Config, error) {
	cfg := txq.DefaultConfig()
	if standalone {
		cfg = txq.StandaloneConfig()
	}

	if t.LedgersInQueue > 0 {
		cfg.LedgersInQueue = uint32(t.LedgersInQueue)
	}
	if t.MinimumQueueSize > 0 {
		cfg.QueueSizeMin = uint32(t.MinimumQueueSize)
	}
	if t.RetrySequencePercent > 0 {
		cfg.RetrySequencePercent = uint32(t.RetrySequencePercent)
	}
	if t.MinimumEscalationMultiplier > 0 {
		cfg.MinimumEscalationMultiplier = uint64(t.MinimumEscalationMultiplier)
	}
	if t.MinimumTxnInLedger > 0 {
		cfg.MinimumTxnInLedger = uint32(t.MinimumTxnInLedger)
	}
	if t.MinimumTxnInLedgerStandalone > 0 {
		cfg.MinimumTxnInLedgerStandalone = uint32(t.MinimumTxnInLedgerStandalone)
	}
	if t.TargetTxnInLedger > 0 {
		cfg.TargetTxnInLedger = uint32(t.TargetTxnInLedger)
	}
	if t.MaximumTxnInLedger > 0 {
		cfg.MaximumTxnInLedger = uint32(t.MaximumTxnInLedger)
	}
	if t.NormalConsensusIncreasePercent > 0 {
		cfg.NormalConsensusIncreasePercent = clampUint32(t.NormalConsensusIncreasePercent, 0, 1000)
	}
	if t.SlowConsensusDecreasePercent > 0 {
		cfg.SlowConsensusDecreasePercent = clampUint32(t.SlowConsensusDecreasePercent, 0, 100)
	}
	if t.MaximumTxnPerAccount > 0 {
		cfg.MaximumTxnPerAccount = uint32(t.MaximumTxnPerAccount)
	}
	if t.MinimumLastLedgerBuffer > 0 {
		cfg.MinimumLastLedgerBuffer = uint32(t.MinimumLastLedgerBuffer)
	}

	// Mirror rippled's hard errors when an explicit maximum is below the
	// effective minimums (TxQ.cpp:1930-1951).
	if cfg.MaximumTxnInLedger > 0 {
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

func clampUint32(v, lo, hi int) uint32 {
	if v < lo {
		v = lo
	}
	if v > hi {
		v = hi
	}
	return uint32(v)
}
