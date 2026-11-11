package node

import (
	xrplgrpc "github.com/LeJamon/go-xrpl/internal/grpc"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// Compile-time interface checks for cross-package wiring done in node.go.
// The implementing types live in packages that intentionally
// don't depend on internal/rpc/types (peermanagement is a lower layer);
// asserting here keeps drift detectable without forcing the layering
// dependency upward.
var (
	_ types.PeerSource = (*peermanagement.Overlay)(nil)
)

// The ledger service is consumed through role interfaces declared by each
// consumer; a signature change breaks the build here.
var (
	_ xrplgrpc.LedgerLookup = (*service.Service)(nil)
	_ rpcadapter.Backend    = (*service.Service)(nil)
	_ ledgerInfoService     = (*service.Service)(nil)
	_ ledgerCleanerService  = (*service.Service)(nil)
)
