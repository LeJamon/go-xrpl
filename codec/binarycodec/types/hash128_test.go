package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHash128(t *testing.T) {
	hash := NewHash128()
	b, err := hash.FromJSON(strings.Repeat("00", 16))
	require.NoError(t, err)
	require.Len(t, b, 16)

	_, err = hash.FromJSON(strings.Repeat("00", 16+1))
	require.Error(t, err)
}
