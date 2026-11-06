package node

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	rpcadapter "github.com/LeJamon/go-xrpl/internal/rpc/adapter"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/stretchr/testify/require"
)

type httpProviderPeerSource struct{ count int }

func (p httpProviderPeerSource) PeersJSON() []map[string]any { return nil }
func (p httpProviderPeerSource) ClusterJSON() map[string]any { return nil }
func (p httpProviderPeerSource) PeerCount() int              { return p.count }

type httpProviderValidatorList struct {
	publishers []types.ValidatorListPublisherInfo
	sites      []types.ValidatorListSiteInfo
}

func (v httpProviderValidatorList) PublisherCount() int { return len(v.publishers) }
func (v httpProviderValidatorList) Threshold() int      { return 1 }
func (v httpProviderValidatorList) IsUNLBlocked() bool  { return false }
func (v httpProviderValidatorList) Publishers() []types.ValidatorListPublisherInfo {
	return v.publishers
}
func (v httpProviderValidatorList) Sites() []types.ValidatorListSiteInfo { return v.sites }
func (v httpProviderValidatorList) TrustedMasterKeys() [][33]byte        { return nil }
func (v httpProviderValidatorList) ListedValidators() []types.ListedValidator {
	return nil
}

func TestPeerServerInfoUsesMachineGuestShape(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("new ledger service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start ledger service: %v", err)
	}
	t.Cleanup(svc.Stop)

	graph := serverStatusTestServices(svc)
	ctx := &types.RpcContext{
		Role:       types.RoleGuest,
		PeerSource: httpProviderPeerSource{count: 9},
		Services:   graph,
	}
	first := peerServerInfo(ctx)
	second := peerServerInfo(ctx)
	if first == nil || second == nil {
		t.Fatal("peer server info returned nil")
	}
	if first["peers"] != 9 {
		t.Fatalf("peer count = %v, want 9", first["peers"])
	}
	first["peers"] = 1
	if second["peers"] != 9 {
		t.Fatalf("second snapshot changed after mutating first: peers=%v", second["peers"])
	}
	if closeInfo, ok := first["last_close"].(map[string]any); ok {
		closeInfo["proposers"] = 999
		if secondClose, ok := second["last_close"].(map[string]any); ok && secondClose["proposers"] == 999 {
			t.Fatal("nested snapshot changed after mutating first")
		}
	}
	third := peerServerInfo(ctx)
	if third["peers"] != 9 {
		t.Fatalf("fresh snapshot changed after mutating first: peers=%v", third["peers"])
	}
	for _, key := range []string{
		"hostid",
		"load_factor_fee_escalation",
		"load_factor_fee_queue",
		"validation_quorum",
		"node_size",
		"git",
		"pubkey_validator",
	} {
		if _, ok := first[key]; ok {
			t.Fatalf("machine crawl info leaked %q", key)
		}
	}
}

func TestPeerUNLInfoSuppressesAdminOnlyCollections(t *testing.T) {
	svc, err := service.New(service.Config{Standalone: true})
	if err != nil {
		t.Fatalf("new ledger service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start ledger service: %v", err)
	}
	t.Cleanup(svc.Stop)

	validatorList := httpProviderValidatorList{
		publishers: []types.ValidatorListPublisherInfo{{
			PublicKeyHex:     "ABCD",
			Available:        true,
			Sequence:         7,
			Version:          2,
			ExpirationUnix:   1,
			ValidatorsBase58: []string{"n9example"},
		}},
		sites: []types.ValidatorListSiteInfo{{
			URI:                "https://vl.example",
			RefreshIntervalMin: 15,
			NextRefreshISO:     "2026-Nov-06 12:00:00 UTC",
			LastRefreshISO:     "2026-Nov-06 11:45:00 UTC",
			LastDisposition:    "accepted",
			LastDispositionSet: true,
			LastError:          "transient warning",
		}},
	}
	services := types.NewServiceContainer(rpcadapter.NewLedgerServiceAdapter(svc))
	services.ValidatorList = validatorList
	graph := types.NewTestServiceGraph(services)
	got := peerUNLInfo(&types.RpcContext{Role: types.RoleGuest, Services: graph})
	for _, key := range []string{"signing_keys", "trusted_validator_keys", "validation_quorum"} {
		if _, ok := got[key]; ok {
			t.Fatalf("UNL crawl info leaked %q", key)
		}
	}
	publishers, ok := got["publisher_lists"].([]map[string]any)
	if !ok {
		t.Fatalf("publisher_lists = %#v, want array", got["publisher_lists"])
	}
	for _, publisher := range publishers {
		if _, ok := publisher["list"]; ok {
			t.Fatal("UNL crawl info leaked publisher list contents")
		}
	}
	if len(publishers) != 1 || publishers[0]["pubkey_publisher"] != "ABCD" {
		t.Fatalf("publisher metadata was not retained: %#v", publishers)
	}
	sites, ok := got["validator_sites"].([]map[string]any)
	if !ok || len(sites) != 1 {
		t.Fatalf("validator_sites = %#v, want non-nil array", got["validator_sites"])
	}
	if sites[0]["uri"] != "https://vl.example" || sites[0]["last_refresh_status"] != "accepted" || sites[0]["last_refresh_message"] != "transient warning" {
		t.Fatalf("validator site metadata was not retained: %#v", sites[0])
	}
}

func TestWirePeerHTTPProvidersBeforeOverlayListen(t *testing.T) {
	if !peertls.Supported() {
		t.Skip("peer TLS requires CGO")
	}
	svc, err := service.New(service.Config{})
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	t.Cleanup(svc.Stop)

	var state atomic.Value
	state.Store("full")
	svc.SetServerStateFunc(func() string { return state.Load().(string) })
	graph := serverStatusTestServices(svc)
	o, err := peermanagement.New(
		peermanagement.WithDataDir(t.TempDir()),
		peermanagement.WithListenAddr("127.0.0.1:0"),
		peermanagement.WithPrivateMode(true),
		peermanagement.WithHTTPConfig(peermanagement.HTTPConfig{
			CrawlServer: true,
		}),
	)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	r := &nodeRuntime{ctx: ctx, consensus: &adaptor.Components{Overlay: o}}
	r.wirePeerHTTPProviders(graph, o)
	runDone := make(chan error, 1)
	go func() { runDone <- o.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-runDone:
		case <-time.After(5 * time.Second):
			t.Error("overlay did not stop")
		}
		require.NoError(t, o.Stop())
	})
	select {
	case <-o.ListenerReady():
	case <-time.After(5 * time.Second):
		t.Fatal("overlay listener did not become ready")
	}

	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	getCrawl := func() map[string]any {
		resp, err := client.Get("https://" + o.ListenAddr() + "/crawl")
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, http.StatusOK, resp.StatusCode)
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.Unmarshal(body, &result))
		return result
	}

	first := getCrawl()
	require.Contains(t, first, "server")
	require.NotContains(t, first, "overlay")
	require.NotContains(t, first, "counts")
	require.NotContains(t, first, "unl")
	server, ok := first["server"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "full", server["server_state"])

	state.Store("syncing")
	second := getCrawl()
	server, ok = second["server"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "syncing", server["server_state"])
	resp, err := client.Get("https://" + o.ListenAddr() + "/health")
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	var health map[string]any
	require.NoError(t, json.Unmarshal(body, &health))
	info, ok := health["info"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "syncing", info["server_state"])
}
