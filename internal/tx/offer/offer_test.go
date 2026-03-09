package offer

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// ptrUint32 returns a pointer to a uint32 value
func ptrUint32(v uint32) *uint32 {
	return &v
}

// Helper functions to create amounts with the correct API
func xrpAmount(drops int64) tx.Amount {
	return tx.NewXRPAmount(drops)
}

func iouAmount(value float64, currency, issuer string) tx.Amount {
	return tx.NewIssuedAmountFromFloat64(value, currency, issuer)
}

// TestOfferCreateValidation tests OfferCreate transaction validation.
// These tests are translated from rippled's Offer_test.cpp focusing on
// validation logic and error conditions from testOfferCancel, testExpiration,
// and the order form validity checks.
func TestOfferCreateValidation(t *testing.T) {
	tests := []struct {
		name        string
		offer       *OfferCreate
		expectError bool
		errorMsg    string
	}{
		// Valid offer scenarios
		{
			name: "valid XRP for IOU offer",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000), // 1000 XRP in drops
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			expectError: false,
		},
		{
			name: "valid IOU for XRP offer",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: iouAmount(100, "USD", "rGateway"),
				TakerPays: xrpAmount(1000000000),
			},
			expectError: false,
		},
		{
			name: "valid IOU for IOU offer",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: iouAmount(100, "USD", "rGateway"),
				TakerPays: iouAmount(80, "EUR", "rEuroGateway"),
			},
			expectError: false,
		},
		// Missing required fields
		{
			name: "missing TakerGets - temBAD_OFFER equivalent",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: tx.Amount{}, // Empty
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			expectError: true,
			errorMsg:    "temBAD_OFFER: TakerGets is required",
		},
		{
			name: "missing TakerPays - temBAD_OFFER equivalent",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: tx.Amount{}, // Empty
			},
			expectError: true,
			errorMsg:    "temBAD_OFFER: TakerPays is required",
		},
		{
			name: "missing account",
			offer: &OfferCreate{
				BaseTx:    tx.BaseTx{Common: tx.Common{TransactionType: "OfferCreate"}},
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
		// Note: XRP-to-XRP validation is done in Preflight(), not Validate()
		// This test is commented out because Validate() only checks required fields
		// The full semantic validation happens in Preflight()
		// Valid offer with optional Expiration field
		{
			name: "valid offer with expiration",
			offer: &OfferCreate{
				BaseTx:     *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets:  xrpAmount(1000000000),
				TakerPays:  iouAmount(100, "USD", "rGateway"),
				Expiration: ptrUint32(700000000),
			},
			expectError: false,
		},
		// Valid offer with OfferSequence for replacing offers
		{
			name: "valid offer with OfferSequence (replace offer)",
			offer: &OfferCreate{
				BaseTx:        *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets:     xrpAmount(1000000000),
				TakerPays:     iouAmount(100, "USD", "rGateway"),
				OfferSequence: ptrUint32(12345),
			},
			expectError: false,
		},
		// Valid offer with both Expiration and OfferSequence
		{
			name: "valid offer with expiration and OfferSequence",
			offer: &OfferCreate{
				BaseTx:        *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets:     xrpAmount(1000000000),
				TakerPays:     iouAmount(100, "USD", "rGateway"),
				Expiration:    ptrUint32(800000000),
				OfferSequence: ptrUint32(12345),
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.offer.Validate()
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

// TestOfferCreateFlags tests OfferCreate transaction flag validation.
// These tests are translated from rippled's Offer_test.cpp focusing on
// the tfPassive, tfImmediateOrCancel, tfFillOrKill, and tfSell flags.
func TestOfferCreateFlags(t *testing.T) {
	tests := []struct {
		name        string
		offer       *OfferCreate
		setupFlags  func(o *OfferCreate)
		expectError bool
		errorMsg    string
		checkFlags  func(t *testing.T, flags uint32)
	}{
		{
			name: "tfPassive flag",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			setupFlags: func(o *OfferCreate) {
				o.SetPassive()
			},
			expectError: false,
			checkFlags: func(t *testing.T, flags uint32) {
				if flags&OfferCreateFlagPassive == 0 {
					t.Error("expected tfPassive flag to be set")
				}
			},
		},
		{
			name: "tfImmediateOrCancel flag",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			setupFlags: func(o *OfferCreate) {
				o.SetImmediateOrCancel()
			},
			expectError: false,
			checkFlags: func(t *testing.T, flags uint32) {
				if flags&OfferCreateFlagImmediateOrCancel == 0 {
					t.Error("expected tfImmediateOrCancel flag to be set")
				}
			},
		},
		{
			name: "tfFillOrKill flag",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			setupFlags: func(o *OfferCreate) {
				o.SetFillOrKill()
			},
			expectError: false,
			checkFlags: func(t *testing.T, flags uint32) {
				if flags&OfferCreateFlagFillOrKill == 0 {
					t.Error("expected tfFillOrKill flag to be set")
				}
			},
		},
		{
			name: "tfSell flag",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			setupFlags: func(o *OfferCreate) {
				flags := o.GetFlags() | OfferCreateFlagSell
				o.SetFlags(flags)
			},
			expectError: false,
			checkFlags: func(t *testing.T, flags uint32) {
				if flags&OfferCreateFlagSell == 0 {
					t.Error("expected tfSell flag to be set")
				}
			},
		},
		{
			name: "tfPassive and tfSell combined",
			offer: &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: xrpAmount(1000000000),
				TakerPays: iouAmount(100, "USD", "rGateway"),
			},
			setupFlags: func(o *OfferCreate) {
				o.SetPassive()
				flags := o.GetFlags() | OfferCreateFlagSell
				o.SetFlags(flags)
			},
			expectError: false,
			checkFlags: func(t *testing.T, flags uint32) {
				if flags&OfferCreateFlagPassive == 0 {
					t.Error("expected tfPassive flag to be set")
				}
				if flags&OfferCreateFlagSell == 0 {
					t.Error("expected tfSell flag to be set")
				}
			},
		},
		// Note: Conflicting flag validation (IoC+FoK) is done in Preflight(), not Validate()
		// This test is commented out because Validate() only checks required fields
		// The full semantic validation happens in Preflight()
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.setupFlags != nil {
				tt.setupFlags(tt.offer)
			}

			err := tt.offer.Validate()
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
				if tt.checkFlags != nil {
					tt.checkFlags(t, tt.offer.GetFlags())
				}
			}
		})
	}
}

// TestOfferCancelValidation tests OfferCancel transaction validation.
// These tests are translated from rippled's Offer_test.cpp focusing on
// testOfferCancelPastAndFuture and related validation.
func TestOfferCancelValidation(t *testing.T) {
	tests := []struct {
		name        string
		offer       *OfferCancel
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid cancel",
			offer: &OfferCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeOfferCancel, "rAlice"),
				OfferSequence: 12345,
			},
			expectError: false,
		},
		{
			name: "valid cancel with high sequence number",
			offer: &OfferCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeOfferCancel, "rAlice"),
				OfferSequence: 4294967295, // Max uint32
			},
			expectError: false,
		},
		{
			name: "missing OfferSequence (zero) - temBAD_SEQUENCE equivalent",
			offer: &OfferCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeOfferCancel, "rAlice"),
				OfferSequence: 0,
			},
			expectError: true,
			errorMsg:    "temBAD_SEQUENCE: OfferSequence is required and cannot be zero",
		},
		{
			name: "missing account",
			offer: &OfferCancel{
				BaseTx:        tx.BaseTx{Common: tx.Common{TransactionType: "OfferCancel"}},
				OfferSequence: 12345,
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.offer.Validate()
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

// TestOfferTransactionTypes tests that transaction types are correctly returned.
func TestOfferTransactionTypes(t *testing.T) {
	t.Run("OfferCreate type", func(t *testing.T) {
		o := NewOfferCreate("rAlice", xrpAmount(1000000), iouAmount(10, "USD", "rGateway"))
		if o.TxType() != tx.TypeOfferCreate {
			t.Errorf("expected tx.TypeOfferCreate, got %v", o.TxType())
		}
	})

	t.Run("OfferCancel type", func(t *testing.T) {
		o := NewOfferCancel("rAlice", 123)
		if o.TxType() != tx.TypeOfferCancel {
			t.Errorf("expected tx.TypeOfferCancel, got %v", o.TxType())
		}
	})
}

// TestNewOfferConstructors tests the constructor functions.
func TestNewOfferConstructors(t *testing.T) {
	t.Run("NewOfferCreate", func(t *testing.T) {
		takerGets := xrpAmount(1000000)
		takerPays := iouAmount(10, "USD", "rGateway")
		o := NewOfferCreate("rAlice", takerGets, takerPays)

		if o.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", o.Account)
		}
		// Check that amounts are set (exact comparison depends on internal representation)
		if o.TakerGets.IsZero() {
			t.Error("expected TakerGets to be non-zero")
		}
		if o.TakerPays.IsZero() {
			t.Error("expected TakerPays to be non-zero")
		}
	})

	t.Run("NewOfferCancel", func(t *testing.T) {
		o := NewOfferCancel("rAlice", 456)

		if o.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", o.Account)
		}
		if o.OfferSequence != 456 {
			t.Errorf("expected OfferSequence=456, got %v", o.OfferSequence)
		}
	})
}

// TestOfferCreateSetterMethods tests the flag setter methods.
func TestOfferCreateSetterMethods(t *testing.T) {
	t.Run("SetPassive", func(t *testing.T) {
		o := NewOfferCreate("rAlice", xrpAmount(1000000), iouAmount(10, "USD", "rGateway"))
		o.SetPassive()

		if o.GetFlags()&OfferCreateFlagPassive == 0 {
			t.Error("expected Passive flag to be set")
		}
	})

	t.Run("SetImmediateOrCancel", func(t *testing.T) {
		o := NewOfferCreate("rAlice", xrpAmount(1000000), iouAmount(10, "USD", "rGateway"))
		o.SetImmediateOrCancel()

		if o.GetFlags()&OfferCreateFlagImmediateOrCancel == 0 {
			t.Error("expected ImmediateOrCancel flag to be set")
		}
	})

	t.Run("SetFillOrKill", func(t *testing.T) {
		o := NewOfferCreate("rAlice", xrpAmount(1000000), iouAmount(10, "USD", "rGateway"))
		o.SetFillOrKill()

		if o.GetFlags()&OfferCreateFlagFillOrKill == 0 {
			t.Error("expected FillOrKill flag to be set")
		}
	})

	t.Run("multiple flags combined", func(t *testing.T) {
		o := NewOfferCreate("rAlice", xrpAmount(1000000), iouAmount(10, "USD", "rGateway"))
		o.SetPassive()
		flags := o.GetFlags() | OfferCreateFlagSell
		o.SetFlags(flags)

		if o.GetFlags()&OfferCreateFlagPassive == 0 {
			t.Error("expected Passive flag to be set")
		}
		if o.GetFlags()&OfferCreateFlagSell == 0 {
			t.Error("expected Sell flag to be set")
		}
	})
}

// TestOfferFlagConstants tests that flag constants have the correct values
// as defined in the XRP Ledger protocol.
func TestOfferFlagConstants(t *testing.T) {
	tests := []struct {
		name     string
		flag     uint32
		expected uint32
	}{
		{"tfPassive", OfferCreateFlagPassive, 0x00010000},
		{"tfImmediateOrCancel", OfferCreateFlagImmediateOrCancel, 0x00020000},
		{"tfFillOrKill", OfferCreateFlagFillOrKill, 0x00040000},
		{"tfSell", OfferCreateFlagSell, 0x00080000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.expected {
				t.Errorf("expected %s=0x%08X, got 0x%08X", tt.name, tt.expected, tt.flag)
			}
		})
	}
}

// TestOfferCreateAmountTypes tests various amount type combinations.
func TestOfferCreateAmountTypes(t *testing.T) {
	tests := []struct {
		name        string
		takerGets   tx.Amount
		takerPays   tx.Amount
		expectError bool
		errorMsg    string
	}{
		{
			name:        "XRP gets, USD pays",
			takerGets:   xrpAmount(1000000),
			takerPays:   iouAmount(100, "USD", "rGateway"),
			expectError: false,
		},
		{
			name:        "USD gets, XRP pays",
			takerGets:   iouAmount(100, "USD", "rGateway"),
			takerPays:   xrpAmount(1000000),
			expectError: false,
		},
		{
			name:        "USD gets, EUR pays",
			takerGets:   iouAmount(100, "USD", "rGateway"),
			takerPays:   iouAmount(90, "EUR", "rEuroGateway"),
			expectError: false,
		},
		{
			name:        "same IOU both sides - different issuers is valid",
			takerGets:   iouAmount(100, "USD", "rGateway1"),
			takerPays:   iouAmount(100, "USD", "rGateway2"),
			expectError: false,
		},
		// Note: XRP-to-XRP validation is done in Preflight(), not Validate()
		// This test is commented out for TestOfferCreateAmountTypes which calls Validate()
		{
			name:        "large XRP amount",
			takerGets:   xrpAmount(100000000000000), // 100 million XRP
			takerPays:   iouAmount(1000000, "USD", "rGateway"),
			expectError: false,
		},
		{
			name:        "small XRP amount (1 drop)",
			takerGets:   xrpAmount(1),
			takerPays:   iouAmount(0.000001, "USD", "rGateway"),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			offer := &OfferCreate{
				BaseTx:    *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
				TakerGets: tt.takerGets,
				TakerPays: tt.takerPays,
			}

			err := offer.Validate()
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

// TestLedgerOfferFlags tests the ledger offer flag constants.
func TestLedgerOfferFlags(t *testing.T) {
	// These are the flags that are stored in the ledger for offers
	if lsfOfferPassive != 0x00010000 {
		t.Errorf("expected lsfOfferPassive=0x00010000, got 0x%08X", lsfOfferPassive)
	}
	if lsfOfferSell != 0x00020000 {
		t.Errorf("expected lsfOfferSell=0x00020000, got 0x%08X", lsfOfferSell)
	}
}

// TestOfferExpirationValidation tests offer expiration validation.
// Reference: rippled Offer_test.cpp::testExpiration
// Note: Expiration validation is done in Preflight(), not Validate().
// Validate() only checks required fields.
func TestOfferExpirationValidation(t *testing.T) {
	// Test that tecEXPIRED result code exists
	if tx.TecEXPIRED != 148 {
		t.Errorf("expected tx.TecEXPIRED=148, got %d", tx.TecEXPIRED)
	}

	// Valid offer with expiration passes Validate() (semantic checks are in Preflight)
	offer := &OfferCreate{
		BaseTx:     *tx.NewBaseTx(tx.TypeOfferCreate, "rAlice"),
		TakerGets:  xrpAmount(1000000),
		TakerPays:  iouAmount(100, "USD", "rGateway"),
		Expiration: ptrUint32(700000000),
	}

	err := offer.Validate()
	if err != nil {
		t.Errorf("expected no error for valid expiration, got %v", err)
	}
}
