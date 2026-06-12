package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHash192(t *testing.T) {
	hash := NewHash192()
	b, err := hash.FromJSON(strings.Repeat("00", 24))
	require.NoError(t, err)
	require.Len(t, b, 24)

	_, err = hash.FromJSON(strings.Repeat("00", 24+1))
	require.Error(t, err)
}
