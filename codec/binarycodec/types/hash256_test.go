package types

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewHash256(t *testing.T) {
	hash := NewHash256()
	b, err := hash.FromJSON(strings.Repeat("00", 32))
	require.NoError(t, err)
	require.Len(t, b, 32)

	_, err = hash.FromJSON(strings.Repeat("00", 32+1))
	require.Error(t, err)
}
