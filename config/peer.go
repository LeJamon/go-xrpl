package config

import (
	"fmt"
	"net"
)

// OverlayConfig represents the [overlay] section.
// Controls settings related to the peer to peer overlay.
// All keys are optional:
//   - public_ip feeds the Local-IP / Remote-IP handshake checks
//   - ip_limit 0 means auto-configure
//   - max_unknown_time / max_diverged_time 0 means use the built-in
//     defaults (rippled: 600s and 300s respectively)
type OverlayConfig struct {
	PublicIP        string `toml:"public_ip" mapstructure:"public_ip"`
	IPLimit         int    `toml:"ip_limit" mapstructure:"ip_limit"`
	MaxUnknownTime  int    `toml:"max_unknown_time" mapstructure:"max_unknown_time"`
	MaxDivergedTime int    `toml:"max_diverged_time" mapstructure:"max_diverged_time"`
}

// TransactionQueueConfig represents the [transaction_queue] section (EXPERIMENTAL).
// Tunes the performance of the transaction queue. All keys are optional;
// 0 means "use the built-in default" (rippled's TxQ::Setup defaults),
// except maximum_txn_in_ledger where 0 also IS the default (no maximum).
type TransactionQueueConfig struct {
	LedgersInQueue                 int `toml:"ledgers_in_queue" mapstructure:"ledgers_in_queue"`
	MinimumQueueSize               int `toml:"minimum_queue_size" mapstructure:"minimum_queue_size"`
	RetrySequencePercent           int `toml:"retry_sequence_percent" mapstructure:"retry_sequence_percent"`
	MinimumEscalationMultiplier    int `toml:"minimum_escalation_multiplier" mapstructure:"minimum_escalation_multiplier"`
	MinimumTxnInLedger             int `toml:"minimum_txn_in_ledger" mapstructure:"minimum_txn_in_ledger"`
	MinimumTxnInLedgerStandalone   int `toml:"minimum_txn_in_ledger_standalone" mapstructure:"minimum_txn_in_ledger_standalone"`
	TargetTxnInLedger              int `toml:"target_txn_in_ledger" mapstructure:"target_txn_in_ledger"`
	MaximumTxnInLedger             int `toml:"maximum_txn_in_ledger" mapstructure:"maximum_txn_in_ledger"`
	NormalConsensusIncreasePercent int `toml:"normal_consensus_increase_percent" mapstructure:"normal_consensus_increase_percent"`
	SlowConsensusDecreasePercent   int `toml:"slow_consensus_decrease_percent" mapstructure:"slow_consensus_decrease_percent"`
	MaximumTxnPerAccount           int `toml:"maximum_txn_per_account" mapstructure:"maximum_txn_per_account"`
	MinimumLastLedgerBuffer        int `toml:"minimum_last_ledger_buffer" mapstructure:"minimum_last_ledger_buffer"`
}

// Validate performs validation on the overlay configuration
func (o *OverlayConfig) Validate() error {
	if o.PublicIP != "" && net.ParseIP(o.PublicIP) == nil {
		return fmt.Errorf("public_ip must be a valid IP address, got %q", o.PublicIP)
	}

	if err := validateNonNegative("ip_limit", o.IPLimit); err != nil {
		return err
	}

	if o.MaxUnknownTime != 0 && (o.MaxUnknownTime < 300 || o.MaxUnknownTime > 1800) {
		return fmt.Errorf("max_unknown_time must be between 300 and 1800 seconds, got %d", o.MaxUnknownTime)
	}

	if o.MaxDivergedTime != 0 && (o.MaxDivergedTime < 60 || o.MaxDivergedTime > 900) {
		return fmt.Errorf("max_diverged_time must be between 60 and 900 seconds, got %d", o.MaxDivergedTime)
	}

	return nil
}

// Validate performs validation on the transaction queue configuration.
// The maximum_txn_in_ledger cross-checks mirror rippled's setup_TxQ
// (TxQ.cpp:1930-1951) for explicitly-set values; the same invariant is
// re-checked against the effective (defaulted) minimums when the queue
// is constructed.
func (tq *TransactionQueueConfig) Validate() error {
	for _, knob := range []struct {
		name  string
		value int
	}{
		{"ledgers_in_queue", tq.LedgersInQueue},
		{"minimum_queue_size", tq.MinimumQueueSize},
		{"retry_sequence_percent", tq.RetrySequencePercent},
		{"minimum_escalation_multiplier", tq.MinimumEscalationMultiplier},
		{"minimum_txn_in_ledger", tq.MinimumTxnInLedger},
		{"minimum_txn_in_ledger_standalone", tq.MinimumTxnInLedgerStandalone},
		{"target_txn_in_ledger", tq.TargetTxnInLedger},
		{"maximum_txn_in_ledger", tq.MaximumTxnInLedger},
		{"normal_consensus_increase_percent", tq.NormalConsensusIncreasePercent},
		{"slow_consensus_decrease_percent", tq.SlowConsensusDecreasePercent},
		{"maximum_txn_per_account", tq.MaximumTxnPerAccount},
		{"minimum_last_ledger_buffer", tq.MinimumLastLedgerBuffer},
	} {
		if err := validateNonNegative(knob.name, knob.value); err != nil {
			return err
		}
	}

	if max := tq.MaximumTxnInLedger; max > 0 {
		if min := tq.MinimumTxnInLedger; min > max {
			return fmt.Errorf("minimum_txn_in_ledger (%d) exceeds maximum_txn_in_ledger (%d)", min, max)
		}
		if minSA := tq.MinimumTxnInLedgerStandalone; minSA > max {
			return fmt.Errorf("minimum_txn_in_ledger_standalone (%d) exceeds maximum_txn_in_ledger (%d)", minSA, max)
		}
	}

	return nil
}
