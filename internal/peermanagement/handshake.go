package peermanagement

import (
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcec/v2/ecdsa"
)

// Protocol version constants.
const (
	ProtocolVersion       = "XRPL/2.2"
	LegacyProtocolVersion = "RTXP/1.2"
)

// HTTP header names for handshake.
const (
	HeaderUpgrade          = "Upgrade"
	HeaderConnection       = "Connection"
	HeaderConnectAs        = "Connect-As"
	HeaderPublicKey        = "Public-Key"
	HeaderSessionSignature = "Session-Signature"
	HeaderNetworkID        = "Network-ID"
	HeaderNetworkTime      = "Network-Time"
	HeaderClosedLedger     = "Closed-Ledger"
	HeaderPreviousLedger   = "Previous-Ledger"
	HeaderCrawl            = "Crawl"
	HeaderUserAgent        = "User-Agent"
)

// Time constants.
const (
	// XRPLEpochOffset is the offset from Unix epoch to XRPL epoch (Jan 1, 2000).
	XRPLEpochOffset = 946684800

	// NetworkClockTolerance is the maximum allowed clock difference.
	NetworkClockTolerance = 20 * time.Second
)

// HandshakeConfig holds configuration for the handshake.
type HandshakeConfig struct {
	UserAgent   string
	NetworkID   uint32
	CrawlPublic bool
}

// DefaultHandshakeConfig returns default handshake configuration.
func DefaultHandshakeConfig() HandshakeConfig {
	return HandshakeConfig{
		UserAgent:   "goXRPL/0.1.0",
		NetworkID:   0,
		CrawlPublic: false,
	}
}

// MakeSharedValue computes a shared value based on the TLS connection state.
func MakeSharedValue(conn *tls.Conn) ([]byte, error) {
	state := conn.ConnectionState()
	localFinished := state.TLSUnique
	if len(localFinished) < 12 {
		return nil, fmt.Errorf("%w: local finished message too short", ErrHandshakeFailed)
	}

	h := sha512.New()
	h.Write(localFinished)
	hash := h.Sum(nil)

	return hash[:32], nil
}

// MakeSharedValueFromFinished computes the shared value from raw finished messages.
func MakeSharedValueFromFinished(localFinished, peerFinished []byte) ([]byte, error) {
	if len(localFinished) < 12 || len(peerFinished) < 12 {
		return nil, fmt.Errorf("%w: finished message too short", ErrHandshakeFailed)
	}

	h1 := sha512.New()
	h1.Write(localFinished)
	cookie1 := h1.Sum(nil)

	h2 := sha512.New()
	h2.Write(peerFinished)
	cookie2 := h2.Sum(nil)

	result := make([]byte, 64)
	allZero := true
	for i := 0; i < 64; i++ {
		result[i] = cookie1[i] ^ cookie2[i]
		if result[i] != 0 {
			allZero = false
		}
	}

	if allZero {
		return nil, fmt.Errorf("%w: identical finished messages", ErrHandshakeFailed)
	}

	h := sha512.New()
	h.Write(result)
	finalHash := h.Sum(nil)

	return finalHash[:32], nil
}

// BuildHandshakeRequest builds an HTTP upgrade request for peer connection.
func BuildHandshakeRequest(id *Identity, sharedValue []byte, cfg HandshakeConfig) (*http.Request, error) {
	req, err := http.NewRequest("GET", "/", nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set(HeaderUserAgent, cfg.UserAgent)
	req.Header.Set(HeaderUpgrade, ProtocolVersion+", "+LegacyProtocolVersion)
	req.Header.Set(HeaderConnection, "Upgrade")
	req.Header.Set(HeaderConnectAs, "Peer")
	req.Header.Set(HeaderCrawl, crawlValue(cfg.CrawlPublic))

	addHandshakeHeaders(req.Header, id, sharedValue, cfg)

	return req, nil
}

// BuildHandshakeResponse builds an HTTP 101 Switching Protocols response.
func BuildHandshakeResponse(id *Identity, sharedValue []byte, cfg HandshakeConfig) *http.Response {
	resp := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Status:     "101 Switching Protocols",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}

	resp.Header.Set(HeaderConnection, "Upgrade")
	resp.Header.Set(HeaderUpgrade, ProtocolVersion)
	resp.Header.Set(HeaderConnectAs, "Peer")
	resp.Header.Set(HeaderCrawl, crawlValue(cfg.CrawlPublic))

	addHandshakeHeaders(resp.Header, id, sharedValue, cfg)

	return resp
}

func addHandshakeHeaders(h http.Header, id *Identity, sharedValue []byte, cfg HandshakeConfig) {
	if cfg.NetworkID > 0 {
		h.Set(HeaderNetworkID, strconv.FormatUint(uint64(cfg.NetworkID), 10))
	}

	networkTime := uint64(time.Now().Unix()) - XRPLEpochOffset
	h.Set(HeaderNetworkTime, strconv.FormatUint(networkTime, 10))
	h.Set(HeaderPublicKey, id.EncodedPublicKey())

	sig, err := id.Sign(sharedValue)
	if err == nil {
		h.Set(HeaderSessionSignature, base64.StdEncoding.EncodeToString(sig))
	}
}

// VerifyPeerHandshake validates the handshake headers and verifies the session signature.
func VerifyPeerHandshake(headers http.Header, sharedValue []byte, localPubKey string, cfg HandshakeConfig) (*PublicKeyToken, error) {
	pubKeyStr := headers.Get(HeaderPublicKey)
	if pubKeyStr == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidHandshake, HeaderPublicKey)
	}

	pubKey, err := ParsePublicKeyToken(pubKeyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
	}

	if pubKeyStr == localPubKey {
		return nil, ErrSelfConnection
	}

	if netIDStr := headers.Get(HeaderNetworkID); netIDStr != "" {
		netID, err := strconv.ParseUint(netIDStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid network ID: %w", err)
		}
		if cfg.NetworkID > 0 && uint32(netID) != cfg.NetworkID {
			return nil, fmt.Errorf("%w: expected %d, got %d", ErrNetworkMismatch, cfg.NetworkID, netID)
		}
	}

	if netTimeStr := headers.Get(HeaderNetworkTime); netTimeStr != "" {
		netTime, err := strconv.ParseInt(netTimeStr, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid network time: %w", err)
		}

		peerTime := time.Unix(netTime+XRPLEpochOffset, 0)
		diff := time.Since(peerTime)
		if diff < 0 {
			diff = -diff
		}
		if diff > NetworkClockTolerance {
			return nil, fmt.Errorf("%w: clock skew %v", ErrHandshakeFailed, diff)
		}
	}

	sigStr := headers.Get(HeaderSessionSignature)
	if sigStr == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidHandshake, HeaderSessionSignature)
	}

	sigBytes, err := base64.StdEncoding.DecodeString(sigStr)
	if err != nil {
		return nil, fmt.Errorf("invalid signature encoding: %w", err)
	}

	if err := verifySessionSignature(pubKey, sharedValue, sigBytes); err != nil {
		return nil, err
	}

	return pubKey, nil
}

func verifySessionSignature(pubKey *PublicKeyToken, sharedValue, signature []byte) error {
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	h := sha512.New()
	h.Write(sharedValue)
	hash := h.Sum(nil)[:32]

	if !sig.Verify(hash, pubKey.BtcecKey()) {
		return ErrInvalidSignature
	}

	return nil
}

func crawlValue(public bool) string {
	if public {
		return "public"
	}
	return "private"
}

// ParseHandshakeProtocolVersion extracts the protocol version from the Upgrade header.
func ParseHandshakeProtocolVersion(upgradeHeader string) string {
	versions := strings.Split(upgradeHeader, ",")
	for _, v := range versions {
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "XRPL/") || strings.HasPrefix(v, "RTXP/") {
			return v
		}
	}
	return ""
}

// Feature represents a protocol feature.
type Feature int

const (
	FeatureValidatorListPropagation Feature = iota
	FeatureLedgerReplay
	FeatureCompression
	FeatureReduceRelay
	FeatureTransactionBatching
)

// String returns the string representation of a feature.
func (f Feature) String() string {
	switch f {
	case FeatureValidatorListPropagation:
		return "validatorListPropagation"
	case FeatureLedgerReplay:
		return "ledgerReplay"
	case FeatureCompression:
		return "compression"
	case FeatureReduceRelay:
		return "reduceRelay"
	case FeatureTransactionBatching:
		return "transactionBatching"
	default:
		return "unknown"
	}
}

// ParseFeature parses a feature string.
func ParseFeature(s string) (Feature, bool) {
	switch strings.ToLower(s) {
	case "validatorlistpropagation":
		return FeatureValidatorListPropagation, true
	case "ledgerreplay":
		return FeatureLedgerReplay, true
	case "compression":
		return FeatureCompression, true
	case "reducerelay":
		return FeatureReduceRelay, true
	case "transactionbatching":
		return FeatureTransactionBatching, true
	default:
		return 0, false
	}
}

// FeatureSet represents a set of supported features.
type FeatureSet struct {
	mu       sync.RWMutex
	features map[Feature]bool
}

// NewFeatureSet creates a new feature set.
func NewFeatureSet() *FeatureSet {
	return &FeatureSet{
		features: make(map[Feature]bool),
	}
}

// DefaultFeatureSet returns the default set of features we support.
func DefaultFeatureSet() *FeatureSet {
	fs := NewFeatureSet()
	fs.Enable(FeatureCompression)
	fs.Enable(FeatureReduceRelay)
	fs.Enable(FeatureValidatorListPropagation)
	return fs
}

// Enable enables a feature.
func (fs *FeatureSet) Enable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.features[f] = true
}

// Disable disables a feature.
func (fs *FeatureSet) Disable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.features, f)
}

// Has returns true if the feature is enabled.
func (fs *FeatureSet) Has(f Feature) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.features[f]
}

// List returns all enabled features.
func (fs *FeatureSet) List() []Feature {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	result := make([]Feature, 0, len(fs.features))
	for f := range fs.features {
		result = append(result, f)
	}
	return result
}

// Intersect returns features that are in both sets.
func (fs *FeatureSet) Intersect(other *FeatureSet) *FeatureSet {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	other.mu.RLock()
	defer other.mu.RUnlock()

	result := NewFeatureSet()
	for f := range fs.features {
		if other.features[f] {
			result.features[f] = true
		}
	}
	return result
}

// PeerCapabilities represents the negotiated capabilities of a peer.
type PeerCapabilities struct {
	mu sync.RWMutex

	ProtocolMajor int
	ProtocolMinor int
	Features      *FeatureSet
	NetworkID     uint32
	ListeningPort uint16
	SupportsCrawl bool
	IsValidator   bool
}

// NewPeerCapabilities creates new peer capabilities.
func NewPeerCapabilities() *PeerCapabilities {
	return &PeerCapabilities{
		Features: NewFeatureSet(),
	}
}

// HasFeature returns true if the peer has a feature.
func (pc *PeerCapabilities) HasFeature(f Feature) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.Features.Has(f)
}

// SupportsCompression returns true if the peer supports compression.
func (pc *PeerCapabilities) SupportsCompression() bool {
	return pc.HasFeature(FeatureCompression)
}

// SupportsReduceRelay returns true if the peer supports reduce-relay.
func (pc *PeerCapabilities) SupportsReduceRelay() bool {
	return pc.HasFeature(FeatureReduceRelay)
}

// X-Protocol-Ctl header constants for feature negotiation.
// Format: feature1=value1[,value2]*[\s*;\s*feature2=value1[,value2]*]*
const (
	HeaderProtocolCtl = "X-Protocol-Ctl"

	// Feature names as defined in rippled
	FeatureNameCompr        = "compr"
	FeatureNameVPRR         = "vprr" // validation/proposal reduce-relay
	FeatureNameTXRR         = "txrr" // transaction reduce-relay
	FeatureNameLedgerReplay = "ledgerreplay"

	// Delimiters
	FeatureDelimiter = ";"
	ValueDelimiter   = ","
)

// GetFeatureValue retrieves the value of a feature from the X-Protocol-Ctl header.
// Returns the feature value and true if found, empty string and false otherwise.
// Reference: rippled Handshake.cpp getFeatureValue()
func GetFeatureValue(headers http.Header, feature string) (string, bool) {
	headerValue := headers.Get(HeaderProtocolCtl)
	if headerValue == "" {
		return "", false
	}

	// Parse features separated by semicolons
	features := strings.Split(headerValue, FeatureDelimiter)
	for _, f := range features {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}

		// Split on first '=' to get feature name and value
		parts := strings.SplitN(f, "=", 2)
		if len(parts) != 2 {
			continue
		}

		name := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		if strings.EqualFold(name, feature) {
			return value, true
		}
	}

	return "", false
}

// IsFeatureValue checks if a feature has a specific value in the X-Protocol-Ctl header.
// The value can be one of multiple comma-separated values.
// Reference: rippled Handshake.cpp isFeatureValue()
func IsFeatureValue(headers http.Header, feature, value string) bool {
	featureValue, found := GetFeatureValue(headers, feature)
	if !found {
		return false
	}

	// Check if value is in the comma-separated list
	values := strings.Split(featureValue, ValueDelimiter)
	for _, v := range values {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return true
		}
	}

	return false
}

// FeatureEnabled checks if a feature is enabled (has value "1").
// Reference: rippled Handshake.cpp featureEnabled()
func FeatureEnabled(headers http.Header, feature string) bool {
	return IsFeatureValue(headers, feature, "1")
}

// PeerFeatureEnabled checks if a feature should be enabled for a peer.
// The feature is enabled if the local config enables it and the peer's header
// contains the specified feature value.
// Reference: rippled Handshake.h peerFeatureEnabled()
func PeerFeatureEnabled(headers http.Header, feature, value string, localEnabled bool) bool {
	return localEnabled && IsFeatureValue(headers, feature, value)
}

// MakeFeaturesRequestHeader creates the X-Protocol-Ctl header value for a request.
// Reference: rippled Handshake.cpp makeFeaturesRequestHeader()
func MakeFeaturesRequestHeader(comprEnabled, ledgerReplayEnabled, txReduceRelayEnabled, vpReduceRelayEnabled bool) string {
	var parts []string

	if comprEnabled {
		parts = append(parts, FeatureNameCompr+"=lz4")
	}
	if ledgerReplayEnabled {
		parts = append(parts, FeatureNameLedgerReplay+"=1")
	}
	if txReduceRelayEnabled {
		parts = append(parts, FeatureNameTXRR+"=1")
	}
	if vpReduceRelayEnabled {
		parts = append(parts, FeatureNameVPRR+"=1")
	}

	return strings.Join(parts, FeatureDelimiter)
}

// MakeFeaturesResponseHeader creates the X-Protocol-Ctl header value for a response.
// Only includes features that are both locally enabled and requested by the peer.
// Reference: rippled Handshake.cpp makeFeaturesResponseHeader()
func MakeFeaturesResponseHeader(requestHeaders http.Header, comprEnabled, ledgerReplayEnabled, txReduceRelayEnabled, vpReduceRelayEnabled bool) string {
	var parts []string

	if comprEnabled && IsFeatureValue(requestHeaders, FeatureNameCompr, "lz4") {
		parts = append(parts, FeatureNameCompr+"=lz4")
	}
	if ledgerReplayEnabled && FeatureEnabled(requestHeaders, FeatureNameLedgerReplay) {
		parts = append(parts, FeatureNameLedgerReplay+"=1")
	}
	if txReduceRelayEnabled && FeatureEnabled(requestHeaders, FeatureNameTXRR) {
		parts = append(parts, FeatureNameTXRR+"=1")
	}
	if vpReduceRelayEnabled && FeatureEnabled(requestHeaders, FeatureNameVPRR) {
		parts = append(parts, FeatureNameVPRR+"=1")
	}

	return strings.Join(parts, FeatureDelimiter)
}

// ParseProtocolCtlFeatures parses the X-Protocol-Ctl header and returns negotiated capabilities.
func ParseProtocolCtlFeatures(headers http.Header) *FeatureSet {
	fs := NewFeatureSet()

	if IsFeatureValue(headers, FeatureNameCompr, "lz4") {
		fs.Enable(FeatureCompression)
	}
	if FeatureEnabled(headers, FeatureNameLedgerReplay) {
		fs.Enable(FeatureLedgerReplay)
	}
	if FeatureEnabled(headers, FeatureNameTXRR) || FeatureEnabled(headers, FeatureNameVPRR) {
		fs.Enable(FeatureReduceRelay)
	}

	return fs
}
