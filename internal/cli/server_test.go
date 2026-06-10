package cli

import (
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/assert"
)

// recordingSink captures every ReloadStaticValidators invocation so the
// SIGHUP-reload error paths can assert the reloader is NOT touched on
// bad inputs (the previous trusted set must be retained on any failure).
type recordingSink struct {
	mu    sync.Mutex
	calls []recordingSinkCall
}

type recordingSinkCall struct {
	validators []consensus.NodeID
	masterKeys [][33]byte
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

// TestReloadTrustedValidators_NilComponentsIsNoOp pins the standalone-mode
// guard: when the server is running without a consensus stack
// (consensusComponents nil — observer / RPC-only / tests), SIGHUP
// must be a complete no-op. Exercises the outer wrapper that
// applyValidatorReload sits behind in production.
func TestReloadTrustedValidators_NilComponentsIsNoOp(t *testing.T) {
	// Should not panic on nil components. No sink is reachable from
	// here (Components.Adaptor wiring is the only path), so the
	// success criterion is simply "doesn't crash".
	reloadTrustedValidators(xrpllog.Discard(), nil)
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
	doShutdown(nil, nil, nil, nil, nil, nil, nil, nil, nil, xrpllog.Discard())
}
