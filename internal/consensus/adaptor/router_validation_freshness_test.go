package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func buildValidationAt(t *testing.T, peerID peermanagement.PeerID, signTime time.Time) *peermanagement.InboundMessage {
	t.Helper()
	key := [33]byte{0x02, 0x77}
	v := &consensus.Validation{
		Full:      true,
		LedgerSeq: 42,
		SignTime:  signTime,
	}
	v.LedgerID = consensus.LedgerID{1}
	v.NodeID = consensus.CalcNodeID(key)
	v.SigningPubKey = key
	v.Signature = make([]byte, 70)
	return &peermanagement.InboundMessage{
		PeerID:  peerID,
		Type:    message.TypeValidation,
		Payload: encodePayload(t, &message.Validation{Validation: SerializeSTValidation(v)}),
	}
}

// TestRouter_ValidationFreshnessGate: a non-current validation (sign-time
// outside the IsCurrent window) must be charged and dropped at ingress —
// before dedup, relay, and the engine — mirroring rippled's PeerImp
// isCurrent check in onMessage(TMValidation). A current one passes
// through uncharged.
func TestRouter_ValidationFreshnessGate(t *testing.T) {
	for _, tc := range []struct {
		name      string
		signTime  time.Time
		delivered bool
	}{
		{"stale sign-time dropped", time.Now().Add(-10 * time.Minute), false},
		{"future sign-time dropped", time.Now().Add(10 * time.Minute), false},
		{"current sign-time delivered", time.Now(), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := &mockEngine{}
			identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
			require.NoError(t, err)
			rs := &badDataRecordingSender{}
			a := New(Config{
				LedgerService: newTestLedgerService(t),
				Sender:        rs,
				Identity:      identity,
			})
			router := newTestRouter(engine, a, make(chan *peermanagement.InboundMessage, 1))

			router.handleValidation(buildValidationAt(t, 7, tc.signTime))

			if tc.delivered {
				require.Len(t, engine.getValidations(), 1, "current validation must reach the engine")
				assert.Empty(t, rs.getBadDataCalls(), "current validation must not be charged")
				return
			}
			assert.Empty(t, engine.getValidations(), "non-current validation must be shed before the engine")
			calls := rs.getBadDataCalls()
			require.Len(t, calls, 1)
			assert.Equal(t, uint64(7), calls[0].peerID)
			assert.Equal(t, "validation-not-current", calls[0].reason)
		})
	}
}
