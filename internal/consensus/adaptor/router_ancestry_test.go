package adaptor

import (
	"errors"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func startTestHeaderDiscovery(
	t *testing.T,
	r *Router,
	baseSeq uint32,
	target standardReplayTestLink,
	peerID uint64,
	source catchupTargetSource,
) {
	t.Helper()
	r.recordValidationCatchupTarget(target.seq, target.hash, peerID, source)
	base := r.adaptor.LedgerService().GetClosedLedger()
	require.NotNil(t, base)
	require.Equal(t, baseSeq, base.Sequence())
	require.True(t, r.startHeaderParentDiscovery(base, target.seq, target.hash, peerID, source))
}

func sendTestHeaderReply(t *testing.T, r *Router, peerID uint64, link standardReplayTestLink) {
	t.Helper()
	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: peermanagement.PeerID(peerID),
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: link.hash[:],
			LedgerSeq:  link.seq,
			InfoType:   message.LedgerInfoBase,
			Nodes:      []message.LedgerNode{{NodeData: link.response.LedgerHeader}},
		}),
	})
}

func TestRouter_HeaderDiscoveryRequiresTrustedCurrentTarget(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)

	assert.False(t, r.startHeaderParentDiscovery(base, link.seq, link.hash, 7, catchupSourcePeer))
	assert.Empty(t, sender.headerRequests())

	r.recordCatchupTarget(link.seq, link.hash, 7)
	assert.False(t, r.startHeaderParentDiscovery(base, link.seq, link.hash, 7, catchupSourceQuorum))
	assert.Empty(t, sender.headerRequests())

	r.recordValidationCatchupTarget(link.seq, link.hash, 7, catchupSourceQuorum)
	assert.True(t, r.startHeaderParentDiscovery(base, link.seq, link.hash, 7, catchupSourceQuorum))
	requests := sender.headerRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, uint64(7), requests[0].peerID)
	assert.Equal(t, link.hash, requests[0].hash)
}

func TestRouter_HeaderDiscoveryFreezesTrustedTarget(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	newer := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)

	startTestHeaderDiscovery(t, r, base.Sequence(), first, 7, catchupSourceQuorum)
	require.Len(t, sender.headerRequests(), 1)
	r.recordValidationCatchupTarget(newer.seq, newer.hash, 7, catchupSourceQuorum)
	require.True(t, r.startHeaderParentDiscovery(base, newer.seq, newer.hash, 7, catchupSourceQuorum))

	r.headerDiscoveryMu.Lock()
	current := *r.headerDiscovery
	r.headerDiscoveryMu.Unlock()
	assert.Equal(t, first.seq, current.targetSeq)
	assert.Equal(t, first.hash, current.targetHash)
	assert.Equal(t, first.seq, current.nextSeq)
	assert.Equal(t, first.hash, current.nextHash)
	assert.Len(t, sender.headerRequests(), 1, "a moving trusted target must wait for the frozen walk")
	r.cancelHeaderDiscovery()
}

func TestRouter_HeaderDiscoveryCommitsVerifiedChain(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	links := make([]standardReplayTestLink, 0, 3)
	parent := base
	for range 3 {
		link := buildAlternativeReplaySuccessor(t, parent, time.Second)
		links = append(links, link)
		parent = link.ledger
	}

	startTestHeaderDiscovery(t, r, base.Sequence(), links[2], 7, catchupSourceQuorum)
	requests := sender.headerRequests()
	require.Len(t, requests, 1)
	assert.Equal(t, links[2].hash, requests[0].hash)

	for i := len(links) - 1; i >= 0; i-- {
		sendTestHeaderReply(t, r, 7, links[i])
		if i > 0 {
			requests = sender.headerRequests()
			require.Len(t, requests, len(links)-i+1)
			assert.Equal(t, links[i-1].hash, requests[len(requests)-1].hash)
		}
		for _, link := range links {
			_, known := r.lookupSeqHash(link.seq)
			if i > 0 {
				assert.False(t, known, "partial header walk must not publish sequence %d", link.seq)
			}
		}
	}

	for _, link := range links {
		entry, known := r.lookupSeqHash(link.seq)
		require.True(t, known)
		assert.Equal(t, link.hash, entry.hash)
		assert.Equal(t, link.ledger.ParentHash(), entry.parentHash)
	}
	r.headerDiscoveryMu.Lock()
	assert.Nil(t, r.headerDiscovery)
	r.headerDiscoveryMu.Unlock()
	assert.Empty(t, sender.legacyCalls(), "header discovery must not start a full-state pivot")
}

func TestRouter_HeaderDiscoveryRetriesMalformedHeaderOnAlternatePeer(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	svc := r.adaptor.LedgerService()
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	trackCatchupPeer(r, 8, link.seq)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7,
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: link.hash[:],
			LedgerSeq:  link.seq,
			InfoType:   message.LedgerInfoBase,
			Nodes:      []message.LedgerNode{{NodeData: []byte{1}}},
		}),
	})

	requests := sender.headerRequests()
	require.Len(t, requests, 2)
	assert.Equal(t, uint64(7), requests[0].peerID)
	assert.Equal(t, uint64(8), requests[1].peerID)
	bad := sender.getBadDataCalls()
	require.Len(t, bad, 1)
	assert.Equal(t, uint64(7), bad[0].peerID)
	assert.Equal(t, "ledger-header-ancestry", bad[0].reason)

	sendTestHeaderReply(t, r, 8, link)
	entry, known := r.lookupSeqHash(link.seq)
	require.True(t, known)
	assert.Equal(t, link.hash, entry.hash)
}

func TestRouter_HeaderDiscoveryRetriesTransientSendError(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	trackCatchupPeer(r, 8, link.seq)
	sender.mu.Lock()
	sender.headerErrs = map[uint64][]error{7: {errors.New("temporary overlay failure")}}
	sender.mu.Unlock()

	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)
	requests := sender.headerRequests()
	require.Len(t, requests, 2)
	assert.Equal(t, uint64(7), requests[0].peerID)
	assert.Equal(t, uint64(8), requests[1].peerID)

	r.headerDiscoveryMu.Lock()
	assert.True(t, r.headerDiscovery.pending)
	assert.Equal(t, uint64(8), r.headerDiscovery.peerID)
	r.headerDiscoveryMu.Unlock()
}

func TestRouter_HeaderDiscoveryRetriesUnavailableReply(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	svc := r.adaptor.LedgerService()
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	trackCatchupPeer(r, 8, link.seq)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.handleMessage(&peermanagement.InboundMessage{
		PeerID: 7,
		Type:   message.TypeLedgerData,
		Payload: encodePayload(t, &message.LedgerData{
			LedgerHash: link.hash[:],
			LedgerSeq:  link.seq,
			InfoType:   message.LedgerInfoBase,
			Error:      message.ReplyErrorNoLedger,
			ErrorSet:   true,
		}),
	})

	requests := sender.headerRequests()
	require.Len(t, requests, 2)
	assert.Equal(t, uint64(8), requests[1].peerID)
	assert.Empty(t, sender.getBadDataCalls(), "unavailable is a retryable response, not bad peer data")
	sendTestHeaderReply(t, r, 8, link)
	_, known := r.lookupSeqHash(link.seq)
	assert.True(t, known)
}

func TestRouter_HeaderDiscoveryTimeoutRotatesPeer(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	trackCatchupPeer(r, 8, link.seq)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.headerDiscoveryMu.Lock()
	r.headerDiscovery.lastSentAt = time.Now().Add(-headerDiscoveryRetryInterval)
	r.headerDiscoveryMu.Unlock()
	r.tickHeaderDiscovery(time.Now())

	requests := sender.headerRequests()
	require.Len(t, requests, 2)
	assert.Equal(t, uint64(8), requests[1].peerID)
	r.headerDiscoveryMu.Lock()
	_, excluded := r.headerDiscovery.excludedPeers[7]
	r.headerDiscoveryMu.Unlock()
	assert.True(t, excluded)
}

func TestRouter_HeaderDiscoveryHonorsWholeSessionDeadline(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.headerDiscoveryMu.Lock()
	r.headerDiscovery.deadline = time.Now().Add(-time.Second)
	r.headerDiscoveryMu.Unlock()
	r.tickHeaderDiscovery(time.Now())

	assert.Len(t, sender.headerRequests(), 1)
	r.headerDiscoveryMu.Lock()
	assert.True(t, r.headerDiscovery.terminal)
	r.headerDiscoveryMu.Unlock()
}

func TestRouter_HeaderDiscoveryRejectsReplyAfterDeadline(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.headerDiscoveryMu.Lock()
	r.headerDiscovery.deadline = time.Now().Add(-time.Second)
	r.headerDiscoveryMu.Unlock()

	handled := r.handleHeaderDiscoveryReply(&message.LedgerData{
		LedgerHash: link.hash[:],
		LedgerSeq:  link.seq,
		InfoType:   message.LedgerInfoBase,
		Nodes:      []message.LedgerNode{{NodeData: link.response.LedgerHeader}},
	}, 7)
	assert.True(t, handled)
	_, known := r.lookupSeqHash(link.seq)
	assert.False(t, known, "a reply arriving after the session deadline must not publish")
	r.headerDiscoveryMu.Lock()
	assert.True(t, r.headerDiscovery.terminal)
	r.headerDiscoveryMu.Unlock()
}

func TestRouter_HeaderDiscoveryValidWrongParentFallsBackWithoutChargingPeer(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	svc := r.adaptor.LedgerService()
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	ancestor, err := svc.GetLedgerBySequence(base.Sequence() - 1)
	require.NoError(t, err)
	wrongParent := buildAlternativeReplaySuccessor(t, ancestor, time.Second)
	wrongTarget := buildAlternativeReplaySuccessor(t, wrongParent.ledger, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), wrongTarget, 7, catchupSourceQuorum)

	sendTestHeaderReply(t, r, 7, wrongTarget)

	bad := sender.getBadDataCalls()
	assert.Empty(t, bad, "a valid header on the wrong branch is a conflict, not malformed peer data")
	assert.Len(t, sender.headerRequests(), 1)
	assert.NotEmpty(t, sender.legacyCalls(), "conflicting ancestry must fall back to a full-state acquisition")
	entry, known := r.lookupSeqHash(wrongTarget.seq)
	assert.False(t, known)
	assert.Equal(t, [32]byte{}, entry.hash)
	r.headerDiscoveryMu.Lock()
	assert.True(t, r.headerDiscovery.terminal)
	r.headerDiscoveryMu.Unlock()
}

func TestRouter_HeaderDiscoveryPublishesNoPrefixAfterLateConflict(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	second := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), second, 7, catchupSourceQuorum)

	sendTestHeaderReply(t, r, 7, second)
	r.headerDiscoveryMu.Lock()
	buffered := r.headerDiscovery.headers[second.seq]
	buffered.ParentHash = [32]byte{0x99}
	r.headerDiscovery.headers[second.seq] = buffered
	r.headerDiscoveryMu.Unlock()

	// The final reply makes the session complete, but the buffered successor
	// now describes a conflicting branch. Finish must validate the whole walk
	// before publishing first's acquired sequence entry.
	sendTestHeaderReply(t, r, 7, first)
	for _, seq := range []uint32{first.seq, second.seq} {
		_, known := r.lookupSeqHash(seq)
		assert.False(t, known, "conflicting walk must not publish prefix at %d", seq)
	}
	r.headerDiscoveryMu.Lock()
	assert.True(t, r.headerDiscovery.terminal)
	r.headerDiscoveryMu.Unlock()
}

func TestRouter_HeaderDiscoveryDropsOutOfOrderPeerReply(t *testing.T) {
	r, _, sender, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	// The request is bound to peer 7. A valid header from another peer must
	// remain untrusted and must not advance or retry the walk.
	r.handleHeaderDiscoveryReply(&message.LedgerData{
		LedgerHash: link.hash[:],
		LedgerSeq:  link.seq,
		InfoType:   message.LedgerInfoBase,
		Nodes:      []message.LedgerNode{{NodeData: link.response.LedgerHeader}},
	}, 8)
	assert.Len(t, sender.headerRequests(), 1)
	_, known := r.lookupSeqHash(link.seq)
	assert.False(t, known)

	sendTestHeaderReply(t, r, 7, link)
	_, known = r.lookupSeqHash(link.seq)
	assert.True(t, known)
}

func TestRouter_HeaderDiscoveryIgnoresDuplicatePriorReply(t *testing.T) {
	r, sender := makeRouterWithBadDataRecorder(t)
	svc := r.adaptor.LedgerService()
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	first := buildAlternativeReplaySuccessor(t, base, time.Second)
	second := buildAlternativeReplaySuccessor(t, first.ledger, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), second, 7, catchupSourceQuorum)

	sendTestHeaderReply(t, r, 7, second)
	requestsAfterFirst := len(sender.headerRequests())
	require.Equal(t, 2, requestsAfterFirst)

	// Header replies do not carry a request ID. A delayed duplicate for the
	// previous step is valid wire traffic and must not poison the peer or
	// restart the current request.
	sendTestHeaderReply(t, r, 7, second)
	assert.Len(t, sender.headerRequests(), requestsAfterFirst)
	assert.Empty(t, sender.getBadDataCalls())

	sendTestHeaderReply(t, r, 7, first)
	entry, known := r.lookupSeqHash(first.seq)
	require.True(t, known)
	assert.Equal(t, first.hash, entry.hash)
}

func TestRouter_HeaderDiscoveryCancellationDoesNotPublishStaleReply(t *testing.T) {
	r, _, _, svc := makeRouter(t)
	base := svc.GetClosedLedger()
	require.NotNil(t, base)
	link := buildAlternativeReplaySuccessor(t, base, time.Second)
	startTestHeaderDiscovery(t, r, base.Sequence(), link, 7, catchupSourceQuorum)

	r.cancelHeaderDiscovery()
	handled := r.handleHeaderDiscoveryReply(&message.LedgerData{
		LedgerHash: link.hash[:],
		LedgerSeq:  link.seq,
		InfoType:   message.LedgerInfoBase,
		Nodes:      []message.LedgerNode{{NodeData: link.response.LedgerHeader}},
	}, 7)
	assert.False(t, handled)
	_, known := r.lookupSeqHash(link.seq)
	assert.False(t, known, "canceled generation must not commit a stale header")
}
