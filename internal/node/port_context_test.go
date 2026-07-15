package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/require"
)

func TestParsePortConfigCarriesAdminCredentials(t *testing.T) {
	pc, err := parsePortConfig("http", "admin", config.PortConfig{
		Admin:         []string{"127.0.0.0/8"},
		AdminUser:     "root",
		AdminPassword: "secret",
	})
	require.NoError(t, err)
	require.Equal(t, "root", pc.AdminUser)
	require.Equal(t, "secret", pc.AdminPassword)
	require.Len(t, pc.AdminNets, 1)
}
