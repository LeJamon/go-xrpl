package entry

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNegativeUNL_Type verifies NegativeUNL returns correct type
func TestNegativeUNL_Type(t *testing.T) {
	nunl := &NegativeUNL{}
	assert.Equal(t, TypeNegativeUNL, nunl.Type())
	assert.Equal(t, "NegativeUNL", nunl.Type().String())
}

// TestNegativeUNL_Validate tests NegativeUNL validation logic
// NegativeUNL is a singleton with all optional fields
func TestNegativeUNL_Validate(t *testing.T) {
	t.Run("Valid empty NegativeUNL", func(t *testing.T) {
		// NegativeUNL is a singleton and all fields are optional
		nunl := &NegativeUNL{}
		err := nunl.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid NegativeUNL with disabled validators", func(t *testing.T) {
		nunl := &NegativeUNL{
			DisabledValidators: []DisabledValidator{
				{
					PublicKey:      [33]byte{1, 2, 3},
					FirstLedgerSeq: 100,
				},
			},
		}
		err := nunl.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid NegativeUNL with validator to disable", func(t *testing.T) {
		pubKey := [33]byte{1, 2, 3, 4, 5}
		nunl := &NegativeUNL{
			ValidatorToDisable: &pubKey,
		}
		err := nunl.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid NegativeUNL with validator to re-enable", func(t *testing.T) {
		pubKey := [33]byte{1, 2, 3, 4, 5}
		nunl := &NegativeUNL{
			ValidatorToReEnable: &pubKey,
		}
		err := nunl.Validate()
		assert.NoError(t, err)
	})

	t.Run("Valid NegativeUNL with all fields", func(t *testing.T) {
		toDisable := [33]byte{1, 2, 3}
		toReEnable := [33]byte{4, 5, 6}
		nunl := &NegativeUNL{
			DisabledValidators: []DisabledValidator{
				{PublicKey: [33]byte{7, 8, 9}, FirstLedgerSeq: 50},
			},
			ValidatorToDisable:  &toDisable,
			ValidatorToReEnable: &toReEnable,
		}
		err := nunl.Validate()
		assert.NoError(t, err)
	})
}

// TestNegativeUNL_Hash tests NegativeUNL hash computation
func TestNegativeUNL_Hash(t *testing.T) {
	nunl := &NegativeUNL{
		BaseEntry: BaseEntry{
			PreviousTxnLgrSeq: 100,
			Flags:             0,
		},
	}

	hash1, err := nunl.Hash()
	require.NoError(t, err)
	assert.NotEqual(t, [32]byte{}, hash1)

	// Same entry should produce same hash
	hash2, err := nunl.Hash()
	require.NoError(t, err)
	assert.Equal(t, hash1, hash2)
}

// TestDisabledValidator tests the DisabledValidator struct
func TestDisabledValidator(t *testing.T) {
	dv := DisabledValidator{
		PublicKey:      [33]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
		FirstLedgerSeq: 12345,
	}

	assert.Equal(t, uint32(12345), dv.FirstLedgerSeq)
	assert.Equal(t, byte(1), dv.PublicKey[0])
}
