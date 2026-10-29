package service

import (
	"context"
	"errors"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger"
)

// ErrConsensusPublicationStale reports that a consensus-built successor no
// longer matches authenticated validation evidence at its sequence. The
// caller may retry the round after adopting the validated network ledger.
var ErrConsensusPublicationStale = errors.New("consensus publication stale")

// AcceptConsensusResultWithPublicationGuard checks eligibility at the final
// frontier fence. The returned lease is released after closed-frontier assignment,
// before persistence; the service lock hides the remaining successor transition.
func (s *Service) AcceptConsensusResultWithPublicationGuard(
	ctx context.Context,
	parent *ledger.Ledger,
	txBlobs, disputedBlobs [][]byte,
	closeTime time.Time,
	closeTimeCorrect bool,
	guard func(*ledger.Ledger) (func(), error),
) (uint32, error) {
	return s.acceptConsensusResult(ctx, parent, txBlobs, disputedBlobs, closeTime, closeTimeCorrect, guard)
}
