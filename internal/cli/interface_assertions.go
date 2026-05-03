package cli

import (
	"github.com/LeJamon/goXRPLd/internal/peermanagement"
	"github.com/LeJamon/goXRPLd/internal/rpc/types"
)

// Compile-time interface checks for cross-package wiring done in
// server.go. The implementing types live in packages that intentionally
// don't depend on internal/rpc/types (peermanagement is a lower layer);
// asserting here keeps drift detectable without forcing the layering
// dependency upward.
var (
	_ types.PeerSource = (*peermanagement.Overlay)(nil)
)
