package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBridge_Type verifies Bridge returns correct type
func TestBridge_Type(t *testing.T) {
	bridge := &Bridge{}
	assert.Equal(t, TypeBridge, bridge.Type())
	assert.Equal(t, "Bridge", bridge.Type().String())
}

// TestBridge_Validate tests Bridge validation logic
// Reference: rippled/src/test/app/XChain_test.cpp
func TestBridge_Validate(t *testing.T) {
	validAccount := [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20}
	validBridge := XChainBridge{
		LockingChainDoor: [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		IssuingChainDoor: [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
	}

	t.Run("Valid Bridge with required fields", func(t *testing.T) {
		bridge := &Bridge{
			Account:         validAccount,
			SignatureReward: 100,
			XChainBridge:    validBridge,
		}
		err := bridge.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid Bridge with optional MinAccountCreateAmount", func(t *testing.T) {
		minAmount := uint64(10000000)
		bridge := &Bridge{
			Account:                validAccount,
			SignatureReward:        100,
			XChainBridge:           validBridge,
			MinAccountCreateAmount: &minAmount,
		}
		err := bridge.Validate()
		assert.NoError(t, err)
	})

	t.Run("Invalid Bridge with empty account", func(t *testing.T) {
		bridge := &Bridge{
			Account:         [20]byte{},
			SignatureReward: 100,
			XChainBridge:    validBridge,
		}
		err := bridge.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "account")
	})

	t.Run("Invalid Bridge with zero signature reward", func(t *testing.T) {
		bridge := &Bridge{
			Account:         validAccount,
			SignatureReward: 0,
			XChainBridge:    validBridge,
		}
		err := bridge.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "signature reward")
	})
}

// TestBridge_Hash tests Bridge hash computation
func TestBridge_Hash(t *testing.T) {
	bridge := &Bridge{
		Account:         [20]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20},
		SignatureReward: 100,
	}

	hash1, err := bridge.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Different account should produce different hash
	bridge2 := &Bridge{
		Account:         [20]byte{20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1},
		SignatureReward: 100,
	}
	hash2, err := bridge2.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, hash1, hash2)
}

// TestXChainBridge tests the XChainBridge struct
func TestXChainBridge(t *testing.T) {
	bridge := XChainBridge{
		LockingChainDoor: [20]byte{1, 2, 3},
		IssuingChainDoor: [20]byte{4, 5, 6},
		LockingChainIssue: Issue{
			Currency: [20]byte{},
			Issuer:   [20]byte{},
		},
		IssuingChainIssue: Issue{
			Currency: [20]byte{7, 8, 9},
			Issuer:   [20]byte{10, 11, 12},
		},
	}

	assert.Equal(t, byte(1), bridge.LockingChainDoor[0])
	assert.Equal(t, byte(4), bridge.IssuingChainDoor[0])
}
