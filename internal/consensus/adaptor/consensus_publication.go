package adaptor

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
)

func consensusPublicationGuard(historian consensus.ValidationHistorian, candidate *ledger.Ledger) (func(), error) {
	if candidate == nil {
		return nil, nil
	}
	return validationPublicationGuard(historian, consensus.LedgerID(candidate.Hash()), candidate.Sequence())
}

func validationPublicationGuard(historian consensus.ValidationHistorian, candidateID consensus.LedgerID, seq uint32) (func(), error) {
	publicationView, publicationOK := historian.(consensus.ValidationPublicationQuorum)
	if !publicationOK {
		return nil, nil
	}
	ids, quorum, available, release := publicationView.BeginValidationPublication(seq)
	if !available {
		if release != nil {
			release()
		}
		return nil, nil
	}
	for _, id := range ids {
		if id == candidateID {
			continue
		}
		if release != nil {
			release()
		}
		return nil, fmt.Errorf(
			"%w: sequence %d built %x conflicts with authenticated quorum %x/%d",
			service.ErrConsensusPublicationStale,
			seq,
			candidateID[:8],
			id[:8],
			quorum,
		)
	}
	return release, nil
}
