package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-viper/mapstructure/v2"
	"github.com/spf13/viper"
)

// LoadConfig loads configuration from the config file.
// No defaults are applied — every required value must be present in the config file.
// Returns an error listing ALL missing/invalid fields at once.
func LoadConfig(paths ConfigPaths) (*Config, error) {
	v := viper.New()

	// Load main configuration file (required)
	if err := loadMainConfig(v, paths.Main); err != nil {
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

	// Load validators configuration
	validators, err := loadValidatorsConfig(paths, config.ValidatorsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load validators config: %w", err)
	}
	config.Validators = *validators

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
// resolved against the main config file's directory, matching rippled's
// handling of [validators_file]. Whichever path is selected MUST exist
// — both sources are explicit requests for a validators file.
func loadValidatorsConfig(paths ConfigPaths, validatorsFile string) (*ValidatorsConfig, error) {
	var filePath string
	switch {
	case validatorsFile != "":
		filePath = validatorsFile
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(filepath.Dir(paths.Main), filePath)
		}
	case paths.Validators != "":
		filePath = paths.Validators
	default:
		// No validators file specified — return empty config
		return &ValidatorsConfig{}, nil
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Try alternative formats
		if before, ok := strings.CutSuffix(filePath, ".toml"); ok {
			txtPath := before + ".txt"
			if _, err := os.Stat(txtPath); err == nil {
				return loadValidatorsTxtFile(txtPath)
			}
		}
		return nil, fmt.Errorf("validators file not found: %s", filePath)
	}

	if strings.HasSuffix(filePath, ".toml") {
		return loadValidatorsTomlFile(filePath)
	}

	if strings.HasSuffix(filePath, ".txt") {
		return loadValidatorsTxtFile(filePath)
	}

	return nil, fmt.Errorf("unsupported validators file format: %s (supported: .toml, .txt)", filePath)
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

	return validators, nil
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
}
