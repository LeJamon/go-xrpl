package invariants

import "github.com/LeJamon/go-xrpl/internal/ledger/state"

// imageCache memoises the decoded forms of one entry's images (Before, After,
// DeleteFinal) for a single CheckInvariants call, so the checkers inspecting an
// image share one decode. The images are not modified while invariants run, and
// checkers only read what they decode: a value obtained here must never be
// mutated, because every other checker sees the same value.
type imageCache [3]decodedImage

type decodedImage struct {
	account decodeResult[*state.AccountRoot]
	line    decodeResult[*state.RippleState]
	model   decodeResult[decodedLendingEntry]
}

type decodeResult[T any] struct {
	value T
	err   error
	done  bool
}

func (r *decodeResult[T]) get(data []byte, decode func([]byte) (T, error)) (T, error) {
	if !r.done {
		r.value, r.err = decode(data)
		r.done = true
	}
	return r.value, r.err
}

// withImageCaches returns a copy of entries in which each entry carries its own
// empty image cache.
func withImageCaches(entries []InvariantEntry) []InvariantEntry {
	cached := make([]InvariantEntry, len(entries))
	caches := make([]imageCache, len(entries))
	for i, e := range entries {
		e.images = &caches[i]
		cached[i] = e
	}
	return cached
}

// image returns the cache slot of data when data is one of e's own images,
// identified by its backing array and length, and nil otherwise.
func (e InvariantEntry) image(data []byte) *decodedImage {
	if e.images == nil || len(data) == 0 {
		return nil
	}
	for i, img := range [...][]byte{e.Before, e.After, e.DeleteFinal} {
		if len(img) == len(data) && &img[0] == &data[0] {
			return &e.images[i]
		}
	}
	return nil
}

func (e InvariantEntry) accountRoot(data []byte) (*state.AccountRoot, error) {
	if img := e.image(data); img != nil {
		return img.account.get(data, state.ParseAccountRoot)
	}
	return state.ParseAccountRoot(data)
}

func (e InvariantEntry) rippleState(data []byte) (*state.RippleState, error) {
	if img := e.image(data); img != nil {
		return img.line.get(data, state.ParseRippleState)
	}
	return state.ParseRippleState(data)
}

func (e InvariantEntry) model(data []byte) (decodedLendingEntry, error) {
	if img := e.image(data); img != nil {
		return img.model.get(data, decodeEntry)
	}
	return decodeEntry(data)
}
