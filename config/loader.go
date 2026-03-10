package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	// Unmarshal into struct
	var config Config
	if err := v.Unmarshal(&config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Load validators configuration
	validators, err := loadValidatorsConfig(paths.Validators, config.ValidatorsFile)
	if err != nil {
		return nil, fmt.Errorf("failed to load validators config: %w", err)
	}
	config.Validators = *validators

	// Process dynamic port configurations
	if err := processPorts(&config, v); err != nil {
		return nil, fmt.Errorf("failed to process ports: %w", err)
	}

	// Store paths for reference
	config.configPath = paths.Main
	config.validatorsPath = paths.Validators

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
// If validators_file is explicitly specified in the config, the file MUST exist.
// If not specified, returns empty config (validation will catch this for non-standalone mode).
func loadValidatorsConfig(validatorsPath, validatorsFile string) (*ValidatorsConfig, error) {
	var filePath string
	if validatorsPath != "" {
		filePath = validatorsPath
	} else if validatorsFile != "" {
		if !filepath.IsAbs(validatorsFile) {
			filePath = validatorsFile
		} else {
			filePath = validatorsFile
		}
	} else {
		// No validators file specified — return empty config
		return &ValidatorsConfig{}, nil
	}

	// If explicitly specified, file MUST exist
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		// Try alternative formats
		if strings.HasSuffix(filePath, ".toml") {
			txtPath := strings.TrimSuffix(filePath, ".toml") + ".txt"
			if _, err := os.Stat(txtPath); err == nil {
				return loadValidatorsTxtFile(txtPath)
			}
		}
		return nil, fmt.Errorf("validators file not found: %s (file was explicitly specified and must exist)", filePath)
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

// applyServerDefaults applies server-level defaults to a port configuration
func applyServerDefaults(portViper *viper.Viper, serverDefaults ServerConfig) {
	if serverDefaults.Port != 0 && !portViper.IsSet("port") {
		portViper.SetDefault("port", serverDefaults.Port)
	}
	if serverDefaults.IP != "" && !portViper.IsSet("ip") {
		portViper.SetDefault("ip", serverDefaults.IP)
	}
	if serverDefaults.Protocol != "" && !portViper.IsSet("protocol") {
		portViper.SetDefault("protocol", serverDefaults.Protocol)
	}
	if serverDefaults.Limit != 0 && !portViper.IsSet("limit") {
		portViper.SetDefault("limit", serverDefaults.Limit)
	}
	if serverDefaults.User != "" && !portViper.IsSet("user") {
		portViper.SetDefault("user", serverDefaults.User)
	}
	if serverDefaults.Password != "" && !portViper.IsSet("password") {
		portViper.SetDefault("password", serverDefaults.Password)
	}
}

// LoadConfigFromDir loads configuration from a directory containing both files
func LoadConfigFromDir(configDir string) (*Config, error) {
	paths := ConfigPathsFromDir(configDir)
	return LoadConfig(paths)
}

// ReloadConfig reloads configuration from the same paths
func ReloadConfig(existingConfig *Config) (*Config, error) {
	paths := ConfigPaths{
		Main:       existingConfig.GetConfigPath(),
		Validators: existingConfig.GetValidatorsPath(),
	}
	return LoadConfig(paths)
}
