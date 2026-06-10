package config

import (
	"fmt"
	"slices"
	"strings"
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
