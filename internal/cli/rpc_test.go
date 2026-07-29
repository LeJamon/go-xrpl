package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/cmdexit"
	rpcserver "github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	rpctypes "github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/spf13/cobra"
)

const successfulRPCResponse = `{"result":{"status":"success"}}`

func specByMethod(t *testing.T, method string) rpcCommandSpec {
	t.Helper()
	for _, spec := range rpcCommandSpecs {
		if spec.methodName() == method {
			return spec
		}
	}
	t.Fatalf("no rpc command spec for method %q", method)
	return rpcCommandSpec{}
}

func TestRPCCommandSpecsAreRegisteredHandlers(t *testing.T) {
	registry := rpctypes.NewMethodRegistry()
	handlers.RegisterAll(registry)

	seen := make(map[string]struct{}, len(rpcCommandSpecs))
	for _, spec := range rpcCommandSpecs {
		method := spec.methodName()
		if _, duplicate := seen[method]; duplicate {
			t.Errorf("duplicate rpc command spec for %q", method)
		}
		seen[method] = struct{}{}
		if _, registered := registry.Get(method); !registered {
			t.Errorf("rpc command %q has no handler registered by handlers.RegisterAll", method)
		}
		if spec.command().Args == nil {
			t.Errorf("rpc command %q has no positional argument validator", method)
		}
	}
}

func TestRPCUnsupportedFixedCommandsRemainGenericOnly(t *testing.T) {
	unsupported := map[string]struct{}{
		"path_find":      {},
		"nft_history":    {},
		"nfts_by_issuer": {},
		"nft_info":       {},
	}
	for _, spec := range rpcCommandSpecs {
		if _, found := unsupported[spec.methodName()]; found {
			t.Errorf("%q must remain available only through rpc json", spec.methodName())
		}
	}
}

func TestRPCEndpointSelection(t *testing.T) {
	tests := []struct {
		name    string
		ports   map[string]config.PortConfig
		want    string
		wantErr string
	}{
		{
			name:  "IPv4",
			ports: rpcPorts(config.PortConfig{IP: "127.0.0.1", Port: 5005, Protocol: "http"}),
			want:  "http://127.0.0.1:5005/",
		},
		{
			name:  "IPv6",
			ports: rpcPorts(config.PortConfig{IP: "::1", Port: 5005, Protocol: "http"}),
			want:  "http://[::1]:5005/",
		},
		{
			name:  "hostname",
			ports: rpcPorts(config.PortConfig{IP: "rpc.example.test", Port: 5005, Protocol: "http"}),
			want:  "http://rpc.example.test:5005/",
		},
		{
			name:  "empty wildcard",
			ports: rpcPorts(config.PortConfig{Port: 5005, Protocol: "http"}),
			want:  "http://127.0.0.1:5005/",
		},
		{
			name:  "IPv4 wildcard",
			ports: rpcPorts(config.PortConfig{IP: "0.0.0.0", Port: 5005, Protocol: "http"}),
			want:  "http://127.0.0.1:5005/",
		},
		{
			name:  "IPv6 wildcard",
			ports: rpcPorts(config.PortConfig{IP: "::", Port: 5005, Protocol: "http"}),
			want:  "http://[::1]:5005/",
		},
		{
			name: "IPv4 admin port preferred",
			ports: map[string]config.PortConfig{
				"a_public": {IP: "127.0.0.1", Port: 5005, Protocol: "http"},
				"z_admin":  {IP: "127.0.0.1", Port: 5006, Protocol: "http", Admin: []string{"127.0.0.1"}},
			},
			want: "http://127.0.0.1:5006/",
		},
		{
			name: "IPv6 admin port preferred",
			ports: map[string]config.PortConfig{
				"a_public": {IP: "::1", Port: 5005, Protocol: "http"},
				"z_admin":  {IP: "::1", Port: 5006, Protocol: "http", Admin: []string{"::1"}},
			},
			want: "http://[::1]:5006/",
		},
		{
			name: "wildcard bind may be admin capable",
			ports: map[string]config.PortConfig{
				"a_public": {IP: "127.0.0.1", Port: 5005, Protocol: "http"},
				"z_admin":  {IP: "0.0.0.0", Port: 5006, Protocol: "http", Admin: []string{"127.0.0.1"}},
			},
			want: "http://127.0.0.1:5006/",
		},
		{
			name: "hostname is not inferred to be admin",
			ports: map[string]config.PortConfig{
				"a_host":  {IP: "localhost", Port: 5005, Protocol: "http", Admin: []string{"127.0.0.1"}},
				"z_admin": {IP: "127.0.0.1", Port: 5006, Protocol: "http", Admin: []string{"127.0.0.1"}},
			},
			want: "http://127.0.0.1:5006/",
		},
		{
			name: "admin network must contain target",
			ports: map[string]config.PortConfig{
				"a_mismatch": {IP: "127.0.0.1", Port: 5005, Protocol: "http", Admin: []string{"192.0.2.0/24"}},
				"z_public":   {IP: "127.0.0.1", Port: 5006, Protocol: "http"},
			},
			want: "http://127.0.0.1:5005/",
		},
		{
			name: "invalid admin network",
			ports: map[string]config.PortConfig{
				"rpc": {IP: "127.0.0.1", Port: 5005, Protocol: "http", Admin: []string{"not-an-ip"}},
			},
			wantErr: "invalid admin configuration",
		},
		{
			name: "no HTTP ports",
			ports: map[string]config.PortConfig{
				"peer": {IP: "0.0.0.0", Port: 51235, Protocol: "peer"},
			},
			wantErr: "no HTTP port configured",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, _, err := rpcEndpoint(&config.Config{Ports: test.ports})
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("rpcEndpoint() error = %v, want error containing %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("rpcEndpoint(): %v", err)
			}
			if endpoint != test.want {
				t.Errorf("rpcEndpoint() = %q, want %q", endpoint, test.want)
			}
		})
	}
}

func TestRPCCommandWireEnvelopes(t *testing.T) {
	hash := strings.Repeat("AB", 32)
	tests := []struct {
		name   string
		method string
		args   []string
		want   string
	}{
		{
			name:   "no params are omitted",
			method: "ping",
			want:   `{"method":"ping"}`,
		},
		{
			name:   "server definitions hash",
			method: "server_definitions",
			args:   []string{"ABCDEF"},
			want:   `{"method":"server_definitions","params":[{"hash":"ABCDEF"}]}`,
		},
		{
			name:   "account info",
			method: "account_info",
			args:   []string{"rAccount", "validated"},
			want:   `{"method":"account_info","params":[{"account":"rAccount","ledger_index":"validated"}]}`,
		},
		{
			name:   "account channels",
			method: "account_channels",
			args:   []string{"rAccount", "rDestination", "closed"},
			want:   `{"method":"account_channels","params":[{"account":"rAccount","destination_account":"rDestination","ledger_index":"closed"}]}`,
		},
		{
			name:   "account currencies",
			method: "account_currencies",
			args:   []string{"rAccount", "current"},
			want:   `{"method":"account_currencies","params":[{"account":"rAccount","ledger_index":"current"}]}`,
		},
		{
			name:   "account lines",
			method: "account_lines",
			args:   []string{"rAccount", "rPeer", "validated"},
			want:   `{"method":"account_lines","params":[{"account":"rAccount","peer":"rPeer","ledger_index":"validated"}]}`,
		},
		{
			name:   "account NFTs",
			method: "account_nfts",
			args:   []string{"rAccount", "closed"},
			want:   `{"method":"account_nfts","params":[{"account":"rAccount","ledger_index":"closed"}]}`,
		},
		{
			name:   "account objects",
			method: "account_objects",
			args:   []string{"rAccount", "validated"},
			want:   `{"method":"account_objects","params":[{"account":"rAccount","ledger_index":"validated"}]}`,
		},
		{
			name:   "account offers",
			method: "account_offers",
			args:   []string{"rAccount", "current"},
			want:   `{"method":"account_offers","params":[{"account":"rAccount","ledger_index":"current"}]}`,
		},
		{
			name:   "gateway balances",
			method: "gateway_balances",
			args:   []string{"rIssuer", "validated", "rHot1", "rHot2"},
			want:   `{"method":"gateway_balances","params":[{"account":"rIssuer","ledger_index":"validated","hotwallet":["rHot1","rHot2"]}]}`,
		},
		{
			name:   "ledger data",
			method: "ledger_data",
			args:   []string{"validated", "25", "MARKER"},
			want:   `{"method":"ledger_data","params":[{"ledger_index":"validated","limit":25,"marker":"MARKER"}]}`,
		},
		{
			name:   "ledger entry",
			method: "ledger_entry",
			args:   []string{"index=ABCDEF", "binary=true"},
			want:   `{"method":"ledger_entry","params":[{"index":"ABCDEF","binary":true}]}`,
		},
		{
			name:   "transaction lookup",
			method: "tx",
			args:   []string{"TXHASH"},
			want:   `{"method":"tx","params":[{"transaction":"TXHASH"}]}`,
		},
		{
			name:   "transaction history",
			method: "tx_history",
			args:   []string{"10"},
			want:   `{"method":"tx_history","params":[{"start":10}]}`,
		},
		{
			name:   "structured transaction JSON",
			method: "submit_multisigned",
			args:   []string{`{"Account":"rAccount","Sequence":7}`},
			want:   `{"method":"submit_multisigned","params":[{"tx_json":{"Account":"rAccount","Sequence":7}}]}`,
		},
		{
			name:   "structured currency JSON",
			method: "book_offers",
			args:   []string{`{"currency":"USD","issuer":"rIssuer"}`, `{"currency":"XRP"}`, "rTaker", "validated", "25"},
			want:   `{"method":"book_offers","params":[{"taker_pays":{"currency":"USD","issuer":"rIssuer"},"taker_gets":{"currency":"XRP"},"taker":"rTaker","ledger_index":"validated","limit":25}]}`,
		},
		{
			name:   "feature accept vote",
			method: "feature",
			args:   []string{"fixAMMv1_1", "accept"},
			want:   `{"method":"feature","params":[{"feature":"fixAMMv1_1","vetoed":false}]}`,
		},
		{
			name:   "feature reject vote",
			method: "feature",
			args:   []string{"fixAMMv1_1", "reject"},
			want:   `{"method":"feature","params":[{"feature":"fixAMMv1_1","vetoed":true}]}`,
		},
		{
			name:   "noripple role",
			method: "noripple_check",
			args:   []string{"rAccount", "gateway", "closed"},
			want:   `{"method":"noripple_check","params":[{"account":"rAccount","role":"gateway","ledger_index":"closed"}]}`,
		},
		{
			name:   "account transaction uint32 ledger range",
			method: "account_tx",
			args:   []string{"rAccount", "0", "4294967295", "1", "binary"},
			want:   `{"method":"account_tx","params":[{"account":"rAccount","ledger_index_min":0,"ledger_index_max":4294967295,"limit":1,"binary":true}]}`,
		},
		{
			name:   "channel amount remains a string",
			method: "channel_verify",
			args:   []string{"EDPublic", "ABC123", "1000000", "Signature"},
			want:   `{"method":"channel_verify","params":[{"public_key":"EDPublic","channel_id":"ABC123","amount":"1000000","signature":"Signature"}]}`,
		},
		{
			name:   "ledger range uses rippled field names",
			method: "ledger_range",
			args:   []string{"10", "20"},
			want:   `{"method":"ledger_range","params":[{"start_ledger":10,"stop_ledger":20}]}`,
		},
		{
			name:   "numeric ledger selector",
			method: "ledger",
			args:   []string{"12345"},
			want:   `{"method":"ledger","params":[{"ledger_index":"12345"}]}`,
		},
		{
			name:   "ledger transaction expansion",
			method: "ledger",
			args:   []string{"validated", "tx"},
			want:   `{"method":"ledger","params":[{"ledger_index":"validated","transactions":true,"expand":true}]}`,
		},
		{
			name:   "hash ledger selector",
			method: "transaction_entry",
			args:   []string{"TXHASH", hash},
			want:   `{"method":"transaction_entry","params":[{"tx_hash":"TXHASH","ledger_hash":"` + hash + `"}]}`,
		},
		{
			name:   "ripple path find",
			method: "ripple_path_find",
			args:   []string{`{"source_account":"rSource","destination_account":"rDestination","destination_amount":"1"}`, "validated"},
			want:   `{"method":"ripple_path_find","params":[{"source_account":"rSource","destination_account":"rDestination","destination_amount":"1","ledger_index":"validated"}]}`,
		},
		{
			name:   "deposit authorization",
			method: "deposit_authorized",
			args:   []string{"rSource", "rDestination", "closed"},
			want:   `{"method":"deposit_authorized","params":[{"source_account":"rSource","destination_account":"rDestination","ledger_index":"closed"}]}`,
		},
		{
			name:   "NFT buy offers",
			method: "nft_buy_offers",
			args:   []string{"NFTID", "validated"},
			want:   `{"method":"nft_buy_offers","params":[{"nft_id":"NFTID","ledger_index":"validated"}]}`,
		},
		{
			name:   "NFT sell offers",
			method: "nft_sell_offers",
			args:   []string{"NFTID", "closed"},
			want:   `{"method":"nft_sell_offers","params":[{"nft_id":"NFTID","ledger_index":"closed"}]}`,
		},
		{
			name:   "validator manifest",
			method: "manifest",
			args:   []string{"PUBLICKEY"},
			want:   `{"method":"manifest","params":[{"public_key":"PUBLICKEY"}]}`,
		},
		{
			name:   "peer reservation add",
			method: "peer_reservations_add",
			args:   []string{"PUBLICKEY", "trusted peer"},
			want:   `{"method":"peer_reservations_add","params":[{"public_key":"PUBLICKEY","description":"trusted peer"}]}`,
		},
		{
			name:   "peer reservation delete",
			method: "peer_reservations_del",
			args:   []string{"PUBLICKEY"},
			want:   `{"method":"peer_reservations_del","params":[{"public_key":"PUBLICKEY"}]}`,
		},
	}

	covered := make(map[string]bool, len(tests))
	for _, test := range tests {
		covered[test.method] = true
		t.Run(test.name, func(t *testing.T) {
			body, _, err := executeCapturedRPCCommand(t, test.method, test.args, "")
			if err != nil {
				t.Fatalf("%s command: %v", test.method, err)
			}
			assertJSONEqual(t, body, test.want)
		})
	}
	for _, spec := range rpcCommandSpecs {
		if spec.params != nil && !covered[spec.methodName()] {
			t.Errorf("non-trivial RPC command %q has no wire-envelope test", spec.methodName())
		}
	}
}

func TestRPCCommandsRejectInvalidInputBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, successfulRPCResponse)
	}))
	defer server.Close()
	setTestConfig(t, server.Listener.Addr().String())

	tests := []struct {
		name   string
		method string
		args   []string
	}{
		{name: "too few arguments", method: "account_info"},
		{name: "too many arguments", method: "ping", args: []string{"unexpected"}},
		{name: "exact arity", method: "ledger_range", args: []string{"10"}},
		{name: "malformed JSON", method: "submit_multisigned", args: []string{"{"}},
		{name: "trailing JSON value", method: "submit_multisigned", args: []string{`{} {}`}},
		{name: "JSON array is not an object", method: "book_offers", args: []string{"[]", `{}`}},
		{name: "signed integer", method: "account_tx", args: []string{"rAccount", "not-an-integer"}},
		{name: "zero standard limit", method: "account_tx", args: []string{"rAccount", "0", "1", "0"}},
		{name: "unsigned integer", method: "ledger_range", args: []string{"-1", "20"}},
		{name: "zero ledger range", method: "ledger_range", args: []string{"0", "20"}},
		{name: "reversed ledger range", method: "ledger_range", args: []string{"20", "10"}},
		{name: "oversized ledger range", method: "ledger_range", args: []string{"1", "1002"}},
		{name: "enum", method: "feature", args: []string{"fixAMMv1_1", "abstain"}},
		{name: "role enum", method: "noripple_check", args: []string{"rAccount", "issuer"}},
		{name: "ledger selector", method: "ledger", args: []string{"latest"}},
		{name: "channel amount", method: "channel_verify", args: []string{"key", "channel", "drops", "signature"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := requests.Load()
			_, _, err := executeRPCCommand(t, specByMethod(t, test.method).command(), test.args, "")
			if err == nil {
				t.Fatalf("%s command succeeded with args %q", test.method, test.args)
			}
			if got := requests.Load(); got != before {
				t.Fatalf("server received %d request(s) after local validation failed", got-before)
			}
		})
	}

	t.Run("generic JSON", func(t *testing.T) {
		before := requests.Load()
		if err := jsonCmd.RunE(jsonCmd, []string{"server_info", "{"}); err == nil {
			t.Fatal("json command accepted malformed JSON")
		}
		if got := requests.Load(); got != before {
			t.Fatalf("server received %d request(s) after local JSON validation failed", got-before)
		}
	})
}

func TestRunRPCAuthentication(t *testing.T) {
	t.Run("admin credentials are request params, not Basic Auth", func(t *testing.T) {
		var body []byte
		var authorization string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			body, _ = io.ReadAll(request.Body)
			authorization = request.Header.Get("Authorization")
			_, _ = io.WriteString(w, successfulRPCResponse)
		}))
		defer server.Close()
		port := testPort(t, server.Listener.Addr().String())
		port.AdminUser = "admin"
		port.AdminPassword = "secret"
		setTestRPCPorts(t, rpcPorts(port))

		if err := runRPC(newRPCCommand(), "stop", nil); err != nil {
			t.Fatalf("runRPC(): %v", err)
		}
		assertJSONEqual(
			t,
			body,
			`{"method":"stop","params":[{"admin_user":"admin","admin_password":"secret"}]}`,
		)
		if authorization != "" {
			t.Fatalf("admin credentials sent as HTTP Authorization: %q", authorization)
		}
	})

	t.Run("ordinary credentials use Basic Auth", func(t *testing.T) {
		var gotUser, gotPassword string
		var gotAuth bool
		var body []byte
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			gotUser, gotPassword, gotAuth = request.BasicAuth()
			body, _ = io.ReadAll(request.Body)
			_, _ = io.WriteString(w, successfulRPCResponse)
		}))
		defer server.Close()
		port := testPort(t, server.Listener.Addr().String())
		port.User = "http-user"
		port.Password = "http-password"
		setTestRPCPorts(t, rpcPorts(port))

		if err := runRPC(newRPCCommand(), "ping", nil); err != nil {
			t.Fatalf("runRPC(): %v", err)
		}
		if !gotAuth || gotUser != "http-user" || gotPassword != "http-password" {
			t.Fatalf("BasicAuth() = %q, %q, %t", gotUser, gotPassword, gotAuth)
		}
		assertJSONEqual(t, body, `{"method":"ping"}`)
	})
}

func TestRunRPCRejectsCleartextCredentialsToNonLoopback(t *testing.T) {
	setTestRPCPorts(t, rpcPorts(config.PortConfig{
		IP:       "rpc.example.test",
		Port:     5005,
		Protocol: "http",
		User:     "http-user",
		Password: "http-password",
	}))

	var requests atomic.Int32
	setTestRPCClient(t, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return rpcHTTPResponse(http.StatusOK, successfulRPCResponse), nil
	})})

	err := runRPC(newRPCCommand(), "ping", nil)
	if err == nil || !strings.Contains(err.Error(), "refusing to send RPC credentials over cleartext HTTP") {
		t.Fatalf("runRPC() error = %v, want cleartext credential refusal", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("transport received %d requests despite refusal", requests.Load())
	}

	command := newRPCCommand()
	command.Flags().Bool(insecureRPCFlag, false, "")
	if err := command.Flags().Set(insecureRPCFlag, "true"); err != nil {
		t.Fatal(err)
	}
	if err := runRPC(command, "ping", nil); err != nil {
		t.Fatalf("runRPC() with --%s: %v", insecureRPCFlag, err)
	}
	if requests.Load() != 1 {
		t.Fatalf("transport received %d requests, want 1", requests.Load())
	}
}

func TestRunRPCDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirectedRequests.Add(1)
		_, _ = io.WriteString(w, successfulRPCResponse)
	}))
	defer redirected.Close()

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, redirected.URL, http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	setTestConfig(t, redirector.Listener.Addr().String())

	err := runRPC(newRPCCommand(), "ping", nil)
	if err == nil || !strings.Contains(err.Error(), "HTTP 307 Temporary Redirect") {
		t.Fatalf("runRPC() error = %v, want redirect status error", err)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", redirectedRequests.Load())
	}
}

func TestRunRPCRejectsNonSuccessfulHTTPStatus(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "request failed", status)
			}))
			defer server.Close()
			setTestConfig(t, server.Listener.Addr().String())

			err := runRPC(newRPCCommand(), "server_info", nil)
			want := "HTTP " + strconv.Itoa(status) + " " + http.StatusText(status)
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("runRPC() error = %v, want containing %q", err, want)
			}
		})
	}
}

func TestPrintRPCResultRejectsInvalidEnvelopes(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "malformed JSON", body: `{`, want: "malformed JSON"},
		{name: "non-object envelope", body: `[]`, want: "malformed JSON"},
		{name: "null envelope", body: `null`, want: "malformed JSON"},
		{name: "missing result", body: `{}`, want: "missing a result object"},
		{name: "null result", body: `{"result":null}`, want: "missing a result object"},
		{name: "string result", body: `{"result":"success"}`, want: "invalid result"},
		{name: "array result", body: `{"result":[]}`, want: "invalid result"},
		{name: "missing status", body: `{"result":{}}`, want: "invalid result status"},
		{name: "non-string status", body: `{"result":{"status":true}}`, want: "invalid result status"},
		{name: "unknown status", body: `{"result":{"status":"pending"}}`, want: "invalid result status"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output strings.Builder
			err := printRPCResult(&output, "test_method", []byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("printRPCResult() error = %v, want containing %q", err, test.want)
			}
			if output.Len() != 0 {
				t.Fatalf("invalid response produced output %q", output.String())
			}
		})
	}
}

func TestPrintRPCResultReportsXrplError(t *testing.T) {
	body := []byte(`{"result":{"status":"error","error":"actNotFound","error_message":"Account not found."}}`)
	var output strings.Builder
	err := printRPCResult(&output, "account_info", body)
	if !errors.Is(err, cmdexit.ErrReported) {
		t.Fatalf("printRPCResult() error = %v, want cmdexit.ErrReported", err)
	}
	if !strings.Contains(output.String(), "actNotFound") || !strings.Contains(output.String(), "Account not found.") {
		t.Fatalf("printed XRPL error is incomplete: %q", output.String())
	}
}

func TestReadLimitedRPCBodyBoundsReads(t *testing.T) {
	reader := &boundedCountingReader{remaining: 100}
	if _, err := readLimitedRPCBody(reader, 8); err == nil {
		t.Fatal("readLimitedRPCBody() accepted an oversized response")
	}
	if reader.read > 9 {
		t.Fatalf("readLimitedRPCBody() read %d bytes, limit is 9", reader.read)
	}
}

func TestRPCSecretCommandWireEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		method string
		args   []string
		want   string
	}{
		{
			name:   "submit",
			method: "submit",
			args:   []string{"--secret-stdin", `{"Account":"rAccount","Sequence":1}`},
			want:   `{"method":"submit","params":[{"secret":"source-secret","tx_json":{"Account":"rAccount","Sequence":1}}]}`,
		},
		{
			name:   "sign",
			method: "sign",
			args:   []string{"--secret-stdin", `{"Account":"rAccount"}`, "offline"},
			want:   `{"method":"sign","params":[{"secret":"source-secret","tx_json":{"Account":"rAccount"},"offline":true}]}`,
		},
		{
			name:   "sign_for",
			method: "sign_for",
			args:   []string{"--secret-stdin", "rSigner", `{"Account":"rAccount"}`, "offline"},
			want:   `{"method":"sign_for","params":[{"account":"rSigner","secret":"source-secret","tx_json":{"Account":"rAccount"},"offline":true}]}`,
		},
		{
			name:   "channel_authorize",
			method: "channel_authorize",
			args:   []string{"--secret-stdin", "ABC123", "1000000"},
			want:   `{"method":"channel_authorize","params":[{"secret":"source-secret","channel_id":"ABC123","amount":"1000000"}]}`,
		},
		{
			name:   "wallet_propose",
			method: "wallet_propose",
			args:   []string{"--secret-stdin"},
			want:   `{"method":"wallet_propose","params":[{"passphrase":"source-secret"}]}`,
		},
		{
			name:   "validation_create",
			method: "validation_create",
			args:   []string{"--secret-stdin"},
			want:   `{"method":"validation_create","params":[{"secret":"source-secret"}]}`,
		},
	}

	covered := make(map[string]bool, len(tests))
	for _, test := range tests {
		covered[test.method] = true
		t.Run(test.name, func(t *testing.T) {
			body, stderr, err := executeCapturedRPCCommand(t, test.method, test.args, "source-secret\n")
			if err != nil {
				t.Fatalf("%s command: %v", test.method, err)
			}
			assertJSONEqual(t, body, test.want)
			if strings.Contains(stderr, "source-secret") {
				t.Fatalf("stderr leaks secret: %q", stderr)
			}
		})
	}
	for _, spec := range rpcCommandSpecs {
		if spec.paramsWithSecret != nil && !covered[spec.methodName()] {
			t.Errorf("secret-backed RPC command %q has no wire-envelope test", spec.methodName())
		}
	}
}

func TestRPCDeprecatedPositionalSecretWarningDoesNotLeak(t *testing.T) {
	const secret = "deprecated-positional-secret"
	body, stderr, err := executeCapturedRPCCommand(
		t,
		"sign",
		[]string{secret, `{"Account":"rAccount"}`},
		"",
	)
	if err != nil {
		t.Fatalf("sign command: %v", err)
	}
	assertJSONEqual(
		t,
		body,
		`{"method":"sign","params":[{"secret":"`+secret+`","tx_json":{"Account":"rAccount"}}]}`,
	)
	if !strings.Contains(stderr, "positional secrets are deprecated") {
		t.Fatalf("missing deprecation warning: %q", stderr)
	}
	if strings.Contains(stderr, secret) {
		t.Fatalf("deprecation warning leaks secret: %q", stderr)
	}
}

func TestRPCSecretCommandsDoNotAdvertisePositionalSecrets(t *testing.T) {
	var found int
	for _, spec := range rpcCommandSpecs {
		if spec.paramsWithSecret == nil {
			continue
		}
		found++
		use := strings.ToLower(spec.use)
		if strings.Contains(use, "<secret") || strings.Contains(use, "[secret") ||
			strings.Contains(use, "<passphrase") || strings.Contains(use, "[passphrase") {
			t.Errorf("%s advertises a positional secret in Use: %q", spec.methodName(), spec.use)
		}
		command := spec.command()
		for _, name := range []string{"secret-prompt", "secret-file", "secret-stdin"} {
			if command.Flags().Lookup(name) == nil {
				t.Errorf("%s does not expose --%s", spec.methodName(), name)
			}
		}
	}
	if found == 0 {
		t.Fatal("no secret-backed RPC commands found")
	}
}

func TestRPCStopUsesAdminCredentialsWithRPCServer(t *testing.T) {
	tests := []struct {
		name          string
		adminUser     string
		adminPassword string
	}{
		{name: "admin network without credentials"},
		{name: "configured credentials", adminUser: "admin", adminPassword: "secret"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			shutdown := make(chan struct{}, 1)
			services := &rpctypes.ServiceContainer{
				ShutdownFunc: func() {
					shutdown <- struct{}{}
				},
			}
			server := rpcserver.NewServer(time.Second, services)
			_, adminNet, err := net.ParseCIDR("127.0.0.0/8")
			if err != nil {
				t.Fatal(err)
			}
			httpServer := httptest.NewServer(rpcserver.PortMiddleware(
				&rpcserver.PortContext{
					PortName:      "rpc",
					AdminNets:     []net.IPNet{*adminNet},
					AdminUser:     test.adminUser,
					AdminPassword: test.adminPassword,
				},
				nil,
				server,
			))
			defer httpServer.Close()

			port := testPort(t, httpServer.Listener.Addr().String())
			port.Admin = []string{"127.0.0.1"}
			port.AdminUser = test.adminUser
			port.AdminPassword = test.adminPassword
			setTestRPCPorts(t, rpcPorts(port))

			var output strings.Builder
			command := newRPCCommand()
			command.SetOut(&output)
			if err := runRPC(command, "stop", nil); err != nil {
				t.Fatalf("runRPC(stop): %v", err)
			}
			select {
			case <-shutdown:
			case <-time.After(time.Second):
				t.Fatal("stop RPC did not invoke the server shutdown hook")
			}
			if !strings.Contains(output.String(), "ripple server stopping") {
				t.Fatalf("stop output = %q", output.String())
			}
		})
	}
}

func TestRunRPCNoConfig(t *testing.T) {
	previousConfig, previousError := globalConfig, globalConfigErr
	globalConfig, globalConfigErr = nil, nil
	t.Cleanup(func() {
		globalConfig, globalConfigErr = previousConfig, previousError
	})

	if err := runRPC(newRPCCommand(), "ping", nil); err == nil {
		t.Fatal("runRPC() succeeded without a loaded config")
	}
}

func executeCapturedRPCCommand(
	t *testing.T,
	method string,
	args []string,
	input string,
) ([]byte, string, error) {
	t.Helper()
	var body []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, _ = io.ReadAll(request.Body)
		_, _ = io.WriteString(w, successfulRPCResponse)
	}))
	t.Cleanup(server.Close)
	setTestConfig(t, server.Listener.Addr().String())

	_, stderr, err := executeRPCCommand(t, specByMethod(t, method).command(), args, input)
	return body, stderr, err
}

func executeRPCCommand(
	t *testing.T,
	command *cobra.Command,
	args []string,
	input string,
) (string, string, error) {
	t.Helper()
	var stdout, stderr strings.Builder
	command.SetIn(strings.NewReader(input))
	command.SetOut(&stdout)
	command.SetErr(&stderr)
	if err := command.ParseFlags(args); err != nil {
		return stdout.String(), stderr.String(), err
	}
	positional := command.Flags().Args()
	if err := command.ValidateArgs(positional); err != nil {
		return stdout.String(), stderr.String(), err
	}
	err := command.RunE(command, positional)
	return stdout.String(), stderr.String(), err
}

func assertJSONEqual(t *testing.T, got []byte, want string) {
	t.Helper()
	decode := func(data []byte) any {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode JSON %q: %v", data, err)
		}
		if err := decoder.Decode(&struct{}{}); err != io.EOF {
			t.Fatalf("JSON %q has trailing data", data)
		}
		return value
	}
	gotValue := decode(got)
	wantValue := decode([]byte(want))
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Errorf("JSON mismatch\ngot:  %s\nwant: %s", got, want)
	}
}

func rpcPorts(port config.PortConfig) map[string]config.PortConfig {
	return map[string]config.PortConfig{"rpc": port}
}

func setTestConfig(t *testing.T, address string) {
	t.Helper()
	setTestRPCPorts(t, rpcPorts(testPort(t, address)))
}

func testPort(t *testing.T, address string) config.PortConfig {
	t.Helper()
	host, portString, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("split %q: %v", address, err)
	}
	port, err := strconv.Atoi(portString)
	if err != nil {
		t.Fatalf("parse port %q: %v", portString, err)
	}
	return config.PortConfig{
		IP:       host,
		Port:     port,
		Protocol: "http",
		Admin:    []string{host},
	}
}

func setTestRPCPorts(t *testing.T, ports map[string]config.PortConfig) {
	t.Helper()
	previousConfig, previousError := globalConfig, globalConfigErr
	globalConfig = &config.Config{Ports: ports}
	globalConfigErr = nil
	t.Cleanup(func() {
		globalConfig, globalConfigErr = previousConfig, previousError
	})
}

func setTestRPCClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := rpcHTTPClient
	rpcHTTPClient = client
	t.Cleanup(func() {
		rpcHTTPClient = previous
	})
}

func newRPCCommand() *cobra.Command {
	command := &cobra.Command{}
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	return command
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type boundedCountingReader struct {
	remaining int
	read      int
}

func (r *boundedCountingReader) Read(buffer []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := min(len(buffer), r.remaining)
	for i := range n {
		buffer[i] = 'x'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}

func rpcHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     strconv.Itoa(status) + " " + http.StatusText(status),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
