package shamap

import (
	"context"
	"errors"
	"testing"
)

type familyAccessBase struct {
	data  []byte
	err   error
	calls int
}

func (f *familyAccessBase) Fetch(ctx context.Context, _ [32]byte) ([]byte, error) {
	f.calls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.data, f.err
}

func (*familyAccessBase) StoreBatch(context.Context, []FlushEntry) error {
	return nil
}

type uncomparableFamily []byte

func (f uncomparableFamily) Fetch(context.Context, [32]byte) ([]byte, error) {
	return f, nil
}

func (uncomparableFamily) StoreBatch(context.Context, []FlushEntry) error {
	return nil
}

type familyAccessDurable struct {
	*familyAccessBase
	durableData  []byte
	durableErr   error
	durableCalls int
}

func (f *familyAccessDurable) FetchDurable(ctx context.Context, _ [32]byte) ([]byte, error) {
	f.durableCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.durableData, f.durableErr
}

type familyAccessPlacement struct {
	*familyAccessDurable
	placementData  []byte
	placementErr   error
	placementCalls int
}

func (f *familyAccessPlacement) FetchForNodePlacement(ctx context.Context, _ [32]byte) ([]byte, error) {
	f.placementCalls++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return f.placementData, f.placementErr
}

func TestFamilyAccessPreferDurable(t *testing.T) {
	hash := [32]byte{1}
	t.Run("ordinary family is durable", func(t *testing.T) {
		family := &familyAccessBase{data: []byte{1}}
		data, durable, err := bindFamily(family).fetchPreferDurable(t.Context(), hash)
		if err != nil || len(data) != 1 || !durable || family.calls != 1 {
			t.Fatalf("fetch = (%x, %v, %v), calls = %d", data, durable, err, family.calls)
		}
	})
	t.Run("durable hit avoids fallback", func(t *testing.T) {
		family := &familyAccessDurable{
			familyAccessBase: &familyAccessBase{data: []byte{2}},
			durableData:      []byte{1},
		}
		data, durable, err := bindFamily(family).fetchPreferDurable(t.Context(), hash)
		if err != nil || len(data) != 1 || !durable || family.durableCalls != 1 || family.calls != 0 {
			t.Fatalf("fetch = (%x, %v, %v), calls = durable:%d fallback:%d", data, durable, err, family.durableCalls, family.calls)
		}
	})
	t.Run("durable miss uses non-durable fallback", func(t *testing.T) {
		family := &familyAccessDurable{familyAccessBase: &familyAccessBase{data: []byte{2}}}
		data, durable, err := bindFamily(family).fetchPreferDurable(t.Context(), hash)
		if err != nil || len(data) != 1 || durable || family.durableCalls != 1 || family.calls != 1 {
			t.Fatalf("fetch = (%x, %v, %v), calls = durable:%d fallback:%d", data, durable, err, family.durableCalls, family.calls)
		}
	})
	t.Run("durable error does not fall back", func(t *testing.T) {
		wantErr := errors.New("durable read")
		family := &familyAccessDurable{
			familyAccessBase: &familyAccessBase{data: []byte{2}},
			durableErr:       wantErr,
		}
		_, _, err := bindFamily(family).fetchPreferDurable(t.Context(), hash)
		if !errors.Is(err, wantErr) || family.durableCalls != 1 || family.calls != 0 {
			t.Fatalf("error = %v, calls = durable:%d fallback:%d", err, family.durableCalls, family.calls)
		}
	})
}

func TestFamilyAccessPlacementAndCancellation(t *testing.T) {
	hash := [32]byte{1}
	family := &familyAccessPlacement{
		familyAccessDurable: &familyAccessDurable{
			familyAccessBase: &familyAccessBase{data: []byte{3}},
			durableData:      []byte{2},
		},
		placementData: []byte{1},
	}
	data, err := bindFamily(family).fetchForPlacement(t.Context(), hash)
	if err != nil || len(data) != 1 || family.placementCalls != 1 || family.durableCalls != 0 || family.calls != 0 {
		t.Fatalf("placement fetch = (%x, %v), calls = placement:%d durable:%d fallback:%d", data, err, family.placementCalls, family.durableCalls, family.calls)
	}

	fallback := &familyAccessDurable{
		familyAccessBase: &familyAccessBase{data: []byte{3}},
		durableData:      []byte{2},
	}
	data, err = bindFamily(fallback).fetchForPlacement(t.Context(), hash)
	if err != nil || len(data) != 1 || data[0] != 2 || fallback.durableCalls != 1 || fallback.calls != 0 {
		t.Fatalf("placement fallback = (%x, %v), calls = durable:%d regular:%d", data, err, fallback.durableCalls, fallback.calls)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, err = bindFamily(family).fetchPreferDurable(ctx, hash)
	if !errors.Is(err, context.Canceled) || family.durableCalls != 1 || family.calls != 0 {
		t.Fatalf("cancelled fetch error = %v, calls = durable:%d fallback:%d", err, family.durableCalls, family.calls)
	}
}

func TestSetFamilyRefreshesCapabilities(t *testing.T) {
	first := &familyAccessPlacement{
		familyAccessDurable: &familyAccessDurable{familyAccessBase: &familyAccessBase{}},
	}
	sm, err := NewBacked(TypeState, first)
	if err != nil {
		t.Fatalf("NewBacked: %v", err)
	}
	if sm.backing.access.durable == nil || sm.backing.access.placement == nil {
		t.Fatal("initial capabilities were not bound")
	}

	second := &familyAccessBase{data: []byte{2}}
	sm.SetFamily(second)
	data, err := sm.backing.access.fetch(t.Context(), [32]byte{})
	if err != nil || len(data) != 1 || data[0] != 2 || second.calls != 1 {
		t.Fatalf("refreshed family fetch = (%x, %v), calls = %d", data, err, second.calls)
	}
	if sm.backing.access.durable != nil || sm.backing.access.placement != nil {
		t.Fatal("stale optional capabilities survived SetFamily")
	}

	uncomparable := uncomparableFamily{1}
	sm.SetFamily(uncomparable)
	data, err = sm.backing.access.fetch(t.Context(), [32]byte{})
	if err != nil || len(data) != 1 {
		t.Fatalf("uncomparable family fetch = (%x, %v)", data, err)
	}
}
