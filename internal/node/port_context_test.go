package node

import (
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/stretchr/testify/require"
)

func TestParsePortConfigCarriesAdminCredentials(t *testing.T) {
	pc, err := parsePortConfig("http", "admin", config.PortConfig{
		Admin:          []string{"127.0.0.0/8"},
		AdminUser:      "root",
		AdminPassword:  "secret",
		User:           "operator",
		Password:       "transport-secret",
		AllowedOrigins: []string{"https://console.example"},
	})
	require.NoError(t, err)
	require.Equal(t, "root", pc.AdminUser)
	require.Equal(t, "secret", pc.AdminPassword)
	require.Equal(t, "operator", pc.User)
	require.Equal(t, "transport-secret", pc.Password)
	require.Equal(t, []string{"https://console.example"}, pc.AllowedOrigins)
	require.Len(t, pc.AdminNets, 1)
}
