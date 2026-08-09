package cli

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/require"
)

func TestMigrateRotationStateRequiresOwnershipConfirmation(t *testing.T) {
	cfg := &config.Config{}
	cfg.NodeDB.Path = t.TempDir()
	command := (&application{}).newMigrateRotationStateCommand()
	err := runMigrateRotationState(command, cfg, false)
	require.ErrorContains(t, err, "--confirm-generation-ownership")
}
