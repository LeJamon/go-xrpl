package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateFixedIPEntryAllowsDefaultPeerPort(t *testing.T) {
	require.NoError(t, validateFixedIPEntry("fixed.example"))
	require.NoError(t, validateFixedIPEntry("2001:db8::1"))
	require.NoError(t, validateFixedIPEntry("fixed.example 0"))
	require.NoError(t, validateIPEntry("bootstrap.example 0"))
}
