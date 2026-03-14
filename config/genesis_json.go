package config

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/LeJamon/goXRPLd/drops"
)

// GenesisJSON represents the JSON genesis file format
type GenesisJSON struct {
	Ledger             GenesisLedgerJSON `json:"ledger"`
	LedgerCurrentIndex int               `json:"ledger_current_index,omitempty"`
	Status             string            `json:"status,omitempty"`
	Validated          bool              `json:"validated,omitempty"`
}

// GenesisLedgerJSON represents the ledger section of genesis JSON
type GenesisLedgerJSON struct {
	Accepted            bool              `json:"accepted"`
	AccountState        []json.RawMessage `json:"accountState"`
	AccountHash         string            `json:"account_hash"`
	CloseFlags          int               `json:"close_flags"`
	CloseTime           int64             `json:"close_time"`
	CloseTimeHuman      string            `json:"close_time_human,omitempty"`
	CloseTimeResolution int               `json:"close_time_resolution"`
	Closed              bool              `json:"closed"`
	Hash                string            `json:"hash"`
	LedgerHash          string            `json:"ledger_hash"`
	LedgerIndex         string            `json:"ledger_index"`
	ParentCloseTime     int64             `json:"parent_close_time"`
	ParentHash          string            `json:"parent_hash"`
	SeqNum              string            `json:"seqNum"`
	TotalCoins          string            `json:"totalCoins"`
	TotalCoinsAlt       string            `json:"total_coins,omitempty"`
	TransactionHash     string            `json:"transaction_hash"`
	Transactions        []json.RawMessage `json:"transactions"`
}

// StateEntryType is a helper struct to determine the type of state entry
type StateEntryType struct {
	LedgerEntryType string `json:"LedgerEntryType"`
}

// AccountRootJSON represents an AccountRoot ledger entry in JSON format
type AccountRootJSON struct {
	LedgerEntryType   string `json:"LedgerEntryType"`
	Account           string `json:"Account"`
	Balance           string `json:"Balance"`
	Flags             uint32 `json:"Flags"`
	OwnerCount        uint32 `json:"OwnerCount"`
	PreviousTxnID     string `json:"PreviousTxnID,omitempty"`
	PreviousTxnLgrSeq uint32 `json:"PreviousTxnLgrSeq,omitempty"`
	Sequence          uint32 `json:"Sequence"`
	Index             string `json:"index"`
}

// AmendmentsJSON represents an Amendments ledger entry in JSON format
type AmendmentsJSON struct {
	LedgerEntryType string   `json:"LedgerEntryType"`
	Amendments      []string `json:"Amendments"`
	Flags           uint32   `json:"Flags"`
	Index           string   `json:"index"`
}

// FeeSettingsJSON represents a FeeSettings ledger entry in JSON format
type FeeSettingsJSON struct {
	LedgerEntryType   string `json:"LedgerEntryType"`
	BaseFee           string `json:"BaseFee"` // Hex string (e.g., "A" for 10)
	Flags             uint32 `json:"Flags"`
	ReferenceFeeUnits uint32 `json:"ReferenceFeeUnits"`
	ReserveBase       uint64 `json:"ReserveBase"`
	ReserveIncrement  uint64 `json:"ReserveIncrement"`
	Index             string `json:"index"`
}

// ParsedGenesisState holds the parsed state from a genesis JSON file
type ParsedGenesisState struct {
	Accounts    []AccountRootJSON
	Amendments  *AmendmentsJSON
	FeeSettings *FeeSettingsJSON
}

// GenesisConfig represents the configuration extracted from a genesis JSON file
// This is used to pass genesis settings to the ledger creation
type GenesisConfig struct {
	// Total XRP in drops
	TotalXRP uint64

	// Close time resolution (10, 20, 30, 60, 90, or 120)
	CloseTimeResolution uint32

	// Fee settings
	BaseFee          drops.XRPAmount
	ReserveBase      drops.XRPAmount
	ReserveIncrement drops.XRPAmount
	UseModernFees    bool

	// Amendments to enable (32-byte hashes)
	Amendments [][32]byte

	// Initial accounts (including genesis account)
	InitialAccounts []InitialAccountConfig
}

// InitialAccountConfig represents an account to create at genesis
type InitialAccountConfig struct {
	Address  string
	Balance  uint64
	Sequence uint32
	Flags    uint32
}

// LoadGenesisJSON loads and parses a genesis JSON file
func LoadGenesisJSON(path string) (*GenesisJSON, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read genesis file: %w", err)
	}

	var genesis GenesisJSON
	if err := json.Unmarshal(data, &genesis); err != nil {
		return nil, fmt.Errorf("failed to parse genesis JSON: %w", err)
	}

	return &genesis, nil
}

// ParseState parses the account state entries from the genesis JSON
func (g *GenesisJSON) ParseState() (*ParsedGenesisState, error) {
	state := &ParsedGenesisState{
		Accounts: make([]AccountRootJSON, 0),
	}

	for i, rawEntry := range g.Ledger.AccountState {
		// First determine the entry type
		var entryType StateEntryType
		if err := json.Unmarshal(rawEntry, &entryType); err != nil {
			return nil, fmt.Errorf("failed to parse entry %d type: %w", i, err)
		}

		switch entryType.LedgerEntryType {
		case "AccountRoot":
			var account AccountRootJSON
			if err := json.Unmarshal(rawEntry, &account); err != nil {
				return nil, fmt.Errorf("failed to parse AccountRoot entry %d: %w", i, err)
			}
			state.Accounts = append(state.Accounts, account)

		case "Amendments":
			var amendments AmendmentsJSON
			if err := json.Unmarshal(rawEntry, &amendments); err != nil {
				return nil, fmt.Errorf("failed to parse Amendments entry %d: %w", i, err)
			}
			state.Amendments = &amendments

		case "FeeSettings":
			var feeSettings FeeSettingsJSON
			if err := json.Unmarshal(rawEntry, &feeSettings); err != nil {
				return nil, fmt.Errorf("failed to parse FeeSettings entry %d: %w", i, err)
			}
			state.FeeSettings = &feeSettings

		default:
			// Unknown entry type - log but don't fail
			// Could be a future entry type we don't support yet
		}
	}

	return state, nil
}

// ToGenesisConfig converts the parsed JSON to a GenesisConfig
func (g *GenesisJSON) ToGenesisConfig() (*GenesisConfig, error) {
	state, err := g.ParseState()
	if err != nil {
		return nil, fmt.Errorf("failed to parse genesis state: %w", err)
	}

	config := &GenesisConfig{
		UseModernFees: true, // Default to modern fees
	}

	// Parse total coins
	totalCoins := g.Ledger.TotalCoins
	if totalCoins == "" {
		totalCoins = g.Ledger.TotalCoinsAlt
	}
	if totalCoins != "" {
		config.TotalXRP, err = strconv.ParseUint(totalCoins, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid totalCoins value: %w", err)
		}
	}

	// Parse close time resolution
	if g.Ledger.CloseTimeResolution > 0 {
		config.CloseTimeResolution = uint32(g.Ledger.CloseTimeResolution)
	}

	// Parse fee settings
	if state.FeeSettings != nil {
		// Parse BaseFee from hex string
		baseFee, err := parseHexFee(state.FeeSettings.BaseFee)
		if err != nil {
			return nil, fmt.Errorf("invalid BaseFee: %w", err)
		}
		config.BaseFee = drops.NewXRPAmount(int64(baseFee))
		config.ReserveBase = drops.NewXRPAmount(int64(state.FeeSettings.ReserveBase))
		config.ReserveIncrement = drops.NewXRPAmount(int64(state.FeeSettings.ReserveIncrement))

		// Detect if using legacy fees (has ReferenceFeeUnits)
		if state.FeeSettings.ReferenceFeeUnits > 0 {
			config.UseModernFees = false
		}
	}

	// Parse amendments
	if state.Amendments != nil && len(state.Amendments.Amendments) > 0 {
		config.Amendments = make([][32]byte, 0, len(state.Amendments.Amendments))
		for _, hexHash := range state.Amendments.Amendments {
			hash, err := parseAmendmentHash(hexHash)
			if err != nil {
				return nil, fmt.Errorf("invalid amendment hash %s: %w", hexHash, err)
			}
			config.Amendments = append(config.Amendments, hash)
		}
	}

	// Parse accounts
	if len(state.Accounts) > 0 {
		config.InitialAccounts = make([]InitialAccountConfig, 0, len(state.Accounts))
		for _, acc := range state.Accounts {
			balance, err := strconv.ParseUint(acc.Balance, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("invalid balance for account %s: %w", acc.Account, err)
			}
			config.InitialAccounts = append(config.InitialAccounts, InitialAccountConfig{
				Address:  acc.Account,
				Balance:  balance,
				Sequence: acc.Sequence,
				Flags:    acc.Flags,
			})
		}
	}

	return config, nil
}

// Validate validates the genesis configuration
func (g *GenesisJSON) Validate() error {
	// Validate total coins
	totalCoins := g.Ledger.TotalCoins
	if totalCoins == "" {
		totalCoins = g.Ledger.TotalCoinsAlt
	}
	if totalCoins != "" {
		total, err := strconv.ParseUint(totalCoins, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid totalCoins: %w", err)
		}
		// 100 billion XRP = 100,000,000,000,000,000 drops
		maxXRP := uint64(100_000_000_000) * 1_000_000
		if total > maxXRP {
			return fmt.Errorf("totalCoins exceeds maximum (100 billion XRP): %d", total)
		}
	}

	// Validate close time resolution
	validResolutions := map[int]bool{10: true, 20: true, 30: true, 60: true, 90: true, 120: true}
	if g.Ledger.CloseTimeResolution > 0 && !validResolutions[g.Ledger.CloseTimeResolution] {
		return fmt.Errorf("invalid close_time_resolution: %d (must be 10, 20, 30, 60, 90, or 120)", g.Ledger.CloseTimeResolution)
	}

	// Parse and validate state entries
	state, err := g.ParseState()
	if err != nil {
		return err
	}

	// Validate fee settings if present
	if state.FeeSettings != nil {
		if _, err := parseHexFee(state.FeeSettings.BaseFee); err != nil {
			return fmt.Errorf("invalid BaseFee: %w", err)
		}
	}

	// Validate amendments if present
	if state.Amendments != nil {
		for _, hexHash := range state.Amendments.Amendments {
			if _, err := parseAmendmentHash(hexHash); err != nil {
				return fmt.Errorf("invalid amendment hash %s: %w", hexHash, err)
			}
		}
	}

	// Validate accounts
	var totalBalance uint64
	for _, acc := range state.Accounts {
		balance, err := strconv.ParseUint(acc.Balance, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid balance for account %s: %w", acc.Account, err)
		}
		totalBalance += balance
	}

	// Check total balance matches total coins
	if totalCoins != "" {
		total, _ := strconv.ParseUint(totalCoins, 10, 64)
		if totalBalance != total {
			return fmt.Errorf("account balances (%d) don't match totalCoins (%d)", totalBalance, total)
		}
	}

	return nil
}

// parseHexFee parses a hex fee string (e.g., "A" or "0A") to uint64
func parseHexFee(hexStr string) (uint64, error) {
	// Remove 0x prefix if present
	hexStr = strings.TrimPrefix(hexStr, "0x")
	hexStr = strings.TrimPrefix(hexStr, "0X")

	// Pad to at least 2 characters
	if len(hexStr) == 1 {
		hexStr = "0" + hexStr
	}

	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return 0, err
	}

	var value uint64
	for _, b := range bytes {
		value = (value << 8) | uint64(b)
	}

	return value, nil
}

// parseAmendmentHash parses a 64-character hex string to a 32-byte hash
func parseAmendmentHash(hexHash string) ([32]byte, error) {
	var hash [32]byte

	if len(hexHash) != 64 {
		return hash, errors.New("amendment hash must be 64 hex characters")
	}

	bytes, err := hex.DecodeString(hexHash)
	if err != nil {
		return hash, err
	}

	copy(hash[:], bytes)
	return hash, nil
}

// DefaultGenesisConfig returns a default genesis configuration matching rippled defaults
func DefaultGenesisConfig() *GenesisConfig {
	return &GenesisConfig{
		TotalXRP:            100_000_000_000 * 1_000_000, // 100 billion XRP
		CloseTimeResolution: 30,
		BaseFee:             drops.NewXRPAmount(10), // 10 drops
		ReserveBase:         drops.DropsPerXRP * 10, // 10 XRP
		ReserveIncrement:    drops.DropsPerXRP * 2,  // 2 XRP
		UseModernFees:       true,
		Amendments:          nil,
		InitialAccounts:     nil, // Will use master passphrase account
	}
}
