package peermanagement

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOverlayNodePublicKeyAccessor(t *testing.T) {
	identity, err := NewIdentity()
	require.NoError(t, err)
	o := &Overlay{identity: identity}

	require.Equal(t, identity.EncodedPublicKey(), o.NodePublicKey())
	require.Empty(t, (&Overlay{}).NodePublicKey())
}
