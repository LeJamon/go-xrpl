package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHash160(t *testing.T) {
	hash := NewHash160()
	b, err := hash.FromJSON(strings.Repeat("00", 20))
	require.NoError(t, err)
	require.Len(t, b, 20)

	_, err = hash.FromJSON(strings.Repeat("00", 20+1))
	require.Error(t, err)
}
