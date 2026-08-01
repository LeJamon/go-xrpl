package peermanagement

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const bootstrapSourceTestWireSize = uint32(60_431_740)

func observeBootstrapProgress(
	t *testing.T,
	d *Discovery,
	address string,
	progress bootstrapFrameProgress,
) bootstrapProgressObservation {
	t.Helper()
	var governor bootstrapGovernor
	lease, ok := governor.tryReserve()
	require.True(t, ok)
	observation := lease.observeProgress(progress)
	require.True(t, observation.sampled)
	d.observeBootstrapSource(address, observation.projected)
	lease.release()
	return observation
}

func observeBootstrapRate(t *testing.T, d *Discovery, address string, rate uint64) bootstrapProgressObservation {
	t.Helper()
	elapsed := bootstrapSampleAge
	bytesRead := rate * uint64(elapsed) / uint64(time.Second)
	return observeBootstrapProgress(t, d, address, bootstrapFrameProgress{
		messageType: TypeManifests,
		wireSize:    bootstrapSourceTestWireSize,
		bytesRead:   bytesRead,
		elapsed:     elapsed,
	})
}

func TestDiscoveryBootstrapSlowIssueRatesCoolDownThenExpire(t *testing.T) {
	now := time.Unix(7_000, 0)
	first := "192.0.2.1:51235"
	second := "192.0.2.2:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 2,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{first, second} {
		d.AddPeer(address, 0, 0)
	}

	firstObservation := observeBootstrapRate(t, d, first, 444_146)
	secondObservation := observeBootstrapRate(t, d, second, 360_735)
	assert.Greater(t, firstObservation.projected, bootstrapTargetDuration)
	assert.Greater(t, secondObservation.projected, bootstrapTargetDuration)
	assert.Empty(t, d.selectPeersToConnect(2, true))
	status := d.bootstrapSourceSummary()
	assert.Equal(t, 2, status.known)
	assert.Equal(t, 2, status.unviable)
	assert.True(t, status.allUnviable())

	now = now.Add(bootstrapPartialRetry - time.Nanosecond)
	assert.Empty(t, d.selectPeersToConnect(2, true))
	now = now.Add(time.Nanosecond)
	assert.ElementsMatch(t, []string{first, second}, d.selectPeersToConnect(2, true))
	status = d.bootstrapSourceSummary()
	assert.Zero(t, status.unviable)
	assert.False(t, status.allUnviable())
}

func TestDiscoveryBootstrapMatureProgressCanRecoverSource(t *testing.T) {
	now := time.Unix(7_100, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)

	slow := observeBootstrapRate(t, d, address, 360_735)
	require.Greater(t, slow.projected, bootstrapTargetDuration)
	assert.True(t, d.bootstrapSourceSummary().allUnviable())

	fast := observeBootstrapRate(t, d, address, 650_000)
	require.LessOrEqual(t, fast.projected, bootstrapTargetDuration)
	status := d.bootstrapSourceSummary()
	assert.Zero(t, status.unviable)
	assert.False(t, status.allUnviable())
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapPartialFailureExtendsCooldown(t *testing.T) {
	now := time.Unix(7_200, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)
	observeBootstrapRate(t, d, address, 360_735)

	now = now.Add(9 * time.Minute)
	d.delayConnectRetry(address, bootstrapPartialRetry)
	now = now.Add(time.Minute)
	assert.Empty(t, d.selectPeersToConnect(1, true), "the original cooldown expired but the partial failure extended it")
	assert.True(t, d.bootstrapSourceSummary().allUnviable())

	now = now.Add(9 * time.Minute)
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
	assert.False(t, d.bootstrapSourceSummary().allUnviable())
}

func TestDiscoveryBootstrapSlightlySlowProjectionIsNotPermanent(t *testing.T) {
	now := time.Unix(7_300, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)

	elapsed := 20 * time.Second
	bytesRead := uint64(float64(bootstrapSourceTestWireSize) * float64(elapsed) / float64(111*time.Second))
	observation := observeBootstrapProgress(t, d, address, bootstrapFrameProgress{
		messageType: TypeManifests,
		wireSize:    bootstrapSourceTestWireSize,
		bytesRead:   bytesRead,
		elapsed:     elapsed,
	})
	require.Greater(t, observation.projected, bootstrapTargetDuration)
	require.Less(t, observation.projected, 112*time.Second)
	assert.Empty(t, d.selectPeersToConnect(1, true))

	now = now.Add(bootstrapPartialRetry)
	assert.Equal(t, []string{address}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapSourceTargetBoundaryRemainsViable(t *testing.T) {
	now := time.Unix(7_400, 0)
	fast := "192.0.2.1:51235"
	boundary := "192.0.2.2:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 2,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(fast, 0, 0)
	d.AddPeer(boundary, 0, 0)
	d.observeBootstrapSource(boundary, bootstrapTargetDuration)
	d.observeBootstrapSource(fast, bootstrapTargetDuration-time.Nanosecond)

	assert.Equal(t, []string{fast, boundary}, d.selectPeersToConnect(2, true))
	status := d.bootstrapSourceSummary()
	assert.Equal(t, 2, status.known)
	assert.Zero(t, status.unviable)
	assert.False(t, status.allUnviable())
}

func TestDiscoveryBootstrapRankingPrefersViableSpeedWithinCompressionTier(t *testing.T) {
	fast := "192.0.2.1:51235"
	slow := "192.0.2.2:51235"
	unknown := "192.0.2.3:51235"
	d := NewDiscovery(&Config{MaxOutbound: 3}, nil)
	for _, address := range []string{fast, slow, unknown} {
		d.AddPeer(address, 0, 0)
		d.markNegotiatedCompression(address, false)
	}
	observeBootstrapRate(t, d, slow, 650_000)
	observeBootstrapRate(t, d, fast, 800_000)

	assert.Equal(t, []string{fast, slow, unknown}, d.selectPeersToConnect(3, true))
}

func TestDiscoveryBootstrapRankingKeepsCompressionPrimary(t *testing.T) {
	compressed := "192.0.2.1:51235"
	uncompressed := "192.0.2.2:51235"
	d := NewDiscovery(&Config{MaxOutbound: 2}, nil)
	d.AddPeer(compressed, 0, 0)
	d.AddPeer(uncompressed, 0, 0)
	d.markNegotiatedCompression(compressed, true)
	d.markNegotiatedCompression(uncompressed, false)
	observeBootstrapRate(t, d, compressed, 650_000)
	observeBootstrapRate(t, d, uncompressed, 800_000)

	assert.Equal(t, []string{compressed}, d.selectPeersToConnect(1, true))
}

func TestDiscoveryBootstrapSourceSummaryDeduplicatesHosts(t *testing.T) {
	now := time.Unix(7_500, 0)
	first := "192.0.2.1:51235"
	alternatePort := "192.0.2.1:2459"
	second := "192.0.2.2:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 3,
		Clock:       func() time.Time { return now },
	}, nil)
	for _, address := range []string{first, alternatePort, second} {
		d.AddPeer(address, 0, 0)
	}

	status := d.bootstrapSourceSummary()
	require.Equal(t, 2, status.known)
	assert.Zero(t, status.unviable)
	assert.False(t, status.allUnviable())

	observeBootstrapRate(t, d, first, 444_146)
	status = d.bootstrapSourceSummary()
	assert.Equal(t, 2, status.known)
	assert.Equal(t, 1, status.unviable)
	assert.False(t, status.allUnviable())

	observeBootstrapRate(t, d, second, 360_735)
	status = d.bootstrapSourceSummary()
	assert.Equal(t, 2, status.unviable)
	assert.True(t, status.allUnviable())
}

func TestOverlayBoundsAllBootstrapSourcesUnviableDiagnosticPerEpisode(t *testing.T) {
	now := time.Unix(7_600, 0)
	address := "192.0.2.1:51235"
	d := NewDiscovery(&Config{
		MaxOutbound: 1,
		Clock:       func() time.Time { return now },
	}, nil)
	d.AddPeer(address, 0, 0)
	overlay := &Overlay{}

	observeBootstrapRate(t, d, address, 360_735)
	status := d.bootstrapSourceSummary()
	require.True(t, status.allUnviable())
	assert.True(t, overlay.shouldLogAllBootstrapSourcesUnviable(status))
	assert.False(t, overlay.shouldLogAllBootstrapSourcesUnviable(status))

	observeBootstrapRate(t, d, address, 650_000)
	status = d.bootstrapSourceSummary()
	require.False(t, status.allUnviable())
	assert.False(t, overlay.shouldLogAllBootstrapSourcesUnviable(status))

	observeBootstrapRate(t, d, address, 444_146)
	status = d.bootstrapSourceSummary()
	require.True(t, status.allUnviable())
	assert.True(t, overlay.shouldLogAllBootstrapSourcesUnviable(status))
	assert.False(t, overlay.shouldLogAllBootstrapSourcesUnviable(status))
}
