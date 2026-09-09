package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestHeaderDiscoveryWireHeaderForms(t *testing.T) {
	for _, tc := range []struct {
		name     string
		suffix   int
		prefixed bool
		truncate bool
		accepted bool
	}{
		{name: "raw", accepted: true},
		{name: "one trailing byte", suffix: 1, accepted: true},
		{name: "four trailing bytes", suffix: 4, accepted: true},
		{name: "ignored trailing hash", suffix: 32, accepted: true},
		{name: "thirty-six trailing bytes", suffix: 36, accepted: true},
		{name: "long suffix", suffix: 64, accepted: true},
		{name: "ledger-master prefix", prefixed: true},
		{name: "prefix and trailing hash", prefixed: true, suffix: 32},
		{name: "truncated", truncate: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			base := r.adaptor.LedgerService().GetClosedLedger()
			link := buildAlternativeReplaySuccessor(t, base, time.Second)
			startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)
			data := header.AddRaw(link.ledger.Header(), false)
			data = append(data, make([]byte, tc.suffix)...)
			if tc.prefixed {
				data = append(protocol.HashPrefixLedgerMaster().Bytes(), data...)
			}
			if tc.truncate {
				data = data[:header.SizeBase-1]
			}
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7, Type: message.TypeLedgerData,
				Payload: encodePayload(t, &message.LedgerData{
					LedgerHash: link.hash[:], LedgerSeq: link.seq,
					InfoType: message.LedgerInfoBase,
					Nodes:    []message.LedgerNode{{NodeData: data}},
				}),
			})
			entry, published := r.lookupSeqHash(link.seq)
			if tc.accepted {
				require.True(t, published)
				require.True(t, entry.haveParent)
				require.Equal(t, link.hash, entry.hash)
				require.Equal(t, base.Hash(), entry.parentHash)
				require.Empty(t, sender.getBadDataCalls())
			} else {
				require.False(t, published)
				bad := sender.getBadDataCalls()
				require.Len(t, bad, 1)
				require.Equal(t, "ledger-header-ancestry", bad[0].reason)
			}
			require.Equal(t, base.Hash(), r.adaptor.LedgerService().GetValidatedLedger().Hash())
		})
	}
}

func TestHeaderDiscoveryWireEnvelope(t *testing.T) {
	for _, tc := range []struct {
		name       string
		age        time.Duration
		gap        int
		emptyError bool
		wantReason string
	}{
		{name: "fresh at sequence limit", age: 10 * time.Second, gap: 10},
		{name: "fresh beyond sequence limit", age: 10 * time.Second, gap: 11, wantReason: "ledger-data-sequence"},
		{name: "stale beyond sequence limit", age: 11 * time.Second, gap: 11},
		{name: "empty unavailable reply", age: 90 * time.Second, gap: 1, emptyError: true, wantReason: "ledger-data-count"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, sender := makeRouterWithBadDataRecorder(t)
			svc := r.adaptor.LedgerService()
			base := svc.GetClosedLedger()
			svc.SetValidatedLedgerAgeClock(func() time.Time { return base.CloseTime().Add(tc.age) })
			require.Equal(t, tc.age, svc.GetValidatedLedgerAge())
			parent := base
			var target standardReplayTestLink
			for range tc.gap {
				target = buildAlternativeReplaySuccessor(t, parent, time.Second)
				parent = target.ledger
			}
			startTestHeaderDiscovery(t, r, base.Sequence(), target, 7, catchupSourceQuorum)
			data := &message.LedgerData{
				LedgerHash: target.hash[:], LedgerSeq: target.seq, InfoType: message.LedgerInfoBase,
				Nodes: []message.LedgerNode{{NodeData: target.response.LedgerHeader}},
			}
			if tc.emptyError {
				data.Nodes = nil
				data.Error = message.ReplyErrorNoLedger
				data.ErrorSet = true
			}
			r.handleMessage(&peermanagement.InboundMessage{
				PeerID: 7, Type: message.TypeLedgerData, Payload: encodePayload(t, data),
			})
			bad := sender.getBadDataCalls()
			if tc.wantReason != "" {
				require.Len(t, bad, 1)
				require.Equal(t, tc.wantReason, bad[0].reason)
				require.Len(t, sender.headerRequests(), 1)
				require.Empty(t, r.headerDiscovery.headers)
			} else {
				require.Empty(t, bad)
				require.Contains(t, r.headerDiscovery.headers, target.seq)
				require.Len(t, sender.headerRequests(), 2)
			}
			require.Equal(t, base.Hash(), svc.GetValidatedLedger().Hash())
		})
	}
}

func TestHeaderDiscoveryWaitsForValidationWindow(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	parent := base
	var target standardReplayTestLink
	for range 30 {
		target = buildAlternativeReplaySuccessor(t, parent, time.Second)
		parent = target.ledger
	}
	r.recordValidationCatchupTarget(target.seq, target.hash, 7, catchupSourceQuorum)
	svc.SetValidatedLedgerAgeClock(func() time.Time { return base.CloseTime().Add(10 * time.Second) })
	r.armCatchupTowardTargetWithPeer(7)
	require.Nil(t, r.headerDiscovery)
	require.Empty(t, sender.headerRequests())
	require.Empty(t, sender.legacyCalls())
	svc.SetValidatedLedgerAgeClock(func() time.Time { return base.CloseTime().Add(11 * time.Second) })
	r.armCatchupTowardTargetWithPeer(7)
	require.NotNil(t, r.headerDiscovery)
	require.Len(t, sender.headerRequests(), 1)
	require.Empty(t, sender.legacyCalls())
}
