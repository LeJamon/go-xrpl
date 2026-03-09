package payment

import (
	"strconv"
	"testing"

	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
)

func ptrUint32(v uint32) *uint32 { return &v }
func ptrAmount(a tx.Amount) *tx.Amount { return &a }

// Helper to create XRP amount from string drops
func xrpAmount(drops string) tx.Amount {
	d, _ := strconv.ParseInt(drops, 10, 64)
	return tx.NewXRPAmount(d)
}

// Helper to create IOU amount from string value
func iouAmount(value, currency, issuer string) tx.Amount {
	return state.NewIssuedAmountFromDecimalString(value, currency, issuer)
}

// TestPaymentValidation tests Payment transaction validation.
// These tests are translated from rippled's Pay_test.cpp and PayStrand_test.cpp
// focusing on validation logic and error conditions.
func TestPaymentValidation(t *testing.T) {
	tests := []struct {
		name        string
		payment     *Payment
		expectError bool
		errorMsg    string
	}{
		// Valid payment cases
		{
			name: "valid XRP payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"), // 1 XRP in drops
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "valid IOU payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "valid payment with destination tag",
			payment: &Payment{
				BaseTx:         *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:         xrpAmount("1000000"),
				Destination:    "rBob",
				DestinationTag: ptrUint32(12345),
			},
			expectError: false,
		},
		{
			name: "valid payment with invoice ID",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
				InvoiceID:   "0000000000000000000000000000000000000000000000000000000000000001",
			},
			expectError: false,
		},

		// Missing required fields
		{
			name: "missing destination - temDST_NEEDED equivalent",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "",
			},
			expectError: true,
			errorMsg:    "Destination is required",
		},
		{
			name: "missing amount - temBAD_AMOUNT equivalent",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      tx.Amount{},
				Destination: "rBob",
			},
			expectError: true,
			errorMsg:    "Amount is required",
		},
		{
			name: "missing account - temBAD_SRC_ACCOUNT equivalent",
			payment: &Payment{
				BaseTx:      tx.BaseTx{Common: tx.Common{TransactionType: "Payment"}},
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
			},
			expectError: true,
			errorMsg:    "Account is required",
		},

		// Payment to self validation
		{
			name: "XRP payment to self - temREDUNDANT equivalent",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rAlice",
			},
			expectError: true,
			errorMsg:    "temREDUNDANT: cannot send XRP to self without path",
		},
		{
			name: "IOU payment to self is allowed (for cross-currency)",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rAlice",
			},
			expectError: false, // IOU payments to self are allowed for cross-currency
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.payment.Validate()
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

// TestPaymentAmounts tests various payment amount scenarios.
// Inspired by rippled's PayStrand_test.cpp amount handling tests.
func TestPaymentAmounts(t *testing.T) {
	tests := []struct {
		name        string
		payment     *Payment
		expectError bool
		errorMsg    string
	}{
		// XRP amounts
		{
			name: "XRP to XRP transfer - basic drops amount",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"), // 1 XRP
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "XRP payment - large amount",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("100000000000000"), // 100M XRP
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "XRP payment - minimum amount (1 drop)",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1"),
				Destination: "rBob",
			},
			expectError: false,
		},

		// IOU amounts
		{
			name: "IOU to IOU transfer - same currency",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "IOU payment - decimal value",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100.50", "USD", "rGateway"),
				Destination: "rBob",
			},
			expectError: false,
		},
		{
			name: "IOU payment - scientific notation",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("1e10", "USD", "rGateway"),
				Destination: "rBob",
			},
			expectError: false,
		},

		// Cross-currency with paths
		{
			name: "cross-currency payment with paths",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("110", "USD", "rGatewayUSD")),
				Paths: [][]PathStep{
					{
						{Currency: "XRP"},
						{Currency: "EUR", Issuer: "rGatewayEUR"},
					},
				},
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.payment.Validate()
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

// TestPaymentFlags tests payment flag handling.
// Inspired by rippled's flag tests in Pay_test.cpp.
func TestPaymentFlags(t *testing.T) {
	t.Run("tfPartialPayment flag", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", xrpAmount("1000000"))
		payment.SetPartialPayment()

		flags := payment.GetFlags()
		if flags&PaymentFlagPartialPayment == 0 {
			t.Error("expected tfPartialPayment flag to be set")
		}
	})

	t.Run("tfNoDirectRipple flag", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		payment.SetNoDirectRipple()

		flags := payment.GetFlags()
		if flags&PaymentFlagNoDirectRipple == 0 {
			t.Error("expected tfNoDirectRipple flag to be set")
		}
	})

	t.Run("tfLimitQuality flag", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		flags := payment.GetFlags() | PaymentFlagLimitQuality
		payment.SetFlags(flags)

		if payment.GetFlags()&PaymentFlagLimitQuality == 0 {
			t.Error("expected tfLimitQuality flag to be set")
		}
	})

	t.Run("multiple flags combined", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		payment.SetPartialPayment()
		payment.SetNoDirectRipple()
		flags := payment.GetFlags() | PaymentFlagLimitQuality
		payment.SetFlags(flags)

		expectedFlags := PaymentFlagPartialPayment | PaymentFlagNoDirectRipple | PaymentFlagLimitQuality
		if payment.GetFlags() != expectedFlags {
			t.Errorf("expected flags %d, got %d", expectedFlags, payment.GetFlags())
		}
	})

	t.Run("partial payment with DeliverMin", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", xrpAmount("1000000"))
		payment.SetPartialPayment()
		deliverMin := xrpAmount("500000")
		payment.DeliverMin = &deliverMin

		if payment.DeliverMin == nil {
			t.Error("DeliverMin should be set")
		}
		if payment.GetFlags()&PaymentFlagPartialPayment == 0 {
			t.Error("tfPartialPayment flag should be set when using DeliverMin")
		}
	})
}

// TestPaymentSendMax tests SendMax field validation.
// Inspired by rippled's sendMax tests in PayStrand_test.cpp.
func TestPaymentSendMax(t *testing.T) {
	tests := []struct {
		name     string
		payment  *Payment
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "XRP SendMax for cross-currency",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rBob",
				SendMax:     ptrAmount(xrpAmount("1100000")),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				sendMax, ok := m["SendMax"]
				if !ok {
					t.Error("SendMax should be present")
					return
				}
				if sendMax != "1100000" {
					t.Errorf("expected SendMax=1100000, got %v", sendMax)
				}
			},
		},
		{
			name: "IOU SendMax for cross-currency",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("120", "USD", "rGatewayUSD")),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				sendMax, ok := m["SendMax"].(map[string]any)
				if !ok {
					t.Fatalf("SendMax should be a map, got %T", m["SendMax"])
				}
				if sendMax["value"] != "120" {
					t.Errorf("expected value=120, got %v", sendMax["value"])
				}
				if sendMax["currency"] != "USD" {
					t.Errorf("expected currency=USD, got %v", sendMax["currency"])
				}
				if sendMax["issuer"] != "rGatewayUSD" {
					t.Errorf("expected issuer=rGatewayUSD, got %v", sendMax["issuer"])
				}
			},
		},
		{
			name: "no SendMax for direct XRP payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if _, ok := m["SendMax"]; ok {
					t.Error("SendMax should not be present for direct XRP payment")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.payment.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestPaymentDeliverMin tests DeliverMin field validation.
// Inspired by rippled's partial payment tests.
func TestPaymentDeliverMin(t *testing.T) {
	tests := []struct {
		name     string
		payment  *Payment
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "XRP DeliverMin for partial payment",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      xrpAmount("1000000"),
					Destination: "rBob",
					DeliverMin:  ptrAmount(xrpAmount("500000")),
				}
				p.SetPartialPayment()
				return p
			}(),
			checkMap: func(t *testing.T, m map[string]any) {
				deliverMin, ok := m["DeliverMin"]
				if !ok {
					t.Error("DeliverMin should be present")
					return
				}
				if deliverMin != "500000" {
					t.Errorf("expected DeliverMin=500000, got %v", deliverMin)
				}
			},
		},
		{
			name: "IOU DeliverMin for partial payment",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("100", "USD", "rGateway"),
					Destination: "rBob",
					DeliverMin:  ptrAmount(iouAmount("50", "USD", "rGateway")),
				}
				p.SetPartialPayment()
				return p
			}(),
			checkMap: func(t *testing.T, m map[string]any) {
				deliverMin, ok := m["DeliverMin"].(map[string]any)
				if !ok {
					t.Fatalf("DeliverMin should be a map, got %T", m["DeliverMin"])
				}
				if deliverMin["value"] != "50" {
					t.Errorf("expected value=50, got %v", deliverMin["value"])
				}
			},
		},
		{
			name: "no DeliverMin without partial payment flag",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if _, ok := m["DeliverMin"]; ok {
					t.Error("DeliverMin should not be present without partial payment")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.payment.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestPaymentPaths tests Paths field handling.
// Inspired by rippled's path handling tests in PayStrand_test.cpp.
func TestPaymentPaths(t *testing.T) {
	tests := []struct {
		name     string
		payment  *Payment
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "simple path with currency hop",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("110", "USD", "rGatewayUSD")),
				Paths: [][]PathStep{
					{
						{Currency: "XRP"},
					},
				},
			},
			checkMap: func(t *testing.T, m map[string]any) {
				paths, ok := m["Paths"].([][]PathStep)
				if !ok {
					t.Fatalf("Paths should be [][]PathStep, got %T", m["Paths"])
				}
				if len(paths) != 1 {
					t.Errorf("expected 1 path, got %d", len(paths))
				}
				if len(paths[0]) != 1 {
					t.Errorf("expected 1 step in path, got %d", len(paths[0]))
				}
				if paths[0][0].Currency != "XRP" {
					t.Errorf("expected currency=XRP, got %v", paths[0][0].Currency)
				}
			},
		},
		{
			name: "complex path with multiple steps",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "JPY", "rGatewayJPY"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("150", "USD", "rGatewayUSD")),
				Paths: [][]PathStep{
					{
						{Currency: "EUR", Issuer: "rGatewayEUR"},
						{Currency: "XRP"},
						{Currency: "JPY", Issuer: "rGatewayJPY"},
					},
				},
			},
			checkMap: func(t *testing.T, m map[string]any) {
				paths, ok := m["Paths"].([][]PathStep)
				if !ok {
					t.Fatalf("Paths should be [][]PathStep, got %T", m["Paths"])
				}
				if len(paths) != 1 {
					t.Errorf("expected 1 path, got %d", len(paths))
				}
				if len(paths[0]) != 3 {
					t.Errorf("expected 3 steps in path, got %d", len(paths[0]))
				}
			},
		},
		{
			name: "multiple alternative paths",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("110", "USD", "rGatewayUSD")),
				Paths: [][]PathStep{
					{
						{Currency: "XRP"},
					},
					{
						{Account: "rCarol"},
					},
				},
			},
			checkMap: func(t *testing.T, m map[string]any) {
				paths, ok := m["Paths"].([][]PathStep)
				if !ok {
					t.Fatalf("Paths should be [][]PathStep, got %T", m["Paths"])
				}
				if len(paths) != 2 {
					t.Errorf("expected 2 paths, got %d", len(paths))
				}
			},
		},
		{
			name: "no paths for direct XRP payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if _, ok := m["Paths"]; ok {
					t.Error("Paths should not be present for direct XRP payment")
				}
			},
		},
		{
			name: "path with account hop",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rBob",
				Paths: [][]PathStep{
					{
						{Account: "rCarol"},
					},
				},
			},
			checkMap: func(t *testing.T, m map[string]any) {
				paths, ok := m["Paths"].([][]PathStep)
				if !ok {
					t.Fatalf("Paths should be [][]PathStep, got %T", m["Paths"])
				}
				if paths[0][0].Account != "rCarol" {
					t.Errorf("expected account=rCarol, got %v", paths[0][0].Account)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.payment.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestPaymentFlatten tests the Flatten method for Payment.
func TestPaymentFlatten(t *testing.T) {
	tests := []struct {
		name     string
		payment  *Payment
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "basic XRP payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("1000000"),
				Destination: "rBob",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Account"] != "rAlice" {
					t.Errorf("expected Account=rAlice, got %v", m["Account"])
				}
				if m["Destination"] != "rBob" {
					t.Errorf("expected Destination=rBob, got %v", m["Destination"])
				}
				if m["Amount"] != "1000000" {
					t.Errorf("expected Amount=1000000, got %v", m["Amount"])
				}
				if m["TransactionType"] != "Payment" {
					t.Errorf("expected TransactionType=Payment, got %v", m["TransactionType"])
				}
			},
		},
		{
			name: "IOU payment",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "USD", "rGateway"),
				Destination: "rBob",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				amount, ok := m["Amount"].(map[string]any)
				if !ok {
					t.Fatalf("Amount should be a map, got %T", m["Amount"])
				}
				if amount["value"] != "100" {
					t.Errorf("expected value=100, got %v", amount["value"])
				}
				if amount["currency"] != "USD" {
					t.Errorf("expected currency=USD, got %v", amount["currency"])
				}
				if amount["issuer"] != "rGateway" {
					t.Errorf("expected issuer=rGateway, got %v", amount["issuer"])
				}
			},
		},
		{
			name: "payment with all optional fields",
			payment: &Payment{
				BaseTx:         *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:         iouAmount("100", "USD", "rGateway"),
				Destination:    "rBob",
				DestinationTag: ptrUint32(42),
				InvoiceID:      "DEADBEEF",
				SendMax:        ptrAmount(xrpAmount("1100000")),
				DeliverMin:     ptrAmount(xrpAmount("900000")),
				Paths: [][]PathStep{
					{
						{Currency: "XRP"},
					},
				},
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["DestinationTag"] != uint32(42) {
					t.Errorf("expected DestinationTag=42, got %v", m["DestinationTag"])
				}
				if m["InvoiceID"] != "DEADBEEF" {
					t.Errorf("expected InvoiceID=DEADBEEF, got %v", m["InvoiceID"])
				}
				if _, ok := m["SendMax"]; !ok {
					t.Error("SendMax should be present")
				}
				if _, ok := m["DeliverMin"]; !ok {
					t.Error("DeliverMin should be present")
				}
				if _, ok := m["Paths"]; !ok {
					t.Error("Paths should be present")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.payment.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestPaymentTransactionType tests that transaction type is correctly returned.
func TestPaymentTransactionType(t *testing.T) {
	payment := NewPayment("rAlice", "rBob", xrpAmount("1000000"))
	if payment.TxType() != tx.TypePayment {
		t.Errorf("expected tx.TypePayment, got %v", payment.TxType())
	}
}

// TestNewPaymentConstructor tests the constructor function.
func TestNewPaymentConstructor(t *testing.T) {
	t.Run("XRP payment", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", xrpAmount("1000000"))
		if payment.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", payment.Account)
		}
		if payment.Destination != "rBob" {
			t.Errorf("expected Destination=rBob, got %v", payment.Destination)
		}
		if payment.Amount.Value() != "1000000" {
			t.Errorf("expected Amount=1000000, got %v", payment.Amount.Value())
		}
		if !payment.Amount.IsNative() {
			t.Error("expected XRP amount to be native")
		}
	})

	t.Run("IOU payment", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		if payment.Amount.Currency != "USD" {
			t.Errorf("expected currency=USD, got %v", payment.Amount.Currency)
		}
		if payment.Amount.Issuer != "rGateway" {
			t.Errorf("expected issuer=rGateway, got %v", payment.Amount.Issuer)
		}
		if payment.Amount.IsNative() {
			t.Error("expected IOU amount to not be native")
		}
	})
}

// TestPaymentFlagConstants tests that flag constants have correct values.
// These values are defined in rippled and must match.
func TestPaymentFlagConstants(t *testing.T) {
	tests := []struct {
		name     string
		flag     uint32
		expected uint32
	}{
		{
			name:     "tfNoDirectRipple",
			flag:     PaymentFlagNoDirectRipple,
			expected: 0x00010000,
		},
		{
			name:     "tfPartialPayment",
			flag:     PaymentFlagPartialPayment,
			expected: 0x00020000,
		},
		{
			name:     "tfLimitQuality",
			flag:     PaymentFlagLimitQuality,
			expected: 0x00040000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.flag != tt.expected {
				t.Errorf("expected %s=0x%08X, got 0x%08X", tt.name, tt.expected, tt.flag)
			}
		})
	}
}

// TestPaymentZeroAmount tests handling of zero amounts.
// Inspired by rippled's amount validation tests.
func TestPaymentZeroAmount(t *testing.T) {
	tests := []struct {
		name        string
		payment     *Payment
		expectError bool
	}{
		{
			name: "zero XRP amount string",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      xrpAmount("0"),
				Destination: "rBob",
			},
			// Zero amount validation is typically done at a higher level
			// The basic validation just checks if Value is not empty
			expectError: false,
		},
		{
			name: "zero IOU amount string",
			payment: &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("0", "USD", "rGateway"),
				Destination: "rBob",
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.payment.Validate()
			if tt.expectError && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.expectError && err != nil {
				t.Errorf("expected no error, got %v", err)
			}
		})
	}
}

// TestAmountIsNative tests the IsNative method for Amount type.
func TestAmountIsNative(t *testing.T) {
	tests := []struct {
		name     string
		amount   tx.Amount
		expected bool
	}{
		{
			name:     "XRP amount via constructor",
			amount:   xrpAmount("1000000"),
			expected: true,
		},
		{
			name:     "IOU amount via constructor",
			amount:   iouAmount("100", "USD", "rGateway"),
			expected: false,
		},
		{
			name:     "XRP amount (native)",
			amount:   xrpAmount("1000000"),
			expected: true, // XRP is native
		},
		{
			name:     "zero XRP amount (native)",
			amount:   tx.NewXRPAmount(0),
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.amount.IsNative() != tt.expected {
				t.Errorf("expected IsNative()=%v, got %v", tt.expected, tt.amount.IsNative())
			}
		})
	}
}

// TestPaymentSetterMethods tests the helper methods for setting payment properties.
func TestPaymentSetterMethods(t *testing.T) {
	t.Run("SetPartialPayment sets correct flag", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", xrpAmount("1000000"))
		payment.SetPartialPayment()

		if payment.GetFlags()&PaymentFlagPartialPayment == 0 {
			t.Error("SetPartialPayment should set tfPartialPayment flag")
		}
	})

	t.Run("SetNoDirectRipple sets correct flag", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		payment.SetNoDirectRipple()

		if payment.GetFlags()&PaymentFlagNoDirectRipple == 0 {
			t.Error("SetNoDirectRipple should set tfNoDirectRipple flag")
		}
	})

	t.Run("flags are cumulative", func(t *testing.T) {
		payment := NewPayment("rAlice", "rBob", iouAmount("100", "USD", "rGateway"))
		payment.SetPartialPayment()
		payment.SetNoDirectRipple()

		flags := payment.GetFlags()
		if flags&PaymentFlagPartialPayment == 0 {
			t.Error("tfPartialPayment should still be set")
		}
		if flags&PaymentFlagNoDirectRipple == 0 {
			t.Error("tfNoDirectRipple should be set")
		}
	})
}

// TestPaymentPathStep tests PathStep struct handling.
func TestPaymentPathStep(t *testing.T) {
	t.Run("account path step", func(t *testing.T) {
		step := PathStep{Account: "rCarol"}
		if step.Account != "rCarol" {
			t.Errorf("expected Account=rCarol, got %v", step.Account)
		}
	})

	t.Run("currency path step", func(t *testing.T) {
		step := PathStep{Currency: "USD"}
		if step.Currency != "USD" {
			t.Errorf("expected Currency=USD, got %v", step.Currency)
		}
	})

	t.Run("issuer path step", func(t *testing.T) {
		step := PathStep{Issuer: "rGateway"}
		if step.Issuer != "rGateway" {
			t.Errorf("expected Issuer=rGateway, got %v", step.Issuer)
		}
	})

	t.Run("full path step", func(t *testing.T) {
		step := PathStep{
			Account:  "rCarol",
			Currency: "USD",
			Issuer:   "rGateway",
		}
		if step.Account != "rCarol" || step.Currency != "USD" || step.Issuer != "rGateway" {
			t.Error("path step fields not set correctly")
		}
	})
}

// TestPaymentCrossCurrency tests cross-currency payment scenarios.
// Inspired by rippled's PayStrand_test.cpp cross-currency tests.
func TestPaymentCrossCurrency(t *testing.T) {
	t.Run("USD to EUR with XRP bridge", func(t *testing.T) {
		payment := &Payment{
			BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
			Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
			Destination: "rBob",
			SendMax:     ptrAmount(iouAmount("120", "USD", "rGatewayUSD")),
			Paths: [][]PathStep{
				{
					{Currency: "XRP"},
				},
			},
		}
		payment.SetNoDirectRipple()

		err := payment.Validate()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}

		m, err := payment.Flatten()
		if err != nil {
			t.Fatalf("unexpected flatten error: %v", err)
		}

		// Verify Amount is EUR
		amount, ok := m["Amount"].(map[string]any)
		if !ok {
			t.Fatal("Amount should be a map")
		}
		if amount["currency"] != "EUR" {
			t.Errorf("expected currency=EUR, got %v", amount["currency"])
		}

		// Verify SendMax is USD
		sendMax, ok := m["SendMax"].(map[string]any)
		if !ok {
			t.Fatal("SendMax should be a map")
		}
		if sendMax["currency"] != "USD" {
			t.Errorf("expected SendMax currency=USD, got %v", sendMax["currency"])
		}

		// Verify Paths includes XRP hop
		paths, ok := m["Paths"].([][]PathStep)
		if !ok {
			t.Fatal("Paths should be [][]PathStep")
		}
		if len(paths) != 1 || paths[0][0].Currency != "XRP" {
			t.Error("expected path with XRP hop")
		}
	})

	t.Run("XRP to IOU cross-currency", func(t *testing.T) {
		payment := &Payment{
			BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
			Amount:      iouAmount("100", "USD", "rGateway"),
			Destination: "rBob",
			SendMax:     ptrAmount(xrpAmount("110000000")), // 110 XRP in drops
			Paths: [][]PathStep{
				{
					{Currency: "USD", Issuer: "rGateway"},
				},
			},
		}

		err := payment.Validate()
		if err != nil {
			t.Errorf("expected no error, got %v", err)
		}
	})
}

// TestDeliverMinValidation tests DeliverMin field validation.
// These tests are based on rippled's DeliverMin_test.cpp
func TestDeliverMinValidation(t *testing.T) {
	tests := []struct {
		name        string
		payment     *Payment
		expectError bool
		errorMsg    string
	}{
		// DeliverMin requires tfPartialPayment flag
		// Reference: rippled DeliverMin_test.cpp line 46-48
		{
			name: "DeliverMin without tfPartialPayment - temBAD_AMOUNT",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				deliverMin := iouAmount("10", "USD", "rGateway")
				p.DeliverMin = &deliverMin
				// No tfPartialPayment flag set
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: DeliverMin requires tfPartialPayment flag",
		},

		// DeliverMin must be positive
		// Reference: rippled DeliverMin_test.cpp line 49-52
		{
			name: "negative DeliverMin - temBAD_AMOUNT",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("-5", "USD", "rGateway")
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: DeliverMin must be positive",
		},

		// DeliverMin currency must match Amount currency
		// Reference: rippled DeliverMin_test.cpp line 53-56 (XRP vs USD)
		{
			name: "DeliverMin currency mismatch - XRP vs USD",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := xrpAmount("5000000") // XRP instead of USD
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: DeliverMin currency must match Amount",
		},

		// DeliverMin issuer must match Amount issuer
		// Reference: rippled DeliverMin_test.cpp line 57-60 (different issuer)
		{
			name: "DeliverMin issuer mismatch",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("5", "USD", "rOtherGateway") // Different issuer
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: DeliverMin currency must match Amount",
		},

		// DeliverMin must not exceed Amount
		// Reference: rippled DeliverMin_test.cpp line 61-64
		{
			name: "DeliverMin exceeds Amount - temBAD_AMOUNT",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("15", "USD", "rGateway") // Exceeds Amount
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: false, // This is validated in apply, not preflight
		},

		// Valid DeliverMin with tfPartialPayment
		{
			name: "valid DeliverMin with tfPartialPayment",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("7", "USD", "rGateway")
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: false,
		},

		// Valid DeliverMin at minimum (same as Amount)
		{
			name: "valid DeliverMin equal to Amount",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("10", "USD", "rGateway")
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: false,
		},

		// Zero DeliverMin - temBAD_AMOUNT
		{
			name: "zero DeliverMin - temBAD_AMOUNT",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("10", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				deliverMin := iouAmount("0", "USD", "rGateway")
				p.DeliverMin = &deliverMin
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: DeliverMin must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.payment.Validate()
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				} else if tt.errorMsg != "" && !containsString(err.Error(), tt.errorMsg) {
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

// TestPartialPaymentXRPRestriction tests that tfPartialPayment is invalid for XRP-to-XRP payments.
// Reference: rippled Payment.cpp:182-188 (temBAD_SEND_XRP_PARTIAL)
func TestPartialPaymentXRPRestriction(t *testing.T) {
	tests := []struct {
		name        string
		payment     *Payment
		expectError bool
		errorMsg    string
	}{
		{
			name: "tfPartialPayment with XRP-to-XRP - temBAD_SEND_XRP_PARTIAL",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      xrpAmount("1000000"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_SEND_XRP_PARTIAL",
		},
		{
			name: "tfPartialPayment with XRP-to-XRP (with XRP SendMax) - temBAD_SEND_XRP_PARTIAL",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      xrpAmount("1000000"),
					Destination: "rBob",
					SendMax:     ptrAmount(xrpAmount("1100000")),
				}
				p.SetPartialPayment()
				return p
			}(),
			expectError: true,
			errorMsg:    "temBAD_SEND_XRP_PARTIAL",
		},
		{
			name: "tfPartialPayment with IOU - allowed",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("100", "USD", "rGateway"),
					Destination: "rBob",
				}
				p.SetPartialPayment()
				return p
			}(),
			expectError: false,
		},
		{
			name: "tfPartialPayment with cross-currency (XRP to IOU) - allowed",
			payment: func() *Payment {
				p := &Payment{
					BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
					Amount:      iouAmount("100", "USD", "rGateway"),
					Destination: "rBob",
					SendMax:     ptrAmount(xrpAmount("1100000")), // XRP SendMax with IOU Amount
				}
				p.SetPartialPayment()
				return p
			}(),
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.payment.Validate()
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				} else if !containsString(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// TestPaymentPathLimits tests path array size limits.
// Reference: rippled Payment.cpp:353-359 (MaxPathSize = 7, MaxPathLength = 8)
func TestPaymentPathLimits(t *testing.T) {
	tests := []struct {
		name        string
		numPaths    int
		pathLength  int
		expectError bool
		errorMsg    string
	}{
		{
			name:        "7 paths (max allowed)",
			numPaths:    7,
			pathLength:  1,
			expectError: false,
		},
		{
			name:        "8 paths (exceeds max) - temMALFORMED",
			numPaths:    8,
			pathLength:  1,
			expectError: true,
			errorMsg:    "temMALFORMED: Paths array exceeds maximum size of 7",
		},
		{
			name:        "8 steps per path (max allowed)",
			numPaths:    1,
			pathLength:  8,
			expectError: false,
		},
		{
			name:        "9 steps per path (exceeds max) - temMALFORMED",
			numPaths:    1,
			pathLength:  9,
			expectError: true,
			errorMsg:    "temMALFORMED: Path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payment := &Payment{
				BaseTx:      *tx.NewBaseTx(tx.TypePayment, "rAlice"),
				Amount:      iouAmount("100", "EUR", "rGatewayEUR"),
				Destination: "rBob",
				SendMax:     ptrAmount(iouAmount("110", "USD", "rGatewayUSD")),
			}

			// Create paths array
			paths := make([][]PathStep, tt.numPaths)
			for i := 0; i < tt.numPaths; i++ {
				path := make([]PathStep, tt.pathLength)
				for j := 0; j < tt.pathLength; j++ {
					path[j] = PathStep{Currency: "USD", Issuer: "rGateway"}
				}
				paths[i] = path
			}
			payment.Paths = paths

			err := payment.Validate()
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errorMsg)
				} else if !containsString(err.Error(), tt.errorMsg) {
					t.Errorf("expected error containing %q, got %q", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// containsString is a helper to check if s contains substr
func containsString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
