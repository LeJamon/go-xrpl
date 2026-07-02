package sigcache

import (
	"testing"
	"time"
)

func id(b byte) [32]byte {
	var x [32]byte
	x[0] = b
	return x
}

func TestCache_MissThenHit(t *testing.T) {
	c := NewCache(16, time.Hour, nil)
	if c.Has(id(1)) {
		t.Fatal("fresh cache must miss")
	}
	c.Add(id(1))
	if !c.Has(id(1)) {
		t.Fatal("added id must hit")
	}
	if c.Has(id(2)) {
		t.Fatal("unrelated id must miss")
	}
}

// A verdict survives one size-driven rotation (moves to prev), evicted after two.
func TestCache_SizeRotationEviction(t *testing.T) {
	c := NewCache(4, time.Hour, nil)
	c.Add(id(100))
	// Fill the current generation to force a rotation; id(100) moves to prev.
	for i := byte(0); i < 4; i++ {
		c.Add(id(i))
	}
	if !c.Has(id(100)) {
		t.Fatal("id must survive one rotation (still in prev generation)")
	}
	// Force a second rotation; id(100) is now dropped entirely.
	for i := byte(10); i < 14; i++ {
		c.Add(id(i))
	}
	if c.Has(id(100)) {
		t.Fatal("id must be evicted after two rotations")
	}
}

// An entry ages out once the TTL elapses without size pressure (injected clock).
func TestCache_TTLRotation(t *testing.T) {
	now := time.Unix(0, 0)
	c := NewCache(1<<20, time.Minute, func() time.Time { return now })
	c.Add(id(1))
	if !c.Has(id(1)) {
		t.Fatal("just-added id must hit")
	}
	// One TTL: a lookup rotates cur→prev, id(1) still reachable via prev.
	now = now.Add(time.Minute)
	if !c.Has(id(1)) {
		t.Fatal("id must survive the first TTL rotation via prev generation")
	}
	// Second TTL past the first rotation: id(1) is dropped.
	now = now.Add(time.Minute)
	if c.Has(id(1)) {
		t.Fatal("id must age out after two TTL rotations")
	}
}

func TestCache_Reset(t *testing.T) {
	c := NewCache(16, time.Hour, nil)
	c.Add(id(1))
	c.Reset()
	if c.Has(id(1)) {
		t.Fatal("Reset must clear the cache")
	}
}

// Exercises the process-wide accessors used by the engine.
func TestGlobal_VerifiedAndReset(t *testing.T) {
	Reset()
	if Verified(id(1)) {
		t.Fatal("miss on a fresh global cache")
	}
	MarkVerified(id(1))
	if !Verified(id(1)) {
		t.Fatal("MarkVerified must be observable via Verified")
	}
	Reset()
	if Verified(id(1)) {
		t.Fatal("Reset must clear the global cache")
	}
}
