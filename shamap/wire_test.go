package shamap

import (
	"testing"
)

func TestSerializeRoot(t *testing.T) {
	t.Run("WithContent", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		var key [32]byte
		key[0] = 1
		if err := sMap.Put(key, make([]byte, 12)); err != nil {
			t.Fatalf("Failed to put: %v", err)
		}

		data, err := sMap.SerializeRoot()
		if err != nil {
			t.Fatalf("SerializeRoot failed: %v", err)
		}

		if len(data) == 0 {
			t.Error("Serialized data should not be empty")
		}
	})

	t.Run("EmptyMap", func(t *testing.T) {
		sMap, err := New(TypeState)
		if err != nil {
			t.Fatalf("Failed to create SHAMap: %v", err)
		}

		// Empty root should still serialize (though it may fail)
		_, _ = sMap.SerializeRoot()
		// This may or may not fail depending on implementation
		// Just verify it doesn't panic
	})
}
