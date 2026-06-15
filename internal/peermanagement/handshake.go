package peermanagement

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/stringutil"
	"github.com/LeJamon/go-xrpl/protocol"
)

// protocolVersion is a (major, minor) peer-protocol pair.
type protocolVersion struct{ major, minor uint16 }

func (v protocolVersion) String() string {
	return fmt.Sprintf("XRPL/%d.%d", v.major, v.minor)
}

func (v protocolVersion) less(o protocolVersion) bool {
	if v.major != o.major {
		return v.major < o.major
	}
	return v.minor < o.minor
}

// supportedProtocols lists the peer-protocol versions go-xrpl
// advertises. Must stay strictly ascending — duplicates are forbidden;
// enforced by init() below.
var supportedProtocols = []protocolVersion{{2, 1}, {2, 2}}

func init() {
	if len(supportedProtocols) == 0 {
		panic("peermanagement: supportedProtocols must not be empty")
	}
	for i := 1; i < len(supportedProtocols); i++ {
		if !supportedProtocols[i-1].less(supportedProtocols[i]) {
			panic(fmt.Sprintf("peermanagement: supportedProtocols must be strictly ascending, got %v", supportedProtocols))
		}
	}
}

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

const NetworkClockTolerance = 20 * time.Second

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
	req.Header.Set(HeaderUpgrade, SupportedProtocolVersions())
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

// BuildHandshakeResponse builds the 101 Switching Protocols response
// for an inbound handshake. `negotiated` is the version returned by
// NegotiateProtocolVersion against the inbound request; an empty value
// falls back to the highest supported version (test convenience).
func BuildHandshakeResponse(id *Identity, sharedValue []byte, cfg HandshakeConfig, negotiated string) *http.Response {
	if negotiated == "" {
		negotiated = supportedProtocols[len(supportedProtocols)-1].String()
	}
	resp := &http.Response{
		StatusCode: http.StatusSwitchingProtocols,
		Status:     "101 Switching Protocols",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}

	resp.Header.Set(HeaderConnection, "Upgrade")
	resp.Header.Set(HeaderUpgrade, negotiated)
	resp.Header.Set(HeaderConnectAs, "Peer")
	resp.Header.Set(HeaderCrawl, crawlValue(cfg.CrawlPublic))
	// rippled peers read our version string from the Server header.
	if cfg.UserAgent != "" {
		resp.Header.Set(HeaderServer, cfg.UserAgent)
	}

	addHandshakeHeaders(resp.Header, id, sharedValue, cfg)

	return resp
}

// BuildHandshakeErrorResponse builds the handshake rejection: 400 Bad
// Request — not 426 Upgrade Required, matching rippled — with the
// failure reason embedded in the status line as "Bad Request (<text>)"
// so a misconfigured peer can read why the upgrade was refused before
// the connection is closed.
func BuildHandshakeErrorResponse(userAgent, remoteAddr, text string) *http.Response {
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Status:     fmt.Sprintf("%d Bad Request (%s)", http.StatusBadRequest, text),
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       http.NoBody,
	}
	if userAgent != "" {
		resp.Header.Set(HeaderServer, userAgent)
	}
	if remoteAddr != "" {
		resp.Header.Set("Remote-Address", remoteAddr)
	}
	resp.Header.Set(HeaderConnection, "close")
	resp.ContentLength = 0
	return resp
}

// BuildRedirectResponse builds the slot-full rejection: 503 Service
// Unavailable carrying a JSON body of alternate peer addresses
// (`{"peer-ips": [...]}`) so a dialer we cannot admit can bootstrap
// elsewhere instead of being dropped with no signal. peerIPs are
// "host:port" strings; an empty list still serializes as `[]`.
func BuildRedirectResponse(userAgent, remoteAddr string, peerIPs []string) *http.Response {
	if peerIPs == nil {
		peerIPs = []string{}
	}
	body, _ := json.Marshal(struct {
		PeerIPs []string `json:"peer-ips"`
	}{PeerIPs: peerIPs})

	resp := &http.Response{
		StatusCode:    http.StatusServiceUnavailable,
		Status:        "503 Service Unavailable",
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Header:        make(http.Header),
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
	if userAgent != "" {
		resp.Header.Set(HeaderServer, userAgent)
	}
	if remoteAddr != "" {
		resp.Header.Set("Remote-Address", remoteAddr)
	}
	resp.Header.Set("Content-Type", "application/json")
	resp.Header.Set(HeaderConnection, "close")
	return resp
}

func addHandshakeHeaders(h http.Header, id *Identity, sharedValue []byte, cfg HandshakeConfig) {
	if cfg.NetworkID > 0 {
		h.Set(HeaderNetworkID, strconv.FormatUint(uint64(cfg.NetworkID), 10))
	}

	networkTime := uint64(time.Now().Unix() - protocol.RippleEpochUnix)
	h.Set(HeaderNetworkTime, strconv.FormatUint(networkTime, 10))
	h.Set(HeaderPublicKey, id.EncodedPublicKey())

	sig, err := id.SignDigest(sharedValue)
	if err != nil {
		// Omitting Session-Signature makes the remote reject the
		// handshake with no local signal — surface the cause instead of
		// silently shipping an unsigned handshake.
		slog.Warn("handshake: session-signature signing failed; omitting header",
			"t", "Handshake", "err", err)
	} else {
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
			// Uppercase hex, matching the format rippled emits.
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

// parseLedgerHashHeader accepts hex or 32-byte base64 — both forms
// appear on the wire (rippled accepts both too).
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

// VerifyPeerHandshake runs the post-Server-Domain verify chain:
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

		peerTime := time.Unix(netTime+protocol.RippleEpochUnix, 0)
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
	if rootcrypto.ECDSACanonicality(signature) == rootcrypto.CanonicityNone {
		return fmt.Errorf("%w: malformed DER signature", ErrInvalidSignature)
	}
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

// SupportedProtocolVersions returns the comma-joined Upgrade header
// value go-xrpl advertises.
func SupportedProtocolVersions() string {
	parts := make([]string, len(supportedProtocols))
	for i, v := range supportedProtocols {
		parts[i] = v.String()
	}
	return strings.Join(parts, ", ")
}

// protocolTokenRe matches a single XRPL/X.Y token: anchored, major ≥ 2,
// no leading zeros.
var protocolTokenRe = regexp.MustCompile(`^XRPL/([2-9]|[1-9][0-9]+)\.(0|[1-9][0-9]*)$`)

// parseProtocolVersions returns the sorted, deduplicated list of valid
// XRPL versions in a comma-separated header value.
func parseProtocolVersions(s string) []protocolVersion {
	var out []protocolVersion
	for tok := range strings.SplitSeq(s, ",") {
		tok = strings.TrimSpace(tok)
		m := protocolTokenRe.FindStringSubmatch(tok)
		if m == nil {
			continue
		}
		maj, errMaj := strconv.ParseUint(m[1], 10, 16)
		min, errMin := strconv.ParseUint(m[2], 10, 16)
		if errMaj != nil || errMin != nil {
			continue
		}
		v := protocolVersion{uint16(maj), uint16(min)}
		// Round-trip sanity: reject tokens that don't reserialise
		// identically.
		if v.String() != tok {
			continue
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].less(out[j]) })
	n := 0
	for i := 0; i < len(out); i++ {
		if i == 0 || out[i] != out[i-1] {
			out[n] = out[i]
			n++
		}
	}
	return out[:n]
}

func isProtocolSupported(v protocolVersion) bool {
	return slices.Contains(supportedProtocols, v)
}

// NegotiateProtocolVersion picks the largest version in the
// intersection of the peer's offered Upgrade list and supportedProtocols,
// or "" if no shared version exists. Use on the INBOUND path where the
// request advertises a list.
func NegotiateProtocolVersion(upgradeHeader string) string {
	theirs := parseProtocolVersions(upgradeHeader)
	var (
		best  protocolVersion
		found bool
	)
	i, j := 0, 0
	for i < len(theirs) && j < len(supportedProtocols) {
		switch {
		case theirs[i].less(supportedProtocols[j]):
			i++
		case supportedProtocols[j].less(theirs[i]):
			j++
		default:
			best = theirs[i]
			found = true
			i++
			j++
		}
	}
	if !found {
		return ""
	}
	return best.String()
}

// VerifyOutboundProtocolVersion accepts the server's Upgrade response
// only if it contains exactly one supported version, returning that
// version's token. Returns "" otherwise (zero, multiple, or
// unsupported).
func VerifyOutboundProtocolVersion(upgradeHeader string) string {
	pvs := parseProtocolVersions(upgradeHeader)
	if len(pvs) == 1 && isProtocolSupported(pvs[0]) {
		return pvs[0].String()
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
	for f := range strings.SplitSeq(headerValue, FeatureDelimiter) {
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
	for v := range strings.SplitSeq(featureValue, ValueDelimiter) {
		if strings.EqualFold(strings.TrimSpace(v), value) {
			return true
		}
	}
	return false
}

func FeatureEnabled(headers http.Header, feature string) bool {
	return IsFeatureValue(headers, feature, "1")
}

// HandshakeExtras carries the typed headers ParseHandshakeExtras
// surfaces. Instance-Cookie / Local-IP / Remote-IP round-trip on the
// wire but are validated-and-discarded.
type HandshakeExtras struct {
	ServerDomain      string
	NetworkID         string
	ClosedLedger      [32]byte
	PreviousLedger    [32]byte
	HasClosedLedger   bool
	HasPreviousLedger bool
	// Raw version headers; applyHandshakeExtras picks one by direction.
	UserAgentHeader string
	ServerHeader    string
}

// ValidateServerDomain enforces the Server-Domain handshake check.
// Runs first in the verify order
// (Server-Domain → Network-ID → Network-Time → Public-Key → ...).
func ValidateServerDomain(headers http.Header) (string, error) {
	v := headers.Get(HeaderServerDomain)
	if v == "" {
		return "", nil
	}
	if !stringutil.IsProperlyFormedTomlDomain(v) {
		return "", fmt.Errorf("%w: invalid Server-Domain %q",
			ErrInvalidHandshake, v)
	}
	return v, nil
}

// ParseHandshakeExtras enforces the post-signature checks: malformed
// ledger hashes, Previous-without-Closed, and Local-IP / Remote-IP
// consistency. Server-Domain is validated separately by
// ValidateServerDomain (which must run first). Instance-Cookie is
// emitted on the wire but never parsed. peerRemote == nil disables the
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

	// Network-ID is surfaced as the raw header string (the peers RPC
	// emits it verbatim). Numeric validation + mismatch rejection
	// happens upstream in VerifyPeerHandshake; here we just round-trip
	// the original string.
	if v := headers.Get(HeaderNetworkID); v != "" {
		out.NetworkID = v
	}

	out.UserAgentHeader = headers.Get(HeaderUserAgent)
	out.ServerHeader = headers.Get(HeaderServer)

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

	// Local-IP / Remote-IP are validated and discarded.
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
