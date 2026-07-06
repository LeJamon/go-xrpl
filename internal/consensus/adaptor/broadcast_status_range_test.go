package adaptor

import (
	"context"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// capturingSender records the last TMStatusChange broadcast so tests can inspect
// the advertised ledger range. All other NetworkSender calls are no-ops.
type capturingSender struct {
	noopSender
	last *message.StatusChange
}

func (c *capturingSender) BroadcastStatusChange(sc *message.StatusChange) error {
	c.last = sc
	return nil
}

// TestAdg_BroadcastStatus_AdvertisesServeRange verifies broadcastStatus fills
// FirstSeq/LastSeq from the served/validated range rather than the old
// hardcoded genesis-to-closed span.
func TestAdg_BroadcastStatus_AdvertisesServeRange(t *testing.T) {
	svc := newTestLedgerService(t)
	for range 5 {
		_, err := svc.AcceptLedger(context.TODO())
		require.NoError(t, err)
	}

	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	sender := &capturingSender{}
	a := New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    []consensus.NodeID{identity.NodeID},
		Sender:        sender,
	})

	a.broadcastStatus(message.NodeEventAcceptedLedger)

	require.NotNil(t, sender.last, "status change must be broadcast")
	require.NotNil(t, sender.last.FirstSeq)
	require.NotNil(t, sender.last.LastSeq)

	wantFirst, wantLast, ok := svc.AdvertisedLedgerRange()
	require.True(t, ok, "driven standalone service must expose a validated range")
	assert.Equal(t, wantFirst, *sender.last.FirstSeq, "FirstSeq must track the served range")
	assert.Equal(t, wantLast, *sender.last.LastSeq, "LastSeq must track the validated tip")
	assert.Equal(t, svc.GetValidatedLedgerIndex(), *sender.last.LastSeq,
		"LastSeq must be the validated tip, not the closed ledger")
}
