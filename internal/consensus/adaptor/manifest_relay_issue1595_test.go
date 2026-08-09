package adaptor

import (
	"bytes"
	"sort"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/stretchr/testify/require"
)

func applyManifestWires(t *testing.T, cache *manifest.Cache, wires ...[]byte) [][33]byte {
	t.Helper()
	masters := make([][33]byte, 0, len(wires))
	for _, wire := range wires {
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		require.Equal(t, manifest.Accepted, cache.ApplyManifest(parsed, manifest.Capped))
		masters = append(masters, parsed.MasterKey())
	}
	return masters
}

func manifestMessage(t *testing.T, peer peermanagement.PeerID, wires ...[]byte) *peermanagement.InboundMessage {
	t.Helper()
	list := make([]message.Manifest, len(wires))
	for i, wire := range wires {
		list[i] = message.Manifest{STObject: wire}
	}
	return &peermanagement.InboundMessage{
		PeerID:  peer,
		Type:    message.TypeManifests,
		Payload: encodePayload(t, &message.Manifests{List: list}),
	}
}

func TestRouter_ManifestAdmissionClassifiesListedAndBoundsUnlisted(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, _ := routerWithCache(t, sender, 0, 0)
	badData := &badDataRecordingSender{}
	router.gossip = badData
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(1)

	unlistedFirst := buildWireManifest(t, 1, 0x01, 0x11)
	listed := buildWireManifest(t, 1, 0x02, 0x12)
	unlistedAfter := buildWireManifest(t, 1, 0x03, 0x13)
	parsedListed, err := manifest.Deserialize(listed)
	require.NoError(t, err)
	router.SetManifestClassifier(func(master [33]byte) manifest.ManifestRateLimitCapPolicy {
		if master == parsedListed.MasterKey() {
			return manifest.Uncapped
		}
		return manifest.Capped
	})

	router.handleManifests(manifestMessage(t, 7, unlistedFirst, listed, unlistedAfter))
	for _, wire := range [][]byte{unlistedFirst, listed} {
		parsed, parseErr := manifest.Deserialize(wire)
		require.NoError(t, parseErr)
		_, ok := cache.GetManifest(parsed.MasterKey())
		require.True(t, ok)
	}
	parsedAfter, err := manifest.Deserialize(unlistedAfter)
	require.NoError(t, err)
	_, ok := cache.GetManifest(parsedAfter.MasterKey())
	require.False(t, ok, "entry after the untrusted budget must be skipped")
	require.Equal(t, 1, cache.UntrustedCount())

	sender.mu.Lock()
	require.Len(t, sender.bcasts, 1)
	require.Empty(t, sender.bcastsExcept)
	relayed := frameToManifestBytes(t, sender.bcasts[0])
	sender.mu.Unlock()
	require.Equal(t, [][]byte{unlistedFirst, listed}, relayed)
	require.Equal(t,
		[]badDataCall{{peerID: 7, reason: "manifests-untrusted-limit"}},
		badData.getBadDataCalls())
}

func TestRouter_ManifestMalformedDoesNotConsumeUntrustedBudget(t *testing.T) {
	router, cache, _ := routerWithCache(t, nil, 0, 0)
	badData := &badDataRecordingSender{}
	router.gossip = badData
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(2)
	router.SetManifestClassifier(func([33]byte) manifest.ManifestRateLimitCapPolicy {
		return manifest.Capped
	})

	first := buildWireManifest(t, 1, 0x21, 0x31)
	second := buildWireManifest(t, 1, 0x22, 0x32)
	third := buildWireManifest(t, 1, 0x23, 0x33)
	router.handleManifests(manifestMessage(t, 8, []byte{0x01}, first, second, third))

	for _, wire := range [][]byte{first, second} {
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		_, ok := cache.GetManifest(parsed.MasterKey())
		require.True(t, ok)
	}
	parsedThird, err := manifest.Deserialize(third)
	require.NoError(t, err)
	_, ok := cache.GetManifest(parsedThird.MasterKey())
	require.False(t, ok, "the third valid unlisted entry must be beyond the budget")
	require.ElementsMatch(t,
		[]badDataCall{{peerID: 8, reason: "manifest-invalid"}, {peerID: 8, reason: "manifests-untrusted-limit"}},
		badData.getBadDataCalls())
}

func TestRouter_ManifestStaleAndInvalidConsumeUntrustedBudget(t *testing.T) {
	router, cache, _ := routerWithCache(t, nil, 0, 0)
	sender := &badDataRecordingSender{}
	router.gossip = sender
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(2)
	router.SetManifestClassifier(func([33]byte) manifest.ManifestRateLimitCapPolicy {
		return manifest.Capped
	})

	stale := buildWireManifest(t, 1, 0x41, 0x51)
	parsedStale, err := manifest.Deserialize(stale)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(parsedStale, manifest.Capped))
	invalid := buildWireManifest(t, 2, 0x42, 0x52)
	invalid[len(invalid)-1] ^= 1
	newer := buildWireManifest(t, 1, 0x43, 0x53)

	router.handleManifests(manifestMessage(t, 9, stale, invalid, newer))
	parsedNewer, err := manifest.Deserialize(newer)
	require.NoError(t, err)
	_, ok := cache.GetManifest(parsedNewer.MasterKey())
	require.False(t, ok, "stale and invalid entries must consume both untrusted slots")
	require.Equal(t, 1, cache.UntrustedCount())
	calls := sender.getBadDataCalls()
	require.Len(t, calls, 2)
	require.ElementsMatch(t,
		[]badDataCall{{peerID: 9, reason: "manifests-untrusted-limit"}, {peerID: 9, reason: "manifest-invalid"}},
		calls)
}

func TestRouter_ManifestCapacityRejectionIsNonMalformed(t *testing.T) {
	router, _, _ := routerWithCache(t, nil, 0, 0)
	cache := manifest.NewCache(1)
	router.SetManifestCache(cache, nil)
	sender := &badDataRecordingSender{}
	router.gossip = sender
	router.SetManifestUntrustedLimit(2)
	router.SetManifestClassifier(func([33]byte) manifest.ManifestRateLimitCapPolicy {
		return manifest.Capped
	})

	prefill := buildWireManifest(t, 1, 0x61, 0x71)
	require.Len(t, applyManifestWires(t, cache, prefill), 1)
	candidateOne := buildWireManifest(t, 1, 0x62, 0x72)
	candidateTwo := buildWireManifest(t, 1, 0x63, 0x73)
	router.handleManifests(manifestMessage(t, 10, candidateOne, candidateTwo))

	for _, wire := range [][]byte{candidateOne, candidateTwo} {
		parsed, err := manifest.Deserialize(wire)
		require.NoError(t, err)
		_, ok := cache.GetManifest(parsed.MasterKey())
		require.False(t, ok)
	}
	require.Equal(t, 1, cache.UntrustedCount())
	require.Empty(t, sender.getBadDataCalls(), "capacity rejection is not malformed")
}

func TestRouter_ManifestSnapshotSelectsBeforeShuffleAndInvalidatesOnTrustChange(t *testing.T) {
	router, cache, _ := routerWithCache(t, nil, 0, 0)
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(1)
	wires := [][]byte{
		buildWireManifest(t, 1, 0x81, 0x91),
		buildWireManifest(t, 1, 0x82, 0x92),
		buildWireManifest(t, 1, 0x83, 0x93),
	}
	masters := applyManifestWires(t, cache, wires...)
	listed := map[[33]byte]struct{}{masters[0]: {}, masters[1]: {}}
	router.SetManifestClassifier(func(master [33]byte) manifest.ManifestRateLimitCapPolicy {
		if _, ok := listed[master]; ok {
			return manifest.Uncapped
		}
		return manifest.Capped
	})
	router.SetManifestShuffle(func(selected [][]byte) {
		sort.Slice(selected, func(i, j int) bool { return bytes.Compare(selected[i], selected[j]) > 0 })
	})

	first := router.cachedManifestFrames()
	require.Len(t, first, 1)
	selected := frameToManifestBytes(t, first[0])
	require.Len(t, selected, 3)
	require.ElementsMatch(t, wires, selected)

	listed = map[[33]byte]struct{}{masters[0]: {}}
	second := router.cachedManifestFrames()
	require.Len(t, second, 1)
	secondSelected := frameToManifestBytes(t, second[0])
	require.Len(t, secondSelected, 2, "trust-only changes must rebuild the snapshot")
	require.Contains(t, secondSelected, wires[0])
}

func TestRouter_ManifestSnapshotUsesMultipleBoundedFrames(t *testing.T) {
	router, cache, _ := routerWithCache(t, nil, 0, 0)
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(101)
	wires := make([][]byte, 101)
	for i := range wires {
		wires[i] = buildWireManifest(t, 1, byte(i), byte(i+101))
	}
	applyManifestWires(t, cache, wires...)
	router.SetManifestClassifier(func([33]byte) manifest.ManifestRateLimitCapPolicy {
		return manifest.Capped
	})
	router.SetManifestShuffle(func([][]byte) {})

	frames := router.cachedManifestFrames()
	require.Len(t, frames, 2)
	var emitted [][]byte
	for _, frame := range frames {
		emitted = append(emitted, frameToManifestBytes(t, frame)...)
	}
	require.Len(t, emitted, len(wires))
	require.ElementsMatch(t, wires, emitted)
}

func TestRouter_ManifestSuppressionRecordsOnlyEmittedSelection(t *testing.T) {
	sender := &fakeManifestSender{}
	router, cache, _ := routerWithCache(t, sender, 0, 0)
	router.SetManifestCache(cache, nil)
	router.SetManifestUntrustedLimit(0)
	wires := [][]byte{
		buildWireManifest(t, 1, 0xA1, 0xB1),
		buildWireManifest(t, 1, 0xA2, 0xB2),
	}
	masters := applyManifestWires(t, cache, wires...)
	router.SetManifestClassifier(func(master [33]byte) manifest.ManifestRateLimitCapPolicy {
		if master == masters[0] {
			return manifest.Uncapped
		}
		return manifest.Capped
	})
	router.SetManifestShuffle(func(selected [][]byte) {
		sort.Slice(selected, func(i, j int) bool { return bytes.Compare(selected[i], selected[j]) < 0 })
	})
	first, err := manifest.Deserialize(wires[0])
	require.NoError(t, err)
	second, err := manifest.Deserialize(wires[1])
	require.NoError(t, err)
	require.False(t, router.messageSeen.seenRecently(first.Hash()))
	require.False(t, router.messageSeen.seenRecently(second.Hash()))
	frames := router.cachedManifestFrames()
	require.Equal(t, 0, router.manifestUntrustedLimit)
	require.Len(t, frames, 1)
	require.Len(t, frameToManifestBytes(t, frames[0]), 1)

	router.SendLocalManifestTo(17)
	require.True(t, router.messageSeen.seenRecently(first.Hash()))
	require.False(t, router.messageSeen.seenRecently(second.Hash()))
}
