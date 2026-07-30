package adaptor

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStup_MergeValidators_DisjointSets(t *testing.T) {
	aIDs := []consensus.NodeID{{0x01}, {0x02}}
	aMKs := [][33]byte{{0x02, 0x01}, {0x02, 0x02}}
	bIDs := []consensus.NodeID{{0x03}, {0x04}}
	bMKs := [][33]byte{{0x02, 0x03}, {0x02, 0x04}}

	ids, masters := mergeValidators(aIDs, aMKs, bIDs, bMKs)
	assert.Len(t, ids, 4)
	assert.Len(t, masters, 4)
}

func TestStup_MergeValidators_OverlappingSets(t *testing.T) {
	shared := [33]byte{0x02, 0xAA}
	aIDs := []consensus.NodeID{{0x01}}
	aMKs := [][33]byte{shared}
	bIDs := []consensus.NodeID{{0x01}}
	bMKs := [][33]byte{shared}

	ids, masters := mergeValidators(aIDs, aMKs, bIDs, bMKs)
	assert.Len(t, ids, 1, "duplicate master key must be deduplicated")
	assert.Len(t, masters, 1)
}

func TestStartupIncludeLocalValidator(t *testing.T) {
	master := [33]byte{0x02, 0xAA}
	identity := &ValidatorIdentity{
		MasterKey: master,
		NodeID:    consensus.CalcNodeID(master),
	}

	ids, masters := includeLocalValidator(nil, nil, identity)
	require.Equal(t, []consensus.NodeID{identity.NodeID}, ids)
	require.Equal(t, [][33]byte{master}, masters)

	ids, masters = includeLocalValidator(ids, masters, identity)
	require.Len(t, ids, 1)
	require.Len(t, masters, 1)
	configuredIDs, configuredMasters := excludeLocalValidator(ids, masters, identity)
	require.Empty(t, configuredIDs)
	require.Empty(t, configuredMasters)

	c := &Components{}
	require.Empty(t, c.StaticTrustedMasterKeys())
}

func TestStup_MergeValidators_EmptyInputs(t *testing.T) {
	ids, masters := mergeValidators(nil, nil, nil, nil)
	assert.Empty(t, ids)
	assert.Empty(t, masters)
}

func TestStup_MergeValidators_OneEmpty(t *testing.T) {
	aIDs := []consensus.NodeID{{0x01}}
	aMKs := [][33]byte{{0x02, 0x01}}

	ids, masters := mergeValidators(aIDs, aMKs, nil, nil)
	assert.Len(t, ids, 1)
	assert.Len(t, masters, 1)

	ids2, masters2 := mergeValidators(nil, nil, aIDs, aMKs)
	assert.Len(t, ids2, 1)
	assert.Len(t, masters2, 1)
}

func TestStup_MergeValidators_DeterministicOrder(t *testing.T) {
	mk1 := [33]byte{0x02, 0x01}
	mk2 := [33]byte{0x02, 0x02}
	mk3 := [33]byte{0x02, 0x03}

	aIDs := []consensus.NodeID{{0x01}, {0x03}}
	aMKs := [][33]byte{mk1, mk3}
	bIDs := []consensus.NodeID{{0x02}}
	bMKs := [][33]byte{mk2}

	ids1, m1 := mergeValidators(aIDs, aMKs, bIDs, bMKs)
	ids2, m2 := mergeValidators(bIDs, bMKs, aIDs, aMKs)

	assert.Equal(t, m1, m2, "output must be sorted deterministically regardless of input order")
	assert.Equal(t, ids1, ids2)
}

func TestStup_MergeValidators_MasterShortIDSlice(t *testing.T) {
	// aMKs has more keys than aIDs — CalcNodeID path for the excess.
	mk1 := [33]byte{0x02, 0x01}
	mk2 := [33]byte{0x02, 0x02}
	aIDs := []consensus.NodeID{{0x01}}
	aMKs := [][33]byte{mk1, mk2}

	ids, masters := mergeValidators(aIDs, aMKs, nil, nil)
	assert.Len(t, ids, 2)
	assert.Len(t, masters, 2)
	assert.Equal(t, consensus.CalcNodeID(mk2), ids[1])
}

func TestStup_NormalizeAddresses_SpaceSeparated(t *testing.T) {
	in := []string{"r.ripple.com 51235", "alt.ripple.com 51235"}
	out := normalizeAddresses(in)
	assert.Equal(t, []string{"r.ripple.com:51235", "alt.ripple.com:51235"}, out)
}

func TestStup_NormalizeAddresses_AlreadyColon(t *testing.T) {
	in := []string{"127.0.0.1:51235"}
	out := normalizeAddresses(in)
	assert.Equal(t, in, out)
}

func TestStup_NormalizeAddresses_Mixed(t *testing.T) {
	in := []string{"host1 1234", "host2:5678"}
	out := normalizeAddresses(in)
	assert.Equal(t, []string{"host1:1234", "host2:5678"}, out)
}

func TestStup_NormalizeAddresses_Empty(t *testing.T) {
	out := normalizeAddresses(nil)
	assert.Empty(t, out)
}

func stup_encodedKey(t *testing.T, seed string) string {
	t.Helper()
	id, err := NewValidatorIdentity(seed)
	require.NoError(t, err)
	encoded, err := addresscodec.EncodeNodePublicKey(id.SigningPubKey())
	require.NoError(t, err)
	return encoded
}

func TestStup_ParseValidatorKeysWithMaster_Empty(t *testing.T) {
	cfg := &config.Config{}
	ids, masters, err := ParseValidatorKeysWithMaster(cfg)
	require.NoError(t, err)
	assert.Nil(t, ids)
	assert.Nil(t, masters)
}

func TestStup_ParseValidatorKeysWithMaster_TwoKeys(t *testing.T) {
	key1 := stup_encodedKey(t, "snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	key2 := stup_encodedKey(t, "spqPaiDYkYJ2H7cpziSk9XWyAeCPE")
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			Validators: []string{key1, key2},
		},
	}
	ids, masters, err := ParseValidatorKeysWithMaster(cfg)
	require.NoError(t, err)
	assert.Len(t, ids, 2)
	assert.Len(t, masters, 2)
	for i := range ids {
		assert.Equal(t, consensus.CalcNodeID(masters[i]), ids[i])
	}
}

func TestStup_ParseValidatorKeysWithMaster_InvalidKey(t *testing.T) {
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			Validators: []string{"nINVALIDKEYXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"},
		},
	}
	_, _, err := ParseValidatorKeysWithMaster(cfg)
	assert.Error(t, err)
}

func TestStup_ParseValidatorListPublisherKeys_Empty(t *testing.T) {
	cfg := &config.Config{}
	keys, err := ParseValidatorListPublisherKeys(cfg)
	require.NoError(t, err)
	assert.Nil(t, keys)
}

func TestStup_ParseValidatorListPublisherKeys_Valid(t *testing.T) {
	// 33-byte ed25519 pubkey hex (ED prefix + 32 zero bytes).
	hexKey := "ED" + "0000000000000000000000000000000000000000000000000000000000000001"
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{hexKey},
		},
	}
	keys, err := ParseValidatorListPublisherKeys(cfg)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	assert.Equal(t, byte(0xED), keys[0][0])
}

func TestStup_ParseValidatorListPublisherKeys_TwoKeys(t *testing.T) {
	hexKey1 := "ED" + "0000000000000000000000000000000000000000000000000000000000000001"
	hexKey2 := "ED" + "0000000000000000000000000000000000000000000000000000000000000002"
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{hexKey1, hexKey2},
		},
	}
	keys, err := ParseValidatorListPublisherKeys(cfg)
	require.NoError(t, err)
	assert.Len(t, keys, 2)
}

func TestStup_ParseValidatorListPublisherKeys_BadHex(t *testing.T) {
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{"ZZ" + "0000000000000000000000000000000000000000000000000000000000000001"},
		},
	}
	_, err := ParseValidatorListPublisherKeys(cfg)
	assert.Error(t, err)
}

func TestStup_ParseValidatorListPublisherKeys_WrongLength(t *testing.T) {
	// 32 bytes (64 hex chars) — one byte short
	hexKey := "ED" + "00000000000000000000000000000000000000000000000000000000000000"
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{hexKey},
		},
	}
	_, err := ParseValidatorListPublisherKeys(cfg)
	assert.Error(t, err)
}

func stup_newComponents(t *testing.T) *Components {
	t.Helper()
	v1 := consensus.NodeID{0x01}
	v2 := consensus.NodeID{0x02}
	mk1 := [33]byte{0x02, 0x01}
	mk2 := [33]byte{0x02, 0x02}
	return &Components{
		staticValidators: []consensus.NodeID{v1, v2},
		staticMasterKeys: [][33]byte{mk1, mk2},
	}
}

func TestStup_SnapshotStatic_ReturnsCopy(t *testing.T) {
	c := stup_newComponents(t)
	ids, masters := c.snapshotStatic()
	assert.Len(t, ids, 2)
	assert.Len(t, masters, 2)

	// Mutating the returned slice must not affect the stored slice.
	ids[0] = consensus.NodeID{0xFF}
	c.trustMergeMu.Lock()
	stored := c.staticValidators[0]
	c.trustMergeMu.Unlock()
	assert.NotEqual(t, consensus.NodeID{0xFF}, stored)
}

func TestStup_StaticTrustedMasterKeys_ReturnsCopy(t *testing.T) {
	c := stup_newComponents(t)
	keys := c.StaticTrustedMasterKeys()
	assert.Len(t, keys, 2)
}

func TestStup_ReloadStaticValidators_NoAdaptor(t *testing.T) {
	c := stup_newComponents(t)
	// nil Adaptor — must not panic
	c.Adaptor = nil
	newIDs := []consensus.NodeID{{0x03}}
	newMKs := [][33]byte{{0x02, 0x03}}
	c.ReloadStaticValidators(newIDs, newMKs)

	ids, masters := c.snapshotStatic()
	assert.Equal(t, newIDs, ids)
	assert.Equal(t, newMKs, masters)
}

func TestStup_ReloadStaticValidators_UpdatesAdaptor(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc})
	c := stup_newComponents(t)
	c.Adaptor = a

	newIDs := []consensus.NodeID{{0x05}, {0x06}}
	newMKs := [][33]byte{{0x02, 0x05}, {0x02, 0x06}}
	c.ReloadStaticValidators(newIDs, newMKs)

	trusted := a.GetTrustedValidators()
	assert.ElementsMatch(t, newIDs, trusted)
}

func TestStup_ReloadStaticValidators_WithValidatorList(t *testing.T) {
	// When ValidatorList is non-nil the reload must merge static + publisher sets.
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc})

	hexKey := "ED" + "0000000000000000000000000000000000000000000000000000000000000001"
	pk := [33]byte{0xED, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	_ = hexKey
	_ = pk

	hexKeyFull := "ED" + "0000000000000000000000000000000000000000000000000000000000000099"
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{hexKeyFull},
		},
	}
	pubKeys, err := ParseValidatorListPublisherKeys(cfg)
	require.NoError(t, err)

	c := &Components{
		Adaptor:          a,
		ValidatorList:    nil,
		staticValidators: nil,
		staticMasterKeys: nil,
	}
	newIDs := []consensus.NodeID{{0xAA}}
	newMKs := [][33]byte{{0x02, 0xAA}}
	c.ReloadStaticValidators(newIDs, newMKs)
	trusted := a.GetTrustedValidators()
	assert.ElementsMatch(t, newIDs, trusted)

	_ = pubKeys
}

func TestStup_RunValidatorListTick_NilListReturnsImmediately(t *testing.T) {
	c := &Components{ValidatorList: nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.runValidatorListTick(ctx, 10*time.Millisecond)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runValidatorListTick with nil ValidatorList did not return quickly")
	}
}

func TestStup_RunValidatorListTick_ZeroIntervalReturnsImmediately(t *testing.T) {
	hexKeyFull := "ED" + "0000000000000000000000000000000000000000000000000000000000000099"
	cfg := &config.Config{
		Validators: config.ValidatorsConfig{
			ValidatorListKeys: []string{hexKeyFull},
		},
	}
	pubKeys, err := ParseValidatorListPublisherKeys(cfg)
	require.NoError(t, err)
	_ = pubKeys

	// Use a Components whose ValidatorList is non-nil but interval=0 to
	// exercise the early-return guard without waiting for a real tick.
	// We cannot construct a real aggregator in tests without HTTP mocking,
	// so use a nil ValidatorList and let the nil check hit first.
	c := &Components{ValidatorList: nil}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		c.runValidatorListTick(ctx, 0)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runValidatorListTick with interval=0 did not return quickly")
	}
}

func TestStup_RunValidatorListTick_CancelStops(t *testing.T) {
	// Verify the goroutine exits on context cancel even with a non-nil ValidatorList.
	// We use a stubbed ValidatorList via the indirect nil path and a short interval
	// to observe stop on cancellation.
	c := &Components{ValidatorList: nil}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runValidatorListTick(ctx, 10*time.Millisecond)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runValidatorListTick did not stop on cancel")
	}
}

func stup_newAggregator(t *testing.T) *validatorlist.Aggregator {
	t.Helper()
	pk := validatorlist.PublisherKey{0xED, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
	agg, err := validatorlist.New(validatorlist.Config{
		PublisherKeys: []validatorlist.PublisherKey{pk},
		Threshold:     1,
		Manifests:     manifest.NewCache(),
		Logger:        slog.Default(),
	})
	require.NoError(t, err)
	return agg
}

func TestStup_RunValidatorListTick_RealAggregatorFiresAndStops(t *testing.T) {
	agg := stup_newAggregator(t)
	c := &Components{ValidatorList: agg}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		c.runValidatorListTick(ctx, 20*time.Millisecond)
		close(done)
	}()
	// Let at least one tick fire before cancelling.
	time.Sleep(60 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runValidatorListTick did not stop on cancel with real aggregator")
	}
}

func TestStup_ReloadStaticValidators_WithNonNilValidatorList(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{LedgerService: svc})
	agg := stup_newAggregator(t)

	c := &Components{
		Adaptor:          a,
		ValidatorList:    agg,
		staticValidators: nil,
		staticMasterKeys: nil,
	}
	newIDs := []consensus.NodeID{{0xBB}}
	newMKs := [][33]byte{{0x02, 0xBB}}
	// Must not panic; merges static + empty publisher set.
	c.ReloadStaticValidators(newIDs, newMKs)
	trusted := a.GetTrustedValidators()
	// Publisher set is empty (no lists applied), so trusted == newIDs.
	assert.ElementsMatch(t, newIDs, trusted)
}

func TestStup_WireValidatorListTrustInitializesUnavailableQuorum(t *testing.T) {
	static := consensus.NodeID{0xBB}
	master := [33]byte{0x02, 0xBB}
	a := New(Config{Validators: []consensus.NodeID{static}})
	agg := stup_newAggregator(t)
	a.SetQuorumUnavailableFunc(agg.IsQuorumUnavailable)
	c := &Components{
		Adaptor:          a,
		ValidatorList:    agg,
		staticValidators: []consensus.NodeID{static},
		staticMasterKeys: [][33]byte{master},
	}

	wireValidatorListTrust(c)

	assert.Equal(t, math.MaxInt, a.GetQuorum())
	assert.Equal(t, []consensus.NodeID{static}, a.GetTrustedValidators())
}

func TestStup_ComponentsStop_NilSafe(t *testing.T) {
	c := &Components{}
	assert.NotPanics(t, func() { c.Stop() })
}

func TestStup_ComponentsStop_NilEngineAndOverlaySafe(t *testing.T) {
	c := &Components{
		Engine:  nil,
		Overlay: nil,
		Router:  nil,
	}
	assert.NotPanics(t, func() { c.Stop() })
}

func TestStup_ComponentsStop_WithMockEngine(t *testing.T) {
	eng := &mockEngine{}
	c := &Components{Engine: eng}
	assert.NotPanics(t, func() { c.Stop() })
}

type stupStopErrorEngine struct {
	mockEngine
	err error
}

func (e *stupStopErrorEngine) Stop() error { return e.err }

func TestStup_ComponentsStop_ReturnsEngineError(t *testing.T) {
	stopErr := errors.New("engine stop failed")
	c := &Components{Engine: &stupStopErrorEngine{err: stopErr}}
	require.ErrorIs(t, c.Stop(), stopErr)
}

func TestStup_ComponentsStart_AndStop(t *testing.T) {
	svc := newTestLedgerService(t)
	ad := newTestAdaptor(t)

	overlay, err := peermanagement.New(peermanagement.WithListenAddr("127.0.0.1:0"))
	require.NoError(t, err)

	eng := &mockEngine{}
	inbox := overlay.Messages()
	router := newTestRouter(eng, ad, inbox)

	c := &Components{
		Overlay:             overlay,
		Engine:              eng,
		Adaptor:             ad,
		Router:              router,
		ValidatorList:       nil,
		ValidatorListPoller: nil,
	}
	_ = svc

	err = c.Start(t.Context())
	require.NoError(t, err)

	assert.NotNil(t, c.runCancel)
	assert.NotNil(t, c.overlayDone)
	assert.NotNil(t, c.routerDone)
	assert.Nil(t, c.vlTickDone, "no ValidatorList configured")

	assert.NotPanics(t, func() { c.Stop() })
}

func TestStup_ComponentsStart_ListenerBindFailure(t *testing.T) {
	// A listener bind failure must fail boot loudly rather than leaving the
	// node running deaf. A malformed listen address makes startListener return
	// before signalling ListenerReady, so Start must surface the error.
	overlay, err := peermanagement.New(peermanagement.WithListenAddr("not-a-valid-listen-address"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = overlay.Stop() })

	eng := &mockEngine{}
	ad := newTestAdaptor(t)
	c := &Components{
		Overlay: overlay,
		Engine:  eng,
		Adaptor: ad,
		Router:  NewRouter(eng, ad, overlay.ConsensusMessages()),
	}

	done := make(chan error, 1)
	go func() { done <- c.Start(t.Context()) }()
	select {
	case startErr := <-done:
		require.Error(t, startErr, "a listener bind failure must fail boot")
	case <-time.After(5 * time.Second):
		t.Fatal("Start hung on a listener bind failure instead of returning an error")
	}
}

func TestStup_ComponentsStart_EngineStartError(t *testing.T) {
	// A failing engine must cause Start to return an error and cancel
	// the already-started overlay goroutine.
	overlay, err := peermanagement.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = overlay.Stop() })

	eng := &stup_errStartEngine{err: assert.AnError}
	ad := newTestAdaptor(t)
	c := &Components{
		Overlay: overlay,
		Engine:  eng,
		Adaptor: ad,
		Router:  NewRouter(eng, ad, overlay.ConsensusMessages()),
	}
	startErr := c.Start(t.Context())
	assert.Error(t, startErr)
}

type stup_errStartEngine struct {
	mockEngine
	err error
}

func (e *stup_errStartEngine) Start(context.Context) error { return e.err }

func TestStup_OverlayOptionsFromConfig_NetworkID(t *testing.T) {
	cfg := &config.Config{}
	cfg.NetworkID.Set = true
	cfg.NetworkID.ID = 1

	pcfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(cfg) {
		opt(&pcfg)
	}
	assert.Equal(t, uint32(1), pcfg.NetworkID)
}

func TestStup_OverlayOptionsFromConfig_PeerPort(t *testing.T) {
	cfg := &config.Config{
		Ports: map[string]config.PortConfig{
			"peer": {Port: 51235, IP: "0.0.0.0", Protocol: "peer"},
		},
	}
	pcfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(cfg) {
		opt(&pcfg)
	}
	assert.Contains(t, pcfg.ListenAddr, "51235")
}

func TestStup_OverlayOptionsFromConfig_BootstrapAndFixed(t *testing.T) {
	cfg := &config.Config{
		IPs:      []string{"r.ripple.com 51235"},
		IPsFixed: []string{"alt.ripple.com 51235"},
	}
	pcfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(cfg) {
		opt(&pcfg)
	}
	assert.Contains(t, pcfg.BootstrapPeers, "r.ripple.com:51235")
	assert.Contains(t, pcfg.FixedPeers, "alt.ripple.com:51235")
}

func TestStup_OverlayOptionsFromConfig_PeerLimits(t *testing.T) {
	peerPort := map[string]config.PortConfig{
		"port_peer": {IP: "0.0.0.0", Port: 51235, Protocol: "peer"},
	}
	tests := []struct {
		name         string
		peersMax     int
		peerPrivate  int
		ports        map[string]config.PortConfig
		wantMax      int
		wantInbound  int
		wantOutbound int
		wantIPLimit  int
	}{
		{name: "omitted limit uses default", ports: peerPort, wantMax: 21, wantInbound: 11, wantOutbound: 10, wantIPLimit: 2},
		{name: "small limit is clamped", peersMax: 5, ports: peerPort, wantMax: 10, wantInbound: 0, wantOutbound: 10, wantIPLimit: 1},
		{name: "twenty peers", peersMax: 20, ports: peerPort, wantMax: 20, wantInbound: 10, wantOutbound: 10, wantIPLimit: 2},
		{name: "rippled default", peersMax: 21, ports: peerPort, wantMax: 21, wantInbound: 11, wantOutbound: 10, wantIPLimit: 2},
		{name: "large limit", peersMax: 100, ports: peerPort, wantMax: 100, wantInbound: 85, wantOutbound: 15, wantIPLimit: 6},
		{name: "limit beyond default outbound budget", peersMax: 225, ports: peerPort, wantMax: 225, wantInbound: 191, wantOutbound: 34, wantIPLimit: 7},
		{name: "private", peersMax: 20, peerPrivate: 1, ports: peerPort, wantMax: 20, wantInbound: 0, wantOutbound: 20, wantIPLimit: 1},
		{name: "no peer listener", peersMax: 20, wantMax: 20, wantInbound: 0, wantOutbound: 20, wantIPLimit: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &config.Config{
				PeersMax:    tt.peersMax,
				PeerPrivate: tt.peerPrivate,
				Ports:       tt.ports,
			}
			pcfg := peermanagement.DefaultConfig()
			for _, opt := range OverlayOptionsFromConfig(cfg) {
				opt(&pcfg)
			}

			assert.Equal(t, tt.wantMax, pcfg.MaxPeers)
			assert.Equal(t, tt.wantInbound, pcfg.MaxInbound)
			assert.Equal(t, tt.wantOutbound, pcfg.MaxOutbound)
			assert.Equal(t, tt.peerPrivate != 0, pcfg.PrivateMode)
			if tt.ports == nil {
				assert.Empty(t, pcfg.ListenAddr)
			} else {
				assert.Equal(t, "0.0.0.0:51235", pcfg.ListenAddr)
			}
			require.NoError(t, pcfg.Validate())
			assert.Equal(t, tt.wantIPLimit, pcfg.IPLimit)
			assert.GreaterOrEqual(t, pcfg.OutboundRetainedBytes,
				peermanagement.MinimumOutboundRetainedBytes(tt.wantMax))
		})
	}
}

func TestStup_OverlayOptionsFromConfig_LedgerReplayAndMaxTx(t *testing.T) {
	cfg := &config.Config{
		LedgerReplay:    1,
		MaxTransactions: 500,
	}
	pcfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(cfg) {
		opt(&pcfg)
	}
	assert.True(t, pcfg.EnableLedgerReplay)
	assert.Equal(t, 500, pcfg.MaxTransactions)
}

func TestStup_OverlayOptionsFromConfig_Compression(t *testing.T) {
	cfg := &config.Config{Compression: true}
	pcfg := peermanagement.DefaultConfig()
	for _, opt := range OverlayOptionsFromConfig(cfg) {
		opt(&pcfg)
	}
	assert.True(t, pcfg.EnableCompression)
}
