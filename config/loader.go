package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// LoadConfig loads configuration from the config file.
// No defaults are applied — every required value must be present in the config file.
// Returns an error listing ALL missing/invalid fields at once.
func LoadConfig(paths Paths) (*Config, error) {
	settings, err := readConfigFile(paths.Main)
	if err != nil {
		return nil, fmt.Errorf("failed to load main config: %w", err)
	}
	config, unknown, err := decodeConfig(settings)
	if err != nil {
		return nil, err
	}
	if len(unknown) > 0 {
		return nil, newUnknownKeysError(paths.Main, unknown)
	}

	if !paths.SkipValidators {
		validators, err := loadValidatorsConfig(paths, config.ValidatorsFile)
		if err != nil {
			return nil, fmt.Errorf("failed to load validators config: %w", err)
		}
		config.Validators = *validators
	}

	// Validate the complete configuration (reports ALL errors at once)
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}

	return config, nil
}

func decodeConfig(settings map[string]any) (*Config, []string, error) {
	v := viper.New()
	// MergeConfigMap normalizes the input keys for case-insensitive lookup.
	// Decode the map itself because Viper's flattened settings omit empty tables.
	if err := v.MergeConfigMap(settings); err != nil {
		return nil, nil, err
	}
	if err := validateConfigSettings(v); err != nil {
		return nil, nil, fmt.Errorf("failed to load main config: %w", err)
	}

	var document struct {
		Config `mapstructure:",squash"`
		Extra  map[string]any `mapstructure:",remain"`
	}
	unused, err := decodeSettings(settings, &document)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}
	config := &document.Config
	config.Voting.ReferenceFeeSet = v.IsSet("voting.reference_fee")
	config.Voting.AccountReserveSet = v.IsSet("voting.account_reserve")
	config.Voting.OwnerReserveSet = v.IsSet("voting.owner_reserve")

	portNames := config.Server.Ports
	if len(portNames) == 0 {
		for name, value := range document.Extra {
			if _, table := value.(map[string]any); table && strings.HasPrefix(name, portSectionPrefix) {
				portNames = append(portNames, name)
			}
		}
		slices.Sort(portNames)
	}
	config.Ports = make(map[string]PortConfig, len(portNames))
	for _, name := range portNames {
		section, ok := v.Get(name).(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("no configuration found for port %s", name)
		}
		port, keys, err := loadPortConfig(section, config.Server)
		if err != nil {
			return nil, nil, fmt.Errorf("failed to load port config %s: %w", name, err)
		}
		config.Ports[name] = port
		for _, key := range keys {
			unused = append(unused, strings.ToLower(name)+"."+key)
		}
	}
	for _, name := range portNames {
		removePortSection(document.Extra, strings.ToLower(name))
	}
	for key, value := range document.Extra {
		if _, table := value.(map[string]any); table && strings.HasPrefix(key, portSectionPrefix) {
			continue
		}
		unused = append(unused, key)
	}
	return config, unused, nil
}

func validateConfigSettings(v *viper.Viper) error {
	if err := validateManifestCounts(v); err != nil {
		return err
	}
	if err := validateAcquisitionMemory(v); err != nil {
		return err
	}
	if err := validateResources(v); err != nil {
		return err
	}
	if v.IsSet("backfill") {
		if _, ok := v.Get("backfill").(bool); !ok {
			return fmt.Errorf("backfill must be a boolean")
		}
	}
	if err := validateIntegerSetting(v, "ledger_cache_size", MinLedgerCacheSize, MaxLedgerCacheSize); err != nil {
		return err
	}
	if err := validateIntegerSetting(v, "sweep_interval", minSweepIntervalSeconds, maxSweepIntervalSeconds); err != nil {
		return err
	}
	if err := validatePositiveDurationSetting(v, "server.checkpoint_shutdown_grace"); err != nil {
		return err
	}
	return validateHTTPSettings(v)
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

func validateAcquisitionMemory(v *viper.Viper) error {
	const key = "acquisition.memory_mb"
	if !v.IsSet(key) {
		return nil
	}

	value := reflect.ValueOf(v.Get(key))
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		memoryMB := value.Int()
		if memoryMB < 0 {
			return fmt.Errorf("%s must be non-negative, got %d", key, memoryMB)
		}
		if uint64(memoryMB) > maxAcquisitionMemoryMB {
			return fmt.Errorf("%s is too large: %d MiB overflows bytes", key, memoryMB)
		}
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		memoryMB := value.Uint()
		if memoryMB > maxAcquisitionMemoryMB {
			return fmt.Errorf("%s is too large: %d MiB overflows bytes", key, memoryMB)
		}
	default:
		return fmt.Errorf("%s must be an integer count", key)
	}
	return nil
}

func validateResources(v *viper.Viper) error {
	const memoryKey = "resources.memory_mb"
	if v.IsSet(memoryKey) {
		value := reflect.ValueOf(v.Get(memoryKey))
		switch value.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			memoryMB := value.Int()
			if memoryMB < 0 {
				return fmt.Errorf("%s must be non-negative, got %d", memoryKey, memoryMB)
			}
			if uint64(memoryMB) > maxResourcesMemoryMB {
				return fmt.Errorf("%s is too large: %d MiB overflows bytes", memoryKey, memoryMB)
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			memoryMB := value.Uint()
			if memoryMB > maxResourcesMemoryMB {
				return fmt.Errorf("%s is too large: %d MiB overflows bytes", memoryKey, memoryMB)
			}
		default:
			return fmt.Errorf("%s must be an integer count", memoryKey)
		}
	}

	const percentKey = "resources.available_memory_percent"
	if v.IsSet(percentKey) {
		value := reflect.ValueOf(v.Get(percentKey))
		switch value.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			percent := value.Int()
			if percent < 0 || percent > 100 {
				return fmt.Errorf("%s must be between 0 and 100, got %d", percentKey, percent)
			}
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			percent := value.Uint()
			if percent > 100 {
				return fmt.Errorf("%s must be between 0 and 100, got %d", percentKey, percent)
			}
		default:
			return fmt.Errorf("%s must be an integer count", percentKey)
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

func readConfigFile(path string) (map[string]any, error) {
	if path == "" {
		return nil, fmt.Errorf("config path cannot be empty")
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("config file does not exist: %s", path)
	}
	if err != nil {
		return nil, err
	}
	format := strings.TrimPrefix(filepath.Ext(path), ".")
	decoder, err := viper.NewCodecRegistry().Decoder(format)
	if err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	settings := make(map[string]any)
	if err := decoder.Decode(data, settings); err != nil {
		return nil, fmt.Errorf("read config file %s: %w", path, err)
	}
	return settings, nil
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
	settings, err := readConfigFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read validators file %s: %w", filePath, err)
	}
	var validators ValidatorsConfig
	unused, err := decodeSettings(settings, &validators)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal validators config: %w", err)
	}
	if len(unused) > 0 {
		return nil, newUnknownKeysError(filePath, unused)
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
	if validators.Validators == nil && validators.ValidatorListKeys == nil {
		return fmt.Errorf("validators file must contain a validators, validator_keys (TXT), or validator_list_keys declaration")
	}
	return nil
}

func loadPortConfig(settings map[string]any, defaults ServerConfig) (PortConfig, []string, error) {
	port := PortConfig{
		Port: defaults.Port, IP: defaults.IP, Protocol: defaults.Protocol, Limit: defaults.Limit,
		User: defaults.User, Password: defaults.Password,
		AdminUser: defaults.AdminUser, AdminPassword: defaults.AdminPassword,
		AllowedOrigins: slices.Clone(defaults.AllowedOrigins), SSLCiphers: defaults.SSLCiphers,
	}
	unused, err := decodeSettings(settings, &port)
	if err != nil {
		return PortConfig{}, nil, err
	}
	port.Admin = append(slices.Clone(defaults.Admin), port.Admin...)
	port.SecureGateway = append(slices.Clone(defaults.SecureGateway), port.SecureGateway...)
	return port, unused, nil
}
