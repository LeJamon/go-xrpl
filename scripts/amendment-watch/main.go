package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/protocol"
)

const (
	defaultRPCURL   = "https://s.altnet.rippletest.net:51234/"
	defaultTimeout  = 15 * time.Second
	maxLedgerAge    = 60 * time.Second
	maxResponseSize = 4 << 20
)

type rpcEndpoint struct {
	url     *url.URL
	display string
}

type serverHealth struct {
	networkID uint32
	ledgerAge time.Duration
}

type remoteFeature struct {
	id          [32]byte
	name        string
	enabled     bool
	hasMajority bool
}

type remoteFeatureJSON struct {
	Name     string          `json:"name"`
	Enabled  *bool           `json:"enabled"`
	Majority json.RawMessage `json:"majority"`
}

type rpcResult struct {
	Status       string                       `json:"status"`
	Error        string                       `json:"error"`
	ErrorMessage string                       `json:"error_message"`
	Features     map[string]remoteFeatureJSON `json:"features"`
}

type serverInfoResult struct {
	Status       string `json:"status"`
	Error        string `json:"error"`
	ErrorMessage string `json:"error_message"`
	Info         *struct {
		NetworkID *uint32 `json:"network_id"`
	} `json:"info"`
}

type ledgerResult struct {
	Status       string `json:"status"`
	Error        string `json:"error"`
	ErrorMessage string `json:"error_message"`
	Validated    *bool  `json:"validated"`
	Ledger       *struct {
		CloseTime *uint32 `json:"close_time"`
	} `json:"ledger"`
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("amendment-watch", flag.ContinueOnError)
	flags.SetOutput(stderr)
	rpcURL := flags.String("rpc-url", defaultRPCURL, "JSON-RPC endpoint to inspect")
	networkID := flags.Uint64("network-id", 1, "expected network ID")
	timeout := flags.Duration("timeout", defaultTimeout, "request timeout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "amendment-watch: unexpected positional arguments")
		return 2
	}
	if *timeout <= 0 {
		fmt.Fprintln(stderr, "amendment-watch: timeout must be greater than zero")
		return 2
	}
	if *networkID > math.MaxUint32 {
		fmt.Fprintln(stderr, "amendment-watch: network-id must fit in uint32")
		return 2
	}
	endpoint, err := parseRPCEndpoint(*rpcURL)
	if err != nil {
		fmt.Fprintf(stderr, "amendment-watch: invalid rpc-url: %v\n", err)
		return 2
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	client := &http.Client{
		Timeout:       *timeout,
		CheckRedirect: secureRedirectPolicy,
	}
	health, err := fetchServerHealth(ctx, client, endpoint, uint32(*networkID))
	if err != nil {
		fmt.Fprintf(stderr, "amendment-watch: %v\n", err)
		return 1
	}
	remote, err := fetchRemoteFeatures(ctx, client, endpoint)
	if err != nil {
		fmt.Fprintf(stderr, "amendment-watch: %v\n", err)
		return 1
	}

	issues := compareRemoteFeatures(remote, localRegistry())
	if len(issues) != 0 {
		fmt.Fprintln(stderr, "amendment-watch: registry check failed:")
		for _, issue := range issues {
			fmt.Fprintf(stderr, "  - %s\n", issue)
		}
		return 1
	}

	fmt.Fprintf(stdout, "amendment-watch: OK: checked %d remote amendments on network %d (validated ledger age %s) against the local registry\n",
		len(remote), health.networkID, health.ledgerAge)
	return 0
}

func parseRPCEndpoint(value string) (rpcEndpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return rpcEndpoint{}, errors.New("malformed URL")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return rpcEndpoint{}, errors.New("scheme must be http or https")
	}
	if parsed.Host == "" {
		return rpcEndpoint{}, errors.New("host is required")
	}

	displayURL := *parsed
	displayURL.User = nil
	displayURL.Path = ""
	displayURL.RawPath = ""
	displayURL.RawQuery = ""
	displayURL.Fragment = ""
	return rpcEndpoint{url: parsed, display: displayURL.String()}, nil
}

func secureRedirectPolicy(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) > 0 && via[len(via)-1].URL.Scheme == "https" && req.URL.Scheme != "https" {
		return errors.New("refusing HTTPS to HTTP redirect")
	}
	return nil
}

func callRPC(ctx context.Context, client *http.Client, endpoint rpcEndpoint, method string, params map[string]any) (json.RawMessage, error) {
	body, err := json.Marshal(map[string]any{"method": method, "params": []any{params}})
	if err != nil {
		return nil, fmt.Errorf("encode %s RPC request: %w", method, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.url.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create %s RPC request: %w", method, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call %s RPC at %s: %w", method, endpoint.display, safeHTTPError(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s RPC at %s returned HTTP status %s", method, endpoint.display, resp.Status)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseSize+1))
	if err != nil {
		return nil, fmt.Errorf("read %s RPC response: %w", method, err)
	}
	if len(data) > maxResponseSize {
		return nil, fmt.Errorf("%s RPC response exceeds %d bytes", method, maxResponseSize)
	}

	var envelope struct {
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode %s RPC response: %w", method, err)
	}
	if len(envelope.Error) != 0 && string(envelope.Error) != "null" {
		return nil, fmt.Errorf("%s RPC returned a top-level error: %s", method, compactJSON(envelope.Error))
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return nil, fmt.Errorf("%s RPC response is missing result", method)
	}
	return envelope.Result, nil
}

func safeHTTPError(err error) error {
	var urlError *url.Error
	if errors.As(err, &urlError) {
		return urlError.Err
	}
	return err
}

func rpcStatusError(method, status, rpcError, message string) error {
	if status == "success" {
		return nil
	}
	detail := strings.TrimSpace(message)
	if detail == "" {
		detail = strings.TrimSpace(rpcError)
	}
	if detail == "" {
		return fmt.Errorf("%s RPC returned status %q", method, status)
	}
	return fmt.Errorf("%s RPC returned status %q: %s", method, status, detail)
}

func fetchServerHealth(ctx context.Context, client *http.Client, endpoint rpcEndpoint, expectedNetworkID uint32) (serverHealth, error) {
	resultJSON, err := callRPC(ctx, client, endpoint, "server_info", map[string]any{})
	if err != nil {
		return serverHealth{}, err
	}
	var result serverInfoResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return serverHealth{}, fmt.Errorf("decode server_info RPC result: %w", err)
	}
	if err := rpcStatusError("server_info", result.Status, result.Error, result.ErrorMessage); err != nil {
		return serverHealth{}, err
	}
	if result.Info == nil {
		return serverHealth{}, errors.New("server_info RPC result is missing info")
	}
	networkID := uint32(0)
	if result.Info.NetworkID == nil && expectedNetworkID != 0 {
		return serverHealth{}, errors.New("server_info RPC result is missing network_id")
	}
	if result.Info.NetworkID != nil {
		networkID = *result.Info.NetworkID
	}
	if networkID != expectedNetworkID {
		return serverHealth{}, fmt.Errorf("server_info RPC returned network_id %d, want %d", networkID, expectedNetworkID)
	}

	ledgerJSON, err := callRPC(ctx, client, endpoint, "ledger", map[string]any{
		"ledger_index": "validated",
		"transactions": false,
		"expand":       false,
	})
	if err != nil {
		return serverHealth{}, err
	}
	var ledger ledgerResult
	if err := json.Unmarshal(ledgerJSON, &ledger); err != nil {
		return serverHealth{}, fmt.Errorf("decode ledger RPC result: %w", err)
	}
	if err := rpcStatusError("ledger", ledger.Status, ledger.Error, ledger.ErrorMessage); err != nil {
		return serverHealth{}, err
	}
	if ledger.Validated == nil || !*ledger.Validated {
		return serverHealth{}, errors.New("ledger RPC did not return a validated ledger")
	}
	if ledger.Ledger == nil || ledger.Ledger.CloseTime == nil {
		return serverHealth{}, errors.New("ledger RPC result is missing ledger.close_time")
	}
	closeUnix := int64(*ledger.Ledger.CloseTime) + protocol.RippleEpochUnix
	ageSeconds := time.Now().Unix() - closeUnix
	maxAgeSeconds := int64(maxLedgerAge / time.Second)
	if ageSeconds > maxAgeSeconds {
		return serverHealth{}, fmt.Errorf("validated ledger is stale: close time age %s exceeds %s",
			time.Duration(ageSeconds)*time.Second, maxLedgerAge)
	}
	if ageSeconds < -maxAgeSeconds {
		return serverHealth{}, fmt.Errorf("validated ledger close time is %s in the future", time.Duration(-ageSeconds)*time.Second)
	}
	if ageSeconds < 0 {
		ageSeconds = 0
	}
	return serverHealth{
		networkID: networkID,
		ledgerAge: time.Duration(ageSeconds) * time.Second,
	}, nil
}

func fetchRemoteFeatures(ctx context.Context, client *http.Client, endpoint rpcEndpoint) ([]remoteFeature, error) {
	resultJSON, err := callRPC(ctx, client, endpoint, "feature", map[string]any{})
	if err != nil {
		return nil, err
	}
	var result rpcResult
	if err := json.Unmarshal(resultJSON, &result); err != nil {
		return nil, fmt.Errorf("decode feature RPC result: %w", err)
	}
	if err := rpcStatusError("feature", result.Status, result.Error, result.ErrorMessage); err != nil {
		return nil, err
	}
	if result.Features == nil {
		return nil, errors.New("feature RPC result is missing features")
	}
	if len(result.Features) == 0 {
		return nil, errors.New("feature RPC result contains no features")
	}

	keys := make([]string, 0, len(result.Features))
	for key := range result.Features {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	remote := make([]remoteFeature, 0, len(keys))
	seen := make(map[[32]byte]string, len(keys))
	for _, key := range keys {
		id, normalized, err := parseFeatureID(key)
		if err != nil {
			return nil, fmt.Errorf("feature RPC returned invalid amendment ID %q: %w", key, err)
		}
		if previous, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("feature RPC returned duplicate amendment ID %s as %q and %q", normalized, previous, key)
		}
		seen[id] = key

		entry := result.Features[key]
		entry.Name = strings.TrimSpace(entry.Name)
		if entry.Enabled == nil {
			return nil, fmt.Errorf("feature RPC amendment %s is missing enabled", featureLabel(normalized, entry.Name))
		}
		hasMajority := len(entry.Majority) != 0
		if hasMajority {
			if string(entry.Majority) == "null" {
				return nil, fmt.Errorf("feature RPC amendment %s has invalid majority: null", featureLabel(normalized, entry.Name))
			}
			var majority uint64
			if err := json.Unmarshal(entry.Majority, &majority); err != nil {
				return nil, fmt.Errorf("feature RPC amendment %s has invalid majority: %w", featureLabel(normalized, entry.Name), err)
			}
		}
		remote = append(remote, remoteFeature{
			id:          id,
			name:        entry.Name,
			enabled:     *entry.Enabled,
			hasMajority: hasMajority,
		})
	}

	sort.Slice(remote, func(i, j int) bool {
		return strings.Compare(featureIDString(remote[i].id), featureIDString(remote[j].id)) < 0
	})
	return remote, nil
}

func compareRemoteFeatures(remote []remoteFeature, local map[[32]byte]*amendment.Feature) []string {
	issues := make([]string, 0)
	for _, feature := range remote {
		id := featureIDString(feature.id)
		localFeature := local[feature.id]
		if localFeature == nil {
			issues = append(issues, fmt.Sprintf(
				"remote amendment %s is not in the local registry; add it as SupportedNo until it is implemented",
				featureLabel(id, feature.name),
			))
			continue
		}
		if feature.name != "" && localFeature.Name != feature.name {
			issues = append(issues, fmt.Sprintf(
				"remote amendment %s is named %q but the local registry names it %q; verify the RPC endpoint and registry",
				id, feature.name, localFeature.Name,
			))
		}
		if localFeature.Supported != amendment.SupportedNo {
			continue
		}
		if feature.enabled {
			issues = append(issues, fmt.Sprintf(
				"unsupported amendment %s (%s) is enabled remotely; implement it and mark it SupportedYes before running this build",
				id, localFeature.Name,
			))
		}
		if feature.hasMajority {
			issues = append(issues, fmt.Sprintf(
				"unsupported amendment %s (%s) holds majority remotely; implement it before activation",
				id, localFeature.Name,
			))
		}
	}
	return issues
}

func featureLabel(id, name string) string {
	if name == "" {
		return id
	}
	return fmt.Sprintf("%s (%s)", id, name)
}

func localRegistry() map[[32]byte]*amendment.Feature {
	features := amendment.AllFeatures()
	registry := make(map[[32]byte]*amendment.Feature, len(features))
	for _, feature := range features {
		registry[feature.ID] = feature
	}
	return registry
}

func parseFeatureID(value string) ([32]byte, string, error) {
	var id [32]byte
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return id, "", err
	}
	if len(decoded) != len(id) {
		return id, "", fmt.Errorf("got %d bytes, want %d", len(decoded), len(id))
	}
	copy(id[:], decoded)
	return id, featureIDString(id), nil
}

func featureIDString(id [32]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

func compactJSON(value json.RawMessage) string {
	var out bytes.Buffer
	if err := json.Compact(&out, value); err != nil {
		return string(value)
	}
	return out.String()
}
