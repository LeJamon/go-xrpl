// Copyright (c) 2024-2026. All rights reserved.
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package inbound

import "time"

// Clock abstracts time.Now so tests can drive timeout behavior
// deterministically without wall-clock waits. Production callers get
// realClock by default via the constructors that accept no clock.
type Clock interface {
	Now() time.Time
}

// realClock reads the wall clock. Used by default in production paths.
type realClock struct{}

// Now returns the current wall-clock time.
func (realClock) Now() time.Time { return time.Now() }

// SystemClock is the shared real-time Clock used by the default
// constructors in this package. Exposed so a caller that already has
// a DI point for clocks can plug this in explicitly.
var SystemClock Clock = realClock{}
