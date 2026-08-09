package node

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

func TestConfigureStandaloneNodeIdentityPersists(t *testing.T) {
	dir := t.TempDir()
	configure := func(standalone bool) (string, error) {
		runtime := &nodeRuntime{
			appConfig:  &config.Config{DatabasePath: dir},
			standalone: standalone,
			services:   types.NewServiceGraphBuilder(nil),
		}
		err := runtime.configureStandaloneNodeIdentity()
		return runtime.services.NodePublicKey, err
	}

	first, err := configure(true)
	if err != nil {
		t.Fatalf("first identity: %v", err)
	}
	second, err := configure(true)
	if err != nil {
		t.Fatalf("second identity: %v", err)
	}
	if first == "" || second != first {
		t.Fatalf("persistent keys = %q, %q", first, second)
	}
	keyPath := filepath.Join(dir, "peers", "node_identity.key")
	if info, err := os.Stat(keyPath); err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("identity file = %#v, %v", info, err)
	}

	nonStandalone, err := configure(false)
	if err != nil || nonStandalone != "" {
		t.Fatalf("consensus identity path mutated services: key=%q err=%v", nonStandalone, err)
	}
}
