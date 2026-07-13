package adaptor

import (
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
)

func TestOverlayOptionsFromConfigDataDir(t *testing.T) {
	appCfg := &config.Config{DatabasePath: "/var/lib/xrpld/db"}
	peerCfg := peermanagement.DefaultConfig()
	for _, option := range OverlayOptionsFromConfig(appCfg) {
		option(&peerCfg)
	}

	want := filepath.Join(appCfg.DatabasePath, "peers")
	if peerCfg.DataDir != want {
		t.Fatalf("DataDir = %q, want %q", peerCfg.DataDir, want)
	}

	peerCfg = peermanagement.DefaultConfig()
	for _, option := range OverlayOptionsFromConfig(&config.Config{}) {
		option(&peerCfg)
	}
	if peerCfg.DataDir != "" {
		t.Fatalf("empty database_path set DataDir to %q", peerCfg.DataDir)
	}
}
