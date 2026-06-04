package nodestore_test

import (
	"testing"

	"github.com/LeJamon/go-xrpl/storage/nodestore"
)

func TestVerify(t *testing.T) {
	t.Run("VerifyMemoryBackend", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 10 {
			data := nodestore.Blob("verify test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		// Verify should pass
		if err := backend.Verify(); err != nil {
			t.Errorf("Verify returned error for valid data: %v", err)
		}
	})

	t.Run("VerifyWithOptions", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 10 {
			data := nodestore.Blob("verify options test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		opts := &nodestore.VerifyOptions{
			StopOnFirstError: true,
			MaxCorruptNodes:  5,
			ProgressInterval: 5,
		}

		if err := backend.VerifyWithOptions(opts); err != nil {
			t.Errorf("VerifyWithOptions returned error for valid data: %v", err)
		}
	})

	t.Run("VerifyAll", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 10 {
			data := nodestore.Blob("verify all test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		result, err := backend.VerifyAll(nil)
		if err != nil {
			t.Fatalf("VerifyAll returned error: %v", err)
		}

		if !result.IsValid() {
			t.Error("result should be valid")
		}

		if result.TotalNodes != 10 {
			t.Errorf("expected 10 total nodes, got %d", result.TotalNodes)
		}

		if result.CorruptNodes != 0 {
			t.Errorf("expected 0 corrupt nodes, got %d", result.CorruptNodes)
		}
	})

	t.Run("VerifyNode", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		data := nodestore.Blob("verify node test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		backend.Store(node)

		// Verify specific node
		if err := backend.VerifyNode(node.Hash); err != nil {
			t.Errorf("VerifyNode returned error for valid node: %v", err)
		}
	})

	t.Run("VerifyNodeNotFound", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Try to verify non-existent node
		hash := nodestore.ComputeHash256(nodestore.Blob("non-existent"))
		if err := backend.VerifyNode(hash); err == nil {
			t.Error("expected error for non-existent node")
		}
	})

	t.Run("VerifyClosedBackend", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()

		// Backend is not open
		if err := backend.Verify(); err == nil {
			t.Error("expected error for closed backend")
		}
	})

	t.Run("ProgressCallback", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some nodes
		for i := range 25 {
			data := nodestore.Blob("progress test " + string(rune(i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		callbackCount := 0
		opts := &nodestore.VerifyOptions{
			ProgressInterval: 5,
			ProgressCallback: func(verified int64) {
				callbackCount++
			},
		}

		backend.VerifyWithOptions(opts)

		// Callback should have been called at least once
		if callbackCount == 0 {
			t.Error("progress callback was never called")
		}
	})
}

func TestBackendVerifier(t *testing.T) {
	t.Run("VerifyGenericBackend", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 10 {
			data := nodestore.Blob("generic verify test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		// Use the generic verifier
		verifier := nodestore.NewBackendVerifier(backend)

		if err := verifier.Verify(); err != nil {
			t.Errorf("Verify returned error for valid data: %v", err)
		}
	})

	t.Run("VerifyAllGeneric", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 5 {
			data := nodestore.Blob("generic verify all test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		verifier := nodestore.NewBackendVerifier(backend)
		result, err := verifier.VerifyAll(nil)
		if err != nil {
			t.Fatalf("VerifyAll returned error: %v", err)
		}

		if result.TotalNodes != 5 {
			t.Errorf("expected 5 total nodes, got %d", result.TotalNodes)
		}

		if !result.IsValid() {
			t.Error("result should be valid")
		}
	})

	t.Run("VerifyNodeGeneric", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		data := nodestore.Blob("generic verify node test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		backend.Store(node)

		verifier := nodestore.NewBackendVerifier(backend)

		if err := verifier.VerifyNode(node.Hash); err != nil {
			t.Errorf("VerifyNode returned error for valid node: %v", err)
		}
	})

	t.Run("VerifyNodeNotFoundGeneric", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		verifier := nodestore.NewBackendVerifier(backend)

		hash := nodestore.ComputeHash256(nodestore.Blob("non-existent"))
		if err := verifier.VerifyNode(hash); err == nil {
			t.Error("expected error for non-existent node")
		}
	})

	t.Run("VerifyClosedBackendGeneric", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		verifier := nodestore.NewBackendVerifier(backend)

		if err := verifier.Verify(); err == nil {
			t.Error("expected error for closed backend")
		}
	})
}

func TestVerificationResult(t *testing.T) {
	t.Run("IsValid", func(t *testing.T) {
		// Valid result
		valid := &nodestore.VerificationResult{
			TotalNodes:   100,
			CorruptNodes: 0,
			MissingData:  0,
			HashMismatch: 0,
		}
		if !valid.IsValid() {
			t.Error("result should be valid")
		}

		// Invalid - corrupt nodes
		corrupt := &nodestore.VerificationResult{
			TotalNodes:   100,
			CorruptNodes: 5,
		}
		if corrupt.IsValid() {
			t.Error("result with corrupt nodes should not be valid")
		}

		// Invalid - missing data
		missing := &nodestore.VerificationResult{
			TotalNodes:  100,
			MissingData: 3,
		}
		if missing.IsValid() {
			t.Error("result with missing data should not be valid")
		}

		// Invalid - hash mismatch
		mismatch := &nodestore.VerificationResult{
			TotalNodes:   100,
			HashMismatch: 2,
		}
		if mismatch.IsValid() {
			t.Error("result with hash mismatch should not be valid")
		}
	})

	t.Run("String", func(t *testing.T) {
		result := &nodestore.VerificationResult{
			TotalNodes:   100,
			CorruptNodes: 5,
			MissingData:  2,
			HashMismatch: 3,
		}

		s := result.String()

		if s == "" {
			t.Error("String() should not be empty")
		}

		// Should indicate corruption
		if !containsString(s, "CORRUPT") {
			t.Error("String should indicate CORRUPT status")
		}

		// Valid result should say VALID
		valid := &nodestore.VerificationResult{
			TotalNodes: 100,
		}
		s2 := valid.String()
		if !containsString(s2, "VALID") {
			t.Error("Valid result String should indicate VALID status")
		}
	})

	t.Run("CorruptHashesLimit", func(t *testing.T) {
		backend := nodestore.NewMemoryBackend()
		if err := backend.Open(true); err != nil {
			t.Fatalf("failed to open backend: %v", err)
		}
		defer backend.Close()

		// Store some valid nodes
		for i := range 5 {
			data := nodestore.Blob("corrupt hashes limit test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			backend.Store(node)
		}

		opts := &nodestore.VerifyOptions{
			MaxCorruptNodes: 3, // Limit corrupt hashes collection
		}

		result, err := backend.VerifyAll(opts)
		if err != nil {
			t.Fatalf("VerifyAll returned error: %v", err)
		}

		// No corruption, so corrupt hashes should be empty
		if len(result.CorruptHashes) != 0 {
			t.Errorf("expected 0 corrupt hashes, got %d", len(result.CorruptHashes))
		}
	})
}

func TestVerifyOptions(t *testing.T) {
	t.Run("Defaults", func(t *testing.T) {
		opts := nodestore.DefaultVerifyOptions()

		if opts.StopOnFirstError {
			t.Error("StopOnFirstError should be false by default")
		}

		if opts.MaxCorruptNodes != 100 {
			t.Errorf("expected MaxCorruptNodes 100, got %d", opts.MaxCorruptNodes)
		}

		if opts.ProgressInterval != 10000 {
			t.Errorf("expected ProgressInterval 10000, got %d", opts.ProgressInterval)
		}
	})
}
