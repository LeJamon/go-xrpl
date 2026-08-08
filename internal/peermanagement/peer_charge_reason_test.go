package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
)

func TestChargeForReasonHaveSetHashSize(t *testing.T) {
	charge := chargeForReason("have-set-hashsize")

	assert.Equal(t, resource.FeeMalformedRequest(), charge)
}

func TestChargeForReasonHaveSetDuplicate(t *testing.T) {
	charge := chargeForReason("have-set-duplicate")

	assert.Equal(t, resource.FeeUselessData(), charge)
}
