package sqlite

import (
	"context"
	"testing"

	"github.com/LeJamon/goXRPLd/storage/relationaldb"
)

func TestAmendmentVoteRepository_RoundTrip(t *testing.T) {
	rm := setupTestDB(t)
	repo := rm.Amendment()
	ctx := context.Background()

	// Empty to start.
	got, err := repo.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no votes, got %d", len(got))
	}

	// Save an upvote and a veto.
	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "AA", Name: "Alpha", Vetoed: false}); err != nil {
		t.Fatalf("save upvote: %v", err)
	}
	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "BB", Name: "Beta", Vetoed: true}); err != nil {
		t.Fatalf("save veto: %v", err)
	}

	got, err = repo.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 votes, got %d", len(got))
	}
	byID := map[string]*relationaldb.AmendmentVoteRecord{}
	for _, r := range got {
		byID[r.Amendment] = r
	}
	if byID["AA"].Vetoed || byID["AA"].Name != "Alpha" {
		t.Fatalf("AA roundtrip wrong: %+v", byID["AA"])
	}
	if !byID["BB"].Vetoed {
		t.Fatalf("BB should be vetoed: %+v", byID["BB"])
	}

	// Upsert: flip AA to vetoed.
	if err := repo.SaveAmendmentVote(ctx, &relationaldb.AmendmentVoteRecord{Amendment: "AA", Name: "Alpha", Vetoed: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 2 {
		t.Fatalf("upsert must not duplicate; got %d rows", len(got))
	}

	// Delete BB.
	if err := repo.DeleteAmendmentVote(ctx, "BB"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 1 || got[0].Amendment != "AA" {
		t.Fatalf("after delete expected only AA, got %+v", got)
	}
}
