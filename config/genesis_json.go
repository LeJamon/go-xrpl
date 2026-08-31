package config

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"os"
	"slices"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
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

// stateEntryType is a helper struct to determine the type of state entry
type stateEntryType struct {
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
	LedgerEntryType       string  `json:"LedgerEntryType"`
	BaseFee               string  `json:"BaseFee"`
	Flags                 uint32  `json:"Flags"`
	ReferenceFeeUnits     uint32  `json:"ReferenceFeeUnits"`
	ReserveBase           uint64  `json:"ReserveBase"`
	ReserveIncrement      uint64  `json:"ReserveIncrement"`
	BaseFeeDrops          *string `json:"BaseFeeDrops"`
	ReserveBaseDrops      *string `json:"ReserveBaseDrops"`
	ReserveIncrementDrops *string `json:"ReserveIncrementDrops"`
	Index                 string  `json:"index"`

	baseFeePresent           bool
	referenceFeeUnitsPresent bool
	reserveBasePresent       bool
	reserveIncrementPresent  bool
}

func (f *FeeSettingsJSON) UnmarshalJSON(data []byte) error {
	type feeSettingsJSON FeeSettingsJSON
	var decoded feeSettingsJSON
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{
		"BaseFee", "ReferenceFeeUnits", "ReserveBase", "ReserveIncrement",
		"BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops",
	} {
		if value, ok := fields[name]; ok && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s must not be null", name)
		}
	}

	*f = FeeSettingsJSON(decoded)
	_, f.baseFeePresent = fields["BaseFee"]
	_, f.referenceFeeUnitsPresent = fields["ReferenceFeeUnits"]
	_, f.reserveBasePresent = fields["ReserveBase"]
	_, f.reserveIncrementPresent = fields["ReserveIncrement"]
	return nil
}

func (f FeeSettingsJSON) MarshalJSON() ([]byte, error) {
	type feeSettingsJSON struct {
		LedgerEntryType       string  `json:"LedgerEntryType"`
		BaseFee               *string `json:"BaseFee,omitempty"`
		Flags                 uint32  `json:"Flags"`
		ReferenceFeeUnits     *uint32 `json:"ReferenceFeeUnits,omitempty"`
		ReserveBase           *uint64 `json:"ReserveBase,omitempty"`
		ReserveIncrement      *uint64 `json:"ReserveIncrement,omitempty"`
		BaseFeeDrops          *string `json:"BaseFeeDrops,omitempty"`
		ReserveBaseDrops      *string `json:"ReserveBaseDrops,omitempty"`
		ReserveIncrementDrops *string `json:"ReserveIncrementDrops,omitempty"`
		Index                 string  `json:"index"`
	}

	wire := feeSettingsJSON{
		LedgerEntryType:       f.LedgerEntryType,
		Flags:                 f.Flags,
		BaseFeeDrops:          f.BaseFeeDrops,
		ReserveBaseDrops:      f.ReserveBaseDrops,
		ReserveIncrementDrops: f.ReserveIncrementDrops,
		Index:                 f.Index,
	}
	legacyPresence := countSet(
		f.baseFeePresent,
		f.referenceFeeUnitsPresent,
		f.reserveBasePresent,
		f.reserveIncrementPresent,
	)
	modernPresence := countSet(f.BaseFeeDrops != nil, f.ReserveBaseDrops != nil, f.ReserveIncrementDrops != nil)
	programmaticLegacy := legacyPresence == 0 && (modernPresence == 0 ||
		f.BaseFee != "" || f.ReferenceFeeUnits != 0 || f.ReserveBase != 0 || f.ReserveIncrement != 0)
	if f.baseFeePresent || programmaticLegacy {
		wire.BaseFee = &f.BaseFee
	}
	if f.referenceFeeUnitsPresent || programmaticLegacy {
		wire.ReferenceFeeUnits = &f.ReferenceFeeUnits
	}
	if f.reserveBasePresent || programmaticLegacy {
		wire.ReserveBase = &f.ReserveBase
	}
	if f.reserveIncrementPresent || programmaticLegacy {
		wire.ReserveIncrement = &f.ReserveIncrement
	}
	return json.Marshal(wire)
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
		var entryType stateEntryType
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
			// Unknown entry type — could be a future type we don't
			// support yet. Skip it, but tell the operator the entry
			// was dropped rather than silently losing genesis state.
			slog.Warn("genesis: skipping unsupported account_state entry",
				"index", i, "ledger_entry_type", entryType.LedgerEntryType)
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

	config := &GenesisConfig{}

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

	if g.Ledger.CloseTimeResolution > 0 {
		config.CloseTimeResolution = uint32(g.Ledger.CloseTimeResolution)
	}

	config.Amendments, err = parseAmendments(state.Amendments)
	if err != nil {
		return nil, err
	}

	if state.FeeSettings != nil {
		config.BaseFee, config.ReserveBase, config.ReserveIncrement, err =
			state.FeeSettings.fees(slices.Contains(config.Amendments, amendment.FeatureXRPFees))
		if err != nil {
			return nil, err
		}
	}

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

	amendments, err := parseAmendments(state.Amendments)
	if err != nil {
		return err
	}

	if state.FeeSettings != nil {
		if _, _, _, err := state.FeeSettings.fees(slices.Contains(amendments, amendment.FeatureXRPFees)); err != nil {
			return err
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
	if hexStr == "" {
		return 0, errors.New("empty hexadecimal value")
	}
	for _, digit := range hexStr {
		if !((digit >= '0' && digit <= '9') || (digit >= 'a' && digit <= 'f') || (digit >= 'A' && digit <= 'F')) {
			return 0, fmt.Errorf("invalid hexadecimal value %q", hexStr)
		}
	}
	return strconv.ParseUint(hexStr, 16, 64)
}

func (f *FeeSettingsJSON) fees(xrpFees bool) (drops.XRPAmount, drops.XRPAmount, drops.XRPAmount, error) {
	legacyFields := countSet(
		f.baseFeePresent,
		f.referenceFeeUnitsPresent,
		f.reserveBasePresent,
		f.reserveIncrementPresent,
	)
	modernFields := countSet(f.BaseFeeDrops != nil, f.ReserveBaseDrops != nil, f.ReserveIncrementDrops != nil)

	if legacyFields > 0 && modernFields > 0 {
		return 0, 0, 0, errors.New("mixed legacy and modern fee settings")
	}
	if xrpFees {
		if modernFields != 3 {
			return 0, 0, 0, errors.New("XRPFees requires complete modern fee settings")
		}
		baseFee, err := parseNativeFee("BaseFeeDrops", *f.BaseFeeDrops)
		if err != nil {
			return 0, 0, 0, err
		}
		reserveBase, err := parseNativeFee("ReserveBaseDrops", *f.ReserveBaseDrops)
		if err != nil {
			return 0, 0, 0, err
		}
		reserveIncrement, err := parseNativeFee("ReserveIncrementDrops", *f.ReserveIncrementDrops)
		if err != nil {
			return 0, 0, 0, err
		}
		return baseFee, reserveBase, reserveIncrement, nil
	}

	if modernFields > 0 {
		return 0, 0, 0, errors.New("modern fee settings require the XRPFees amendment")
	}
	if legacyFields != 4 {
		return 0, 0, 0, errors.New("complete legacy fee settings are required")
	}
	baseFee, err := parseHexFee(f.BaseFee)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid BaseFee: %w", err)
	}
	if baseFee > math.MaxInt64 {
		return 0, 0, 0, fmt.Errorf("BaseFee out of range: %d", baseFee)
	}
	if f.ReserveBase > uint64(^uint32(0)) {
		return 0, 0, 0, fmt.Errorf("ReserveBase out of range: %d", f.ReserveBase)
	}
	if f.ReserveIncrement > uint64(^uint32(0)) {
		return 0, 0, 0, fmt.Errorf("ReserveIncrement out of range: %d", f.ReserveIncrement)
	}
	return drops.NewXRPAmount(int64(baseFee)),
		drops.NewXRPAmount(int64(f.ReserveBase)),
		drops.NewXRPAmount(int64(f.ReserveIncrement)), nil
}

func countSet(values ...bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func parseNativeFee(name, value string) (drops.XRPAmount, error) {
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if strconv.FormatUint(parsed, 10) != value {
		return 0, fmt.Errorf("invalid %s: non-canonical decimal value %q", name, value)
	}
	if parsed > uint64(drops.MaxDrops) {
		return 0, fmt.Errorf("%s out of range: %s", name, value)
	}
	return drops.NewXRPAmount(int64(parsed)), nil
}

func parseAmendments(entry *AmendmentsJSON) ([][32]byte, error) {
	if entry == nil || len(entry.Amendments) == 0 {
		return nil, nil
	}
	amendments := make([][32]byte, 0, len(entry.Amendments))
	for _, hexHash := range entry.Amendments {
		hash, err := parseAmendmentHash(hexHash)
		if err != nil {
			return nil, fmt.Errorf("invalid amendment hash %s: %w", hexHash, err)
		}
		amendments = append(amendments, hash)
	}
	return amendments, nil
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

// DefaultGenesisConfig returns a default genesis configuration matching rippled defaults.
// Fee format is derived from amendments: legacy when XRPFees is absent, modern when present.
func DefaultGenesisConfig() *GenesisConfig {
	return &GenesisConfig{
		TotalXRP:            100_000_000_000 * 1_000_000, // 100 billion XRP
		CloseTimeResolution: 30,
		BaseFee:             drops.NewXRPAmount(10), // 10 drops
		ReserveBase:         drops.DropsPerXRP * 10, // 10 XRP
		ReserveIncrement:    drops.DropsPerXRP * 2,  // 2 XRP
		Amendments:          nil,
		InitialAccounts:     nil, // Will use master passphrase account
	}
}
