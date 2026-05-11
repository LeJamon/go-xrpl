package service

import (
	"github.com/LeJamon/goXRPLd/internal/ledger/openledger"
)

// pendingTx is an in-package alias for openledger.PendingTx. The
// underlying type moved to internal/ledger/openledger as part of issue
// #407 Task 1, but service-internal call sites keep the short name and
// lowercase field accessors.
type pendingTx = openledger.PendingTx

// canonicalSort, parsePendingTx, computeSalt are thin wrappers around
// the openledger helpers so that the existing service.go callers don't
// have to change. See internal/ledger/openledger/types.go for full docs.
func canonicalSort(txs []pendingTx, salt [32]byte) { openledger.CanonicalSort(txs, salt) }

func parsePendingTx(blob []byte) (pendingTx, error) { return openledger.ParsePendingTx(blob) }

func computeSalt(txs []pendingTx) [32]byte { return openledger.ComputeSalt(txs) }
