package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ValidatorsConfig represents the validators.toml structure
// This mirrors the structure of validators.txt but in TOML format
type ValidatorsConfig struct {
	Validators             []string `toml:"validators" mapstructure:"validators"`
	ValidatorListSites     []string `toml:"validator_list_sites" mapstructure:"validator_list_sites"`
	ValidatorListKeys      []string `toml:"validator_list_keys" mapstructure:"validator_list_keys"`
	ValidatorListThreshold int      `toml:"validator_list_threshold" mapstructure:"validator_list_threshold"`
}

// Validate performs validation on the validators configuration
func (v *ValidatorsConfig) Validate() error {
	for i, validator := range v.Validators {
		if err := validateValidatorKey(validator); err != nil {
			return fmt.Errorf("invalid validator at index %d: %w", i, err)
		}
	}

	for i, site := range v.ValidatorListSites {
		if err := validateValidatorListSite(site); err != nil {
			return fmt.Errorf("invalid validator_list_site at index %d: %w", i, err)
		}
	}

	for i, key := range v.ValidatorListKeys {
		if err := validateValidatorListKey(key); err != nil {
			return fmt.Errorf("invalid validator_list_key at index %d: %w", i, err)
		}
	}

	if v.ValidatorListThreshold < 0 {
		return fmt.Errorf("validator_list_threshold must be non-negative, got %d", v.ValidatorListThreshold)
	}

	// A positive threshold with zero publisher keys would trip rippled's
	// XRPL_ASSERT(listThreshold_ > 0 && listThreshold_ <= publisherLists_.size())
	// at ValidatorList.cpp:201-203 — catch it here so the operator gets
	// a clean message at startup instead of a runtime divergence.
	uniquePublisherCount := uniqueValidatorListKeyCount(v.ValidatorListKeys)
	if v.ValidatorListThreshold > 0 && uniquePublisherCount == 0 {
		return fmt.Errorf("validator_list_threshold (%d) requires at least one validator_list_keys entry",
			v.ValidatorListThreshold)
	}

	if uniquePublisherCount > 0 && v.ValidatorListThreshold > uniquePublisherCount {
		return fmt.Errorf("validator_list_threshold (%d) cannot be greater than number of unique validator_list_keys (%d)",
			v.ValidatorListThreshold, uniquePublisherCount)
	}

	return nil
}

// EffectiveListThreshold returns the effective threshold value
func (v *ValidatorsConfig) EffectiveListThreshold() int {
	publisherCount := uniqueValidatorListKeyCount(v.ValidatorListKeys)
	if v.ValidatorListThreshold == 0 && publisherCount > 0 {
		// Calculate threshold as per rippled logic
		if publisherCount < 3 {
			return 1
		}
		return (publisherCount / 2) + 1
	}
	return v.ValidatorListThreshold
}

func uniqueValidatorListKeyCount(keys []string) int {
	unique := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		unique[strings.ToUpper(key)] = struct{}{}
	}
	return len(unique)
}

// validateValidatorKey validates a single validator public key
func validateValidatorKey(key string) error {
	if key == "" {
		return errors.New("validator key cannot be empty")
	}

	// Basic validation - should start with 'n' and be the right length
	if !strings.HasPrefix(key, "n") {
		return fmt.Errorf("validator key must start with 'n', got: %s", key)
	}

	// Length check (rippled node public keys are 52 characters in base58)
	if len(key) != 52 {
		return fmt.Errorf("validator key has invalid length %d, expected 52", len(key))
	}

	// Character set validation (base58)
	if !isValidBase58(key) {
		return errors.New("validator key contains invalid characters")
	}

	return nil
}

// validateValidatorListSite validates a validator list site URL
func validateValidatorListSite(site string) error {
	if site == "" {
		return errors.New("validator list site cannot be empty")
	}

	// Basic URL validation
	if !strings.HasPrefix(site, "http://") &&
		!strings.HasPrefix(site, "https://") &&
		!strings.HasPrefix(site, "file://") {
		return errors.New("validator list site must use http://, https://, or file:// scheme")
	}

	return nil
}

// validateValidatorListKey validates a validator list publisher key.
// Publisher keys are 33-byte compressed public keys hex-encoded
// (66 chars) with a key-type prefix byte: 0xED for ed25519 — the
// common case in practice; vl.ripple.com / vl.xrplf.org both publish
// ed25519 — or 0x02/0x03 for secp256k1.
func validateValidatorListKey(key string) error {
	if key == "" {
		return errors.New("validator list key cannot be empty")
	}

	if len(key) != 66 {
		return fmt.Errorf("validator list key has invalid length %d, expected 66 (33-byte hex)", len(key))
	}

	if !isValidHex(key) {
		return errors.New("validator list key contains invalid hex characters")
	}

	// Sanity-check the key-type prefix byte.
	prefix := strings.ToUpper(key[:2])
	if prefix != "ED" && prefix != "02" && prefix != "03" {
		return fmt.Errorf("validator list key has unrecognized key-type prefix %q (want ED, 02, or 03)", prefix)
	}

	return nil
}

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// isValidBase58 checks if a string contains only valid base58 characters
func isValidBase58(s string) bool {
	for _, char := range s {
		if !strings.ContainsRune(base58Alphabet, char) {
			return false
		}
	}
	return true
}

// isValidHex checks if a string contains only valid hexadecimal characters
func isValidHex(s string) bool {
	for _, char := range s {
		if !((char >= '0' && char <= '9') ||
			(char >= 'a' && char <= 'f') ||
			(char >= 'A' && char <= 'F')) {
			return false
		}
	}
	return true
}

// ParseValidatorsTxt parses a traditional rippled validators.txt file and
// converts it to a ValidatorsConfig.
//
// [validators] and legacy [validator_keys] lines are
// `<key> [optional comment/nickname]` — rippled matches "node identity"
// followed by an optional comment (ValidatorList.cpp:145-155) — so only
// the first whitespace-separated token is taken as the key. The same
// applies to the other key/site sections for symmetry with rippled's
// tokenization.
func ParseValidatorsTxt(content string) (*ValidatorsConfig, error) {
	config := &ValidatorsConfig{}
	var legacyValidatorKeys []string
	thresholdSet := false
	content = strings.ReplaceAll(content, "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")

	currentSection := ""
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = line[1 : len(line)-1]
			continue
		}

		token := strings.Fields(line)[0]

		switch currentSection {
		case "validators":
			config.Validators = append(config.Validators, token)
		case "validator_keys":
			legacyValidatorKeys = append(legacyValidatorKeys, token)
		case "validator_list_sites":
			config.ValidatorListSites = append(config.ValidatorListSites, token)
		case "validator_list_keys":
			config.ValidatorListKeys = append(config.ValidatorListKeys, token)
		case "validator_list_threshold":
			if thresholdSet {
				return nil, fmt.Errorf("validator_list_threshold must contain exactly one value")
			}
			thresholdSet = true
			value := line
			if before, _, found := strings.Cut(value, "#"); found {
				value = strings.TrimSpace(before)
			}
			threshold, err := strconv.Atoi(value)
			if err != nil {
				return nil, fmt.Errorf("invalid validator_list_threshold value %q: %w", value, err)
			}
			config.ValidatorListThreshold = threshold
		}
	}
	config.Validators = append(config.Validators, legacyValidatorKeys...)

	return config, nil
}
