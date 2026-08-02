package node

import (
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/LeJamon/go-xrpl/storage/relationaldb"
	"github.com/stretchr/testify/assert"
)

// recordingSink captures every ReloadStaticValidators invocation so the
// SIGHUP-reload error paths can assert the reloader is NOT touched on
// bad inputs (the previous trusted set must be retained on any failure).
type recordingSink struct {
	mu                 sync.Mutex
	calls              []recordingSinkCall
	validateErr        error
	validations        int
	publisherKeys      [][33]byte
	publisherSites     []string
	publisherThreshold int
	staticCount        int
}

type recordingSinkCall struct {
	validators []consensus.NodeID
	masterKeys [][33]byte
}

type shutdownErrorEngine struct {
	consensus.Engine
	err error
}

func (e *shutdownErrorEngine) Stop() error { return e.err }

type shutdownErrorNodeStore struct {
	nodestore.Database
	err error
}

func (s *shutdownErrorNodeStore) Close() error { return s.err }

type shutdownErrorRepositoryManager struct {
	relationaldb.RepositoryManager
	err error
}

func (m *shutdownErrorRepositoryManager) Close() error { return m.err }

func TestEffectivePeerFetchDepth(t *testing.T) {
	tests := []struct {
		name         string
		fetchDepth   uint32
		onlineDelete int
		want         uint32
	}{
		{name: "online delete clamps full history", fetchDepth: ^uint32(0), onlineDelete: 256, want: 256},
		{name: "smaller fetch depth wins", fetchDepth: 512, onlineDelete: 1024, want: 512},
		{name: "online delete disabled", fetchDepth: 512, onlineDelete: 0, want: 512},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, effectivePeerFetchDepth(tc.fetchDepth, tc.onlineDelete))
		})
	}
}

func TestServerInfoConfigSnapshot(t *testing.T) {
	cfg := &config.Config{
		Server:       config.ServerConfig{Ports: []string{"public", "blank-admin", "admin-user"}},
		ServerDomain: "example.test",
		Ports: map[string]config.PortConfig{
			"public": {
				Port:     5005,
				Protocol: "http",
			},
			"blank-admin": {
				Port:     6005,
				Protocol: "ws",
				Admin:    []string{"", "  "},
			},
			"admin-user": {
				Port:      6006,
				Protocol:  "ws",
				AdminUser: "operator",
			},
		},
	}

	snapshot := serverInfoConfigSnapshot(cfg)
	assert.Equal(t, "example.test", snapshot.ServerDomain)
	assert.Equal(t, "medium", snapshot.NodeSize)
	assert.Equal(t, []types.ServerInfoPortSnapshot{
		{Port: 5005, Protocol: "http"},
		{Port: 6005, Protocol: "ws"},
		{Port: 6006, Protocol: "ws", Admin: true},
	}, snapshot.Ports)
}

func TestIsFullGitHash(t *testing.T) {
	assert.True(t, isFullGitHash("81f392511234abcd81f392511234abcd81f39251"))
	assert.False(t, isFullGitHash("81f3925"))
	assert.False(t, isFullGitHash("81f392511234abcd81f392511234abcd81f3925z"))
}

func TestResolveBuildRevisionFallsBackToInjectedFullHash(t *testing.T) {
	const injected = "81f392511234abcd81f392511234abcd81f39251"
	assert.Equal(t, injected, resolveBuildRevision(&debug.BuildInfo{}, true, injected))
	assert.Empty(t, resolveBuildRevision(&debug.BuildInfo{}, true, "v3.2.0"))
}

func (s *recordingSink) ReloadStaticValidators(validators []consensus.NodeID, masterKeys [][33]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := make([]consensus.NodeID, len(validators))
	copy(v, validators)
	var mk [][33]byte
	if len(masterKeys) > 0 {
		mk = make([][33]byte, len(masterKeys))
		copy(mk, masterKeys)
	}
	s.calls = append(s.calls, recordingSinkCall{validators: v, masterKeys: mk})
}

func (s *recordingSink) ValidateValidatorReload(publisherKeys [][33]byte, publisherSites []string, publisherThreshold, staticCount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.validations++
	s.publisherKeys = append([][33]byte(nil), publisherKeys...)
	s.publisherSites = append([]string(nil), publisherSites...)
	s.publisherThreshold = publisherThreshold
	s.staticCount = staticCount
	return s.validateErr
}

// TestApplyValidatorReload_EmptyConfigPathIsNoOp pins the
// "no --conf path set" branch: a SIGHUP delivered to a node that
// wasn't started with --conf must not clear the trusted set; the
// helper warn-logs and returns without touching the sink. The
// previous trusted set is thereby retained, matching the doc-comment
// contract that "a bad reload must not wedge the node".
func TestApplyValidatorReload_EmptyConfigPathIsNoOp(t *testing.T) {
	sink := &recordingSink{}
	applyValidatorReload(xrpllog.Discard(), sink, "")
	assert.Empty(t, sink.calls, "empty configPath must not invoke ReloadStaticValidators")
}

// TestApplyValidatorReload_MissingFileIsNoOp pins the LoadConfig
// failure branch: a SIGHUP after the operator deletes or renames the
// config file must surface as an error log without disturbing the
// in-memory trusted set. Same retention contract as the empty-path
// case — the sink must not be called.
func TestApplyValidatorReload_MissingFileIsNoOp(t *testing.T) {
	sink := &recordingSink{}
	missing := filepath.Join(t.TempDir(), "does-not-exist.toml")
	applyValidatorReload(xrpllog.Discard(), sink, missing)
	assert.Empty(t, sink.calls, "nonexistent configPath must not invoke ReloadStaticValidators")
}

// TestApplyValidatorReload_MalformedFileIsNoOp pins the parse-failure
// branch: a config file present-but-corrupt (truncated TOML, invalid
// types, missing required fields) must NOT propagate through to the
// sink. Otherwise an operator who fat-fingers their config and HUPs
// would clear the UNL out from under a running validator.
func TestApplyValidatorReload_MalformedFileIsNoOp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "malformed.toml")
	// Unterminated string — TOML parse error before any field is read.
	require := assert.New(t)
	require.NoError(os.WriteFile(path, []byte("database_path = \"oops\n"), 0o600))

	sink := &recordingSink{}
	applyValidatorReload(xrpllog.Discard(), sink, path)
	assert.Empty(t, sink.calls, "malformed config must not invoke ReloadStaticValidators")
}

func TestApplyValidatorReload_RejectsEmptyTrust(t *testing.T) {
	path := writeReloadConfig(t, "")
	sink := &recordingSink{validateErr: errors.New("trusted validator configuration cannot be empty")}

	applyValidatorReload(xrpllog.Discard(), sink, path)

	assert.Equal(t, 1, sink.validations)
	assert.Empty(t, sink.calls)
}

func TestApplyValidatorReload_RejectsPublisherChanges(t *testing.T) {
	path := writeReloadConfig(t, `[validator_list_keys]
ED264807102805220DA0F312E71FC2C69E1552C9C5790F6C25E3729DEB573D5860
`)
	sink := &recordingSink{validateErr: errors.New("validator_list_keys changes require a node restart")}

	applyValidatorReload(xrpllog.Discard(), sink, path)

	assert.Equal(t, 1, sink.validations)
	assert.Len(t, sink.publisherKeys, 1)
	assert.Equal(t, 1, sink.publisherThreshold)
	assert.Zero(t, sink.staticCount)
	assert.Empty(t, sink.calls)
}

func TestApplyValidatorReload_AppliesStaticValidators(t *testing.T) {
	path := writeReloadConfig(t, `[validators]
n949f75evCHwgyP4fPVgaHqNHxUVN15PsJEZ3B3HnXPcPjcZAoy7
`)
	sink := &recordingSink{}

	applyValidatorReload(xrpllog.Discard(), sink, path)

	assert.Equal(t, 1, sink.validations)
	assert.Equal(t, 1, sink.staticCount)
	assert.Empty(t, sink.publisherKeys)
	assert.Empty(t, sink.publisherSites)
	assert.Zero(t, sink.publisherThreshold)
	assert.Len(t, sink.calls, 1)
	assert.Len(t, sink.calls[0].validators, 1)
}

func writeReloadConfig(t *testing.T, validators string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "xrpld.toml")
	content := `
database_path = "/tmp/test/db"
network_id = "main"
debug_logfile = "/tmp/test/debug.log"

[server]
ports = ["port_test"]

[port_test]
port = 8080
ip = "127.0.0.1"
protocol = "http"

[node_db]
type = "pebble"
path = "/tmp/test/db"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if validators != "" {
		if err := os.WriteFile(filepath.Join(dir, "validators.txt"), []byte(validators), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return path
}

// TestReloadTrustedValidators_NilComponentsIsNoOp pins the standalone-mode
// guard: when the server is running without a consensus stack
// (consensusComponents nil — observer / RPC-only / tests), SIGHUP
// must be a complete no-op. Exercises the outer wrapper that
// applyValidatorReload sits behind in production.
func TestReloadTrustedValidators_NilComponentsIsNoOp(t *testing.T) {
	// Should not panic on nil components. No sink is reachable from
	// here (Components.Adaptor wiring is the only path), so the
	// success criterion is simply "doesn't crash".
	reloadTrustedValidators(xrpllog.Discard(), nil, "")
}

// TestDoShutdown_ToleratesNilComponents pins the partial-init teardown
// contract: the deferred shutdown installed in runServer fires for whatever
// the init path managed to populate, so any component — including wsServer —
// may be nil when an early error return triggers it. doShutdown must drain
// and log without dereferencing a nil component. Before the wsServer guard, a
// startup that failed before the WebSocket server was constructed crashed
// here on wsServer.Close(), masking the real startup error with a panic.
func TestDoShutdown_ToleratesNilComponents(t *testing.T) {
	// All components nil reproduces the earliest failure path. The success
	// criterion is "doesn't crash": WebSocketServer.Close dereferences its
	// receiver on the first line (connectionsMutex.Lock), so a nil wsServer
	// would panic without the guard this test pins.
	if err := doShutdown(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, xrpllog.Discard()); err != nil {
		t.Fatal(err)
	}
}

func TestDoShutdownReturnsConsensusPersistenceFailure(t *testing.T) {
	want := errors.New("manifest persistence failed")
	components := &adaptor.Components{Engine: &shutdownErrorEngine{err: want}}
	err := doShutdown(nil, nil, nil, nil, nil, nil, components, nil, nil, nil, xrpllog.Discard())
	if !errors.Is(err, want) {
		t.Fatalf("doShutdown error = %v, want %v", err, want)
	}
}

func TestDoShutdownReturnsStorageCloseFailures(t *testing.T) {
	nodeStoreErr := errors.New("node store close failed")
	repositoryErr := errors.New("repository close failed")
	err := doShutdown(
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		nil,
		&shutdownErrorNodeStore{err: nodeStoreErr},
		&shutdownErrorRepositoryManager{err: repositoryErr},
		xrpllog.Discard(),
	)
	if !errors.Is(err, nodeStoreErr) {
		t.Fatalf("doShutdown error = %v, want %v", err, nodeStoreErr)
	}
	if !errors.Is(err, repositoryErr) {
		t.Fatalf("doShutdown error = %v, want %v", err, repositoryErr)
	}
}
