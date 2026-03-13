package manager

import (
	"context"

	"github.com/LeJamon/goXRPLd/internal/ledger"
)

type LedgerStorage interface {
	//StoreLedger Store complete ledger with all nodes
	StoreLedger(ctx context.Context, ledger *ledger.Ledger) error

	//GetLedger Retrieve ledger by sequence
	GetLedger(ctx context.Context, seq uint32) (*ledger.Ledger, error)

	//GetLedgerByHash Retrieve ledger by sequence
	GetLedgerByHash(ctx context.Context, hash [32]byte) (*ledger.Ledger, error)

	// Track completeness
	MarkLedgerComplete(ctx context.Context, seq uint32) error
	IsLedgerComplete(ctx context.Context, seq uint32) (bool, error)
	GetCompleteLedgerRange() (min, max uint32, err error)
}
