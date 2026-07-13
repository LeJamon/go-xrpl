package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestRunSuccess(t *testing.T) {
	supported := amendment.FeatureByName("DID")
	unsupported := amendment.FeatureByName("MPTokensV2")
	if supported == nil || unsupported == nil {
		t.Fatal("test amendments are missing from the registry")
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		request := decodeRPCRequest(t, r)
		switch request.Method {
		case "server_info":
			writeServerInfoResponse(t, w, 1, 2)
		case "ledger":
			writeLedgerResponse(t, w, 2*time.Second)
		case "feature":
			writeRPCResponse(t, w, map[string]any{
				featureIDString(supported.ID): map[string]any{
					"name":    supported.Name,
					"enabled": true,
				},
				featureIDString(unsupported.ID): map[string]any{
					"name":    unsupported.Name,
					"enabled": false,
				},
			})
		default:
			t.Errorf("unexpected RPC method %q", request.Method)
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-rpc-url", server.URL, "-timeout", "1s"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0; stderr: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "OK: checked 2 remote amendments on network 1") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunDetectsRegistryRisks(t *testing.T) {
	unsupported := amendment.FeatureByName("MPTokensV2")
	if unsupported == nil || unsupported.Supported != amendment.SupportedNo {
		t.Fatal("MPTokensV2 must be registered as unsupported for this test")
	}
	unknownID := strings.Repeat("A", 64)
	unknownBytes, _, err := parseFeatureID(unknownID)
	if err != nil || amendment.FeatureByID(unknownBytes) != nil {
		t.Fatal("test ID unexpectedly exists in the registry")
	}

	tests := []struct {
		name      string
		id        string
		entry     map[string]any
		wantError string
	}{
		{
			name: "unknown disabled amendment",
			id:   unknownID,
			entry: map[string]any{
				"enabled": false,
			},
			wantError: "is not in the local registry",
		},
		{
			name: "unsupported enabled amendment",
			id:   featureIDString(unsupported.ID),
			entry: map[string]any{
				"name":    unsupported.Name,
				"enabled": true,
			},
			wantError: "is enabled remotely",
		},
		{
			name: "unsupported majority amendment",
			id:   featureIDString(unsupported.ID),
			entry: map[string]any{
				"name":     unsupported.Name,
				"enabled":  false,
				"majority": 812345678,
			},
			wantError: "holds majority remotely",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch decodeRPCRequest(t, r).Method {
				case "server_info":
					writeServerInfoResponse(t, w, 1, 1)
				case "ledger":
					writeLedgerResponse(t, w, time.Second)
				case "feature":
					writeRPCResponse(t, w, map[string]any{tc.id: tc.entry})
				default:
					t.Error("unexpected RPC method")
				}
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			if code := run([]string{"-rpc-url", server.URL, "-timeout", "1s"}, &stdout, &stderr); code != 1 {
				t.Fatalf("run code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantError)
			}
		})
	}
}

func TestFetchRemoteFeaturesRejectsInvalidResponses(t *testing.T) {
	feature := amendment.FeatureByName("DID")
	id := featureIDString(feature.ID)
	tests := []struct {
		name       string
		statusCode int
		body       string
		wantError  string
	}{
		{
			name:       "HTTP status",
			statusCode: http.StatusServiceUnavailable,
			body:       `{}`,
			wantError:  "HTTP status 503 Service Unavailable",
		},
		{
			name:       "RPC status",
			statusCode: http.StatusOK,
			body:       `{"result":{"status":"error","error":"noNetwork"}}`,
			wantError:  `status "error": noNetwork`,
		},
		{
			name:       "missing features",
			statusCode: http.StatusOK,
			body:       `{"result":{"status":"success"}}`,
			wantError:  "missing features",
		},
		{
			name:       "missing enabled",
			statusCode: http.StatusOK,
			body:       fmt.Sprintf(`{"result":{"status":"success","features":{"%s":{"name":"DID"}}}}`, id),
			wantError:  "is missing enabled",
		},
		{
			name:       "invalid majority",
			statusCode: http.StatusOK,
			body:       fmt.Sprintf(`{"result":{"status":"success","features":{"%s":{"name":"DID","enabled":false,"majority":"soon"}}}}`, id),
			wantError:  "has invalid majority",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()

			client := &http.Client{Timeout: time.Second}
			_, err := fetchRemoteFeatures(context.Background(), client, testEndpoint(t, server.URL))
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestFetchRemoteFeaturesTimesOut(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		<-release
	}))

	client := &http.Client{Timeout: 20 * time.Millisecond}
	_, err := fetchRemoteFeatures(context.Background(), client, testEndpoint(t, server.URL))
	close(release)
	server.Close()
	if err == nil {
		t.Fatal("expected request timeout")
	}
}

func TestFetchRemoteFeaturesVerifiesTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeRPCResponse(t, w, map[string]any{})
	}))
	defer server.Close()

	client := &http.Client{Timeout: time.Second}
	_, err := fetchRemoteFeatures(context.Background(), client, testEndpoint(t, server.URL))
	if err == nil {
		t.Fatal("default TLS verification accepted an untrusted test certificate")
	}
}

func TestRunRejectsUnhealthyEndpoint(t *testing.T) {
	tests := []struct {
		name            string
		networkID       uint32
		validated       bool
		ledgerAge       time.Duration
		omitCloseTime   bool
		serverLedgerAge uint32
		wantError       string
	}{
		{
			name:          "missing ledger close time",
			networkID:     1,
			validated:     true,
			omitCloseTime: true,
			wantError:     "missing ledger.close_time",
		},
		{
			name:            "rippled stale ledger encoded as age zero",
			networkID:       1,
			validated:       true,
			ledgerAge:       1_000_001 * time.Second,
			serverLedgerAge: 0,
			wantError:       "validated ledger is stale",
		},
		{
			name:      "ledger is not validated",
			networkID: 1,
			validated: false,
			ledgerAge: time.Second,
			wantError: "did not return a validated ledger",
		},
		{
			name:      "wrong network",
			networkID: 2,
			validated: true,
			ledgerAge: time.Second,
			wantError: "network_id 2, want 1",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch got := decodeRPCRequest(t, r).Method; got {
				case "server_info":
					writeServerInfoResponse(t, w, tc.networkID, tc.serverLedgerAge)
				case "ledger":
					result := map[string]any{
						"status":    "success",
						"validated": tc.validated,
						"ledger":    map[string]any{},
					}
					if !tc.omitCloseTime {
						result["ledger"].(map[string]any)["close_time"] = rippleCloseTime(tc.ledgerAge)
					}
					writeResultResponse(t, w, result)
				default:
					t.Errorf("unexpected RPC method %q", got)
				}
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			if code := run([]string{"-rpc-url", server.URL, "-timeout", "1s"}, &stdout, &stderr); code != 1 {
				t.Fatalf("run code = %d, want 1", code)
			}
			if !strings.Contains(stderr.String(), tc.wantError) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), tc.wantError)
			}
		})
	}
}

func TestRunAcceptsOmittedMainnetNetworkID(t *testing.T) {
	feature := amendment.FeatureByName("DID")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch decodeRPCRequest(t, r).Method {
		case "server_info":
			writeResultResponse(t, w, map[string]any{
				"status": "success",
				"info":   map[string]any{},
			})
		case "ledger":
			writeLedgerResponse(t, w, time.Second)
		case "feature":
			writeRPCResponse(t, w, map[string]any{
				featureIDString(feature.ID): map[string]any{
					"name":    feature.Name,
					"enabled": true,
				},
			})
		}
	}))
	defer server.Close()

	var stdout, stderr bytes.Buffer
	if code := run([]string{"-rpc-url", server.URL, "-network-id", "0", "-timeout", "1s"}, &stdout, &stderr); code != 0 {
		t.Fatalf("run code = %d, want 0; stderr: %s", code, stderr.String())
	}
}

func TestFetchRemoteFeaturesRejectsHTTPSDowngrade(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer httpServer.Close()

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, httpServer.URL, http.StatusTemporaryRedirect)
	}))
	defer tlsServer.Close()

	client := tlsServer.Client()
	client.Timeout = time.Second
	client.CheckRedirect = secureRedirectPolicy
	_, err := fetchRemoteFeatures(context.Background(), client, testEndpoint(t, tlsServer.URL))
	if err == nil || !strings.Contains(err.Error(), "refusing HTTPS to HTTP redirect") {
		t.Fatalf("error = %v, want HTTPS downgrade rejection", err)
	}
}

func TestFetchRemoteFeaturesRedactsEndpointSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	rawURL := strings.Replace(server.URL, "http://", "http://alice:hunter2@", 1) + "/v1/path-secret?token=token-secret"
	_, err := fetchRemoteFeatures(context.Background(), &http.Client{Timeout: time.Second}, testEndpoint(t, rawURL))
	if err == nil {
		t.Fatal("expected HTTP status error")
	}
	for _, secret := range []string{"alice", "hunter2", "path-secret", "token-secret"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("error leaked %q: %v", secret, err)
		}
	}
}

func TestRunRedactsMalformedEndpointSecrets(t *testing.T) {
	const rawURL = "http://alice:hunter2@%gh/?token=token-secret"
	var stdout, stderr bytes.Buffer
	if code := run([]string{"-rpc-url", rawURL}, &stdout, &stderr); code != 2 {
		t.Fatalf("run code = %d, want 2", code)
	}
	for _, secret := range []string{"alice", "hunter2", "token-secret"} {
		if strings.Contains(stderr.String(), secret) {
			t.Fatalf("error leaked %q: %s", secret, stderr.String())
		}
	}
}

func TestCompareRemoteFeaturesAllowsLocalOnlyEntries(t *testing.T) {
	remoteID := amendment.FeatureID("RemoteDisabled")
	localOnlyID := amendment.FeatureID("LocalOnly")
	local := map[[32]byte]*amendment.Feature{
		remoteID: {
			Name:      "RemoteDisabled",
			ID:        remoteID,
			Supported: amendment.SupportedNo,
		},
		localOnlyID: {
			Name:      "LocalOnly",
			ID:        localOnlyID,
			Supported: amendment.SupportedNo,
		},
	}
	remote := []remoteFeature{{id: remoteID, name: "RemoteDisabled"}}
	if issues := compareRemoteFeatures(remote, local); len(issues) != 0 {
		t.Fatalf("compareRemoteFeatures() = %v, want no issues", issues)
	}
}

type testRPCRequest struct {
	Method string           `json:"method"`
	Params []map[string]any `json:"params"`
}

func decodeRPCRequest(t *testing.T, r *http.Request) testRPCRequest {
	t.Helper()
	var request testRPCRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		t.Errorf("decode request: %v", err)
	}
	if len(request.Params) != 1 {
		t.Errorf("params = %+v, want one parameter object", request.Params)
	}
	return request
}

func testEndpoint(t *testing.T, value string) rpcEndpoint {
	t.Helper()
	endpoint, err := parseRPCEndpoint(value)
	if err != nil {
		t.Fatalf("parse endpoint: %v", err)
	}
	return endpoint
}

func writeResultResponse(t *testing.T, w http.ResponseWriter, result map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"result": result}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func writeServerInfoResponse(t *testing.T, w http.ResponseWriter, networkID, age uint32) {
	t.Helper()
	writeResultResponse(t, w, map[string]any{
		"status": "success",
		"info": map[string]any{
			"network_id":       networkID,
			"validated_ledger": map[string]any{"age": age},
		},
	})
}

func rippleCloseTime(age time.Duration) uint32 {
	return uint32(time.Now().Add(-age).Unix() - protocol.RippleEpochUnix)
}

func writeLedgerResponse(t *testing.T, w http.ResponseWriter, age time.Duration) {
	t.Helper()
	writeResultResponse(t, w, map[string]any{
		"status":    "success",
		"validated": true,
		"ledger": map[string]any{
			"close_time": rippleCloseTime(age),
		},
	})
}

func writeRPCResponse(t *testing.T, w http.ResponseWriter, features map[string]any) {
	t.Helper()
	writeResultResponse(t, w, map[string]any{
		"status":   "success",
		"features": features,
	})
}
