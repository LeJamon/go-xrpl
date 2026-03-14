package nodestore_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/storage/nodestore"
)

func TestRotatingDatabase(t *testing.T) {
	t.Run("Creation", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if rd.Name() != "rotating" {
			t.Errorf("expected name 'rotating', got %q", rd.Name())
		}
	})

	t.Run("NilConfig", func(t *testing.T) {
		_, err := nodestore.NewRotatingDatabase(nil, nodestore.NewMemoryBackendFromConfig)
		if err == nil {
			t.Error("expected error for nil config")
		}
	})

	t.Run("NilFactory", func(t *testing.T) {
		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig:     &nodestore.Config{Path: "/tmp/test"},
			RotatingPath:      "/tmp/rotating",
		}

		_, err := nodestore.NewRotatingDatabase(config, nil)
		if err == nil {
			t.Error("expected error for nil factory")
		}
	})

	t.Run("OpenClose", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if rd.IsOpen() {
			t.Error("should not be open initially")
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}

		if !rd.IsOpen() {
			t.Error("should be open after Open()")
		}

		if err := rd.Close(); err != nil {
			t.Errorf("Close returned error: %v", err)
		}

		if rd.IsOpen() {
			t.Error("should not be open after Close()")
		}
	})

	t.Run("StoreAndFetch", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Store a node
		data := nodestore.Blob("rotating store test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		if status := rd.Store(node); status != nodestore.OK {
			t.Fatalf("failed to store: %v", status)
		}

		// Fetch the node
		fetched, status := rd.Fetch(node.Hash)
		if status != nodestore.OK {
			t.Fatalf("failed to fetch: %v", status)
		}

		if string(fetched.Data) != string(node.Data) {
			t.Error("fetched data doesn't match")
		}
	})

	t.Run("FetchNotFound", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		hash := nodestore.ComputeHash256(nodestore.Blob("non-existent"))
		_, status := rd.Fetch(hash)

		if status != nodestore.NotFound {
			t.Errorf("expected NotFound, got %v", status)
		}
	})

	t.Run("StoreBatch", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Create batch
		nodes := make([]*nodestore.Node, 5)
		for i := 0; i < 5; i++ {
			data := nodestore.Blob("batch test " + string(rune('A'+i)))
			nodes[i] = nodestore.NewNode(nodestore.NodeTransaction, data)
		}

		if status := rd.StoreBatch(nodes); status != nodestore.OK {
			t.Fatalf("failed to store batch: %v", status)
		}

		// Verify all nodes
		for _, node := range nodes {
			fetched, status := rd.Fetch(node.Hash)
			if status != nodestore.OK {
				t.Errorf("failed to fetch node from batch: %v", status)
			}
			if string(fetched.Data) != string(node.Data) {
				t.Error("fetched data doesn't match")
			}
		}
	})

	t.Run("FetchBatch", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Store some nodes
		hashes := make([]nodestore.Hash256, 5)
		for i := 0; i < 5; i++ {
			data := nodestore.Blob("fetch batch test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			rd.Store(node)
			hashes[i] = node.Hash
		}

		// Fetch batch
		fetched, status := rd.FetchBatch(hashes)
		if status != nodestore.OK {
			t.Fatalf("failed to fetch batch: %v", status)
		}

		if len(fetched) != 5 {
			t.Errorf("expected 5 nodes, got %d", len(fetched))
		}
	})

	t.Run("Rotate", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 10,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Store some data in primary
		var hashes []nodestore.Hash256
		for i := 0; i < 5; i++ {
			data := nodestore.Blob("pre-rotation " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			rd.Store(node)
			hashes = append(hashes, node.Hash)
		}

		// Rotate
		if err := rd.Rotate(); err != nil {
			t.Fatalf("Rotate returned error: %v", err)
		}

		// Should have one rotating backend
		rotatingBackends := rd.RotatingBackends()
		if len(rotatingBackends) != 1 {
			t.Errorf("expected 1 rotating backend, got %d", len(rotatingBackends))
		}

		// Data should still be accessible from rotating backend
		for _, hash := range hashes {
			fetched, status := rd.Fetch(hash)
			if status != nodestore.OK {
				t.Errorf("failed to fetch from rotating backend: %v", status)
			}
			if fetched == nil {
				t.Error("fetched node is nil")
			}
		}

		// New writes go to new primary
		data := nodestore.Blob("post-rotation")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		rd.Store(node)

		fetched, status := rd.Fetch(node.Hash)
		if status != nodestore.OK {
			t.Errorf("failed to fetch from new primary: %v", status)
		}
		if fetched == nil {
			t.Error("fetched node is nil")
		}
	})

	t.Run("ShouldRotate", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 10,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Should not need rotation initially
		if rd.ShouldRotate() {
			t.Error("should not need rotation initially")
		}

		// Write enough data to trigger rotation threshold
		for i := 0; i < 15; i++ {
			data := nodestore.Blob("rotation threshold test " + string(rune(i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			rd.Store(node)
		}

		// Should now recommend rotation
		if !rd.ShouldRotate() {
			t.Error("should recommend rotation after threshold exceeded")
		}
	})

	t.Run("Sync", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		if status := rd.Sync(); status != nodestore.OK {
			t.Errorf("Sync returned error: %v", status)
		}
	})

	t.Run("ForEach", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Store some nodes
		for i := 0; i < 5; i++ {
			data := nodestore.Blob("foreach test " + string(rune('A'+i)))
			node := nodestore.NewNode(nodestore.NodeTransaction, data)
			rd.Store(node)
		}

		// Count nodes
		count := 0
		err = rd.ForEach(func(node *nodestore.Node) error {
			count++
			return nil
		})

		if err != nil {
			t.Errorf("ForEach returned error: %v", err)
		}

		if count != 5 {
			t.Errorf("expected 5 nodes, counted %d", count)
		}
	})

	t.Run("Stats", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		// Perform some operations
		data := nodestore.Blob("stats test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)
		rd.Store(node)
		rd.Fetch(node.Hash)

		stats := rd.Stats()

		if stats.PrimaryWrites < 1 {
			t.Error("expected at least 1 primary write")
		}

		if stats.PrimaryReads < 1 {
			t.Error("expected at least 1 primary read")
		}
	})

	t.Run("PrimaryBackend", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		if err := rd.Open(true); err != nil {
			t.Fatalf("failed to open: %v", err)
		}
		defer rd.Close()

		primary := rd.PrimaryBackend()
		if primary == nil {
			t.Error("PrimaryBackend should not be nil")
		}
	})

	t.Run("OperationsOnClosedDatabase", func(t *testing.T) {
		tempDir := t.TempDir()

		config := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig: &nodestore.Config{
				Path:       filepath.Join(tempDir, "primary"),
				Compressor: "none",
			},
			RotatingPath: filepath.Join(tempDir, "rotating"),
		}

		rd, err := nodestore.NewRotatingDatabase(config, nodestore.NewMemoryBackendFromConfig)
		if err != nil {
			t.Fatalf("failed to create rotating database: %v", err)
		}

		// Operations on closed database should fail
		data := nodestore.Blob("closed test")
		node := nodestore.NewNode(nodestore.NodeTransaction, data)

		if status := rd.Store(node); status == nodestore.OK {
			t.Error("Store should fail on closed database")
		}

		if _, status := rd.Fetch(node.Hash); status == nodestore.OK {
			t.Error("Fetch should fail on closed database")
		}

		if err := rd.Rotate(); err == nil {
			t.Error("Rotate should fail on closed database")
		}
	})
}

func TestRotationConfig(t *testing.T) {
	t.Run("Defaults", func(t *testing.T) {
		config := nodestore.DefaultRotationConfig()

		if config.RotationThreshold <= 0 {
			t.Error("RotationThreshold should be positive")
		}

		if config.RetentionPeriod <= 0 {
			t.Error("RetentionPeriod should be positive")
		}
	})

	t.Run("Validation", func(t *testing.T) {
		// Valid config
		valid := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig:     &nodestore.Config{Path: "/tmp/test"},
			RotatingPath:      "/tmp/rotating",
		}
		if err := valid.Validate(); err != nil {
			t.Errorf("valid config returned error: %v", err)
		}

		// Invalid rotation threshold
		invalid1 := &nodestore.RotationConfig{
			RotationThreshold: 0,
			RetentionPeriod:   time.Hour,
			PrimaryConfig:     &nodestore.Config{Path: "/tmp/test"},
			RotatingPath:      "/tmp/rotating",
		}
		if err := invalid1.Validate(); err == nil {
			t.Error("expected error for invalid rotation threshold")
		}

		// Invalid retention period
		invalid2 := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   0,
			PrimaryConfig:     &nodestore.Config{Path: "/tmp/test"},
			RotatingPath:      "/tmp/rotating",
		}
		if err := invalid2.Validate(); err == nil {
			t.Error("expected error for invalid retention period")
		}

		// Missing primary config
		invalid3 := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			RotatingPath:      "/tmp/rotating",
		}
		if err := invalid3.Validate(); err == nil {
			t.Error("expected error for missing primary config")
		}

		// Missing rotating path
		invalid4 := &nodestore.RotationConfig{
			RotationThreshold: 100,
			RetentionPeriod:   time.Hour,
			PrimaryConfig:     &nodestore.Config{Path: "/tmp/test"},
		}
		if err := invalid4.Validate(); err == nil {
			t.Error("expected error for missing rotating path")
		}
	})
}

func TestRotatingDatabaseStats(t *testing.T) {
	t.Run("String", func(t *testing.T) {
		stats := nodestore.RotatingDatabaseStats{
			Rotations:        5,
			PrimaryReads:     1000,
			RotatingReads:    500,
			PrimaryWrites:    800,
			BytesWritten:     10240,
			BytesRead:        5120,
			DisposedBackends: 2,
			RotatingCount:    3,
		}

		s := stats.String()

		if s == "" {
			t.Error("Stats.String() should not be empty")
		}

		// Should contain key metrics
		if !containsString(s, "5") {
			t.Error("String should contain rotations count")
		}
	})
}
