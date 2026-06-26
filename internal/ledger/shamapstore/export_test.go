package shamapstore

import "context"

// NotifyForTest drives a single rotation decision synchronously, bypassing the
// background worker so tests can assert deletion effects deterministically.
func (r *Rotator) NotifyForTest(validatedSeq uint32) {
	r.maybeRotate(context.Background(), validatedSeq)
}
