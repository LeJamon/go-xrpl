package mpt

import (
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

// preflightMPTSet runs MPTokenIssuanceSet's preflight body in engine order: the
// rules-free Validate() (flags mask + IssuanceID) then PreflightRules() (the
// DomainID/Holder, lock/unlock, no-op and mutation checks).
func preflightMPTSet(m *MPTokenIssuanceSet, rules *amendment.Rules) error {
	if err := m.Validate(); err != nil {
		return err
	}
	return m.PreflightRules(rules)
}

// Helper functions for pointers
func ptrUint8MPT(v uint8) *uint8 {
	return &v
}

func ptrUint16MPT(v uint16) *uint16 {
	return &v
}

func ptrUint64MPT(v uint64) *uint64 {
	return &v
}

// ptrUint32 creates a pointer to a uint32 value (test helper)
func ptrUint32AccountSet(v uint32) *uint32 {
	return &v
}

func ptrStringMPT(v string) *string {
	return &v
}

// TestMPTokenIssuanceCreateValidation tests MPTokenIssuanceCreate transaction validation.
// Reference: rippled MPTokenIssuanceCreate.cpp preflight
func TestMPTokenIssuanceCreateValidation(t *testing.T) {
	tests := []struct {
		name        string
		tx          *MPTokenIssuanceCreate
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name: "valid basic issuance create",
			tx: &MPTokenIssuanceCreate{
				BaseTx: *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
			},
			expectError: false,
		},
		{
			name: "valid with all optional fields",
			tx: func() *MPTokenIssuanceCreate {
				tx := NewMPTokenIssuanceCreate("rAlice")
				tx.AssetScale = ptrUint8MPT(2)
				tx.MaximumAmount = ptrUint64MPT(1000000000)
				tx.TransferFee = ptrUint16MPT(100)
				tx.MPTokenMetadata = ptrStringMPT("48656c6c6f") // "Hello" in hex
				// Need tfMPTCanTransfer for TransferFee
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceCreateFlagCanTransfer)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid with tfMPTCanLock",
			tx: func() *MPTokenIssuanceCreate {
				tx := NewMPTokenIssuanceCreate("rAlice")
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceCreateFlagCanLock)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid with tfMPTCanTransfer and TransferFee",
			tx: func() *MPTokenIssuanceCreate {
				tx := NewMPTokenIssuanceCreate("rAlice")
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceCreateFlagCanTransfer)
				tx.TransferFee = ptrUint16MPT(1000)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid with multiple flags",
			tx: func() *MPTokenIssuanceCreate {
				tx := NewMPTokenIssuanceCreate("rAlice")
				flags := MPTokenIssuanceCreateFlagCanLock |
					MPTokenIssuanceCreateFlagRequireAuth |
					MPTokenIssuanceCreateFlagCanTransfer
				tx.Flags = ptrUint32AccountSet(flags)
				return tx
			}(),
			expectError: false,
		},
		// Invalid cases
		{
			name: "invalid flags - temINVALID_FLAG",
			tx: func() *MPTokenIssuanceCreate {
				tx := NewMPTokenIssuanceCreate("rAlice")
				tx.Flags = ptrUint32AccountSet(0x01000000) // Invalid flag
				return tx
			}(),
			expectError: true,
			errorMsg:    "temINVALID_FLAG: invalid flags for MPTokenIssuanceCreate",
		},
		{
			name: "TransferFee exceeds max - temBAD_TRANSFER_FEE",
			tx: &MPTokenIssuanceCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
				TransferFee: ptrUint16MPT(50001), // Max is 50000
			},
			expectError: true,
			errorMsg:    "temBAD_TRANSFER_FEE: TransferFee cannot exceed 50000",
		},
		{
			name: "TransferFee without tfMPTCanTransfer - temMALFORMED",
			tx: &MPTokenIssuanceCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
				TransferFee: ptrUint16MPT(100), // Non-zero fee without CanTransfer flag
			},
			expectError: true,
			errorMsg:    "temMALFORMED: TransferFee requires tfMPTCanTransfer flag",
		},
		{
			name: "MaximumAmount zero - temMALFORMED",
			tx: &MPTokenIssuanceCreate{
				BaseTx:        *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
				MaximumAmount: ptrUint64MPT(0),
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MaximumAmount cannot be zero",
		},
		{
			name: "invalid metadata - temMALFORMED",
			tx: &MPTokenIssuanceCreate{
				BaseTx:          *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
				MPTokenMetadata: ptrStringMPT("XYZ"), // Invalid hex
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenMetadata must be valid hex",
		},
		{
			name: "empty metadata hex - temMALFORMED",
			tx: &MPTokenIssuanceCreate{
				BaseTx:          *tx.NewBaseTx(tx.TypeMPTokenIssuanceCreate, "rAlice"),
				MPTokenMetadata: ptrStringMPT(""), // Empty string = 0 bytes decoded
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenMetadata length must be 1-1024 bytes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tx.Validate()
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

// TestMPTokenIssuanceDestroyValidation tests MPTokenIssuanceDestroy transaction validation.
// Reference: rippled MPTokenIssuanceDestroy.cpp preflight
func TestMPTokenIssuanceDestroyValidation(t *testing.T) {
	tests := []struct {
		name        string
		tx          *MPTokenIssuanceDestroy
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name: "valid destroy",
			tx: &MPTokenIssuanceDestroy{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "rAlice"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
			},
			expectError: false,
		},
		// Invalid cases
		{
			name: "missing issuance ID - temMALFORMED",
			tx: &MPTokenIssuanceDestroy{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "rAlice"),
				MPTokenIssuanceID: "",
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID is required",
		},
		{
			name: "invalid issuance ID length - temMALFORMED",
			tx: &MPTokenIssuanceDestroy{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "rAlice"),
				MPTokenIssuanceID: "0001", // Too short
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID must be 48 hex characters",
		},
		{
			name: "invalid issuance ID hex - temMALFORMED",
			tx: &MPTokenIssuanceDestroy{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceDestroy, "rAlice"),
				MPTokenIssuanceID: "ZZZZ00000000000000000000000000000000000000000001", // 48 chars but invalid hex
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID must be valid hex",
		},
		{
			name: "invalid flags - temINVALID_FLAG",
			tx: func() *MPTokenIssuanceDestroy {
				tx := NewMPTokenIssuanceDestroy("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(0x00000001) // Invalid flag
				return tx
			}(),
			expectError: true,
			errorMsg:    "temINVALID_FLAG: invalid flags for MPTokenIssuanceDestroy",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tx.Validate()
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

// TestMPTokenIssuanceSetValidation tests MPTokenIssuanceSet transaction validation.
// Reference: rippled MPTokenIssuanceSet.cpp preflight
func TestMPTokenIssuanceSetValidation(t *testing.T) {
	tests := []struct {
		name        string
		tx          *MPTokenIssuanceSet
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name: "valid set without flags",
			tx: &MPTokenIssuanceSet{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "rAlice"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
			},
			expectError: false,
		},
		{
			name: "valid set with tfMPTLock",
			tx: func() *MPTokenIssuanceSet {
				tx := NewMPTokenIssuanceSet("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagLock)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid set with tfMPTUnlock",
			tx: func() *MPTokenIssuanceSet {
				tx := NewMPTokenIssuanceSet("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagUnlock)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid set with holder",
			tx: &MPTokenIssuanceSet{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "rAlice"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
				Holder:            "rBob",
			},
			expectError: false,
		},
		// Invalid cases
		{
			name: "missing issuance ID - temMALFORMED",
			tx: &MPTokenIssuanceSet{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "rAlice"),
				MPTokenIssuanceID: "",
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID is required",
		},
		{
			name: "both lock and unlock flags - temINVALID_FLAG",
			tx: func() *MPTokenIssuanceSet {
				tx := NewMPTokenIssuanceSet("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(MPTokenIssuanceSetFlagLock | MPTokenIssuanceSetFlagUnlock)
				return tx
			}(),
			expectError: true,
			errorMsg:    "temINVALID_FLAG: cannot set both tfMPTLock and tfMPTUnlock",
		},
		{
			name: "invalid flags - temINVALID_FLAG",
			tx: func() *MPTokenIssuanceSet {
				tx := NewMPTokenIssuanceSet("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(0x00000004) // Invalid flag
				return tx
			}(),
			expectError: true,
			errorMsg:    "temINVALID_FLAG: invalid flags for MPTokenIssuanceSet",
		},
		{
			name: "holder same as account - temMALFORMED",
			tx: &MPTokenIssuanceSet{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenIssuanceSet, "rAlice"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
				Holder:            "rAlice", // Same as Account
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Holder cannot be the same as Account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Run the full preflight body: the flags mask and IssuanceID checks
			// live in Validate(), the DomainID/Holder and lock/unlock shape checks
			// in PreflightRules(). allRules() excludes SingleAssetVault (and
			// DynamicMPT is unsupported), so the no-op check does not disturb
			// these cases.
			err := preflightMPTSet(tt.tx, allRules())
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

// TestMPTokenIssuanceSetPreflightOrder pins the MPTokenIssuanceSet precedence
// findings: the mutation-fields-require-DynamicMPT temDISABLED gate leads the
// preflight body (so it beats the DomainID+Holder temMALFORMED check), the flags
// mask (Validate) precedes the DomainID+Holder check, and the "changes nothing"
// no-op check is a preflight (not apply-time) rejection.
// Reference: rippled MPTokenIssuanceSet.cpp preflight().
func TestMPTokenIssuanceSetPreflightOrder(t *testing.T) {
	const validID = "000000000000000000000000000000000000000000000001"
	const someDomain = "1111111111111111111111111111111111111111111111111111111111111111"

	// Finding: isMutate temDISABLED (DynamicMPT off) wins over DomainID+Holder.
	t.Run("mutation temDISABLED beats DomainID+Holder", func(t *testing.T) {
		m := NewMPTokenIssuanceSet("rAlice", validID)
		m.MutableFlags = ptrUint32AccountSet(TmfMPTSetCanLock) // mutation → isMutate
		dom := someDomain
		m.DomainID = &dom
		m.hasDomainID = true
		m.Holder = "rBob" // DomainID+Holder together → temMALFORMED if reached
		if err := preflightMPTSet(m, allRules()); err == nil || err.Error() != "temDISABLED: mutation fields require DynamicMPT" {
			t.Fatalf("got %v, want temDISABLED", err)
		}
	})

	// Finding: the flags mask (Validate) precedes the DomainID+Holder check
	// (PreflightRules), so an undefined flag bit wins over temMALFORMED.
	t.Run("flags mask beats DomainID+Holder", func(t *testing.T) {
		m := NewMPTokenIssuanceSet("rAlice", validID)
		m.SetFlags(0x00000004) // undefined bit → temINVALID_FLAG
		dom := someDomain
		m.DomainID = &dom
		m.hasDomainID = true
		m.Holder = "rBob"
		if err := preflightMPTSet(m, allRules()); err == nil || err.Error() != "temINVALID_FLAG: invalid flags for MPTokenIssuanceSet" {
			t.Fatalf("got %v, want temINVALID_FLAG", err)
		}
	})

	// Finding: the "changes nothing" no-op check is enforced in preflight (under
	// SingleAssetVault), before signature verification.
	t.Run("no-op is a preflight temMALFORMED", func(t *testing.T) {
		savRules := amendment.NewRulesBuilder().FromPreset(amendment.PresetAllSupported).EnableByName("SingleAssetVault").Build()
		m := NewMPTokenIssuanceSet("rAlice", validID) // flags 0, no domain, no mutation
		if err := m.PreflightRules(savRules); err == nil || err.Error() != "temMALFORMED: MPTokenIssuanceSet changes nothing" {
			t.Fatalf("got %v, want temMALFORMED (no-op)", err)
		}
	})
}

// allRules mirrors rippled's testSetValidation(all - featureSingleAssetVault):
// every supported amendment except SingleAssetVault, so the SAV/DynamicMPT
// "changes nothing" no-op rejection does not fire and the legacy Set-validation
// shape checks are exercised in isolation. The SAV-on no-op behaviour has its
// own coverage via a rules set that enables SingleAssetVault.
func allRules() *amendment.Rules {
	return amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		DisableByName("SingleAssetVault").
		Build()
}

// TestMPTokenAuthorizeValidation tests MPTokenAuthorize transaction validation.
// Reference: rippled MPTokenAuthorize.cpp preflight
func TestMPTokenAuthorizeValidation(t *testing.T) {
	tests := []struct {
		name        string
		tx          *MPTokenAuthorize
		expectError bool
		errorMsg    string
	}{
		// Valid cases
		{
			name: "valid holder authorize (create MPToken)",
			tx: &MPTokenAuthorize{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "rBob"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
			},
			expectError: false,
		},
		{
			name: "valid holder unauthorize (delete MPToken)",
			tx: func() *MPTokenAuthorize {
				tx := NewMPTokenAuthorize("rBob", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(MPTokenAuthorizeFlagUnauthorize)
				return tx
			}(),
			expectError: false,
		},
		{
			name: "valid issuer authorize holder",
			tx: &MPTokenAuthorize{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "rAlice"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
				Holder:            "rBob",
			},
			expectError: false,
		},
		{
			name: "valid issuer unauthorize holder",
			tx: func() *MPTokenAuthorize {
				tx := NewMPTokenAuthorize("rAlice", "000000000000000000000000000000000000000000000001")
				tx.Holder = "rBob"
				tx.Flags = ptrUint32AccountSet(MPTokenAuthorizeFlagUnauthorize)
				return tx
			}(),
			expectError: false,
		},
		// Invalid cases
		{
			name: "missing issuance ID - temMALFORMED",
			tx: &MPTokenAuthorize{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "rBob"),
				MPTokenIssuanceID: "",
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID is required",
		},
		{
			name: "invalid issuance ID - temMALFORMED",
			tx: &MPTokenAuthorize{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "rBob"),
				MPTokenIssuanceID: "0001", // Too short
			},
			expectError: true,
			errorMsg:    "temMALFORMED: MPTokenIssuanceID must be 48 hex characters",
		},
		{
			name: "invalid flags - temINVALID_FLAG",
			tx: func() *MPTokenAuthorize {
				tx := NewMPTokenAuthorize("rBob", "000000000000000000000000000000000000000000000001")
				tx.Flags = ptrUint32AccountSet(0x00000002) // Invalid flag
				return tx
			}(),
			expectError: true,
			errorMsg:    "temINVALID_FLAG: invalid flags for MPTokenAuthorize",
		},
		{
			name: "holder same as account - temMALFORMED",
			tx: &MPTokenAuthorize{
				BaseTx:            *tx.NewBaseTx(tx.TypeMPTokenAuthorize, "rBob"),
				MPTokenIssuanceID: "000000000000000000000000000000000000000000000001",
				Holder:            "rBob", // Same as Account
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Holder cannot be the same as Account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.tx.Validate()
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
