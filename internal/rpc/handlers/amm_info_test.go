package handlers

import (
	"testing"

	"github.com/LeJamon/goXRPLd/amendment"
	addresscodec "github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
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

// parseSLEIssue Tests

func TestParseSLEIssue_XRP(t *testing.T) {
	issue, ok := parseSLEIssue(map[string]interface{}{"currency": "XRP"})
	require.True(t, ok)
	assert.True(t, issue.IsXRP)
	assert.Equal(t, "XRP", issue.Currency)
	assert.Equal(t, [20]byte{}, issue.IssuerID)
}

func TestParseSLEIssue_IOU(t *testing.T) {
	issuer := "rrrrrrrrrrrrrrrrrrrrBZbvji" // ACCOUNT_ONE
	issue, ok := parseSLEIssue(map[string]interface{}{"currency": "USD", "issuer": issuer})
	require.True(t, ok)
	assert.False(t, issue.IsXRP)
	assert.Equal(t, "USD", issue.Currency)
	assert.Equal(t, issuer, issue.IssuerStr)
}

func TestParseSLEIssue_Invalid(t *testing.T) {
	_, ok := parseSLEIssue(nil)
	assert.False(t, ok)
	_, ok = parseSLEIssue(map[string]interface{}{"currency": "USD"})
	assert.False(t, ok)
	_, ok = parseSLEIssue(map[string]interface{}{"currency": "USD", "issuer": "not-an-address"})
	assert.False(t, ok)
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

	root := &state.AccountRoot{Balance: 12_345_000_000}
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
