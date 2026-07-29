package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

func TestAmendmentVoteRepository_RoundTrip(t *testing.T) {
	rm := setupTestDB(t)
	repo := rm.Amendment()
	ctx := context.Background()
	alpha := strings.Repeat("A", 64)
	beta := strings.Repeat("B", 64)

	// Empty to start.
	got, err := repo.LoadAmendmentVotes(ctx)
	if err != nil {
		t.Fatalf("load empty: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected no votes, got %d", len(got))
	}

	// Save an upvote and a veto.
	if err := repo.SaveAmendmentVote(ctx, relationaldb.AmendmentVoteRecord{Amendment: alpha, Name: "Alpha", Vetoed: false}); err != nil {
		t.Fatalf("save upvote: %v", err)
	}
	if err := repo.SaveAmendmentVote(ctx, relationaldb.AmendmentVoteRecord{Amendment: beta, Name: "Beta", Vetoed: true}); err != nil {
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
	if byID[alpha].Vetoed || byID[alpha].Name != "Alpha" {
		t.Fatalf("alpha roundtrip wrong: %+v", byID[alpha])
	}
	if !byID[beta].Vetoed {
		t.Fatalf("beta should be vetoed: %+v", byID[beta])
	}

	// Upsert: flip alpha to vetoed.
	if err := repo.SaveAmendmentVote(ctx, relationaldb.AmendmentVoteRecord{Amendment: alpha, Name: "Alpha", Vetoed: true}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 2 {
		t.Fatalf("upsert must not duplicate; got %d rows", len(got))
	}

	// Delete beta.
	if err := repo.DeleteAmendmentVote(ctx, beta); err != nil {
		t.Fatalf("delete: %v", err)
	}
	got, _ = repo.LoadAmendmentVotes(ctx)
	if len(got) != 1 || got[0].Amendment != alpha {
		t.Fatalf("after delete expected only alpha, got %+v", got)
	}
}
