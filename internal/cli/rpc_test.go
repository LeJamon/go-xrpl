package cli

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	"github.com/spf13/cobra"
)

func specByMethod(t *testing.T, method string) rpcCommandSpec {
	t.Helper()
	for _, s := range rpcCommandSpecs {
		if s.methodName() == method {
			return s
		}
	}
	t.Fatalf("no rpc command spec for method %q", method)
	return rpcCommandSpec{}
}

func TestRPCEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		ports   map[string]config.PortConfig
		want    string
		wantErr bool
	}{
		{
			name:  "single http port",
			ports: map[string]config.PortConfig{"rpc": {IP: "127.0.0.1", Port: 5005, Protocol: "http"}},
			want:  "http://127.0.0.1:5005/",
		},
		{
			name:  "wildcard ip rewritten to loopback",
			ports: map[string]config.PortConfig{"rpc": {IP: "0.0.0.0", Port: 5005, Protocol: "http"}},
			want:  "http://127.0.0.1:5005/",
		},
		{
			name: "admin port preferred over plain port",
			ports: map[string]config.PortConfig{
				"a_public": {IP: "127.0.0.1", Port: 80, Protocol: "http"},
				"b_admin":  {IP: "127.0.0.1", Port: 5005, Protocol: "http", Admin: []string{"127.0.0.1"}},
			},
			want: "http://127.0.0.1:5005/",
		},
		{
			name:    "no http ports",
			ports:   map[string]config.PortConfig{"peer": {IP: "0.0.0.0", Port: 51235, Protocol: "peer"}},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Ports: tc.ports}
			got, _, err := rpcEndpoint(cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("rpcEndpoint: %v", err)
			}
			if got != tc.want {
				t.Errorf("rpcEndpoint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRPCCommandSpecsParams(t *testing.T) {
	t.Run("account_tx parses ints and binary", func(t *testing.T) {
		p, err := specByMethod(t, "account_tx").params([]string{"rAcct", "10", "20", "5", "binary"})
		if err != nil {
			t.Fatal(err)
		}
		m := p.(map[string]any)
		if m["account"] != "rAcct" || m["ledger_index_min"] != 10 || m["ledger_index_max"] != 20 || m["limit"] != 5 || m["binary"] != true {
			t.Errorf("account_tx params = %v", m)
		}
	})

	t.Run("ledger identifier dispatch", func(t *testing.T) {
		build := specByMethod(t, "ledger").params
		cur, _ := build([]string{"validated"})
		if cur.(map[string]any)["ledger_index"] != "validated" {
			t.Errorf("validated: %v", cur)
		}
		num, _ := build([]string{"12345"})
		if num.(map[string]any)["ledger_index"] != "12345" {
			t.Errorf("numeric: %v", num)
		}
		hash, _ := build([]string{"ABCDEF"})
		if hash.(map[string]any)["ledger_hash"] != "ABCDEF" {
			t.Errorf("hash: %v", hash)
		}
	})

	t.Run("ledger_entry key=value coercion", func(t *testing.T) {
		p, err := specByMethod(t, "ledger_entry").params([]string{"index=ABC", "binary=true", "x=7"})
		if err != nil {
			t.Fatal(err)
		}
		m := p.(map[string]any)
		if m["index"] != "ABC" || m["binary"] != true || m["x"] != 7 {
			t.Errorf("ledger_entry params = %v", m)
		}
		if _, err := specByMethod(t, "ledger_entry").params([]string{"bogus"}); err == nil {
			t.Error("expected error for non key=value arg")
		}
	})

	t.Run("submit one vs two args", func(t *testing.T) {
		build := specByMethod(t, "submit").params
		one, _ := build([]string{"DEADBEEF"})
		if one.(map[string]any)["tx_blob"] != "DEADBEEF" {
			t.Errorf("one arg: %v", one)
		}
		two, _ := build([]string{"sEd", "{}"})
		if two.(map[string]any)["secret"] != "sEd" {
			t.Errorf("two args: %v", two)
		}
	})

	t.Run("no-param command has nil builder", func(t *testing.T) {
		if specByMethod(t, "ping").params != nil {
			t.Error("ping should have no params builder")
		}
	})
}

// setTestConfig points the package-global config at addr (host:port) for the
// duration of a test.
func setTestConfig(t *testing.T, addr string) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	prevCfg, prevErr := globalConfig, globalConfigErr
	globalConfig = &config.Config{Ports: map[string]config.PortConfig{
		"rpc": {IP: host, Port: port, Protocol: "http", Admin: []string{"127.0.0.1"}},
	}}
	globalConfigErr = nil
	t.Cleanup(func() { globalConfig, globalConfigErr = prevCfg, prevErr })
}

func TestRunRPC_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":{"status":"success","server_state":"full"}}`))
	}))
	defer srv.Close()
	setTestConfig(t, srv.Listener.Addr().String())

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runRPC(cmd, "server_info", nil); err != nil {
		t.Fatalf("runRPC: %v", err)
	}
	if !strings.Contains(out.String(), "server_state") {
		t.Errorf("output missing result fields: %q", out.String())
	}
}

func TestRunRPC_ErrorStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":{"status":"error","error":"actNotFound","error_message":"Account not found."}}`))
	}))
	defer srv.Close()
	setTestConfig(t, srv.Listener.Addr().String())

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	err := runRPC(cmd, "account_info", map[string]any{"account": "rBad"})
	if !errors.Is(err, cmdexit.ErrReported) {
		t.Fatalf("expected cmdexit.ErrReported, got %v", err)
	}
	if !strings.Contains(out.String(), "actNotFound") {
		t.Errorf("error detail not printed: %q", out.String())
	}
}

func TestRunRPC_NoConfig(t *testing.T) {
	prevCfg, prevErr := globalConfig, globalConfigErr
	globalConfig, globalConfigErr = nil, nil
	t.Cleanup(func() { globalConfig, globalConfigErr = prevCfg, prevErr })

	cmd := &cobra.Command{}
	cmd.SetOut(&strings.Builder{})
	if err := runRPC(cmd, "ping", nil); err == nil {
		t.Fatal("expected error when no config is loaded")
	}
}
