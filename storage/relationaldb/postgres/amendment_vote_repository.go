package postgres

import (
	"context"
	"database/sql"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

// AmendmentVoteRepository is the PostgreSQL-backed store of operator
// amendment-vote preferences (the feature_votes table).
type AmendmentVoteRepository struct {
	db *sql.DB
}

// Compile-time interface check.
var _ relationaldb.AmendmentVoteRepository = (*AmendmentVoteRepository)(nil)

// NewAmendmentVoteRepository creates a PostgreSQL amendment-vote repository.
func NewAmendmentVoteRepository(db *sql.DB) *AmendmentVoteRepository {
	return &AmendmentVoteRepository{db: db}
}

// LoadAmendmentVotes returns all recorded operator amendment-vote preferences.
func (r *AmendmentVoteRepository) LoadAmendmentVotes(ctx context.Context) ([]*relationaldb.AmendmentVoteRecord, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT amendment, name, vetoed FROM feature_votes`)
	if err != nil {
		return nil, relationaldb.NewQueryError("amendment_vote_load", "failed to query feature votes", err)
	}
	defer rows.Close()

	var result []*relationaldb.AmendmentVoteRecord
	for rows.Next() {
		var rec relationaldb.AmendmentVoteRecord
		if err := rows.Scan(&rec.Amendment, &rec.Name, &rec.Vetoed); err != nil {
			return nil, relationaldb.NewQueryError("amendment_vote_load", "failed to scan row", err)
		}
		result = append(result, &rec)
	}
	if err := rows.Err(); err != nil {
		return nil, relationaldb.NewQueryError("amendment_vote_load", "row iteration error", err)
	}
	return result, nil
}

// SaveAmendmentVote inserts or updates an amendment-vote preference (upsert on amendment).
func (r *AmendmentVoteRepository) SaveAmendmentVote(ctx context.Context, rec *relationaldb.AmendmentVoteRecord) error {
	if rec == nil {
		return relationaldb.NewDataError("amendment_vote_save", "nil record", nil)
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO feature_votes (amendment, name, vetoed) VALUES ($1, $2, $3)
		ON CONFLICT (amendment) DO UPDATE SET name = excluded.name, vetoed = excluded.vetoed
	`, rec.Amendment, rec.Name, rec.Vetoed)
	if err != nil {
		return relationaldb.NewQueryError("amendment_vote_save", "failed to upsert feature vote", err)
	}
	return nil
}

// DeleteAmendmentVote removes the vote preference for the given amendment.
func (r *AmendmentVoteRepository) DeleteAmendmentVote(ctx context.Context, amendment string) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM feature_votes WHERE amendment = $1`, amendment)
	if err != nil {
		return relationaldb.NewQueryError("amendment_vote_delete", "failed to delete feature vote", err)
	}
	return nil
}
