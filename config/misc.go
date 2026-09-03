package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

const (
	defaultSweepInterval    = 60 * time.Second
	minSweepIntervalSeconds = 10
	maxSweepIntervalSeconds = 600
)

// validateZeroOrOne validates an int knob that rippled treats as a boolean.
func validateZeroOrOne(name string, v int) error {
	if v != 0 && v != 1 {
		return fmt.Errorf("%s must be 0 or 1, got %d", name, v)
	}
	return nil
}

// validateNonNegative validates an int knob that must be >= 0.
func validateNonNegative(name string, v int) error {
	if v < 0 {
		return fmt.Errorf("%s must be non-negative, got %d", name, v)
	}
	return nil
}

// ValidateNodeSize validates the node_size setting.
// Empty means unset — the built-in default ("medium") applies.
func ValidateNodeSize(nodeSize string) error {
	if nodeSize == "" {
		return nil
	}

	validSizes := []string{"tiny", "small", "medium", "large", "huge"}
	if slices.Contains(validSizes, nodeSize) {
		return nil
	}

	return fmt.Errorf("invalid node_size: %s (valid options: tiny, small, medium, large, huge)", nodeSize)
}

// SweepIntervalForNodeSize returns rippled's cache sweep cadence for a node-size profile.
func SweepIntervalForNodeSize(nodeSize string) time.Duration {
	switch nodeSize {
	case "tiny":
		return 10 * time.Second
	case "small":
		return 30 * time.Second
	case "large":
		return 90 * time.Second
	case "huge":
		return 120 * time.Second
	case "", "medium":
		return defaultSweepInterval
	default:
		return defaultSweepInterval
	}
}

// ResolvedSweepInterval returns the explicit sweep interval or the node-size default.
func (c *Config) ResolvedSweepInterval() time.Duration {
	if c == nil {
		return defaultSweepInterval
	}
	if c.SweepInterval != nil {
		return time.Duration(*c.SweepInterval) * time.Second
	}
	return SweepIntervalForNodeSize(c.NodeSize)
}

// ValidateSweepInterval accepts an unset interval or rippled's supported range in seconds.
func ValidateSweepInterval(interval *int) error {
	if interval == nil {
		return nil
	}
	if *interval < minSweepIntervalSeconds || *interval > maxSweepIntervalSeconds {
		return fmt.Errorf("sweep_interval must be between %d and %d seconds, got %d",
			minSweepIntervalSeconds, maxSweepIntervalSeconds, *interval)
	}
	return nil
}

// ValidateMaxTransactions validates the max_transactions setting.
// 0 means unset — the built-in default (250) applies.
func ValidateMaxTransactions(maxTxn int) error {
	if maxTxn == 0 {
		return nil
	}

	if maxTxn < 100 || maxTxn > 1000 {
		return fmt.Errorf("max_transactions must be between 100 and 1000, got %d", maxTxn)
	}

	return nil
}

// ValidatePeersMax validates the maximum peer count setting
func ValidatePeersMax(peersMax int) error {
	return validateNonNegative("peers_max", peersMax)
}

// ValidatePeerPrivate validates the peer private setting
func ValidatePeerPrivate(peerPrivate int) error {
	return validateZeroOrOne("peer_private", peerPrivate)
}

// ValidateLedgerReplay validates the ledger replay setting
func ValidateLedgerReplay(ledgerReplay int) error {
	return validateZeroOrOne("ledger_replay", ledgerReplay)
}

// ValidateBetaRPCAPI validates the beta RPC API setting
func ValidateBetaRPCAPI(betaAPI int) error {
	return validateZeroOrOne("beta_rpc_api", betaAPI)
}

// ValidatePathSearchMax validates the maximum path-search level.
func ValidatePathSearchMax(pathSearchMax *int) error {
	if pathSearchMax == nil {
		return nil
	}
	return validateNonNegative("path_search_max", *pathSearchMax)
}

// ValidateLedgerCacheSize accepts an unset size or a value within the supported cache bounds.
func ValidateLedgerCacheSize(size *int) error {
	if size == nil {
		return nil
	}
	if *size < MinLedgerCacheSize || *size > MaxLedgerCacheSize {
		return fmt.Errorf("ledger_cache_size must be between %d and %d, got %d",
			MinLedgerCacheSize, MaxLedgerCacheSize, *size)
	}
	return nil
}

// ValidateWebsocketPingFrequency validates the websocket ping frequency
func ValidateWebsocketPingFrequency(frequency int) error {
	return validateNonNegative("websocket_ping_frequency", frequency)
}

// validateRelayPolicy validates a relay_proposals / relay_validations
// value. Matching is case-insensitive, mirroring rippled's
// boost::iequals comparison (Config.cpp:607-633). Empty means unset —
// rippled's defaults apply (relay_proposals: trusted, relay_validations: all).
func validateRelayPolicy(key, value string) error {
	if value == "" {
		return nil
	}

	for _, valid := range []string{"all", "trusted", "drop_untrusted"} {
		if strings.EqualFold(value, valid) {
			return nil
		}
	}

	return fmt.Errorf("invalid %s: %s (valid options: all, trusted, drop_untrusted)", key, value)
}

// ValidateRelayProposals validates the relay proposals setting
func ValidateRelayProposals(relayProposals string) error {
	return validateRelayPolicy("relay_proposals", relayProposals)
}

// ValidateRelayValidations validates the relay validations setting
func ValidateRelayValidations(relayValidations string) error {
	return validateRelayPolicy("relay_validations", relayValidations)
}
