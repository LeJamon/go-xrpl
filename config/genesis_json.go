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
	"regexp"
	"slices"
	"strconv"
	"strings"

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
	LedgerIndex           string  `json:"LedgerIndex,omitempty"`
	Sponsor               string  `json:"Sponsor,omitempty"`
	PreviousTxnID         string  `json:"PreviousTxnID,omitempty"`
	PreviousTxnLgrSeq     *uint32 `json:"PreviousTxnLgrSeq,omitempty"`
	Index                 string  `json:"index"`

	baseFeePresent           bool
	referenceFeeUnitsPresent bool
	reserveBasePresent       bool
	reserveIncrementPresent  bool
}

// UnmarshalJSON decodes a FeeSettings entry using exact XRPL field names.
func (f *FeeSettingsJSON) UnmarshalJSON(data []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("FeeSettings must be a JSON object")
	}
	for name, value := range fields {
		switch name {
		case "LedgerEntryType", "BaseFee", "Flags", "ReferenceFeeUnits", "ReserveBase",
			"ReserveIncrement", "BaseFeeDrops", "ReserveBaseDrops", "ReserveIncrementDrops",
			"LedgerIndex", "Sponsor", "PreviousTxnID", "PreviousTxnLgrSeq", "index":
		default:
			return fmt.Errorf("unknown FeeSettings field %q", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("%s must not be null", name)
		}
	}

	var decoded FeeSettingsJSON
	if err := decodeJSONField(fields, "LedgerEntryType", &decoded.LedgerEntryType); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "index", &decoded.Index); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "LedgerIndex", &decoded.LedgerIndex); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "Sponsor", &decoded.Sponsor); err != nil {
		return err
	}
	if err := decodeJSONField(fields, "PreviousTxnID", &decoded.PreviousTxnID); err != nil {
		return err
	}
	if value, present, err := parseJSONUnsigned(fields, "PreviousTxnLgrSeq", 10, 32); err != nil {
		return err
	} else if present {
		sequence := uint32(value)
		decoded.PreviousTxnLgrSeq = &sequence
	}
	if value, present, err := parseJSONUnsigned(fields, "Flags", 10, 32); err != nil {
		return err
	} else if present {
		decoded.Flags = uint32(value)
	}
	if value, present, err := parseJSONUnsigned(fields, "BaseFee", 16, 64); err != nil {
		return err
	} else if present {
		decoded.BaseFee = strconv.FormatUint(value, 16)
	}
	if value, present, err := parseJSONUnsigned(fields, "ReferenceFeeUnits", 10, 32); err != nil {
		return err
	} else if present {
		decoded.ReferenceFeeUnits = uint32(value)
	}
	if value, present, err := parseJSONUnsigned(fields, "ReserveBase", 10, 64); err != nil {
		return err
	} else if present {
		decoded.ReserveBase = value
	}
	if value, present, err := parseJSONUnsigned(fields, "ReserveIncrement", 10, 64); err != nil {
		return err
	} else if present {
		decoded.ReserveIncrement = value
	}
	for name, target := range map[string]**string{
		"BaseFeeDrops":          &decoded.BaseFeeDrops,
		"ReserveBaseDrops":      &decoded.ReserveBaseDrops,
		"ReserveIncrementDrops": &decoded.ReserveIncrementDrops,
	} {
		value, present := fields[name]
		if !present {
			continue
		}
		amount, err := parseNativeFeeJSON(name, value)
		if err != nil {
			return err
		}
		canonical := strconv.FormatInt(int64(amount), 10)
		*target = &canonical
	}

	*f = decoded
	_, f.baseFeePresent = fields["BaseFee"]
	_, f.referenceFeeUnitsPresent = fields["ReferenceFeeUnits"]
	_, f.reserveBasePresent = fields["ReserveBase"]
	_, f.reserveIncrementPresent = fields["ReserveIncrement"]
	return nil
}

// MarshalJSON encodes only the selected FeeSettings schema in canonical form.
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
		LedgerIndex           string  `json:"LedgerIndex,omitempty"`
		Sponsor               string  `json:"Sponsor,omitempty"`
		PreviousTxnID         string  `json:"PreviousTxnID,omitempty"`
		PreviousTxnLgrSeq     *uint32 `json:"PreviousTxnLgrSeq,omitempty"`
		Index                 string  `json:"index"`
	}

	wire := feeSettingsJSON{
		LedgerEntryType:   f.LedgerEntryType,
		Flags:             f.Flags,
		LedgerIndex:       f.LedgerIndex,
		Sponsor:           f.Sponsor,
		PreviousTxnID:     f.PreviousTxnID,
		PreviousTxnLgrSeq: f.PreviousTxnLgrSeq,
		Index:             f.Index,
	}
	for name, source := range map[string]*string{
		"BaseFeeDrops":          f.BaseFeeDrops,
		"ReserveBaseDrops":      f.ReserveBaseDrops,
		"ReserveIncrementDrops": f.ReserveIncrementDrops,
	} {
		if source == nil {
			continue
		}
		amount, err := parseNativeFee(name, *source)
		if err != nil {
			return nil, err
		}
		canonical := strconv.FormatInt(int64(amount), 10)
		switch name {
		case "BaseFeeDrops":
			wire.BaseFeeDrops = &canonical
		case "ReserveBaseDrops":
			wire.ReserveBaseDrops = &canonical
		case "ReserveIncrementDrops":
			wire.ReserveIncrementDrops = &canonical
		}
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
		baseFee, err := parseHexFee(f.BaseFee)
		if err != nil {
			return nil, fmt.Errorf("invalid BaseFee: %w", err)
		}
		canonical := strconv.FormatUint(baseFee, 16)
		wire.BaseFee = &canonical
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
		for _, fee := range []struct {
			name   string
			amount drops.XRPAmount
		}{
			{"BaseFeeDrops", baseFee},
			{"ReserveBaseDrops", reserveBase},
			{"ReserveIncrementDrops", reserveIncrement},
		} {
			if fee.amount < 0 {
				return 0, 0, 0, fmt.Errorf("%s out of range: %d must be nonnegative", fee.name, fee.amount)
			}
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
	if baseFee > uint64(drops.MaxDrops) {
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
	return parseNativeFeeJSON(name, json.RawMessage(strconv.Quote(value)))
}

var nativeFeeNumberPattern = regexp.MustCompile(
	`^([-+]?)(0|[1-9][0-9]*)(\.([0-9]+))?([eE]([+-]?)([0-9]+))?$`,
)

func parseNativeFeeJSON(name string, value json.RawMessage) (drops.XRPAmount, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}

	bareNumber := false
	var amountValue any
	switch decoded := decoded.(type) {
	case string:
		elements := splitNativeAmountString(decoded)
		if len(elements) > 3 {
			return 0, fmt.Errorf("invalid %s: invalid amount string", name)
		}
		amountValue = elements[0]
		if len(elements) > 1 && elements[1] != "" && elements[1] != "XRP" {
			return 0, fmt.Errorf("invalid %s: non-native amount", name)
		}
	case []any:
		amountValue = json.Number("0")
		if len(decoded) > 0 {
			amountValue = decoded[0]
		}
		if len(decoded) > 1 {
			if currency, ok := decoded[1].(string); ok && currency != "" && currency != "XRP" {
				return 0, fmt.Errorf("invalid %s: non-native amount", name)
			}
		}
	case json.Number:
		bareNumber = true
		amountValue = decoded
	default:
		return 0, fmt.Errorf("invalid %s: invalid amount type", name)
	}

	parts, err := parseNativeFeeParts(amountValue, bareNumber)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: %w", name, err)
	}
	if parts.exponent < 0 {
		return 0, fmt.Errorf("invalid %s: fractional native amount", name)
	}
	if parts.mantissa == 0 {
		return 0, nil
	}
	if parts.exponent > 17 {
		return 0, fmt.Errorf("%s out of range: %s", name, bytes.TrimSpace(value))
	}
	amount := parts.mantissa
	for exponent := int64(0); exponent < parts.exponent; exponent++ {
		if amount > uint64(drops.MaxDrops)/10 {
			return 0, fmt.Errorf("%s out of range: %s", name, bytes.TrimSpace(value))
		}
		amount *= 10
	}
	if amount > uint64(drops.MaxDrops) {
		return 0, fmt.Errorf("%s out of range: %s", name, bytes.TrimSpace(value))
	}
	parsed := int64(amount)
	if parts.negative {
		parsed = -parsed
	}
	return drops.NewXRPAmount(parsed), nil
}

type nativeFeeParts struct {
	mantissa uint64
	exponent int64
	negative bool
}

func parseNativeFeeParts(value any, bareNumber bool) (nativeFeeParts, error) {
	switch value := value.(type) {
	case json.Number:
		raw := value.String()
		if strings.ContainsAny(raw, ".eE") {
			return nativeFeeParts{}, errors.New("invalid amount type")
		}
		integer, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || integer < math.MinInt32 ||
			(integer > math.MaxInt32 && (!bareNumber || integer > math.MaxUint32)) {
			return nativeFeeParts{}, errors.New("invalid amount type")
		}
		if integer < 0 {
			return nativeFeeParts{mantissa: uint64(-integer), negative: true}, nil
		}
		return nativeFeeParts{mantissa: uint64(integer)}, nil
	case string:
		matches := nativeFeeNumberPattern.FindStringSubmatch(value)
		if matches == nil {
			return nativeFeeParts{}, fmt.Errorf("%q is not a number", value)
		}
		digits := matches[2] + matches[4]
		mantissa, err := strconv.ParseUint(digits, 10, 64)
		if err != nil {
			return nativeFeeParts{}, fmt.Errorf("%q is not a number", value)
		}
		exponent := -int64(len(matches[4]))
		if matches[7] != "" {
			adjustment, err := strconv.ParseInt(matches[7], 10, 32)
			if err != nil {
				return nativeFeeParts{}, fmt.Errorf("%q is not a number", value)
			}
			if matches[6] == "-" {
				exponent -= adjustment
			} else {
				exponent += adjustment
			}
		}
		return nativeFeeParts{mantissa: mantissa, exponent: exponent, negative: matches[1] == "-"}, nil
	default:
		return nativeFeeParts{}, errors.New("invalid amount type")
	}
}

func splitNativeAmountString(value string) []string {
	elements := []string{}
	start := 0
	for index, char := range value {
		if strings.ContainsRune("\t\n\r ,/", char) {
			elements = append(elements, value[start:index])
			start = index + 1
		}
	}
	return append(elements, value[start:])
}

func parseJSONUnsigned(fields map[string]json.RawMessage, name string, stringBase, bitSize int) (uint64, bool, error) {
	raw, present := fields[name]
	if !present {
		return 0, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return 0, true, fmt.Errorf("invalid %s: %w", name, err)
	}
	var value string
	base := 10
	numeric := false
	switch decoded := decoded.(type) {
	case string:
		value = decoded
		base = stringBase
	case json.Number:
		numeric = true
		value = decoded.String()
		if strings.ContainsAny(value, ".eE") {
			return 0, true, fmt.Errorf("invalid %s: expected an unsigned integer", name)
		}
	default:
		return 0, true, fmt.Errorf("invalid %s: expected a string or unsigned integer", name)
	}
	parsed, err := strconv.ParseUint(value, base, bitSize)
	if err != nil {
		return 0, true, fmt.Errorf("invalid %s: %w", name, err)
	}
	if numeric && parsed > math.MaxUint32 {
		return 0, true, fmt.Errorf("%s out of range: %s", name, value)
	}
	return parsed, true, nil
}

func decodeJSONField(fields map[string]json.RawMessage, name string, target any) error {
	raw, present := fields[name]
	if !present {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("invalid %s: %w", name, err)
	}
	return nil
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
