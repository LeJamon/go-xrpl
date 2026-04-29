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

	"github.com/LeJamon/goXRPLd/crypto/secp256k1"
)

const (
	ProtocolVersion       = "XRPL/2.2"
	LegacyProtocolVersion = "RTXP/1.2"
)

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
	HeaderServer           = "Server"
)

const (
	// XRPLEpochOffset converts Unix seconds to XRPL epoch (2000-01-01).
	XRPLEpochOffset       = 946684800
	NetworkClockTolerance = 20 * time.Second
)

type HandshakeConfig struct {
	UserAgent   string
	NetworkID   uint32
	CrawlPublic bool

	// X-Protocol-Ctl advertisements. Peers gate feature-specific
	// messages on these flags in both directions.
	EnableLedgerReplay  bool
	EnableCompression   bool
	EnableVPReduceRelay bool
	EnableTxReduceRelay bool

	InstanceCookie     uint64
	ServerDomain       string
	PublicIP           net.IP // nil disables Local-IP emission and Remote-IP check
	LedgerHintProvider func() (hints LedgerHints, ok bool)
}

type LedgerHints struct {
	Closed [32]byte
	Parent [32]byte
}

func DefaultHandshakeConfig() HandshakeConfig {
	return HandshakeConfig{
		UserAgent:          "goXRPL/0.1.0",
		EnableLedgerReplay: true,
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

// WriteRawHandshakeRequest writes the request without the extra headers
// (Host, Content-Length, ...) that http.Request.Write adds — rippled's
// parser rejects them.
func WriteRawHandshakeRequest(w io.Writer, req *http.Request) error {
	var buf bytes.Buffer
	buf.WriteString("GET / HTTP/1.1\r\n")
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
	// rippled reads the Server header via PeerImp::getVersion.
	if cfg.UserAgent != "" {
		resp.Header.Set(HeaderServer, cfg.UserAgent)
	}

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

	if ctl := MakeFeaturesRequestHeader(
		cfg.EnableCompression,
		cfg.EnableLedgerReplay,
		cfg.EnableTxReduceRelay,
		cfg.EnableVPReduceRelay,
	); ctl != "" {
		h.Set(HeaderProtocolCtl, ctl)
	}

	h.Set(HeaderInstanceCookie, strconv.FormatUint(cfg.InstanceCookie, 10))
	if cfg.ServerDomain != "" {
		h.Set(HeaderServerDomain, cfg.ServerDomain)
	}
	if cfg.LedgerHintProvider != nil {
		if hints, ok := cfg.LedgerHintProvider(); ok {
			// Uppercase hex to match rippled's strHex.
			h.Set(HeaderClosedLedger, strings.ToUpper(hex.EncodeToString(hints.Closed[:])))
			h.Set(HeaderPreviousLedger, strings.ToUpper(hex.EncodeToString(hints.Parent[:])))
		}
	}
}

// addAddressHeaders emits Remote-IP / Local-IP. Per-conn because
// peerRemote isn't known at HandshakeConfig time.
func addAddressHeaders(h http.Header, cfg HandshakeConfig, peerRemote net.IP) {
	if peerRemote != nil && isPublicIP(peerRemote) {
		h.Set(HeaderRemoteIP, peerRemote.String())
	}
	if cfg.PublicIP != nil && !cfg.PublicIP.IsUnspecified() {
		h.Set(HeaderLocalIP, cfg.PublicIP.String())
	}
}

// ipFamilyEqual mirrors boost::asio::ip::address::operator==: families
// must agree before bytes match. Go's net.IP.Equal would equate
// ::ffff:1.2.3.4 with 1.2.3.4, so callers pass family hints explicitly.
func ipFamilyEqual(a, b net.IP, aIsV6, bIsV6 bool) bool {
	if aIsV6 != bIsV6 {
		return false
	}
	return a.Equal(b)
}

// socketIPIsV6: a 16-byte slice from a TCPAddr came from an AF_INET6
// socket (including v4-mapped). To4()-based classification would falsely
// flag those as v4 and reject "::ffff:x.x.x.x" peers.
func socketIPIsV6(ip net.IP) bool {
	return len(ip) == net.IPv6len
}

// headerIPIsV6 reads the family from textual form because net.ParseIP
// normalises both forms to the same 16-byte slice.
func headerIPIsV6(s string) bool {
	return strings.Contains(s, ":")
}

// configIPIsV6 classifies operator-config IPs (no original text). Pure
// v6 has To4()==nil; v4 and v4-mapped both surface as v4.
func configIPIsV6(ip net.IP) bool {
	return ip.To4() == nil
}

// isPublicIP mirrors beast::IP::is_public. v6 link-local is private
// (fe80::/10) — Go's IsPrivate doesn't cover that, so we add it.
func isPublicIP(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() {
		return false
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsMulticast() {
		return false
	}
	if ip.To4() == nil && ip.IsLinkLocalUnicast() {
		return false
	}
	return true
}

// parseLedgerHashHeader accepts hex or 32-byte base64 (PeerImp::run does too).
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

// isWellFormedDomain mirrors rippled's isProperlyFormedTomlDomain.
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

// VerifyPeerHandshake runs the post-Server-Domain rippled verify chain:
// Network-ID → Network-Time → Public-Key → Session-Signature →
// self-connection. Callers must run ValidateServerDomain first.
func VerifyPeerHandshake(headers http.Header, sharedValue []byte, localPubKey string, cfg HandshakeConfig) (*PublicKeyToken, error) {
	// cfg.NetworkID == 0 means "unconfigured" (mainnet); the header is
	// only enforced when both sides are seated and disagree.
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
	if !secp256k1.VerifyDigestBytes(sharedValue, pubKey.Bytes(), signature) {
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

type Feature int

const (
	FeatureValidatorListPropagation Feature = iota
	FeatureLedgerReplay
	FeatureCompression
	// vprr — validator-proposal reduce-relay (gates TMSquelch).
	FeatureVpReduceRelay
	// txrr — transaction reduce-relay. Independent of vprr.
	FeatureTxReduceRelay
	FeatureTransactionBatching
)

// FeatureReduceRelay is a legacy alias for FeatureVpReduceRelay.
const FeatureReduceRelay = FeatureVpReduceRelay

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

// ParseFeature accepts the legacy "reduceRelay" alias plus vprr/txrr.
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

func NewFeatureSet() *FeatureSet {
	return &FeatureSet{
		features: make(map[Feature]bool),
	}
}

func DefaultFeatureSet() *FeatureSet {
	fs := NewFeatureSet()
	fs.Enable(FeatureCompression)
	fs.Enable(FeatureReduceRelay)
	fs.Enable(FeatureValidatorListPropagation)
	return fs
}

func (fs *FeatureSet) Enable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.features[f] = true
}

func (fs *FeatureSet) Disable(f Feature) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	delete(fs.features, f)
}

func (fs *FeatureSet) Has(f Feature) bool {
	fs.mu.RLock()
	defer fs.mu.RUnlock()
	return fs.features[f]
}

func (fs *FeatureSet) List() []Feature {
	fs.mu.RLock()
	defer fs.mu.RUnlock()

	result := make([]Feature, 0, len(fs.features))
	for f := range fs.features {
		result = append(result, f)
	}
	return result
}

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

// PeerCapabilities holds only fields that the handshake actually
// populates — no protocol metadata stored as zero values.
type PeerCapabilities struct {
	mu       sync.RWMutex
	Features *FeatureSet
}

func NewPeerCapabilities() *PeerCapabilities {
	return &PeerCapabilities{
		Features: NewFeatureSet(),
	}
}

func (pc *PeerCapabilities) HasFeature(f Feature) bool {
	pc.mu.RLock()
	defer pc.mu.RUnlock()
	return pc.Features.Has(f)
}

func (pc *PeerCapabilities) SupportsCompression() bool {
	return pc.HasFeature(FeatureCompression)
}

func (pc *PeerCapabilities) SupportsReduceRelay() bool {
	return pc.HasFeature(FeatureReduceRelay)
}

// X-Protocol-Ctl: feature1=v1,v2;feature2=v3
const (
	HeaderProtocolCtl = "X-Protocol-Ctl"

	FeatureNameCompr        = "compr"
	FeatureNameVPRR         = "vprr"
	FeatureNameTXRR         = "txrr"
	FeatureNameLedgerReplay = "ledgerreplay"

	FeatureDelimiter = ";"
	ValueDelimiter   = ","
)

func GetFeatureValue(headers http.Header, feature string) (string, bool) {
	headerValue := headers.Get(HeaderProtocolCtl)
	if headerValue == "" {
		return "", false
	}
	for _, f := range strings.Split(headerValue, FeatureDelimiter) {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
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

// IsFeatureValue reports whether feature's comma-separated value list
// contains value.
func IsFeatureValue(headers http.Header, feature, value string) bool {
	featureValue, found := GetFeatureValue(headers, feature)
	if !found {
		return false
	}
	for _, v := range strings.Split(featureValue, ValueDelimiter) {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return true
		}
	}
	return false
}

func FeatureEnabled(headers http.Header, feature string) bool {
	return IsFeatureValue(headers, feature, "1")
}

func PeerFeatureEnabled(headers http.Header, feature, value string, localEnabled bool) bool {
	return localEnabled && IsFeatureValue(headers, feature, value)
}

// HandshakeExtras carries the typed headers ParseHandshakeExtras
// surfaces. Instance-Cookie / Local-IP / Remote-IP round-trip on the
// wire but are validated-and-discarded (matching rippled PeerImp).
type HandshakeExtras struct {
	ServerDomain      string
	ClosedLedger      [32]byte
	PreviousLedger    [32]byte
	HasClosedLedger   bool
	HasPreviousLedger bool
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
// (Handshake.cpp:325-359). Server-Domain is validated separately by
// ValidateServerDomain (which must run first to match rippled's order).
// Instance-Cookie is emitted on the wire but never parsed (rippled's
// verifyHandshake doesn't inspect it). peerRemote == nil disables the
// IP comparisons.
func ParseHandshakeExtras(
	headers http.Header,
	localPublicIP net.IP,
	peerRemote net.IP,
) (HandshakeExtras, error) {
	var out HandshakeExtras

	// Server-Domain is validated by ValidateServerDomain upstream.
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

	// Local-IP / Remote-IP are validated and discarded (rippled
	// doesn't store them on PeerImp).
	if v := headers.Get(HeaderLocalIP); v != "" {
		localReported := net.ParseIP(v)
		if localReported == nil {
			return out, fmt.Errorf("%w: invalid Local-IP %q",
				ErrInvalidHandshake, v)
		}
		if peerRemote != nil && isPublicIP(peerRemote) &&
			!ipFamilyEqual(peerRemote, localReported,
				socketIPIsV6(peerRemote), headerIPIsV6(v)) {
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
		if peerRemote != nil && isPublicIP(peerRemote) &&
			localPublicIP != nil && !localPublicIP.IsUnspecified() &&
			!ipFamilyEqual(remoteReported, localPublicIP,
				headerIPIsV6(v), configIPIsV6(localPublicIP)) {
			return out, fmt.Errorf("%w: Incorrect Remote-IP: %s instead of %s",
				ErrInvalidHandshake, localPublicIP.String(), remoteReported.String())
		}
	}

	return out, nil
}

// MakeFeaturesRequestHeader builds the X-Protocol-Ctl value for a request.
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

// MakeFeaturesResponseHeader echoes back only features that are both
// locally enabled AND requested by the peer.
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

// ParseProtocolCtlFeatures decodes the negotiated capabilities. txrr
// and vprr are tracked independently — they gate different behaviour
// (tx relay vs TMSquelch) and operators can enable one without the
// other.
func ParseProtocolCtlFeatures(headers http.Header) *FeatureSet {
	fs := NewFeatureSet()

	if IsFeatureValue(headers, FeatureNameCompr, "lz4") {
		fs.Enable(FeatureCompression)
	}
	if FeatureEnabled(headers, FeatureNameLedgerReplay) {
		fs.Enable(FeatureLedgerReplay)
	}
	if FeatureEnabled(headers, FeatureNameTXRR) {
		fs.Enable(FeatureTxReduceRelay)
	}
	if FeatureEnabled(headers, FeatureNameVPRR) {
		fs.Enable(FeatureVpReduceRelay)
	}

	return fs
}
