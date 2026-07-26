package shamap

import (
	"context"
	"testing"
)

func TestSme_Has(t *testing.T) {
	sm := New(TypeState)
	k := sme_keyFromByte(0x10)
	found, err := sm.Has(k)
	if err != nil || found {
		t.Errorf("Has on empty: err=%v found=%v", err, found)
	}
	if err := sm.Put(k, sme_data12(1)); err != nil {
		t.Fatalf("Put: %v", err)
	}
	found, err = sm.Has(k)
	if err != nil || !found {
		t.Errorf("Has after Put: err=%v found=%v", err, found)
	}
	k2 := sme_keyFromByte(0x20)
	found, err = sm.Has(k2)
	if err != nil || found {
		t.Errorf("Has absent: err=%v found=%v", err, found)
	}
}

func TestSme_GetEmptyMap(t *testing.T) {
	sm := New(TypeState)
	item, ok, err := sm.Get(sme_keyFromByte(0xAA))
	if err != nil || ok || item != nil {
		t.Errorf("Get on empty: item=%v ok=%v err=%v", item, ok, err)
	}
}

func TestSme_ForEachCtxCancelled(t *testing.T) {
	sm := New(TypeState)
	for i := 0; i < 10; i++ {
		if err := sm.Put(sme_keyFromByte(byte(i+1)), sme_data12(byte(i))); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := sm.ForEachCtx(ctx, func(*Item) bool { return true })
	if err == nil {
		t.Error("ForEachCtx with cancelled context should return error")
	}
}

func TestSme_ForEachEarlyStop(t *testing.T) {
	sm := New(TypeState)
	for i := byte(1); i <= 5; i++ {
		if err := sm.Put(sme_keyFromByte(i), sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	count := 0
	if err := sm.ForEach(func(*Item) bool {
		count++
		return false
	}); err != nil {
		t.Fatalf("ForEach with fn returning false must not error: %v", err)
	}
	if count != 1 {
		t.Errorf("ForEach early-stop visited %d items, want exactly 1", count)
	}
}

func TestSme_SizeMutableNoCaching(t *testing.T) {
	sm := New(TypeState)
	if sz := sm.Size(); sz != 0 {
		t.Errorf("Size empty mutable = %d, want 0", sz)
	}
	for i := byte(1); i <= 3; i++ {
		if err := sm.Put(sme_keyFromByte(i), sme_data12(i)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if sz := sm.Size(); sz != 3 {
		t.Errorf("Size mutable = %d, want 3", sz)
	}
}
