package trustset

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/core/tx"
)

func ptrUint32(v uint32) *uint32 { return &v }

// TestTrustSetValidation tests TrustSet transaction validation.
// These tests are translated from rippled's TrustAndBalance_test.cpp and Freeze_test.cpp
// focusing on validation logic and error conditions.
func TestTrustSetValidation(t *testing.T) {
	tests := []struct {
		name        string
		trustSet    *TrustSet
		expectError bool
		errorMsg    string
	}{
		// === Valid Trust Line Creation Tests ===
		{
			name: "valid trust line creation with positive limit",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(800, "USD", "rGateway"),
			},
			expectError: false,
		},
		{
			name: "valid trust line with EUR currency",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(1000, "EUR", "rGateway"),
			},
			expectError: false,
		},
		{
			name: "valid trust line with BTC currency",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rBob"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "BTC", "rGateway"),
			},
			expectError: false,
		},
		{
			name: "valid trust line with large limit",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(999999999999, "USD", "rGateway"),
			},
			expectError: false,
		},

		// === Remove Trust Line (Zero Limit) Tests ===
		{
			name: "valid remove trust line with zero limit",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(0, "USD", "rGateway"),
			},
			expectError: false,
		},

		// === Missing LimitAmount Tests ===
		{
			name: "missing LimitAmount currency - should fail",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "", "rGateway"),
			},
			expectError: true,
			errorMsg:    "temBAD_CURRENCY: currency is required",
		},
		{
			name: "missing LimitAmount issuer - should fail",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", ""),
			},
			expectError: true,
			errorMsg:    "temDST_NEEDED: issuer is required",
		},
		{
			name: "XRP as LimitAmount - cannot create trust line for XRP",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewXRPAmount(1000000),
			},
			expectError: true,
			errorMsg:    "temBAD_LIMIT: cannot create trust line for XRP",
		},
		{
			name: "XRP currency code - should fail",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "XRP", "rGateway"),
			},
			expectError: true,
			errorMsg:    "temBAD_CURRENCY: cannot use XRP as IOU currency",
		},
		{
			name: "negative limit - should fail",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(-100, "USD", "rGateway"),
			},
			expectError: true,
			errorMsg:    "temBAD_LIMIT: negative credit limit",
		},

		// === Trust Line to Self Tests ===
		{
			name: "trust line to self - should fail",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rAlice"),
			},
			expectError: true,
			errorMsg:    "temDST_IS_SRC: cannot create trust line to self",
		},

		// === Missing Account Tests ===
		{
			name: "missing account - should fail",
			trustSet: &TrustSet{
				BaseTx: tx.BaseTx{Common: tx.Common{TransactionType: "TrustSet"}},
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
			},
			expectError: true,
			errorMsg:    "Account is required",
		},

		// === QualityIn and QualityOut Tests ===
		{
			name: "valid trust line with QualityIn",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
				QualityIn:   ptrUint32(1000000000), // 1:1 ratio
			},
			expectError: false,
		},
		{
			name: "valid trust line with QualityOut",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
				QualityOut:  ptrUint32(1000000000), // 1:1 ratio
			},
			expectError: false,
		},
		{
			name: "valid trust line with both QualityIn and QualityOut",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
				QualityIn:   ptrUint32(900000000),  // 0.9 ratio
				QualityOut:  ptrUint32(1100000000), // 1.1 ratio
			},
			expectError: false,
		},
		{
			name: "valid trust line with zero QualityIn (removes quality)",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
				QualityIn:   ptrUint32(0),
			},
			expectError: false,
		},
		{
			name: "valid trust line with zero QualityOut (removes quality)",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
				QualityOut:  ptrUint32(0),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.trustSet.Validate()
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				} else if tt.errorMsg != "" && err.Error() != tt.errorMsg {
					t.Errorf("expected error %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// TestTrustSetFlags tests TrustSet flag handling.
// Tests based on rippled's Freeze_test.cpp flag behavior.
func TestTrustSetFlags(t *testing.T) {
	tests := []struct {
		name           string
		setupFunc      func() *TrustSet
		expectedFlags  uint32
		checkCondition func(t *testing.T, ts *TrustSet)
	}{
		{
			name: "tfSetfAuth flag - authorize other party",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.SetFlags(TrustSetFlagSetfAuth)
				return ts
			},
			expectedFlags: TrustSetFlagSetfAuth,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				if ts.GetFlags()&TrustSetFlagSetfAuth == 0 {
					t.Error("expected tfSetfAuth flag to be set")
				}
			},
		},
		{
			name: "tfSetNoRipple flag - block rippling",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.SetNoRipple()
				return ts
			},
			expectedFlags: TrustSetFlagSetNoRipple,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				if ts.GetFlags()&TrustSetFlagSetNoRipple == 0 {
					t.Error("expected tfSetNoRipple flag to be set")
				}
			},
		},
		{
			name: "tfClearNoRipple flag - clear no ripple",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.ClearNoRipple()
				return ts
			},
			expectedFlags: TrustSetFlagClearNoRipple,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				if ts.GetFlags()&TrustSetFlagClearNoRipple == 0 {
					t.Error("expected tfClearNoRipple flag to be set")
				}
			},
		},
		{
			name: "tfSetFreeze flag - freeze trust line",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.SetFreeze()
				return ts
			},
			expectedFlags: TrustSetFlagSetFreeze,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				if ts.GetFlags()&TrustSetFlagSetFreeze == 0 {
					t.Error("expected tfSetFreeze flag to be set")
				}
			},
		},
		{
			name: "tfClearFreeze flag - unfreeze trust line",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.SetFlags(TrustSetFlagClearFreeze)
				return ts
			},
			expectedFlags: TrustSetFlagClearFreeze,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				if ts.GetFlags()&TrustSetFlagClearFreeze == 0 {
					t.Error("expected tfClearFreeze flag to be set")
				}
			},
		},
		{
			name: "multiple flags - SetNoRipple and SetFreeze",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
				ts.SetNoRipple()
				ts.SetFreeze()
				return ts
			},
			expectedFlags: TrustSetFlagSetNoRipple | TrustSetFlagSetFreeze,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				flags := ts.GetFlags()
				if flags&TrustSetFlagSetNoRipple == 0 {
					t.Error("expected tfSetNoRipple flag to be set")
				}
				if flags&TrustSetFlagSetFreeze == 0 {
					t.Error("expected tfSetFreeze flag to be set")
				}
			},
		},
		{
			name: "combining SetfAuth with SetNoRipple",
			setupFunc: func() *TrustSet {
				ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
				ts.SetFlags(TrustSetFlagSetfAuth | TrustSetFlagSetNoRipple)
				return ts
			},
			expectedFlags: TrustSetFlagSetfAuth | TrustSetFlagSetNoRipple,
			checkCondition: func(t *testing.T, ts *TrustSet) {
				flags := ts.GetFlags()
				if flags&TrustSetFlagSetfAuth == 0 {
					t.Error("expected tfSetfAuth flag to be set")
				}
				if flags&TrustSetFlagSetNoRipple == 0 {
					t.Error("expected tfSetNoRipple flag to be set")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := tt.setupFunc()
			if ts.GetFlags() != tt.expectedFlags {
				t.Errorf("expected flags %d, got %d", tt.expectedFlags, ts.GetFlags())
			}
			tt.checkCondition(t, ts)
		})
	}
}

// TestTrustSetConflictingFlags tests detection of conflicting flags.
// Based on rippled's Freeze_test.cpp testSetAndClear which shows that
// conflicting flags (SetNoRipple + ClearNoRipple) result in tecNO_PERMISSION.
func TestTrustSetConflictingFlags(t *testing.T) {
	tests := []struct {
		name         string
		flags        uint32
		isConflict   bool
		conflictDesc string
	}{
		{
			name:         "SetNoRipple alone - no conflict",
			flags:        TrustSetFlagSetNoRipple,
			isConflict:   false,
			conflictDesc: "",
		},
		{
			name:         "ClearNoRipple alone - no conflict",
			flags:        TrustSetFlagClearNoRipple,
			isConflict:   false,
			conflictDesc: "",
		},
		{
			name:         "SetNoRipple + ClearNoRipple - CONFLICT",
			flags:        TrustSetFlagSetNoRipple | TrustSetFlagClearNoRipple,
			isConflict:   true,
			conflictDesc: "temINVALID_FLAG: cannot set and clear NoRipple",
		},
		{
			name:         "SetFreeze alone - no conflict",
			flags:        TrustSetFlagSetFreeze,
			isConflict:   false,
			conflictDesc: "",
		},
		{
			name:         "ClearFreeze alone - no conflict",
			flags:        TrustSetFlagClearFreeze,
			isConflict:   false,
			conflictDesc: "",
		},
		{
			name:         "SetFreeze + ClearFreeze - CONFLICT",
			flags:        TrustSetFlagSetFreeze | TrustSetFlagClearFreeze,
			isConflict:   true,
			conflictDesc: "temINVALID_FLAG: cannot set and clear freeze in same transaction",
		},
		{
			name:         "SetNoRipple + SetFreeze - no conflict (different flag families)",
			flags:        TrustSetFlagSetNoRipple | TrustSetFlagSetFreeze,
			isConflict:   false,
			conflictDesc: "",
		},
		{
			name:         "ClearNoRipple + ClearFreeze - no conflict",
			flags:        TrustSetFlagClearNoRipple | TrustSetFlagClearFreeze,
			isConflict:   false,
			conflictDesc: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hasConflict := hasConflictingTrustSetFlags(tt.flags)
			if hasConflict != tt.isConflict {
				if tt.isConflict {
					t.Errorf("expected conflict (%s) but none detected", tt.conflictDesc)
				} else {
					t.Error("detected false conflict")
				}
			}
		})
	}
}

// hasConflictingTrustSetFlags checks if flags have conflicting set/clear combinations.
// This helper function detects the same conflicts that rippled checks.
func hasConflictingTrustSetFlags(flags uint32) bool {
	// Check NoRipple conflict
	if flags&TrustSetFlagSetNoRipple != 0 && flags&TrustSetFlagClearNoRipple != 0 {
		return true
	}
	// Check Freeze conflict
	if flags&TrustSetFlagSetFreeze != 0 && flags&TrustSetFlagClearFreeze != 0 {
		return true
	}
	return false
}

// TestTrustSetFlatten tests the Flatten method for TrustSet.
func TestTrustSetFlatten(t *testing.T) {
	tests := []struct {
		name     string
		trustSet *TrustSet
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "basic trust line",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Account"] != "rAlice" {
					t.Errorf("expected Account=rAlice, got %v", m["Account"])
				}
				limitAmount, ok := m["LimitAmount"].(map[string]any)
				if !ok {
					t.Fatalf("LimitAmount should be a map, got %T", m["LimitAmount"])
				}
				if limitAmount["value"] != "100" {
					t.Errorf("expected value=100, got %v", limitAmount["value"])
				}
				if limitAmount["currency"] != "USD" {
					t.Errorf("expected currency=USD, got %v", limitAmount["currency"])
				}
				if limitAmount["issuer"] != "rGateway" {
					t.Errorf("expected issuer=rGateway, got %v", limitAmount["issuer"])
				}
			},
		},
		{
			name: "trust line with QualityIn",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(200, "EUR", "rIssuer"),
				QualityIn:   ptrUint32(1000000000),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["QualityIn"] != uint32(1000000000) {
					t.Errorf("expected QualityIn=1000000000, got %v", m["QualityIn"])
				}
				if _, ok := m["QualityOut"]; ok {
					t.Error("QualityOut should not be present")
				}
			},
		},
		{
			name: "trust line with QualityOut",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rBob"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(500, "BTC", "rIssuer"),
				QualityOut:  ptrUint32(900000000),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["QualityOut"] != uint32(900000000) {
					t.Errorf("expected QualityOut=900000000, got %v", m["QualityOut"])
				}
				if _, ok := m["QualityIn"]; ok {
					t.Error("QualityIn should not be present")
				}
			},
		},
		{
			name: "trust line with both quality fields",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(1000, "USD", "rGateway"),
				QualityIn:   ptrUint32(1100000000),
				QualityOut:  ptrUint32(900000000),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["QualityIn"] != uint32(1100000000) {
					t.Errorf("expected QualityIn=1100000000, got %v", m["QualityIn"])
				}
				if m["QualityOut"] != uint32(900000000) {
					t.Errorf("expected QualityOut=900000000, got %v", m["QualityOut"])
				}
			},
		},
		{
			name: "trust line with zero limit (removal)",
			trustSet: &TrustSet{
				BaseTx: *tx.NewBaseTx(tx.TypeTrustSet, "rAlice"),
				LimitAmount: tx.NewIssuedAmountFromFloat64(0, "USD", "rGateway"),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				limitAmount, ok := m["LimitAmount"].(map[string]any)
				if !ok {
					t.Fatalf("LimitAmount should be a map, got %T", m["LimitAmount"])
				}
				if limitAmount["value"] != "0" {
					t.Errorf("expected value=0, got %v", limitAmount["value"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.trustSet.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestTrustSetTransactionType tests that transaction type is correctly returned.
func TestTrustSetTransactionType(t *testing.T) {
	ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
	if ts.TxType() != tx.TypeTrustSet {
		t.Errorf("expected tx.TypeTrustSet, got %v", ts.TxType())
	}
}

// TestNewTrustSetConstructor tests the NewTrustSet constructor function.
func TestNewTrustSetConstructor(t *testing.T) {
	t.Run("basic constructor", func(t *testing.T) {
		ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))

		if ts.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", ts.Account)
		}
		if ts.LimitAmount.Value() != "100" {
			t.Errorf("expected LimitAmount.Value=100, got %v", ts.LimitAmount.Value())
		}
		if ts.LimitAmount.Currency != "USD" {
			t.Errorf("expected LimitAmount.Currency=USD, got %v", ts.LimitAmount.Currency)
		}
		if ts.LimitAmount.Issuer != "rGateway" {
			t.Errorf("expected LimitAmount.Issuer=rGateway, got %v", ts.LimitAmount.Issuer)
		}
		if ts.TransactionType != "TrustSet" {
			t.Errorf("expected TransactionType=TrustSet, got %v", ts.TransactionType)
		}
	})

	t.Run("constructor with different currencies", func(t *testing.T) {
		currencies := []string{"USD", "EUR", "BTC", "ETH", "JPY", "GBP"}
		for _, currency := range currencies {
			ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(500, currency, "rIssuer"))
			if ts.LimitAmount.Currency != currency {
				t.Errorf("expected currency=%s, got %v", currency, ts.LimitAmount.Currency)
			}
		}
	})
}

// TestTrustSetHelperMethods tests the helper methods on TrustSet.
func TestTrustSetHelperMethods(t *testing.T) {
	t.Run("SetNoRipple", func(t *testing.T) {
		ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
		ts.SetNoRipple()

		if ts.GetFlags()&TrustSetFlagSetNoRipple == 0 {
			t.Error("SetNoRipple should set the tfSetNoRipple flag")
		}
	})

	t.Run("ClearNoRipple", func(t *testing.T) {
		ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
		ts.ClearNoRipple()

		if ts.GetFlags()&TrustSetFlagClearNoRipple == 0 {
			t.Error("ClearNoRipple should set the tfClearNoRipple flag")
		}
	})

	t.Run("SetFreeze", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFreeze()

		if ts.GetFlags()&TrustSetFlagSetFreeze == 0 {
			t.Error("SetFreeze should set the tfSetFreeze flag")
		}
	})

	t.Run("SetNoRipple then ClearNoRipple (cumulative)", func(t *testing.T) {
		ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
		ts.SetNoRipple()
		ts.ClearNoRipple()

		// Both flags should be set (OR operation)
		flags := ts.GetFlags()
		if flags&TrustSetFlagSetNoRipple == 0 {
			t.Error("SetNoRipple flag should still be set")
		}
		if flags&TrustSetFlagClearNoRipple == 0 {
			t.Error("ClearNoRipple flag should be set")
		}
	})
}

// TestTrustSetVariousCurrencies tests trust lines with various currency codes.
// Based on rippled's support for 3-character and hex currency codes.
func TestTrustSetVariousCurrencies(t *testing.T) {
	tests := []struct {
		name     string
		currency string
		valid    bool
	}{
		{"standard USD", "USD", true},
		{"standard EUR", "EUR", true},
		{"standard BTC", "BTC", true},
		{"standard JPY", "JPY", true},
		{"standard GBP", "GBP", true},
		{"standard CNY", "CNY", true},
		{"standard AUD", "AUD", true},
		{"standard CAD", "CAD", true},
		{"standard CHF", "CHF", true},
		{"standard INR", "INR", true},
		// Hex currency codes (40 hex characters)
		{"hex currency", "0158415500000000C1F76FF6ECB0BAC600000000", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, tt.currency, "rGateway"))
			err := ts.Validate()

			if tt.valid && err != nil {
				t.Errorf("expected valid trust line for currency %s, got error: %v", tt.currency, err)
			}
		})
	}
}

// TestTrustSetIssuerValidation tests issuer address handling.
func TestTrustSetIssuerValidation(t *testing.T) {
	tests := []struct {
		name        string
		account     string
		issuer      string
		expectError bool
		errorMsg    string
	}{
		{
			name:        "valid different account and issuer",
			account:     "rAlice",
			issuer:      "rGateway",
			expectError: false,
		},
		{
			name:        "same account and issuer - should fail",
			account:     "rAlice",
			issuer:      "rAlice",
			expectError: true,
			errorMsg:    "temDST_IS_SRC: cannot create trust line to self",
		},
		{
			name:        "empty issuer - should fail",
			account:     "rAlice",
			issuer:      "",
			expectError: true,
			errorMsg:    "temDST_NEEDED: issuer is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewTrustSet(tt.account, tx.NewIssuedAmountFromFloat64(100, "USD", tt.issuer))
			err := ts.Validate()

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error %q, got nil", tt.errorMsg)
				} else if err.Error() != tt.errorMsg {
					t.Errorf("expected error %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// TestTrustSetAmountValues tests various limit amount values.
// Based on rippled's testCreditLimit showing positive and negative limits.
func TestTrustSetAmountValues(t *testing.T) {
	tests := []struct {
		name        string
		value       float64
		expectError bool
		description string
	}{
		{
			name:        "positive integer limit",
			value:       800,
			expectError: false,
			description: "credit limit of 800",
		},
		{
			name:        "modified positive limit",
			value:       700,
			expectError: false,
			description: "modified credit limit",
		},
		{
			name:        "zero limit (remove trust line)",
			value:       0,
			expectError: false,
			description: "set zero limit to remove trust line",
		},
		{
			name:        "large limit",
			value:       999999999999999,
			expectError: false,
			description: "very large credit limit",
		},
		{
			name:        "decimal limit",
			value:       100.5,
			expectError: false,
			description: "decimal credit limit",
		},
		{
			name:        "small decimal limit",
			value:       0.001,
			expectError: false,
			description: "small decimal credit limit",
		},
		// Negative limits are rejected with temBAD_LIMIT
		{
			name:        "negative limit - temBAD_LIMIT",
			value:       -1,
			expectError: true,
			description: "negative limit - temBAD_LIMIT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(tt.value, "USD", "rGateway"))
			err := ts.Validate()

			if tt.expectError && err == nil {
				t.Errorf("expected error for %s, got nil", tt.description)
			}
			if !tt.expectError && err != nil {
				t.Errorf("expected no error for %s, got: %v", tt.description, err)
			}
		})
	}
}

// TestTrustSetFreezeScenarios tests various freeze-related scenarios.
// Based on rippled's Freeze_test.cpp testRippleState and testDeepFreeze.
func TestTrustSetFreezeScenarios(t *testing.T) {
	t.Run("issuer can freeze trust line", func(t *testing.T) {
		// Issuer (G1) freezes the trust line with bob
		// env(trust(G1, bob["USD"](0), tfSetFreeze))
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rBob"))
		ts.SetFreeze()

		if err := ts.Validate(); err != nil {
			t.Errorf("issuer should be able to create freeze transaction: %v", err)
		}
		if ts.GetFlags()&TrustSetFlagSetFreeze == 0 {
			t.Error("tfSetFreeze flag should be set")
		}
	})

	t.Run("issuer can clear freeze", func(t *testing.T) {
		// env(trust(G1, bob["USD"](0), tfClearFreeze))
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rBob"))
		ts.SetFlags(TrustSetFlagClearFreeze)

		if err := ts.Validate(); err != nil {
			t.Errorf("issuer should be able to create clear freeze transaction: %v", err)
		}
		if ts.GetFlags()&TrustSetFlagClearFreeze == 0 {
			t.Error("tfClearFreeze flag should be set")
		}
	})

	t.Run("holder cannot freeze their own line (structurally valid)", func(t *testing.T) {
		// Note: While structurally valid, rippled will reject this
		// The holder (rAlice) setting freeze on their line to the issuer
		ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
		ts.SetFreeze()

		// This passes local validation but would fail on rippled
		if err := ts.Validate(); err != nil {
			t.Errorf("local validation should pass: %v", err)
		}
	})
}

// TestTrustSetQualityRatios tests quality ratio edge cases.
func TestTrustSetQualityRatios(t *testing.T) {
	tests := []struct {
		name       string
		qualityIn  *uint32
		qualityOut *uint32
		desc       string
	}{
		{
			name:       "1:1 ratio (1e9)",
			qualityIn:  ptrUint32(1000000000),
			qualityOut: ptrUint32(1000000000),
			desc:       "standard 1:1 exchange rate",
		},
		{
			name:       "favorable to incoming (1.1:1)",
			qualityIn:  ptrUint32(1100000000),
			qualityOut: nil,
			desc:       "incoming transfers get 10% bonus",
		},
		{
			name:       "unfavorable to outgoing (0.9:1)",
			qualityIn:  nil,
			qualityOut: ptrUint32(900000000),
			desc:       "outgoing transfers cost 10% more",
		},
		{
			name:       "zero quality (removes setting)",
			qualityIn:  ptrUint32(0),
			qualityOut: ptrUint32(0),
			desc:       "zero removes the quality setting",
		},
		{
			name:       "very high quality ratio",
			qualityIn:  ptrUint32(2000000000),
			qualityOut: nil,
			desc:       "2:1 quality ratio",
		},
		{
			name:       "very low quality ratio",
			qualityIn:  nil,
			qualityOut: ptrUint32(500000000),
			desc:       "0.5:1 quality ratio",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := NewTrustSet("rAlice", tx.NewIssuedAmountFromFloat64(100, "USD", "rGateway"))
			ts.QualityIn = tt.qualityIn
			ts.QualityOut = tt.qualityOut

			if err := ts.Validate(); err != nil {
				t.Errorf("expected valid transaction for %s, got: %v", tt.desc, err)
			}

			// Verify Flatten includes the quality fields correctly
			m, err := ts.Flatten()
			if err != nil {
				t.Fatalf("Flatten failed: %v", err)
			}

			if tt.qualityIn != nil {
				if m["QualityIn"] != *tt.qualityIn {
					t.Errorf("expected QualityIn=%d, got %v", *tt.qualityIn, m["QualityIn"])
				}
			} else {
				if _, exists := m["QualityIn"]; exists {
					t.Error("QualityIn should not be present when nil")
				}
			}

			if tt.qualityOut != nil {
				if m["QualityOut"] != *tt.qualityOut {
					t.Errorf("expected QualityOut=%d, got %v", *tt.qualityOut, m["QualityOut"])
				}
			} else {
				if _, exists := m["QualityOut"]; exists {
					t.Error("QualityOut should not be present when nil")
				}
			}
		})
	}
}

// TestTrustSetFlagConstants verifies the flag constant values match rippled.
func TestTrustSetFlagConstants(t *testing.T) {
	// These values must match rippled's TxFlags.h
	tests := []struct {
		name     string
		flag     uint32
		expected uint32
	}{
		{"tfSetfAuth", TrustSetFlagSetfAuth, 0x00010000},
		{"tfSetNoRipple", TrustSetFlagSetNoRipple, 0x00020000},
		{"tfClearNoRipple", TrustSetFlagClearNoRipple, 0x00040000},
		{"tfSetFreeze", TrustSetFlagSetFreeze, 0x00100000},
		{"tfClearFreeze", TrustSetFlagClearFreeze, 0x00200000},
		{"tfSetDeepFreeze", TrustSetFlagSetDeepFreeze, 0x00400000},
		{"tfClearDeepFreeze", TrustSetFlagClearDeepFreeze, 0x00800000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.expected {
				t.Errorf("expected %s = 0x%08X, got 0x%08X", tt.name, tt.expected, tt.flag)
			}
		})
	}
}

// TestTrustSetDeepFreezeFlags tests deep freeze flag handling.
// Reference: rippled SetTrust.cpp featureDeepFreeze
func TestTrustSetDeepFreezeFlags(t *testing.T) {
	t.Run("SetDeepFreeze flag", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagSetDeepFreeze)

		if err := ts.Validate(); err != nil {
			t.Errorf("SetDeepFreeze should be valid: %v", err)
		}
		if ts.GetFlags()&TrustSetFlagSetDeepFreeze == 0 {
			t.Error("tfSetDeepFreeze flag should be set")
		}
	})

	t.Run("ClearDeepFreeze flag", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagClearDeepFreeze)

		if err := ts.Validate(); err != nil {
			t.Errorf("ClearDeepFreeze should be valid: %v", err)
		}
		if ts.GetFlags()&TrustSetFlagClearDeepFreeze == 0 {
			t.Error("tfClearDeepFreeze flag should be set")
		}
	})

	t.Run("SetDeepFreeze + ClearDeepFreeze - CONFLICT", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagSetDeepFreeze | TrustSetFlagClearDeepFreeze)

		err := ts.Validate()
		if err == nil {
			t.Error("expected error for conflicting deep freeze flags")
		}
	})

	t.Run("SetFreeze + ClearDeepFreeze - CONFLICT", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagSetFreeze | TrustSetFlagClearDeepFreeze)

		err := ts.Validate()
		if err == nil {
			t.Error("expected error for conflicting freeze/clear deep freeze flags")
		}
	})

	t.Run("SetDeepFreeze + ClearFreeze - CONFLICT", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagSetDeepFreeze | TrustSetFlagClearFreeze)

		err := ts.Validate()
		if err == nil {
			t.Error("expected error for conflicting deep freeze/clear freeze flags")
		}
	})

	t.Run("SetFreeze + SetDeepFreeze - no conflict", func(t *testing.T) {
		ts := NewTrustSet("rGateway", tx.NewIssuedAmountFromFloat64(0, "USD", "rAlice"))
		ts.SetFlags(TrustSetFlagSetFreeze | TrustSetFlagSetDeepFreeze)

		if err := ts.Validate(); err != nil {
			t.Errorf("SetFreeze + SetDeepFreeze should be valid: %v", err)
		}
	})
}

// TestTrustSetQualityOneConstant tests the QUALITY_ONE constant.
func TestTrustSetQualityOneConstant(t *testing.T) {
	if tx.QualityOne != 1000000000 {
		t.Errorf("expected QualityOne=1000000000, got %d", tx.QualityOne)
	}
}
