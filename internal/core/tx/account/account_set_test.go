package account

import (
	"github.com/LeJamon/goXRPLd/internal/core/tx"
	"testing"
)

// Helper function to create a uint32 pointer
func ptrUint32AccountSet(v uint32) *uint32 {
	return &v
}

// Helper function to create a string pointer
func strPtr(v string) *string {
	return &v
}

// Helper function to create a uint8 pointer
func ptrUint8(v uint8) *uint8 {
	return &v
}

// TestAccountSetValidation tests AccountSet transaction validation.
// Reference: rippled AccountSet_test.cpp
func TestAccountSetValidation(t *testing.T) {
	tests := []struct {
		name        string
		accountSet  *AccountSet
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name: "valid empty AccountSet",
			accountSet: &AccountSet{
				BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
			},
			expectError: false,
		},
		{
			name: "valid AccountSet with SetFlag",
			accountSet: &AccountSet{
				BaseTx:  *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				SetFlag: ptrUint32AccountSet(AccountSetFlagRequireDest),
			},
			expectError: false,
		},
		{
			name: "valid AccountSet with ClearFlag",
			accountSet: &AccountSet{
				BaseTx:    *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				ClearFlag: ptrUint32AccountSet(AccountSetFlagRequireDest),
			},
			expectError: false,
		},
		{
			name: "valid AccountSet with Domain",
			accountSet: &AccountSet{
				BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				Domain: strPtr("6578616d706c652e636f6d"), // example.com in hex
			},
			expectError: false,
		},
		// Invalid: Set and Clear same flag
		// Reference: rippled AccountSet_test.cpp testBadInputs
		{
			name: "set and clear same flag - temINVALID_FLAG",
			accountSet: &AccountSet{
				BaseTx:    *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				SetFlag:   ptrUint32AccountSet(AccountSetFlagDisallowXRP),
				ClearFlag: ptrUint32AccountSet(AccountSetFlagDisallowXRP),
			},
			expectError: true,
			errorMsg:    "temINVALID_FLAG: cannot set and clear the same flag",
		},
		{
			name: "set and clear RequireAuth - temINVALID_FLAG",
			accountSet: &AccountSet{
				BaseTx:    *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				SetFlag:   ptrUint32AccountSet(AccountSetFlagRequireAuth),
				ClearFlag: ptrUint32AccountSet(AccountSetFlagRequireAuth),
			},
			expectError: true,
			errorMsg:    "temINVALID_FLAG: cannot set and clear the same flag",
		},
		{
			name: "set and clear RequireDest - temINVALID_FLAG",
			accountSet: &AccountSet{
				BaseTx:    *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				SetFlag:   ptrUint32AccountSet(AccountSetFlagRequireDest),
				ClearFlag: ptrUint32AccountSet(AccountSetFlagRequireDest),
			},
			expectError: true,
			errorMsg:    "temINVALID_FLAG: cannot set and clear the same flag",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.accountSet.Validate()
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

// TestAccountSetTransferRate tests TransferRate validation.
// Reference: rippled AccountSet_test.cpp testTransferRate
func TestAccountSetTransferRate(t *testing.T) {
	tests := []struct {
		name         string
		transferRate uint32
		expectError  bool
		errorMsg     string
	}{
		// Valid rates
		{
			name:         "transfer rate 1.0 (QUALITY_ONE)",
			transferRate: 1000000000,
			expectError:  false,
		},
		{
			name:         "transfer rate 1.1",
			transferRate: 1100000000,
			expectError:  false,
		},
		{
			name:         "transfer rate 2.0 (max)",
			transferRate: 2000000000,
			expectError:  false,
		},
		{
			name:         "transfer rate 0 (clear)",
			transferRate: 0,
			expectError:  false,
		},
		// Invalid rates
		{
			name:         "transfer rate too large (2.1)",
			transferRate: 2100000000,
			expectError:  true,
			errorMsg:     "temBAD_TRANSFER_RATE: transfer rate too large",
		},
		{
			name:         "transfer rate too small (0.9)",
			transferRate: 900000000,
			expectError:  true,
			errorMsg:     "temBAD_TRANSFER_RATE: transfer rate too small",
		},
		{
			name:         "transfer rate too small (1)",
			transferRate: 1,
			expectError:  true,
			errorMsg:     "temBAD_TRANSFER_RATE: transfer rate too small",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountSet := &AccountSet{
				BaseTx:       *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				TransferRate: ptrUint32AccountSet(tt.transferRate),
			}
			err := accountSet.Validate()
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

// TestAccountSetTickSize tests TickSize validation.
// Reference: rippled Quality.h minTickSize=3, maxTickSize=15
func TestAccountSetTickSize(t *testing.T) {
	tests := []struct {
		name        string
		tickSize    uint8
		expectError bool
		errorMsg    string
	}{
		// Valid tick sizes
		{
			name:        "tick size 0 (clear)",
			tickSize:    0,
			expectError: false,
		},
		{
			name:        "tick size 3 (min)",
			tickSize:    3,
			expectError: false,
		},
		{
			name:        "tick size 10",
			tickSize:    10,
			expectError: false,
		},
		{
			name:        "tick size 15 (max)",
			tickSize:    15,
			expectError: false,
		},
		// Invalid tick sizes
		{
			name:        "tick size 1 - temBAD_TICK_SIZE",
			tickSize:    1,
			expectError: true,
			errorMsg:    "temBAD_TICK_SIZE: tick size must be 0 or 3-15",
		},
		{
			name:        "tick size 2 - temBAD_TICK_SIZE",
			tickSize:    2,
			expectError: true,
			errorMsg:    "temBAD_TICK_SIZE: tick size must be 0 or 3-15",
		},
		{
			name:        "tick size 16 - temBAD_TICK_SIZE",
			tickSize:    16,
			expectError: true,
			errorMsg:    "temBAD_TICK_SIZE: tick size must be 0 or 3-15",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountSet := &AccountSet{
				BaseTx:   *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				TickSize: ptrUint8(tt.tickSize),
			}
			err := accountSet.Validate()
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

// TestAccountSetDomain tests Domain validation.
// Reference: rippled AccountSet_test.cpp testDomain
func TestAccountSetDomain(t *testing.T) {
	tests := []struct {
		name        string
		domain      string
		expectError bool
		errorMsg    string
	}{
		// Valid domains
		{
			name:        "valid domain (example.com in hex)",
			domain:      "6578616d706c652e636f6d",
			expectError: false,
		},
		{
			name:        "empty domain (clear)",
			domain:      "",
			expectError: false,
		},
		{
			name:        "max length domain (256 bytes = 512 hex chars)",
			domain:      makeHexString(256),
			expectError: false,
		},
		// Invalid domains
		{
			name:        "domain too long (257 bytes)",
			domain:      makeHexString(257),
			expectError: true,
			errorMsg:    "telBAD_DOMAIN: domain too long",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountSet := &AccountSet{
				BaseTx: *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				Domain: &tt.domain,
			}
			err := accountSet.Validate()
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

// makeHexString creates a hex string of n bytes (2n hex characters)
func makeHexString(n int) string {
	result := make([]byte, n*2)
	for i := range result {
		result[i] = 'a'
	}
	return string(result)
}

// TestAccountSetNFTokenMinter tests NFTokenMinter validation.
// Reference: rippled SetAccount.cpp:177-187
func TestAccountSetNFTokenMinter(t *testing.T) {
	tests := []struct {
		name          string
		setFlag       *uint32
		clearFlag     *uint32
		nfTokenMinter string
		expectError   bool
		errorMsg      string
	}{
		// Valid cases
		{
			name:          "set NFTokenMinter with flag and minter address",
			setFlag:       ptrUint32AccountSet(AccountSetFlagAuthorizedNFTokenMinter),
			nfTokenMinter: "rBob",
			expectError:   false,
		},
		{
			name:          "clear NFTokenMinter with flag only",
			clearFlag:     ptrUint32AccountSet(AccountSetFlagAuthorizedNFTokenMinter),
			nfTokenMinter: "",
			expectError:   false,
		},
		// Invalid cases
		{
			name:          "set NFTokenMinter flag without minter - temMALFORMED",
			setFlag:       ptrUint32AccountSet(AccountSetFlagAuthorizedNFTokenMinter),
			nfTokenMinter: "",
			expectError:   true,
			errorMsg:      "temMALFORMED: NFTokenMinter required when setting asfAuthorizedNFTokenMinter",
		},
		{
			name:          "clear NFTokenMinter flag with minter present - temMALFORMED",
			clearFlag:     ptrUint32AccountSet(AccountSetFlagAuthorizedNFTokenMinter),
			nfTokenMinter: "rBob",
			expectError:   true,
			errorMsg:      "temMALFORMED: NFTokenMinter must be empty when clearing asfAuthorizedNFTokenMinter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			accountSet := &AccountSet{
				BaseTx:        *tx.NewBaseTx(tx.TypeAccountSet, "rAlice"),
				SetFlag:       tt.setFlag,
				ClearFlag:     tt.clearFlag,
				NFTokenMinter: tt.nfTokenMinter,
			}
			err := accountSet.Validate()
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

// TestAccountSetFlags tests the account set flag constants.
func TestAccountSetFlags(t *testing.T) {
	// Verify flag values match rippled protocol
	tests := []struct {
		name     string
		flag     uint32
		expected uint32
	}{
		{"asfRequireDest", AccountSetFlagRequireDest, 1},
		{"asfRequireAuth", AccountSetFlagRequireAuth, 2},
		{"asfDisallowXRP", AccountSetFlagDisallowXRP, 3},
		{"asfDisableMaster", AccountSetFlagDisableMaster, 4},
		{"asfAccountTxnID", AccountSetFlagAccountTxnID, 5},
		{"asfNoFreeze", AccountSetFlagNoFreeze, 6},
		{"asfGlobalFreeze", AccountSetFlagGlobalFreeze, 7},
		{"asfDefaultRipple", AccountSetFlagDefaultRipple, 8},
		{"asfDepositAuth", AccountSetFlagDepositAuth, 9},
		{"asfAuthorizedNFTokenMinter", AccountSetFlagAuthorizedNFTokenMinter, 10},
		{"asfDisallowIncomingNFTokenOffer", AccountSetFlagDisallowIncomingNFTokenOffer, 12},
		{"asfDisallowIncomingCheck", AccountSetFlagDisallowIncomingCheck, 13},
		{"asfDisallowIncomingPayChan", AccountSetFlagDisallowIncomingPayChan, 14},
		{"asfDisallowIncomingTrustline", AccountSetFlagDisallowIncomingTrustline, 15},
		{"asfAllowTrustLineClawback", AccountSetFlagAllowTrustLineClawback, 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.expected {
				t.Errorf("expected %s=%d, got %d", tt.name, tt.expected, tt.flag)
			}
		})
	}
}

// TestAccountSetTransactionFlags tests the transaction flag constants.
func TestAccountSetTransactionFlags(t *testing.T) {
	// Verify transaction flag values match rippled protocol
	tests := []struct {
		name     string
		flag     uint32
		expected uint32
	}{
		{"tfRequireDestTag", AccountSetTxFlagRequireDestTag, 0x00010000},
		{"tfOptionalDestTag", AccountSetTxFlagOptionalDestTag, 0x00020000},
		{"tfRequireAuth", AccountSetTxFlagRequireAuth, 0x00040000},
		{"tfOptionalAuth", AccountSetTxFlagOptionalAuth, 0x00080000},
		{"tfDisallowXRP", AccountSetTxFlagDisallowXRP, 0x00100000},
		{"tfAllowXRP", AccountSetTxFlagAllowXRP, 0x00200000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.expected {
				t.Errorf("expected %s=0x%08X, got 0x%08X", tt.name, tt.expected, tt.flag)
			}
		})
	}
}

// TestAccountSetFlatten tests the Flatten method.
func TestAccountSetFlatten(t *testing.T) {
	tests := []struct {
		name     string
		setup    func() *AccountSet
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "basic AccountSet",
			setup: func() *AccountSet {
				return NewAccountSet("rAlice")
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Account"] != "rAlice" {
					t.Errorf("expected Account=rAlice, got %v", m["Account"])
				}
				if m["TransactionType"] != "AccountSet" {
					t.Errorf("expected TransactionType=AccountSet, got %v", m["TransactionType"])
				}
			},
		},
		{
			name: "AccountSet with SetFlag",
			setup: func() *AccountSet {
				as := NewAccountSet("rAlice")
				as.SetFlag = ptrUint32AccountSet(AccountSetFlagRequireDest)
				return as
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["SetFlag"] != uint32(1) {
					t.Errorf("expected SetFlag=1, got %v", m["SetFlag"])
				}
			},
		},
		{
			name: "AccountSet with Domain",
			setup: func() *AccountSet {
				as := NewAccountSet("rAlice")
				d := "6578616d706c652e636f6d"
				as.Domain = &d
				return as
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Domain"] != "6578616d706c652e636f6d" {
					t.Errorf("expected Domain hex value, got %v", m["Domain"])
				}
			},
		},
		{
			name: "AccountSet with TransferRate",
			setup: func() *AccountSet {
				as := NewAccountSet("rAlice")
				as.TransferRate = ptrUint32AccountSet(1100000000)
				return as
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["TransferRate"] != uint32(1100000000) {
					t.Errorf("expected TransferRate=1100000000, got %v", m["TransferRate"])
				}
			},
		},
		{
			name: "AccountSet with TickSize",
			setup: func() *AccountSet {
				as := NewAccountSet("rAlice")
				as.TickSize = ptrUint8(5)
				return as
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["TickSize"] != uint8(5) {
					t.Errorf("expected TickSize=5, got %v", m["TickSize"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			as := tt.setup()
			m, err := as.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestAccountSetHelperMethods tests the helper methods.
func TestAccountSetHelperMethods(t *testing.T) {
	t.Run("EnableRequireDest", func(t *testing.T) {
		as := NewAccountSet("rAlice")
		as.EnableRequireDest()
		if as.SetFlag == nil || *as.SetFlag != AccountSetFlagRequireDest {
			t.Error("EnableRequireDest did not set SetFlag correctly")
		}
	})

	t.Run("EnableDepositAuth", func(t *testing.T) {
		as := NewAccountSet("rAlice")
		as.EnableDepositAuth()
		if as.SetFlag == nil || *as.SetFlag != AccountSetFlagDepositAuth {
			t.Error("EnableDepositAuth did not set SetFlag correctly")
		}
	})

	t.Run("EnableDefaultRipple", func(t *testing.T) {
		as := NewAccountSet("rAlice")
		as.EnableDefaultRipple()
		if as.SetFlag == nil || *as.SetFlag != AccountSetFlagDefaultRipple {
			t.Error("EnableDefaultRipple did not set SetFlag correctly")
		}
	})

	t.Run("TxType", func(t *testing.T) {
		as := NewAccountSet("rAlice")
		if as.TxType() != tx.TypeAccountSet {
			t.Errorf("expected TypeAccountSet, got %v", as.TxType())
		}
	})
}
