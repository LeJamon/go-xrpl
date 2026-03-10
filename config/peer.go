package config

import "fmt"

// OverlayConfig represents the [overlay] section
// Controls settings related to the peer to peer overlay
type OverlayConfig struct {
	PublicIP         string `toml:"public_ip" mapstructure:"public_ip"`
	IPLimit          int    `toml:"ip_limit" mapstructure:"ip_limit"`
	MaxUnknownTime   int    `toml:"max_unknown_time" mapstructure:"max_unknown_time"`
	MaxDivergedTime  int    `toml:"max_diverged_time" mapstructure:"max_diverged_time"`
}

// TransactionQueueConfig represents the [transaction_queue] section (EXPERIMENTAL)
// Tunes the performance of the transaction queue
type TransactionQueueConfig struct {
	LedgersInQueue                  int `toml:"ledgers_in_queue" mapstructure:"ledgers_in_queue"`
	MinimumQueueSize                int `toml:"minimum_queue_size" mapstructure:"minimum_queue_size"`
	RetrySequencePercent            int `toml:"retry_sequence_percent" mapstructure:"retry_sequence_percent"`
	MinimumEscalationMultiplier     int `toml:"minimum_escalation_multiplier" mapstructure:"minimum_escalation_multiplier"`
	MinimumTxnInLedger              int `toml:"minimum_txn_in_ledger" mapstructure:"minimum_txn_in_ledger"`
	MinimumTxnInLedgerStandalone    int `toml:"minimum_txn_in_ledger_standalone" mapstructure:"minimum_txn_in_ledger_standalone"`
	TargetTxnInLedger               int `toml:"target_txn_in_ledger" mapstructure:"target_txn_in_ledger"`
	MaximumTxnInLedger              int `toml:"maximum_txn_in_ledger" mapstructure:"maximum_txn_in_ledger"`
	NormalConsensusIncreasePercent  int `toml:"normal_consensus_increase_percent" mapstructure:"normal_consensus_increase_percent"`
	SlowConsensusDecreasePercent    int `toml:"slow_consensus_decrease_percent" mapstructure:"slow_consensus_decrease_percent"`
	MaximumTxnPerAccount            int `toml:"maximum_txn_per_account" mapstructure:"maximum_txn_per_account"`
	MinimumLastLedgerBuffer         int `toml:"minimum_last_ledger_buffer" mapstructure:"minimum_last_ledger_buffer"`
	ZeroBaseFeeTransactionFeeLevel  int `toml:"zero_basefee_transaction_feelevel" mapstructure:"zero_basefee_transaction_feelevel"`
}

// Validate performs validation on the overlay configuration
func (o *OverlayConfig) Validate() error {
	// Validate max_unknown_time
	if o.MaxUnknownTime != 0 && (o.MaxUnknownTime < 300 || o.MaxUnknownTime > 1800) {
		return fmt.Errorf("max_unknown_time must be between 300 and 1800 seconds, got %d", o.MaxUnknownTime)
	}

	// Validate max_diverged_time
	if o.MaxDivergedTime != 0 && (o.MaxDivergedTime < 60 || o.MaxDivergedTime > 900) {
		return fmt.Errorf("max_diverged_time must be between 60 and 900 seconds, got %d", o.MaxDivergedTime)
	}

	return nil
}

// Validate performs validation on the transaction queue configuration
func (tq *TransactionQueueConfig) Validate() error {
	if tq.LedgersInQueue < 0 {
		return fmt.Errorf("ledgers_in_queue must be non-negative, got %d", tq.LedgersInQueue)
	}
	if tq.MinimumQueueSize < 0 {
		return fmt.Errorf("minimum_queue_size must be non-negative, got %d", tq.MinimumQueueSize)
	}
	if tq.RetrySequencePercent < 0 {
		return fmt.Errorf("retry_sequence_percent must be non-negative, got %d", tq.RetrySequencePercent)
	}
	if tq.MinimumEscalationMultiplier < 0 {
		return fmt.Errorf("minimum_escalation_multiplier must be non-negative, got %d", tq.MinimumEscalationMultiplier)
	}
	if tq.MinimumTxnInLedger < 0 {
		return fmt.Errorf("minimum_txn_in_ledger must be non-negative, got %d", tq.MinimumTxnInLedger)
	}
	if tq.MinimumTxnInLedgerStandalone < 0 {
		return fmt.Errorf("minimum_txn_in_ledger_standalone must be non-negative, got %d", tq.MinimumTxnInLedgerStandalone)
	}
	if tq.TargetTxnInLedger < 0 {
		return fmt.Errorf("target_txn_in_ledger must be non-negative, got %d", tq.TargetTxnInLedger)
	}
	if tq.MaximumTxnInLedger < 0 {
		return fmt.Errorf("maximum_txn_in_ledger must be non-negative, got %d", tq.MaximumTxnInLedger)
	}
	if tq.NormalConsensusIncreasePercent < 0 {
		return fmt.Errorf("normal_consensus_increase_percent must be non-negative, got %d", tq.NormalConsensusIncreasePercent)
	}
	if tq.SlowConsensusDecreasePercent < 0 {
		return fmt.Errorf("slow_consensus_decrease_percent must be non-negative, got %d", tq.SlowConsensusDecreasePercent)
	}
	if tq.MaximumTxnPerAccount < 0 {
		return fmt.Errorf("maximum_txn_per_account must be non-negative, got %d", tq.MaximumTxnPerAccount)
	}
	if tq.MinimumLastLedgerBuffer < 0 {
		return fmt.Errorf("minimum_last_ledger_buffer must be non-negative, got %d", tq.MinimumLastLedgerBuffer)
	}
	if tq.ZeroBaseFeeTransactionFeeLevel < 0 {
		return fmt.Errorf("zero_basefee_transaction_feelevel must be non-negative, got %d", tq.ZeroBaseFeeTransactionFeeLevel)
	}

	return nil
}

// GetMaxUnknownTime returns the max unknown time
func (o *OverlayConfig) GetMaxUnknownTime() int {
	return o.MaxUnknownTime
}

// GetMaxDivergedTime returns the max diverged time
func (o *OverlayConfig) GetMaxDivergedTime() int {
	return o.MaxDivergedTime
}

// HasPublicIP returns true if a public IP is configured
func (o *OverlayConfig) HasPublicIP() bool {
	return o.PublicIP != ""
}

// GetIPLimit returns the IP limit with auto-configuration if not set
func (o *OverlayConfig) GetIPLimit() int {
	if o.IPLimit == 0 {
		// Return 0 to indicate auto-configuration should be used
		return 0
	}
	return o.IPLimit
}

// GetLedgersInQueue returns ledgers in queue
func (tq *TransactionQueueConfig) GetLedgersInQueue() int {
	return tq.LedgersInQueue
}

// GetMinimumQueueSize returns minimum queue size
func (tq *TransactionQueueConfig) GetMinimumQueueSize() int {
	return tq.MinimumQueueSize
}

// GetRetrySequencePercent returns retry sequence percent
func (tq *TransactionQueueConfig) GetRetrySequencePercent() int {
	return tq.RetrySequencePercent
}

// GetMinimumEscalationMultiplier returns minimum escalation multiplier
func (tq *TransactionQueueConfig) GetMinimumEscalationMultiplier() int {
	return tq.MinimumEscalationMultiplier
}

// GetMinimumTxnInLedger returns minimum transactions in ledger
func (tq *TransactionQueueConfig) GetMinimumTxnInLedger() int {
	return tq.MinimumTxnInLedger
}

// GetMinimumTxnInLedgerStandalone returns minimum transactions in ledger for standalone
func (tq *TransactionQueueConfig) GetMinimumTxnInLedgerStandalone() int {
	return tq.MinimumTxnInLedgerStandalone
}

// GetTargetTxnInLedger returns target transactions in ledger
func (tq *TransactionQueueConfig) GetTargetTxnInLedger() int {
	return tq.TargetTxnInLedger
}

// GetMaximumTxnInLedger returns maximum transactions in ledger (0 means no maximum)
func (tq *TransactionQueueConfig) GetMaximumTxnInLedger() int {
	return tq.MaximumTxnInLedger
}

// GetNormalConsensusIncreasePercent returns normal consensus increase percent
func (tq *TransactionQueueConfig) GetNormalConsensusIncreasePercent() int {
	return tq.NormalConsensusIncreasePercent
}

// GetSlowConsensusDecreasePercent returns slow consensus decrease percent
func (tq *TransactionQueueConfig) GetSlowConsensusDecreasePercent() int {
	return tq.SlowConsensusDecreasePercent
}

// GetMaximumTxnPerAccount returns maximum transactions per account
func (tq *TransactionQueueConfig) GetMaximumTxnPerAccount() int {
	return tq.MaximumTxnPerAccount
}

// GetMinimumLastLedgerBuffer returns minimum last ledger buffer
func (tq *TransactionQueueConfig) GetMinimumLastLedgerBuffer() int {
	return tq.MinimumLastLedgerBuffer
}

// GetZeroBaseFeeTransactionFeeLevel returns zero base fee transaction fee level
func (tq *TransactionQueueConfig) GetZeroBaseFeeTransactionFeeLevel() int {
	return tq.ZeroBaseFeeTransactionFeeLevel
}