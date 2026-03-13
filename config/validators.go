package config

import (
	"fmt"
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
	// Validate individual validator keys
	for i, validator := range v.Validators {
		if err := validateValidatorKey(validator); err != nil {
			return fmt.Errorf("invalid validator at index %d: %w", i, err)
		}
	}

	// Validate validator list sites
	for i, site := range v.ValidatorListSites {
		if err := validateValidatorListSite(site); err != nil {
			return fmt.Errorf("invalid validator_list_site at index %d: %w", i, err)
		}
	}

	// Validate validator list keys
	for i, key := range v.ValidatorListKeys {
		if err := validateValidatorListKey(key); err != nil {
			return fmt.Errorf("invalid validator_list_key at index %d: %w", i, err)
		}
	}

	// Validate threshold
	if v.ValidatorListThreshold < 0 {
		return fmt.Errorf("validator_list_threshold must be non-negative, got %d", v.ValidatorListThreshold)
	}

	if len(v.ValidatorListKeys) > 0 && v.ValidatorListThreshold > len(v.ValidatorListKeys) {
		return fmt.Errorf("validator_list_threshold (%d) cannot be greater than number of validator_list_keys (%d)",
			v.ValidatorListThreshold, len(v.ValidatorListKeys))
	}

	return nil
}

// GetValidatorListThreshold returns the effective threshold value
func (v *ValidatorsConfig) GetValidatorListThreshold() int {
	if v.ValidatorListThreshold == 0 && len(v.ValidatorListKeys) > 0 {
		// Calculate threshold as per rippled logic
		if len(v.ValidatorListKeys) < 3 {
			return 1
		}
		return (len(v.ValidatorListKeys) / 2) + 1
	}
	return v.ValidatorListThreshold
}

// HasValidators returns true if any validators are configured
func (v *ValidatorsConfig) HasValidators() bool {
	return len(v.Validators) > 0
}

// HasValidatorListSites returns true if validator list sites are configured
func (v *ValidatorsConfig) HasValidatorListSites() bool {
	return len(v.ValidatorListSites) > 0
}

// HasValidatorListKeys returns true if validator list keys are configured
func (v *ValidatorsConfig) HasValidatorListKeys() bool {
	return len(v.ValidatorListKeys) > 0
}

// IsEmpty returns true if no validator configuration is present
func (v *ValidatorsConfig) IsEmpty() bool {
	return !v.HasValidators() && !v.HasValidatorListSites() && !v.HasValidatorListKeys()
}

// GetValidatorCount returns the total number of configured validators
func (v *ValidatorsConfig) GetValidatorCount() int {
	return len(v.Validators)
}

// GetValidatorListSiteCount returns the number of validator list sites
func (v *ValidatorsConfig) GetValidatorListSiteCount() int {
	return len(v.ValidatorListSites)
}

// GetValidatorListKeyCount returns the number of validator list keys
func (v *ValidatorsConfig) GetValidatorListKeyCount() int {
	return len(v.ValidatorListKeys)
}

// validateValidatorKey validates a single validator public key
func validateValidatorKey(key string) error {
	if key == "" {
		return fmt.Errorf("validator key cannot be empty")
	}

	// Basic validation - should start with 'n' and be the right length
	if !strings.HasPrefix(key, "n") {
		return fmt.Errorf("validator key must start with 'n', got: %s", key)
	}

	// Length check (rippled validator keys are typically 51 characters)
	if len(key) != 51 {
		return fmt.Errorf("validator key has invalid length %d, expected 51", len(key))
	}

	// Character set validation (base58)
	if !isValidBase58(key) {
		return fmt.Errorf("validator key contains invalid characters")
	}

	return nil
}

// validateValidatorListSite validates a validator list site URL
func validateValidatorListSite(site string) error {
	if site == "" {
		return fmt.Errorf("validator list site cannot be empty")
	}

	// Basic URL validation
	if !strings.HasPrefix(site, "http://") &&
		!strings.HasPrefix(site, "https://") &&
		!strings.HasPrefix(site, "file://") {
		return fmt.Errorf("validator list site must use http://, https://, or file:// scheme")
	}

	return nil
}

// validateValidatorListKey validates a validator list publisher key
func validateValidatorListKey(key string) error {
	if key == "" {
		return fmt.Errorf("validator list key cannot be empty")
	}

	// Should be hex-encoded and 64 characters long (32 bytes * 2)
	if len(key) != 64 {
		return fmt.Errorf("validator list key has invalid length %d, expected 64", len(key))
	}

	// Hex character validation
	if !isValidHex(key) {
		return fmt.Errorf("validator list key contains invalid hex characters")
	}

	return nil
}

// isValidBase58 checks if a string contains only valid base58 characters
func isValidBase58(s string) bool {
	base58Chars := "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	for _, char := range s {
		found := false
		for _, valid := range base58Chars {
			if char == valid {
				found = true
				break
			}
		}
		if !found {
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

// ParseValidatorsTxt parses a traditional validators.txt file format and converts to ValidatorsConfig
// This helper function allows migration from the old format
func ParseValidatorsTxt(content string) (*ValidatorsConfig, error) {
	config := &ValidatorsConfig{}

	lines := strings.Split(content, "\n")
	currentSection := ""

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Skip empty lines and comments
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Check for section headers
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.Trim(line, "[]")
			continue
		}

		// Parse content based on current section
		switch currentSection {
		case "validators":
			config.Validators = append(config.Validators, line)
		case "validator_list_sites":
			config.ValidatorListSites = append(config.ValidatorListSites, line)
		case "validator_list_keys":
			config.ValidatorListKeys = append(config.ValidatorListKeys, line)
		case "validator_list_threshold":
			// This is typically just a number, parse it
			var threshold int
			if _, err := fmt.Sscanf(line, "%d", &threshold); err == nil {
				config.ValidatorListThreshold = threshold
			}
		}
	}

	return config, nil
}
