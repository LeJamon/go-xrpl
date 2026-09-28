package handlers

import (
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
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

func TestBuildAuctionSlotPresence(t *testing.T) {
	var slot ledgerfields.AuctionSlotValue
	result, err := buildAuctionSlot(slot, 0)
	require.NoError(t, err)
	require.Nil(t, result)

	account := [20]byte{1}
	require.NoError(t, slot.SetAccountValue(account))
	slot.SetExpiration(172800)
	require.NoError(t, slot.SetPriceValue(ledgerfields.AmountValue{
		Value: "100", Currency: "LPT", Issuer: state.EncodeAccountIDSafe(account),
	}))
	result, err = buildAuctionSlot(slot, 90720)
	require.NoError(t, err)
	assert.Equal(t, state.EncodeAccountIDSafe(account), result["account"])
	assert.Equal(t, uint32(1), result["time_interval"])
	assert.Equal(t, "2000-01-03T00:00:00+0000", result["expiration"])
	assert.Equal(t, uint16(0), result["discounted_fee"])
	assert.NotContains(t, result, "auth_accounts")

	slot.SetDiscountedFee(0)
	result, err = buildAuctionSlot(slot, 300000)
	require.NoError(t, err)
	assert.Equal(t, uint16(0), result["discounted_fee"])
	assert.Equal(t, uint32(20), result["time_interval"])
}

func TestBuildAuctionSlotAuthorizedAccounts(t *testing.T) {
	var slot ledgerfields.AuctionSlotValue
	require.NoError(t, slot.SetAccountValue([20]byte{1}))
	slot.SetExpiration(86400)
	accounts := []ledgerfields.AuthAccountValue{{}, {}}
	for i := range accounts {
		require.NoError(t, accounts[i].SetAccountValue([20]byte{byte(i + 2)}))
	}
	require.NoError(t, slot.SetAuthAccountsValue(accounts))
	result, err := buildAuctionSlot(slot, 0)
	require.NoError(t, err)
	assert.Equal(t, "2000-01-02T00:00:00+0000", result["expiration"])
	assert.Equal(t, []map[string]any{
		{"account": state.EncodeAccountIDSafe([20]byte{2})},
		{"account": state.EncodeAccountIDSafe([20]byte{3})},
	}, result["auth_accounts"])
	require.NoError(t, slot.SetAuthAccountsValue(nil))
	result, err = buildAuctionSlot(slot, 0)
	require.NoError(t, err)
	assert.Contains(t, result, "auth_accounts")
	assert.Empty(t, result["auth_accounts"])
}

func TestExtractIssue(t *testing.T) {
	const issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	for _, tc := range []struct {
		name  string
		value ledgerfields.IssueValue
		valid bool
		xrp   bool
	}{
		{name: "XRP", value: ledgerfields.IssueValue{Currency: "XRP"}, valid: true, xrp: true},
		{name: "IOU", value: ledgerfields.IssueValue{Currency: "USD", Issuer: issuer}, valid: true},
		{name: "hex currency", value: ledgerfields.IssueValue{Currency: "0158415500000000C1F76FF6ECB0BAC600000000", Issuer: issuer}, valid: true},
		{name: "missing issuer", value: ledgerfields.IssueValue{Currency: "USD"}},
		{name: "invalid issuer", value: ledgerfields.IssueValue{Currency: "USD", Issuer: "invalid"}},
		{name: "empty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue, ok := extractIssue(tc.value)
			require.Equal(t, tc.valid, ok)
			if ok {
				assert.Equal(t, tc.xrp, issue.IsXRP())
				assert.Equal(t, tc.value.Currency, issue.Currency)
				assert.Equal(t, tc.value.Issuer, issue.IssuerR)
			}
		})
	}
}

func TestParseAMMAsset(t *testing.T) {
	const id = "00000001B5F762798A53D543A014CAF8B297CFF8F2F937E8"
	for _, tc := range []struct {
		name, raw  string
		valid, mpt bool
	}{
		{"XRP", `{"currency":"XRP"}`, true, false},
		{"IOU", `{"currency":"USD","issuer":"rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"}`, true, false},
		{"MPT", `{"mpt_issuance_id":"` + id + `"}`, true, true},
		{"zero MPT", `{"mpt_issuance_id":"0"}`, true, true},
		{"mixed currency", `{"currency":"XRP","mpt_issuance_id":"` + id + `"}`, false, false},
		{"mixed issuer", `{"issuer":null,"mpt_issuance_id":"` + id + `"}`, false, false},
		{"numeric MPT", `{"mpt_issuance_id":1}`, false, false},
		{"short MPT", `{"mpt_issuance_id":"01"}`, false, false},
		{"empty MPT", `{"mpt_issuance_id":""}`, false, false},
		{"null MPT", `{"mpt_issuance_id":null}`, false, false},
		{"null", `null`, false, false},
		{"array", `[]`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issue, err := parseAMMAsset(json.RawMessage(tc.raw))
			if !tc.valid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.mpt, issue.MPTID != nil)
			if tc.mpt {
				require.False(t, issue.IsXRP())
			}
		})
	}
}
