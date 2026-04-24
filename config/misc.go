package config

import "fmt"

// CrawlConfig represents the [crawl] section
// Controls what data is reported through the /crawl endpoint
type CrawlConfig struct {
	Enabled bool `toml:"enabled" mapstructure:"enabled"`
	Overlay int  `toml:"overlay" mapstructure:"overlay"`
	Server  int  `toml:"server" mapstructure:"server"`
	Counts  int  `toml:"counts" mapstructure:"counts"`
	UNL     int  `toml:"unl" mapstructure:"unl"`
}

// VLConfig represents the [vl] section
// Controls what data is reported through the /vl endpoint
type VLConfig struct {
	Enable int `toml:"enable" mapstructure:"enable"`
}

// Validate performs validation on the crawl configuration
func (c *CrawlConfig) Validate() error {
	if c.Overlay != 0 && c.Overlay != 1 {
		return fmt.Errorf("crawl overlay must be 0 or 1, got %d", c.Overlay)
	}
	if c.Server != 0 && c.Server != 1 {
		return fmt.Errorf("crawl server must be 0 or 1, got %d", c.Server)
	}
	if c.Counts != 0 && c.Counts != 1 {
		return fmt.Errorf("crawl counts must be 0 or 1, got %d", c.Counts)
	}
	if c.UNL != 0 && c.UNL != 1 {
		return fmt.Errorf("crawl unl must be 0 or 1, got %d", c.UNL)
	}

	return nil
}

// Validate performs validation on the VL configuration
func (v *VLConfig) Validate() error {
	if v.Enable != 0 && v.Enable != 1 {
		return fmt.Errorf("vl enable must be 0 or 1, got %d", v.Enable)
	}

	return nil
}

// IsEnabled returns true if crawl endpoint is enabled
func (c *CrawlConfig) IsEnabled() bool {
	return c.Enabled
}

// IsOverlayEnabled returns true if overlay info should be reported
func (c *CrawlConfig) IsOverlayEnabled() bool {
	return c.Overlay == 1
}

// IsServerEnabled returns true if server info should be reported
func (c *CrawlConfig) IsServerEnabled() bool {
	return c.Server == 1
}

// IsCountsEnabled returns true if counts info should be reported
func (c *CrawlConfig) IsCountsEnabled() bool {
	return c.Counts == 1
}

// IsUNLEnabled returns true if UNL info should be reported
func (c *CrawlConfig) IsUNLEnabled() bool {
	return c.UNL == 1
}

// IsEnabled returns true if VL endpoint is enabled
func (v *VLConfig) IsEnabled() bool {
	return v.Enable == 1
}

// ValidateNodeSize validates the node_size setting
func ValidateNodeSize(nodeSize string) error {
	if nodeSize == "" {
		return fmt.Errorf("node_size is required (valid options: tiny, small, medium, large, huge)")
	}

	validSizes := []string{"tiny", "small", "medium", "large", "huge"}
	for _, valid := range validSizes {
		if nodeSize == valid {
			return nil
		}
	}

	return fmt.Errorf("invalid node_size: %s (valid options: tiny, small, medium, large, huge)", nodeSize)
}

// ValidateMaxTransactions validates the max_transactions setting
func ValidateMaxTransactions(maxTxn int) error {
	if maxTxn == 0 {
		return nil // 0 means use default
	}

	if maxTxn < 100 || maxTxn > 1000 {
		return fmt.Errorf("max_transactions must be between 100 and 1000, got %d", maxTxn)
	}

	return nil
}

// ValidateWorkers validates worker count settings
func ValidateWorkers(workers int) error {
	if workers < 0 {
		return fmt.Errorf("workers must be non-negative, got %d", workers)
	}
	return nil
}

// ValidateIOWorkers validates IO worker count settings
func ValidateIOWorkers(ioWorkers int) error {
	if ioWorkers < 0 {
		return fmt.Errorf("io_workers must be non-negative, got %d", ioWorkers)
	}
	return nil
}

// ValidatePrefetchWorkers validates prefetch worker count settings
func ValidatePrefetchWorkers(prefetchWorkers int) error {
	if prefetchWorkers < 0 {
		return fmt.Errorf("prefetch_workers must be non-negative, got %d", prefetchWorkers)
	}
	return nil
}

// ValidatePathSearch validates path search aggressiveness settings
func ValidatePathSearch(pathSearch int) error {
	if pathSearch < 0 {
		return fmt.Errorf("path_search must be non-negative, got %d", pathSearch)
	}
	return nil
}

// ValidatePathSearchFast validates fast path search setting
func ValidatePathSearchFast(pathSearchFast int) error {
	if pathSearchFast < 0 {
		return fmt.Errorf("path_search_fast must be non-negative, got %d", pathSearchFast)
	}
	return nil
}

// ValidatePathSearchMax validates max path search setting
func ValidatePathSearchMax(pathSearchMax int) error {
	if pathSearchMax < 0 {
		return fmt.Errorf("path_search_max must be non-negative, got %d", pathSearchMax)
	}
	return nil
}

// ValidatePathSearchOld validates old path search setting
func ValidatePathSearchOld(pathSearchOld int) error {
	if pathSearchOld < 0 {
		return fmt.Errorf("path_search_old must be non-negative, got %d", pathSearchOld)
	}
	return nil
}

// ValidatePeersMax validates the maximum peer count setting
func ValidatePeersMax(peersMax int) error {
	if peersMax < 0 {
		return fmt.Errorf("peers_max must be non-negative, got %d", peersMax)
	}
	return nil
}

// ValidatePeerPrivate validates the peer private setting
func ValidatePeerPrivate(peerPrivate int) error {
	if peerPrivate != 0 && peerPrivate != 1 {
		return fmt.Errorf("peer_private must be 0 or 1, got %d", peerPrivate)
	}
	return nil
}

// ValidateLedgerReplay validates the ledger replay setting
func ValidateLedgerReplay(ledgerReplay int) error {
	if ledgerReplay != 0 && ledgerReplay != 1 {
		return fmt.Errorf("ledger_replay must be 0 or 1, got %d", ledgerReplay)
	}
	return nil
}

// ValidateSSLVerify validates the SSL verify setting
func ValidateSSLVerify(sslVerify int) error {
	if sslVerify != 0 && sslVerify != 1 {
		return fmt.Errorf("ssl_verify must be 0 or 1, got %d", sslVerify)
	}
	return nil
}

// ValidateBetaRPCAPI validates the beta RPC API setting
func ValidateBetaRPCAPI(betaAPI int) error {
	if betaAPI != 0 && betaAPI != 1 {
		return fmt.Errorf("beta_rpc_api must be 0 or 1, got %d", betaAPI)
	}
	return nil
}

// ValidateWebsocketPingFrequency validates the websocket ping frequency
func ValidateWebsocketPingFrequency(frequency int) error {
	if frequency < 0 {
		return fmt.Errorf("websocket_ping_frequency must be non-negative, got %d", frequency)
	}
	return nil
}

// ValidateRelayProposals validates the relay proposals setting
func ValidateRelayProposals(relayProposals string) error {
	if relayProposals == "" {
		return fmt.Errorf("relay_proposals is required (valid options: all, trusted, drop_untrusted)")
	}

	validOptions := []string{"all", "trusted", "drop_untrusted"}
	for _, valid := range validOptions {
		if relayProposals == valid {
			return nil
		}
	}

	return fmt.Errorf("invalid relay_proposals: %s (valid options: all, trusted, drop_untrusted)", relayProposals)
}

// ValidateRelayValidations validates the relay validations setting
func ValidateRelayValidations(relayValidations string) error {
	if relayValidations == "" {
		return fmt.Errorf("relay_validations is required (valid options: all, trusted, drop_untrusted)")
	}

	validOptions := []string{"all", "trusted", "drop_untrusted"}
	for _, valid := range validOptions {
		if relayValidations == valid {
			return nil
		}
	}

	return fmt.Errorf("invalid relay_validations: %s (valid options: all, trusted, drop_untrusted)", relayValidations)
}
