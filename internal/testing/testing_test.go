package testing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewAccount(t *testing.T) {
	// Test deterministic account creation
	alice1 := NewAccount("alice")
	alice2 := NewAccount("alice")

	// Same name should produce same account
	assert.Equal(t, alice1.Address, alice2.Address)
	assert.Equal(t, alice1.PublicKey, alice2.PublicKey)
	assert.Equal(t, alice1.PrivateKey, alice2.PrivateKey)

	// Different name should produce different account
	bob := NewAccount("bob")
	assert.NotEqual(t, alice1.Address, bob.Address)
}

func TestNewAccountWithKeyType(t *testing.T) {
	// Test secp256k1
	aliceSecp := NewAccountWithKeyType("alice", KeyTypeSecp256k1)
	assert.True(t, aliceSecp.IsSecp256k1())
	assert.False(t, aliceSecp.IsEd25519())

	// Test ed25519
	aliceEd := NewAccountWithKeyType("alice", KeyTypeEd25519)
	assert.True(t, aliceEd.IsEd25519())
	assert.False(t, aliceEd.IsSecp256k1())

	// Different key types should produce different addresses
	assert.NotEqual(t, aliceSecp.Address, aliceEd.Address)
}

func TestMasterAccount(t *testing.T) {
	master := MasterAccount()

	// Should be the well-known genesis account
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", master.Address)
	assert.Equal(t, "master", master.Name)
}

func TestAccountHuman(t *testing.T) {
	alice := NewAccount("alice")

	// Human() should return the address
	assert.Equal(t, alice.Address, alice.Human())
}

func TestAccountString(t *testing.T) {
	alice := NewAccount("alice")

	// String() should include name and address
	str := alice.String()
	assert.Contains(t, str, "alice")
	assert.Contains(t, str, alice.Address)
}

func TestXRPConversion(t *testing.T) {
	// 1 XRP = 1,000,000 drops
	assert.Equal(t, int64(1_000_000), XRP(1))
	assert.Equal(t, int64(100_000_000), XRP(100))
	assert.Equal(t, int64(1_000_000_000_000), XRP(1_000_000))
}

func TestDropsConversion(t *testing.T) {
	// Drops should pass through unchanged
	assert.Equal(t, int64(1000), Drops(1000))
	assert.Equal(t, int64(0), Drops(0))
}

func TestManualClock(t *testing.T) {
	clock := NewManualClock()

	// Default time should be Jan 1, 2020
	now := clock.Now()
	assert.Equal(t, 2020, now.Year())
	assert.Equal(t, time.January, now.Month())
	assert.Equal(t, 1, now.Day())

	// Advance time
	clock.Advance(10 * time.Second)
	now2 := clock.Now()
	assert.Equal(t, 10*time.Second, now2.Sub(now))

	// Set time
	newTime := time.Date(2025, 6, 15, 12, 0, 0, 0, time.UTC)
	clock.Set(newTime)
	assert.Equal(t, newTime, clock.Now())
}

func TestManualClockAt(t *testing.T) {
	startTime := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	clock := NewManualClockAt(startTime)

	assert.Equal(t, startTime, clock.Now())
}

func TestTxResult(t *testing.T) {
	// Success
	success := ResultSuccess()
	assert.True(t, success.IsSuccess())
	assert.False(t, success.IsClaimed())
	assert.False(t, success.IsRetry())
	assert.False(t, success.IsMalformed())
	assert.False(t, success.IsFailed())

	// Claimed (tec)
	claimed := ResultWithCode("tecUNFUNDED_PAYMENT", false, "insufficient funds")
	assert.False(t, claimed.IsSuccess())
	assert.True(t, claimed.IsClaimed())
	assert.False(t, claimed.IsRetry())

	// Retry (ter)
	retry := ResultWithCode("terPRE_SEQ", false, "pre-sequence")
	assert.False(t, retry.IsSuccess())
	assert.True(t, retry.IsRetry())

	// Malformed (tem)
	malformed := ResultWithCode("temMALFORMED", false, "malformed")
	assert.False(t, malformed.IsSuccess())
	assert.True(t, malformed.IsMalformed())

	// Failed (tef)
	failed := ResultWithCode("tefPAST_SEQ", false, "past sequence")
	assert.False(t, failed.IsSuccess())
	assert.True(t, failed.IsFailed())
}

func TestResultCodeCategory(t *testing.T) {
	assert.Equal(t, "success", ResultCodeCategory("tesSUCCESS"))
	assert.Equal(t, "claimed", ResultCodeCategory("tecUNFUNDED_PAYMENT"))
	assert.Equal(t, "failure", ResultCodeCategory("tefPAST_SEQ"))
	assert.Equal(t, "retry", ResultCodeCategory("terPRE_SEQ"))
	assert.Equal(t, "malformed", ResultCodeCategory("temMALFORMED"))
	assert.Equal(t, "unknown", ResultCodeCategory("xyz"))
	assert.Equal(t, "unknown", ResultCodeCategory("ab"))
}

func TestFormatBalance(t *testing.T) {
	formatted := FormatBalance(1_000_000)
	assert.Contains(t, formatted, "1.000000 XRP")
	assert.Contains(t, formatted, "1000000 drops")

	formatted2 := FormatBalance(100_500_000)
	assert.Contains(t, formatted2, "100.500000 XRP")
}

func TestIssuedCurrencyHelpers(t *testing.T) {
	gateway := NewAccount("gateway")

	// USD
	usd := USD(gateway, 100.50)
	assert.Equal(t, "USD", usd.Currency)
	assert.Equal(t, gateway.Address, usd.Issuer)
	assert.InDelta(t, 100.5, usd.Float64(), 0.0001)

	// EUR
	eur := EUR(gateway, 50.0)
	assert.Equal(t, "EUR", eur.Currency)
	assert.Equal(t, gateway.Address, eur.Issuer)

	// BTC
	btc := BTC(gateway, 0.001)
	assert.Equal(t, "BTC", btc.Currency)
	assert.Equal(t, gateway.Address, btc.Issuer)

	// Custom currency
	jpy := IssuedCurrency(gateway, "JPY", 1000.0)
	assert.Equal(t, "JPY", jpy.Currency)
	assert.Equal(t, gateway.Address, jpy.Issuer)
}

func TestXRPTxAmount(t *testing.T) {
	amount := XRPTxAmount(1_000_000)
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(1000000), amount.Drops())
}

func TestXRPTxAmountFromXRP(t *testing.T) {
	amount := XRPTxAmountFromXRP(100.0)
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(100000000), amount.Drops())
}

// TestNewTestEnv tests the basic TestEnv creation
// This test requires the ledger and genesis packages to be properly implemented
func TestNewTestEnv(t *testing.T) {
	env := NewTestEnv(t)
	require.NotNil(t, env)

	// Should have master account registered
	master := env.MasterAccount()
	require.NotNil(t, master)
	assert.Equal(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh", master.Address)

	// Should start at ledger sequence 2
	assert.Equal(t, uint32(2), env.LedgerSeq())

	// Should have default fees
	assert.Equal(t, uint64(10), env.BaseFee())
	assert.Equal(t, uint64(200_000_000), env.ReserveBase())
	assert.Equal(t, uint64(50_000_000), env.ReserveIncrement())
}
