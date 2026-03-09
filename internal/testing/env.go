package testing

import (
	"encoding/hex"
	"testing"
	"time"

	addresscodec "github.com/LeJamon/goXRPLd/codec/address-codec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/amendment"
	"github.com/LeJamon/goXRPLd/internal/core/ledger"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/genesis"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/shamap"
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/core/tx/depositpreauth"
	"github.com/LeJamon/goXRPLd/internal/core/tx/offer"
	"github.com/LeJamon/goXRPLd/internal/core/tx/payment"
	"github.com/LeJamon/goXRPLd/internal/core/tx/signerlist"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	"github.com/LeJamon/goXRPLd/internal/core/tx/ticket"
	"github.com/LeJamon/goXRPLd/internal/core/tx/trustset"
)

// TestEnv manages a test ledger environment for transaction testing.
// It provides a simplified interface for creating accounts, funding them,
// submitting transactions, and verifying results.
type TestEnv struct {
	t        *testing.T
	ledger   *ledger.Ledger
	clock    *ManualClock
	accounts map[string]*Account

	// Genesis ledger reference
	genesisLedger *ledger.Ledger

	// Lightweight ledger history: sequence → state map root hash.
	// Matches rippled's LedgerHistory pattern — stores only hashes, not full objects.
	// Past state can be reconstructed on demand via NewFromRootHash(hash, family).
	ledgerRootHashes map[uint32][32]byte

	// Current ledger sequence
	currentSeq uint32

	// Fees configuration
	baseFee          uint64
	reserveBase      uint64
	reserveIncrement uint64

	// Amendment rules - controls which amendments are enabled.
	// Reference: rippled's FeatureBitset in test/jtx/Env.h
	rulesBuilder *amendment.RulesBuilder

	// NetworkID for engine configuration (0 = mainnet default, >1024 requires NetworkID in txns)
	networkID uint32

	// Optional state map family for backed SHAMaps (PebbleDB on disk).
	// Only set when using NewTestEnvBacked() for heavy tests that would OOM otherwise.
	// When nil, SHAMaps use unbacked mode (fast, full in-memory clones).
	stateFamily *shamap.NodeStoreFamily
}

// NewTestEnv creates a new test environment with a genesis ledger.
func NewTestEnv(t *testing.T) *TestEnv {
	t.Helper()

	// Create genesis ledger with test configuration matching rippled's test env
	// (200 XRP base reserve, 50 XRP increment — see rippled/src/test/jtx/impl/envconfig.cpp)
	genesisConfig := genesis.DefaultConfig()
	genesisConfig.Fees.ReserveBase = drops.DropsPerXRP * 200      // 200 XRP
	genesisConfig.Fees.ReserveIncrement = drops.DropsPerXRP * 50  // 50 XRP
	genesisResult, err := genesis.Create(genesisConfig)
	if err != nil {
		t.Fatalf("Failed to create genesis ledger: %v", err)
	}

	// Create the ledger from genesis
	// Note: drops.Fees has unexported fields, so we use a zero value
	var fees drops.Fees
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)

	// Create the first open ledger
	clock := NewManualClock()
	openLedger, err := ledger.NewOpen(genesisLedger, clock.Now())
	if err != nil {
		t.Fatalf("Failed to create open ledger: %v", err)
	}

	env := &TestEnv{
		t:                t,
		ledger:           openLedger,
		clock:            clock,
		accounts:         make(map[string]*Account),
		genesisLedger:    genesisLedger,
		ledgerRootHashes: make(map[uint32][32]byte),
		currentSeq:       2,
		baseFee:          10,
		reserveBase:      200_000_000, // 200 XRP (matches rippled test env)
		reserveIncrement: 50_000_000,  // 50 XRP (matches rippled test env)
		// Initialize with all supported amendments enabled (like rippled's testable_amendments())
		rulesBuilder: amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported),
	}

	// Register master account
	master := MasterAccount()
	env.accounts[master.Name] = master

	return env
}

// NewTestEnvBacked creates a test environment with PebbleDB-backed SHAMaps.
// Use this for heavy tests (e.g., crossing_limits with 2000+ offers) that would
// OOM with unbacked mode. Data goes to disk; only the LRU cache lives in RAM.
func NewTestEnvBacked(t *testing.T) *TestEnv {
	t.Helper()
	env := NewTestEnv(t)
	env.enablePebbleBacking(t)
	return env
}

// NewTestEnvWithConfigBacked creates a test environment with custom config and PebbleDB backing.
func NewTestEnvWithConfigBacked(t *testing.T, cfg genesis.Config) *TestEnv {
	t.Helper()
	env := NewTestEnvWithConfig(t, cfg)
	env.enablePebbleBacking(t)
	return env
}

// enablePebbleBacking enables PebbleDB-backed SHAMaps on the environment.
// Must be called before any transactions are submitted.
func (e *TestEnv) enablePebbleBacking(t *testing.T) {
	t.Helper()
	stateFamily, err := shamap.NewPebbleNodeStoreFamily(t.TempDir(), 200000)
	if err != nil {
		t.Fatalf("Failed to create state family: %v", err)
	}
	t.Cleanup(func() { stateFamily.Close() })
	e.stateFamily = stateFamily
	e.genesisLedger.SetStateMapFamily(stateFamily)

	// Recreate the open ledger so it inherits the backed state map
	openLedger, err := ledger.NewOpen(e.genesisLedger, e.clock.Now())
	if err != nil {
		t.Fatalf("Failed to recreate open ledger with backing: %v", err)
	}
	e.ledger = openLedger
}

// NewTestEnvWithConfig creates a new test environment with custom genesis configuration.
func NewTestEnvWithConfig(t *testing.T, cfg genesis.Config) *TestEnv {
	t.Helper()

	genesisResult, err := genesis.Create(cfg)
	if err != nil {
		t.Fatalf("Failed to create genesis ledger: %v", err)
	}

	// Note: drops.Fees has unexported fields, so we use a zero value
	var fees drops.Fees
	genesisLedger := ledger.FromGenesis(
		genesisResult.Header,
		genesisResult.StateMap,
		genesisResult.TxMap,
		fees,
	)

	clock := NewManualClock()
	openLedger, err := ledger.NewOpen(genesisLedger, clock.Now())
	if err != nil {
		t.Fatalf("Failed to create open ledger: %v", err)
	}

	env := &TestEnv{
		t:                t,
		ledger:           openLedger,
		clock:            clock,
		accounts:         make(map[string]*Account),
		genesisLedger:    genesisLedger,
		ledgerRootHashes: make(map[uint32][32]byte),
		currentSeq:       2,
		baseFee:          uint64(cfg.Fees.BaseFee.Drops()),
		reserveBase:      uint64(cfg.Fees.ReserveBase.Drops()),
		reserveIncrement: uint64(cfg.Fees.ReserveIncrement.Drops()),
		// Initialize with all supported amendments enabled (like rippled's testable_amendments())
		rulesBuilder: amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported),
	}
	master := MasterAccount()
	env.accounts[master.Name] = master

	return env
}

// Fund funds the specified accounts from the master account.
// Each account receives the specified amount or a default of 1000 XRP.
func (e *TestEnv) Fund(accounts ...*Account) {
	e.t.Helper()

	for _, acc := range accounts {
		e.FundAmount(acc, uint64(XRP(1000)))
	}
}

// FundAmount funds an account with a specific amount.
// Like rippled's test environment, this also enables DefaultRipple on the account.
// This is important for trust line behavior - without DefaultRipple, trust lines
// cannot be deleted when limit is set to 0 (the NoRipple state would be "non-default").
func (e *TestEnv) FundAmount(acc *Account, amount uint64) {
	e.t.Helper()

	// Register account
	e.accounts[acc.Name] = acc

	// Create a payment from master to the new account
	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	// Fund with extra to cover the AccountSet fee (for enabling DefaultRipple)
	// This ensures the account ends up with the requested amount.
	totalFunding := amount + e.baseFee

	// Create payment transaction
	seq := e.Seq(master)
	payment := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(totalFunding)))
	payment.Fee = formatUint64(e.baseFee)
	payment.Sequence = &seq

	// Submit the payment
	result := e.Submit(payment)
	if !result.Success {
		e.t.Fatalf("Failed to fund account %s: %s", acc.Name, result.Code)
	}

	// Enable DefaultRipple on the account (matching rippled's test environment)
	// This allows trust lines to be properly deleted when limits are set to 0.
	e.enableDefaultRipple(acc)
}

// Pay sends XRP from master to an already-funded account.
// This is useful for tests that need to top-up an account with additional XRP
// (e.g., to meet reserve requirements). Unlike FundAmount, this does not
// register the account or enable DefaultRipple.
func (e *TestEnv) Pay(acc *Account, drops uint64) {
	e.t.Helper()

	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	seq := e.Seq(master)
	p := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(drops)))
	p.Fee = formatUint64(e.baseFee)
	p.Sequence = &seq

	result := e.Submit(p)
	if !result.Success {
		e.t.Fatalf("Failed to pay %d drops to %s: %s", drops, acc.Name, result.Code)
	}
}

// enableDefaultRipple enables the DefaultRipple flag on an account.
// This matches rippled's test environment behavior.
func (e *TestEnv) enableDefaultRipple(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.EnableDefaultRipple()
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable DefaultRipple for account %s: %s", acc.Name, result.Code)
	}
}

// EnableDepositAuth enables the DepositAuth flag on an account.
// When enabled, the account can only receive payments from preauthorized accounts.
func (e *TestEnv) EnableDepositAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.EnableDepositAuth()
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable DepositAuth for account %s: %s", acc.Name, result.Code)
	}
}

// DisableDepositAuth disables the DepositAuth flag on an account.
func (e *TestEnv) DisableDepositAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDepositAuth
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable DepositAuth for account %s: %s", acc.Name, result.Code)
	}
}

// EnableGlobalFreeze enables the GlobalFreeze flag on an account.
// When enabled, all trust lines for this account's issued currencies are frozen.
func (e *TestEnv) EnableGlobalFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagGlobalFreeze
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable GlobalFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// DisableGlobalFreeze disables the GlobalFreeze flag on an account.
// Note: Cannot be cleared if NoFreeze is set.
func (e *TestEnv) DisableGlobalFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagGlobalFreeze
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable GlobalFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// EnableNoFreeze enables the NoFreeze flag on an account.
// Once set, this flag cannot be cleared. It prevents the account from freezing trust lines.
func (e *TestEnv) EnableNoFreeze(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagNoFreeze
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable NoFreeze for account %s: %s", acc.Name, result.Code)
	}
}

// SetTransferRate sets the transfer rate for an account.
// Rate is specified as a multiplier * 1e9 (1e9 = 100%, 1.1e9 = 110% means 10% fee).
// Use 0 to clear the transfer rate (sets it back to 1e9 / 100%).
func (e *TestEnv) SetTransferRate(acc *Account, rate uint32) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.TransferRate = &rate
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to set transfer rate for account %s: %s", acc.Name, result.Code)
	}
}

// EnableRequireAuth enables the RequireAuth flag on an account.
// When enabled, trust lines to this account require authorization.
func (e *TestEnv) EnableRequireAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireAuth
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable RequireAuth for account %s: %s", acc.Name, result.Code)
	}
}

// DisableRequireAuth disables the RequireAuth flag on an account.
func (e *TestEnv) DisableRequireAuth(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireAuth
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable RequireAuth for account %s: %s", acc.Name, result.Code)
	}
}

// EnableRequireDest enables the RequireDest flag on an account.
// When enabled, the account requires a destination tag on incoming payments.
func (e *TestEnv) EnableRequireDest(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireDest
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to enable RequireDest for account %s: %s", acc.Name, result.Code)
	}
}

// DisableRequireDest disables the RequireDest flag on an account.
func (e *TestEnv) DisableRequireDest(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagRequireDest
	accountSet.ClearFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable RequireDest for account %s: %s", acc.Name, result.Code)
	}
}

// Preauthorize allows owner to preauthorize authorized for deposits.
// This creates a DepositPreauth ledger entry.
func (e *TestEnv) Preauthorize(owner, authorized *Account) {
	e.t.Helper()

	preauth := depositpreauth.NewDepositPreauth(owner.Address)
	preauth.SetAuthorize(authorized.Address)
	preauth.Fee = formatUint64(e.baseFee)
	seq := e.Seq(owner)
	preauth.Sequence = &seq

	result := e.Submit(preauth)
	if !result.Success {
		e.t.Fatalf("Failed to preauthorize %s for %s: %s", authorized.Name, owner.Name, result.Code)
	}
}

// Unauthorize removes preauthorization for authorized from owner.
func (e *TestEnv) Unauthorize(owner, authorized *Account) {
	e.t.Helper()

	preauth := depositpreauth.NewDepositPreauth(owner.Address)
	preauth.SetUnauthorize(authorized.Address)
	preauth.Fee = formatUint64(e.baseFee)
	seq := e.Seq(owner)
	preauth.Sequence = &seq

	result := e.Submit(preauth)
	if !result.Success {
		e.t.Fatalf("Failed to unauthorize %s for %s: %s", authorized.Name, owner.Name, result.Code)
	}
}

// CreateOffer creates an offer on the DEX.
// takerGets is what the offer creator will receive (what the taker pays).
// takerPays is what the offer creator will pay (what the taker gets).
func (e *TestEnv) CreateOffer(acc *Account, takerGets, takerPays tx.Amount) TxResult {
	e.t.Helper()

	offerTx := offer.NewOfferCreate(acc.Address, takerGets, takerPays)
	offerTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	offerTx.Sequence = &seq

	return e.Submit(offerTx)
}

// CreatePassiveOffer creates a passive offer on the DEX.
// Passive offers don't immediately consume offers at an equal or better rate.
func (e *TestEnv) CreatePassiveOffer(acc *Account, takerGets, takerPays tx.Amount) TxResult {
	e.t.Helper()

	offerTx := offer.NewOfferCreate(acc.Address, takerGets, takerPays)
	offerTx.SetPassive()
	offerTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	offerTx.Sequence = &seq

	return e.Submit(offerTx)
}

// Close closes the current ledger and advances to a new one.
// This is equivalent to "ledger_accept" in rippled.
func (e *TestEnv) Close() {
	e.t.Helper()

	// Advance time
	e.clock.Advance(10 * time.Second)

	// Close current ledger
	if err := e.ledger.Close(e.clock.Now(), 0); err != nil {
		e.t.Fatalf("Failed to close ledger: %v", err)
	}

	// Validate the ledger (in test mode, we auto-validate)
	if err := e.ledger.SetValidated(); err != nil {
		e.t.Fatalf("Failed to validate ledger: %v", err)
	}

	// Store lightweight state root hash in history (matching rippled's LedgerHistory pattern)
	if h, err := e.ledger.StateMapHash(); err == nil {
		e.ledgerRootHashes[e.ledger.Sequence()] = h
	}

	// Sweep nodestore caches if backed mode is enabled
	if e.stateFamily != nil {
		e.stateFamily.Sweep()
	}

	// Create new open ledger
	newLedger, err := ledger.NewOpen(e.ledger, e.clock.Now())
	if err != nil {
		e.t.Fatalf("Failed to create new ledger: %v", err)
	}

	e.ledger = newLedger
	e.currentSeq++
}

// CloseAt closes ledgers until the ledger reaches the target sequence.
// If already at or past target, does nothing.
func (e *TestEnv) CloseAt(targetSeq uint32) {
	e.t.Helper()
	for e.ledger.Sequence() < targetSeq {
		e.Close()
	}
}

// Submit submits a transaction to the current open ledger.
// If the transaction doesn't have a sequence number set, it will be auto-filled
// from the account's current sequence in the ledger.
func (e *TestEnv) Submit(transaction interface{}) TxResult {
	e.t.Helper()

	// Convert to tx.Transaction interface
	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill sequence if not set (skip when using tickets)
	common := txn.GetCommon()
	if common.Sequence == nil && common.TicketSequence == nil {
		// Look up the account to get current sequence
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil {
			e.t.Fatalf("Failed to decode account address: %v", err)
			return TxResult{Code: "temINVALID", Success: false, Message: "Invalid account address"}
		}

		var id [20]byte
		copy(id[:], accountID)
		accountKey := keylet.Account(id)

		data, err := e.ledger.Read(accountKey)
		if err != nil || data == nil {
			e.t.Fatalf("Failed to read account for sequence auto-fill: %v", err)
			return TxResult{Code: "terNO_ACCOUNT", Success: false, Message: "Account not found"}
		}

		accountRoot, err := sle.ParseAccountRootFromBytes(data)
		if err != nil {
			e.t.Fatalf("Failed to parse account root: %v", err)
			return TxResult{Code: "temINVALID", Success: false, Message: "Failed to parse account"}
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}

	// Create engine config
	// ParentCloseTime is in Ripple epoch seconds (Unix - 946684800)
	// Current time minus Ripple epoch = Ripple epoch time
	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: true, // Skip signatures in test mode
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
	}

	// Create engine with current ledger
	engine := tx.NewEngine(e.ledger, engineConfig)

	// Apply the transaction
	applyResult := engine.Apply(txn)

	// Success should only be true for tesSUCCESS, not for tec codes
	// (tec codes are "applied" but not "successful" - fee is charged but operation failed)
	return TxResult{
		Code:     applyResult.Result.String(),
		Success:  applyResult.Result.IsSuccess(),
		Message:  applyResult.Message,
		Metadata: applyResult.Metadata,
	}
}

// Balance returns the XRP balance of an account in drops.
func (e *TestEnv) Balance(acc *Account) uint64 {
	e.t.Helper()

	// Get account keylet
	accountKey := keylet.Account(acc.ID)

	// Check if account exists
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return 0
	}
	if !exists {
		return 0 // Account doesn't exist
	}

	// Read account data
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to read account: %v", err)
		return 0
	}

	// Parse account root to get balance
	accountRoot, err := sle.ParseAccountRootFromBytes(data)
	if err != nil {
		e.t.Fatalf("Failed to parse account data: %v", err)
		return 0
	}

	return accountRoot.Balance
}

// IOUBalance returns the IOU balance of an account for a specific currency and issuer.
// The balance is returned from the perspective of the holder (not the issuer).
// Positive means the holder has tokens, negative means they owe tokens.
func (e *TestEnv) IOUBalance(holder, issuer *Account, currency string) *sle.Amount {
	e.t.Helper()

	// Get trust line keylet
	lineKey := keylet.Line(holder.ID, issuer.ID, currency)

	// Check if trust line exists
	exists, err := e.ledger.Exists(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to check trust line existence: %v", err)
		return nil
	}
	if !exists {
		// No trust line = zero balance
		zero := sle.NewIssuedAmountFromFloat64(0, currency, issuer.Address)
		return &zero
	}

	// Read trust line data
	data, err := e.ledger.Read(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to read trust line: %v", err)
		return nil
	}

	// Parse RippleState
	rs, err := sle.ParseRippleState(data)
	if err != nil {
		e.t.Fatalf("Failed to parse trust line: %v", err)
		return nil
	}

	// Determine if holder is low or high account
	// Balance sign convention: positive means low owes high
	isLow := keylet.IsLowAccount(holder.ID, issuer.ID)

	balance := rs.Balance
	if !isLow {
		// If holder is high account, negate the balance
		// (positive balance means low owes high, so high has positive tokens)
		balance = balance.Negate()
	}

	// RippleState Balance stores zero issuer; override with actual issuer address.
	// This ensures amounts returned here can be used directly in payments.
	balance.Issuer = issuer.Address

	return &balance
}

// BalanceIOU returns the IOU balance of an account for a specific currency and issuer as float64.
// This is a convenience method for tests that need simple numeric comparisons.
func (e *TestEnv) BalanceIOU(holder *Account, currency string, issuer *Account) float64 {
	e.t.Helper()

	balance := e.IOUBalance(holder, issuer, currency)
	if balance == nil {
		return 0.0
	}

	return balance.Float64()
}

// TrustLineExists checks if a trust line exists between two accounts for a currency.
func (e *TestEnv) TrustLineExists(acc1, acc2 *Account, currency string) bool {
	e.t.Helper()

	lineKey := keylet.Line(acc1.ID, acc2.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil {
		e.t.Fatalf("Failed to check trust line existence: %v", err)
		return false
	}
	return exists
}

// TrustLineFlags returns the flags on a trust line between two accounts.
// Returns the flags from the perspective of 'account' (which side's flags).
func (e *TestEnv) TrustLineFlags(account, counterparty *Account, currency string) uint32 {
	e.t.Helper()

	lineKey := keylet.Line(account.ID, counterparty.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return 0
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return 0
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return 0
	}

	return rs.Flags
}

// ClearTrustLineAuth clears the authorization flags on a trust line between two accounts.
// This directly modifies ledger state, simulating rippled's rawInsert for tests
// that require unauthorized but funded trust lines.
func (e *TestEnv) ClearTrustLineAuth(acc1, acc2 *Account, currency string) {
	e.t.Helper()

	lineKey := keylet.Line(acc1.ID, acc2.ID, currency)
	data, err := e.ledger.Read(lineKey)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: trust line not found: %v", err)
		return
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to parse trust line: %v", err)
		return
	}

	rs.Flags &^= sle.LsfLowAuth | sle.LsfHighAuth

	updated, err := sle.SerializeRippleState(rs)
	if err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to serialize: %v", err)
		return
	}

	if err := e.ledger.Update(lineKey, updated); err != nil {
		e.t.Fatalf("ClearTrustLineAuth: failed to update: %v", err)
	}
}

// HasNoRipple checks if the account's side of the trust line has NoRipple set.
func (e *TestEnv) HasNoRipple(account, counterparty *Account, currency string) bool {
	e.t.Helper()

	lineKey := keylet.Line(account.ID, counterparty.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return false
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return false
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return false
	}

	// Determine if account is low or high
	isLow := keylet.IsLowAccount(account.ID, counterparty.ID)

	if isLow {
		return (rs.Flags & sle.LsfLowNoRipple) != 0
	}
	return (rs.Flags & sle.LsfHighNoRipple) != 0
}

// Now returns the current time on the test clock.
func (e *TestEnv) Now() time.Time {
	return e.clock.Now()
}

// Seq returns the current sequence number for an account.
func (e *TestEnv) Seq(acc *Account) uint32 {
	e.t.Helper()

	// Get account keylet
	accountKey := keylet.Account(acc.ID)

	// Check if account exists
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return 1
	}
	if !exists {
		return 1 // Default sequence for new accounts
	}

	// Read account data
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to read account: %v", err)
		return 1
	}

	// Parse account root to get sequence
	accountRoot, err := sle.ParseAccountRootFromBytes(data)
	if err != nil {
		e.t.Fatalf("Failed to parse account data: %v", err)
		return 1
	}

	return accountRoot.Sequence
}

// Ledger returns the current open ledger.
func (e *TestEnv) Ledger() *ledger.Ledger {
	return e.ledger
}

// LedgerSeq returns the current ledger sequence number.
func (e *TestEnv) LedgerSeq() uint32 {
	return e.ledger.Sequence()
}

// GetAccount returns a registered account by name.
func (e *TestEnv) GetAccount(name string) *Account {
	return e.accounts[name]
}

// AdvanceTime advances the test clock by the specified duration.
func (e *TestEnv) AdvanceTime(d time.Duration) {
	e.clock.Advance(d)
}

// SetTime sets the test clock to a specific time.
func (e *TestEnv) SetTime(t time.Time) {
	e.clock.Set(t)
}

// Exists checks if an account exists in the ledger.
func (e *TestEnv) Exists(acc *Account) bool {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	exists, err := e.ledger.Exists(accountKey)
	if err != nil {
		e.t.Fatalf("Failed to check account existence: %v", err)
		return false
	}
	return exists
}

// AccountInfo returns detailed account information.
func (e *TestEnv) AccountInfo(acc *Account) *AccountInfo {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)

	exists, err := e.ledger.Exists(accountKey)
	if err != nil || !exists {
		return nil
	}

	data, err := e.ledger.Read(accountKey)
	if err != nil {
		return nil
	}

	accountRoot, err := sle.ParseAccountRootFromBytes(data)
	if err != nil {
		return nil
	}

	return &AccountInfo{
		Address:        acc.Address,
		Balance:        accountRoot.Balance,
		Sequence:       accountRoot.Sequence,
		OwnerCount:     accountRoot.OwnerCount,
		Flags:          accountRoot.Flags,
		MintedNFTokens: accountRoot.MintedNFTokens,
		BurnedNFTokens: accountRoot.BurnedNFTokens,
		NFTokenMinter:  accountRoot.NFTokenMinter,
		Domain:         accountRoot.Domain,
		EmailHash:      accountRoot.EmailHash,
		MessageKey:     accountRoot.MessageKey,
		WalletLocator:  accountRoot.WalletLocator,
		AccountTxnID:   accountRoot.AccountTxnID,
		TransferRate:   accountRoot.TransferRate,
	}
}

// AccountInfo contains account information from the ledger.
type AccountInfo struct {
	Address        string
	Balance        uint64
	Sequence       uint32
	OwnerCount     uint32
	Flags          uint32
	MintedNFTokens uint32
	BurnedNFTokens uint32
	NFTokenMinter  string
	Domain         string
	EmailHash      string
	MessageKey     string
	WalletLocator  string
	AccountTxnID   [32]byte
	TransferRate   uint32
}

// MintedCount returns the number of NFTokens minted by this issuer.
// Reference: rippled's mintedCount() test helper.
func (e *TestEnv) MintedCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.MintedNFTokens
}

// BurnedCount returns the number of NFTokens burned for this issuer.
// Reference: rippled's burnedCount() test helper.
func (e *TestEnv) BurnedCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.BurnedNFTokens
}

// MasterAccount returns the master account for the test environment.
func (e *TestEnv) MasterAccount() *Account {
	return e.accounts["master"]
}

// BaseFee returns the base fee in drops.
func (e *TestEnv) BaseFee() uint64 {
	return e.baseFee
}

// ReserveBase returns the base reserve in drops.
func (e *TestEnv) ReserveBase() uint64 {
	return e.reserveBase
}

// ReserveIncrement returns the reserve increment in drops.
func (e *TestEnv) ReserveIncrement() uint64 {
	return e.reserveIncrement
}

// EnableFeature enables an amendment by name for subsequent transactions.
// Reference: rippled's Env::enableFeature() in test/jtx/impl/Env.cpp
func (e *TestEnv) EnableFeature(name string) {
	e.rulesBuilder.EnableByName(name)
}

// DisableFeature disables an amendment by name for subsequent transactions.
// Reference: rippled's Env::disableFeature() in test/jtx/impl/Env.cpp
func (e *TestEnv) DisableFeature(name string) {
	e.rulesBuilder.DisableByName(name)
}

// SetNetworkID sets the network identifier for the test environment.
// Networks with ID > 1024 require NetworkID in transactions.
// Networks with ID <= 1024 are legacy networks and cannot have NetworkID in transactions.
// Reference: rippled's Config::NETWORK_ID
func (e *TestEnv) SetNetworkID(id uint32) {
	e.networkID = id
}

// FeatureEnabled returns true if the named amendment is currently enabled.
// Reference: rippled's Env::enabled() in test/jtx/Env.h
func (e *TestEnv) FeatureEnabled(name string) bool {
	f := amendment.GetFeatureByName(name)
	if f == nil {
		return false
	}
	rules := e.rulesBuilder.Build()
	return rules.Enabled(f.ID)
}

// DecodeAddress decodes an XRPL address to a 20-byte account ID.
func DecodeAddress(address string) ([20]byte, error) {
	_, accountIDBytes, err := addresscodec.DecodeClassicAddressToAccountID(address)
	if err != nil {
		return [20]byte{}, err
	}

	var accountID [20]byte
	copy(accountID[:], accountIDBytes)
	return accountID, nil
}

// WithSeq sets the sequence number on a transaction manually.
// This bypasses autofill and allows testing transactions from non-existent accounts.
// Reference: rippled's seq(1) funclet in test/jtx/seq.h
func WithSeq(transaction tx.Transaction, seq uint32) tx.Transaction {
	transaction.GetCommon().Sequence = &seq
	return transaction
}

// formatUint64 formats a uint64 as a string (for XRP amounts).
func formatUint64(n uint64) string {
	// Simple conversion without importing strconv
	if n == 0 {
		return "0"
	}

	digits := make([]byte, 0, 20)
	for n > 0 {
		digits = append(digits, byte('0'+n%10))
		n /= 10
	}

	// Reverse
	for i, j := 0, len(digits)-1; i < j; i, j = i+1, j-1 {
		digits[i], digits[j] = digits[j], digits[i]
	}

	return string(digits)
}

// ===========================================================================
// Phase 1a: Signature helpers
// ===========================================================================

// privateKeyHex returns the prefixed hex private key for use with tx.SignTransaction.
// tx.SignTransaction expects 0x00 prefix for secp256k1 and 0xED prefix for ed25519.
func privateKeyHex(acc *Account) string {
	switch acc.KeyType {
	case KeyTypeEd25519:
		return "ED" + hex.EncodeToString(acc.PrivateKey)
	case KeyTypeSecp256k1:
		return "00" + hex.EncodeToString(acc.PrivateKey)
	default:
		panic("unsupported key type: " + acc.KeyType)
	}
}

// SignWith signs a transaction using a specific account's key pair.
// Sets SigningPubKey and TxnSignature on the transaction.
// Reference: rippled's sig.h — sig(account) funclet.
func (e *TestEnv) SignWith(txn tx.Transaction, signer *Account) tx.Transaction {
	e.t.Helper()

	common := txn.GetCommon()
	common.SigningPubKey = hex.EncodeToString(signer.PublicKey)

	sig, err := tx.SignTransaction(txn, privateKeyHex(signer))
	if err != nil {
		e.t.Fatalf("Failed to sign transaction: %v", err)
	}
	common.TxnSignature = sig

	return txn
}

// SubmitSigned signs the transaction with the account's own key and submits
// with signature verification enabled.
// The signing account is inferred from the transaction's Account field.
func (e *TestEnv) SubmitSigned(transaction interface{}) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Look up the account by address
	acc := e.findAccountByAddress(txn.GetCommon().Account)
	if acc == nil {
		e.t.Fatalf("SubmitSigned: account %s not registered in test env", txn.GetCommon().Account)
		return TxResult{Code: "terNO_ACCOUNT", Success: false, Message: "Account not found"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.SignWith(txn, acc)
	return e.submitWithSigVerification(txn)
}

// SubmitSignedWith signs the transaction with a different key (e.g. a regular key)
// and submits with signature verification enabled.
// Reference: rippled's sig(account) — sign with regular key.
func (e *TestEnv) SubmitSignedWith(transaction interface{}, signer *Account) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)
	e.SignWith(txn, signer)
	return e.submitWithSigVerification(txn)
}

// SubmitMultiSigned attaches multi-signatures from the given signers and submits
// with signature verification enabled.
// Each signer signs the transaction with their key, sorted by account ID.
// Reference: rippled's msig(signers...) funclet.
func (e *TestEnv) SubmitMultiSigned(transaction interface{}, signers []*Account) TxResult {
	e.t.Helper()

	txn, ok := transaction.(tx.Transaction)
	if !ok {
		e.t.Fatalf("Transaction does not implement tx.Transaction interface")
		return TxResult{Code: "temINVALID", Success: false, Message: "Invalid transaction type"}
	}

	// Auto-fill BEFORE signing, since sequence/fee are part of the signed payload.
	e.autoFillForSigning(txn)

	common := txn.GetCommon()

	// Clear single-signature fields for multi-sign
	common.SigningPubKey = ""
	common.TxnSignature = ""

	// Calculate multi-sign fee: (numSigners + 1) * baseFee
	multisigFee := uint64(len(signers)+1) * e.baseFee
	common.Fee = formatUint64(multisigFee)

	// Each signer signs and is added (AddMultiSigner maintains sorted order)
	for _, signer := range signers {
		sig, err := tx.SignTransactionForMultiSign(txn, signer.Address, privateKeyHex(signer))
		if err != nil {
			e.t.Fatalf("Failed to multi-sign for %s: %v", signer.Name, err)
		}

		err = tx.AddMultiSigner(txn, signer.Address, hex.EncodeToString(signer.PublicKey), sig)
		if err != nil {
			e.t.Fatalf("Failed to add multi-signer %s: %v", signer.Name, err)
		}
	}

	return e.submitWithSigVerification(txn)
}

// autoFillForSigning fills in sequence and fee fields before signing.
// This must be called before signing, since these fields are part of the signed payload.
func (e *TestEnv) autoFillForSigning(txn tx.Transaction) {
	e.t.Helper()

	common := txn.GetCommon()

	// Auto-fill sequence if not set
	if common.Sequence == nil && common.TicketSequence == nil {
		_, accountID, err := addresscodec.DecodeClassicAddressToAccountID(common.Account)
		if err != nil {
			e.t.Fatalf("autoFillForSigning: failed to decode account address: %v", err)
			return
		}

		var id [20]byte
		copy(id[:], accountID)
		accountKey := keylet.Account(id)

		data, err := e.ledger.Read(accountKey)
		if err != nil || data == nil {
			e.t.Fatalf("autoFillForSigning: failed to read account: %v", err)
			return
		}

		accountRoot, err := sle.ParseAccountRootFromBytes(data)
		if err != nil {
			e.t.Fatalf("autoFillForSigning: failed to parse account root: %v", err)
			return
		}

		seq := accountRoot.Sequence
		common.Sequence = &seq
	}

	// Auto-fill fee if not set
	if common.Fee == "" {
		common.Fee = formatUint64(e.baseFee)
	}
}

// submitWithSigVerification is the internal submit path with signature verification enabled.
// Callers must auto-fill and sign BEFORE calling this.
func (e *TestEnv) submitWithSigVerification(txn tx.Transaction) TxResult {
	e.t.Helper()

	parentCloseTime := uint32(e.clock.Now().Unix() - 946684800)
	engineConfig := tx.EngineConfig{
		BaseFee:                   e.baseFee,
		ReserveBase:               e.reserveBase,
		ReserveIncrement:          e.reserveIncrement,
		LedgerSequence:            e.ledger.Sequence(),
		SkipSignatureVerification: false, // Verify signatures
		Rules:                     e.rulesBuilder.Build(),
		ParentCloseTime:           parentCloseTime,
		NetworkID:                 e.networkID,
		ParentHash:                e.ledger.ParentHash(),
	}

	engine := tx.NewEngine(e.ledger, engineConfig)
	applyResult := engine.Apply(txn)

	return TxResult{
		Code:    applyResult.Result.String(),
		Success: applyResult.Result.IsSuccess(),
		Message: applyResult.Message,
	}
}

// findAccountByAddress looks up a registered account by its XRPL address.
func (e *TestEnv) findAccountByAddress(address string) *Account {
	for _, acc := range e.accounts {
		if acc.Address == address {
			return acc
		}
	}
	return nil
}

// ===========================================================================
// Phase 1b: Regular Key helpers
// ===========================================================================

// SetRegularKey sets a regular key on an account.
// Reference: rippled's regkey(account, signer) in regkey.h
func (e *TestEnv) SetRegularKey(acc, regularKey *Account) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.SetKey(regularKey.Address)
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if !result.Success {
		e.t.Fatalf("Failed to set regular key for %s: %s", acc.Name, result.Code)
	}
}

// DisableRegularKey removes the regular key from an account.
// Reference: rippled's regkey(account, disabled) in regkey.h
func (e *TestEnv) DisableRegularKey(acc *Account) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if !result.Success {
		e.t.Fatalf("Failed to disable regular key for %s: %s", acc.Name, result.Code)
	}
}

// DisableRegularKeyExpect attempts to clear the regular key and expects a specific result.
func (e *TestEnv) DisableRegularKeyExpect(acc *Account, expectedCode string) {
	e.t.Helper()

	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	setKey.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	setKey.Sequence = &seq

	result := e.Submit(setKey)
	if result.Code != expectedCode {
		e.t.Fatalf("DisableRegularKeyExpect: expected %s, got %s", expectedCode, result.Code)
	}
}

// DisableMasterKey disables the master key on an account using AccountSet.
// The account must have a regular key or signer list set first.
func (e *TestEnv) DisableMasterKey(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisableMaster
	accountSet.SetFlag = &flag
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed to disable master key for %s: %s", acc.Name, result.Code)
	}
}

// ===========================================================================
// Phase 1c: SignerList helpers
// ===========================================================================

// TestSigner represents a signer entry for use in SetSignerList.
type TestSigner struct {
	Account *Account
	Weight  uint16
}

// SetSignerList sets a signer list on an account.
// Reference: rippled's signers(account, quorum, signerList) in multisign.h
func (e *TestEnv) SetSignerList(acc *Account, quorum uint32, signers []TestSigner) {
	e.t.Helper()

	sl := signerlist.NewSignerListSet(acc.Address, quorum)
	for _, s := range signers {
		sl.AddSigner(s.Account.Address, s.Weight)
	}
	sl.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	sl.Sequence = &seq

	result := e.Submit(sl)
	if !result.Success {
		e.t.Fatalf("Failed to set signer list for %s: %s", acc.Name, result.Code)
	}
}

// RemoveSignerList removes the signer list from an account.
// Reference: rippled's signers(account, none) in multisign.h
func (e *TestEnv) RemoveSignerList(acc *Account) {
	e.t.Helper()

	sl := signerlist.NewSignerListSet(acc.Address, 0)
	sl.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	sl.Sequence = &seq

	result := e.Submit(sl)
	if !result.Success {
		e.t.Fatalf("Failed to remove signer list for %s: %s", acc.Name, result.Code)
	}
}

// ===========================================================================
// Phase 1c (continued): Raw transaction helpers for multisign tests
// ===========================================================================

// NewSignerListSetTx creates a raw SignerListSet transaction without submitting.
// Use this when you need to submit via SubmitMultiSigned or SubmitSignedWith.
func NewSignerListSetTx(acc *Account, quorum uint32, signers []TestSigner) tx.Transaction {
	sl := signerlist.NewSignerListSet(acc.Address, quorum)
	for _, s := range signers {
		sl.AddSigner(s.Account.Address, s.Weight)
	}
	return sl
}

// NewRemoveSignerListTx creates a raw SignerListSet transaction that removes the signer list.
func NewRemoveSignerListTx(acc *Account) tx.Transaction {
	return signerlist.NewSignerListSet(acc.Address, 0)
}

// NewSetRegularKeyTx creates a raw SetRegularKey transaction that sets a regular key.
func NewSetRegularKeyTx(acc *Account, regularKey *Account) tx.Transaction {
	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.SetKey(regularKey.Address)
	return setKey
}

// NewDisableRegularKeyTx creates a raw SetRegularKey transaction that clears the regular key.
func NewDisableRegularKeyTx(acc *Account) tx.Transaction {
	setKey := signerlist.NewSetRegularKey(acc.Address)
	setKey.ClearKey()
	return setKey
}

// ===========================================================================
// Phase 1d: Ticket helpers
// ===========================================================================

// CreateTickets creates N tickets for an account.
// Returns the first ticket sequence number.
// Reference: rippled's ticket::create(account, count) in ticket.h
func (e *TestEnv) CreateTickets(acc *Account, count uint32) uint32 {
	e.t.Helper()

	// The starting ticket sequence is the account's current sequence
	startSeq := e.Seq(acc)

	tc := ticket.NewTicketCreate(acc.Address, count)
	tc.Fee = formatUint64(e.baseFee)
	seq := startSeq
	tc.Sequence = &seq

	result := e.Submit(tc)
	if !result.Success {
		e.t.Fatalf("Failed to create %d tickets for %s: %s", count, acc.Name, result.Code)
	}

	return startSeq + 1 // Tickets start at seq+1 (seq itself is consumed by TicketCreate)
}

// WithTicketSeq sets TicketSequence on a transaction (Sequence becomes 0).
// Reference: rippled's ticket::use(ticketSeq) in ticket.h
func WithTicketSeq(transaction tx.Transaction, ticketSeq uint32) tx.Transaction {
	common := transaction.GetCommon()
	zero := uint32(0)
	common.Sequence = &zero
	common.TicketSequence = &ticketSeq
	return transaction
}

// ===========================================================================
// Phase 2: Query & Convenience Helpers
// ===========================================================================

// OwnerCount returns the owner count for an account (0 if account doesn't exist).
// Reference: rippled's Env::ownerCount(account) in Env.h
func (e *TestEnv) OwnerCount(acc *Account) uint32 {
	e.t.Helper()
	info := e.AccountInfo(acc)
	if info == nil {
		return 0
	}
	return info.OwnerCount
}

// LedgerEntryExists checks if a ledger entry exists by keylet.
func (e *TestEnv) LedgerEntryExists(key keylet.Keylet) bool {
	e.t.Helper()
	exists, err := e.ledger.Exists(key)
	if err != nil {
		e.t.Fatalf("Failed to check ledger entry existence: %v", err)
		return false
	}
	return exists
}

// LedgerEntry reads a raw ledger entry by keylet.
func (e *TestEnv) LedgerEntry(key keylet.Keylet) ([]byte, error) {
	e.t.Helper()
	return e.ledger.Read(key)
}

// FundNoRipple funds accounts WITHOUT enabling DefaultRipple.
// Reference: rippled's noripple(accounts...) in Env.h
func (e *TestEnv) FundNoRipple(accounts ...*Account) {
	e.t.Helper()
	for _, acc := range accounts {
		e.FundAmountNoRipple(acc, uint64(XRP(1000)))
	}
}

// FundAmountNoRipple funds an account with a specific amount but does NOT enable DefaultRipple.
func (e *TestEnv) FundAmountNoRipple(acc *Account, amount uint64) {
	e.t.Helper()

	e.accounts[acc.Name] = acc

	master := e.accounts["master"]
	if master == nil {
		e.t.Fatal("Master account not found")
	}

	seq := e.Seq(master)
	pay := payment.NewPayment(master.Address, acc.Address, tx.NewXRPAmount(int64(amount)))
	pay.Fee = formatUint64(e.baseFee)
	pay.Sequence = &seq

	result := e.Submit(pay)
	if !result.Success {
		e.t.Fatalf("Failed to fund account %s (no ripple): %s", acc.Name, result.Code)
	}
}

// Noop submits a no-op AccountSet to bump an account's sequence number.
// Reference: rippled's noop(account) in noop.h
func (e *TestEnv) Noop(acc *Account) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed noop for %s: %s", acc.Name, result.Code)
	}
}

// NoopWithFee submits a no-op AccountSet with a custom fee.
// Reference: rippled's env(noop(account), fee(f))
func (e *TestEnv) NoopWithFee(acc *Account, fee uint64) {
	e.t.Helper()

	accountSet := account.NewAccountSet(acc.Address)
	accountSet.Fee = formatUint64(fee)
	seq := e.Seq(acc)
	accountSet.Sequence = &seq

	result := e.Submit(accountSet)
	if !result.Success {
		e.t.Fatalf("Failed noop for %s: %s", acc.Name, result.Code)
	}
}

// Trust creates a trust line and refunds the fee from master.
// Reference: rippled's Env::trust(amount, account) in Env.h
func (e *TestEnv) Trust(acc *Account, amount tx.Amount) {
	e.t.Helper()

	ts := trustset.NewTrustSet(acc.Address, amount)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to set trust line for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingCheck enables the DisallowIncomingCheck flag on an account.
func (e *TestEnv) EnableDisallowIncomingCheck(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingCheck
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingCheck for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingCheck disables the DisallowIncomingCheck flag on an account.
func (e *TestEnv) DisableDisallowIncomingCheck(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingCheck
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingCheck for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingPayChan enables the DisallowIncomingPayChan flag on an account.
func (e *TestEnv) EnableDisallowIncomingPayChan(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingPayChan
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingPayChan for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingPayChan disables the DisallowIncomingPayChan flag on an account.
func (e *TestEnv) DisableDisallowIncomingPayChan(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingPayChan
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingPayChan for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingNFTokenOffer enables the DisallowIncomingNFTokenOffer flag on an account.
func (e *TestEnv) EnableDisallowIncomingNFTokenOffer(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingNFTokenOffer
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingNFTokenOffer for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingNFTokenOffer disables the DisallowIncomingNFTokenOffer flag on an account.
func (e *TestEnv) DisableDisallowIncomingNFTokenOffer(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingNFTokenOffer
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingNFTokenOffer for %s: %s", acc.Name, result.Code)
	}
}

// EnableDisallowIncomingTrustline enables the DisallowIncomingTrustline flag on an account.
func (e *TestEnv) EnableDisallowIncomingTrustline(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingTrustline
	as.SetFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to enable DisallowIncomingTrustline for %s: %s", acc.Name, result.Code)
	}
}

// DisableDisallowIncomingTrustline disables the DisallowIncomingTrustline flag on an account.
func (e *TestEnv) DisableDisallowIncomingTrustline(acc *Account) {
	e.t.Helper()
	as := account.NewAccountSet(acc.Address)
	flag := account.AccountSetFlagDisallowIncomingTrustline
	as.ClearFlag = &flag
	as.Fee = formatUint64(e.baseFee)
	seq := e.Seq(acc)
	as.Sequence = &seq
	result := e.Submit(as)
	if !result.Success {
		e.t.Fatalf("Failed to disable DisallowIncomingTrustline for %s: %s", acc.Name, result.Code)
	}
}

// FreezeTrustLine freezes a specific trust line (issuer-side freeze).
// Reference: rippled's trust(account, amount, peer, tfSetFreeze)
func (e *TestEnv) FreezeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	// The issuer sets a trust line with the Freeze flag
	amount := sle.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFreeze()
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to freeze trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// UnfreezeTrustLine unfreezes a specific trust line.
func (e *TestEnv) UnfreezeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	amount := sle.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFlags(ts.GetFlags() | trustset.TrustSetFlagClearFreeze)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to unfreeze trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// AuthorizeTrustLine authorizes a trust line (when RequireAuth is set on the issuer).
// Reference: rippled's trust(account, amount, tfSetfAuth)
func (e *TestEnv) AuthorizeTrustLine(issuer, holder *Account, currency string) {
	e.t.Helper()

	amount := sle.NewIssuedAmountFromFloat64(0, currency, holder.Address)
	ts := trustset.NewTrustSet(issuer.Address, amount)
	ts.SetFlags(ts.GetFlags() | trustset.TrustSetFlagSetfAuth)
	ts.Fee = formatUint64(e.baseFee)
	seq := e.Seq(issuer)
	ts.Sequence = &seq

	result := e.Submit(ts)
	if !result.Success {
		e.t.Fatalf("Failed to authorize trust line %s/%s for %s: %s", currency, issuer.Name, holder.Name, result.Code)
	}
}

// IncLedgerSeqForAccDel closes enough ledgers so account deletion is allowed.
// rippled requires 256 ledgers after account creation before deletion.
// Reference: rippled's incLgrSeqForAccDel() in acctdelete.h
func (e *TestEnv) IncLedgerSeqForAccDel(acc *Account) {
	e.t.Helper()

	// AccountDelete requires the account's sequence to be at least 256 ledgers
	// behind the current ledger sequence. Close ledgers until this is satisfied.
	for e.LedgerSeq()-e.Seq(acc) < 256 {
		e.Close()
	}
}

// Limit returns the trust line limit for an account/issue.
// Returns 0 if the trust line doesn't exist.
// Reference: rippled's Env::limit(account, issue)
func (e *TestEnv) Limit(holder, issuer *Account, currency string) float64 {
	e.t.Helper()

	lineKey := keylet.Line(holder.ID, issuer.ID, currency)
	exists, err := e.ledger.Exists(lineKey)
	if err != nil || !exists {
		return 0
	}

	data, err := e.ledger.Read(lineKey)
	if err != nil {
		return 0
	}

	rs, err := sle.ParseRippleState(data)
	if err != nil {
		return 0
	}

	// Determine which side is the holder's limit
	isLow := keylet.IsLowAccount(holder.ID, issuer.ID)
	if isLow {
		return rs.LowLimit.Float64()
	}
	return rs.HighLimit.Float64()
}

// PayIOU sends an IOU payment from sender to receiver.
// The issuer is the gateway that issued the currency.
// Reference: rippled's env(pay(sender, receiver, amount))
func (e *TestEnv) PayIOU(sender, receiver *Account, issuer *Account, currency string, amount float64) {
	e.t.Helper()

	amt := tx.NewIssuedAmountFromFloat64(amount, currency, issuer.Address)
	payTx := payment.NewPayment(sender.Address, receiver.Address, amt)
	payTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(sender)
	payTx.Sequence = &seq

	result := e.Submit(payTx)
	if !result.Success {
		e.t.Fatalf("Failed to pay %f %s from %s to %s: %s", amount, currency, sender.Name, receiver.Name, result.Code)
	}
}

// PayIOUWithSendMax sends an IOU payment with a SendMax limit.
// Reference: rippled's env(pay(sender, receiver, amount), sendmax(max))
func (e *TestEnv) PayIOUWithSendMax(sender, receiver *Account, issuer *Account, currency string, amount, sendMax float64) {
	e.t.Helper()

	amt := tx.NewIssuedAmountFromFloat64(amount, currency, issuer.Address)
	maxAmt := tx.NewIssuedAmountFromFloat64(sendMax, currency, issuer.Address)
	payTx := payment.NewPayment(sender.Address, receiver.Address, amt)
	payTx.SendMax = &maxAmt
	payTx.Fee = formatUint64(e.baseFee)
	seq := e.Seq(sender)
	payTx.Sequence = &seq

	result := e.Submit(payTx)
	if !result.Success {
		e.t.Fatalf("Failed to pay %f %s (sendmax %f) from %s to %s: %s",
			amount, currency, sendMax, sender.Name, receiver.Name, result.Code)
	}
}

// SetTransferRateDirect modifies the TransferRate directly in ledger state.
// This bypasses transaction validation, allowing out-of-bounds rates for testing
// legacy MainNet accounts.
// Reference: rippled AccountSet_test.cpp lines 446-460 (env.app().openLedger().modify())
func (e *TestEnv) SetTransferRateDirect(acc *Account, rate uint32) {
	e.t.Helper()

	accountKey := keylet.Account(acc.ID)
	data, err := e.ledger.Read(accountKey)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to read account: %v", err)
	}

	accountRoot, err := sle.ParseAccountRoot(data)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to parse account: %v", err)
	}

	accountRoot.TransferRate = rate

	updated, err := sle.SerializeAccountRoot(accountRoot)
	if err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to serialize: %v", err)
	}

	if err := e.ledger.Update(accountKey, updated); err != nil {
		e.t.Fatalf("SetTransferRateDirect: failed to update: %v", err)
	}
}
