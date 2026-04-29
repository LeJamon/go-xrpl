package peermanagement

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildHandshakeRequest tests building an HTTP upgrade request
// Reference: rippled Handshake.cpp makeRequest
func TestBuildHandshakeRequest(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	sharedValue := make([]byte, 32)
	for i := range sharedValue {
		sharedValue[i] = byte(i)
	}

	cfg := DefaultHandshakeConfig()
	cfg.UserAgent = "goXRPL-test/1.0"

	req, err := BuildHandshakeRequest(id, sharedValue, cfg)
	require.NoError(t, err)
	require.NotNil(t, req)

	// Check required headers
	assert.Equal(t, "GET", req.Method)
	assert.Equal(t, "/", req.URL.Path)

	// Upgrade header should contain protocol versions
	upgrade := req.Header.Get(HeaderUpgrade)
	assert.Contains(t, upgrade, "XRPL")
	assert.Contains(t, upgrade, "RTXP")

	// Connection header
	assert.Equal(t, "Upgrade", req.Header.Get(HeaderConnection))

	// Connect-As header
	assert.Equal(t, "Peer", req.Header.Get(HeaderConnectAs))

	// Public-Key header should start with 'n'
	pubKey := req.Header.Get(HeaderPublicKey)
	assert.True(t, strings.HasPrefix(pubKey, "n"))

	// Session-Signature should be base64 encoded
	sig := req.Header.Get(HeaderSessionSignature)
	assert.NotEmpty(t, sig)
	_, err = base64.StdEncoding.DecodeString(sig)
	assert.NoError(t, err, "signature should be valid base64")

	// User-Agent
	assert.Equal(t, "goXRPL-test/1.0", req.Header.Get(HeaderUserAgent))

	// Crawl header
	assert.Equal(t, "private", req.Header.Get(HeaderCrawl))
}

// TestBuildHandshakeRequest_WithNetworkID tests request with network ID
func TestBuildHandshakeRequest_WithNetworkID(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	sharedValue := make([]byte, 32)

	cfg := DefaultHandshakeConfig()
	cfg.NetworkID = 12345

	req, err := BuildHandshakeRequest(id, sharedValue, cfg)
	require.NoError(t, err)

	// Network-ID header should be set
	assert.Equal(t, "12345", req.Header.Get(HeaderNetworkID))
}

// TestBuildHandshakeResponse tests building an HTTP 101 response
// Reference: rippled Handshake.cpp makeResponse
func TestBuildHandshakeResponse(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	sharedValue := make([]byte, 32)
	for i := range sharedValue {
		sharedValue[i] = byte(i)
	}

	cfg := DefaultHandshakeConfig()
	cfg.CrawlPublic = true

	resp := BuildHandshakeResponse(id, sharedValue, cfg)
	require.NotNil(t, resp)

	// Status should be 101 Switching Protocols
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)

	// Check headers
	assert.Equal(t, "Upgrade", resp.Header.Get(HeaderConnection))
	assert.Contains(t, resp.Header.Get(HeaderUpgrade), "XRPL")
	assert.Equal(t, "Peer", resp.Header.Get(HeaderConnectAs))
	assert.Equal(t, "public", resp.Header.Get(HeaderCrawl))

	// Public key and signature
	assert.True(t, strings.HasPrefix(resp.Header.Get(HeaderPublicKey), "n"))
	assert.NotEmpty(t, resp.Header.Get(HeaderSessionSignature))

	// Server header — rippled's makeResponse (Handshake.cpp:408) sets
	// it to BuildInfo::getFullVersionString(); we emit cfg.UserAgent.
	assert.Equal(t, cfg.UserAgent, resp.Header.Get(HeaderServer))
}

// TestVerifyPeerHandshake tests handshake verification
// Reference: rippled Handshake.cpp verifyHandshake
func TestVerifyPeerHandshake(t *testing.T) {
	// Create two identities
	localId, err := NewIdentity()
	require.NoError(t, err)

	remoteId, err := NewIdentity()
	require.NoError(t, err)

	// Generate shared value
	sharedValue := make([]byte, 32)
	for i := range sharedValue {
		sharedValue[i] = byte(i + 100)
	}

	cfg := DefaultHandshakeConfig()

	// Create response headers from remote
	resp := BuildHandshakeResponse(remoteId, sharedValue, cfg)

	// Verify the handshake
	pubKey, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		localId.EncodedPublicKey(),
		cfg,
	)
	require.NoError(t, err)
	require.NotNil(t, pubKey)

	// Returned public key should match remote identity
	assert.Equal(t, remoteId.EncodedPublicKey(), pubKey.Encode())
}

// TestVerifyPeerHandshake_MissingPublicKey tests missing Public-Key header
func TestVerifyPeerHandshake_MissingPublicKey(t *testing.T) {
	headers := http.Header{}
	headers.Set(HeaderSessionSignature, "dummysig")

	_, err := VerifyPeerHandshake(headers, make([]byte, 32), "nLocalKey", DefaultHandshakeConfig())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Public-Key")
}

// TestVerifyPeerHandshake_MissingSignature tests missing Session-Signature header
func TestVerifyPeerHandshake_MissingSignature(t *testing.T) {
	remoteId, _ := NewIdentity()

	headers := http.Header{}
	headers.Set(HeaderPublicKey, remoteId.EncodedPublicKey())

	_, err := VerifyPeerHandshake(headers, make([]byte, 32), "nLocalKey", DefaultHandshakeConfig())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "Session-Signature")
}

// TestParsePublicKeyToken_RejectsEd25519Prefix pins R5.13: the 0xED
// (ed25519) key-type prefix must be rejected at parse time. Rippled
// requires secp256k1 for node public keys (Handshake.cpp:294-295) —
// an ed25519 validator key is different from a node key, and a peer
// advertising the wrong family is either misconfigured or hostile.
// Regression guard against a future btcec refactor that might
// accidentally accept 33-byte ed25519 keys (which share the same
// compressed length).
func TestParsePublicKeyToken_RejectsEd25519Prefix(t *testing.T) {
	// Build a synthetic node-pubkey payload with the ed25519 0xED
	// prefix in the key-bytes position. The token format is:
	//   [NodePublicKeyPrefix=0x1C] [33 key bytes starting with 0xED] [4-byte checksum]
	keyBytes := make([]byte, CompressedPubKeyLen)
	keyBytes[0] = 0xED
	for i := 1; i < CompressedPubKeyLen; i++ {
		keyBytes[i] = byte(i)
	}

	payload := make([]byte, 1+CompressedPubKeyLen)
	payload[0] = NodePublicKeyPrefix
	copy(payload[1:], keyBytes)
	checksum := doubleSHA256Identity(payload)[:ChecksumLen]
	full := append(payload, checksum...)
	encoded := addresscodec.EncodeBase58(full)

	_, err := ParsePublicKeyToken(encoded)
	require.Error(t, err, "ed25519-tagged node key must be rejected at parse time")
	assert.Contains(t, err.Error(), "ed25519",
		"error message should name the rejected key type for operator clarity")
}

// TestVerifyPeerHandshake_SelfConnection tests self-connection detection
// Reference: rippled Handshake.cpp verifyHandshake - "Self connection"
func TestVerifyPeerHandshake_SelfConnection(t *testing.T) {
	id, _ := NewIdentity()
	sharedValue := make([]byte, 32)

	cfg := DefaultHandshakeConfig()
	resp := BuildHandshakeResponse(id, sharedValue, cfg)

	// Try to verify with same identity (self-connection)
	_, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		id.EncodedPublicKey(), // Same as remote
		cfg,
	)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrSelfConnection)
}

// TestVerifyPeerHandshake_NetworkMismatch tests network ID mismatch
// Reference: rippled Handshake.cpp verifyHandshake - "Peer is on a different network"
func TestVerifyPeerHandshake_NetworkMismatch(t *testing.T) {
	localId, _ := NewIdentity()
	remoteId, _ := NewIdentity()
	sharedValue := make([]byte, 32)

	// Remote uses network ID 1
	remoteCfg := DefaultHandshakeConfig()
	remoteCfg.NetworkID = 1
	resp := BuildHandshakeResponse(remoteId, sharedValue, remoteCfg)

	// Local expects network ID 2
	localCfg := DefaultHandshakeConfig()
	localCfg.NetworkID = 2

	_, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		localId.EncodedPublicKey(),
		localCfg,
	)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrNetworkMismatch)
}

// TestVerifyPeerHandshake_MainnetAcceptsNonzeroNetworkIDFromPeer pins
// rippled parity for Handshake.cpp:241-250: when the local network is
// the default (NetworkID=0, equivalent to rippled's unseated optional),
// the Network-ID header is NOT checked. A mainnet node accepts a peer
// advertising any Network-ID value.
func TestVerifyPeerHandshake_MainnetAcceptsNonzeroNetworkIDFromPeer(t *testing.T) {
	localId, _ := NewIdentity()
	remoteId, _ := NewIdentity()
	sharedValue := make([]byte, 32)

	remoteCfg := DefaultHandshakeConfig()
	remoteCfg.NetworkID = 1
	resp := BuildHandshakeResponse(remoteId, sharedValue, remoteCfg)

	localCfg := DefaultHandshakeConfig() // NetworkID=0
	_, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		localId.EncodedPublicKey(),
		localCfg,
	)
	require.NoError(t, err,
		"rippled parity: mainnet must accept any peer Network-ID — Handshake.cpp only checks when local networkID is seated")
}

// TestVerifyPeerHandshake_NonDefaultAcceptsMissingNetworkID pins the
// symmetric case: rippled silently accepts peers that omit Network-ID
// even when the local node is on a non-default network. The header is
// only checked when present.
func TestVerifyPeerHandshake_NonDefaultAcceptsMissingNetworkID(t *testing.T) {
	localId, _ := NewIdentity()
	remoteId, _ := NewIdentity()
	sharedValue := make([]byte, 32)

	// Remote omits NetworkID by using the default (0).
	remoteCfg := DefaultHandshakeConfig()
	resp := BuildHandshakeResponse(remoteId, sharedValue, remoteCfg)

	// Local is on a non-default network.
	localCfg := DefaultHandshakeConfig()
	localCfg.NetworkID = 2

	_, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue,
		localId.EncodedPublicKey(),
		localCfg,
	)
	require.NoError(t, err,
		"rippled parity: missing Network-ID header is always accepted — Handshake.cpp:241 only enters the check when the header is present")
}

// TestVerifyPeerHandshake_InvalidSignature tests invalid signature rejection
func TestVerifyPeerHandshake_InvalidSignature(t *testing.T) {
	localId, _ := NewIdentity()
	remoteId, _ := NewIdentity()

	sharedValue1 := make([]byte, 32)
	sharedValue2 := make([]byte, 32)
	sharedValue2[0] = 0xFF // Different shared value

	cfg := DefaultHandshakeConfig()

	// Create response with sharedValue1
	resp := BuildHandshakeResponse(remoteId, sharedValue1, cfg)

	// Try to verify with different shared value
	_, err := VerifyPeerHandshake(
		resp.Header,
		sharedValue2, // Different!
		localId.EncodedPublicKey(),
		cfg,
	)
	assert.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidSignature)
}

// TestParseHandshakeProtocolVersion tests protocol version parsing
func TestParseHandshakeProtocolVersion(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"XRPL/2.2", "XRPL/2.2"},
		{"RTXP/1.2", "RTXP/1.2"},
		{"XRPL/2.2, RTXP/1.2", "XRPL/2.2"},
		{"  XRPL/2.0  ", "XRPL/2.0"},
		{"HTTP/1.1", ""},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := ParseHandshakeProtocolVersion(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestNetworkTime tests that network time is set in headers
func TestNetworkTime(t *testing.T) {
	id, _ := NewIdentity()
	sharedValue := make([]byte, 32)
	cfg := DefaultHandshakeConfig()

	req, err := BuildHandshakeRequest(id, sharedValue, cfg)
	require.NoError(t, err)

	// Network-Time should be present
	netTime := req.Header.Get(HeaderNetworkTime)
	assert.NotEmpty(t, netTime)
}

// TestDefaultHandshakeConfig tests default configuration values
func TestDefaultHandshakeConfig(t *testing.T) {
	cfg := DefaultHandshakeConfig()

	assert.Equal(t, "goXRPL/0.1.0", cfg.UserAgent)
	assert.Equal(t, uint32(0), cfg.NetworkID)
	assert.False(t, cfg.CrawlPublic)
}

// TestCrawlHeader tests crawl header values
func TestCrawlHeader(t *testing.T) {
	id, _ := NewIdentity()
	sharedValue := make([]byte, 32)

	// Test private crawl
	privateCfg := DefaultHandshakeConfig()
	privateCfg.CrawlPublic = false
	privateReq, _ := BuildHandshakeRequest(id, sharedValue, privateCfg)
	assert.Equal(t, "private", privateReq.Header.Get(HeaderCrawl))

	// Test public crawl
	publicCfg := DefaultHandshakeConfig()
	publicCfg.CrawlPublic = true
	publicReq, _ := BuildHandshakeRequest(id, sharedValue, publicCfg)
	assert.Equal(t, "public", publicReq.Header.Get(HeaderCrawl))
}

// TestFeatureString tests the Feature String method
func TestFeatureString(t *testing.T) {
	tests := []struct {
		feature  Feature
		expected string
	}{
		{FeatureValidatorListPropagation, "validatorListPropagation"},
		{FeatureLedgerReplay, "ledgerReplay"},
		{FeatureCompression, "compression"},
		{FeatureVpReduceRelay, "vpReduceRelay"},
		{FeatureTxReduceRelay, "txReduceRelay"},
		{FeatureTransactionBatching, "transactionBatching"},
		{Feature(99), "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.expected, func(t *testing.T) {
			assert.Equal(t, tt.expected, tt.feature.String())
		})
	}
}

// TestParseFeature tests parsing feature strings
func TestParseFeature(t *testing.T) {
	tests := []struct {
		input    string
		expected Feature
		ok       bool
	}{
		{"validatorlistpropagation", FeatureValidatorListPropagation, true},
		{"compression", FeatureCompression, true},
		{"reducerelay", FeatureReduceRelay, true},
		{"unknown_feature", 0, false},
		{"", 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			f, ok := ParseFeature(tt.input)
			assert.Equal(t, tt.ok, ok)
			if ok {
				assert.Equal(t, tt.expected, f)
			}
		})
	}
}

// TestFeatureSet tests the FeatureSet functionality
func TestFeatureSet(t *testing.T) {
	fs := NewFeatureSet()

	// Initially empty
	assert.False(t, fs.Has(FeatureCompression))

	// Enable a feature
	fs.Enable(FeatureCompression)
	assert.True(t, fs.Has(FeatureCompression))

	// Disable a feature
	fs.Disable(FeatureCompression)
	assert.False(t, fs.Has(FeatureCompression))
}

// TestFeatureSetIntersect tests feature set intersection
func TestFeatureSetIntersect(t *testing.T) {
	fs1 := NewFeatureSet()
	fs1.Enable(FeatureCompression)
	fs1.Enable(FeatureReduceRelay)

	fs2 := NewFeatureSet()
	fs2.Enable(FeatureCompression)
	fs2.Enable(FeatureLedgerReplay)

	intersection := fs1.Intersect(fs2)

	// Only compression should be in both
	assert.True(t, intersection.Has(FeatureCompression))
	assert.False(t, intersection.Has(FeatureReduceRelay))
	assert.False(t, intersection.Has(FeatureLedgerReplay))
}

// TestDefaultFeatureSet tests the default feature set
func TestDefaultFeatureSet(t *testing.T) {
	fs := DefaultFeatureSet()

	assert.True(t, fs.Has(FeatureCompression))
	assert.True(t, fs.Has(FeatureReduceRelay))
	assert.True(t, fs.Has(FeatureValidatorListPropagation))
}

// TestPeerCapabilities tests peer capabilities
func TestPeerCapabilities(t *testing.T) {
	pc := NewPeerCapabilities()

	assert.False(t, pc.SupportsCompression())
	assert.False(t, pc.SupportsReduceRelay())

	pc.Features.Enable(FeatureCompression)
	assert.True(t, pc.SupportsCompression())

	pc.Features.Enable(FeatureReduceRelay)
	assert.True(t, pc.SupportsReduceRelay())
}

// Reference: rippled src/test/overlay/handshake_test.cpp

// TestXProtocolCtlParsing tests X-Protocol-Ctl header parsing
// Reference: rippled handshake_test.cpp testHandshake()
func TestXProtocolCtlParsing(t *testing.T) {
	// Test header format: feature1=v1,v2,v3; feature2=v4; feature3=10; feature4=1; feature5=v6
	headers := http.Header{}
	headers.Set(HeaderProtocolCtl,
		"feature1=v1,v2,v3; feature2=v4; feature3=10; feature4=1; feature5=v6")

	// feature1 should NOT be "enabled" (enabled means =1)
	assert.False(t, FeatureEnabled(headers, "feature1"))

	// feature1 should NOT have value "2"
	assert.False(t, IsFeatureValue(headers, "feature1", "2"))

	// feature1 should have values v1, v2, v3
	assert.True(t, IsFeatureValue(headers, "feature1", "v1"))
	assert.True(t, IsFeatureValue(headers, "feature1", "v2"))
	assert.True(t, IsFeatureValue(headers, "feature1", "v3"))

	// feature2 should have value v4
	assert.True(t, IsFeatureValue(headers, "feature2", "v4"))

	// feature3=10 should NOT match "1"
	assert.False(t, IsFeatureValue(headers, "feature3", "1"))
	// feature3=10 should match "10"
	assert.True(t, IsFeatureValue(headers, "feature3", "10"))

	// feature4=1 should NOT match "10"
	assert.False(t, IsFeatureValue(headers, "feature4", "10"))
	// feature4=1 should match "1"
	assert.True(t, IsFeatureValue(headers, "feature4", "1"))
	// feature4 should be "enabled" (=1)
	assert.True(t, FeatureEnabled(headers, "feature4"))

	// "v6" is not a feature name, it's feature5's value
	assert.False(t, FeatureEnabled(headers, "v6"))
}

// TestGetFeatureValue tests GetFeatureValue function
func TestGetFeatureValue(t *testing.T) {
	tests := []struct {
		name       string
		header     string
		feature    string
		wantValue  string
		wantExists bool
	}{
		{
			name:       "simple_feature",
			header:     "compr=lz4",
			feature:    "compr",
			wantValue:  "lz4",
			wantExists: true,
		},
		{
			name:       "multiple_features",
			header:     "compr=lz4;ledgerreplay=1;vprr=1",
			feature:    "ledgerreplay",
			wantValue:  "1",
			wantExists: true,
		},
		{
			name:       "feature_with_spaces",
			header:     "compr=lz4 ; ledgerreplay=1 ; vprr=1",
			feature:    "vprr",
			wantValue:  "1",
			wantExists: true,
		},
		{
			name:       "comma_separated_values",
			header:     "compr=lz4,zstd,none",
			feature:    "compr",
			wantValue:  "lz4,zstd,none",
			wantExists: true,
		},
		{
			name:       "feature_not_found",
			header:     "compr=lz4;vprr=1",
			feature:    "txrr",
			wantValue:  "",
			wantExists: false,
		},
		{
			name:       "empty_header",
			header:     "",
			feature:    "compr",
			wantValue:  "",
			wantExists: false,
		},
		{
			name:       "case_insensitive",
			header:     "COMPR=lz4",
			feature:    "compr",
			wantValue:  "lz4",
			wantExists: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set(HeaderProtocolCtl, tt.header)
			}

			gotValue, gotExists := GetFeatureValue(headers, tt.feature)
			assert.Equal(t, tt.wantExists, gotExists)
			if gotExists {
				assert.Equal(t, tt.wantValue, gotValue)
			}
		})
	}
}

// TestIsFeatureValue tests IsFeatureValue function with multi-value features
func TestIsFeatureValue(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		feature string
		value   string
		want    bool
	}{
		{
			name:    "exact_match",
			header:  "compr=lz4",
			feature: "compr",
			value:   "lz4",
			want:    true,
		},
		{
			name:    "first_value_in_list",
			header:  "compr=lz4,zstd,none",
			feature: "compr",
			value:   "lz4",
			want:    true,
		},
		{
			name:    "middle_value_in_list",
			header:  "compr=lz4,zstd,none",
			feature: "compr",
			value:   "zstd",
			want:    true,
		},
		{
			name:    "last_value_in_list",
			header:  "compr=lz4,zstd,none",
			feature: "compr",
			value:   "none",
			want:    true,
		},
		{
			name:    "value_not_in_list",
			header:  "compr=lz4,zstd",
			feature: "compr",
			value:   "gzip",
			want:    false,
		},
		{
			name:    "partial_match_should_fail",
			header:  "compr=lz4",
			feature: "compr",
			value:   "lz",
			want:    false,
		},
		{
			name:    "case_insensitive_value",
			header:  "compr=LZ4",
			feature: "compr",
			value:   "lz4",
			want:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(HeaderProtocolCtl, tt.header)

			got := IsFeatureValue(headers, tt.feature, tt.value)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestFeatureEnabled tests FeatureEnabled function
func TestFeatureEnabled(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		feature string
		want    bool
	}{
		{
			name:    "feature_enabled",
			header:  "ledgerreplay=1",
			feature: "ledgerreplay",
			want:    true,
		},
		{
			name:    "feature_disabled_zero",
			header:  "ledgerreplay=0",
			feature: "ledgerreplay",
			want:    false,
		},
		{
			name:    "feature_with_other_value",
			header:  "compr=lz4",
			feature: "compr",
			want:    false,
		},
		{
			name:    "feature_not_present",
			header:  "vprr=1",
			feature: "txrr",
			want:    false,
		},
		{
			name:    "multiple_features_check_enabled",
			header:  "compr=lz4;ledgerreplay=1;vprr=1;txrr=0",
			feature: "vprr",
			want:    true,
		},
		{
			name:    "multiple_features_check_disabled",
			header:  "compr=lz4;ledgerreplay=1;vprr=1;txrr=0",
			feature: "txrr",
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			headers.Set(HeaderProtocolCtl, tt.header)

			got := FeatureEnabled(headers, tt.feature)
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestMakeFeaturesRequestHeader tests request header generation
func TestMakeFeaturesRequestHeader(t *testing.T) {
	tests := []struct {
		name         string
		compr        bool
		ledgerReplay bool
		txrr         bool
		vprr         bool
		wantContains []string
		wantExcludes []string
	}{
		{
			name:         "all_enabled",
			compr:        true,
			ledgerReplay: true,
			txrr:         true,
			vprr:         true,
			wantContains: []string{"compr=lz4", "ledgerreplay=1", "txrr=1", "vprr=1"},
		},
		{
			name:         "none_enabled",
			compr:        false,
			ledgerReplay: false,
			txrr:         false,
			vprr:         false,
			wantContains: []string{},
		},
		{
			name:         "only_compression",
			compr:        true,
			ledgerReplay: false,
			txrr:         false,
			vprr:         false,
			wantContains: []string{"compr=lz4"},
			wantExcludes: []string{"ledgerreplay", "txrr", "vprr"},
		},
		{
			name:         "reduce_relay_features",
			compr:        false,
			ledgerReplay: false,
			txrr:         true,
			vprr:         true,
			wantContains: []string{"txrr=1", "vprr=1"},
			wantExcludes: []string{"compr", "ledgerreplay"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MakeFeaturesRequestHeader(tt.compr, tt.ledgerReplay, tt.txrr, tt.vprr)

			for _, want := range tt.wantContains {
				assert.Contains(t, result, want)
			}
			for _, exclude := range tt.wantExcludes {
				assert.NotContains(t, result, exclude)
			}
		})
	}
}

// TestMakeFeaturesResponseHeader tests response header generation
func TestMakeFeaturesResponseHeader(t *testing.T) {
	tests := []struct {
		name          string
		requestHeader string
		compr         bool
		ledgerReplay  bool
		txrr          bool
		vprr          bool
		wantContains  []string
		wantExcludes  []string
	}{
		{
			name:          "both_sides_enabled",
			requestHeader: "compr=lz4;ledgerreplay=1;txrr=1;vprr=1",
			compr:         true,
			ledgerReplay:  true,
			txrr:          true,
			vprr:          true,
			wantContains:  []string{"compr=lz4", "ledgerreplay=1", "txrr=1", "vprr=1"},
		},
		{
			name:          "local_enabled_peer_disabled",
			requestHeader: "",
			compr:         true,
			ledgerReplay:  true,
			txrr:          true,
			vprr:          true,
			wantContains:  []string{},
		},
		{
			name:          "peer_enabled_local_disabled",
			requestHeader: "compr=lz4;ledgerreplay=1;txrr=1;vprr=1",
			compr:         false,
			ledgerReplay:  false,
			txrr:          false,
			vprr:          false,
			wantContains:  []string{},
		},
		{
			name:          "partial_overlap",
			requestHeader: "compr=lz4;ledgerreplay=1",
			compr:         true,
			ledgerReplay:  false,
			txrr:          true,
			vprr:          true,
			wantContains:  []string{"compr=lz4"},
			wantExcludes:  []string{"ledgerreplay", "txrr", "vprr"},
		},
		{
			name:          "compression_requires_lz4",
			requestHeader: "compr=gzip", // peer requests gzip, not lz4
			compr:         true,
			ledgerReplay:  false,
			txrr:          false,
			vprr:          false,
			wantContains:  []string{},
			wantExcludes:  []string{"compr"}, // should not include compr
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			requestHeaders := http.Header{}
			if tt.requestHeader != "" {
				requestHeaders.Set(HeaderProtocolCtl, tt.requestHeader)
			}

			result := MakeFeaturesResponseHeader(requestHeaders, tt.compr, tt.ledgerReplay, tt.txrr, tt.vprr)

			for _, want := range tt.wantContains {
				assert.Contains(t, result, want)
			}
			for _, exclude := range tt.wantExcludes {
				assert.NotContains(t, result, exclude)
			}
		})
	}
}

// TestMakeFeaturesRequestHeader_IndependentVPRRTXRR pins R5.12: the
// two reduce-relay flags advertise independently on the wire. An
// operator who enables only VPRR must NOT see txrr=1 on the
// handshake, and vice versa. Matches rippled's Handshake.cpp which
// treats them as independent toggles.
func TestMakeFeaturesRequestHeader_IndependentVPRRTXRR(t *testing.T) {
	tests := []struct {
		name          string
		compr, replay bool
		txrr, vprr    bool
		wantContains  []string
		wantExcludes  []string
	}{
		{
			name:         "only_vprr",
			vprr:         true,
			wantContains: []string{"vprr=1"},
			wantExcludes: []string{"txrr=1"},
		},
		{
			name:         "only_txrr",
			txrr:         true,
			wantContains: []string{"txrr=1"},
			wantExcludes: []string{"vprr=1"},
		},
		{
			name:         "both",
			vprr:         true,
			txrr:         true,
			wantContains: []string{"vprr=1", "txrr=1"},
		},
		{
			name:         "neither",
			wantExcludes: []string{"vprr=1", "txrr=1"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			hdr := MakeFeaturesRequestHeader(tc.compr, tc.replay, tc.txrr, tc.vprr)
			for _, want := range tc.wantContains {
				assert.Contains(t, hdr, want)
			}
			for _, nope := range tc.wantExcludes {
				assert.NotContains(t, hdr, nope)
			}
		})
	}
}

// TestParseProtocolCtlFeatures tests full feature set parsing. VPRR and
// TXRR are tracked independently — rippled's Handshake.cpp publishes
// them as separate flags, so an operator may enable one without the
// other. Tests pin both the "all on" and the "only one" cases.
func TestParseProtocolCtlFeatures(t *testing.T) {
	tests := []struct {
		name      string
		header    string
		hasCompr  bool
		hasReplay bool
		hasVPRR   bool
		hasTXRR   bool
	}{
		{
			name:      "all_features",
			header:    "compr=lz4;ledgerreplay=1;vprr=1;txrr=1",
			hasCompr:  true,
			hasReplay: true,
			hasVPRR:   true,
			hasTXRR:   true,
		},
		{
			name:      "only_compression",
			header:    "compr=lz4",
			hasCompr:  true,
			hasReplay: false,
			hasVPRR:   false,
			hasTXRR:   false,
		},
		{
			name:      "only_vprr",
			header:    "vprr=1",
			hasCompr:  false,
			hasReplay: false,
			hasVPRR:   true,
			hasTXRR:   false,
		},
		{
			name:      "only_txrr",
			header:    "txrr=1",
			hasCompr:  false,
			hasReplay: false,
			hasVPRR:   false,
			hasTXRR:   true,
		},
		{
			name:      "empty_header",
			header:    "",
			hasCompr:  false,
			hasReplay: false,
			hasVPRR:   false,
			hasTXRR:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set(HeaderProtocolCtl, tt.header)
			}

			fs := ParseProtocolCtlFeatures(headers)

			assert.Equal(t, tt.hasCompr, fs.Has(FeatureCompression))
			assert.Equal(t, tt.hasReplay, fs.Has(FeatureLedgerReplay))
			assert.Equal(t, tt.hasVPRR, fs.Has(FeatureVpReduceRelay),
				"vprr flag must be tracked independently of txrr")
			assert.Equal(t, tt.hasTXRR, fs.Has(FeatureTxReduceRelay),
				"txrr flag must be tracked independently of vprr")
		})
	}
}

// TestPeerFeatureEnabled tests combined local/remote feature negotiation
func TestPeerFeatureEnabled(t *testing.T) {
	tests := []struct {
		name         string
		header       string
		feature      string
		value        string
		localEnabled bool
		want         bool
	}{
		{
			name:         "both_enabled",
			header:       "compr=lz4",
			feature:      "compr",
			value:        "lz4",
			localEnabled: true,
			want:         true,
		},
		{
			name:         "local_disabled",
			header:       "compr=lz4",
			feature:      "compr",
			value:        "lz4",
			localEnabled: false,
			want:         false,
		},
		{
			name:         "remote_disabled",
			header:       "",
			feature:      "compr",
			value:        "lz4",
			localEnabled: true,
			want:         false,
		},
		{
			name:         "value_mismatch",
			header:       "compr=gzip",
			feature:      "compr",
			value:        "lz4",
			localEnabled: true,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			headers := http.Header{}
			if tt.header != "" {
				headers.Set(HeaderProtocolCtl, tt.header)
			}

			got := PeerFeatureEnabled(headers, tt.feature, tt.value, tt.localEnabled)
			assert.Equal(t, tt.want, got)
		})
	}
}

// newTestOverlayWithPeers returns a partially-initialised Overlay
// whose only valid surface is the peers map + peersMu — enough to
// drive PeersWithClosedLedger / PeersJSON in tests that don't need a
// running overlay.
func newTestOverlayWithPeers(peers map[PeerID]*Peer) *Overlay {
	return &Overlay{peers: peers}
}

// asSocketIP normalises an IP to the byte length a real *net.TCPAddr
// would carry — 4 bytes for an AF_INET socket, 16 bytes for AF_INET6.
// Tests that simulate peerRemote must use this; net.ParseIP returns
// v4-in-v6 16-byte form, which the production family detector reads
// as v6 and would mismatch a v4 textual header.
func asSocketIP(t *testing.T, ip net.IP) net.IP {
	t.Helper()
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

// buildAllHeadersRequest builds a handshake request with every issue-#270
// header set. peerRemote drives Remote-IP / Local-IP emission.
func buildAllHeadersRequest(t *testing.T, id *Identity, cfg HandshakeConfig, peerRemote net.IP) http.Header {
	t.Helper()
	sharedValue := make([]byte, 32)
	for i := range sharedValue {
		sharedValue[i] = byte(i + 7)
	}
	req, err := BuildHandshakeRequest(id, sharedValue, cfg)
	require.NoError(t, err)
	if peerRemote != nil {
		addAddressHeaders(req.Header, cfg, peerRemote)
	}
	return req.Header
}

// Strict rippled parity: Instance-Cookie round-trips but is NEVER
// compared (Handshake.cpp:226-362). Self-connection stays pubkey-only
// at line 322. This test verifies both halves.
func TestHandshake_InstanceCookie_DetectsSelfConnection(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	const cookie uint64 = 0xDEADBEEFCAFEBABE
	cfg := DefaultHandshakeConfig()
	cfg.InstanceCookie = cookie

	headers := buildAllHeadersRequest(t, id, cfg, nil)
	require.Equal(t, strconv.FormatUint(cookie, 10), headers.Get(HeaderInstanceCookie))

	// Strict rippled parity: the cookie is on the wire (Handshake.cpp:208)
	// but verifyHandshake never inspects it. ParseHandshakeExtras must
	// not fail on it and must not surface a typed value.
	_, err = ParseHandshakeExtras(headers, nil, nil)
	require.NoError(t, err)

	// The rippled-canonical self-connection signal is pubkey identity
	// (Handshake.cpp:322 publicKey == app.nodeIdentity().first). Build a
	// fully-signed handshake from `id` and verify against the same id's
	// pubkey — VerifyPeerHandshake must reach the self-connection branch
	// (after Session-Signature passes) and surface ErrSelfConnection.
	// sharedValue must match the one buildAllHeadersRequest signs over.
	sharedValue := make([]byte, 32)
	for i := range sharedValue {
		sharedValue[i] = byte(i + 7)
	}
	_, err = VerifyPeerHandshake(headers, sharedValue, id.EncodedPublicKey(), cfg)
	assert.ErrorIs(t, err, ErrSelfConnection,
		"matching Public-Key triggers self-connection — the only signal rippled uses")
}

// Closed-Ledger / Previous-Ledger hints round-trip and end up readable
// on Peer accessors (mirrors PeerImp storing closedLedgerHash_).
func TestHandshake_ClosedLedgerHint_ReadableAfterHandshake(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	var closed, parent [32]byte
	for i := range closed {
		closed[i] = byte(i + 1)
		parent[i] = byte(0xFF - i)
	}

	cfg := DefaultHandshakeConfig()
	cfg.LedgerHintProvider = func() (LedgerHints, bool) {
		return LedgerHints{Closed: closed, Parent: parent}, true
	}

	headers := buildAllHeadersRequest(t, id, cfg, nil)
	require.Equal(t, strings.ToUpper(hex.EncodeToString(closed[:])), headers.Get(HeaderClosedLedger))
	require.Equal(t, strings.ToUpper(hex.EncodeToString(parent[:])), headers.Get(HeaderPreviousLedger))

	extras, err := ParseHandshakeExtras(headers, nil, nil)
	require.NoError(t, err)
	assert.True(t, extras.HasClosedLedger, "Closed-Ledger header must mark closed hint as present")
	assert.True(t, extras.HasPreviousLedger, "Previous-Ledger header must mark previous hint as present")
	assert.Equal(t, closed, extras.ClosedLedger)
	assert.Equal(t, parent, extras.PreviousLedger)

	// Confirm Peer accessor surface produces the same values.
	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1234}, true, id, nil)
	peer.applyHandshakeExtras(extras)
	gotClosed, ok := peer.ClosedLedger()
	require.True(t, ok)
	assert.Equal(t, closed, gotClosed)
	gotParent, hasParent := peer.PreviousLedger()
	assert.True(t, hasParent)
	assert.Equal(t, parent, gotParent)
}

// Rippled accepts Closed-Ledger without Previous-Ledger
// (PeerImp.cpp:198-201 — each is set independently). The peer must
// surface only the closed hint, with PreviousLedger() reporting
// "absent" rather than the zero hash.
func TestHandshake_ClosedLedgerWithoutPrevious(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	var closed [32]byte
	for i := range closed {
		closed[i] = byte(i + 1)
	}

	headers := http.Header{}
	headers.Set(HeaderClosedLedger, strings.ToUpper(hex.EncodeToString(closed[:])))

	extras, err := ParseHandshakeExtras(headers, nil, nil)
	require.NoError(t, err)
	assert.True(t, extras.HasClosedLedger)
	assert.False(t, extras.HasPreviousLedger,
		"missing Previous-Ledger must not be inferred from Closed-Ledger presence")
	assert.Equal(t, closed, extras.ClosedLedger)
	assert.Equal(t, [32]byte{}, extras.PreviousLedger)

	peer := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1234}, true, id, nil)
	peer.applyHandshakeExtras(extras)
	gotClosed, hasClosed := peer.ClosedLedger()
	require.True(t, hasClosed)
	assert.Equal(t, closed, gotClosed)
	_, hasPrev := peer.PreviousLedger()
	assert.False(t, hasPrev,
		"PreviousLedger() must report absent when peer omitted the header")
}

// Remote-IP consistency check per Handshake.cpp:340-359.
func TestHandshake_RemoteIPSelfReported_MatchesTcpConn(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	publicIP := net.ParseIP("198.51.100.1")
	cfg := DefaultHandshakeConfig()
	cfg.PublicIP = publicIP

	socketIP := asSocketIP(t, publicIP)

	t.Run("matching_public_ip_passes", func(t *testing.T) {
		headers := buildAllHeadersRequest(t, id, cfg, socketIP)
		require.Equal(t, "198.51.100.1", headers.Get(HeaderRemoteIP))

		_, err := ParseHandshakeExtras(headers, publicIP, socketIP)
		require.NoError(t, err)
	})

	t.Run("mismatched_public_ip_rejected", func(t *testing.T) {
		headers := buildAllHeadersRequest(t, id, cfg, socketIP)
		// Peer claims our IP is 203.0.113.5 — but our config says
		// otherwise and they connected from a public address. Reject.
		headers.Set(HeaderRemoteIP, "203.0.113.5")

		_, err := ParseHandshakeExtras(headers, publicIP, socketIP)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidHandshake))
		assert.Contains(t, err.Error(), "Incorrect Remote-IP")
	})

	t.Run("loopback_peer_skips_check", func(t *testing.T) {
		// Same mismatched header, but the peer connected from
		// loopback — rippled and we both skip the comparison.
		headers := buildAllHeadersRequest(t, id, cfg, socketIP)
		headers.Set(HeaderRemoteIP, "203.0.113.5")

		_, err := ParseHandshakeExtras(headers, publicIP, asSocketIP(t, net.ParseIP("127.0.0.1")))
		require.NoError(t, err)
	})

	t.Run("malformed_remote_ip_rejected", func(t *testing.T) {
		headers := buildAllHeadersRequest(t, id, cfg, socketIP)
		headers.Set(HeaderRemoteIP, "not-an-ip")

		_, err := ParseHandshakeExtras(headers, publicIP, socketIP)
		require.Error(t, err)
		assert.True(t, errors.Is(err, ErrInvalidHandshake))
		assert.Contains(t, err.Error(), "invalid Remote-IP")
	})
}

// Round-trip topology: sender A (public IP pA) → receiver B (public IP pB).
// Verifies the full set of issue-#270 headers + IP consistency
// (Handshake.cpp:325-359).
func TestHandshake_AllHeaders_RoundTrip(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	pA := net.ParseIP("198.51.100.42") // sender's public IP
	pB := net.ParseIP("203.0.113.7")   // receiver's public IP

	var closed, parent [32]byte
	for i := range closed {
		closed[i] = byte(i)
		parent[i] = byte(0xA0 + i)
	}

	// Sender's HandshakeConfig: PublicIP = pA. Sender sees peer (B) at
	// pB, so addAddressHeaders emits Remote-IP=pB, Local-IP=pA.
	senderCfg := DefaultHandshakeConfig()
	senderCfg.InstanceCookie = 0x1234567890ABCDEF
	senderCfg.ServerDomain = "validator.example.com"
	senderCfg.PublicIP = pA
	senderCfg.LedgerHintProvider = func() (LedgerHints, bool) {
		return LedgerHints{Closed: closed, Parent: parent}, true
	}

	pASock, pBSock := asSocketIP(t, pA), asSocketIP(t, pB)

	t.Run("request_path", func(t *testing.T) {
		// Build with sender's view: peer (B) is at pB.
		headers := buildAllHeadersRequest(t, id, senderCfg, pBSock)

		// Wire-level checks: each header is present in the right form.
		assert.Equal(t, strconv.FormatUint(senderCfg.InstanceCookie, 10), headers.Get(HeaderInstanceCookie))
		assert.Equal(t, "validator.example.com", headers.Get(HeaderServerDomain))
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(closed[:])), headers.Get(HeaderClosedLedger))
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(parent[:])), headers.Get(HeaderPreviousLedger))
		assert.Equal(t, pB.String(), headers.Get(HeaderRemoteIP))
		assert.Equal(t, pA.String(), headers.Get(HeaderLocalIP))

		// Existing X-Protocol-Ctl / Public-Key / Network-Time still emitted.
		assert.NotEmpty(t, headers.Get(HeaderPublicKey))
		assert.NotEmpty(t, headers.Get(HeaderSessionSignature))
		assert.NotEmpty(t, headers.Get(HeaderNetworkTime))

		// Parse from the receiver (B)'s vantage: peerRemote=pA,
		// localPublicIP=pB. Strict rippled parity: only the headers
		// rippled stores typed (Server-Domain, Closed/Previous-Ledger)
		// surface on extras. Instance-Cookie and IP self-reports are
		// validated and discarded.
		extras, err := ParseHandshakeExtras(headers, pB, pASock)
		require.NoError(t, err)
		assert.Equal(t, senderCfg.ServerDomain, extras.ServerDomain)
		assert.True(t, extras.HasClosedLedger)
		assert.True(t, extras.HasPreviousLedger)
		assert.Equal(t, closed, extras.ClosedLedger)
		assert.Equal(t, parent, extras.PreviousLedger)
	})

	t.Run("response_path", func(t *testing.T) {
		sharedValue := make([]byte, 32)
		resp := BuildHandshakeResponse(id, sharedValue, senderCfg)
		// Sender (now the responder) sees peer (now the requester) at
		// pB and emits its own Local-IP = pA.
		addAddressHeaders(resp.Header, senderCfg, pBSock)

		// Symmetric checks. The response must carry the same six
		// headers — without them the inbound peer can't enforce the
		// rippled-style consistency contract on us either.
		assert.Equal(t, strconv.FormatUint(senderCfg.InstanceCookie, 10), resp.Header.Get(HeaderInstanceCookie))
		assert.Equal(t, "validator.example.com", resp.Header.Get(HeaderServerDomain))
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(closed[:])), resp.Header.Get(HeaderClosedLedger))
		assert.Equal(t, strings.ToUpper(hex.EncodeToString(parent[:])), resp.Header.Get(HeaderPreviousLedger))
		assert.Equal(t, pB.String(), resp.Header.Get(HeaderRemoteIP))
		assert.Equal(t, pA.String(), resp.Header.Get(HeaderLocalIP))

		extras, err := ParseHandshakeExtras(resp.Header, pB, pASock)
		require.NoError(t, err)
		assert.Equal(t, senderCfg.ServerDomain, extras.ServerDomain)
		assert.True(t, extras.HasClosedLedger)
		assert.True(t, extras.HasPreviousLedger)
	})

	t.Run("malformed_server_domain_rejected", func(t *testing.T) {
		// Server-Domain is the first thing rippled validates
		// (Handshake.cpp:235-239), so the dedicated validator runs
		// upstream of ParseHandshakeExtras.
		headers := buildAllHeadersRequest(t, id, senderCfg, pBSock)
		headers.Set(HeaderServerDomain, "-bad.example.com") // leading hyphen

		_, err := ValidateServerDomain(headers)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "invalid Server-Domain")
	})
}

// Catchup peer-picker reads Closed-Ledger handshake hints (rippled
// NetworkOPs / PeerImp::closedLedgerHash_).
func TestLedgerSync_PreferredPeersForLedger_ConsumesClosedLedgerHint(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	var target, parent [32]byte
	for i := range target {
		target[i] = byte(0xCC)
		parent[i] = byte(0xBB)
	}

	mkPeer := func(idNum PeerID, hint *[32]byte) *Peer {
		p := NewPeer(idNum, Endpoint{Host: "127.0.0.1", Port: 1000 + uint16(idNum)}, false, id, nil)
		p.setState(PeerStateConnected)
		if hint != nil {
			p.applyHandshakeExtras(HandshakeExtras{
				ClosedLedger:      *hint,
				PreviousLedger:    parent,
				HasClosedLedger:   true,
				HasPreviousLedger: true,
			})
		}
		return p
	}

	overlay := newTestOverlayWithPeers(map[PeerID]*Peer{
		1: mkPeer(1, &target),         // matches → expected
		2: mkPeer(2, &[32]byte{0xAA}), // different hint → filtered
		3: mkPeer(3, nil),             // no hint → filtered
		4: mkPeer(4, &target),         // matches → expected
		// peer 5: disconnected with matching hint → filtered by state.
		5: func() *Peer {
			p := NewPeer(5, Endpoint{}, false, id, nil)
			p.applyHandshakeExtras(HandshakeExtras{ClosedLedger: target, HasClosedLedger: true})
			return p
		}(),
	})

	got := overlay.PeersWithClosedLedger(target)
	want := map[PeerID]struct{}{1: {}, 4: {}}
	assert.Len(t, got, len(want))
	for _, id := range got {
		_, ok := want[id]
		assert.True(t, ok, "unexpected peer %d in result", id)
	}
}

// applyStatusChange mirrors PeerImp.cpp:1812-1883.
func TestApplyStatusChange_RippledSemantics(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	u32 := func(v uint32) *uint32 { return &v }

	mk := func() *Peer {
		p := NewPeer(1, Endpoint{Host: "127.0.0.1", Port: 1}, false, id, nil)
		var initialClosed, initialParent [32]byte
		for i := range initialClosed {
			initialClosed[i] = byte(0x11)
			initialParent[i] = byte(0x22)
		}
		p.applyHandshakeExtras(HandshakeExtras{
			ClosedLedger:      initialClosed,
			PreviousLedger:    initialParent,
			HasClosedLedger:   true,
			HasPreviousLedger: true,
		})
		return p
	}

	t.Run("lost_sync_zeroes_both", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, true, nil, nil)
		_, hasC := p.ClosedLedger()
		_, hasP := p.PreviousLedger()
		assert.False(t, hasC)
		assert.False(t, hasP)
	})

	t.Run("updates_to_new_pair", func(t *testing.T) {
		p := mk()
		var newClosed, newParent [32]byte
		for i := range newClosed {
			newClosed[i] = byte(0xAA)
			newParent[i] = byte(0xBB)
		}
		p.applyStatusChange(newClosed[:], newParent[:], false, nil, nil)
		gotC, hasC := p.ClosedLedger()
		gotP, hasP := p.PreviousLedger()
		assert.True(t, hasC)
		assert.True(t, hasP)
		assert.Equal(t, newClosed, gotC)
		assert.Equal(t, newParent, gotP)
	})

	t.Run("missing_closed_zeroes_closed_only", func(t *testing.T) {
		p := mk()
		var newParent [32]byte
		for i := range newParent {
			newParent[i] = byte(0xCC)
		}
		p.applyStatusChange(nil, newParent[:], false, nil, nil)
		_, hasC := p.ClosedLedger()
		gotP, hasP := p.PreviousLedger()
		assert.False(t, hasC)
		assert.True(t, hasP)
		assert.Equal(t, newParent, gotP)
	})

	t.Run("malformed_closed_zeroes_closed", func(t *testing.T) {
		p := mk()
		p.applyStatusChange([]byte{0x01, 0x02}, nil, false, nil, nil) // 2 bytes ≠ 32
		_, hasC := p.ClosedLedger()
		_, hasP := p.PreviousLedger()
		assert.False(t, hasC)
		assert.False(t, hasP)
	})

	// PeerImp.cpp:1874-1883 — ledger range update + clamp.
	t.Run("ledger_range_valid_pair_stored", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(100), u32(200))
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(100), minSeq)
		assert.Equal(t, uint32(200), maxSeq)
	})

	t.Run("ledger_range_first_zero_clamped", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(0), u32(200))
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(0), minSeq)
		assert.Equal(t, uint32(0), maxSeq)
	})

	t.Run("ledger_range_last_zero_clamped", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(100), u32(0))
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(0), minSeq)
		assert.Equal(t, uint32(0), maxSeq)
	})

	t.Run("ledger_range_inverted_clamped", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(200), u32(100))
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(0), minSeq)
		assert.Equal(t, uint32(0), maxSeq)
	})

	// PeerImp.cpp:1812-1832 — lostSync returns before the range
	// block, so the previously-advertised range must persist.
	t.Run("ledger_range_lost_sync_preserves_range", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(100), u32(200))
		p.applyStatusChange(nil, nil, true, nil, nil)
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(100), minSeq)
		assert.Equal(t, uint32(200), maxSeq)
	})

	// PeerImp.cpp:1874 — gate on has_firstseq() && has_lastseq().
	// A status change without the range fields must not touch the
	// previously-stored range.
	t.Run("ledger_range_absent_preserves_existing", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(100), u32(200))
		p.applyStatusChange(nil, nil, false, nil, nil)
		minSeq, maxSeq := p.LedgerRange()
		assert.Equal(t, uint32(100), minSeq)
		assert.Equal(t, uint32(200), maxSeq)
	})

	t.Run("info_complete_ledgers_formatted", func(t *testing.T) {
		p := mk()
		p.applyStatusChange(nil, nil, false, u32(100), u32(200))
		assert.Equal(t, "100 - 200", p.Info().CompleteLedgers,
			"matches rippled PeerImp.cpp:434-435 format with surrounding spaces")
	})

	t.Run("info_complete_ledgers_empty_when_no_range", func(t *testing.T) {
		p := mk()
		assert.Empty(t, p.Info().CompleteLedgers)
	})
}

// Malformed Closed-Ledger / Previous-Ledger / Previous-without-Closed
// must fail the handshake (PeerImp.cpp:175-194).
func TestHandshake_MalformedLedgerHashRejected(t *testing.T) {
	t.Run("malformed_closed_ledger", func(t *testing.T) {
		h := http.Header{}
		h.Set(HeaderClosedLedger, "not-hex-not-base64")
		_, err := ParseHandshakeExtras(h, nil, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidHandshake)
		assert.Contains(t, err.Error(), "malformed Closed-Ledger")
	})

	t.Run("malformed_previous_ledger", func(t *testing.T) {
		var closed [32]byte
		closed[0] = 0xAA
		h := http.Header{}
		h.Set(HeaderClosedLedger, hex.EncodeToString(closed[:]))
		h.Set(HeaderPreviousLedger, "garbage")
		_, err := ParseHandshakeExtras(h, nil, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidHandshake)
		assert.Contains(t, err.Error(), "malformed Previous-Ledger")
	})

	t.Run("previous_without_closed", func(t *testing.T) {
		var parent [32]byte
		parent[0] = 0xBB
		h := http.Header{}
		h.Set(HeaderPreviousLedger, hex.EncodeToString(parent[:]))
		_, err := ParseHandshakeExtras(h, nil, nil)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrInvalidHandshake)
		assert.Contains(t, err.Error(), "Previous-Ledger without Closed-Ledger")
	})
}

// generateInstanceCookie must produce values in [1, MAX_UINT64] to
// match rippled `1 + rand_int(prng, MAX_UINT64 - 1)`, which uses a
// closed interval and so includes MAX_UINT64. Only 0 is excluded.
func TestInstanceCookie_GeneratorRange(t *testing.T) {
	for i := 0; i < 10000; i++ {
		v, err := generateInstanceCookie()
		require.NoError(t, err)
		assert.NotZero(t, v, "cookie must never be zero")
	}
}

// isPublicIP must mirror beast::IP::is_public: IPv4 link-local stays
// public (rippled IPAddressV4.cpp only flags RFC1918+loopback); IPv6
// link-local is private to match rippled IPAddressV6.cpp's `(byte0 &
// 0xfd)` catching fe80::/10. Loopback / RFC1918 / multicast are private
// in both families.
func TestIsPublicIP_BeastParity(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"public_v4", "8.8.8.8", true},
		{"link_local_v4", "169.254.1.1", true},
		{"loopback_v4", "127.0.0.1", false},
		{"rfc1918_10", "10.0.0.1", false},
		{"rfc1918_172_16", "172.16.5.4", false},
		{"rfc1918_192_168", "192.168.1.1", false},
		{"multicast_v4", "224.0.0.1", false},
		{"unspecified_v4", "0.0.0.0", false},
		{"public_v6", "2001:db8::1", true},
		{"loopback_v6", "::1", false},
		{"unspecified_v6", "::", false},
		{"ula_v6", "fc00::1", false},
		{"link_local_v6", "fe80::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isPublicIP(net.ParseIP(tc.ip)))
		})
	}
}

// ipFamilyEqual must match boost::asio::ip::address::operator==:
// "::ffff:1.2.3.4" (v6 textual form) and "1.2.3.4" (v4 textual form)
// are treated as different addresses despite normalising to the same
// bytes via net.ParseIP.
func TestIpFamilyEqual_BoostParity(t *testing.T) {
	v4 := net.ParseIP("1.2.3.4")
	v4mapped := net.ParseIP("::ffff:1.2.3.4")
	require.NotNil(t, v4)
	require.NotNil(t, v4mapped)

	assert.True(t, ipFamilyEqual(v4, v4, false, false), "same v4 must be equal")
	assert.False(t, ipFamilyEqual(v4, v4mapped, false, true), "v4 vs v4-mapped-v6 must differ")
	assert.False(t, ipFamilyEqual(v4mapped, v4, true, false), "v4-mapped-v6 vs v4 must differ")
	assert.True(t, ipFamilyEqual(v4mapped, v4mapped, true, true), "same v4-mapped-v6 must be equal")
}

// socketIPIsV6 must classify by underlying socket family, not by
// To4(). A v4-mapped-v6 socket address (16-byte slice that To4()
// reports as v4) must report as v6 to match what boost::asio surfaces
// as address_v6, otherwise a peer announcing "::ffff:x.x.x.x" while
// connecting via AF_INET6 is incorrectly rejected as family-mismatched.
func TestSocketIPIsV6_FamilyFromByteLength(t *testing.T) {
	v4Socket := net.IP{1, 2, 3, 4}                                                 // AF_INET socket
	v6Socket := net.ParseIP("2001:db8::1")                                         // AF_INET6 socket
	v4MappedSocket := net.IP{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xff, 0xff, 1, 2, 3, 4} // AF_INET6 receiving v4

	assert.False(t, socketIPIsV6(v4Socket), "4-byte socket IP is AF_INET")
	assert.True(t, socketIPIsV6(v6Socket), "16-byte non-mapped socket IP is AF_INET6")
	assert.True(t, socketIPIsV6(v4MappedSocket), "16-byte v4-mapped is AF_INET6 (boost address_v6)")
}

// headerIPIsV6 keys off textual form because net.ParseIP normalises v4
// to 16-byte v4-in-v6 bytes. The colon is the only signal of intent.
func TestHeaderIPIsV6_FamilyFromTextForm(t *testing.T) {
	assert.False(t, headerIPIsV6("1.2.3.4"))
	assert.True(t, headerIPIsV6("2001:db8::1"))
	assert.True(t, headerIPIsV6("::ffff:1.2.3.4"))
}

// isWellFormedDomain must agree with rippled's regex
// (StringUtilities.cpp:142-153). Go regexp has no lookarounds, so the
// no-leading/trailing-hyphen rule is encoded directly.
func TestIsWellFormedDomain_RegexCrossCheck(t *testing.T) {
	rippledLike := regexp.MustCompile(
		`^([A-Za-z0-9](?:[A-Za-z0-9\-]{0,61}[A-Za-z0-9])?\.)+[A-Za-z]{2,63}$`,
	)
	check := func(s string) bool {
		if len(s) < 4 || len(s) > 128 {
			return false
		}
		return rippledLike.MatchString(s)
	}

	inputs := []string{
		"a.io", "example.com", "validator.example.com", "node-1.example.org",
		"a.b.c.d.example.com", "X-1.X-2.example.io",
		"localhost", "example", "a.b", "x.123", "a.b.c",
		"-bad.example.com", "bad-.example.com", "_bad.example.com",
		"example.com.", "example..com", ".example.com",
		"", "a",
		strings.Repeat("a", 64) + ".example.com",
		strings.Repeat("a", 200) + ".com",
		"foo.bar.MUSEUM",
	}
	for _, s := range inputs {
		want := check(s)
		got := isWellFormedDomain(s)
		assert.Equal(t, want, got, "isWellFormedDomain(%q): want=%v got=%v", s, want, got)
	}
}

// isWellFormedDomain matches rippled's isProperlyFormedTomlDomain
// (StringUtilities.cpp:131-156).
func TestIsWellFormedDomain_TomlParity(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"valid_two_label", "a.io", true},
		{"valid_subdomain", "validator.example.com", true},
		{"valid_with_digits", "node-1.example.org", true},
		{"too_short", "a.b", false},
		{"too_long", strings.Repeat("a", 120) + ".example.com", false},
		{"single_label", "example", false},
		{"numeric_tld", "x.123", false},
		{"one_char_tld", "a.b.c", false},
		{"trailing_dot", "example.com.", false},
		{"leading_hyphen_label", "-bad.example.com", false},
		{"trailing_hyphen_label", "bad-.example.com", false},
		{"empty", "", false},
		{"underscore", "bad_label.example.com", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isWellFormedDomain(tc.in))
		})
	}
}

// WriteRawHandshakeRequest must place every issue-#270 header on the
// wire — the whitelist in the writer is otherwise uncovered.
func TestWriteRawHandshakeRequest_EmitsAllNewHeaders(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	var closed, parent [32]byte
	for i := range closed {
		closed[i] = byte(i)
		parent[i] = byte(0x80 + i)
	}
	cfg := DefaultHandshakeConfig()
	cfg.InstanceCookie = 0xCAFEBABE12345678
	cfg.ServerDomain = "example.com"
	cfg.PublicIP = net.ParseIP("198.51.100.10")
	cfg.LedgerHintProvider = func() (LedgerHints, bool) {
		return LedgerHints{Closed: closed, Parent: parent}, true
	}
	req, err := BuildHandshakeRequest(id, make([]byte, 32), cfg)
	require.NoError(t, err)
	addAddressHeaders(req.Header, cfg, net.ParseIP("203.0.113.5"))

	var buf bytes.Buffer
	require.NoError(t, WriteRawHandshakeRequest(&buf, req))
	wire := buf.String()

	for _, h := range []string{
		HeaderInstanceCookie + ": ",
		HeaderServerDomain + ": example.com",
		HeaderClosedLedger + ": " + strings.ToUpper(hex.EncodeToString(closed[:])),
		HeaderPreviousLedger + ": " + strings.ToUpper(hex.EncodeToString(parent[:])),
		HeaderRemoteIP + ": 203.0.113.5",
		HeaderLocalIP + ": 198.51.100.10",
	} {
		assert.Contains(t, wire, h, "missing %s on the wire", h)
	}
}

// PeerImp.cpp:430-435 — emit complete_ledgers iff the peer has
// advertised a non-zero range, formatted as "<min> - <max>" with
// surrounding spaces.
func TestPeersJSON_CompleteLedgers(t *testing.T) {
	id, err := NewIdentity()
	require.NoError(t, err)

	u32 := func(v uint32) *uint32 { return &v }

	t.Run("emits_when_range_advertised", func(t *testing.T) {
		p := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
		p.applyStatusChange(nil, nil, false, u32(100), u32(200))

		o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		assert.Equal(t, "100 - 200", entries[0]["complete_ledgers"])
	})

	t.Run("absent_when_no_range", func(t *testing.T) {
		p := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
		o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		_, present := entries[0]["complete_ledgers"]
		assert.False(t, present, "rippled gates emission on (min,max) != (0,0)")
	})

	t.Run("absent_after_clamp_on_invalid_range", func(t *testing.T) {
		p := NewPeer(1, Endpoint{Host: "192.0.2.1", Port: 51235}, false, id, nil)
		p.applyStatusChange(nil, nil, false, u32(0), u32(200)) // first=0 → clamped to (0,0)

		o := newTestOverlayWithPeers(map[PeerID]*Peer{1: p})
		entries := o.PeersJSON()
		require.Len(t, entries, 1)
		_, present := entries[0]["complete_ledgers"]
		assert.False(t, present)
	})
}
