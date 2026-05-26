package relationaldb

import "context"

// AmendmentVoteRecord is one operator amendment-vote preference: the node's
// persisted decision to upvote or veto a single amendment. Mirrors a row of
// rippled's wallet.db FeatureVotes table. The preference survives restarts so
// runtime `feature <amendment> reject/accept` overrides config until changed.
type AmendmentVoteRecord struct {
	// Amendment is the 64-char uppercase hex amendment ID.
	Amendment string
	// Name is the amendment's registry name (informational; may be empty for
	// amendments unknown to this build).
	Name string
	// Vetoed is true for a veto (down vote), false for an upvote (up vote).
	Vetoed bool
}

// AmendmentVoteRepository persists operator amendment-vote preferences. Writes
// are keyed by amendment ID (one preference per amendment); Save upserts the
// latest preference. Backends guarantee idempotent writes.
type AmendmentVoteRepository interface {
	// LoadAmendmentVotes returns every persisted operator preference.
	LoadAmendmentVotes(ctx context.Context) ([]*AmendmentVoteRecord, error)

	// SaveAmendmentVote upserts one operator preference (keyed by Amendment).
	SaveAmendmentVote(ctx context.Context, rec *AmendmentVoteRecord) error

	// DeleteAmendmentVote removes the preference for the given amendment ID,
	// returning it to the registry default. No-op when absent.
	DeleteAmendmentVote(ctx context.Context, amendment string) error
}
