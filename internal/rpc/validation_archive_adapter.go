package rpc

import (
	"context"

	"github.com/LeJamon/goXRPLd/internal/rpc/types"
	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// ValidationArchiveAdapter projects relationaldb.ValidationRepository
// rows into the RPC-shaped types.ValidationArchiveLookup interface so
// internal/rpc/types stays free of any storage import. Read-only.
//
// The repository methods take a context.Context, but the
// types.ValidationArchiveLookup contract is context-free (handlers may
// be invoked from contexts where threading one through would force a
// signature change every time we add a new lookup). The adapter holds a
// background context internally and the relationaldb layer enforces its
// own statement timeouts; this matches how the SQL-row mappers in
// LedgerServiceAdapter work elsewhere in this package.
type ValidationArchiveAdapter struct {
	repo relationaldb.ValidationRepository
	ctx  context.Context
}

// Compile-time check that we satisfy the RPC contract.
var _ types.ValidationArchiveLookup = (*ValidationArchiveAdapter)(nil)

// NewValidationArchiveAdapter wraps a ValidationRepository for use by
// the RPC layer. Returns nil if repo is nil so callers can `if a !=
// nil` before assigning to Services.ValidationArchive.
func NewValidationArchiveAdapter(repo relationaldb.ValidationRepository) *ValidationArchiveAdapter {
	if repo == nil {
		return nil
	}
	return &ValidationArchiveAdapter{repo: repo, ctx: context.Background()}
}

func (a *ValidationArchiveAdapter) GetValidationsForLedger(ledgerSeq uint32) ([]types.ArchivedValidation, error) {
	rows, err := a.repo.GetValidationsForLedger(a.ctx, relationaldb.LedgerIndex(ledgerSeq))
	if err != nil {
		return nil, err
	}
	return projectRows(rows), nil
}

func (a *ValidationArchiveAdapter) GetValidationsByValidator(nodeKey []byte, limit int) ([]types.ArchivedValidation, error) {
	rows, err := a.repo.GetValidationsByValidator(a.ctx, nodeKey, limit)
	if err != nil {
		return nil, err
	}
	return projectRows(rows), nil
}

func (a *ValidationArchiveAdapter) GetValidationCount() (int64, error) {
	return a.repo.GetValidationCount(a.ctx)
}

// projectRows converts storage records to the RPC-facing DTO. Times go
// from time.Time → unix seconds (the RPC shape uses seconds since the
// Unix epoch; the relationaldb layer round-trips via XRPL epoch
// internally). The byte-slice fields (NodePubKey, Raw) are copied
// rather than aliased so a downstream JSON encode can never observe a
// mutation from the caller side.
func projectRows(rows []*relationaldb.ValidationRecord) []types.ArchivedValidation {
	if len(rows) == 0 {
		return nil
	}
	out := make([]types.ArchivedValidation, 0, len(rows))
	for _, r := range rows {
		if r == nil {
			continue
		}
		entry := types.ArchivedValidation{
			LedgerSeq: uint32(r.LedgerSeq),
			LedgerHash: r.LedgerHash,
			Flags:     r.Flags,
		}
		if !r.SignTime.IsZero() {
			entry.SignTimeS = r.SignTime.Unix()
		}
		if !r.SeenTime.IsZero() {
			entry.SeenTimeS = r.SeenTime.Unix()
		}
		if len(r.NodePubKey) > 0 {
			entry.NodePubKey = append([]byte(nil), r.NodePubKey...)
		}
		if len(r.Raw) > 0 {
			entry.Raw = append([]byte(nil), r.Raw...)
		}
		out = append(out, entry)
	}
	return out
}
