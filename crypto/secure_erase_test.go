package crypto

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSecureErase(t *testing.T) {
	t.Run("Erases data", func(t *testing.T) {
		data := []byte{0x01, 0x02, 0x03, 0x04, 0x05}
		original := make([]byte, len(data))
		copy(original, data)

		SecureErase(data)

		// All bytes should be zero
		assert.True(t, bytes.Equal(data, make([]byte, len(data))))
		// Should have been modified
		assert.False(t, bytes.Equal(data, original))
	})

	t.Run("Handles empty slice", func(t *testing.T) {
		// Should not panic
		SecureErase([]byte{})
		SecureErase(nil)
	})

	t.Run("Erases large buffer", func(t *testing.T) {
		data := make([]byte, 1024)
		for i := range data {
			data[i] = byte(i % 256)
		}

		SecureErase(data)

		for i := range data {
			assert.Equal(t, byte(0), data[i])
		}
	})
}
