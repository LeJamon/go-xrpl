package escrow

import (
	"testing"

	"github.com/LeJamon/goXRPLd/internal/tx"
)

// TestEscrowCreateValidation tests EscrowCreate transaction validation.
// These tests are translated from rippled's Escrow_test.cpp focusing on
// validation logic and error conditions.
func TestEscrowCreateValidation(t *testing.T) {
	tests := []struct {
		name        string
		escrow      *EscrowCreate
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid escrow with finish time",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000), // 1000 XRP in drops
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000), // Some future time
			},
			expectError: false,
		},
		{
			name: "valid escrow with cancel time and condition",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				CancelAfter: ptrUint32(700000100),
				Condition:   ptrString("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"),
			},
			expectError: false,
		},
		{
			name: "valid escrow with both times",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
				CancelAfter: ptrUint32(700000100), // CancelAfter > FinishAfter
			},
			expectError: false,
		},
		{
			name: "missing destination - temDST_NEEDED equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: true,
			errorMsg:    "temDST_NEEDED: Destination is required",
		},
		{
			name: "missing amount - temBAD_AMOUNT equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.Amount{},
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: Amount must be positive",
		},
		{
			// With featureTokenEscrow, IOU amounts are valid in Validate().
			// The amendment check is deferred to Apply() where rules are available.
			name: "non-XRP amount - passes Validate (amendment checked in Apply)",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewIssuedAmountFromFloat64(100.0, "USD", "rGateway"),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: false,
		},
		{
			name: "negative amount - temBAD_AMOUNT equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(-1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: Amount must be positive",
		},
		{
			name: "zero amount - temBAD_AMOUNT equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(0),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: true,
			errorMsg:    "temBAD_AMOUNT: Amount must be positive",
		},
		{
			name: "missing both times - temBAD_EXPIRATION equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				// No CancelAfter or FinishAfter
			},
			expectError: true,
			errorMsg:    "temBAD_EXPIRATION: must specify CancelAfter or FinishAfter",
		},
		{
			// fix1571 check is amendment-gated and deferred to Apply().
			// Validate() only checks stateless invariants.
			name: "cancel only without condition - passes Validate (fix1571 checked in Apply)",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				CancelAfter: ptrUint32(700000100),
				// No FinishAfter and no Condition
			},
			expectError: false,
		},
		{
			name: "cancel time equals finish time - temBAD_EXPIRATION equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
				CancelAfter: ptrUint32(700000000), // Equal, should fail
			},
			expectError: true,
			errorMsg:    "temBAD_EXPIRATION: CancelAfter must be after FinishAfter",
		},
		{
			name: "cancel time before finish time - temBAD_EXPIRATION equivalent",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000100),
				CancelAfter: ptrUint32(700000000), // Before, should fail
			},
			expectError: true,
			errorMsg:    "temBAD_EXPIRATION: CancelAfter must be after FinishAfter",
		},
		{
			name: "valid escrow with only condition (fix1571 behavior)",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				CancelAfter: ptrUint32(700000100),
				Condition:   ptrString("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"),
			},
			expectError: false,
		},
		{
			name: "missing account - temBAD_SRC_ACCOUNT equivalent",
			escrow: &EscrowCreate{
				BaseTx:      tx.BaseTx{Common: tx.Common{TransactionType: "EscrowCreate"}},
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.escrow.Validate()
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

// TestEscrowFinishValidation tests EscrowFinish transaction validation.
// Tests translated from rippled's testLockup and testEscrowConditions.
func TestEscrowFinishValidation(t *testing.T) {
	tests := []struct {
		name        string
		escrow      *EscrowFinish
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid unconditional finish",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: false,
		},
		{
			name: "valid conditional finish with both condition and fulfillment",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
				Condition:     ptrString("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"),
				Fulfillment:   ptrString("A0028000"),
			},
			expectError: false,
		},
		{
			name: "missing owner",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "",
				OfferSequence: 12345,
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Owner is required",
		},
		{
			name: "condition without fulfillment - temMALFORMED equivalent",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
				Condition:     ptrString("A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100"),
				// Missing Fulfillment
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Condition and Fulfillment must be provided together",
		},
		{
			name: "fulfillment without condition - temMALFORMED equivalent",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
				Fulfillment:   ptrString("A0028000"),
				// Missing Condition
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Condition and Fulfillment must be provided together",
		},
		{
			name: "missing account",
			escrow: &EscrowFinish{
				BaseTx:        tx.BaseTx{Common: tx.Common{TransactionType: "EscrowFinish"}},
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
		{
			name: "self-finish allowed (unconditional)",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rAlice"),
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.escrow.Validate()
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

// TestEscrowCancelValidation tests EscrowCancel transaction validation.
func TestEscrowCancelValidation(t *testing.T) {
	tests := []struct {
		name        string
		escrow      *EscrowCancel
		expectError bool
		errorMsg    string
	}{
		{
			name: "valid cancel",
			escrow: &EscrowCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: false,
		},
		{
			name: "self cancel allowed",
			escrow: &EscrowCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, "rAlice"),
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: false,
		},
		{
			name: "missing owner",
			escrow: &EscrowCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, "rBob"),
				Owner:         "",
				OfferSequence: 12345,
			},
			expectError: true,
			errorMsg:    "temMALFORMED: Owner is required",
		},
		{
			name: "missing account",
			escrow: &EscrowCancel{
				BaseTx:        tx.BaseTx{Common: tx.Common{TransactionType: "EscrowCancel"}},
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			expectError: true,
			errorMsg:    "Account is required",
		},
		{
			name: "sequence zero is valid",
			escrow: &EscrowCancel{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 0,
			},
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.escrow.Validate()
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

// TestEscrowCreateFlatten tests the Flatten method for EscrowCreate.
func TestEscrowCreateFlatten(t *testing.T) {
	tests := []struct {
		name     string
		escrow   *EscrowCreate
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "basic escrow",
			escrow: &EscrowCreate{
				BaseTx:      *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:      tx.NewXRPAmount(1000000000),
				Destination: "rBob",
				FinishAfter: ptrUint32(700000000),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Account"] != "rAlice" {
					t.Errorf("expected Account=rAlice, got %v", m["Account"])
				}
				if m["Destination"] != "rBob" {
					t.Errorf("expected Destination=rBob, got %v", m["Destination"])
				}
				if m["Amount"] != "1000000000" {
					t.Errorf("expected Amount=1000000000, got %v", m["Amount"])
				}
				if m["FinishAfter"] != uint32(700000000) {
					t.Errorf("expected FinishAfter=700000000, got %v", m["FinishAfter"])
				}
			},
		},
		{
			name: "escrow with all optional fields",
			escrow: &EscrowCreate{
				BaseTx:         *tx.NewBaseTx(tx.TypeEscrowCreate, "rAlice"),
				Amount:         tx.NewXRPAmount(2000000000),
				Destination:    "rBob",
				DestinationTag: ptrUint32(42),
				FinishAfter:    ptrUint32(700000000),
				CancelAfter:    ptrUint32(800000000),
				Condition:      ptrString("DEADBEEF"),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["DestinationTag"] != uint32(42) {
					t.Errorf("expected DestinationTag=42, got %v", m["DestinationTag"])
				}
				if m["CancelAfter"] != uint32(800000000) {
					t.Errorf("expected CancelAfter=800000000, got %v", m["CancelAfter"])
				}
				if m["Condition"] != "DEADBEEF" {
					t.Errorf("expected Condition=DEADBEEF, got %v", m["Condition"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.escrow.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestEscrowFinishFlatten tests the Flatten method for EscrowFinish.
func TestEscrowFinishFlatten(t *testing.T) {
	tests := []struct {
		name     string
		escrow   *EscrowFinish
		checkMap func(t *testing.T, m map[string]any)
	}{
		{
			name: "unconditional finish",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Owner"] != "rAlice" {
					t.Errorf("expected Owner=rAlice, got %v", m["Owner"])
				}
				if m["OfferSequence"] != uint32(12345) {
					t.Errorf("expected OfferSequence=12345, got %v", m["OfferSequence"])
				}
				if _, ok := m["Condition"]; ok {
					t.Error("Condition should not be present")
				}
			},
		},
		{
			name: "conditional finish",
			escrow: &EscrowFinish{
				BaseTx:        *tx.NewBaseTx(tx.TypeEscrowFinish, "rBob"),
				Owner:         "rAlice",
				OfferSequence: 12345,
				Condition:     ptrString("DEADBEEF"),
				Fulfillment:   ptrString("CAFEBABE"),
			},
			checkMap: func(t *testing.T, m map[string]any) {
				if m["Condition"] != "DEADBEEF" {
					t.Errorf("expected Condition=DEADBEEF, got %v", m["Condition"])
				}
				if m["Fulfillment"] != "CAFEBABE" {
					t.Errorf("expected Fulfillment=CAFEBABE, got %v", m["Fulfillment"])
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := tt.escrow.Flatten()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			tt.checkMap(t, m)
		})
	}
}

// TestEscrowCancelFlatten tests the Flatten method for EscrowCancel.
func TestEscrowCancelFlatten(t *testing.T) {
	e := &EscrowCancel{
		BaseTx:        *tx.NewBaseTx(tx.TypeEscrowCancel, "rBob"),
		Owner:         "rAlice",
		OfferSequence: 12345,
	}

	m, err := e.Flatten()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if m["Owner"] != "rAlice" {
		t.Errorf("expected Owner=rAlice, got %v", m["Owner"])
	}
	if m["OfferSequence"] != uint32(12345) {
		t.Errorf("expected OfferSequence=12345, got %v", m["OfferSequence"])
	}
}

// TestEscrowTransactionTypes tests that transaction types are correctly returned.
func TestEscrowTransactionTypes(t *testing.T) {
	t.Run("EscrowCreate type", func(t *testing.T) {
		e := NewEscrowCreate("rAlice", "rBob", tx.NewXRPAmount(1000000))
		if e.TxType() != tx.TypeEscrowCreate {
			t.Errorf("expected TypeEscrowCreate, got %v", e.TxType())
		}
	})

	t.Run("EscrowFinish type", func(t *testing.T) {
		e := NewEscrowFinish("rBob", "rAlice", 123)
		if e.TxType() != tx.TypeEscrowFinish {
			t.Errorf("expected TypeEscrowFinish, got %v", e.TxType())
		}
	})

	t.Run("EscrowCancel type", func(t *testing.T) {
		e := NewEscrowCancel("rBob", "rAlice", 123)
		if e.TxType() != tx.TypeEscrowCancel {
			t.Errorf("expected TypeEscrowCancel, got %v", e.TxType())
		}
	})
}

// TestNewEscrowConstructors tests the constructor functions.
func TestNewEscrowConstructors(t *testing.T) {
	t.Run("NewEscrowCreate", func(t *testing.T) {
		e := NewEscrowCreate("rAlice", "rBob", tx.NewXRPAmount(1000000))
		if e.Account != "rAlice" {
			t.Errorf("expected Account=rAlice, got %v", e.Account)
		}
		if e.Destination != "rBob" {
			t.Errorf("expected Destination=rBob, got %v", e.Destination)
		}
		if e.Amount.Value() != "1000000" {
			t.Errorf("expected Amount=1000000, got %v", e.Amount.Value())
		}
	})

	t.Run("NewEscrowFinish", func(t *testing.T) {
		e := NewEscrowFinish("rBob", "rAlice", 123)
		if e.Account != "rBob" {
			t.Errorf("expected Account=rBob, got %v", e.Account)
		}
		if e.Owner != "rAlice" {
			t.Errorf("expected Owner=rAlice, got %v", e.Owner)
		}
		if e.OfferSequence != 123 {
			t.Errorf("expected OfferSequence=123, got %v", e.OfferSequence)
		}
	})

	t.Run("NewEscrowCancel", func(t *testing.T) {
		e := NewEscrowCancel("rBob", "rAlice", 456)
		if e.Account != "rBob" {
			t.Errorf("expected Account=rBob, got %v", e.Account)
		}
		if e.Owner != "rAlice" {
			t.Errorf("expected Owner=rAlice, got %v", e.Owner)
		}
		if e.OfferSequence != 456 {
			t.Errorf("expected OfferSequence=456, got %v", e.OfferSequence)
		}
	})
}

// Helper function to create a pointer to uint32
func ptrUint32(v uint32) *uint32 {
	return &v
}

func ptrString(v string) *string {
	return &v
}

// TestCryptoConditionValidation tests the crypto-condition verification.
// Reference: rippled Escrow_test.cpp condition/fulfillment tests
func TestCryptoConditionValidation(t *testing.T) {
	// Test vectors from rippled's Escrow_test.cpp
	// These correspond to the test conditions in internal/testing/builders/conditions.go

	tests := []struct {
		name        string
		condition   string // hex-encoded condition
		fulfillment string // hex-encoded fulfillment
		expectError bool
	}{
		{
			// cb1/fb1: Empty preimage - SHA256("") produces E3B0C442...
			name:        "empty preimage - valid",
			condition:   "A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100",
			fulfillment: "A0028000",
			expectError: false,
		},
		{
			// cb2/fb2: Preimage "aaa" - SHA256("aaa") produces 9834876D...
			name:        "preimage aaa - valid",
			condition:   "A02580209834876DCFB05CB167A5C24953EBA58C4AC89B1ADF57F28F2F9D09AF107EE8F0810103",
			fulfillment: "A0058003616161", // A0=type, 05=length, 80=preimage tag, 03=preimage len, 616161="aaa"
			expectError: false,
		},
		{
			// cb3/fb3: Preimage "nikb" (0x6E696B62) from rippled - SHA256("nikb") produces 6E4C7145...
			name:        "preimage nikb - valid",
			condition:   "A02580206E4C714530C0A4268B3FA63B1B606F2D264A2D857BE8A09C1DFD570D15858BD4810104",
			fulfillment: "A00680046E696B62", // A0=type, 06=length, 80=preimage tag, 04=preimage len, 6E696B62="nikb"
			expectError: false,
		},
		{
			// Wrong fulfillment for condition - "bbb" doesn't match "aaa" condition
			name:        "wrong fulfillment - invalid",
			condition:   "A02580209834876DCFB05CB167A5C24953EBA58C4AC89B1ADF57F28F2F9D09AF107EE8F0810103",
			fulfillment: "A005800362626262", // "bbb" instead of "aaa"
			expectError: true,
		},
		{
			// Empty fulfillment
			name:        "empty fulfillment - invalid",
			condition:   "A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100",
			fulfillment: "",
			expectError: true,
		},
		{
			// Malformed condition (too short)
			name:        "malformed condition - invalid",
			condition:   "A025",
			fulfillment: "A0028000",
			expectError: true,
		},
		{
			// Malformed fulfillment (too short)
			name:        "malformed fulfillment - invalid",
			condition:   "A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100",
			fulfillment: "A002",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCryptoCondition(tt.fulfillment, tt.condition)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
			}
		})
	}
}

// TestParseCondition tests parsing of crypto-conditions
func TestParseCondition(t *testing.T) {
	tests := []struct {
		name         string
		conditionHex string
		expectType   uint8
		expectFPLen  int
		expectError  bool
	}{
		{
			name:         "PREIMAGE-SHA-256 condition (empty preimage)",
			conditionHex: "A0258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100",
			expectType:   0, // PREIMAGE-SHA-256
			expectFPLen:  32,
			expectError:  false,
		},
		{
			name:         "too short",
			conditionHex: "A025",
			expectType:   0,
			expectFPLen:  0,
			expectError:  true,
		},
		{
			// Invalid tag: 0x30 is universal constructed (SEQUENCE), not context-specific
			name:         "invalid tag - universal constructed",
			conditionHex: "30258020E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855810100",
			expectType:   0,
			expectFPLen:  0,
			expectError:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := decodeHex(tt.conditionHex)
			if err != nil {
				t.Fatalf("invalid test data: %v", err)
			}

			fp, condType, err := parseCondition(data)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				if condType != tt.expectType {
					t.Errorf("expected type %d, got %d", tt.expectType, condType)
				}
				if len(fp) != tt.expectFPLen {
					t.Errorf("expected fingerprint length %d, got %d", tt.expectFPLen, len(fp))
				}
			}
		})
	}
}

// TestParseFulfillment tests parsing of crypto-fulfillments
func TestParseFulfillment(t *testing.T) {
	tests := []struct {
		name           string
		fulfillmentHex string
		expectType     uint8
		expectPreimage string // hex of expected preimage
		expectError    bool
	}{
		{
			name:           "empty preimage",
			fulfillmentHex: "A0028000",
			expectType:     0,
			expectPreimage: "",
			expectError:    false,
		},
		{
			name:           "preimage aaa",
			fulfillmentHex: "A005800361616161",
			expectType:     0,
			expectPreimage: "616161", // "aaa" in hex
			expectError:    false,
		},
		{
			name:           "preimage zzz",
			fulfillmentHex: "A00580037A7A7A",
			expectType:     0,
			expectPreimage: "7A7A7A", // "zzz" in hex
			expectError:    false,
		},
		{
			name:           "too short",
			fulfillmentHex: "A002",
			expectType:     0,
			expectPreimage: "",
			expectError:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := decodeHex(tt.fulfillmentHex)
			if err != nil {
				t.Fatalf("invalid test data: %v", err)
			}

			preimage, fulfType, _, err := parseFulfillment(data)
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error, got nil")
				}
			} else {
				if err != nil {
					t.Errorf("expected no error, got %v", err)
				}
				if fulfType != tt.expectType {
					t.Errorf("expected type %d, got %d", tt.expectType, fulfType)
				}
				expectedPreimage, _ := decodeHex(tt.expectPreimage)
				if len(preimage) != len(expectedPreimage) {
					t.Errorf("expected preimage length %d, got %d", len(expectedPreimage), len(preimage))
				}
				for i := range expectedPreimage {
					if preimage[i] != expectedPreimage[i] {
						t.Errorf("preimage mismatch at %d: expected %02x, got %02x", i, expectedPreimage[i], preimage[i])
					}
				}
			}
		})
	}
}

// decodeHex is a test helper that decodes hex strings
func decodeHex(s string) ([]byte, error) {
	if s == "" {
		return []byte{}, nil
	}
	result := make([]byte, len(s)/2)
	for i := 0; i < len(s)/2; i++ {
		b := hexDigit(s[i*2])<<4 | hexDigit(s[i*2+1])
		result[i] = b
	}
	return result, nil
}

func hexDigit(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10
	default:
		return 0
	}
}
