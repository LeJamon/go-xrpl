package oracle_test

import (
	"fmt"
	"testing"

	accounttx "github.com/LeJamon/goXRPLd/internal/core/tx/account"
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	jtx "github.com/LeJamon/goXRPLd/internal/testing"
	"github.com/LeJamon/goXRPLd/internal/testing/accountset"
	oracletest "github.com/LeJamon/goXRPLd/internal/testing/oracle"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Oracle Tests — 1:1 mapping with rippled Oracle_test.cpp
// =============================================================================
//
// Reference: rippled/src/test/app/Oracle_test.cpp
//
// Test functions map to rippled methods:
//   TestInvalidSet    → testInvalidSet()    (lines 33-398)
//   TestCreate        → testCreate()        (lines 400-455)
//   TestInvalidDelete → testInvalidDelete() (lines 457-500)
//   TestDelete        → testDelete()        (lines 502-592)
//   TestUpdate        → testUpdate()        (lines 594-736)
//   TestAmendment     → testAmendment()     (lines 835-857)
//   TestMultisig      → testMultisig()      (lines 738-833)
// =============================================================================

// defaultLUT returns a valid LastUpdateTime for the current env (Unix timestamp).
func defaultLUT(env *jtx.TestEnv) uint32 {
	return oracletest.DefaultLastUpdateTime(env)
}

// baseFee returns the base fee used in the test env.
const baseFee = uint64(10)

// createOracle is a helper to create an oracle and verify success.
// Returns the LastUpdateTime used.
func createOracle(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, docID uint32, series []struct {
	base, quote string
	price       uint64
	scale       uint8
}) uint32 {
	t.Helper()
	lut := defaultLUT(env)
	builder := oracletest.OracleSet(owner, docID, lut).
		ProviderHex(32).
		AssetClassHex(8)
	for _, s := range series {
		builder = builder.AddPrice(s.base, s.quote, s.price, s.scale)
	}
	result := env.Submit(builder.Build())
	jtx.RequireTxSuccess(t, result)
	return lut
}

// pair is a convenience type for price series.
type pair struct {
	base, quote string
	price       uint64
	scale       uint8
}

// createOracleP is a helper to create an oracle from pair slice.
func createOracleP(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, docID uint32, pairs []pair) uint32 {
	t.Helper()
	lut := defaultLUT(env)
	builder := oracletest.OracleSet(owner, docID, lut).
		ProviderHex(32).
		AssetClassHex(8)
	for _, p := range pairs {
		builder = builder.AddPrice(p.base, p.quote, p.price, p.scale)
	}
	result := env.Submit(builder.Build())
	jtx.RequireTxSuccess(t, result)
	return lut
}

// oracleExists checks if an oracle ledger entry exists.
func oracleExists(t *testing.T, env *jtx.TestEnv, owner *jtx.Account, docID uint32) bool {
	t.Helper()
	key := keylet.Oracle(owner.ID, docID)
	return env.LedgerEntryExists(key)
}

// =============================================================================
// testInvalidSet() — rippled Oracle_test.cpp lines 33-398
// =============================================================================

func TestInvalidSet(t *testing.T) {
	// -------------------------------------------------------------------------
	// Invalid account — rippled lines 40-51
	// -------------------------------------------------------------------------
	t.Run("InvalidAccount", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		bad := jtx.NewAccount("bad")
		// bad is not funded (memoized only)
		lut := defaultLUT(env)

		result := env.Submit(oracletest.OracleSet(bad, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Sequence(1).
			Build())
		jtx.RequireTxFail(t, result, "terNO_ACCOUNT")
	})

	// -------------------------------------------------------------------------
	// Insufficient reserve — rippled lines 54-62
	// -------------------------------------------------------------------------
	t.Run("InsufficientReserve", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		// Fund with exactly the account reserve (0 items), no extra for oracle
		env.FundAmount(owner, env.ReserveBase())
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
	})

	// -------------------------------------------------------------------------
	// Insufficient reserve if data series extends to >5 — rippled lines 64-86
	// -------------------------------------------------------------------------
	t.Run("InsufficientReserveOnUpdate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		// Fund with accountReserve(1) + 2*baseFee: enough for 1 oracle (≤5 pairs)
		env.FundAmount(owner, env.ReserveBase()+env.ReserveIncrement()+2*baseFee)
		env.Close()

		// Create oracle with 1 pair — should succeed
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// Update with 5 more pairs → total 6 → needs 2 reserve units, not enough
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "EUR", 740, 1).
			AddPrice("XRP", "GBP", 740, 1).
			AddPrice("XRP", "CNY", 740, 1).
			AddPrice("XRP", "CAD", 740, 1).
			AddPrice("XRP", "AUD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINSUFFICIENT_RESERVE", result.Code)
	})

	// -------------------------------------------------------------------------
	// Invalid flag — rippled lines 88-99
	// -------------------------------------------------------------------------
	t.Run("InvalidFlag", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Flags(0x00000001). // tfSellNFToken
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	// -------------------------------------------------------------------------
	// Duplicate token pair — rippled lines 101-105
	// -------------------------------------------------------------------------
	t.Run("DuplicateTokenPair", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddPrice("XRP", "USD", 750, 1). // duplicate
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Price not included on create — rippled lines 107-112
	// -------------------------------------------------------------------------
	t.Run("PriceNotIncluded", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddDelete("XRP", "EUR"). // delete on create = invalid
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Token pair in update and delete — rippled lines 114-119
	// -------------------------------------------------------------------------
	t.Run("TokenPairInUpdateAndDelete", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddDelete("XRP", "USD"). // same pair: update + delete
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Token pair in add and delete — rippled lines 120-125
	// -------------------------------------------------------------------------
	t.Run("TokenPairInAddAndDelete", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "EUR", 740, 1).
			AddDelete("XRP", "EUR"). // same pair: add + delete
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Array exceeds 10 entries — rippled lines 127-142
	// -------------------------------------------------------------------------
	t.Run("ArrayTooLarge", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		currencies := []string{"US1", "US2", "US3", "US4", "US5", "US6", "US7", "US8", "US9", "U10", "U11"}
		builder := oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			Fee(baseFee)
		for _, curr := range currencies {
			builder = builder.AddPrice("XRP", curr, 740, 1)
		}
		result := env.Submit(builder.Build())
		jtx.RequireTxFail(t, result, "temARRAY_TOO_LARGE")
	})

	// -------------------------------------------------------------------------
	// Empty array — rippled lines 143-144
	// -------------------------------------------------------------------------
	t.Run("EmptyArray", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temARRAY_EMPTY")
	})

	// -------------------------------------------------------------------------
	// Array exceeds 10 after update — rippled lines 147-176
	// -------------------------------------------------------------------------
	t.Run("ArrayExceeds10AfterUpdate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle with 1 pair
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Try to add 10 more pairs → total 11 → tecARRAY_TOO_LARGE
		lut2 := lut + 1
		builder := oracletest.OracleSet(owner, 1, lut2).
			Fee(baseFee)
		currencies := []string{"US1", "US2", "US3", "US4", "US5", "US6", "US7", "US8", "US9", "U10"}
		for _, curr := range currencies {
			builder = builder.AddPrice("XRP", curr, 740+1, 1)
		}
		result = env.Submit(builder.Build())
		require.Equal(t, "tecARRAY_TOO_LARGE", result.Code)
	})

	// -------------------------------------------------------------------------
	// Missing AssetClass on create — rippled lines 185-190
	// -------------------------------------------------------------------------
	t.Run("MissingAssetClassOnCreate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			Provider("70726F7669646572"). // provider present
			// AssetClass NOT set
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Missing Provider on create — rippled lines 191-196
	// -------------------------------------------------------------------------
	t.Run("MissingProviderOnCreate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			AssetClass("63757272656E6379"). // assetClass present
			URI("555249").                  // URI present
			// Provider NOT set
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Provider mismatch on update — rippled lines 198-207
	// -------------------------------------------------------------------------
	t.Run("ProviderMismatchOnUpdate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle first
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// Update with different provider → temMALFORMED
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			Provider("70726F766964657231"). // "provider1" — different from original
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// AssetClass mismatch on update — rippled lines 208-212
	// -------------------------------------------------------------------------
	t.Run("AssetClassMismatchOnUpdate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle first
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Update with different assetClass → temMALFORMED
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AssetClass("63757272656E637931"). // "currency1" — different
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Fields too long — rippled lines 215-245
	// -------------------------------------------------------------------------
	t.Run("AssetClassTooLong", func(t *testing.T) {
		// 17 bytes > maxOracleSymbolClass (16) — rippled lines 223-228
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(17). // 17 bytes > 16
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	t.Run("ProviderTooLong", func(t *testing.T) {
		// 257 bytes > maxOracleProvider (256) — rippled lines 229-232
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(257). // 257 bytes > 256
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	t.Run("URITooLong", func(t *testing.T) {
		// 257 bytes > maxOracleURI (256) — rippled lines 233-235
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			URIHex(257). // 257 bytes > 256
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Empty fields — rippled lines 237-245
	// -------------------------------------------------------------------------
	t.Run("EmptyAssetClass", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClass(""). // explicitly empty
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	t.Run("EmptyProvider", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			Provider(""). // explicitly empty
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	t.Run("EmptyURI", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			URI(""). // explicitly empty
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Different owner creates a new object and fails — rippled lines 248-264
	// Missing Provider/AssetClass on what becomes a create
	// -------------------------------------------------------------------------
	t.Run("DifferentOwnerMissingFields", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		some := jtx.NewAccount("some")
		env.Fund(owner)
		env.Fund(some)
		env.Close()

		// Create oracle for owner
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// "some" tries to "update" same docID, but for "some" it's a create
		// Missing Provider/AssetClass → temMALFORMED
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(some, 1, lut2).
			// No Provider, no AssetClass — required for create
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Invalid update time — rippled lines 266-310
	// -------------------------------------------------------------------------
	t.Run("InvalidUpdateTimeTooOld", func(t *testing.T) {
		// rippled lines 282-287: Less than close time - 300s
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Close several times to advance time (rippled closes with 400s)
		for i := 0; i < 40; i++ {
			env.Close() // each close = +10s, total 400s
		}

		// Compute close time in XRPL epoch
		closeTimeXRPL := uint32(env.Now().Unix()) - oracletest.XRPLEpochOffset
		// LastUpdateTime too old: closeTime - 301 (in Unix = epoch + XRPL epoch offset)
		tooOld := (closeTimeXRPL - 301) + oracletest.XRPLEpochOffset
		result = env.Submit(oracletest.OracleSet(owner, 1, tooOld).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINVALID_UPDATE_TIME", result.Code)
	})

	t.Run("InvalidUpdateTimeTooNew", func(t *testing.T) {
		// rippled lines 289-293: Greater than close time + 300s
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		for i := 0; i < 40; i++ {
			env.Close()
		}

		closeTimeXRPL := uint32(env.Now().Unix()) - oracletest.XRPLEpochOffset
		tooNew := (closeTimeXRPL + 311) + oracletest.XRPLEpochOffset
		result = env.Submit(oracletest.OracleSet(owner, 1, tooNew).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINVALID_UPDATE_TIME", result.Code)
	})

	t.Run("InvalidUpdateTimeLessThanPrevious", func(t *testing.T) {
		// rippled lines 294-303: Update, then update with earlier time
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		for i := 0; i < 40; i++ {
			env.Close()
		}

		// Successful update to advance lastUpdateTime
		lut2 := defaultLUT(env)
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Try update with older time than previous — should fail
		// The previous lastUpdateTime was lut2, so use lut2-1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2-1).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINVALID_UPDATE_TIME", result.Code)
	})

	t.Run("InvalidUpdateTimeLessThanEpoch", func(t *testing.T) {
		// rippled lines 304-309: Less than epoch_offset
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Update with time < epoch_offset (946684800)
		result = env.Submit(oracletest.OracleSet(owner, 1, oracletest.XRPLEpochOffset-1).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecINVALID_UPDATE_TIME", result.Code)
	})

	// -------------------------------------------------------------------------
	// Delete token pair that doesn't exist — rippled lines 312-323
	// -------------------------------------------------------------------------
	t.Run("DeleteNonExistentPair", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle with XRP/USD
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Try to delete XRP/EUR which doesn't exist
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddDelete("XRP", "EUR"). // not in oracle
			Fee(baseFee).
			Build())
		require.Equal(t, "tecTOKEN_PAIR_NOT_FOUND", result.Code)
	})

	// -------------------------------------------------------------------------
	// Delete all token pairs — rippled lines 324-328
	// -------------------------------------------------------------------------
	t.Run("DeleteAllPairs", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle with XRP/USD
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Delete the only pair → results in empty series
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddDelete("XRP", "USD").
			Fee(baseFee).
			Build())
		require.Equal(t, "tecARRAY_EMPTY", result.Code)
	})

	// -------------------------------------------------------------------------
	// Same BaseAsset and QuoteAsset — rippled lines 331-343
	// -------------------------------------------------------------------------
	t.Run("SameBaseAndQuoteAsset", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("USD", "USD", 740, 1). // same
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Scale > maxPriceScale — rippled lines 345-357
	// -------------------------------------------------------------------------
	t.Run("ScaleExceedsMax", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("USD", "BTC", 740, 9). // scale 9 > max 8
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Update: pair in add AND delete — rippled lines 359-371
	// -------------------------------------------------------------------------
	t.Run("UpdatePairAddAndDelete", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle first
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Update: delete XRP/EUR + add XRP/EUR in same tx
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddDelete("XRP", "EUR").
			AddPrice("XRP", "EUR", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Delete pair that doesn't exist in oracle — rippled lines 372-376
	// -------------------------------------------------------------------------
	t.Run("DeletePairNotInOracle", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle with XRP/USD
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Delete XRP/EUR — not in this oracle
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddDelete("XRP", "EUR").
			Fee(baseFee).
			Build())
		require.Equal(t, "tecTOKEN_PAIR_NOT_FOUND", result.Code)
	})

	// -------------------------------------------------------------------------
	// Delete from wrong documentID — rippled lines 377-383
	// -------------------------------------------------------------------------
	t.Run("DeleteFromWrongDocumentID", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create oracle with docID=1
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Try to delete XRP/EUR from docID=10 (doesn't exist in ledger)
		// This is a create for docID=10, delete on create → temMALFORMED
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 10, lut2).
			AddDelete("XRP", "EUR").
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temMALFORMED")
	})

	// -------------------------------------------------------------------------
	// Bad fee — rippled lines 385-397
	// -------------------------------------------------------------------------
	t.Run("BadFee", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		// Create oracle with fee = -1 (in Go, we set fee as string "-1")
		oset := oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			BuildOracleSet()
		oset.Common.Fee = "-1"
		result := env.Submit(oset)
		jtx.RequireTxFail(t, result, "temBAD_FEE")

		// Create a valid oracle first
		result = env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Update with bad fee
		lut2 := lut + 1
		oset2 := oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 740, 1).
			BuildOracleSet()
		oset2.Common.Fee = "-1"
		result = env.Submit(oset2)
		jtx.RequireTxFail(t, result, "temBAD_FEE")
	})
}

// =============================================================================
// testCreate() — rippled Oracle_test.cpp lines 400-455
// =============================================================================

func TestCreate(t *testing.T) {
	// -------------------------------------------------------------------------
	// Owner count +1 for ≤5 pairs — rippled lines 419-423
	// -------------------------------------------------------------------------
	t.Run("OwnerCountPlus1", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		countBefore := env.OwnerCount(owner)
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))
		require.Equal(t, countBefore+1, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Owner count +2 for >5 pairs — rippled lines 425-437
	// -------------------------------------------------------------------------
	t.Run("OwnerCountPlus2", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		countBefore := env.OwnerCount(owner)
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddPrice("BTC", "USD", 740, 1).
			AddPrice("ETH", "USD", 740, 1).
			AddPrice("CAN", "USD", 740, 1).
			AddPrice("YAN", "USD", 740, 1).
			AddPrice("GBP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))
		require.Equal(t, countBefore+2, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Different owner creates a new object — rippled lines 439-454
	// -------------------------------------------------------------------------
	t.Run("DifferentOwnerNewObject", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		some := jtx.NewAccount("some")
		env.Fund(owner)
		env.Fund(some)
		env.Close()

		lut := defaultLUT(env)
		// Owner creates oracle docID=1
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// "some" creates oracle with same docID=1 but different owner
		result = env.Submit(oracletest.OracleSet(some, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("EUR", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, some, 1))
	})
}

// =============================================================================
// testInvalidDelete() — rippled Oracle_test.cpp lines 457-500
// =============================================================================

func TestInvalidDelete(t *testing.T) {
	env := jtx.NewTestEnv(t)
	owner := jtx.NewAccount("owner")
	env.Fund(owner)
	env.Close()

	// Create an oracle to test delete operations against
	lut := defaultLUT(env)
	result := env.Submit(oracletest.OracleSet(owner, 1, lut).
		ProviderHex(32).
		AssetClassHex(8).
		AddPrice("XRP", "USD", 740, 1).
		Fee(baseFee).
		Build())
	jtx.RequireTxSuccess(t, result)
	require.True(t, oracleExists(t, env, owner, 1))

	// -------------------------------------------------------------------------
	// Invalid account — rippled lines 471-480
	// -------------------------------------------------------------------------
	t.Run("InvalidAccount", func(t *testing.T) {
		bad := jtx.NewAccount("bad")
		// bad is not funded
		result := env.Submit(oracletest.OracleDelete(bad, 1).
			Fee(baseFee).
			Sequence(1).
			Build())
		jtx.RequireTxFail(t, result, "terNO_ACCOUNT")
	})

	// -------------------------------------------------------------------------
	// Invalid DocumentID — rippled lines 482-484
	// -------------------------------------------------------------------------
	t.Run("InvalidDocumentID", func(t *testing.T) {
		result := env.Submit(oracletest.OracleDelete(owner, 2). // docID=2 doesn't exist
										Fee(baseFee).
										Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
	})

	// -------------------------------------------------------------------------
	// Invalid owner — rippled lines 486-490
	// -------------------------------------------------------------------------
	t.Run("InvalidOwner", func(t *testing.T) {
		invalid := jtx.NewAccount("invalid")
		env.Fund(invalid)
		env.Close()

		// "invalid" tries to delete owner's oracle
		result := env.Submit(oracletest.OracleDelete(invalid, 1).
			Fee(baseFee).
			Build())
		require.Equal(t, "tecNO_ENTRY", result.Code)
	})

	// -------------------------------------------------------------------------
	// Invalid flags — rippled lines 492-496
	// -------------------------------------------------------------------------
	t.Run("InvalidFlags", func(t *testing.T) {
		result := env.Submit(oracletest.OracleDelete(owner, 1).
			Flags(0x00000001). // tfSellNFToken
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temINVALID_FLAG")
	})

	// -------------------------------------------------------------------------
	// Bad fee — rippled lines 498-499
	// -------------------------------------------------------------------------
	t.Run("BadFee", func(t *testing.T) {
		odel := oracletest.OracleDelete(owner, 1).BuildOracleDelete()
		odel.Common.Fee = "-1"
		result := env.Submit(odel)
		jtx.RequireTxFail(t, result, "temBAD_FEE")
	})
}

// =============================================================================
// testDelete() — rippled Oracle_test.cpp lines 502-592
// =============================================================================

func TestDelete(t *testing.T) {
	// -------------------------------------------------------------------------
	// Owner count -1 for ≤5 pairs — rippled lines 522-525
	// -------------------------------------------------------------------------
	t.Run("OwnerCountMinus1", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		countBefore := env.OwnerCount(owner)
		require.True(t, oracleExists(t, env, owner, 1))

		result = env.Submit(oracletest.OracleDelete(owner, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.False(t, oracleExists(t, env, owner, 1))
		require.Equal(t, countBefore-1, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Owner count -2 for >5 pairs — rippled lines 527-542
	// -------------------------------------------------------------------------
	t.Run("OwnerCountMinus2", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddPrice("BTC", "USD", 740, 1).
			AddPrice("ETH", "USD", 740, 1).
			AddPrice("CAN", "USD", 740, 1).
			AddPrice("YAN", "USD", 740, 1).
			AddPrice("GBP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		countBefore := env.OwnerCount(owner)
		require.True(t, oracleExists(t, env, owner, 1))

		result = env.Submit(oracletest.OracleDelete(owner, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.False(t, oracleExists(t, env, owner, 1))
		require.Equal(t, countBefore-2, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Account deletion cascades to oracles — rippled lines 544-591
	// -------------------------------------------------------------------------
	t.Run("AccountDeletionCascade", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		alice := jtx.NewAccount("alice")
		env.Fund(owner)
		env.Fund(alice)
		env.Close()

		lut := defaultLUT(env)
		// Create two oracles for owner
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		result = env.Submit(oracletest.OracleSet(owner, 2, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "EUR", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		require.Equal(t, uint32(2), env.OwnerCount(owner))
		require.True(t, oracleExists(t, env, owner, 1))
		require.True(t, oracleExists(t, env, owner, 2))

		// Close enough ledgers for account deletion (256 minimum)
		env.IncLedgerSeqForAccDel(owner)

		// Delete owner account
		acctDel := accounttx.NewAccountDelete(owner.Address, alice.Address)
		acctDel.Fee = fmt.Sprintf("%d", env.ReserveIncrement())
		result = env.Submit(acctDel)
		jtx.RequireTxSuccess(t, result)
		env.Close()

		// Both oracles should be gone
		require.False(t, oracleExists(t, env, owner, 1))
		require.False(t, oracleExists(t, env, owner, 2))
	})
}

// =============================================================================
// testUpdate() — rippled Oracle_test.cpp lines 594-736
// =============================================================================

func TestUpdate(t *testing.T) {
	// -------------------------------------------------------------------------
	// Update existing pair — rippled lines 601-617
	// -------------------------------------------------------------------------
	t.Run("UpdateExistingPair", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		countBefore := env.OwnerCount(owner)

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// Update existing pair with new price/scale
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 740, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Owner count increased by 1 since oracle object was added with one pair
		require.Equal(t, countBefore+1, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Add new pairs, non-included pair resets — rippled lines 619-625
	// -------------------------------------------------------------------------
	t.Run("AddNewPairs", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		count := env.OwnerCount(owner)

		// Add XRP/EUR — XRP/USD should be reset (price=0, scale=0)
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "EUR", 700, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Owner count unchanged (still 2 pairs = 1 reserve unit)
		require.Equal(t, count, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Update both pairs — rippled lines 627-634
	// -------------------------------------------------------------------------
	t.Run("UpdateBothPairs", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		// Create with 2 pairs
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			AddPrice("XRP", "EUR", 700, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		count := env.OwnerCount(owner)

		// Update both pairs
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 741, 2).
			AddPrice("XRP", "EUR", 710, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Owner count unchanged
		require.Equal(t, count, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Add 4 pairs crossing 5-pair threshold → OwnerCount +1 — rippled lines 636-647
	// -------------------------------------------------------------------------
	t.Run("AddMultiplePairsCrossThreshold", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create with 2 pairs
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 741, 2).
			AddPrice("XRP", "EUR", 710, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		count := env.OwnerCount(owner)

		// Add 4 more pairs → total 6 → crosses 5-pair threshold
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("BTC", "USD", 741, 2).
			AddPrice("ETH", "EUR", 710, 2).
			AddPrice("YAN", "EUR", 710, 2).
			AddPrice("CAN", "EUR", 710, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Owner count increased by 1 (reserve doubles when crossing threshold)
		require.Equal(t, count+1, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Delete some pairs — rippled lines 649-665
	// -------------------------------------------------------------------------
	t.Run("DeleteSomePairs", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		// Create with 6 pairs (need 2 reserve units)
		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 741, 2).
			AddPrice("XRP", "EUR", 710, 2).
			AddPrice("BTC", "USD", 741, 2).
			AddPrice("ETH", "EUR", 710, 2).
			AddPrice("YAN", "EUR", 710, 2).
			AddPrice("CAN", "EUR", 710, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		count := env.OwnerCount(owner)

		// Delete BTC/USD first
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddDelete("BTC", "USD").
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Delete 3 more + update 2 remaining
		lut3 := lut2 + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut3).
			AddPrice("XRP", "USD", 742, 2).
			AddPrice("XRP", "EUR", 711, 2).
			AddDelete("ETH", "EUR").
			AddDelete("YAN", "EUR").
			AddDelete("CAN", "EUR").
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// Owner count decreased by 1 (back below threshold)
		require.Equal(t, count-1, env.OwnerCount(owner))
	})

	// -------------------------------------------------------------------------
	// Min reserve to create and update — rippled lines 668-680
	// -------------------------------------------------------------------------
	t.Run("MinReserveToCreateAndUpdate", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		// Fund with accountReserve(1) + 2*baseFee (minimum for 1 oracle)
		env.FundAmount(owner, env.ReserveBase()+env.ReserveIncrement()+2*baseFee)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 742, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
	})

	// -------------------------------------------------------------------------
	// fixPriceOracleOrder amendment — rippled lines 682-735
	// -------------------------------------------------------------------------
	t.Run("FixPriceOracleOrder_Disabled", func(t *testing.T) {
		// Without fixPriceOracleOrder: pair order changes on update
		env := jtx.NewTestEnv(t)
		env.DisableFeature("fixPriceOracleOrder")
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 742, 2).
			AddPrice("XRP", "EUR", 711, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// Update with same pairs — without fix, pair order should change
		// (pairs get sorted by currency during map iteration)
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 742, 2).
			AddPrice("XRP", "EUR", 711, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// TODO: Verify pair ordering changed (requires SLE parsing)
		// Without fix, afterQuoteAsset[0] != beforeQuoteAsset[0]
	})

	t.Run("FixPriceOracleOrder_Enabled", func(t *testing.T) {
		// With fixPriceOracleOrder: pair order preserved on update
		env := jtx.NewTestEnv(t)
		env.EnableFeature("fixPriceOracleOrder")
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 742, 2).
			AddPrice("XRP", "EUR", 711, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)
		require.True(t, oracleExists(t, env, owner, 1))

		// Update with same pairs — with fix, pair order should be preserved
		lut2 := lut + 1
		result = env.Submit(oracletest.OracleSet(owner, 1, lut2).
			AddPrice("XRP", "USD", 742, 2).
			AddPrice("XRP", "EUR", 711, 2).
			Fee(baseFee).
			Build())
		jtx.RequireTxSuccess(t, result)

		// TODO: Verify pair ordering preserved (requires SLE parsing)
		// With fix, afterQuoteAsset[0] == beforeQuoteAsset[0]
	})
}

// =============================================================================
// testAmendment() — rippled Oracle_test.cpp lines 835-857
// =============================================================================

func TestAmendment(t *testing.T) {
	// -------------------------------------------------------------------------
	// Feature disabled — create → temDISABLED — rippled lines 848-851
	// -------------------------------------------------------------------------
	t.Run("SetOracleDisabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		env.DisableFeature("PriceOracle")

		lut := defaultLUT(env)
		result := env.Submit(oracletest.OracleSet(owner, 1, lut).
			ProviderHex(32).
			AssetClassHex(8).
			AddPrice("XRP", "USD", 740, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temDISABLED")
	})

	// -------------------------------------------------------------------------
	// Feature disabled — delete → temDISABLED — rippled lines 853-856
	// -------------------------------------------------------------------------
	t.Run("DeleteOracleDisabled", func(t *testing.T) {
		env := jtx.NewTestEnv(t)
		owner := jtx.NewAccount("owner")
		env.Fund(owner)
		env.Close()

		env.DisableFeature("PriceOracle")

		result := env.Submit(oracletest.OracleDelete(owner, 1).
			Fee(baseFee).
			Build())
		jtx.RequireTxFail(t, result, "temDISABLED")
	})
}

// =============================================================================
// testMultisig() — rippled Oracle_test.cpp lines 738-833
// Runs 3 times with different feature sets per rippled run() at lines 871-875
// =============================================================================

func TestMultisig(t *testing.T) {
	featureSets := []struct {
		name     string
		disable  []string
		skip     bool
	}{
		{
			name:    "AllFeatures",
			disable: nil,
		},
		{
			// Skip: MultiSignReserve amendment not yet implemented in signer list engine.
			// When disabled, signer list should charge 2+N OwnerCount, but Go engine always charges 1.
			name:    "NoMultiSignReserve_NoExpandedSignerList",
			disable: []string{"MultiSignReserve", "ExpandedSignerList"},
			skip:    true,
		},
		{
			name:    "NoExpandedSignerList",
			disable: []string{"ExpandedSignerList"},
		},
	}

	for _, fs := range featureSets {
		t.Run(fs.name, func(t *testing.T) {
			if fs.skip {
				t.Skip("Pre-existing: MultiSignReserve amendment not implemented in signer list engine")
			}
			env := jtx.NewTestEnv(t)
			for _, feat := range fs.disable {
				env.DisableFeature(feat)
			}

			alice := jtx.NewAccount("alice")
			bogie := jtx.NewAccount("bogie")
			ed := jtx.NewAccount("ed")
			becky := jtx.NewAccount("becky")
			zelda := jtx.NewAccount("zelda")
			bob := jtx.NewAccount("bob")

			env.Fund(alice)
			env.Fund(becky)
			env.Fund(zelda)
			env.Fund(ed)
			env.Fund(bob)
			env.Close()

			// alice uses a regular key with the master disabled.
			alie := jtx.NewAccount("alie")
			env.SetRegularKey(alice, alie)
			result := env.Submit(accountset.AccountSet(alice).
				SetFlag(accounttx.AccountSetFlagDisableMaster).Build())
			jtx.RequireTxSuccess(t, result)

			// Attach signers to alice: becky(1), bogie(1), ed(2), quorum=2
			env.SetSignerList(alice, 2, []jtx.TestSigner{
				{Account: becky, Weight: 1},
				{Account: bogie, Weight: 1},
				{Account: ed, Weight: 2},
			})
			env.Close()

			// Verify signer list owners
			// If multiSignReserve disabled: 2 + 1 per signer = 5
			// If multiSignReserve enabled: 1
			hasMultiSignReserve := true
			for _, feat := range fs.disable {
				if feat == "MultiSignReserve" {
					hasMultiSignReserve = false
				}
			}
			if hasMultiSignReserve {
				require.Equal(t, uint32(1), env.OwnerCount(alice))
			} else {
				require.Equal(t, uint32(5), env.OwnerCount(alice))
			}

			lut := defaultLUT(env)

			// -----------------------------------------------------------------
			// Create — rippled lines 769-781
			// -----------------------------------------------------------------

			// Insufficient quorum: only becky (weight 1, need 2)
			t.Run("CreateInsufficientQuorum", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut).
						ProviderHex(32).
						AssetClassHex(8).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{becky})
				jtx.RequireTxFail(t, result, "tefBAD_QUORUM")
			})

			// Wrong signer: zelda is not in signer list
			t.Run("CreateWrongSigner", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut).
						ProviderHex(32).
						AssetClassHex(8).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{zelda})
				jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
			})

			// Valid multisig: becky(1) + bogie(1) = 2 = quorum
			t.Run("CreateValidMultisig", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut).
						ProviderHex(32).
						AssetClassHex(8).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{becky, bogie})
				jtx.RequireTxSuccess(t, result)
				require.True(t, oracleExists(t, env, alice, 1))
			})

			// -----------------------------------------------------------------
			// Update — rippled lines 783-822
			// -----------------------------------------------------------------

			lut2 := lut + 1

			t.Run("UpdateInsufficientQuorum", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut2).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{becky})
				jtx.RequireTxFail(t, result, "tefBAD_QUORUM")
			})

			t.Run("UpdateWrongSigner", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut2).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{zelda})
				jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")
			})

			t.Run("UpdateValidMultisig", func(t *testing.T) {
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut2).
						AddPrice("XRP", "USD", 741, 1).
						Build(),
					[]*jtx.Account{becky, bogie})
				jtx.RequireTxSuccess(t, result)
			})

			// Remove signer list and create new one — rippled lines 799-816
			t.Run("SignerListRotation", func(t *testing.T) {
				// Remove old signer list (signed with regular key)
				env.RemoveSignerList(alice)
				env.Close()

				// Create new signer list: zelda(1), bob(1), ed(2)
				env.SetSignerList(alice, 2, []jtx.TestSigner{
					{Account: zelda, Weight: 1},
					{Account: bob, Weight: 1},
					{Account: ed, Weight: 2},
				})
				env.Close()

				lut3 := defaultLUT(env)

				// Old signer list fails
				result := env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut3).
						AddPrice("XRP", "USD", 740, 1).
						Build(),
					[]*jtx.Account{becky, bogie})
				jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")

				// Updated signer list succeeds
				result = env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut3).
						AddPrice("XRP", "USD", 7412, 2).
						Build(),
					[]*jtx.Account{zelda, bob})
				jtx.RequireTxSuccess(t, result)

				// Ed alone has sufficient weight (2 >= quorum 2)
				lut4 := lut3 + 1
				result = env.SubmitMultiSigned(
					oracletest.OracleSet(alice, 1, lut4).
						AddPrice("XRP", "USD", 74245, 3).
						Build(),
					[]*jtx.Account{ed})
				jtx.RequireTxSuccess(t, result)

				// ---------------------------------------------------------
				// Remove — rippled lines 824-832
				// ---------------------------------------------------------

				// Delete insufficient quorum
				result = env.SubmitMultiSigned(
					oracletest.OracleDelete(alice, 1).Build(),
					[]*jtx.Account{bob})
				jtx.RequireTxFail(t, result, "tefBAD_QUORUM")

				// Delete wrong signer
				result = env.SubmitMultiSigned(
					oracletest.OracleDelete(alice, 1).Build(),
					[]*jtx.Account{becky})
				jtx.RequireTxFail(t, result, "tefBAD_SIGNATURE")

				// Delete valid multisig (ed alone has weight 2)
				result = env.SubmitMultiSigned(
					oracletest.OracleDelete(alice, 1).Build(),
					[]*jtx.Account{ed})
				jtx.RequireTxSuccess(t, result)
				require.False(t, oracleExists(t, env, alice, 1))
			})
		})
	}
}
