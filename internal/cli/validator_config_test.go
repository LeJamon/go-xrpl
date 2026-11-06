package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/node"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRPCWithEmptyValidatorTrust(t *testing.T) {
	for _, test := range []struct {
		file    string
		content string
	}{
		{"validators.txt", "[validators]\n"},
		{"validators.toml", "validator_list_keys = []\n"},
	} {
		t.Run(test.file, func(t *testing.T) {
			requests := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request struct {
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, err.Error(), http.StatusBadRequest)
					return
				}
				requests <- request.Method
				_, _ = io.WriteString(w, successfulRPCResponse)
			}))
			defer server.Close()

			dir := t.TempDir()
			path := filepath.Join(dir, "goxrpl.toml")
			port := testPort(t, server.Listener.Addr().String())
			content := strings.ReplaceAll(generateConfigContent("main"), "validators.toml", test.file)
			content = strings.Replace(content, "port = 5005", fmt.Sprintf("port = %d", port.Port), 1)
			require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, test.file), []byte(test.content), 0o600))

			var output bytes.Buffer
			root := NewRootCommand(IOStreams{Out: &output, ErrOut: io.Discard})
			root.SetArgs([]string{"rpc", "--conf", path, "ping"})
			_, err := root.ExecuteC()
			require.NoError(t, err)
			assert.JSONEq(t, `{"status":"success"}`, output.String())
			select {
			case method := <-requests:
				assert.Equal(t, "ping", method)
			default:
				t.Fatal("RPC request was not sent")
			}

			cfg, err := config.LoadConfig(config.Paths{Main: path})
			require.NoError(t, err)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = node.Run(ctx, cfg, path, false, service.StartupConfig{}, xrpllog.Discard(), xrpllog.Discard())
			require.ErrorContains(t, err, "trusted validator configuration is empty")
		})
	}
}
