package adaptor

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
)

// Test-only post-construction wiring for routers built with NewRouter.
// Production supplies these through routerDeps.

func (r *Router) SetTxInbox(txInbox <-chan *peermanagement.InboundMessage) {
	r.txInbox = txInbox
}

func (r *Router) SetConsensusControlInbox(inbox <-chan *peermanagement.InboundMessage) {
	r.consensusControlInbox = inbox
}

func (r *Router) SetAcqInbox(acqInbox <-chan *peermanagement.InboundMessage) {
	r.acqInbox = acqInbox
}

func (r *Router) SetManifestInbox(manifestInbox <-chan *peermanagement.InboundMessage) {
	r.manifestInbox = manifestInbox
}

func (r *Router) SetMinimumOnlineFloor(floor MinimumOnlineFloor) {
	r.catchupReplay.setFloor(floor)
}

func (r *Router) SetManifestCache(cache *manifest.Cache, overlay routerOverlay) {
	r.manifests = cache
	r.overlay = overlay
	if cache != nil && !r.manifestLimitSet {
		r.manifestUntrustedLimit = cache.MaxUntrustedCount()
	}
}

func (r *Router) SetManifestClassifier(classify func([33]byte) manifest.ManifestRateLimitCapPolicy) {
	r.manifestClassify = classify
}

func (r *Router) SetManifestUntrustedLimit(limit int) {
	r.manifestUntrustedLimit = max(limit, 0)
	r.manifestLimitSet = true
}

func (r *Router) setPeerSessionView(view peerSessionView) {
	r.peerSessions = view
	r.catchupReplay.setPeerSessionView(view)
}

func (r *Router) SetValidatorListAggregator(agg *validatorlist.Aggregator) {
	r.validatorList = agg
	if agg == nil {
		r.catchupReplay.setValidatorPeerForget(nil)
		return
	}
	r.catchupReplay.setValidatorPeerForget(agg.ForgetPeer)
}

// setTxSetRetryKnobsForTest dials tx-set retry timings down. The lock matches
// the read in handleTxSetData, so it may race an active inbox goroutine.
func (r *Router) setTxSetRetryKnobsForTest(knobs txSetRetryKnobs) {
	r.txSetAcquireMu.Lock()
	defer r.txSetAcquireMu.Unlock()
	r.txSetRetryKnobs = knobs
}

func TestNewRouterAppliesDeps(t *testing.T) {
	a := New(Config{})
	consensusControl := make(chan *peermanagement.InboundMessage)
	service := make(chan *peermanagement.InboundMessage)
	tx := make(chan *peermanagement.InboundMessage)
	acq := make(chan *peermanagement.InboundMessage)
	manifests := make(chan *peermanagement.InboundMessage)
	cache := manifest.NewCache(7)
	overlay, err := peermanagement.New(peermanagement.WithListenAddr("127.0.0.1:0"))
	require.NoError(t, err)
	agg, err := validatorlist.New(validatorlist.Config{})
	require.NoError(t, err)
	floor := minimumOnlineFloorStub(42)
	limit := -3

	r := newRouter(nil, a, nil, routerNetworkConfig{}, routerDeps{
		consensusControlInbox:  consensusControl,
		serviceInbox:           service,
		txInbox:                tx,
		acqInbox:               acq,
		manifestInbox:          manifests,
		manifests:              cache,
		overlay:                overlay,
		manifestClassify:       func([33]byte) manifest.ManifestRateLimitCapPolicy { return manifest.Capped },
		manifestUntrustedLimit: &limit,
		peerSessions:           overlay,
		floor:                  floor,
		validatorList:          agg,
	})

	require.Equal(t, (<-chan *peermanagement.InboundMessage)(consensusControl), r.consensusControlInbox)
	require.Equal(t, (<-chan *peermanagement.InboundMessage)(service), r.serviceInbox)
	require.Equal(t, (<-chan *peermanagement.InboundMessage)(tx), r.txInbox)
	require.Equal(t, (<-chan *peermanagement.InboundMessage)(acq), r.acqInbox)
	require.Equal(t, (<-chan *peermanagement.InboundMessage)(manifests), r.manifestInbox)
	require.Same(t, cache, r.manifests)
	require.Equal(t, routerOverlay(overlay), r.overlay)
	require.Equal(t, manifest.Capped, r.manifestClassify([33]byte{}))
	require.Zero(t, r.manifestUntrustedLimit)
	require.True(t, r.manifestLimitSet)
	require.Equal(t, peerSessionView(overlay), r.peerSessions)
	require.Equal(t, peerSessionView(overlay), r.catchupReplay.peerSessions)
	require.EqualValues(t, 42, r.catchupReplay.minimumOnline())
	require.Same(t, agg, r.validatorList)
	require.NotNil(t, r.catchupReplay.forgetValidatorPeer)
}

func TestNewRouterManifestLimitDefaultsToCache(t *testing.T) {
	r := newRouter(nil, New(Config{}), nil, routerNetworkConfig{}, routerDeps{
		manifests: manifest.NewCache(7),
	})
	require.Equal(t, 7, r.manifestUntrustedLimit)
	require.False(t, r.manifestLimitSet)
}

type minimumOnlineFloorStub uint32

func (f minimumOnlineFloorStub) MinimumOnline() uint32 { return uint32(f) }
