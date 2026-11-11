package service

import (
	"context"
	"errors"
	"testing"

	"github.com/LeJamon/go-xrpl/shamap/backend"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/require"
)

type failOnceRepositories struct {
	relationaldb.RepositoryManager
	failSeq relationaldb.LedgerIndex
	failed  bool
}

func (r *failOnceRepositories) PersistValidatedLedger(ctx context.Context, validated relationaldb.ValidatedLedger) error {
	if !r.failed && validated.Ledger.Sequence == r.failSeq {
		r.failed = true
		return errors.New("injected relational failure")
	}
	return r.RepositoryManager.PersistValidatedLedger(ctx, validated)
}

// A ledger whose relational index failed to persist must not end up below the
// durable validated tip without its index: restart resumes from the tip and
// nothing re-indexes the skipped ledger.
func TestRelationalPersistFailureLeavesNoUnindexedLedgerBelowTip(t *testing.T) {
	t.Skip("known gap (#2192): a failed persist is logged, never retried, and the next ledger advances the durable tip past it")

	ctx := context.Background()
	db := newTestNodeStore(t, 10_000)
	repositories := newTestRepositories(t, ctx)
	relational := &failOnceRepositories{RepositoryManager: repositories}
	cfg := DefaultConfig()
	cfg.NodeStore = db
	cfg.SHAMapFamily = backend.New(db)
	cfg.RelationalDB = relational
	svc, err := New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	relational.failSeq = relationaldb.LedgerIndex(svc.GetClosedLedgerIndex() + 1)
	failed, err := svc.AcceptLedger(ctx)
	require.NoError(t, err)
	require.True(t, relational.failed)
	_, err = svc.AcceptLedger(ctx)
	require.NoError(t, err)
	svc.FlushPersists()

	tip, err := db.Fetch(ctx, validatedTipKey)
	require.NoError(t, err)
	require.NotNil(t, tip)
	info, err := repositories.Ledger().GetLedgerInfoBySeq(ctx, relationaldb.LedgerIndex(failed))
	indexed := err == nil && info != nil
	require.Truef(t, indexed || tip.LedgerSeq < failed,
		"durable tip %d is above ledger %d, which has no relational index", tip.LedgerSeq, failed)
}
