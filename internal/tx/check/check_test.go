package check

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// ptrUint32 returns a pointer to a uint32 value
func ptrUint32(v uint32) *uint32 {
	return &v
}

// ptrAmount returns a pointer to an Amount value
func ptrAmount(a tx.Amount) *tx.Amount {
	return &a
}

// TestCheckCreateValidation tests CheckCreate transaction validation.
func TestCheckCreateValidation(t *testing.T) {
	tests := []struct {
		name        string
		check       *CheckCreate
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid XRP check",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rBob",
				SendMax:     tx.NewXRPAmount(10000000000),
			},
			expectError: false,
		},
		{
			name: "valid IOU check",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rBob",
				SendMax:     tx.NewIssuedAmountFromFloat64(50.0, "USD", "rGateway"),
			},
			expectError: false,
		},
		{
			name: "valid check with optional fields",
			check: &CheckCreate{
				BaseTx:         *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination:    "rBob",
				SendMax:        tx.NewXRPAmount(10000000000),
				DestinationTag: ptrUint32(42),
				Expiration:     ptrUint32(700000000),
				InvoiceID:      "0000000000000000000000000000000000000000000000000000000000000004",
			},
			expectError: false,
		},
		{
			name: "missing destination - temDST_NEEDED equivalent",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "",
				SendMax:     tx.NewXRPAmount(10000000000),
			},
			expectError: true,
			errorMsg:    "temDST_NEEDED: Destination is required",
		},
		{
			name: "missing SendMax - temBAD_AMOUNT equivalent",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rBob",
				SendMax:     tx.Amount{},
			},
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: SendMax must be positive",
		},
		{
			name: "check to self - temREDUNDANT equivalent",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rAlice",
				SendMax:     tx.NewXRPAmount(10000000000),
			},
			expectError: true,
			errorMsg:    "temREDUNDANT: cannot create check to self",
		},
		{
			name: "missing account",
			check: &CheckCreate{
				BaseTx:      tx.BaseTx{Common: tx.Common{TransactionType: "CheckCreate"}},
				Destination: "rBob",
				SendMax:     tx.NewXRPAmount(10000000000),
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
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

// TestCheckCashValidation tests CheckCash transaction validation.
func TestCheckCashValidation(t *testing.T) {
	tests := []struct {
		name        string
		check       *CheckCash
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid cash with exact Amount (XRP)",
			check: &CheckCash{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				Amount:  ptrAmount(tx.NewXRPAmount(10000000000)),
			},
			expectError: false,
		},
		{
			name: "valid cash with DeliverMin (XRP)",
			check: &CheckCash{
				BaseTx:     *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID:    "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				DeliverMin: ptrAmount(tx.NewXRPAmount(5000000000)),
			},
			expectError: false,
		},
		{
			name: "valid cash with exact Amount (IOU)",
			check: &CheckCash{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				Amount:  ptrAmount(tx.NewIssuedAmountFromFloat64(10.0, "USD", "rGateway")),
			},
			expectError: false,
		},
		{
			name: "valid cash with DeliverMin (IOU)",
			check: &CheckCash{
				BaseTx:     *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID:    "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				DeliverMin: ptrAmount(tx.NewIssuedAmountFromFloat64(5.0, "USD", "rGateway")),
			},
			expectError: false,
		},
		{
			name: "missing CheckID",
			check: &CheckCash{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID: "",
				Amount:  ptrAmount(tx.NewXRPAmount(10000000000)),
			},
			expectError: true,
			errorMsg:    "temMALFORMED: CheckID is required",
		},
		{
			name: "missing both Amount and DeliverMin - temMALFORMED equivalent",
			check: &CheckCash{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
			},
			expectError: true,
			errorMsg:    "temMALFORMED: must specify exactly one of Amount or DeliverMin",
		},
		{
			name: "both Amount and DeliverMin specified - temMALFORMED equivalent",
			check: &CheckCash{
				BaseTx:     *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID:    "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				Amount:     ptrAmount(tx.NewXRPAmount(10000000000)),
				DeliverMin: ptrAmount(tx.NewXRPAmount(5000000000)),
			},
			expectError: true,
			errorMsg:    "temMALFORMED: must specify exactly one of Amount or DeliverMin",
		},
		{
			name: "missing account",
			check: &CheckCash{
				BaseTx:  tx.BaseTx{Common: tx.Common{TransactionType: "CheckCash"}},
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				Amount:  ptrAmount(tx.NewXRPAmount(10000000000)),
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
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

// TestCheckCancelValidation tests CheckCancel transaction validation.
func TestCheckCancelValidation(t *testing.T) {
	tests := []struct {
		name        string
		check       *CheckCancel
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid cancel",
			check: &CheckCancel{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCancel, "rBob"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
			},
			expectError: false,
		},
		{
			name: "cancel by check creator",
			check: &CheckCancel{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCancel, "rAlice"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
			},
			expectError: false,
		},
		{
			name: "missing CheckID",
			check: &CheckCancel{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCancel, "rBob"),
				CheckID: "",
			},
			expectError: true,
			errorMsg:    "CheckID is required",
		},
		{
			name: "missing account",
			check: &CheckCancel{
				BaseTx:  tx.BaseTx{Common: tx.Common{TransactionType: "CheckCancel"}},
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.check.Validate()
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

// TestCheckCreateFlatten tests the Flatten method for CheckCreate.
func TestCheckCreateFlatten(t *testing.T) {
	tests := []struct {
		name     string
		check    *CheckCreate
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "basic XRP check",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rBob",
				SendMax:     tx.NewXRPAmount(10000000000),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Account"] != "rAlice" {
					t.Errorf("expected Account=rAlice, got %v", m["Account"])
				}
				if m["Destination"] != "rBob" {
					t.Errorf("expected Destination=rBob, got %v", m["Destination"])
				}
				if m["SendMax"] != "10000000000" {
					t.Errorf("expected SendMax=10000000000, got %v", m["SendMax"])
				}
			},
		},
		{
			name: "IOU check",
			check: &CheckCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination: "rBob",
				SendMax:     tx.NewIssuedAmountFromFloat64(50.0, "USD", "rGateway"),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				sendMax, ok := m["SendMax"].(map[string]any)
				if !ok {
					t.Fatalf("SendMax should be a map, got %T", m["SendMax"])
				}
				if sendMax["value"] != "50" {
					t.Errorf("expected value=50, got %v", sendMax["value"])
				}
				if sendMax["currency"] != "USD" {
					t.Errorf("expected currency=USD, got %v", sendMax["currency"])
				}
				if sendMax["issuer"] != "rGateway" {
					t.Errorf("expected issuer=rGateway, got %v", sendMax["issuer"])
				}
			},
		},
		{
			name: "check with all optional fields",
			check: &CheckCreate{
				BaseTx:         *tx.NewBaseTx(tx.TypeCheckCreate, "rAlice"),
				Destination:    "rBob",
				SendMax:        tx.NewXRPAmount(10000000000),
				DestinationTag: ptrUint32(42),
				Expiration:     ptrUint32(700000000),
				InvoiceID:      "DEADBEEF",
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["DestinationTag"] != uint32(42) {
					t.Errorf("expected DestinationTag=42, got %v", m["DestinationTag"])
				}
				if m["Expiration"] != uint32(700000000) {
					t.Errorf("expected Expiration=700000000, got %v", m["Expiration"])
				}
				if m["InvoiceID"] != "DEADBEEF" {
					t.Errorf("expected InvoiceID=DEADBEEF, got %v", m["InvoiceID"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.check.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestCheckCashFlatten tests the Flatten method for CheckCash.
func TestCheckCashFlatten(t *testing.T) {
	tests := []struct {
		name     string
		check    *CheckCash
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "cash with Amount",
			check: &CheckCash{
				BaseTx:  *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				Amount:  ptrAmount(tx.NewXRPAmount(10000000000)),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["CheckID"] != "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0" {
					t.Errorf("expected CheckID, got %v", m["CheckID"])
				}
				if m["Amount"] != "10000000000" {
					t.Errorf("expected Amount=10000000000, got %v", m["Amount"])
				}
				if _, ok := m["DeliverMin"]; ok {
					t.Error("DeliverMin should not be present")
				}
			},
		},
		{
			name: "cash with DeliverMin",
			check: &CheckCash{
				BaseTx:     *tx.NewBaseTx(tx.TypeCheckCash, "rBob"),
				CheckID:    "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
				DeliverMin: ptrAmount(tx.NewXRPAmount(5000000000)),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["DeliverMin"] != "5000000000" {
					t.Errorf("expected DeliverMin=5000000000, got %v", m["DeliverMin"])
				}
				if _, ok := m["Amount"]; ok {
					t.Error("Amount should not be present")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.check.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestCheckCancelFlatten tests the Flatten method for CheckCancel.
func TestCheckCancelFlatten(t *testing.T) {
	check := &CheckCancel{
		BaseTx:  *tx.NewBaseTx(tx.TypeCheckCancel, "rBob"),
		CheckID: "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0",
	}

	m, err := check.Flatten()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m["CheckID"] != "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0" {
		t.Errorf("expected CheckID, got %v", m["CheckID"])
	}
}

// TestCheckTransactionTypes tests that transaction types are correctly returned.
func TestCheckTransactionTypes(t *testing.T) {
	t.Run("CheckCreate type", func(t *testing.T) {
		c := NewCheckCreate("rAlice", "rBob", tx.NewXRPAmount(1000000))
		if c.TxType() != tx.TypeCheckCreate {
			t.Errorf("expected TypeCheckCreate, got %v", c.TxType())
		}
	})

	t.Run("CheckCash type", func(t *testing.T) {
		c := NewCheckCash("rBob", "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0")
		if c.TxType() != tx.TypeCheckCash {
			t.Errorf("expected TypeCheckCash, got %v", c.TxType())
		}
	})

	t.Run("CheckCancel type", func(t *testing.T) {
		c := NewCheckCancel("rBob", "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0")
		if c.TxType() != tx.TypeCheckCancel {
			t.Errorf("expected TypeCheckCancel, got %v", c.TxType())
		}
	})
}

// TestNewCheckConstructors tests the constructor functions.
func TestNewCheckConstructors(t *testing.T) {
	t.Run("NewCheckCreate", func(t *testing.T) {
		c := NewCheckCreate("rAlice", "rBob", tx.NewXRPAmount(1000000))
		if c.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", c.Account)
		}
		if c.Destination != "rBob" {
			t.Errorf("expected Destination=rBob, got %v", c.Destination)
		}
		if c.SendMax.Value() != "1000000" {
			t.Errorf("expected SendMax=1000000, got %v", c.SendMax.Value())
		}
	})

	t.Run("NewCheckCash", func(t *testing.T) {
		checkID := "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0"
		c := NewCheckCash("rBob", checkID)
		if c.Account != "rBob" {
			t.Errorf("expected Account=rBob, got %v", c.Account)
		}
		if c.CheckID != checkID {
			t.Errorf("expected CheckID=%s, got %v", checkID, c.CheckID)
		}
	})

	t.Run("NewCheckCancel", func(t *testing.T) {
		checkID := "49647F0D748DC3FE26BDACBC57F251AADEFFF391403EC9BF87C97F67E9977FB0"
		c := NewCheckCancel("rAlice", checkID)
		if c.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", c.Account)
		}
		if c.CheckID != checkID {
			t.Errorf("expected CheckID=%s, got %v", checkID, c.CheckID)
		}
	})
}

// TestCheckCashHelperMethods tests the SetExactAmount and SetDeliverMin helper methods.
func TestCheckCashHelperMethods(t *testing.T) {
	t.Run("SetExactAmount", func(t *testing.T) {
		c := NewCheckCash("rBob", "DEADBEEF")
		amount := tx.NewXRPAmount(1000000)
		c.SetExactAmount(amount)

		if c.Amount == nil {
			t.Error("Amount should not be nil")
		}
		if c.Amount.Value() != "1000000" {
			t.Errorf("expected Amount=1000000, got %v", c.Amount.Value())
		}
		if c.DeliverMin != nil {
			t.Error("DeliverMin should be nil")
		}
	})

	t.Run("SetDeliverMin", func(t *testing.T) {
		c := NewCheckCash("rBob", "DEADBEEF")
		amount := tx.NewXRPAmount(500000)
		c.SetDeliverMin(amount)

		if c.DeliverMin == nil {
			t.Error("DeliverMin should not be nil")
		}
		if c.DeliverMin.Value() != "500000" {
			t.Errorf("expected DeliverMin=500000, got %v", c.DeliverMin.Value())
		}
		if c.Amount != nil {
			t.Error("Amount should be nil")
		}
	})

	t.Run("SetExactAmount clears DeliverMin", func(t *testing.T) {
		c := NewCheckCash("rBob", "DEADBEEF")
		c.SetDeliverMin(tx.NewXRPAmount(500000))
		c.SetExactAmount(tx.NewXRPAmount(1000000))

		if c.DeliverMin != nil {
			t.Error("DeliverMin should be nil after SetExactAmount")
		}
		if c.Amount == nil {
			t.Error("Amount should not be nil")
		}
	})

	t.Run("SetDeliverMin clears Amount", func(t *testing.T) {
		c := NewCheckCash("rBob", "DEADBEEF")
		c.SetExactAmount(tx.NewXRPAmount(1000000))
		c.SetDeliverMin(tx.NewXRPAmount(500000))

		if c.Amount != nil {
			t.Error("Amount should be nil after SetDeliverMin")
		}
		if c.DeliverMin == nil {
			t.Error("DeliverMin should not be nil")
		}
	})
}
