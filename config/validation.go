package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/protocol"
)

// ValidateConfig performs comprehensive validation on the complete configuration.
// It collects ALL errors and returns them at once so operators can fix everything in one pass.
// Individual errors are preserved via errors.Join so callers may inspect them
// with errors.Is / errors.As.
func ValidateConfig(config *Config) error {
	var errs []error

	// 1. Check all required fields are present
	for _, msg := range validateRequiredFields(config) {
		errs = append(errs, errors.New(msg))
	}

	// 2. Validate port configurations (if ports exist)
	if (config.Server.User == "") != (config.Server.Password == "") {
		errs = append(errs, errors.New("server user and password must be configured together"))
	}
	if _, err := NormalizeOrigins(config.Server.AllowedOrigins); err != nil {
		errs = append(errs, fmt.Errorf("server allowed_origins: %w", err))
	}
	if len(config.Ports) > 0 {
		errs = append(errs, validatePorts(config.Ports)...)
	}

	// 3. Validate peer protocol settings
	errs = append(errs, validatePeerProtocol(config)...)

	// 4. Validate ripple protocol settings
	errs = append(errs, validateRippleProtocol(config)...)

	// 5. Validate database configuration
	if err := config.NodeDB.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("node_db: %w", err))
	}
	if err := config.SQLite.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("sqlite: %w", err))
	}
	if err := config.ValidationArchive.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("validation_archive: %w", err))
	}

	// 6. Validate diagnostics configuration
	if err := config.Logging.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("logging: %w", err))
	}
	if err := config.Watchdog.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("watchdog: %w", err))
	}

	// 7. Validate voting configuration
	if err := config.Voting.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("voting: %w", err))
	}
	if err := config.Amendments.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("amendments: %w", err))
	}

	// 8. Validate misc settings
	errs = append(errs, validateMiscSettings(config)...)

	// 9. Validate validators configuration
	if err := config.Validators.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("validators: %w", err))
	}

	// 10. Validate overlay and transaction queue
	if err := config.Overlay.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("overlay: %w", err))
	}
	if err := config.TransactionQueue.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("transaction_queue: %w", err))
	}

	// 11. Cross-validation checks
	errs = append(errs, validateCrossReferences(config)...)

	if len(errs) > 0 {
		return fmt.Errorf("configuration errors:\n%w", errors.Join(errs...))
	}

	return nil
}

// validateRequiredFields checks that all required fields are present in the
// config. Only keys an actual consumer reads are required; optional tuning
// keys are validated by the per-section validators when set.
// Returns a list of all missing fields so operators can fix everything at once.
func validateRequiredFields(config *Config) []string {
	var missing []string

	// Server ports
	if len(config.Server.Ports) == 0 {
		missing = append(missing, "missing required field: server.ports (at least one port must be specified)")
	}

	// Database
	if config.NodeDB.Type == "" {
		missing = append(missing, "missing required field: node_db.type")
	}
	if config.NodeDB.Path == "" {
		missing = append(missing, "missing required field: node_db.path")
	}
	if config.DatabasePath == "" {
		missing = append(missing, "missing required field: database_path")
	}

	// Network
	if config.NetworkID.IsZero() {
		missing = append(missing, "missing required field: network_id")
	}

	// Logging
	if config.DebugLogfile == "" {
		missing = append(missing, "missing required field: debug_logfile")
	}

	return missing
}

// validatePorts validates all port configurations, collecting every error.
func validatePorts(ports map[string]PortConfig) []error {
	var errs []error

	usedPorts := make(map[string]string)
	peerPortCount := 0

	for portName, portConfig := range ports {
		if err := portConfig.Validate(); err != nil {
			errs = append(errs, fmt.Errorf("port %s: %w", portName, err))
		}
		if (len(portConfig.Admin) > 0 || portConfig.AdminUser != "" || portConfig.AdminPassword != "") &&
			len(portConfig.AllowedOrigins) > 0 && (portConfig.User == "" || portConfig.Password == "") {
			errs = append(errs, fmt.Errorf("port %s: admin-capable browser listener requires both user and password", portName))
		}

		portKey := fmt.Sprintf("%s:%d", portConfig.IP, portConfig.Port)
		if existingPort, exists := usedPorts[portKey]; exists {
			errs = append(errs, fmt.Errorf("port conflict: both %s and %s are trying to use %s", existingPort, portName, portKey))
		}
		usedPorts[portKey] = portName

		if portConfig.HasPeer() {
			peerPortCount++
		}
	}

	if peerPortCount > 1 {
		errs = append(errs, fmt.Errorf("only one port may be configured to support the peer protocol, found %d", peerPortCount))
	}

	return errs
}

// validatePeerProtocol validates peer protocol settings, collecting every error.
func validatePeerProtocol(config *Config) []error {
	var errs []error

	if err := ValidatePeersMax(config.PeersMax); err != nil {
		errs = append(errs, err)
	}
	if err := ValidatePeerPrivate(config.PeerPrivate); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateMaxTransactions(config.MaxTransactions); err != nil {
		errs = append(errs, err)
	}

	for i, ip := range config.IPs {
		if err := validateIPEntry(ip); err != nil {
			errs = append(errs, fmt.Errorf("invalid IP entry at index %d: %w", i, err))
		}
	}

	for i, ip := range config.IPsFixed {
		if err := validateFixedIPEntry(ip); err != nil {
			errs = append(errs, fmt.Errorf("invalid fixed IP entry at index %d: %w", i, err))
		}
	}

	return errs
}

// validateRippleProtocol validates ripple protocol settings, collecting every error.
func validateRippleProtocol(config *Config) []error {
	var errs []error

	if err := ValidateRelayProposals(config.RelayProposals); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateRelayValidations(config.RelayValidations); err != nil {
		errs = append(errs, err)
	}

	if config.NetworkID.Set {
		if _, err := config.ResolvedNetworkID(); err != nil {
			errs = append(errs, fmt.Errorf("invalid network_id: %w", err))
		}
	}

	if err := ValidateLedgerReplay(config.LedgerReplay); err != nil {
		errs = append(errs, err)
	}

	return errs
}

// validateMiscSettings validates miscellaneous settings, collecting every error.
func validateMiscSettings(config *Config) []error {
	var errs []error

	if err := ValidateNodeSize(config.NodeSize); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateBetaRPCAPI(config.BetaRPCAPI); err != nil {
		errs = append(errs, err)
	}
	if err := ValidateWebsocketPingFrequency(config.WebsocketPingFrequency); err != nil {
		errs = append(errs, err)
	}
	if config.ServerDomain != "" && !protocol.IsProperlyFormedTomlDomain(config.ServerDomain) {
		errs = append(errs, errors.New("invalid server_domain: the domain name does not appear to meet the requirements"))
	}
	if config.FeeDefault != nil {
		if err := validateVotingValue("fee_default", *config.FeeDefault, uint64(drops.MaxDrops)); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

// validateCrossReferences validates cross-references between different
// config sections, collecting every error.
//
// The online_delete vs ledger_history check mirrors rippled's
// SHAMapStoreImp.cpp:148-154 invariant ("online_delete must not be less than
// ledger_history"). With ledger_history = "full" rippled stores uint32::max,
// so the comparison always fires; LedgerHistory.Value() returns math.MaxInt32
// to preserve that semantic here. The Full case is reported with a friendlier
// message so the error doesn't expose the sentinel.
//
// The validation_seed / validator_token exclusion matches rippled's hard
// error (Config.cpp:635-638) — both set means an ambiguous signing identity.
func validateCrossReferences(config *Config) []error {
	var errs []error

	if config.ValidationSeed != "" && config.ValidatorToken != "" {
		errs = append(errs, errors.New("cannot have both [validation_seed] and [validator_token] config sections"))
	}

	ledgerHistory := config.ResolvedLedgerHistory()
	if config.NodeDB.OnlineDelete > 0 && ledgerHistory > 0 && config.NodeDB.OnlineDelete < ledgerHistory {
		if config.LedgerHistory.Full {
			errs = append(errs, fmt.Errorf("online_delete (%d) must be greater than or equal to ledger_history (\"full\")",
				config.NodeDB.OnlineDelete))
		} else {
			errs = append(errs, fmt.Errorf("online_delete (%d) must be greater than or equal to ledger_history (%d)",
				config.NodeDB.OnlineDelete, ledgerHistory))
		}
	}

	if config.IsValidator() {
		if _, _, hasPeerPort := config.PeerPort(); !hasPeerPort {
			errs = append(errs, errors.New("validator configuration requires a peer port to be configured"))
		}
	}

	return errs
}

// validateIPEntry validates an IP entry from the [ips] section
func validateIPEntry(entry string) error {
	if entry == "" {
		return errors.New("IP entry cannot be empty")
	}

	parts := strings.Fields(entry)
	if len(parts) > 2 || len(parts) == 0 {
		return errors.New("invalid format, expected 'IP [port]'")
	}

	if parts[0] == "" {
		return errors.New("IP address cannot be empty")
	}

	if len(parts) == 2 {
		if err := validatePeerPortString(parts[1]); err != nil {
			return fmt.Errorf("invalid port: %w", err)
		}
	}

	return nil
}

// validateFixedIPEntry validates an IP entry from the [ips_fixed] section
func validateFixedIPEntry(entry string) error {
	if entry == "" {
		return errors.New("fixed IP entry cannot be empty")
	}

	return validateIPEntry(entry)
}

func validatePeerPortString(portStr string) error {
	if portStr == "" {
		return errors.New("port cannot be empty")
	}
	for _, digit := range portStr {
		if digit < '0' || digit > '9' {
			return errors.New("port must be numeric")
		}
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port must be numeric: %w", err)
	}
	if port < 0 || port > 65535 {
		return fmt.Errorf("port must be between 0 and 65535, got %d", port)
	}
	return nil
}

// validatePortString validates a port number in string format
func validatePortString(portStr string) error {
	if portStr == "" {
		return errors.New("port cannot be empty")
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("port must be numeric: %w", err)
	}

	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}

	return nil
}
