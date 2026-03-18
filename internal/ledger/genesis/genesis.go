package genesis

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	binarycodec "github.com/LeJamon/goXRPLd/codec/binarycodec"
	"github.com/LeJamon/goXRPLd/crypto/common"
	secp256k1 "github.com/LeJamon/goXRPLd/crypto/secp256k1"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/header"
	"github.com/LeJamon/goXRPLd/keylet"
	ledgerentries "github.com/LeJamon/goXRPLd/ledger/entry"
	"github.com/LeJamon/goXRPLd/protocol"
	"github.com/LeJamon/goXRPLd/shamap"
)

const (
	// InitialXRP is the total XRP in existence (100 billion XRP in drops)
	InitialXRP = 100_000_000_000 * 1_000_000

	// GenesisTimeResolution is the close time resolution for the genesis ledger.
	// Reference: rippled LedgerTiming.h ledgerGenesisTimeResolution = ledgerPossibleTimeResolutions[0] = 10s
	GenesisTimeResolution = 10

	// GenesisLedgerSequence is the sequence number of the genesis ledger
	GenesisLedgerSequence = 1

	// MasterPassphrase is the passphrase used to derive the genesis account
	MasterPassphrase = "masterpassphrase"
)

// DefaultFees defines the default fee configuration for genesis
type DefaultFees struct {
	BaseFee          drops.XRPAmount
	ReserveBase      drops.XRPAmount
	ReserveIncrement drops.XRPAmount
}

// StandardFees returns the standard XRPL fee configuration
func StandardFees() DefaultFees {
	return DefaultFees{
		BaseFee:          drops.NewXRPAmount(10), // 10 drops
		ReserveBase:      drops.DropsPerXRP * 10, // 10 XRP
		ReserveIncrement: drops.DropsPerXRP * 2,  // 2 XRP
	}
}

// Config holds the configuration for genesis ledger creation
type Config struct {
	// TotalXRP is the total XRP supply in drops (default: 100 billion XRP)
	TotalXRP uint64

	// MasterPassphrase is the passphrase to derive the genesis account from
	// If empty, uses "masterpassphrase" (produces rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh)
	MasterPassphrase string

	// CloseTimeResolution is the close time resolution in seconds (default: 30)
	// Valid values: 10, 20, 30, 60, 90, 120
	CloseTimeResolution uint32

	// Fees to use in the genesis ledger
	Fees DefaultFees

	// Amendments to enable at genesis (empty for standard genesis)
	Amendments [][32]byte

	// InitialAccounts specifies additional accounts to create at genesis
	// The balance for these is deducted from the genesis account
	InitialAccounts []InitialAccount
}

// InitialAccount represents an account to create at genesis
type InitialAccount struct {
	Address  string
	Balance  uint64
	Sequence uint32
	Flags    uint32
}

// DefaultGenesisAmendments returns the amendment IDs to include in the genesis
// ledger. This matches rippled's behavior of enabling all non-vetoed amendments
// at genesis via getDesired() (AmendmentTable.h:134-139).
func DefaultGenesisAmendments() [][32]byte {
	features := amendment.DefaultYesFeatures()
	ids := make([][32]byte, len(features))
	for i, f := range features {
		ids[i] = f.ID
	}
	// Sort by hash to match rippled's std::map<uint256> ordering
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids
}

// DefaultConfig returns the default genesis configuration.
// Fee format (modern vs legacy) is automatically derived from whether
// the XRPFees amendment is in the Amendments list, matching rippled's
// genesis behavior (Ledger.cpp:168-229).
func DefaultConfig() Config {
	return Config{
		TotalXRP:            InitialXRP,
		MasterPassphrase:    MasterPassphrase,
		CloseTimeResolution: GenesisTimeResolution,
		Fees:                StandardFees(),
		Amendments:          DefaultGenesisAmendments(),
		InitialAccounts:     nil,
	}
}

// hasXRPFeesAmendment checks if the XRPFees amendment is in the amendments list.
// This determines whether to use modern (Amount) or legacy (UInt32/UInt64) fee fields.
// Reference: rippled Ledger.cpp checks for featureXRPFees in the amendments vector.
func hasXRPFeesAmendment(amendments [][32]byte) bool {
	for _, a := range amendments {
		if a == amendment.FeatureXRPFees {
			return true
		}
	}
	return false
}

// GenesisLedger represents a freshly created genesis ledger
type GenesisLedger struct {
	Header         header.LedgerHeader
	StateMap       *shamap.SHAMap
	TxMap          *shamap.SHAMap
	GenesisAccount [20]byte
	GenesisAddress string
}

// GenerateGenesisAccountID derives the genesis account ID from the default master passphrase.
// This produces the well-known genesis account: rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh
func GenerateGenesisAccountID() ([20]byte, string, error) {
	return GenerateAccountIDFromPassphrase(MasterPassphrase)
}

// GenerateAccountIDFromPassphrase derives an account ID from a passphrase.
func GenerateAccountIDFromPassphrase(passphrase string) ([20]byte, string, error) {
	// Generate seed from passphrase using SHA512-Half
	seedHash := common.Sha512Half([]byte(passphrase))
	seed := seedHash[:16] // Use first 16 bytes as seed

	// Derive keypair using secp256k1
	algo := secp256k1.SECP256K1()
	_, pubKeyHex, err := algo.DeriveKeypair(seed, false)
	if err != nil {
		return [20]byte{}, "", err
	}

	// Generate classic address from public key
	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		return [20]byte{}, "", err
	}

	// Decode back to get the 20-byte account ID
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return [20]byte{}, "", err
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)

	return accountID, address, nil
}

// DecodeAddress decodes an XRPL address to a 20-byte account ID
func DecodeAddress(address string) ([20]byte, error) {
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return [20]byte{}, err
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)
	return accountID, nil
}

// Create creates a new genesis ledger with the given configuration.
func Create(cfg Config) (*GenesisLedger, error) {
	// Apply defaults for zero values
	totalXRP := cfg.TotalXRP
	if totalXRP == 0 {
		totalXRP = InitialXRP
	}

	passphrase := cfg.MasterPassphrase
	if passphrase == "" {
		passphrase = MasterPassphrase
	}

	closeTimeRes := cfg.CloseTimeResolution
	if closeTimeRes == 0 {
		closeTimeRes = GenesisTimeResolution
	}

	// Generate genesis account from passphrase
	accountID, address, err := GenerateAccountIDFromPassphrase(passphrase)
	if err != nil {
		return nil, errors.New("failed to generate genesis account: " + err.Error())
	}

	// Create state map (account state tree)
	stateMap, err := shamap.New(shamap.TypeState)
	if err != nil {
		return nil, errors.New("failed to create state map: " + err.Error())
	}

	// Create transaction map (empty for genesis)
	txMap, err := shamap.New(shamap.TypeTransaction)
	if err != nil {
		return nil, errors.New("failed to create transaction map: " + err.Error())
	}

	// Calculate genesis account balance (total XRP minus initial accounts)
	genesisBalance := totalXRP
	for _, acc := range cfg.InitialAccounts {
		if acc.Balance > genesisBalance {
			return nil, errors.New("initial accounts balance exceeds total XRP")
		}
		genesisBalance -= acc.Balance
	}

	// 1. Create genesis account with remaining XRP
	if err := createGenesisAccountWithBalance(stateMap, accountID, genesisBalance); err != nil {
		return nil, errors.New("failed to create genesis account: " + err.Error())
	}

	// 2. Create initial accounts if specified
	for _, acc := range cfg.InitialAccounts {
		accID, err := DecodeAddress(acc.Address)
		if err != nil {
			return nil, errors.New("failed to decode address " + acc.Address + ": " + err.Error())
		}
		if err := createInitialAccount(stateMap, accID, acc.Balance, acc.Sequence, acc.Flags); err != nil {
			return nil, errors.New("failed to create account " + acc.Address + ": " + err.Error())
		}
	}

	// 3. Create fee settings
	if err := createFeeSettings(stateMap, cfg); err != nil {
		return nil, errors.New("failed to create fee settings: " + err.Error())
	}

	// 4. Create amendments if specified
	if len(cfg.Amendments) > 0 {
		if err := createAmendments(stateMap, cfg.Amendments); err != nil {
			return nil, errors.New("failed to create amendments: " + err.Error())
		}
	}

	// Make state map immutable
	if err := stateMap.SetImmutable(); err != nil {
		return nil, errors.New("failed to make state map immutable: " + err.Error())
	}

	// Make tx map immutable
	if err := txMap.SetImmutable(); err != nil {
		return nil, errors.New("failed to make tx map immutable: " + err.Error())
	}

	// Get hashes
	accountHash, err := stateMap.Hash()
	if err != nil {
		return nil, errors.New("failed to get state map hash: " + err.Error())
	}

	txHash, err := txMap.Hash()
	if err != nil {
		return nil, errors.New("failed to get tx map hash: " + err.Error())
	}

	// Create ledger header
	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         GenesisLedgerSequence,
		ParentCloseTime:     time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		CloseTime:           time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		CloseTimeResolution: closeTimeRes,
		CloseFlags:          0,
		ParentHash:          [32]byte{}, // Genesis has no parent
		TxHash:              txHash,
		AccountHash:         accountHash,
		Drops:               totalXRP,
		Validated:           true,
		Accepted:            true,
	}

	// Calculate ledger hash
	ledgerHeader.Hash = CalculateLedgerHash(ledgerHeader)

	return &GenesisLedger{
		Header:         ledgerHeader,
		StateMap:       stateMap,
		TxMap:          txMap,
		GenesisAccount: accountID,
		GenesisAddress: address,
	}, nil
}

// createGenesisAccountWithBalance creates the genesis account entry with a specific balance.
func createGenesisAccountWithBalance(stateMap *shamap.SHAMap, accountID [20]byte, balance uint64) error {
	// Create account root entry
	account := &ledgerentries.AccountRoot{
		Account:    accountID,
		Sequence:   1,
		Balance:    balance,
		OwnerCount: 0,
	}

	// Serialize the account entry
	data, err := SerializeAccountRoot(account)
	if err != nil {
		return err
	}

	// Get the keylet for this account
	k := keylet.Account(accountID)

	// Add to state map
	return stateMap.Put(k.Key, data)
}

// createInitialAccount creates an additional account at genesis.
func createInitialAccount(stateMap *shamap.SHAMap, accountID [20]byte, balance uint64, sequence uint32, flags uint32) error {
	// Use sequence 1 if not specified
	if sequence == 0 {
		sequence = 1
	}

	// Create account root entry
	account := &ledgerentries.AccountRoot{
		BaseEntry: ledgerentries.BaseEntry{
			Flags: flags,
		},
		Account:    accountID,
		Sequence:   sequence,
		Balance:    balance,
		OwnerCount: 0,
	}

	// Serialize the account entry
	data, err := SerializeAccountRoot(account)
	if err != nil {
		return err
	}

	// Get the keylet for this account
	k := keylet.Account(accountID)

	// Add to state map
	return stateMap.Put(k.Key, data)
}

// createFeeSettings creates the fee settings entry.
// The fee format is derived from the amendments list: modern (Amount fields)
// if XRPFees is present, legacy (UInt32/UInt64) otherwise.
// Reference: rippled Ledger.cpp checks featureXRPFees in the amendments vector.
func createFeeSettings(stateMap *shamap.SHAMap, cfg Config) error {
	var feeSettings *ledgerentries.FeeSettings

	if hasXRPFeesAmendment(cfg.Amendments) {
		feeSettings = ledgerentries.NewFeeSettings(
			cfg.Fees.BaseFee,
			cfg.Fees.ReserveBase,
			cfg.Fees.ReserveIncrement,
		)
	} else {
		feeSettings = ledgerentries.NewLegacyFeeSettings(
			uint64(cfg.Fees.BaseFee.Drops()),
			10, // ReferenceFeeUnits (deprecated)
			uint32(cfg.Fees.ReserveBase.Drops()),
			uint32(cfg.Fees.ReserveIncrement.Drops()),
		)
	}

	// Serialize the fee settings entry
	data, err := serializeFeeSettings(feeSettings)
	if err != nil {
		return err
	}

	// Get the keylet for fee settings
	k := keylet.Fees()

	// Add to state map
	return stateMap.Put(k.Key, data)
}

// createAmendments creates the amendments entry with the specified amendments.
func createAmendments(stateMap *shamap.SHAMap, amendments [][32]byte) error {
	// Serialize amendments
	data, err := serializeAmendments(amendments)
	if err != nil {
		return err
	}

	// Get the keylet for amendments
	k := keylet.Amendments()

	// Add to state map
	return stateMap.Put(k.Key, data)
}

// CalculateLedgerHash computes the hash of a ledger header.
// This matches rippled's calculateLedgerHash function.
func CalculateLedgerHash(h header.LedgerHeader) [32]byte {
	// Build the data to hash
	// Format: prefix + seq + drops + parentHash + txHash + accountHash +
	//         parentCloseTime + closeTime + closeTimeRes + closeFlags

	var data []byte

	// Hash prefix for ledger master
	data = append(data, protocol.HashPrefixLedgerMaster.Bytes()...)

	// Sequence (uint32, big-endian)
	seqBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(seqBytes, h.LedgerIndex)
	data = append(data, seqBytes...)

	// Total drops (uint64, big-endian)
	dropsBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(dropsBytes, h.Drops)
	data = append(data, dropsBytes...)

	// Parent hash (32 bytes)
	data = append(data, h.ParentHash[:]...)

	// Transaction hash (32 bytes)
	data = append(data, h.TxHash[:]...)

	// Account hash (32 bytes)
	data = append(data, h.AccountHash[:]...)

	// Parent close time (uint32, seconds since ripple epoch - Jan 1, 2000)
	const rippleEpochUnix int64 = 946684800
	parentCloseBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(parentCloseBytes, uint32(h.ParentCloseTime.Unix()-rippleEpochUnix))
	data = append(data, parentCloseBytes...)

	// Close time (uint32, seconds since ripple epoch - Jan 1, 2000)
	closeTimeBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(closeTimeBytes, uint32(h.CloseTime.Unix()-rippleEpochUnix))
	data = append(data, closeTimeBytes...)

	// Close time resolution (uint8)
	data = append(data, byte(h.CloseTimeResolution))

	// Close flags (uint8)
	data = append(data, h.CloseFlags)

	return common.Sha512Half(data)
}

// serializeAccountRoot serializes an AccountRoot entry to bytes using the XRPL binary codec.
func SerializeAccountRoot(a *ledgerentries.AccountRoot) ([]byte, error) {
	// Convert account ID to classic address
	address, err := addresscodec.EncodeAccountIDToClassicAddress(a.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}

	// Build the JSON representation for the binary codec
	jsonObj := map[string]any{
		"LedgerEntryType":   "AccountRoot",
		"Account":           address,
		"Balance":           fmt.Sprintf("%d", a.Balance), // XRP balance as drops string
		"Sequence":          a.Sequence,
		"OwnerCount":        a.OwnerCount,
		"Flags":             a.Flags,
		"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
		"PreviousTxnLgrSeq": uint32(0),
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode AccountRoot: %w", err)
	}

	// Convert hex string to bytes
	return hex.DecodeString(hexStr)
}

// serializeFeeSettings serializes a FeeSettings entry to bytes using the XRPL binary codec.
func serializeFeeSettings(f *ledgerentries.FeeSettings) ([]byte, error) {
	// Build the JSON representation for the binary codec
	jsonObj := map[string]any{
		"LedgerEntryType": "FeeSettings",
		"Flags":           uint32(0),
	}

	if f.IsUsingModernFees() {
		// Modern format (XRPFees amendment) - uses Amount fields
		jsonObj["BaseFeeDrops"] = fmt.Sprintf("%d", f.BaseFeeDrops)
		jsonObj["ReserveBaseDrops"] = fmt.Sprintf("%d", f.ReserveBaseDrops)
		jsonObj["ReserveIncrementDrops"] = fmt.Sprintf("%d", f.ReserveIncrementDrops)
	} else {
		// Legacy format - uses UInt64/UInt32 fields
		// UInt64 fields (BaseFee) must be hex strings without leading zeros
		if f.BaseFee != nil {
			jsonObj["BaseFee"] = fmt.Sprintf("%x", *f.BaseFee)
		}
		if f.ReferenceFeeUnits != nil {
			jsonObj["ReferenceFeeUnits"] = *f.ReferenceFeeUnits
		}
		if f.ReserveBase != nil {
			jsonObj["ReserveBase"] = *f.ReserveBase
		}
		if f.ReserveIncrement != nil {
			jsonObj["ReserveIncrement"] = *f.ReserveIncrement
		}
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode FeeSettings: %w", err)
	}

	// Convert hex string to bytes
	return hex.DecodeString(hexStr)
}

// serializeAmendments serializes an amendments list to bytes using the XRPL binary codec.
func serializeAmendments(amendments [][32]byte) ([]byte, error) {
	// Convert amendment hashes to hex strings for Vector256
	amendmentHexes := make([]string, len(amendments))
	for i, amendment := range amendments {
		amendmentHexes[i] = fmt.Sprintf("%064X", amendment)
	}

	// Build the JSON representation for the binary codec
	jsonObj := map[string]any{
		"LedgerEntryType": "Amendments",
		"Flags":           uint32(0),
		"Amendments":      amendmentHexes,
	}

	// Encode using the binary codec
	hexStr, err := binarycodec.Encode(jsonObj)
	if err != nil {
		return nil, fmt.Errorf("failed to encode Amendments: %w", err)
	}

	// Convert hex string to bytes
	return hex.DecodeString(hexStr)
}
