package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
)

func TestChargeForReasonHaveSetHashSize(t *testing.T) {
	charge := chargeForReason("have-set-hashsize")

	assert.Equal(t, resource.FeeMalformedRequest.Cost(), charge.Cost())
	assert.Equal(t, resource.FeeMalformedRequest.Label(), charge.Label())
}

func TestChargeForReasonHaveSetDuplicate(t *testing.T) {
	charge := chargeForReason("have-set-duplicate")

	assert.Equal(t, resource.FeeUselessData.Cost(), charge.Cost())
	assert.Equal(t, resource.FeeUselessData.Label(), charge.Label())
}
