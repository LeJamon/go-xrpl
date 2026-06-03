package peermanagement

import (
	"testing"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
	"github.com/stretchr/testify/assert"
)

// TestChargeForReason_InnerBatchTxn pins the resource fee for a peer that
// relays a standalone tfInnerBatchTxn to feeModerateBurdenPeer (250), matching
// rippled PeerImp::handleTransaction (PeerImp.cpp:1293). Without an explicit
// mapping the reason falls through to FeeInvalidData (400), over-charging the
// peer relative to rippled.
func TestChargeForReason_InnerBatchTxn(t *testing.T) {
	assert.Equal(t, resource.FeeModerateBurdenPeer, chargeForReason("inner-batch-txn"))
}
