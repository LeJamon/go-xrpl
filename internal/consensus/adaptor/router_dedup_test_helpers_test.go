package adaptor

import "github.com/LeJamon/go-xrpl/internal/consensus"

func hashProposalSuppression(p *consensus.Proposal) [32]byte {
	hash, err := hashProposalSuppressionChecked(p)
	if err != nil {
		panic(err)
	}
	return hash
}
