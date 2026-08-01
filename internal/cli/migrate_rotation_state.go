package cli

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/config"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/spf13/cobra"
)

func (a *application) newMigrateRotationStateCommand() *cobra.Command {
	var confirmOwnership bool
	command := &cobra.Command{
		Use:   "migrate-rotation-state",
		Short: "Migrate an offline version-1 rotating-store manifest",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.requireConfig(true)
			if err != nil {
				return err
			}
			return runMigrateRotationState(cmd, cfg, confirmOwnership)
		},
	}
	command.Flags().BoolVar(
		&confirmOwnership,
		"confirm-generation-ownership",
		false,
		"confirm the server is stopped and both manifest generations belong to this store",
	)
	return command
}

func runMigrateRotationState(cmd *cobra.Command, cfg *config.Config, confirmOwnership bool) error {
	if cfg.NodeDB.Path == "" {
		return fmt.Errorf("configured node_db path is empty")
	}
	if !confirmOwnership {
		return fmt.Errorf(
			"verify that the server is stopped and both generations in %s.generations.json belong to this store, then pass --confirm-generation-ownership",
			cfg.NodeDB.Path,
		)
	}
	if err := kvpebble.MigrateLegacyRotationState(cfg.NodeDB.Path); err != nil {
		return fmt.Errorf("migrate rotating storage state: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Rotation state migration completed.")
	return nil
}
