// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

// Package inboundtest provides shared test utilities for the
// internal/ledger/inbound package and its sibling-package consumers.
// Kept as a separate package (rather than a *_test.go helper) so it is
// importable from tests in other packages like internal/consensus/adaptor.
package inboundtest

import (
	"sync"
	"time"
)

// FakeClock is an inbound.Clock whose Now is advanced explicitly by
// tests, so timeout behavior can be exercised without wall-clock waits.
// Goroutine-safe.
type FakeClock struct {
	mu  sync.Mutex
	now time.Time
}

// NewFakeClock returns a FakeClock anchored at t.
func NewFakeClock(t time.Time) *FakeClock {
	return &FakeClock{now: t}
}

// Now returns the fake's current time. Satisfies inbound.Clock.
func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

// Advance moves the fake forward by d.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}
