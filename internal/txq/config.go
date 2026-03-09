package txq

// Config holds configuration for the transaction queue.
// These values control queue behavior and fee escalation.
type Config struct {
	// LedgersInQueue is how many ledgers' worth of transactions the queue can hold.
	// For example, if the last ledger had 150 transactions, then up to
	// LedgersInQueue * 150 = 3000 transactions can be queued.
	LedgersInQueue uint32

	// QueueSizeMin is the minimum queue capacity regardless of ledger size.
	// Ensures the queue doesn't become too small during low-activity periods.
	QueueSizeMin uint32

	// RetrySequencePercent is the extra percentage required on the fee level
	// of a queued transaction to replace it with another having the same sequence.
	// For example, if a queued tx has fee level 512, a replacement needs at least
	// 512 * (1 + 0.25) = 640 to be considered.
	RetrySequencePercent uint32

	// MinimumEscalationMultiplier is the minimum value of the escalation multiplier,
	// regardless of the prior ledger's median fee level.
	MinimumEscalationMultiplier uint64

	// MinimumTxnInLedger is the minimum number of transactions to allow into
	// the ledger before fee escalation kicks in.
	MinimumTxnInLedger uint32

	// MinimumTxnInLedgerStandalone is like MinimumTxnInLedger but for standalone mode.
	// Primarily so that tests don't need to worry about queuing.
	MinimumTxnInLedgerStandalone uint32

	// TargetTxnInLedger is the number of transactions per ledger that fee
	// escalation "works towards".
	TargetTxnInLedger uint32

	// MaximumTxnInLedger is an optional maximum for transactions per ledger
	// before fee escalation kicks in. If zero, there's no explicit maximum.
	MaximumTxnInLedger uint32

	// NormalConsensusIncreasePercent is the percentage to increase expected
	// ledger size when ledgers close normally with more transactions than expected.
	NormalConsensusIncreasePercent uint32

	// SlowConsensusDecreasePercent is the percentage to decrease expected
	// ledger size when consensus is slow.
	SlowConsensusDecreasePercent uint32

	// MaximumTxnPerAccount is the maximum number of transactions that can be
	// queued for a single account.
	MaximumTxnPerAccount uint32

	// MinimumLastLedgerBuffer is the minimum difference between the current
	// ledger sequence and a transaction's LastLedgerSequence for the
	// transaction to be queueable.
	MinimumLastLedgerBuffer uint32

	// Standalone indicates if running in standalone mode (relaxes some validation).
	Standalone bool
}

// DefaultConfig returns the default TxQ configuration matching rippled defaults.
func DefaultConfig() Config {
	return Config{
		LedgersInQueue:                 20,
		QueueSizeMin:                   2000,
		RetrySequencePercent:           25,
		MinimumEscalationMultiplier:    BaseLevel * 500, // 128000
		MinimumTxnInLedger:             32,
		MinimumTxnInLedgerStandalone:   1000,
		TargetTxnInLedger:              256,
		MaximumTxnInLedger:             0, // No limit by default
		NormalConsensusIncreasePercent: 20,
		SlowConsensusDecreasePercent:   50,
		MaximumTxnPerAccount:           10,
		MinimumLastLedgerBuffer:        2,
		Standalone:                     false,
	}
}

// StandaloneConfig returns a configuration suitable for standalone/testing mode.
func StandaloneConfig() Config {
	cfg := DefaultConfig()
	cfg.Standalone = true
	return cfg
}
