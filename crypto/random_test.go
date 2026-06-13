package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRandomBytes(t *testing.T) {
	t.Run("Generates correct length", func(t *testing.T) {
		for _, n := range []int{1, 16, 32, 64, 128} {
			b, err := RandomBytes(n)
			require.NoError(t, err)
			assert.Equal(t, n, len(b))
		}
	})

	t.Run("Zero length returns nil", func(t *testing.T) {
		b, err := RandomBytes(0)
		require.NoError(t, err)
		assert.Nil(t, b)
	})

	t.Run("Negative length returns nil", func(t *testing.T) {
		b, err := RandomBytes(-1)
		require.NoError(t, err)
		assert.Nil(t, b)
	})

	t.Run("Generates different values", func(t *testing.T) {
		b1, err := RandomBytes(32)
		require.NoError(t, err)
		b2, err := RandomBytes(32)
		require.NoError(t, err)

		// Extremely unlikely to be equal
		assert.False(t, bytes.Equal(b1, b2))
	})
}

func TestRandomSeed(t *testing.T) {
	t.Run("Generates 16 byte seed", func(t *testing.T) {
		seed, err := RandomSeed()
		require.NoError(t, err)
		assert.Equal(t, 16, len(seed))
	})

	t.Run("Generates different seeds", func(t *testing.T) {
		seed1, err := RandomSeed()
		require.NoError(t, err)

		seed2, err := RandomSeed()
		require.NoError(t, err)

		assert.False(t, bytes.Equal(seed1, seed2))
	})
}
