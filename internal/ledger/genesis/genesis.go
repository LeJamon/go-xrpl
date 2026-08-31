package genesis

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	secp256k1 "github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	// InitialXRP is the total XRP in existence (100 billion XRP in drops)
	InitialXRP = 100_000_000_000 * 1_000_000

	// GenesisTimeResolution is the genesis close-time resolution (10s, smallest valid).
	GenesisTimeResolution = 10

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
		BaseFee:          drops.NewXRPAmount(10),
		ReserveBase:      drops.DropsPerXRP * 10,
		ReserveIncrement: drops.DropsPerXRP * 2,
	}
}

// Config holds the configuration for genesis ledger creation
type Config struct {
	// TotalXRP is the total XRP supply in drops (default: 100 billion XRP)
	TotalXRP uint64

	// MasterPassphrase derives the genesis account; empty uses "masterpassphrase"
	// (produces rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh).
	MasterPassphrase string

	// CloseTimeResolution in seconds (default 30; valid: 10, 20, 30, 60, 90, 120).
	CloseTimeResolution uint32

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

type validatedInitialAccount struct {
	InitialAccount
	id [20]byte
}

// DefaultGenesisAmendments returns all non-vetoed amendment IDs for the genesis ledger.
func DefaultGenesisAmendments() [][32]byte {
	features := amendment.DefaultYesFeatures()
	ids := make([][32]byte, len(features))
	for i, f := range features {
		ids[i] = f.ID
	}
	// Sort by hash to match rippled's amendment ordering.
	sort.Slice(ids, func(i, j int) bool {
		return bytes.Compare(ids[i][:], ids[j][:]) < 0
	})
	return ids
}

// DefaultConfig returns the default genesis configuration. Fee format (modern vs
// legacy) is derived from whether XRPFees is in the Amendments list.
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

// hasXRPFeesAmendment reports whether XRPFees is present (selects modern vs legacy fee fields).
func hasXRPFeesAmendment(amendments [][32]byte) bool {
	return slices.Contains(amendments, amendment.FeatureXRPFees)
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
	seedHash := sha512half.Sum([]byte(passphrase))
	seed := seedHash[:16]

	algo := secp256k1.Algorithm{}
	_, pubKeyHex, err := algo.DeriveKeypair(seed, false)
	if err != nil {
		return [20]byte{}, "", err
	}

	address, err := addresscodec.EncodeClassicAddressFromPublicKeyHex(pubKeyHex)
	if err != nil {
		return [20]byte{}, "", err
	}

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
	totalXRP := cfg.TotalXRP
	if totalXRP == 0 {
		totalXRP = InitialXRP
	}
	if totalXRP > uint64(drops.MaxDrops) {
		return nil, fmt.Errorf("genesis XRP supply %d exceeds protocol maximum %d", totalXRP, drops.MaxDrops)
	}
	cfg.Amendments = canonicalAmendments(cfg.Amendments)
	feeSettings, err := buildFeeSettings(cfg)
	if err != nil {
		return nil, fmt.Errorf("invalid genesis fee settings: %w", err)
	}

	passphrase := cfg.MasterPassphrase
	if passphrase == "" {
		passphrase = MasterPassphrase
	}

	closeTimeRes := cfg.CloseTimeResolution
	if closeTimeRes == 0 {
		closeTimeRes = GenesisTimeResolution
	}
	if !validCloseTimeResolution(closeTimeRes) {
		return nil, fmt.Errorf(
			"invalid close time resolution %d: must be 10, 20, 30, 60, 90, or 120",
			closeTimeRes,
		)
	}

	accountID, address, err := GenerateAccountIDFromPassphrase(passphrase)
	if err != nil {
		return nil, fmt.Errorf("failed to generate genesis account: %w", err)
	}

	genesisBalance := totalXRP
	initialAccounts := make([]validatedInitialAccount, 0, len(cfg.InitialAccounts))
	seen := make(map[[20]byte]struct{}, len(cfg.InitialAccounts))
	for _, acc := range cfg.InitialAccounts {
		accID, err := DecodeAddress(acc.Address)
		if err != nil {
			return nil, fmt.Errorf("failed to decode address %s: %w", acc.Address, err)
		}
		if accID == accountID {
			return nil, fmt.Errorf("initial account %s is the genesis account", acc.Address)
		}
		if _, duplicate := seen[accID]; duplicate {
			return nil, fmt.Errorf("duplicate initial account %s", acc.Address)
		}
		if acc.Balance > genesisBalance {
			return nil, errors.New("initial accounts balance exceeds total XRP")
		}
		genesisBalance -= acc.Balance
		seen[accID] = struct{}{}
		initialAccounts = append(initialAccounts, validatedInitialAccount{
			InitialAccount: acc,
			id:             accID,
		})
	}

	stateMap := shamap.New(shamap.TypeState)
	txMap := shamap.New(shamap.TypeTransaction)

	if err := createGenesisAccountWithBalance(stateMap, accountID, genesisBalance); err != nil {
		return nil, fmt.Errorf("failed to create genesis account: %w", err)
	}

	for _, acc := range initialAccounts {
		if err := createInitialAccount(stateMap, acc.id, acc.Balance, acc.Sequence, acc.Flags); err != nil {
			return nil, fmt.Errorf("failed to create account %s: %w", acc.Address, err)
		}
	}

	if err := createFeeSettings(stateMap, feeSettings); err != nil {
		return nil, fmt.Errorf("failed to create fee settings: %w", err)
	}

	if len(cfg.Amendments) > 0 {
		if err := createAmendments(stateMap, cfg.Amendments); err != nil {
			return nil, fmt.Errorf("failed to create amendments: %w", err)
		}
	}

	if err := stateMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("failed to make state map immutable: %w", err)
	}

	if err := txMap.SetImmutable(); err != nil {
		return nil, fmt.Errorf("failed to make tx map immutable: %w", err)
	}

	accountHash, err := stateMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("failed to get state map hash: %w", err)
	}

	txHash, err := txMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("failed to get tx map hash: %w", err)
	}

	ledgerHeader := header.LedgerHeader{
		LedgerIndex:         GenesisLedgerSequence,
		ParentCloseTime:     time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		CloseTime:           time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC),
		CloseTimeResolution: uint8(closeTimeRes),
		CloseFlags:          0,
		ParentHash:          [32]byte{}, // Genesis has no parent
		TxHash:              txHash,
		AccountHash:         accountHash,
		Drops:               totalXRP,
		Validated:           true,
		Accepted:            true,
	}

	ledgerHeader.Hash = CalculateLedgerHash(ledgerHeader)

	return &GenesisLedger{
		Header:         ledgerHeader,
		StateMap:       stateMap,
		TxMap:          txMap,
		GenesisAccount: accountID,
		GenesisAddress: address,
	}, nil
}

func canonicalAmendments(amendments [][32]byte) [][32]byte {
	if len(amendments) < 2 {
		return append([][32]byte(nil), amendments...)
	}
	canonical := append([][32]byte(nil), amendments...)
	sort.Slice(canonical, func(i, j int) bool {
		return bytes.Compare(canonical[i][:], canonical[j][:]) < 0
	})
	write := 1
	for read := 1; read < len(canonical); read++ {
		if canonical[read] == canonical[write-1] {
			continue
		}
		canonical[write] = canonical[read]
		write++
	}
	return canonical[:write]
}

func validCloseTimeResolution(resolution uint32) bool {
	switch resolution {
	case 10, 20, 30, 60, 90, 120:
		return true
	default:
		return false
	}
}

func createGenesisAccountWithBalance(stateMap *shamap.SHAMap, accountID [20]byte, balance uint64) error {
	account := &accountRoot{
		Account:    accountID,
		Sequence:   1,
		Balance:    balance,
		OwnerCount: 0,
	}

	data, err := serializeAccountRoot(account)
	if err != nil {
		return err
	}

	k := keylet.Account(accountID)
	return stateMap.Put(k.Key, data)
}

func createInitialAccount(stateMap *shamap.SHAMap, accountID [20]byte, balance uint64, sequence uint32, flags uint32) error {
	if sequence == 0 {
		sequence = 1
	}

	account := &accountRoot{
		Flags:      flags,
		Account:    accountID,
		Sequence:   sequence,
		Balance:    balance,
		OwnerCount: 0,
	}

	data, err := serializeAccountRoot(account)
	if err != nil {
		return err
	}

	k := keylet.Account(accountID)
	return stateMap.Put(k.Key, data)
}

func buildFeeSettings(cfg Config) (*feeSettings, error) {
	for _, fee := range []struct {
		name   string
		amount drops.XRPAmount
	}{
		{"BaseFee", cfg.Fees.BaseFee},
		{"ReserveBase", cfg.Fees.ReserveBase},
		{"ReserveIncrement", cfg.Fees.ReserveIncrement},
	} {
		if fee.amount < 0 {
			return nil, fmt.Errorf("%s %d must be nonnegative", fee.name, fee.amount)
		}
		if fee.amount > drops.MaxDrops {
			return nil, fmt.Errorf("%s %d exceeds maximum XRP amount %d", fee.name, fee.amount, drops.MaxDrops)
		}
	}

	if hasXRPFeesAmendment(cfg.Amendments) {
		return newFeeSettings(cfg.Fees.BaseFee, cfg.Fees.ReserveBase, cfg.Fees.ReserveIncrement), nil
	}

	if cfg.Fees.ReserveBase > math.MaxUint32 {
		return nil, fmt.Errorf("ReserveBase %d exceeds legacy UInt32 maximum %d", cfg.Fees.ReserveBase, uint64(math.MaxUint32))
	}
	if cfg.Fees.ReserveIncrement > math.MaxUint32 {
		return nil, fmt.Errorf("ReserveIncrement %d exceeds legacy UInt32 maximum %d", cfg.Fees.ReserveIncrement, uint64(math.MaxUint32))
	}

	return newLegacyFeeSettings(
		uint64(cfg.Fees.BaseFee),
		10,
		uint32(cfg.Fees.ReserveBase),
		uint32(cfg.Fees.ReserveIncrement),
	), nil
}

func createFeeSettings(stateMap *shamap.SHAMap, feeSettings *feeSettings) error {
	data, err := serializeFeeSettings(feeSettings)
	if err != nil {
		return err
	}

	k := keylet.Fees()
	return stateMap.Put(k.Key, data)
}

func createAmendments(stateMap *shamap.SHAMap, amendments [][32]byte) error {
	data, err := serializeAmendments(amendments)
	if err != nil {
		return err
	}

	k := keylet.Amendments()
	return stateMap.Put(k.Key, data)
}

// CalculateLedgerHash hashes a ledger header via the canonical header.CalculateHash
// so genesis, ledger, and inbound-replay paths hash byte-identically.
func CalculateLedgerHash(h header.LedgerHeader) [32]byte {
	return header.CalculateHash(h)
}

func serializeAccountRoot(a *accountRoot) ([]byte, error) {
	if a == nil {
		return nil, errors.New("failed to encode AccountRoot: nil entry")
	}

	address, err := addresscodec.EncodeAccountIDToClassicAddress(a.Account[:])
	if err != nil {
		return nil, fmt.Errorf("failed to encode account address: %w", err)
	}

	var sle ledgerfields.AccountRoot
	sle.SetFlags(a.Flags)
	sle.SetAccount(address)
	sle.SetBalance(fmt.Sprintf("%d", a.Balance))
	sle.SetSequence(a.Sequence)
	sle.SetOwnerCount(a.OwnerCount)
	sle.SetPreviousTxnID("0000000000000000000000000000000000000000000000000000000000000000")
	sle.SetPreviousTxnLgrSeq(0)

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode AccountRoot: %w", err)
	}
	return data, nil
}

func serializeFeeSettings(f *feeSettings) ([]byte, error) {
	if f == nil {
		return nil, errors.New("failed to encode FeeSettings: nil entry")
	}

	var sle ledgerfields.FeeSettings
	sle.SetFlags(0)

	if f.IsUsingModernFees() {
		sle.SetBaseFeeDrops(fmt.Sprintf("%d", f.BaseFeeDrops))
		sle.SetReserveBaseDrops(fmt.Sprintf("%d", f.ReserveBaseDrops))
		sle.SetReserveIncrementDrops(fmt.Sprintf("%d", f.ReserveIncrementDrops))
	} else {
		if f.BaseFee != nil {
			sle.SetBaseFee(fmt.Sprintf("%x", *f.BaseFee))
		}
		if f.ReferenceFeeUnits != nil {
			sle.SetReferenceFeeUnits(*f.ReferenceFeeUnits)
		}
		if f.ReserveBase != nil {
			sle.SetReserveBase(*f.ReserveBase)
		}
		if f.ReserveIncrement != nil {
			sle.SetReserveIncrement(*f.ReserveIncrement)
		}
	}

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode FeeSettings: %w", err)
	}
	return data, nil
}

func serializeAmendments(amendments [][32]byte) ([]byte, error) {
	// Amendment hashes become hex strings for the Vector256 field.
	amendmentHexes := make([]string, len(amendments))
	for i, amendment := range amendments {
		amendmentHexes[i] = fmt.Sprintf("%064X", amendment)
	}

	var sle ledgerfields.Amendments
	sle.SetFlags(0)
	sle.SetAmendments(amendmentHexes)

	data, err := sle.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode Amendments: %w", err)
	}
	return data, nil
}
