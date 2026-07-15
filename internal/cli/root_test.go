package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInitConfigStandaloneSkipsValidators(t *testing.T) {
	previousConfigFile := configFile
	previousStandalone := standalone
	previousConfig := globalConfig
	previousConfigErr := globalConfigErr
	t.Cleanup(func() {
		configFile = previousConfigFile
		standalone = previousStandalone
		globalConfig = previousConfig
		globalConfigErr = previousConfigErr
	})

	configFile = filepath.Join(t.TempDir(), "xrpld.toml")
	require.NoError(t, os.WriteFile(configFile, []byte(generateConfigContent("main")), 0o600))
	standalone = true

	initConfig()

	require.NoError(t, globalConfigErr)
	require.NotNil(t, globalConfig)
	assert.Empty(t, globalConfig.Validators.Validators)
	assert.Empty(t, globalConfig.Validators.ValidatorListKeys)
}
