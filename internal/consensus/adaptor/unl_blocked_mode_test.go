package adaptor

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/LeJamon/go-xrpl/internal/consensus"
)

func TestUNLBlockedOperatingMode(t *testing.T) {
	blocked := false
	a := New(Config{})
	a.SetUNLBlockedFunc(func() bool { return blocked })

	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())

	blocked = true
	a.SetTrustedValidators(nil, nil)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeTracking)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeDisconnected)
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())

	blocked = false
	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
}
