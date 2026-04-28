package peermanagement

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
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
	HeaderInstanceCookie   = "Instance-Cookie"
	HeaderServerDomain     = "Server-Domain"
	HeaderRemoteIP         = "Remote-IP"
	HeaderLocalIP          = "Local-IP"
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

	// Protocol feature toggles advertised via X-Protocol-Ctl. Rippled
	// peers gate feature-specific requests (mtREPLAY_DELTA_REQUEST,
	// mtTRANSACTIONS batch relay, LZ4 compression) on the handshake
	// advertisement — if we don't tell peers we support a feature, they
	// won't send it to us and they won't accept it from us. Matches
	// rippled's Handshake.cpp / peerFeatureEnabled.
	EnableLedgerReplay  bool
	EnableCompression   bool
	EnableVPReduceRelay bool
	EnableTxReduceRelay bool

	// InstanceCookie is emitted on every handshake. Production wiring
	// goes through Overlay where the value is generated non-zero;
	// callers that build a HandshakeConfig directly own setting it.
	InstanceCookie uint64
	// ServerDomain is the operator domain; empty suppresses the header.
	ServerDomain string
	// PublicIP is our observed public address; nil/unspecified suppresses
	// Local-IP emission and disables the Remote-IP consistency check.
	PublicIP net.IP
	// LedgerHintProvider returns (hints, ok). ok=false suppresses both
	// Closed-Ledger and Previous-Ledger headers.
	LedgerHintProvider func() (hints LedgerHints, ok bool)
}

// LedgerHints is the (closed, parent) pair for the ledger hints.
type LedgerHints struct {
	Closed [32]byte
	Parent [32]byte
}

// DefaultHandshakeConfig returns default handshake configuration.
func DefaultHandshakeConfig() HandshakeConfig {
	return HandshakeConfig{
		UserAgent:          "goXRPL/0.1.0",
		NetworkID:          0,
		CrawlPublic:        false,
		EnableLedgerReplay: true, // Phase B wires the server+client paths
	}
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

// WriteRawHandshakeRequest writes the handshake request as raw bytes.
// Rippled's HTTP parser is strict and rejects the extra headers
// (Host, Content-Length, etc.) that Go's http.Request.Write adds.
func WriteRawHandshakeRequest(w io.Writer, req *http.Request) error {
	var buf bytes.Buffer
	buf.WriteString("GET / HTTP/1.1\r\n")
	// Write headers in a fixed order for predictability
	writeHeader := func(key string) {
		for _, v := range req.Header.Values(key) {
			buf.WriteString(key + ": " + v + "\r\n")
		}
	}
	writeHeader(HeaderUserAgent)
	writeHeader(HeaderUpgrade)
	writeHeader(HeaderConnection)
	writeHeader(HeaderConnectAs)
	writeHeader(HeaderCrawl)
	writeHeader(HeaderPublicKey)
	writeHeader(HeaderSessionSignature)
	writeHeader(HeaderNetworkID)
	writeHeader(HeaderNetworkTime)
	// X-Protocol-Ctl advertises the features (ledgerreplay, compr, vprr,
	// txrr) the outbound side is willing to negotiate. Missing from the
	// initial whitelist — without it the peer's inbound parser sees an
	// empty feature set and refuses to serve feature-gated requests
	// like mtREPLAY_DELTA_REQ and mtPROOF_PATH_REQ. Mirrors rippled's
	// makeHandshakeHeaders writing the same header on outbound.
	writeHeader(HeaderProtocolCtl)
	writeHeader(HeaderInstanceCookie)
	writeHeader(HeaderServerDomain)
	writeHeader(HeaderClosedLedger)
	writeHeader(HeaderPreviousLedger)
	writeHeader(HeaderRemoteIP)
	writeHeader(HeaderLocalIP)
	buf.WriteString("\r\n")
	_, err := w.Write(buf.Bytes())
	return err
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

	sig, err := id.SignDigest(sharedValue)
	if err == nil {
		h.Set(HeaderSessionSignature, base64.StdEncoding.EncodeToString(sig))
	}

	// Advertise supported protocol features so peers know what to
	// request from us (and what to accept from us). Without this, our
	// replay-delta handlers, compression, and reduce-relay relay paths
	// are silently gated off by any standards-compliant peer.
	if ctl := MakeFeaturesRequestHeader(
		cfg.EnableCompression,
		cfg.EnableLedgerReplay,
		cfg.EnableTxReduceRelay,
		cfg.EnableVPReduceRelay,
	); ctl != "" {
		h.Set(HeaderProtocolCtl, ctl)
	}

	// Always emitted (rippled Handshake.cpp:208).
	h.Set(HeaderInstanceCookie, strconv.FormatUint(cfg.InstanceCookie, 10))
	if cfg.ServerDomain != "" {
		h.Set(HeaderServerDomain, cfg.ServerDomain)
	}
	if cfg.LedgerHintProvider != nil {
		if hints, ok := cfg.LedgerHintProvider(); ok {
			// Uppercase to match rippled's strHex.
			h.Set(HeaderClosedLedger, strings.ToUpper(hex.EncodeToString(hints.Closed[:])))
			h.Set(HeaderPreviousLedger, strings.ToUpper(hex.EncodeToString(hints.Parent[:])))
		}
	}
}

// addAddressHeaders emits Remote-IP / Local-IP per Handshake.cpp:213-217.
// Per-conn helper (peerRemote isn't available at HandshakeConfig time).
func addAddressHeaders(h http.Header, cfg HandshakeConfig, peerRemote net.IP) {
	if peerRemote != nil && isPublicIP(peerRemote) {
		h.Set(HeaderRemoteIP, peerRemote.String())
	}
	if cfg.PublicIP != nil && !cfg.PublicIP.IsUnspecified() {
		h.Set(HeaderLocalIP, cfg.PublicIP.String())
	}
}

// ipFamilyEqual compares two IPs taking address family into account
// (v4 vs v6) to mirror boost::asio::ip::address::operator==, which
// treats ::ffff:1.2.3.4 and 1.2.3.4 as distinct because their family
// differs. Go's net.IP.Equal is family-agnostic and would equate them.
//
// Heuristic: a 4-byte slice or a v4-mapped 16-byte slice is "v4". Any
// other 16-byte slice is "v6". headerIPv6Form lets the caller pass the
// original textual form so a header value like "::ffff:1.2.3.4" stays
// classified as v6 even after net.ParseIP normalises it to the same
// bytes as "1.2.3.4".
func ipFamilyEqual(a, b net.IP, aIsV6Text, bIsV6Text bool) bool {
	if ipFamilyV4(a, aIsV6Text) != ipFamilyV4(b, bIsV6Text) {
		return false
	}
	return a.Equal(b)
}

func ipFamilyV4(ip net.IP, isV6Text bool) bool {
	if isV6Text {
		return false
	}
	return ip.To4() != nil
}

// isIPv6Text returns true when s is written in IPv6 textual form
// (contains a colon). Used to distinguish "::ffff:1.2.3.4" from
// "1.2.3.4" since net.ParseIP normalises both to the same bytes.
func isIPv6Text(s string) bool {
	return strings.Contains(s, ":")
}

// isPublicIP mirrors beast::IP::is_public: !is_private && !is_multicast.
// beast's is_private = RFC1918 + loopback. Link-local stays public.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() {
		return false
	}
	return true
}

// parseLedgerHashHeader: hex (rippled wire format) or 32-byte base64
// fallback (PeerImp::run accepts both).
func parseLedgerHashHeader(s string) ([32]byte, error) {
	var out [32]byte
	if len(s) == hex.EncodedLen(32) {
		if _, err := hex.Decode(out[:], []byte(s)); err == nil {
			return out, nil
		}
	}
	if dec, err := base64.StdEncoding.DecodeString(s); err == nil && len(dec) == 32 {
		copy(out[:], dec)
		return out, nil
	}
	return out, fmt.Errorf("unrecognised ledger hash %q", s)
}

// isWellFormedDomain ports rippled's isProperlyFormedTomlDomain
// (StringUtilities.cpp:131-156).
func isWellFormedDomain(s string) bool {
	if len(s) < 4 || len(s) > 128 {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	tld := labels[len(labels)-1]
	if len(tld) < 2 || len(tld) > 63 {
		return false
	}
	for i := 0; i < len(tld); i++ {
		c := tld[i]
		if !((c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	for _, label := range labels[:len(labels)-1] {
		if len(label) < 1 || len(label) > 63 {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for i := 0; i < len(label); i++ {
			c := label[i]
			ok := (c >= 'A' && c <= 'Z') ||
				(c >= 'a' && c <= 'z') ||
				(c >= '0' && c <= '9') ||
				c == '-'
			if !ok {
				return false
			}
		}
	}
	return true
}

// VerifyPeerHandshake validates the handshake headers and verifies the
// session signature. Check order mirrors rippled's verifyHandshake
// (Handshake.cpp:227-362) starting from Network-ID:
// Network-ID → Network-Time → Public-Key → Session-Signature → self-connection.
//
// Server-Domain (Handshake.cpp:235-239 — the very first throw) is the
// caller's responsibility via ValidateServerDomain. It must be invoked
// BEFORE this function so a malformed Server-Domain is rejected before
// any signature work.
func VerifyPeerHandshake(headers http.Header, sharedValue []byte, localPubKey string, cfg HandshakeConfig) (*PublicKeyToken, error) {
	// Network-ID parity with rippled (Handshake.cpp:241-250). cfg.NetworkID
	// == 0 stands in for rippled's `std::optional<networkID>` being unseated
	// (mainnet / no network configured): in that mode the header is not
	// checked at all, even if the peer advertises a non-zero value. A peer
	// that omits Network-ID is always accepted — rippled only enforces the
	// match when both the header is present AND the local networkID is
	// seated.
	if netIDStr := headers.Get(HeaderNetworkID); netIDStr != "" {
		netID, err := strconv.ParseUint(netIDStr, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid network ID: %w", err)
		}
		if cfg.NetworkID != 0 && uint32(netID) != cfg.NetworkID {
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

	pubKeyStr := headers.Get(HeaderPublicKey)
	if pubKeyStr == "" {
		return nil, fmt.Errorf("%w: missing %s", ErrInvalidHandshake, HeaderPublicKey)
	}

	pubKey, err := ParsePublicKeyToken(pubKeyStr)
	if err != nil {
		return nil, fmt.Errorf("invalid public key: %w", err)
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

	if pubKeyStr == localPubKey {
		return nil, ErrSelfConnection
	}

	return pubKey, nil
}

func verifySessionSignature(pubKey *PublicKeyToken, sharedValue, signature []byte) error {
	sig, err := ecdsa.ParseDERSignature(signature)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSignature, err)
	}

	// The shared value is already a 32-byte SHA-512 Half digest.
	// Verify directly against it (matching rippled's verifyDigest).
	if !sig.Verify(sharedValue, pubKey.BtcecKey()) {
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
	// FeatureVpReduceRelay corresponds to rippled's vprr
	// (validator-proposal reduce-relay). Gating on this feature
	// controls validator-proposal + validation relay suppression and
	// TMSquelch for validators.
	FeatureVpReduceRelay
	// FeatureTxReduceRelay corresponds to rippled's txrr (transaction
	// reduce-relay). A peer may enable TXRR without VPRR and
	// vice-versa; they are independent in rippled's Handshake.cpp.
	FeatureTxReduceRelay
	FeatureTransactionBatching
)

// FeatureReduceRelay is kept as a transitional alias for the VPRR
// flag so existing callers that "check for reduce-relay" continue to
// compile; new code should pick FeatureVpReduceRelay or
// FeatureTxReduceRelay explicitly.
const FeatureReduceRelay = FeatureVpReduceRelay

// String returns the string representation of a feature.
func (f Feature) String() string {
	switch f {
	case FeatureValidatorListPropagation:
		return "validatorListPropagation"
	case FeatureLedgerReplay:
		return "ledgerReplay"
	case FeatureCompression:
		return "compression"
	case FeatureVpReduceRelay:
		return "vpReduceRelay"
	case FeatureTxReduceRelay:
		return "txReduceRelay"
	case FeatureTransactionBatching:
		return "transactionBatching"
	default:
		return "unknown"
	}
}

// ParseFeature parses a feature string. Accepts both "reduceRelay"
// (legacy) and the explicit vprr/txrr names.
func ParseFeature(s string) (Feature, bool) {
	switch strings.ToLower(s) {
	case "validatorlistpropagation":
		return FeatureValidatorListPropagation, true
	case "ledgerreplay":
		return FeatureLedgerReplay, true
	case "compression":
		return FeatureCompression, true
	case "reducerelay", "vpreducerelay", "vprr":
		return FeatureVpReduceRelay, true
	case "txreducerelay", "txrr":
		return FeatureTxReduceRelay, true
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
//
// Only fields that are actually populated during handshake (via
// ParseProtocolCtlFeatures) are kept here. Previously this struct
// carried ProtocolMajor/Minor, ListeningPort, SupportsCrawl, and
// IsValidator — none of which were ever written, so every HasFeature-
// adjacent query silently returned the zero value. Removing them
// avoids dead code and makes the wire contract explicit: if a field
// shows up here, the handshake populates it.
type PeerCapabilities struct {
	mu sync.RWMutex

	Features *FeatureSet
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

// HandshakeExtras carries the headers parsed by ParseHandshakeExtras.
//
// HasClosedLedger and HasPreviousLedger track presence of the
// corresponding headers independently — rippled's PeerImp.cpp:198-201
// keeps them as separate optionals because a peer may advertise a
// closed ledger without a parent (cold-start, post-pruning).
type HandshakeExtras struct {
	InstanceCookie    uint64
	ServerDomain      string
	ClosedLedger      [32]byte
	PreviousLedger    [32]byte
	HasClosedLedger   bool
	HasPreviousLedger bool
	RemoteIPSelf      string // peer's view of our public IP
	LocalIPSelf       string // peer's view of their own public IP
}

// ValidateServerDomain enforces verifyHandshake's Server-Domain check
// (Handshake.cpp:235-239). Run first to match rippled's verify order
// (Server-Domain → Network-ID → Network-Time → Public-Key → ...).
func ValidateServerDomain(headers http.Header) (string, error) {
	v := headers.Get(HeaderServerDomain)
	if v == "" {
		return "", nil
	}
	if !isWellFormedDomain(v) {
		return "", fmt.Errorf("%w: invalid Server-Domain %q",
			ErrInvalidHandshake, v)
	}
	return v, nil
}

// ParseHandshakeExtras enforces the post-signature checks: ledger-hash
// malformed (PeerImp.cpp:175-191), Previous-without-Closed
// (PeerImp.cpp:193-194), Local-IP / Remote-IP consistency
// (Handshake.cpp:325-359). Instance-Cookie is stored only.
// Server-Domain is handled separately by ValidateServerDomain (which
// must run first to match rippled's order). peerRemote == nil
// disables the IP comparisons.
func ParseHandshakeExtras(
	headers http.Header,
	localPublicIP net.IP,
	peerRemote net.IP,
) (HandshakeExtras, error) {
	var out HandshakeExtras

	if v := headers.Get(HeaderInstanceCookie); v != "" {
		cookie, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return out, fmt.Errorf("%w: malformed Instance-Cookie %q: %v",
				ErrInvalidHandshake, v, err)
		}
		out.InstanceCookie = cookie
	}

	// Server-Domain is validated upstream by ValidateServerDomain
	// (rippled order); we just surface the value here.
	if v := headers.Get(HeaderServerDomain); v != "" {
		out.ServerDomain = v
	}

	if v := headers.Get(HeaderClosedLedger); v != "" {
		h, err := parseLedgerHashHeader(v)
		if err != nil {
			return out, fmt.Errorf("%w: malformed Closed-Ledger %q: %v",
				ErrInvalidHandshake, v, err)
		}
		out.ClosedLedger = h
		out.HasClosedLedger = true
	}
	if v := headers.Get(HeaderPreviousLedger); v != "" {
		h, err := parseLedgerHashHeader(v)
		if err != nil {
			return out, fmt.Errorf("%w: malformed Previous-Ledger %q: %v",
				ErrInvalidHandshake, v, err)
		}
		out.PreviousLedger = h
		out.HasPreviousLedger = true
	}
	if out.HasPreviousLedger && !out.HasClosedLedger {
		return out, fmt.Errorf("%w: Previous-Ledger without Closed-Ledger",
			ErrInvalidHandshake)
	}

	if v := headers.Get(HeaderLocalIP); v != "" {
		localReported := net.ParseIP(v)
		if localReported == nil {
			return out, fmt.Errorf("%w: invalid Local-IP %q",
				ErrInvalidHandshake, v)
		}
		out.LocalIPSelf = localReported.String()
		if peerRemote != nil && isPublicIP(peerRemote) &&
			!ipFamilyEqual(peerRemote, localReported, false, isIPv6Text(v)) {
			return out, fmt.Errorf("%w: Incorrect Local-IP: %s instead of %s",
				ErrInvalidHandshake, peerRemote.String(), localReported.String())
		}
	}

	if v := headers.Get(HeaderRemoteIP); v != "" {
		remoteReported := net.ParseIP(v)
		if remoteReported == nil {
			return out, fmt.Errorf("%w: invalid Remote-IP %q",
				ErrInvalidHandshake, v)
		}
		out.RemoteIPSelf = remoteReported.String()
		if peerRemote != nil && isPublicIP(peerRemote) &&
			localPublicIP != nil && !localPublicIP.IsUnspecified() &&
			!ipFamilyEqual(remoteReported, localPublicIP, isIPv6Text(v), false) {
			return out, fmt.Errorf("%w: Incorrect Remote-IP: %s instead of %s",
				ErrInvalidHandshake, localPublicIP.String(), remoteReported.String())
		}
	}

	return out, nil
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
	// Track txrr and vprr independently. Rippled's Handshake.cpp
	// publishes two separate features so operators can enable one
	// without the other; collapsing them into a single flag loses the
	// ability to correctly gate per-feature behavior (TMSquelch is
	// VPRR, tx relay suppression is TXRR).
	if FeatureEnabled(headers, FeatureNameTXRR) {
		fs.Enable(FeatureTxReduceRelay)
	}
	if FeatureEnabled(headers, FeatureNameVPRR) {
		fs.Enable(FeatureVpReduceRelay)
	}

	return fs
}
