// Package cmdexit defines the sentinel error a CLI command returns to request
// a non-zero process exit without an additional error line. Commands return it
// when they have already printed their own failure report — a replay
// divergence or a state-compare diff — so the only thing left is the exit code.
// Returning an error (rather than calling os.Exit) lets deferred cleanup run.
// Execute() maps it to exit code 1 and suppresses the "Error:" prefix.
package cmdexit

import "errors"

// ErrReported signals "exit with a non-zero status; the failure has already
// been reported to the user." It carries no message of its own.
var ErrReported = errors.New("command reported a failure")
