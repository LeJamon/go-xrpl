package inbound

import "time"

// progressTimeout bounds waits for something that must happen. A passing test
// returns as soon as it does, so the bound is generous enough for loaded CI.
const progressTimeout = 15 * time.Second
