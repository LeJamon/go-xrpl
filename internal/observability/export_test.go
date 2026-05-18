package observability

import "time"

// resetForTest stops any running sampler, waits up to 2*SamplerInterval
// for it to exit, then clears the published value, sample count, and
// done channel so a fresh sampler can be started in the next test.
//
// Tests that start a sampler MUST cancel its context (via t.Cleanup
// or defer cancel()) before the test returns; resetForTest will then
// observe the closed done channel immediately.
func resetForTest() {
	samplerMu.Lock()
	done := samplerDone
	samplerMu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-time.After(2 * SamplerInterval):
			// The prior test leaked its sampler. We can't force-cancel
			// here, so accept the leak; the publishedNs atomic is
			// last-write-wins so callers should still see their own
			// stores within their own time slice.
		}
	}
	samplerMu.Lock()
	samplerDone = nil
	samplerMu.Unlock()
	publishedNs.Store(0)
	samplesCount.Store(0)
	// Replace the default collector with a fresh MemoryCollector so
	// tests get a clean baseline. Tests that installed a custom
	// collector via SetCollector should re-install it after calling
	// resetForTest.
	SetCollector(NewMemoryCollector())
}

// samplerDoneForTest returns the active sampler's done channel, or
// nil if no sampler is running. A test that calls cancel() on its
// context can then receive on this channel to deterministically wait
// for the goroutine to exit.
func samplerDoneForTest() <-chan struct{} {
	samplerMu.Lock()
	defer samplerMu.Unlock()
	return samplerDone
}

// samplesCountForTest returns the number of iterations the sampler
// loop has completed since the last resetForTest. Used by cadence
// tests (rippled's testSampleOngoing analog).
func samplesCountForTest() int64 {
	return samplesCount.Load()
}
