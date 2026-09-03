package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// LoadConfig loads configuration from the config file.
// No defaults are applied — every required value must be present in the config file.
// Returns an error listing ALL missing/invalid fields at once.
func LoadConfig(paths Paths) (*Config, error) {
	v := viper.New()

	// Load main configuration file (required)
	if err := loadMainConfig(v, paths.Main); err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}
	if err := validateManifestCounts(v); err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}
	if err := validateIntegerSetting(v, "ledger_cache_size", MinLedgerCacheSize, MaxLedgerCacheSize); err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}
	if err := validateIntegerSetting(v, "sweep_interval", minSweepIntervalSeconds, maxSweepIntervalSeconds); err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}
	if err := validatePositiveDurationSetting(v, "server.checkpoint_shutdown_grace"); err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}

	// Unmarshal into struct.
	// The custom decode hook is required so viper can decode the typed
	// union fields (LedgerHistory, FetchDepth, NetworkID) from raw TOML
	// scalars. The remaining hooks preserve viper's default behaviour
	// for time durations and slices.
	var config Config
	if err := v.Unmarshal(&config, viper.DecodeHook(mapstructure.ComposeDecodeHookFunc(
		configDecodeHook(),
		mapstructure.StringToTimeDurationHookFunc(),
		mapstructure.StringToSliceHookFunc(","),
	))); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	config.Voting.ReferenceFeeSet = v.IsSet("voting.reference_fee")
	config.Voting.AccountReserveSet = v.IsSet("voting.account_reserve")
	config.Voting.OwnerReserveSet = v.IsSet("voting.owner_reserve")

	if !paths.SkipValidators {
		validators, err := loadValidatorsConfig(paths, config.ValidatorsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load validators config: %w", err)
		}
		config.Validators = *validators
	}

	// Process dynamic port configurations
	if err := processPorts(&config, v); err != nil {
		return nil, fmt.Errorf("failed to process ports: %w", err)
	}

	// Validate the complete configuration (reports ALL errors at once)
	if err := ValidateConfig(&config); err != nil {
		return nil, err
	}

	return &config, nil
}

func validatePositiveDurationSetting(v *viper.Viper, key string) error {
	if !v.IsSet(key) {
		return nil
	}
	value, ok := v.Get(key).(string)
	if !ok {
		return fmt.Errorf("%s must be a duration string", key)
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return fmt.Errorf("%s must be a valid duration string, got %q: %w", key, value, err)
	}
	if duration <= 0 {
		return fmt.Errorf("%s must be positive, got %q", key, value)
	}
	return nil
}

// validateManifestCounts rejects weakly-typed TOML values before
// mapstructure can coerce them into *int fields. Manifest counts are integer
// settings; accepting strings or floating-point values here would make a
// malformed configuration appear valid after decoding.
func validateManifestCounts(v *viper.Viper) error {
	for _, key := range []string{"overlay.max_untrusted_count", "overlay.max_trusted_count"} {
		if err := validateIntegerSetting(v, key, MinManifestCount, MaxManifestCount); err != nil {
			return err
		}
	}
	return nil
}

func validateIntegerSetting(v *viper.Viper, key string, minValue, maxValue int) error {
	if !v.IsSet(key) {
		return nil
	}

	value := reflect.ValueOf(v.Get(key))
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		count := value.Int()
		if count < int64(minValue) || count > int64(maxValue) {
			return fmt.Errorf("%s must be between %d and %d, got %d", key, minValue, maxValue, count)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		count := value.Uint()
		if count < uint64(minValue) || count > uint64(maxValue) {
			return fmt.Errorf("%s must be between %d and %d, got %d", key, minValue, maxValue, count)
		}
	default:
		return fmt.Errorf("%s must be an integer count", key)
	}
	return nil
}

// loadMainConfig loads the main configuration file
func loadMainConfig(v *viper.Viper, configPath string) error {
	if configPath == "" {
		return fmt.Errorf("config path cannot be empty")
	}

	v.SetConfigFile(configPath)

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return fmt.Errorf("config file does not exist: %s", configPath)
	}

	if err := v.ReadInConfig(); err != nil {
		return fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	return nil
}

// loadValidatorsConfig loads the validators configuration.
//
// The operator's validators_file key takes precedence over a
// caller-supplied paths.Validators; a relative validators_file is
// resolved against the main config file's directory. With neither
// explicit source, an adjacent validators.txt is loaded when present.
// Explicitly selected files must exist.
func loadValidatorsConfig(paths Paths, validatorsFile string) (*ValidatorsConfig, error) {
	var filePath string
	implicit := false
	switch {
	case validatorsFile != "":
		filePath = validatorsFile
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(filepath.Dir(paths.Main), filePath)
		}
	case paths.Validators != "":
		filePath = paths.Validators
	default:
		if paths.Main == "" {
			return &ValidatorsConfig{}, nil
		}
		filePath = filepath.Join(filepath.Dir(paths.Main), "validators.txt")
		implicit = true
		info, err := os.Lstat(filePath)
		if err != nil {
			if os.IsNotExist(err) {
				return &ValidatorsConfig{}, nil
			}
			return nil, fmt.Errorf("stat default validators file %s: %w", filePath, err)
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return &ValidatorsConfig{}, nil
		}
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		if implicit {
			return &ValidatorsConfig{}, nil
		}
		return nil, fmt.Errorf("validators file not found: %s", filePath)
	}

	if strings.EqualFold(filepath.Ext(filePath), ".toml") {
		return loadValidatorsTomlFile(filePath)
	}
	return loadValidatorsTxtFile(filePath)
}

// loadValidatorsTomlFile loads validators from TOML format
func loadValidatorsTomlFile(filePath string) (*ValidatorsConfig, error) {
	v := viper.New()
	v.SetConfigFile(filePath)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read validators file %s: %w", filePath, err)
	}

	var validators ValidatorsConfig
	if err := v.Unmarshal(&validators); err != nil {
		return nil, fmt.Errorf("failed to unmarshal validators config: %w", err)
	}
	if err := validateValidatorsFile(&validators); err != nil {
		return nil, err
	}

	return &validators, nil
}

// loadValidatorsTxtFile loads validators from TXT format (rippled format)
func loadValidatorsTxtFile(filePath string) (*ValidatorsConfig, error) {
	content, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read validators file %s: %w", filePath, err)
	}

	validators, err := ParseValidatorsTxt(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse validators file %s: %w", filePath, err)
	}
	if err := validateValidatorsFile(validators); err != nil {
		return nil, fmt.Errorf("invalid validators file %s: %w", filePath, err)
	}

	return validators, nil
}

func validateValidatorsFile(validators *ValidatorsConfig) error {
	if len(validators.ValidatorListSites) > 0 && len(validators.ValidatorListKeys) == 0 {
		return fmt.Errorf("validator_list_sites requires at least one validator_list_keys entry")
	}
	if len(validators.Validators) == 0 && len(validators.ValidatorListKeys) == 0 {
		return fmt.Errorf("validators file must configure validators or validator_list_keys")
	}
	return nil
}

// processPorts processes dynamic port configurations
func processPorts(config *Config, v *viper.Viper) error {
	config.Ports = make(map[string]PortConfig)

	serverPorts := config.Server.Ports
	if len(serverPorts) == 0 {
		serverPorts = findPortSections(v)
	}

	for _, portName := range serverPorts {
		portConfig, err := loadPortConfig(v, portName, config.Server)
		if err != nil {
			return fmt.Errorf("failed to load port config %s: %w", portName, err)
		}
		config.Ports[portName] = portConfig
	}

	return nil
}

// findPortSections scans viper for sections that start with "port_"
func findPortSections(v *viper.Viper) []string {
	var ports []string

	allKeys := v.AllKeys()
	portMap := make(map[string]bool)

	for _, key := range allKeys {
		parts := strings.Split(key, ".")
		if len(parts) >= 2 && strings.HasPrefix(parts[0], "port_") {
			portName := parts[0]
			if !portMap[portName] {
				ports = append(ports, portName)
				portMap[portName] = true
			}
		}
	}

	return ports
}

// loadPortConfig loads configuration for a specific port
func loadPortConfig(v *viper.Viper, portName string, serverDefaults ServerConfig) (PortConfig, error) {
	var portConfig PortConfig

	portViper := v.Sub(portName)
	if portViper == nil {
		return PortConfig{}, fmt.Errorf("no configuration found for port %s", portName)
	}

	applyServerDefaults(portViper, serverDefaults)

	if err := portViper.Unmarshal(&portConfig); err != nil {
		return PortConfig{}, fmt.Errorf("failed to unmarshal port config: %w", err)
	}
	portConfig.Admin = append(append([]string(nil), serverDefaults.Admin...), portConfig.Admin...)
	portConfig.SecureGateway = append(append([]string(nil), serverDefaults.SecureGateway...), portConfig.SecureGateway...)

	return portConfig, nil
}

// applyServerDefaults applies server-level defaults to a port
// configuration. SetDefault already yields to values set explicitly in
// the port section, so no IsSet guards are needed.
func applyServerDefaults(portViper *viper.Viper, serverDefaults ServerConfig) {
	if serverDefaults.Port != 0 {
		portViper.SetDefault("port", serverDefaults.Port)
	}
	if serverDefaults.IP != "" {
		portViper.SetDefault("ip", serverDefaults.IP)
	}
	if serverDefaults.Protocol != "" {
		portViper.SetDefault("protocol", serverDefaults.Protocol)
	}
	if serverDefaults.Limit != 0 {
		portViper.SetDefault("limit", serverDefaults.Limit)
	}
	if serverDefaults.User != "" {
		portViper.SetDefault("user", serverDefaults.User)
	}
	if serverDefaults.Password != "" {
		portViper.SetDefault("password", serverDefaults.Password)
	}
	if serverDefaults.AdminUser != "" {
		portViper.SetDefault("admin_user", serverDefaults.AdminUser)
	}
	if serverDefaults.AdminPassword != "" {
		portViper.SetDefault("admin_password", serverDefaults.AdminPassword)
	}
	if len(serverDefaults.AllowedOrigins) > 0 {
		portViper.SetDefault("allowed_origins", serverDefaults.AllowedOrigins)
	}
	if serverDefaults.SSLCiphers != "" {
		portViper.SetDefault("ssl_ciphers", serverDefaults.SSLCiphers)
	}
}
