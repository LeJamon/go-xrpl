package node

import (
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/assert"
)

func TestReplayFaultPathUsesLocalStateDirectory(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "local database",
			cfg:  &config.Config{DatabasePath: "/var/lib/goxrpl"},
			want: filepath.Join("/var/lib/goxrpl", "replay-fault.json"),
		},
		{
			name: "node db beside postgres",
			cfg: &config.Config{
				DatabasePath: "postgres://user:secret@db.example/xrpl",
				NodeDB:       config.NodeDBConfig{Path: "/var/lib/goxrpl/nodestore/state.db"},
			},
			want: filepath.Join("/var/lib/goxrpl/nodestore", "replay-fault.json"),
		},
		{
			name: "no local state",
			cfg:  &config.Config{DatabasePath: "postgres://user:secret@db.example/xrpl"},
			want: "",
		},
		{
			name: "nil config",
			want: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assert.Equal(t, test.want, replayFaultPath(test.cfg))
		})
	}
}
