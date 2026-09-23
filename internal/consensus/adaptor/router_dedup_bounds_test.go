package adaptor

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
)

func TestHashProposalSuppressionRejectsUnencodableVL(t *testing.T) {
	proposal := &consensus.Proposal{
		Signature: make([]byte, maxVLEncodedLength+1),
	}

	_, err := hashProposalSuppressionChecked(proposal)
	assert.ErrorIs(t, err, errVLEncodedTooLong)
}
