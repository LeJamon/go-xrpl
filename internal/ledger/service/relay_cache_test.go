package service

import (
	"testing"
	"time"
)

func TestServiceRelayCacheRetainsBlobAndUsesNewStatus(t *testing.T) {
	s := &Service{}
	hash := [32]byte{0xA1}
	blob := []byte{0x01, 0x02}
	s.rememberRelayTransaction(hash, blob, false)
	blob[0] = 0xFF

	got, included, deferred, ok := s.TransactionForRelay(hash)
	if !ok || included || deferred {
		t.Fatalf("TransactionForRelay() = (%x, %v, %v, %v), want retained cache record", got, included, deferred, ok)
	}
	if got[0] != 0x01 {
		t.Fatalf("retained blob was not copied: %x", got)
	}
}

func TestServiceRelayCacheIsBoundedAndExpires(t *testing.T) {
	s := &Service{relayTxCache: make(map[[32]byte]relayTxRecord)}
	for i := 0; i < relayTxCacheMaxEntries+1; i++ {
		var hash [32]byte
		hash[0] = byte(i >> 8)
		hash[1] = byte(i)
		s.rememberRelayTransaction(hash, []byte{byte(i)}, true)
	}
	if got := len(s.relayTxCache); got > relayTxCacheMaxEntries {
		t.Fatalf("relay cache size = %d, max %d", got, relayTxCacheMaxEntries)
	}

	hash := [32]byte{0xFE}
	s.rememberRelayTransaction(hash, []byte{0xAA}, true)
	s.relayTxCacheMu.Lock()
	record := s.relayTxCache[hash]
	record.seenAt = time.Now().Add(-relayTxCacheTTL - time.Second)
	s.relayTxCache[hash] = record
	s.relayTxCacheMu.Unlock()
	if _, _, _, ok := s.relayCacheGet(hash); ok {
		t.Fatal("expired relay cache record was returned")
	}
	if got := len(s.relayTxCacheOrder) - s.relayTxCacheHead; got > relayTxCacheMaxEntries {
		t.Fatalf("active order entries = %d, max %d", got, relayTxCacheMaxEntries)
	}
}

func TestServiceRelayCacheIsByteBounded(t *testing.T) {
	s := &Service{
		relayTxCache:      make(map[[32]byte]relayTxRecord),
		relayTxCacheLimit: 4,
	}
	first := [32]byte{0x01}
	second := [32]byte{0x02}
	s.rememberRelayTransaction(first, []byte{1, 2, 3}, false)
	s.rememberRelayTransaction(second, []byte{4, 5, 6}, false)

	if s.relayTxCacheBytes > s.relayTxCacheLimit {
		t.Fatalf("relay cache bytes = %d, limit %d", s.relayTxCacheBytes, s.relayTxCacheLimit)
	}
	if _, _, _, ok := s.relayCacheGet(first); ok {
		t.Fatal("oldest record survived byte-bound eviction")
	}
	if _, _, _, ok := s.relayCacheGet(second); !ok {
		t.Fatal("newest record was evicted despite fitting the byte bound")
	}

	s.rememberRelayTransaction(second, []byte{7, 8}, true)
	if s.relayTxCacheBytes != 2 {
		t.Fatalf("replacement bytes = %d, want 2", s.relayTxCacheBytes)
	}
}

func TestServiceRelayCacheCompactsExpiredOrderTombstones(t *testing.T) {
	s := &Service{relayTxCache: make(map[[32]byte]relayTxRecord)}
	anchor := [32]byte{0xFF}
	s.rememberRelayTransaction(anchor, []byte{0x01}, false)
	for i := 0; i < 2*relayTxCacheMaxEntries+16; i++ {
		var hash [32]byte
		hash[0] = byte(i >> 16)
		hash[1] = byte(i >> 8)
		hash[2] = byte(i)
		s.rememberRelayTransaction(hash, []byte{0x02}, false)
		s.relayTxCacheMu.Lock()
		record := s.relayTxCache[hash]
		record.seenAt = time.Now().Add(-relayTxCacheTTL - time.Second)
		s.relayTxCache[hash] = record
		s.relayTxCacheMu.Unlock()
		_, _, _, _ = s.relayCacheGet(hash)
	}

	if got := len(s.relayTxCacheOrder) - s.relayTxCacheHead; got > 2*relayTxCacheMaxEntries {
		t.Fatalf("active order entries = %d, max %d", got, 2*relayTxCacheMaxEntries)
	}
}
