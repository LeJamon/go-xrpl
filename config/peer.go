package config

import (
	"fmt"
	"math"
	"net"
)

// OverlayConfig represents the [overlay] section.
// Controls settings related to the peer to peer overlay.
// All keys are optional:
//   - public_ip feeds the Local-IP / Remote-IP handshake checks
//   - ip_limit 0 means auto-configure
//   - max_unknown_time / max_diverged_time 0 means use the built-in
//     defaults (rippled: 600s and 300s respectively)
//   - verify_endpoints (0|1) validates addresses received in TMEndpoints
//     gossip; nil (absent) means the built-in default (1 = verify on),
//     0 skips validation for local dev networks — a security risk on a
//     public network
//   - max_untrusted_count / max_trusted_count are optional manifest limits;
//     absence defaults independently to 300 and configured values must be in
//     the inclusive range 50..1000
type OverlayConfig struct {
	PublicIP             string               `toml:"public_ip" mapstructure:"public_ip"`
	IPLimit              int                  `toml:"ip_limit" mapstructure:"ip_limit"`
	MaxUnknownTime       int                  `toml:"max_unknown_time" mapstructure:"max_unknown_time"`
	MaxDivergedTime      int                  `toml:"max_diverged_time" mapstructure:"max_diverged_time"`
	VerifyEndpoints      *int                 `toml:"verify_endpoints" mapstructure:"verify_endpoints"`
	MaxUntrustedCount    *int                 `toml:"max_untrusted_count" mapstructure:"max_untrusted_count"`
	MaxTrustedCount      *int                 `toml:"max_trusted_count" mapstructure:"max_trusted_count"`
	InboundRetainedBytes int64                `toml:"inbound_retained_bytes" mapstructure:"inbound_retained_bytes"`
	ResourceLimits       ResourceLimitsConfig `toml:"resource_limits" mapstructure:"resource_limits"`
}

const (
	DefaultMaxUntrustedCount = 300
	DefaultMaxTrustedCount   = 300
	MinManifestCount         = 50
	MaxManifestCount         = 1000
)

// ResourceLimitsConfig bounds peer reputation and cluster-gossip state.
// Zero values retain the built-in limits.
type ResourceLimitsConfig struct {
	MaxEntries         int `toml:"max_entries" mapstructure:"max_entries"`
	MaxImportedEntries int `toml:"max_imported_entries" mapstructure:"max_imported_entries"`
	MaxImportOrigins   int `toml:"max_import_origins" mapstructure:"max_import_origins"`
	MaxImportItems     int `toml:"max_import_items" mapstructure:"max_import_items"`
}

// TransactionQueueConfig represents the [transaction_queue] section (EXPERIMENTAL).
// Tunes the performance of the transaction queue. Every key is optional and
// pointer-typed: a nil field means "absent — use the built-in default"
// (rippled's TxQ::Setup defaults), while a present key overrides the default
// with its exact value, including 0. This mirrors rippled's setup_TxQ, which
// overrides on key presence (BasicConfig::set), not on a non-zero value.
// An absent maximum leaves the limit unset; an explicit zero remains present
// and fails the minimum cross-check.
type TransactionQueueConfig struct {
	LedgersInQueue                 *int `toml:"ledgers_in_queue" mapstructure:"ledgers_in_queue"`
	MinimumQueueSize               *int `toml:"minimum_queue_size" mapstructure:"minimum_queue_size"`
	RetrySequencePercent           *int `toml:"retry_sequence_percent" mapstructure:"retry_sequence_percent"`
	MinimumEscalationMultiplier    *int `toml:"minimum_escalation_multiplier" mapstructure:"minimum_escalation_multiplier"`
	MinimumTxnInLedger             *int `toml:"minimum_txn_in_ledger" mapstructure:"minimum_txn_in_ledger"`
	MinimumTxnInLedgerStandalone   *int `toml:"minimum_txn_in_ledger_standalone" mapstructure:"minimum_txn_in_ledger_standalone"`
	TargetTxnInLedger              *int `toml:"target_txn_in_ledger" mapstructure:"target_txn_in_ledger"`
	MaximumTxnInLedger             *int `toml:"maximum_txn_in_ledger" mapstructure:"maximum_txn_in_ledger"`
	NormalConsensusIncreasePercent *int `toml:"normal_consensus_increase_percent" mapstructure:"normal_consensus_increase_percent"`
	SlowConsensusDecreasePercent   *int `toml:"slow_consensus_decrease_percent" mapstructure:"slow_consensus_decrease_percent"`
	MaximumTxnPerAccount           *int `toml:"maximum_txn_per_account" mapstructure:"maximum_txn_per_account"`
	MinimumLastLedgerBuffer        *int `toml:"minimum_last_ledger_buffer" mapstructure:"minimum_last_ledger_buffer"`
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

	if o.VerifyEndpoints != nil {
		if err := validateZeroOrOne("verify_endpoints", *o.VerifyEndpoints); err != nil {
			return err
		}
	}
	if o.InboundRetainedBytes != 0 && o.InboundRetainedBytes < 3*64*1024*1024 {
		return fmt.Errorf("inbound_retained_bytes must be at least %d, got %d", 3*64*1024*1024, o.InboundRetainedBytes)
	}
	limits := o.ResourceLimits
	for _, limit := range []struct {
		name  string
		value int
	}{
		{"resource_limits.max_entries", limits.MaxEntries},
		{"resource_limits.max_imported_entries", limits.MaxImportedEntries},
		{"resource_limits.max_import_origins", limits.MaxImportOrigins},
		{"resource_limits.max_import_items", limits.MaxImportItems},
	} {
		if err := validateNonNegative(limit.name, limit.value); err != nil {
			return err
		}
	}
	effectiveEntries := limits.MaxEntries
	if effectiveEntries == 0 {
		effectiveEntries = 32768
	}
	if limits.MaxImportedEntries != 0 && limits.MaxImportedEntries > effectiveEntries {
		return fmt.Errorf("resource_limits.max_imported_entries (%d) exceeds max_entries (%d)", limits.MaxImportedEntries, effectiveEntries)
	}
	if limits.MaxImportItems > 1024 {
		return fmt.Errorf("resource_limits.max_import_items must not exceed 1024, got %d", limits.MaxImportItems)
	}
	for _, count := range []struct {
		name  string
		value *int
	}{
		{name: "max_untrusted_count", value: o.MaxUntrustedCount},
		{name: "max_trusted_count", value: o.MaxTrustedCount},
	} {
		if count.value == nil {
			continue
		}
		if *count.value < MinManifestCount || *count.value > MaxManifestCount {
			return fmt.Errorf("%s must be between %d and %d, got %d",
				count.name, MinManifestCount, MaxManifestCount, *count.value)
		}
	}

	return nil
}

func (o *OverlayConfig) EffectiveMaxUntrustedCount() int {
	if o == nil || o.MaxUntrustedCount == nil {
		return DefaultMaxUntrustedCount
	}
	return *o.MaxUntrustedCount
}

func (o *OverlayConfig) EffectiveMaxTrustedCount() int {
	if o == nil || o.MaxTrustedCount == nil {
		return DefaultMaxTrustedCount
	}
	return *o.MaxTrustedCount
}

// Validate performs validation on the transaction queue configuration.
// The maximum_txn_in_ledger cross-checks mirror rippled's setup_TxQ
// (TxQ.cpp:1930-1951) for explicitly-set values; the same invariant is
// re-checked against the effective (defaulted) minimums when the queue
// is constructed.
func (tq *TransactionQueueConfig) Validate() error {
	for _, knob := range []struct {
		name  string
		value *int
	}{
		{"ledgers_in_queue", tq.LedgersInQueue},
		{"minimum_queue_size", tq.MinimumQueueSize},
		{"retry_sequence_percent", tq.RetrySequencePercent},
		{"minimum_txn_in_ledger", tq.MinimumTxnInLedger},
		{"minimum_txn_in_ledger_standalone", tq.MinimumTxnInLedgerStandalone},
		{"target_txn_in_ledger", tq.TargetTxnInLedger},
		{"maximum_txn_in_ledger", tq.MaximumTxnInLedger},
		{"normal_consensus_increase_percent", tq.NormalConsensusIncreasePercent},
		{"slow_consensus_decrease_percent", tq.SlowConsensusDecreasePercent},
		{"maximum_txn_per_account", tq.MaximumTxnPerAccount},
		{"minimum_last_ledger_buffer", tq.MinimumLastLedgerBuffer},
	} {
		if knob.value == nil {
			continue
		}
		if err := validateNonNegative(knob.name, *knob.value); err != nil {
			return err
		}
		if uint64(*knob.value) > math.MaxUint32 {
			return fmt.Errorf("%s exceeds uint32 range: %d", knob.name, *knob.value)
		}
	}
	if tq.MinimumEscalationMultiplier != nil {
		if err := validateNonNegative("minimum_escalation_multiplier", *tq.MinimumEscalationMultiplier); err != nil {
			return err
		}
	}
	if tq.LedgersInQueue != nil {
		if *tq.LedgersInQueue == 0 {
			return fmt.Errorf("ledgers_in_queue must be positive")
		}
		if *tq.LedgersInQueue > 1<<20 {
			return fmt.Errorf("ledgers_in_queue exceeds allocation cap: %d", *tq.LedgersInQueue)
		}
	}

	// Cross-check only explicitly-set values; the same invariant is
	// re-checked against the effective (defaulted) minimums when the queue
	// is constructed.
	if tq.MaximumTxnInLedger != nil {
		max := *tq.MaximumTxnInLedger
		if tq.MinimumTxnInLedger != nil && *tq.MinimumTxnInLedger > max {
			return fmt.Errorf("minimum_txn_in_ledger (%d) exceeds maximum_txn_in_ledger (%d)", *tq.MinimumTxnInLedger, max)
		}
		if tq.MinimumTxnInLedgerStandalone != nil && *tq.MinimumTxnInLedgerStandalone > max {
			return fmt.Errorf("minimum_txn_in_ledger_standalone (%d) exceeds maximum_txn_in_ledger (%d)", *tq.MinimumTxnInLedgerStandalone, max)
		}
	}

	return nil
}
