package shamap

import "context"

// OverlayFamily layers a writable overlay Family over a read-only base Family,
// mirroring geth's disk-layer / diff-layer split. Fetch consults the overlay
// first and falls back to the base; StoreBatch writes only to the overlay, so
// the base is never mutated and can back many workers read-only and be shared
// across a node's checkpoint cache.
//
// In the mainnet-replay worker this gives the "shared read-only base checkpoint
// + per-worker copy-on-write overlay" model: the base holds the immutable
// checkpoint state and the overlay holds only a segment's mutations.
type OverlayFamily struct {
	base    Family
	overlay Family
}

// NewOverlayFamily returns a Family that reads overlay-then-base and writes
// only to overlay. Both must be non-nil.
func NewOverlayFamily(base, overlay Family) *OverlayFamily {
	if base == nil || overlay == nil {
		panic("shamap: NewOverlayFamily requires non-nil base and overlay")
	}
	return &OverlayFamily{base: base, overlay: overlay}
}

// Fetch returns the node from the overlay if present, otherwise from the base.
// Returns (nil, nil) when absent from both, matching the Family contract.
func (f *OverlayFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	data, err := f.overlay.Fetch(ctx, hash)
	if err != nil {
		return nil, err
	}
	// A present node always carries bytes, so a zero-length overlay hit means
	// absent: fall through to the base rather than shadowing it with empty data.
	if len(data) > 0 {
		return data, nil
	}
	return f.base.Fetch(ctx, hash)
}

// StoreBatch persists nodes to the overlay only, leaving the shared base intact.
func (f *OverlayFamily) StoreBatch(ctx context.Context, entries []FlushEntry) error {
	return f.overlay.StoreBatch(ctx, entries)
}
