package tx

import (
	"testing"
)

// TestReserveCalculations tests reserve calculation logic.
// These tests are based on rippled's SetTrust_test.cpp::testFreeTrustlines
// and similar reserve validation tests.

// TestAccountReserve tests the account reserve calculation.
func TestAccountReserve(t *testing.T) {
	tests := []struct {
		name             string
		reserveBase      uint64
		reserveIncrement uint64
		ownerCount       uint32
		expected         uint64
	}{
		{
			name:             "zero owners",
			reserveBase:      10000000, // 10 XRP in drops
			reserveIncrement: 2000000,  // 2 XRP in drops
			ownerCount:       0,
			expected:         10000000, // Just base reserve
		},
		{
			name:             "one owner",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			ownerCount:       1,
			expected:         12000000, // 10 + 2
		},
		{
			name:             "five owners",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			ownerCount:       5,
			expected:         20000000, // 10 + (5 * 2)
		},
		{
			name:             "testnet values",
			reserveBase:      1000000, // 1 XRP
			reserveIncrement: 200000,  // 0.2 XRP
			ownerCount:       10,
			expected:         3000000, // 1 + (10 * 0.2)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &Engine{
				config: EngineConfig{
					ReserveBase:      tt.reserveBase,
					ReserveIncrement: tt.reserveIncrement,
				},
			}

			result := engine.AccountReserve(tt.ownerCount)
			if result != tt.expected {
				t.Errorf("AccountReserve(%d) = %d, want %d",
					tt.ownerCount, result, tt.expected)
			}
		})
	}
}

// TestReserveForNewObject tests the reserve needed for creating new ledger objects.
// Reference: rippled SetTrust.cpp:405-407 - first 2 objects are free
func TestReserveForNewObject(t *testing.T) {
	tests := []struct {
		name             string
		reserveBase      uint64
		reserveIncrement uint64
		currentOwners    uint32
		expected         uint64
	}{
		{
			name:             "first object - free",
			reserveBase:      10000000, // 10 XRP
			reserveIncrement: 2000000,  // 2 XRP
			currentOwners:    0,
			expected:         0, // First 2 objects are free
		},
		{
			name:             "second object - free",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			currentOwners:    1,
			expected:         0, // First 2 objects are free
		},
		{
			name:             "third object - requires reserve",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			currentOwners:    2,
			expected:         16000000, // accountReserve(3) = 10 + 3*2 = 16 XRP
		},
		{
			name:             "fourth object",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			currentOwners:    3,
			expected:         18000000, // accountReserve(4) = 10 + 4*2 = 18 XRP
		},
		{
			name:             "fifth object",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			currentOwners:    4,
			expected:         20000000, // accountReserve(5) = 10 + 5*2 = 20 XRP
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &Engine{
				config: EngineConfig{
					ReserveBase:      tt.reserveBase,
					ReserveIncrement: tt.reserveIncrement,
				},
			}

			result := engine.ReserveForNewObject(tt.currentOwners)
			if result != tt.expected {
				t.Errorf("ReserveForNewObject(%d) = %d, want %d",
					tt.currentOwners, result, tt.expected)
			}
		})
	}
}

// TestCanCreateNewObject tests whether an account can create a new ledger object.
func TestCanCreateNewObject(t *testing.T) {
	tests := []struct {
		name             string
		reserveBase      uint64
		reserveIncrement uint64
		priorBalance     uint64
		currentOwners    uint32
		canCreate        bool
	}{
		{
			name:             "can create first object with minimal balance",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     1, // Just 1 drop
			currentOwners:    0,
			canCreate:        true, // First 2 objects are free
		},
		{
			name:             "can create second object with minimal balance",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     1,
			currentOwners:    1,
			canCreate:        true, // First 2 objects are free
		},
		{
			name:             "cannot create third object - insufficient balance",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     15999999, // Just below reserve requirement
			currentOwners:    2,
			canCreate:        false,
		},
		{
			name:             "can create third object - exact balance",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     16000000, // Exactly reserve requirement
			currentOwners:    2,
			canCreate:        true,
		},
		{
			name:             "can create third object - excess balance",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     100000000, // 100 XRP
			currentOwners:    2,
			canCreate:        true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &Engine{
				config: EngineConfig{
					ReserveBase:      tt.reserveBase,
					ReserveIncrement: tt.reserveIncrement,
				},
			}

			result := engine.CanCreateNewObject(tt.priorBalance, tt.currentOwners)
			if result != tt.canCreate {
				t.Errorf("CanCreateNewObject(%d, %d) = %v, want %v",
					tt.priorBalance, tt.currentOwners, result, tt.canCreate)
			}
		})
	}
}

// TestCheckReserveIncrease tests the reserve increase check.
func TestCheckReserveIncrease(t *testing.T) {
	tests := []struct {
		name             string
		reserveBase      uint64
		reserveIncrement uint64
		priorBalance     uint64
		currentOwners    uint32
		expectedResult   Result
	}{
		{
			name:             "first object - always succeeds",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     1,
			currentOwners:    0,
			expectedResult:   TesSUCCESS,
		},
		{
			name:             "second object - always succeeds",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     1,
			currentOwners:    1,
			expectedResult:   TesSUCCESS,
		},
		{
			name:             "third object - insufficient reserve",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     15000000, // 15 XRP, need 16 XRP
			currentOwners:    2,
			expectedResult:   TecINSUFFICIENT_RESERVE,
		},
		{
			name:             "third object - sufficient reserve",
			reserveBase:      10000000,
			reserveIncrement: 2000000,
			priorBalance:     20000000, // 20 XRP
			currentOwners:    2,
			expectedResult:   TesSUCCESS,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := &Engine{
				config: EngineConfig{
					ReserveBase:      tt.reserveBase,
					ReserveIncrement: tt.reserveIncrement,
				},
			}

			result := engine.CheckReserveIncrease(tt.priorBalance, tt.currentOwners)
			if result != tt.expectedResult {
				t.Errorf("CheckReserveIncrease(%d, %d) = %v, want %v",
					tt.priorBalance, tt.currentOwners, result, tt.expectedResult)
			}
		})
	}
}

// TestFreeTrustlines tests that the first 2 trust lines are free (no reserve increase).
// This matches rippled's SetTrust_test.cpp::testFreeTrustlines
func TestFreeTrustlines(t *testing.T) {
	baseReserve := uint64(10000000)      // 10 XRP
	reserveIncrement := uint64(2000000)  // 2 XRP

	engine := &Engine{
		config: EngineConfig{
			ReserveBase:      baseReserve,
			ReserveIncrement: reserveIncrement,
		},
	}

	// Account funded with just base reserve (enough for account itself)
	accountBalance := baseReserve

	// First trust line - should be free (ownerCount = 0)
	reserveNeeded := engine.ReserveForNewObject(0)
	if reserveNeeded != 0 {
		t.Errorf("First trust line should be free, got reserve requirement %d", reserveNeeded)
	}
	if !engine.CanCreateNewObject(accountBalance, 0) {
		t.Error("Should be able to create first trust line with minimal balance")
	}

	// Second trust line - should be free (ownerCount = 1)
	reserveNeeded = engine.ReserveForNewObject(1)
	if reserveNeeded != 0 {
		t.Errorf("Second trust line should be free, got reserve requirement %d", reserveNeeded)
	}
	if !engine.CanCreateNewObject(accountBalance, 1) {
		t.Error("Should be able to create second trust line with minimal balance")
	}

	// Third trust line - requires reserve (ownerCount = 2)
	reserveNeeded = engine.ReserveForNewObject(2)
	expectedReserve := baseReserve + 3*reserveIncrement // accountReserve(3)
	if reserveNeeded != expectedReserve {
		t.Errorf("Third trust line reserve = %d, want %d", reserveNeeded, expectedReserve)
	}

	// With only base reserve, cannot create third trust line
	if engine.CanCreateNewObject(accountBalance, 2) {
		t.Error("Should NOT be able to create third trust line with only base reserve")
	}

	// With sufficient balance, can create third trust line
	sufficientBalance := expectedReserve
	if !engine.CanCreateNewObject(sufficientBalance, 2) {
		t.Errorf("Should be able to create third trust line with balance %d", sufficientBalance)
	}
}

// Benchmark tests
func BenchmarkAccountReserve(b *testing.B) {
	engine := &Engine{
		config: EngineConfig{
			ReserveBase:      10000000,
			ReserveIncrement: 2000000,
		},
	}
	for i := 0; i < b.N; i++ {
		engine.AccountReserve(10)
	}
}

func BenchmarkReserveForNewObject(b *testing.B) {
	engine := &Engine{
		config: EngineConfig{
			ReserveBase:      10000000,
			ReserveIncrement: 2000000,
		},
	}
	for i := 0; i < b.N; i++ {
		engine.ReserveForNewObject(5)
	}
}
