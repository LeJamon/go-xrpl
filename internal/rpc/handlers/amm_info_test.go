package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// =============================================================================
// rippleEpochToISO8601 Tests
// =============================================================================

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

// =============================================================================
// ammAuctionTimeSlot Tests
// Based on rippled's ammAuctionTimeSlot() in AMMCore.cpp
// =============================================================================

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

// =============================================================================
// toUint32 Tests
// =============================================================================

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

// =============================================================================
// buildAuctionSlot Tests
// =============================================================================

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
