package nodestore

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/storage/kvstore"
)

func testHash(data []byte) Hash256 {
	return Hash256(sha256.Sum256(data))
}

func testNode(nodeType NodeType, data []byte, ledgerSeq uint32) *Node {
	payload := append([]byte(nil), data...)
	return &Node{
		Type:      nodeType,
		Hash:      testHash(payload),
		Data:      payload,
		LedgerSeq: ledgerSeq,
	}
}

func testDatabase(t testing.TB, store kvstore.KeyValueStore, config DatabaseConfig) *KVDatabase {
	t.Helper()
	database, err := NewKVDatabase(store, config)
	if err != nil {
		t.Fatalf("NewKVDatabase: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	return database
}

func noCacheConfig() DatabaseConfig {
	return DatabaseConfig{}
}

func positiveCacheConfig(size int) DatabaseConfig {
	return DatabaseConfig{
		PositiveCache: CacheConfig{
			Enabled:    true,
			MaxEntries: size,
			TTL:        testCacheTTL,
		},
	}
}

const testCacheTTL = time.Hour
