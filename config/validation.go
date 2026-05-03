package config

import (
	"fmt"
	"strings"
)

// ValidateConfig performs comprehensive validation on the complete configuration.
// It collects ALL errors and returns them at once so operators can fix everything in one pass.
func ValidateConfig(config *Config) error {
	var errors []string

	// 1. Check all required fields are present
	errors = append(errors, validateRequiredFields(config)...)

	// 2. Validate port configurations (if ports exist)
	if len(config.Ports) > 0 {
		if portErrors := validatePorts(config.Ports); portErrors != nil {
			errors = append(errors, portErrors.Error())
		}
	}

	// 3. Validate peer protocol settings
	if err := validatePeerProtocol(config); err != nil {
		errors = append(errors, err.Error())
	}

	// 4. Validate ripple protocol settings
	if err := validateRippleProtocol(config); err != nil {
		errors = append(errors, err.Error())
	}

	// 5. Validate database configuration
	if err := config.NodeDB.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("node_db: %s", err.Error()))
	}
	if err := config.ImportDB.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("import_db: %s", err.Error()))
	}
	if err := config.SQLite.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("sqlite: %s", err.Error()))
	}
	if err := config.ValidationArchive.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("validation_archive: %s", err.Error()))
	}

	// 6. Validate diagnostics configuration
	if err := config.Logging.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("logging: %s", err.Error()))
	}
	if err := config.Insight.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("insight: %s", err.Error()))
	}
	if err := config.Perf.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("perf: %s", err.Error()))
	}

	// 7. Validate voting configuration
	if err := config.Voting.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("voting: %s", err.Error()))
	}

	// 8. Validate misc settings
	if err := validateMiscSettings(config); err != nil {
		errors = append(errors, err.Error())
	}

	// 9. Validate crawl and VL configuration
	if err := config.Crawl.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("crawl: %s", err.Error()))
	}
	if err := config.VL.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("vl: %s", err.Error()))
	}

	// 10. Validate validators configuration
	if err := config.Validators.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("validators: %s", err.Error()))
	}

	// 11. Validate overlay and transaction queue
	if err := config.Overlay.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("overlay: %s", err.Error()))
	}
	if err := config.TransactionQueue.Validate(); err != nil {
		errors = append(errors, fmt.Sprintf("transaction_queue: %s", err.Error()))
	}

	// 12. Cross-validation checks
	if crossErrors := validateCrossReferences(config); crossErrors != nil {
		errors = append(errors, crossErrors.Error())
	}

	if len(errors) > 0 {
		return fmt.Errorf("configuration errors:\n  - %s", strings.Join(errors, "\n  - "))
	}

	return nil
}

// validateRequiredFields checks that all required fields are present in the config.
// Returns a list of all missing fields so operators can fix everything at once.
func validateRequiredFields(config *Config) []string {
	var missing []string

	// Server ports
	if len(config.Server.Ports) == 0 {
		missing = append(missing, "missing required field: server.ports (at least one port must be specified)")
	}

	// Port configurations
	for name, port := range config.Ports {
		if port.Port == 0 {
			missing = append(missing, fmt.Sprintf("missing required field: %s.port", name))
		}
		if port.IP == "" {
			missing = append(missing, fmt.Sprintf("missing required field: %s.ip", name))
		}
		if port.Protocol == "" {
			missing = append(missing, fmt.Sprintf("missing required field: %s.protocol", name))
		}
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

	// Ledger
	if config.LedgerHistory.IsZero() {
		missing = append(missing, "missing required field: ledger_history")
	}
	if config.FetchDepth.IsZero() {
		missing = append(missing, "missing required field: fetch_depth")
	}

	// Node sizing
	if config.NodeSize == "" {
		missing = append(missing, "missing required field: node_size (valid: tiny, small, medium, large, huge)")
	}

	// Logging
	if config.DebugLogfile == "" {
		missing = append(missing, "missing required field: debug_logfile")
	}

	// Relay policy
	if config.RelayProposals == "" {
		missing = append(missing, "missing required field: relay_proposals (valid: all, trusted, drop_untrusted)")
	}
	if config.RelayValidations == "" {
		missing = append(missing, "missing required field: relay_validations (valid: all, trusted, drop_untrusted)")
	}

	// Overlay
	if config.Overlay.MaxUnknownTime == 0 {
		missing = append(missing, "missing required field: overlay.max_unknown_time")
	}
	if config.Overlay.MaxDivergedTime == 0 {
		missing = append(missing, "missing required field: overlay.max_diverged_time")
	}

	// Transaction queue
	if config.TransactionQueue.LedgersInQueue == 0 {
		missing = append(missing, "missing required field: transaction_queue.ledgers_in_queue")
	}
	if config.TransactionQueue.MinimumQueueSize == 0 {
		missing = append(missing, "missing required field: transaction_queue.minimum_queue_size")
	}
	if config.TransactionQueue.RetrySequencePercent == 0 {
		missing = append(missing, "missing required field: transaction_queue.retry_sequence_percent")
	}
	if config.TransactionQueue.MinimumEscalationMultiplier == 0 {
		missing = append(missing, "missing required field: transaction_queue.minimum_escalation_multiplier")
	}
	if config.TransactionQueue.MinimumTxnInLedger == 0 {
		missing = append(missing, "missing required field: transaction_queue.minimum_txn_in_ledger")
	}
	if config.TransactionQueue.MinimumTxnInLedgerStandalone == 0 {
		missing = append(missing, "missing required field: transaction_queue.minimum_txn_in_ledger_standalone")
	}
	if config.TransactionQueue.TargetTxnInLedger == 0 {
		missing = append(missing, "missing required field: transaction_queue.target_txn_in_ledger")
	}
	// maximum_txn_in_ledger: 0 is valid (means no maximum), so NOT required
	if config.TransactionQueue.NormalConsensusIncreasePercent == 0 {
		missing = append(missing, "missing required field: transaction_queue.normal_consensus_increase_percent")
	}
	if config.TransactionQueue.SlowConsensusDecreasePercent == 0 {
		missing = append(missing, "missing required field: transaction_queue.slow_consensus_decrease_percent")
	}
	if config.TransactionQueue.MaximumTxnPerAccount == 0 {
		missing = append(missing, "missing required field: transaction_queue.maximum_txn_per_account")
	}
	if config.TransactionQueue.MinimumLastLedgerBuffer == 0 {
		missing = append(missing, "missing required field: transaction_queue.minimum_last_ledger_buffer")
	}
	if config.TransactionQueue.ZeroBaseFeeTransactionFeeLevel == 0 {
		missing = append(missing, "missing required field: transaction_queue.zero_basefee_transaction_feelevel")
	}

	// Max transactions
	if config.MaxTransactions == 0 {
		missing = append(missing, "missing required field: max_transactions")
	}

	// SQLite settings (required when safety_level is not set)
	if config.SQLite.SafetyLevel == "" {
		if config.SQLite.JournalMode == "" {
			missing = append(missing, "missing required field: sqlite.journal_mode")
		}
		if config.SQLite.Synchronous == "" {
			missing = append(missing, "missing required field: sqlite.synchronous")
		}
		if config.SQLite.TempStore == "" {
			missing = append(missing, "missing required field: sqlite.temp_store")
		}
		if config.SQLite.PageSize == 0 {
			missing = append(missing, "missing required field: sqlite.page_size")
		}
		if config.SQLite.JournalSizeLimit == 0 {
			missing = append(missing, "missing required field: sqlite.journal_size_limit")
		}
	}

	return missing
}

// validatePorts validates all port configurations
func validatePorts(ports map[string]PortConfig) error {
	if len(ports) == 0 {
		return fmt.Errorf("no ports configured")
	}

	usedPorts := make(map[string]string)
	peerPortCount := 0

	for portName, portConfig := range ports {
		if err := portConfig.Validate(); err != nil {
			return fmt.Errorf("port %s: %w", portName, err)
		}

		portKey := fmt.Sprintf("%s:%d", portConfig.IP, portConfig.Port)
		if existingPort, exists := usedPorts[portKey]; exists {
			return fmt.Errorf("port conflict: both %s and %s are trying to use %s", existingPort, portName, portKey)
		}
		usedPorts[portKey] = portName

		if portConfig.HasPeer() {
			peerPortCount++
		}
	}

	if peerPortCount > 1 {
		return fmt.Errorf("only one port may be configured to support the peer protocol, found %d", peerPortCount)
	}

	return nil
}

// validatePeerProtocol validates peer protocol settings
func validatePeerProtocol(config *Config) error {
	if err := ValidatePeersMax(config.PeersMax); err != nil {
		return err
	}
	if err := ValidatePeerPrivate(config.PeerPrivate); err != nil {
		return err
	}
	if err := ValidateMaxTransactions(config.MaxTransactions); err != nil {
		return err
	}

	for i, ip := range config.IPs {
		if err := validateIPEntry(ip); err != nil {
			return fmt.Errorf("invalid IP entry at index %d: %w", i, err)
		}
	}

	for i, ip := range config.IPsFixed {
		if err := validateFixedIPEntry(ip); err != nil {
			return fmt.Errorf("invalid fixed IP entry at index %d: %w", i, err)
		}
	}

	return nil
}

// validateRippleProtocol validates ripple protocol settings
func validateRippleProtocol(config *Config) error {
	if err := ValidateRelayProposals(config.RelayProposals); err != nil {
		return err
	}
	if err := ValidateRelayValidations(config.RelayValidations); err != nil {
		return err
	}

	if _, err := config.GetLedgerHistory(); err != nil {
		return fmt.Errorf("invalid ledger_history: %w", err)
	}

	if _, err := config.GetFetchDepth(); err != nil {
		return fmt.Errorf("invalid fetch_depth: %w", err)
	}

	if _, err := config.GetNetworkID(); err != nil {
		return fmt.Errorf("invalid network_id: %w", err)
	}

	if err := ValidatePathSearch(config.PathSearch); err != nil {
		return err
	}
	if err := ValidatePathSearchFast(config.PathSearchFast); err != nil {
		return err
	}
	if err := ValidatePathSearchMax(config.PathSearchMax); err != nil {
		return err
	}
	if err := ValidatePathSearchOld(config.PathSearchOld); err != nil {
		return err
	}

	if err := ValidateWorkers(config.Workers); err != nil {
		return err
	}
	if err := ValidateIOWorkers(config.IOWorkers); err != nil {
		return err
	}
	if err := ValidatePrefetchWorkers(config.PrefetchWorkers); err != nil {
		return err
	}

	if err := ValidateLedgerReplay(config.LedgerReplay); err != nil {
		return err
	}

	return nil
}

// validateMiscSettings validates miscellaneous settings
func validateMiscSettings(config *Config) error {
	if err := ValidateNodeSize(config.NodeSize); err != nil {
		return err
	}
	if err := ValidateSSLVerify(config.SSLVerify); err != nil {
		return err
	}
	if err := ValidateBetaRPCAPI(config.BetaRPCAPI); err != nil {
		return err
	}
	if err := ValidateWebsocketPingFrequency(config.WebsocketPingFrequency); err != nil {
		return err
	}
	if err := config.WebSocket.Validate(); err != nil {
		return err
	}

	return nil
}

// validateCrossReferences validates cross-references between different config sections.
//
// The online_delete vs ledger_history check mirrors rippled's
// SHAMapStoreImp.cpp:148-154 invariant ("online_delete must not be less than
// ledger_history"). With ledger_history = "full" rippled stores uint32::max,
// so the comparison always fires; LedgerHistory.Value() returns math.MaxInt32
// to preserve that semantic here. The Full case is reported with a friendlier
// message so the error doesn't expose the sentinel.
func validateCrossReferences(config *Config) error {
	ledgerHistory, err := config.GetLedgerHistory()
	if err != nil {
		return err
	}

	if config.NodeDB.OnlineDelete > 0 && ledgerHistory > 0 && config.NodeDB.OnlineDelete < ledgerHistory {
		if config.LedgerHistory.Full {
			return fmt.Errorf("online_delete (%d) must be greater than or equal to ledger_history (\"full\")",
				config.NodeDB.OnlineDelete)
		}
		return fmt.Errorf("online_delete (%d) must be greater than or equal to ledger_history (%d)",
			config.NodeDB.OnlineDelete, ledgerHistory)
	}

	if config.IsValidator() {
		_, _, hasPeerPort := config.GetPeerPort()
		if !hasPeerPort {
			return fmt.Errorf("validator configuration requires a peer port to be configured")
		}
	}

	for i, cmd := range config.RPCStartup {
		if cmd.Command == "" {
			return fmt.Errorf("rpc_startup command at index %d missing 'command' field", i)
		}
	}

	return nil
}

// validateIPEntry validates an IP entry from the [ips] section
func validateIPEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("IP entry cannot be empty")
	}

	parts := strings.Fields(entry)
	if len(parts) > 2 || len(parts) == 0 {
		return fmt.Errorf("invalid format, expected 'IP [port]'")
	}

	if parts[0] == "" {
		return fmt.Errorf("IP address cannot be empty")
	}

	if len(parts) == 2 {
		if err := validatePortString(parts[1]); err != nil {
			return fmt.Errorf("invalid port: %w", err)
		}
	}

	return nil
}

// validateFixedIPEntry validates an IP entry from the [ips_fixed] section
func validateFixedIPEntry(entry string) error {
	if entry == "" {
		return fmt.Errorf("fixed IP entry cannot be empty")
	}

	parts := strings.Fields(entry)
	if len(parts) != 2 {
		return fmt.Errorf("fixed IP entries must include a port, expected 'IP port'")
	}

	if parts[0] == "" {
		return fmt.Errorf("IP address cannot be empty")
	}

	if err := validatePortString(parts[1]); err != nil {
		return fmt.Errorf("invalid port: %w", err)
	}

	return nil
}

// validatePortString validates a port number in string format
func validatePortString(portStr string) error {
	if portStr == "" {
		return fmt.Errorf("port cannot be empty")
	}

	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		return fmt.Errorf("port must be numeric: %w", err)
	}

	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}

	return nil
}

// ValidateConfigPaths validates that configuration file paths are accessible
func ValidateConfigPaths(paths ConfigPaths) error {
	if paths.Main == "" {
		return fmt.Errorf("main config path cannot be empty")
	}

	return nil
}
