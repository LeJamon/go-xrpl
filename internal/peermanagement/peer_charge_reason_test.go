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

func TestChargeForReasonProtocolTiers(t *testing.T) {
	tests := map[string]resource.Charge{
		"cluster-no-pubkey":               resource.FeeUselessData(),
		"cluster-not-member":              resource.FeeUselessData(),
		"get-objects-txn-unnegotiated":    resource.FeeMalformedRequest(),
		"have-transactions-unnegotiated":  resource.FeeMalformedRequest(),
		"have-transactions-hashsize":      resource.FeeMalformedRequest(),
		"transactions-batch-unnegotiated": resource.FeeMalformedRequest(),
		"proposal-malformed-pubkey-type":  resource.FeeInvalidSignature(),
		"validation-invalid-signature":    resource.FeeInvalidSignature(),
		"vl-coll-heavy-too-many-blobs":    resource.FeeHeavyBurdenPeer(),
	}
	for reason, want := range tests {
		t.Run(reason, func(t *testing.T) {
			assert.Equal(t, want, chargeForReason(reason))
		})
	}
}
