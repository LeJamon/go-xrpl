package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/stretchr/testify/require"
)

func TestGeneratedMainConfigStandaloneStartupAdvancesLedgers(t *testing.T) {
	t.Setenv("GOXRPL_PPROF", "")
	t.Setenv("GOXRPL_METRICS", "")
	t.Setenv("GOXRPL_PPROF_ALLOW_UNSAFE", "")

	dir := t.TempDir()
	t.Chdir(dir)
	configPath := filepath.Join(dir, "goxrpl.toml")
	generate := NewRootCommand(IOStreams{Out: io.Discard, ErrOut: io.Discard})
	generate.SetArgs([]string{"generate-config", "--network", "main", "--output", configPath})
	_, err := generate.ExecuteC()
	require.NoError(t, err)

	content, err := os.ReadFile(configPath)
	require.NoError(t, err)
	require.Contains(t, string(content), "online_delete = 512")
	require.Contains(t, string(content), "earliest_seq = 32570")

	ports := startupTestPorts(t)
	contentString := strings.NewReplacer(
		"[resources]\nmemory_mb = 0", "[resources]\nmemory_mb = 2048",
		"port = 5005", fmt.Sprintf("port = %d", ports[0]),
		"port = 2459", fmt.Sprintf("port = %d", ports[1]),
		"port = 6006", fmt.Sprintf("port = %d", ports[2]),
	).Replace(string(content))
	require.NoError(t, os.WriteFile(configPath, []byte(contentString), 0o600))

	loaded, err := config.LoadConfig(config.Paths{Main: configPath, SkipValidators: true})
	require.NoError(t, err)
	require.Equal(t, 512, loaded.NodeDB.OnlineDelete)
	require.Equal(t, 32570, loaded.NodeDB.EarliestSeq)

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	server := NewRootCommand(IOStreams{Out: io.Discard, ErrOut: io.Discard})
	server.SetArgs([]string{"--conf", configPath, "server", "--standalone", "--start"})
	serverDone := make(chan struct{})
	var serverErr error
	go func() {
		_, serverErr = server.ExecuteContextC(ctx)
		close(serverDone)
	}()
	defer func() {
		cancel()
		select {
		case <-serverDone:
			if serverErr != nil && !errors.Is(serverErr, context.Canceled) {
				t.Errorf("server shutdown: %v", serverErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("server did not shut down after context cancellation")
		}
	}()

	client := &http.Client{Timeout: 2 * time.Second}
	rpcURL := fmt.Sprintf("http://127.0.0.1:%d/", ports[0])
	waitForStartupRPC(t, client, rpcURL, serverDone, func() error { return serverErr })

	_, source, err := genesis.GenerateGenesisAccountID()
	require.NoError(t, err)
	_, destinationOne, err := genesis.GenerateAccountIDFromPassphrase("issue-2115-destination-one")
	require.NoError(t, err)
	_, destinationTwo, err := genesis.GenerateAccountIDFromPassphrase("issue-2115-destination-two")
	require.NoError(t, err)

	for _, payment := range []struct {
		sequence    uint32
		destination string
	}{
		{sequence: 1, destination: destinationOne},
		{sequence: 2, destination: destinationTwo},
	} {
		result := callStartupRPC(t, client, rpcURL, "submit", map[string]any{
			"secret": genesis.MasterPassphrase,
			"tx_json": map[string]any{
				"TransactionType": "Payment",
				"Account":         source,
				"Destination":     payment.destination,
				"Amount":          "20000000",
			},
		})
		require.Equal(t, "tesSUCCESS", result["engine_result"], "payment sequence %d", payment.sequence)
		require.Equal(t, float64(0), result["engine_result_code"], "payment sequence %d", payment.sequence)
		require.Equal(t, true, result["applied"], "payment sequence %d", payment.sequence)
	}

	for i := uint32(0); i < 3; i++ {
		result := callStartupRPC(t, client, rpcURL, "ledger_accept", map[string]any{})
		require.Equal(t, float64(4+i), result["ledger_current_index"], "ledger_accept %d", i+1)
	}

	for _, destination := range []string{destinationOne, destinationTwo} {
		result := callStartupRPC(t, client, rpcURL, "account_info", map[string]any{
			"account":      destination,
			"ledger_index": "validated",
		})
		accountData, ok := result["account_data"].(map[string]any)
		require.True(t, ok, "account_info account_data = %#v", result["account_data"])
		require.Equal(t, "20000000", accountData["Balance"], "destination %s", destination)
		require.Equal(t, float64(3), accountData["Sequence"], "destination %s", destination)
	}

	result := callStartupRPC(t, client, rpcURL, "account_info", map[string]any{
		"account":      source,
		"ledger_index": "validated",
	})
	accountData, ok := result["account_data"].(map[string]any)
	require.True(t, ok, "genesis account_data = %#v", result["account_data"])
	require.Equal(t, float64(3), accountData["Sequence"])
}

func startupTestPorts(t *testing.T) [3]int {
	t.Helper()
	var ports [3]int
	listeners := make([]net.Listener, 0, len(ports))
	defer func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}()
	for i := range ports {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		listeners = append(listeners, listener)
		_, portText, err := net.SplitHostPort(listener.Addr().String())
		require.NoError(t, err)
		ports[i], err = strconv.Atoi(portText)
		require.NoError(t, err)
	}
	return ports
}

func waitForStartupRPC(t *testing.T, client *http.Client, rpcURL string, serverDone <-chan struct{}, serverError func() error) {
	t.Helper()
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequest(http.MethodGet, rpcURL+"health", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_, _ = io.Copy(io.Discard, response.Body)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return
				}
			}
		}
		select {
		case <-serverDone:
			t.Fatalf("server exited before RPC readiness: %v", serverError())
		case <-deadline.C:
			t.Fatal("standalone RPC did not become ready")
		case <-ticker.C:
		}
	}
}

func callStartupRPC(t *testing.T, client *http.Client, rpcURL, method string, params map[string]any) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{
		"method": method,
		"params": []any{params},
	})
	require.NoError(t, err)
	request, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(body))
	require.NoError(t, err)
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	require.NoError(t, err)
	defer response.Body.Close()
	require.Equal(t, http.StatusOK, response.StatusCode)
	var envelope struct {
		Result map[string]any `json:"result"`
	}
	require.NoError(t, json.NewDecoder(response.Body).Decode(&envelope))
	require.NotNil(t, envelope.Result)
	require.NotContains(t, envelope.Result, "error", "RPC %s failed", method)
	return envelope.Result
}
