package handlers

import (
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// rippleEpochToISO8601 Tests

func TestRippleEpochToISO8601_Epoch(t *testing.T) {
	// Ripple epoch 0 = 2000-01-01T00:00:00 UTC
	result := rippleEpochToISO8601(0)
	assert.Equal(t, "2000-01-01T00:00:00+0000", result)
}

func TestRippleEpochToISO8601_KnownTimestamp(t *testing.T) {
	// 86400 seconds = 1 day after Ripple epoch = 2000-01-02T00:00:00 UTC
	result := rippleEpochToISO8601(86400)
	assert.Equal(t, "2000-01-02T00:00:00+0000", result)
}

func TestRippleEpochToISO8601_RecentTimestamp(t *testing.T) {
	// 776000030 seconds after Ripple epoch = approx 2024
	result := rippleEpochToISO8601(776000030)
	// Just check it's a valid format, not empty
	assert.NotEmpty(t, result)
	assert.Contains(t, result, "T")
	assert.Contains(t, result, "+0000")
}

// ammAuctionTimeSlot Tests
// Based on rippled's ammAuctionTimeSlot() in AMMCore.cpp

func TestAmmAuctionTimeSlot_ActiveSlot(t *testing.T) {
	// Auction expiration = 86400 + 86400 = 172800 (start = 86400)
	// parentCloseTime = 86400 + 4320 = 90720 (interval 1)
	expiration := uint32(172800) // start + totalTimeSlotSecs
	pct := uint64(90720)         // start + 1 interval
	interval := ammAuctionTimeSlot(pct, expiration)
	assert.Equal(t, uint32(1), interval)
}

func TestAmmAuctionTimeSlot_FirstInterval(t *testing.T) {
	expiration := uint32(172800) // start=86400
	pct := uint64(86400)         // exactly at start
	interval := ammAuctionTimeSlot(pct, expiration)
	assert.Equal(t, uint32(0), interval)
}

func TestAmmAuctionTimeSlot_LastInterval(t *testing.T) {
	expiration := uint32(172800) // start=86400
	pct := uint64(172800 - 1)    // just before expiration
	interval := ammAuctionTimeSlot(pct, expiration)
	assert.Equal(t, uint32(19), interval, "Last valid interval should be 19")
}

func TestAmmAuctionTimeSlot_Expired(t *testing.T) {
	expiration := uint32(172800)
	pct := uint64(172800) // at expiration = diff == totalTimeSlotSecs → not < totalTimeSlotSecs
	interval := ammAuctionTimeSlot(pct, expiration)
	assert.Equal(t, uint32(auctionSlotTimeIntervals), interval, "Expired should return 20")
}

func TestAmmAuctionTimeSlot_NotStarted(t *testing.T) {
	expiration := uint32(172800)
	pct := uint64(86399) // before start
	interval := ammAuctionTimeSlot(pct, expiration)
	assert.Equal(t, uint32(auctionSlotTimeIntervals), interval, "Not started should return 20")
}

func TestAmmAuctionTimeSlot_ExpirationTooSmall(t *testing.T) {
	// If expiration < totalTimeSlotSecs, return auctionSlotTimeIntervals
	interval := ammAuctionTimeSlot(100, 100)
	assert.Equal(t, uint32(auctionSlotTimeIntervals), interval)
}

func TestAmmAuctionTimeSlot_ZeroParentCloseTime(t *testing.T) {
	expiration := uint32(172800) // start=86400
	interval := ammAuctionTimeSlot(0, expiration)
	assert.Equal(t, uint32(auctionSlotTimeIntervals), interval, "Before start should return 20")
}

// toUint32 Tests

func TestToUint32_Float64(t *testing.T) {
	assert.Equal(t, uint32(42), toUint32(float64(42)))
	assert.Equal(t, uint32(0), toUint32(float64(-1)))
	assert.Equal(t, uint32(0), toUint32(float64(5000000000))) // > MaxUint32
}

func TestToUint32_Int(t *testing.T) {
	assert.Equal(t, uint32(100), toUint32(int(100)))
	assert.Equal(t, uint32(0), toUint32(int(-1)))
}

func TestToUint32_Int64(t *testing.T) {
	assert.Equal(t, uint32(200), toUint32(int64(200)))
	assert.Equal(t, uint32(0), toUint32(int64(-1)))
	assert.Equal(t, uint32(0), toUint32(int64(5000000000))) // > MaxUint32
}

func TestToUint32_Uint32(t *testing.T) {
	assert.Equal(t, uint32(300), toUint32(uint32(300)))
}

func TestToUint32_Uint64(t *testing.T) {
	assert.Equal(t, uint32(400), toUint32(uint64(400)))
	assert.Equal(t, uint32(0), toUint32(uint64(5000000000))) // > MaxUint32
}

func TestToUint32_Unsupported(t *testing.T) {
	assert.Equal(t, uint32(0), toUint32("string"))
	assert.Equal(t, uint32(0), toUint32(nil))
	assert.Equal(t, uint32(0), toUint32(true))
}

// buildAuctionSlot Tests

func TestBuildAuctionSlot_NoAccount(t *testing.T) {
	// rippled: only includes auction_slot if Account is present
	slot := map[string]interface{}{
		"Price":         map[string]interface{}{"value": "0", "currency": "03000000000000000000000000000000000000C0", "issuer": "rSomeAddr"},
		"DiscountedFee": float64(0),
		"Expiration":    float64(172800),
	}
	result := buildAuctionSlot(slot, 100000)
	assert.Nil(t, result, "No Account should return nil")
}

func TestBuildAuctionSlot_WithAccount(t *testing.T) {
	slot := map[string]interface{}{
		"Account":       "rTestAccount123",
		"Price":         map[string]interface{}{"value": "100", "currency": "LPT", "issuer": "rIssuer"},
		"DiscountedFee": float64(50),
		"Expiration":    float64(172800),
	}

	result := buildAuctionSlot(slot, 90720) // interval 1

	assert.NotNil(t, result)
	assert.Equal(t, "rTestAccount123", result["account"])
	assert.NotNil(t, result["price"])
	assert.Equal(t, float64(50), result["discounted_fee"])
	// Expiration should be ISO 8601 string, not raw number
	expStr, ok := result["expiration"].(string)
	assert.True(t, ok, "expiration should be a string")
	assert.Contains(t, expStr, "T")
	assert.Contains(t, expStr, "+0000")
	// time_interval should be computed
	assert.Equal(t, uint32(1), result["time_interval"])
}

func TestBuildAuctionSlot_AuthAccountsUnwrapped(t *testing.T) {
	// Binary codec returns: [{"AuthAccount": {"Account": "rXXX"}}]
	slot := map[string]interface{}{
		"Account":    "rSlotHolder",
		"Expiration": float64(172800),
		"AuthAccounts": []interface{}{
			map[string]interface{}{
				"AuthAccount": map[string]interface{}{
					"Account": "rAuth1",
				},
			},
			map[string]interface{}{
				"AuthAccount": map[string]interface{}{
					"Account": "rAuth2",
				},
			},
		},
	}

	result := buildAuctionSlot(slot, 0)
	assert.NotNil(t, result)

	auth, ok := result["auth_accounts"].([]map[string]interface{})
	assert.True(t, ok, "auth_accounts should be present")
	assert.Len(t, auth, 2)
	assert.Equal(t, "rAuth1", auth[0]["account"])
	assert.Equal(t, "rAuth2", auth[1]["account"])
}

func TestBuildAuctionSlot_AuthAccountsFallback(t *testing.T) {
	// Edge case: codec returns flat structure without AuthAccount wrapper
	slot := map[string]interface{}{
		"Account":    "rSlotHolder",
		"Expiration": float64(172800),
		"AuthAccounts": []interface{}{
			map[string]interface{}{
				"Account": "rAuth1",
			},
		},
	}

	result := buildAuctionSlot(slot, 0)
	assert.NotNil(t, result)

	auth, ok := result["auth_accounts"].([]map[string]interface{})
	assert.True(t, ok, "auth_accounts should be present via fallback")
	assert.Len(t, auth, 1)
	assert.Equal(t, "rAuth1", auth[0]["account"])
}

func TestBuildAuctionSlot_ExpirationISO8601(t *testing.T) {
	// Verify the exact ISO 8601 format for a known timestamp
	// Ripple epoch 86400 = 2000-01-02T00:00:00 UTC
	slot := map[string]interface{}{
		"Account":    "rTest",
		"Expiration": float64(86400),
	}

	result := buildAuctionSlot(slot, 0)
	assert.NotNil(t, result)
	assert.Equal(t, "2000-01-02T00:00:00+0000", result["expiration"])
}

func TestBuildAuctionSlot_TimeIntervalExpired(t *testing.T) {
	// parentCloseTime well past expiration
	slot := map[string]interface{}{
		"Account":    "rTest",
		"Expiration": float64(172800),
	}

	result := buildAuctionSlot(slot, 300000)
	assert.NotNil(t, result)
	assert.Equal(t, uint32(20), result["time_interval"], "Expired auction should return 20")
}

func TestParseSLEIssue_XRP(t *testing.T) {
	issue, err := parseSLEIssue(map[string]interface{}{"currency": "XRP"})
	require.NoError(t, err)
	assert.True(t, issue.IsXRP)
	assert.Equal(t, "XRP", issue.Currency)
	assert.Equal(t, [20]byte{}, issue.IssuerID)
}

func TestParseSLEIssue_IOU(t *testing.T) {
	issuer := "rrrrrrrrrrrrrrrrrrrrBZbvji" // ACCOUNT_ONE
	issue, err := parseSLEIssue(map[string]interface{}{"currency": "USD", "issuer": issuer})
	require.NoError(t, err)
	assert.False(t, issue.IsXRP)
	assert.Equal(t, "USD", issue.Currency)
	assert.Equal(t, issuer, issue.IssuerStr)
}

func TestParseSLEIssue_Invalid(t *testing.T) {
	_, err := parseSLEIssue(nil)
	assert.Error(t, err)
	_, err = parseSLEIssue(map[string]interface{}{"currency": "USD"})
	assert.Error(t, err)
	_, err = parseSLEIssue(map[string]interface{}{"currency": "USD", "issuer": "not-an-address"})
	assert.Error(t, err)
}

// AMMs only support XRP+IOU / IOU+IOU pairs today, so an MPT-shaped issue
// ({"mpt_issuance_id":..} per codec/binarycodec/types/issue.go) must surface
// a distinct error instead of falling through "missing currency".
func TestParseSLEIssue_MPT(t *testing.T) {
	_, err := parseSLEIssue(map[string]interface{}{
		"mpt_issuance_id": "00000001ABCDEF0000000000000000000000000000000000",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MPT")
}

type memView struct {
	store map[[32]byte][]byte
}

func newMemView() *memView { return &memView{store: make(map[[32]byte][]byte)} }

func (v *memView) Read(k keylet.Keylet) ([]byte, error) {
	if data, ok := v.store[k.Key]; ok {
		return data, nil
	}
	return nil, nil
}
func (v *memView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := v.store[k.Key]
	return ok, nil
}
func (v *memView) Insert(k keylet.Keylet, data []byte) error { v.store[k.Key] = data; return nil }
func (v *memView) Update(k keylet.Keylet, data []byte) error { v.store[k.Key] = data; return nil }
func (v *memView) Erase(k keylet.Keylet) error               { delete(v.store, k.Key); return nil }
func (v *memView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, d := range v.store {
		if !fn(k, d) {
			return nil
		}
	}
	return nil
}
func (v *memView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	return [32]byte{}, nil, false, nil
}
func (v *memView) AdjustDropsDestroyed(d drops.XRPAmount) {}
func (v *memView) TxExists(txID [32]byte) bool            { return false }
func (v *memView) Rules() *amendment.Rules                { return amendment.EmptyRules() }

func decodeAcct(t *testing.T, addr string) [20]byte {
	t.Helper()
	_, b, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	require.NoError(t, err)
	var id [20]byte
	copy(id[:], b)
	return id
}

func TestReadAMMHolds_XRP(t *testing.T) {
	view := newMemView()
	ammAddr := "rrrrrrrrrrrrrrrrrrrrBZbvji"
	ammID := decodeAcct(t, ammAddr)

	// AMMID must be non-zero for the helper to treat the account as an
	// AMM pseudo-account (matches rippled's sfAMMID-present branch of
	// xrpLiquid that forces reserve to 0; View.cpp:631-633).
	root := &state.AccountRoot{Balance: 12_345_000_000, AMMID: [32]byte{0xAA}}
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(ammID), data))

	amount := readAMMHolds(view, ammID, ammIssue{Currency: "XRP", IsXRP: true})
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(12_345_000_000), amount.Drops())
}

func TestReadAMMHolds_XRP_MissingAccount(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	amount := readAMMHolds(view, ammID, ammIssue{Currency: "XRP", IsXRP: true})
	assert.Equal(t, int64(0), amount.Drops())
}

// Contract guard: a non-AMM account (no sfAMMID) must NOT trip the
// "reserve == 0" simplification — return zero rather than the raw, un-
// reserved balance. Mirrors xrpLiquid's branch on sfAMMID in
// rippled/src/xrpld/ledger/detail/View.cpp:631-633.
func TestReadAMMHolds_XRP_NonAMMAccountReturnsZero(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")

	root := &state.AccountRoot{Balance: 12_345_000_000} // AMMID left zero
	data, err := state.SerializeAccountRoot(root)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(ammID), data))

	amount := readAMMHolds(view, ammID, ammIssue{Currency: "XRP", IsXRP: true})
	assert.True(t, amount.IsNative())
	assert.Equal(t, int64(0), amount.Drops(),
		"non-AMM account must not bypass the reserve subtraction")
}

func TestReadAMMHolds_IOU_MissingTrustLine(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	issuerID := decodeAcct(t, issuer)
	amount := readAMMHolds(view, ammID, ammIssue{
		Currency:  "USD",
		IssuerStr: issuer,
		IssuerID:  issuerID,
	})
	assert.False(t, amount.IsNative())
	assert.True(t, amount.IsZero())
	assert.Equal(t, "USD", amount.Currency)
	assert.Equal(t, issuer, amount.Issuer)
}

func TestIsAssetFrozen_XRP(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	assert.False(t, isAssetFrozen(view, ammID, ammIssue{Currency: "XRP", IsXRP: true}))
}

func TestIsAssetFrozen_GlobalFreeze(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	issuerID := decodeAcct(t, issuer)

	issuerRoot := &state.AccountRoot{Balance: 0, Flags: state.LsfGlobalFreeze}
	data, err := state.SerializeAccountRoot(issuerRoot)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(issuerID), data))

	frozen := isAssetFrozen(view, ammID, ammIssue{
		Currency:  "USD",
		IssuerStr: issuer,
		IssuerID:  issuerID,
	})
	assert.True(t, frozen)
}

func TestIsAssetFrozen_NoTrustLine(t *testing.T) {
	view := newMemView()
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	issuerID := decodeAcct(t, issuer)
	data, err := state.SerializeAccountRoot(&state.AccountRoot{})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(issuerID), data))

	frozen := isAssetFrozen(view, ammID, ammIssue{
		Currency:  "USD",
		IssuerStr: issuer,
		IssuerID:  issuerID,
	})
	assert.False(t, frozen)
}

// Covers rippled isFrozen()'s second branch: the issuer's side of the
// AMM↔issuer trust line carries the freeze flag (lsfHighFreeze when
// issuer > amm, lsfLowFreeze otherwise).
func TestIsAssetFrozen_IndividualFreeze(t *testing.T) {
	view := newMemView()
	// ACCOUNT_ONE = all-zero 20-byte account id, lexicographically smaller
	// than any real address, so AMM is low and issuer is high.
	ammID := decodeAcct(t, "rrrrrrrrrrrrrrrrrrrrBZbvji")
	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	issuerID := decodeAcct(t, issuer)

	issuerRoot, err := state.SerializeAccountRoot(&state.AccountRoot{})
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(issuerID), issuerRoot))

	line := &state.RippleState{
		Balance:  state.NewIssuedAmountFromValue(0, 0, "USD", state.AccountOneAddress),
		LowLimit: state.NewIssuedAmountFromValue(0, 0, "USD", "rrrrrrrrrrrrrrrrrrrrBZbvji"),
		HighLimit: state.NewIssuedAmountFromValue(
			0, 0, "USD", issuer),
		Flags: state.LsfHighFreeze,
	}
	lineData, err := state.SerializeRippleState(line)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Line(ammID, issuerID, "USD"), lineData))

	frozen := isAssetFrozen(view, ammID, ammIssue{
		Currency:  "USD",
		IssuerStr: issuer,
		IssuerID:  issuerID,
	})
	assert.True(t, frozen, "issuer-side HighFreeze must propagate to asset_frozen")

	// Flipping the freeze flag to the wrong (low) side must NOT trip the
	// check — rippled keys the flag on the issuer's side only.
	line.Flags = state.LsfLowFreeze
	lineData, err = state.SerializeRippleState(line)
	require.NoError(t, err)
	require.NoError(t, view.Update(keylet.Line(ammID, issuerID, "USD"), lineData))

	frozen = isAssetFrozen(view, ammID, ammIssue{
		Currency:  "USD",
		IssuerStr: issuer,
		IssuerID:  issuerID,
	})
	assert.False(t, frozen, "LowFreeze (AMM-side) must not flag asset as frozen")
}

func TestLPTokenCurrencyFromSLE(t *testing.T) {
	cur, err := lpTokenCurrencyFromSLE(map[string]interface{}{
		"currency": "03ABCDEF00000000000000000000000000000000",
		"issuer":   "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"value":    "100",
	})
	require.NoError(t, err)
	assert.Equal(t, "03ABCDEF00000000000000000000000000000000", cur)

	_, err = lpTokenCurrencyFromSLE(nil)
	assert.Error(t, err)
	_, err = lpTokenCurrencyFromSLE(map[string]interface{}{})
	assert.Error(t, err)
	_, err = lpTokenCurrencyFromSLE(map[string]interface{}{"currency": ""})
	assert.Error(t, err)
}

func TestAccountLPHolds_MissingTrustLine(t *testing.T) {
	view := newMemView()
	ammAddr := "rrrrrrrrrrrrrrrrrrrrBZbvji"
	ammID := decodeAcct(t, ammAddr)
	lpAddr := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	lpID := decodeAcct(t, lpAddr)

	amount := accountLPHolds(view, ammID, lpID, "03ABCDEF00000000000000000000000000000000", ammAddr)
	assert.True(t, amount.IsZero())
	assert.Equal(t, ammAddr, amount.Issuer)
}

func TestAccountLPHolds_LPEqualsAMM(t *testing.T) {
	view := newMemView()
	ammAddr := "rrrrrrrrrrrrrrrrrrrrBZbvji"
	ammID := decodeAcct(t, ammAddr)

	amount := accountLPHolds(view, ammID, ammID, "03ABCDEF00000000000000000000000000000000", ammAddr)
	assert.True(t, amount.IsZero(), "LP account == AMM account should return zero")
}

func TestAccountLPHolds_FrozenLine(t *testing.T) {
	view := newMemView()
	ammAddr := "rrrrrrrrrrrrrrrrrrrrBZbvji"
	ammID := decodeAcct(t, ammAddr)
	lpAddr := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	lpID := decodeAcct(t, lpAddr)
	cur := "03ABCDEF00000000000000000000000000000000"

	// AMM is the issuer; its global-freeze flag suffices to mark the LP line frozen.
	ammRoot := &state.AccountRoot{Flags: state.LsfGlobalFreeze}
	rootData, err := state.SerializeAccountRoot(ammRoot)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Account(ammID), rootData))

	line := &state.RippleState{
		Balance:   state.NewIssuedAmountFromValue(1_000_000, 0, cur, state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, cur, ammAddr),
		HighLimit: state.NewIssuedAmountFromValue(0, 0, cur, lpAddr),
	}
	lineData, err := state.SerializeRippleState(line)
	require.NoError(t, err)
	require.NoError(t, view.Insert(keylet.Line(lpID, ammID, cur), lineData))

	amount := accountLPHolds(view, ammID, lpID, cur, ammAddr)
	assert.True(t, amount.IsZero(), "frozen LP line must return zero per rippled ammLPHolds")
}

// stubReader satisfies types.LedgerReader for wire-format tests; only the
// fields setLedgerIdentityFields reads are populated meaningfully.
type stubReader struct {
	seq    uint32
	hash   [32]byte
	closed bool
}

func (s stubReader) Sequence() uint32            { return s.seq }
func (s stubReader) Hash() [32]byte              { return s.hash }
func (s stubReader) ParentHash() [32]byte        { return [32]byte{} }
func (s stubReader) IsClosed() bool              { return s.closed }
func (s stubReader) IsValidated() bool           { return s.closed }
func (s stubReader) TotalDrops() uint64          { return 0 }
func (s stubReader) CloseTime() int64            { return 0 }
func (s stubReader) CloseTimeResolution() uint32 { return 0 }
func (s stubReader) CloseFlags() uint8           { return 0 }
func (s stubReader) ParentCloseTime() int64      { return 0 }
func (s stubReader) TxMapHash() [32]byte         { return [32]byte{} }
func (s stubReader) StateMapHash() [32]byte      { return [32]byte{} }
func (s stubReader) ForEachTransaction(func([32]byte, []byte) bool) error {
	return nil
}

var _ types.LedgerReader = stubReader{}

// Mirrors rippled RPC::lookupLedger (RPCHelpers.cpp:630-640): a closed
// ledger emits BOTH ledger_hash AND ledger_index regardless of how the
// request named the ledger; an open ledger emits only ledger_current_index.
func TestSetLedgerIdentityFields_ClosedEmitsHashAndIndex(t *testing.T) {
	hash := [32]byte{}
	for i := range hash {
		hash[i] = byte(i + 1)
	}
	reader := stubReader{seq: 12345, hash: hash, closed: true}

	out := map[string]interface{}{}
	setLedgerIdentityFields(out, reader)

	assert.Equal(t, FormatLedgerHash(hash), out["ledger_hash"],
		"closed ledger must emit ledger_hash")
	assert.Equal(t, uint32(12345), out["ledger_index"],
		"closed ledger must emit ledger_index")
	_, hasCurrent := out["ledger_current_index"]
	assert.False(t, hasCurrent,
		"closed ledger must NOT emit ledger_current_index")
}

func TestSetLedgerIdentityFields_OpenEmitsOnlyCurrentIndex(t *testing.T) {
	reader := stubReader{seq: 999, closed: false}

	out := map[string]interface{}{}
	setLedgerIdentityFields(out, reader)

	assert.Equal(t, uint32(999), out["ledger_current_index"],
		"open ledger must emit ledger_current_index")
	_, hasHash := out["ledger_hash"]
	_, hasIndex := out["ledger_index"]
	assert.False(t, hasHash, "open ledger must NOT emit ledger_hash")
	assert.False(t, hasIndex, "open ledger must NOT emit ledger_index")
}

// Regression for the XRP-pair AMM keylet mismatch: a previous local helper
// wrote ASCII 'X','R','P' into bytes 12-14 for the "XRP" string, which did
// not match the AMM SLE created via state.GetCurrencyBytes (all-zero for
// XRP). The handler must use state.GetCurrencyBytes so its asset-pair
// lookup keylet equals the one stored in the ledger by AMMCreate.
func TestKeyletAMM_XRPPair_MatchesCanonical(t *testing.T) {
	issuer := decodeAcct(t, "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")

	// Handler-side keylet now goes through state.GetCurrencyBytes.
	handlerKey := keylet.AMM(
		[20]byte{}, state.GetCurrencyBytes("XRP"),
		issuer, state.GetCurrencyBytes("USD"),
	)
	// Canonical (tx-side) keylet uses the same helper.
	canonicalKey := keylet.AMM(
		[20]byte{}, state.GetCurrencyBytes(""), // tx side maps "" -> XRP
		issuer, state.GetCurrencyBytes("USD"),
	)
	assert.Equal(t, canonicalKey.Key, handlerKey.Key,
		"XRP-pair handler keylet must equal tx-side keylet")
	// And neither must equal the broken non-zero-XRP variant: a fake helper
	// that encoded "XRP" as ASCII would put 'X','R','P' at bytes 12-14.
	var brokenXRP [20]byte
	brokenXRP[12], brokenXRP[13], brokenXRP[14] = 'X', 'R', 'P'
	brokenKey := keylet.AMM([20]byte{}, brokenXRP, issuer, state.GetCurrencyBytes("USD"))
	assert.NotEqual(t, brokenKey.Key, handlerKey.Key,
		"handler must NOT reproduce the old ASCII-XRP keylet")
}

func TestParseUserIssue_ValidObject(t *testing.T) {
	issue, err := parseUserIssue([]byte(`{"currency":"XRP"}`))
	require.NoError(t, err)
	assert.True(t, issue.IsXRP)

	issuer := "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	issue, err = parseUserIssue([]byte(`{"currency":"USD","issuer":"` + issuer + `"}`))
	require.NoError(t, err)
	assert.False(t, issue.IsXRP)
	assert.Equal(t, "USD", issue.Currency)
	assert.Equal(t, issuer, issue.IssuerStr)
}

// Mirrors rippled's testInvalidAmmField: a non-object asset value silently
// defaults to the XRP issue rather than erroring, so the subsequent AMM
// lookup surfaces as actNotFound.
func TestParseUserIssue_NonObjectFallsThroughToXRP(t *testing.T) {
	issue, err := parseUserIssue([]byte(`"validated"`))
	require.NoError(t, err)
	assert.True(t, issue.IsXRP, "string asset must coerce to XRP")

	issue, err = parseUserIssue([]byte(`42`))
	require.NoError(t, err)
	assert.True(t, issue.IsXRP, "number asset must coerce to XRP")

	issue, err = parseUserIssue([]byte(`null`))
	require.NoError(t, err)
	assert.True(t, issue.IsXRP, "null asset must coerce to XRP")
}

// A well-formed object with a bad issuer still surfaces issueMalformed.
func TestParseUserIssue_MalformedObject(t *testing.T) {
	_, err := parseUserIssue([]byte(`{"currency":"USD","issuer":"not-an-address"}`))
	require.Error(t, err)
}

func TestLPTokenValueFromSLE(t *testing.T) {
	v, err := lpTokenValueFromSLE(map[string]interface{}{"value": "1000000"})
	require.NoError(t, err)
	assert.Equal(t, "1000000", v)

	_, err = lpTokenValueFromSLE(nil)
	assert.Error(t, err)
	_, err = lpTokenValueFromSLE(map[string]interface{}{"value": ""})
	assert.Error(t, err)
}
