package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPeerHTTPConfigDefaults(t *testing.T) {
	cfg, err := LoadConfig(Paths{Main: writeConfig(t, t.TempDir(), "goxrpl.toml", minimalTestConfig())})
	require.NoError(t, err)

	assert.True(t, cfg.Crawl.OverlayEnabled())
	assert.True(t, cfg.Crawl.ServerEnabled())
	assert.False(t, cfg.Crawl.CountsEnabled())
	assert.True(t, cfg.Crawl.UNLEnabled())
	assert.True(t, cfg.VL.EnabledValue())
}

func TestPeerHTTPConfigRoundTripAndExplicitDisable(t *testing.T) {
	contents := minimalTestConfig() + `

[crawl]
enabled = true
overlay = false
server = false
counts = true
unl = false

[vl]
enabled = false
`
	cfg, err := LoadConfig(Paths{Main: writeConfig(t, t.TempDir(), "goxrpl.toml", contents)})
	require.NoError(t, err)

	assert.False(t, cfg.Crawl.OverlayEnabled())
	assert.False(t, cfg.Crawl.ServerEnabled())
	assert.True(t, cfg.Crawl.CountsEnabled())
	assert.False(t, cfg.Crawl.UNLEnabled())
	assert.False(t, cfg.VL.EnabledValue())
}

func TestPeerHTTPConfigRootDisable(t *testing.T) {
	contents := minimalTestConfig() + `

[crawl]
enabled = false
overlay = true
server = true
counts = true
unl = true
`
	cfg, err := LoadConfig(Paths{Main: writeConfig(t, t.TempDir(), "goxrpl.toml", contents)})
	require.NoError(t, err)

	assert.False(t, cfg.Crawl.OverlayEnabled())
	assert.False(t, cfg.Crawl.ServerEnabled())
	assert.False(t, cfg.Crawl.CountsEnabled())
	assert.False(t, cfg.Crawl.UNLEnabled())
}

func TestPeerHTTPConfigRejectsInvalidBoolean(t *testing.T) {
	contents := minimalTestConfig() + `

[crawl]
counts = 2
`
	_, err := LoadConfig(Paths{Main: writeConfig(t, t.TempDir(), "goxrpl.toml", contents)})
	require.ErrorContains(t, err, "crawl.counts must be a boolean or 0/1")
}

func TestPeerHTTPConfigAcceptsZeroOneValues(t *testing.T) {
	contents := minimalTestConfig() + `

[crawl]
overlay = 0
server = 1
counts = 1
unl = 0

[vl]
enabled = 0
`
	cfg, err := LoadConfig(Paths{Main: writeConfig(t, t.TempDir(), "goxrpl.toml", contents)})
	require.NoError(t, err)
	assert.False(t, cfg.Crawl.OverlayEnabled())
	assert.True(t, cfg.Crawl.ServerEnabled())
	assert.True(t, cfg.Crawl.CountsEnabled())
	assert.False(t, cfg.Crawl.UNLEnabled())
	assert.False(t, cfg.VL.EnabledValue())
}
