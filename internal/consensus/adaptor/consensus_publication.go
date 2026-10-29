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
	return validationPublicationGuard(
		historian,
		consensus.LedgerID(candidate.Hash()),
		candidate.Sequence(),
		func(seq uint32) (consensus.LedgerID, bool, error) {
			hash, ok, err := candidate.HashOfSeq(seq)
			return consensus.LedgerID(hash), ok, err
		},
	)
}

func validationPublicationGuard(
	historian consensus.ValidationHistorian,
	candidateID consensus.LedgerID,
	seq uint32,
	hashOfSeq func(uint32) (consensus.LedgerID, bool, error),
) (func(), error) {
	publicationView, publicationOK := historian.(consensus.ValidationPublicationQuorum)
	if !publicationOK {
		return nil, nil
	}
	snapshot := publicationView.BeginValidationPublication(seq)
	for _, id := range snapshot.LedgerIDs {
		if id == candidateID {
			continue
		}
		if snapshot.Release != nil {
			snapshot.Release()
		}
		return nil, fmt.Errorf(
			"%w: sequence %d built %x conflicts with authenticated quorum %x/%d",
			service.ErrConsensusPublicationStale,
			seq,
			candidateID[:8],
			id[:8],
			snapshot.Quorum,
		)
	}
	if hashOfSeq != nil && snapshot.RetainedLedgerSeq != 0 && seq > snapshot.RetainedLedgerSeq {
		hash, ok, err := hashOfSeq(snapshot.RetainedLedgerSeq)
		if err == nil && ok && hash != snapshot.RetainedLedgerID {
			if snapshot.Release != nil {
				snapshot.Release()
			}
			return nil, fmt.Errorf(
				"%w: sequence %d built %x conflicts with retained quorum %x/%d",
				service.ErrConsensusPublicationStale,
				seq,
				candidateID[:8],
				snapshot.RetainedLedgerID[:8],
				snapshot.RetainedLedgerSeq,
			)
		}
	}
	return snapshot.Release, nil
}
