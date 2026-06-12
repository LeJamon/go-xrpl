package ledger

import (
	"bytes"
	"context"
)

// PageState walks the ledger's state entries in key order, invoking visit
// for each entry. When hasMarker is set the walk resumes strictly after
// marker (the marker entry is never re-emitted); when hasEnd is set it stops
// after the entry equal to endMarker (the bound is inclusive). At most limit
// entries are visited. When the walk is cut short by limit, more is true and
// next holds the last visited key, which the caller uses as the next page's
// marker.
//
// Both the resume point and the end bound are computed via the state map's
// upper_bound, so an absent or synthetic marker — for example a nextKey-1
// value that is not itself an entry — resumes at the next greater key rather
// than truncating the page to empty.
func (l *Ledger) PageState(
	ctx context.Context,
	marker [32]byte, hasMarker bool,
	endMarker [32]byte, hasEnd bool,
	limit int,
	visit func(key [32]byte, data []byte),
) (next [32]byte, more bool, err error) {
	l.mu.RLock()
	defer l.mu.RUnlock()

	start := marker
	if !hasMarker {
		start = [32]byte{}
	}

	count := 0
	it := l.stateMap.UpperBound(start)
	for ; it.Valid(); it.Next() {
		if err := ctx.Err(); err != nil {
			return next, false, err
		}
		item := it.Item()
		key := item.Key()
		if hasEnd && bytes.Compare(key[:], endMarker[:]) > 0 {
			return next, false, nil
		}
		if count == limit {
			return next, true, nil
		}
		visit(key, item.Data())
		next = key
		count++
	}
	if err := it.Err(); err != nil {
		return next, false, err
	}
	return next, false, nil
}
