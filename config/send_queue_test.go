package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPortConfigSendQueueLimitValidation(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   int
		wantErr bool
	}{
		{name: "zero uses default", value: 0},
		{name: "minimum explicit value", value: 1},
		{name: "maximum explicit value", value: 65535},
		{name: "negative rejected", value: -1, wantErr: true},
		{name: "above uint16 rejected", value: 65536, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			port := PortConfig{
				Port:           8080,
				IP:             "127.0.0.1",
				Protocol:       "ws",
				SendQueueLimit: test.value,
			}
			err := port.Validate()
			if test.wantErr {
				require.Error(t, err)
				require.True(t, strings.Contains(err.Error(), "send_queue_limit"))
				return
			}
			require.NoError(t, err)
		})
	}
}
