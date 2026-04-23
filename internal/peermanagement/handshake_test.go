package peermanagement

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMakeSharedValueFromFinished tests the TLS cookie generation
// Reference: rippled Handshake.cpp makeSharedValue - XOR of SHA-512 hashes
func TestMakeSharedValueFromFinished(t *testing.T) {
	// Simulate TLS finished messages
	localFinished := []byte("local_finished_message_12345678")
	peerFinished := []byte("peer_finished_message_12345678_")

	sharedValue, err := MakeSharedValueFromFinished(localFinished, peerFinished)
	require.NoError(t, err)
	require.NotNil(t, sharedValue)

	// Should be 32 bytes (256 bits)
	assert.Len(t, sharedValue, 32)

	// Same inputs should produce same output
	sharedValue2, err := MakeSharedValueFromFinished(localFinished, peerFinished)
	require.NoError(t, err)
	assert.Equal(t, sharedValue, sharedValue2)

	// Different inputs should produce different output
	differentPeer := []byte("different_peer_message_1234567_")
	differentValue, err := MakeSharedValueFromFinished(localFinished, differentPeer)
	require.NoError(t, err)
	assert.NotEqual(t, sharedValue, differentValue)
}

// TestMakeSharedValueFromFinished_TooShort tests minimum length requirement
// Reference: rippled Handshake.cpp - constexpr std::size_t sslMinimumFinishedLength = 12
func TestMakeSharedValueFromFinished_TooShort(t *testing.T) {
	tests := []struct {
		name          string
		localFinished []byte
		peerFinished  []byte
	}{
		{"local_too_short", []byte("short"), []byte("valid_finished_msg")},
		{"peer_too_short", []byte("valid_finished_msg"), []byte("short")},
		{"both_too_short", []byte("short"), []byte("also")},
		{"empty_local", []byte{}, []byte("valid_finished_msg")},
		{"empty_peer", []byte("valid_finished_msg"), []byte{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := MakeSharedValueFromFinished(tt.localFinished, tt.peerFinished)
			assert.Error(t, err)
			assert.Contains(t, err.Error(), "too short")
		})
	}
}

// TestMakeSharedValueFromFinished_Identical tests rejection of identical messages
// Reference: rippled Handshake.cpp - "Cookie generation: identical finished messages"
func TestMakeSharedValueFromFinished_Identical(t *testing.T) {
	// Identical messages would XOR to zero
	sameMessage := []byte("identical_message_1234567890")

	_, err := MakeSharedValueFromFinished(sameMessage, sameMessage)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "identical")
}

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

// TestVerifyHandshakeHeadersNoSig covers R6.1 + R6.2: the inbound
// handshake's non-signature verification path. Each subtest exercises
// one failure mode that rippled enforces but pre-R6 goXRPL silently
// accepted or mis-handled.
func TestVerifyHandshakeHeadersNoSig(t *testing.T) {
	localId, err := NewIdentity()
	require.NoError(t, err)
	remoteId, err := NewIdentity()
	require.NoError(t, err)

	mkHeaders := func(pubKey, netID, netTime string) http.Header {
		h := http.Header{}
		if pubKey != "" {
			h.Set(HeaderPublicKey, pubKey)
		}
		if netID != "" {
			h.Set(HeaderNetworkID, netID)
		}
		if netTime != "" {
			h.Set(HeaderNetworkTime, netTime)
		}
		return h
	}
	xrplNow := func() string {
		return strconvUnixXRPL(time.Now())
	}

	t.Run("happy_path_mainnet", func(t *testing.T) {
		h := mkHeaders(remoteId.EncodedPublicKey(), "", xrplNow())
		pk, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		require.NoError(t, err)
		require.NotNil(t, pk)
		assert.Equal(t, remoteId.EncodedPublicKey(), pk.Encode())
	})

	t.Run("mainnet_rejects_nonzero_networkid", func(t *testing.T) {
		// Pre-R6.1: mainnet (NetworkID=0) silently accepted any
		// Network-ID the peer advertised. Now we reject.
		h := mkHeaders(remoteId.EncodedPublicKey(), "1", xrplNow())
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		assert.ErrorIs(t, err, ErrNetworkMismatch,
			"mainnet must reject a peer advertising Network-ID=1 (testnet)")
	})

	t.Run("non_default_network_peer_missing_netid", func(t *testing.T) {
		// Symmetric case: we're on network 2, peer omits Network-ID.
		h := mkHeaders(remoteId.EncodedPublicKey(), "", xrplNow())
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 2)
		assert.ErrorIs(t, err, ErrNetworkMismatch,
			"non-default-network node must reject a peer omitting Network-ID")
	})

	t.Run("malformed_networkid_rejected", func(t *testing.T) {
		// Pre-R6.1: ParseUint failures were silently ignored.
		h := mkHeaders(remoteId.EncodedPublicKey(), "not-a-number", xrplNow())
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed Network-ID")
	})

	t.Run("malformed_networktime_rejected", func(t *testing.T) {
		// R6.2: Network-Time was never checked in performInboundHandshake.
		h := mkHeaders(remoteId.EncodedPublicKey(), "", "not-a-number")
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "malformed Network-Time")
	})

	t.Run("network_time_clock_skew", func(t *testing.T) {
		// Peer timestamp 5 minutes ahead of our clock — way beyond
		// NetworkClockTolerance (20s).
		farFuture := strconvUnixXRPL(time.Now().Add(5 * time.Minute))
		h := mkHeaders(remoteId.EncodedPublicKey(), "", farFuture)
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "clock skew")
	})

	t.Run("self_connection", func(t *testing.T) {
		h := mkHeaders(localId.EncodedPublicKey(), "", xrplNow())
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		assert.ErrorIs(t, err, ErrSelfConnection)
	})

	t.Run("missing_public_key", func(t *testing.T) {
		h := mkHeaders("", "", xrplNow())
		_, err := VerifyHandshakeHeadersNoSig(h, localId.EncodedPublicKey(), 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Public-Key")
	})
}

// strconvUnixXRPL formats a time as XRPL epoch seconds (like rippled's
// Network-Time header builder). Test helper for R6.2 tests.
func strconvUnixXRPL(t time.Time) string {
	xrplSec := t.Unix() - XRPLEpochOffset
	return fmtInt(xrplSec)
}

func fmtInt(n int64) string {
	// Simple base-10 stringification without importing another package.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := make([]byte, 0, 20)
	for n > 0 {
		buf = append(buf, byte('0'+n%10))
		n /= 10
	}
	if neg {
		buf = append(buf, '-')
	}
	// reverse
	for i, j := 0, len(buf)-1; i < j; i, j = i+1, j-1 {
		buf[i], buf[j] = buf[j], buf[i]
	}
	return string(buf)
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

// ============== Feature Tests ==============

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

// ============== X-Protocol-Ctl Header Tests ==============
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
