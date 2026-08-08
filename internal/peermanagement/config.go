package peermanagement

import (
	"errors"
	"fmt"
	"math"
	"net"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Default configuration values.
const (
	DefaultListenAddr  = ":51235"
	DefaultMaxPeers    = 50
	DefaultMaxInbound  = 25
	DefaultMaxOutbound = 25

	DefaultConnectTimeout   = 10 * time.Second
	DefaultHandshakeTimeout = 5 * time.Second

	DefaultEventBufferSize             = 256
	DefaultMessageBufferSize           = 256
	DefaultSendBufferSize              = 64
	DefaultInboundRetainedBytes  int64 = 3 * message.MaxMessageSize
	DefaultOutboundRetainedBytes int64 = 8 * message.MaxMessageSize
	acquisitionEventBufferSize         = 16

	reliableSendBufferSize = 512

	controlSendMinimum     = 8
	consensusSendMinimum   = 256
	acquisitionSendMinimum = 64
	ordinarySendMinimum    = DefaultSendBufferSize

	controlSendMaximum     = 16
	consensusSendMaximum   = 320
	acquisitionSendMaximum = 192
	ordinarySendMaximum    = 128

	bulkSequenceBufferSize         = 4
	reliableFramesPerBulkFrame     = 16
	outboundCriticalByteReserve    = 2 * 1024 * 1024
	outboundNonCriticalByteMaximum = message.MaxMessageSize + message.HeaderSizeCompressed
	outboundRetainedByteMaximum    = outboundNonCriticalByteMaximum + outboundCriticalByteReserve
	maxOutboundReservedPeers       = (math.MaxInt64 - int64(outboundNonCriticalByteMaximum)) / int64(outboundCriticalByteReserve)

	// DefaultLedgerDataBufferSize sizes the dedicated acquisition-reply
	// lane (mtLEDGER_DATA and the replay-delta / proof-path responses).
	// Its bounded backpressure keeps requested replies from being discarded
	// behind unrelated gossip while limiting retained wire data.
	DefaultLedgerDataBufferSize = 64

	// DefaultMaxTransactions is the per-type in-flight ceiling
	// consulted at TMTransaction ingress before dispatching to the
	// router. Matches rippled's [max_transactions] default.
	DefaultMaxTransactions = 250
	peerIPLimitStep        = 21

	// DefaultTxReduceRelayMinPeers / DefaultTxRelayPercentage govern
	// the reduce-relay tx peer-selection in Overlay.RelayTransaction.
	// Both match rippled's defaults.
	DefaultTxReduceRelayMinPeers = 20
	DefaultTxRelayPercentage     = 25

	DefaultUserAgent = "goXRPL/0.1.0"
)

func manifestMessageBufferSize(maxPeers int) int {
	if maxPeers < 1 {
		return 1
	}
	return maxPeers
}

// MinimumOutboundRetainedBytes returns the shared ceiling required to preserve
// one critical reservation per configured peer.
func MinimumOutboundRetainedBytes(maxPeers int) int64 {
	if maxPeers <= 0 {
		return int64(outboundNonCriticalByteMaximum)
	}
	if int64(maxPeers) > maxOutboundReservedPeers {
		return math.MaxInt64
	}
	return int64(outboundNonCriticalByteMaximum) +
		int64(maxPeers)*int64(outboundCriticalByteReserve)
}

// Config holds the configuration for the overlay network.
type Config struct {
	// Network settings
	ListenAddr string
	NetworkID  uint32
	UserAgent  string
	SSLCiphers string

	// Peer limits
	MaxPeers    int
	MaxInbound  int
	MaxOutbound int
	IPLimit     int

	// Bootstrap peers
	BootstrapPeers []string
	FixedPeers     []string

	// Privacy
	PrivateMode bool // Only auto-connect to fixed peers and don't share our address

	// Storage
	DataDir string // For boot cache persistence

	// Timeouts
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration

	// Buffer sizes. EventBufferSize and MessageBufferSize size the
	// overlay's internal channels (see New).
	EventBufferSize   int
	MessageBufferSize int
	// OutboundRetainedBytes bounds queued and actively written frame bytes
	// across all peers. Noncritical traffic cannot consume the critical
	// reserve derived from MaxPeers.
	OutboundRetainedBytes int64
	// InboundRetainedBytes bounds bulk wire, decoded, and spooled payload bytes
	// across all peers until downstream processing completes.
	InboundRetainedBytes int64

	// MaxTransactions sizes the overlay's dedicated inbound
	// TMTransaction lane. Inbound tx frames past this ceiling are shed
	// (bumping droppedTransactions); the separate lane keeps a tx flood
	// from crowding consensus/acquisition traffic. The analog of
	// [max_transactions] in rippled.cfg. Non-positive falls back to
	// DefaultMaxTransactions — the lane is always bounded.
	MaxTransactions int

	// Features — advertised via X-Protocol-Ctl during handshake so
	// peers know which optional protocol extensions we speak (the
	// compr / vprr / txrr / ledgerreplay feature toggles).
	//
	// All three reduce-relay flags default to false, matching rippled.
	// Reduce-relay is opt-in: an operator must explicitly set one of
	// these flags (or WithReduceRelay(true)) to advertise vprr/txrr
	// and activate the slot-squelching engine. Shipping with the flags
	// on would cause a stock go-xrpl node to squelch traffic on a
	// stock rippled network where the peer majority does not
	// reciprocate.
	//
	// EnableReduceRelay is a legacy alias that enables BOTH vprr and
	// txrr at once. New code should set EnableVPReduceRelay and
	// EnableTxReduceRelay independently so an operator can run one
	// without the other — the two features are independent on the
	// wire. When EnableReduceRelay is set, it is propagated to both at
	// Validate() time if either specific toggle is still false.
	EnableReduceRelay   bool
	EnableVPReduceRelay bool
	EnableTxReduceRelay bool
	EnableCompression   bool
	EnableLedgerReplay  bool

	// EnableTxReduceRelayMetrics accumulates the tx_reduce_relay
	// rolling-average metrics even for peers that did not negotiate
	// tx-reduce-relay. Off by default — metrics then accrue only for
	// negotiated peers.
	EnableTxReduceRelayMetrics bool

	// TxReduceRelayMinPeers is the minimum number of enabled peers the
	// reduce-relay selection always relays a transaction to, and the floor
	// below which (plus disabled peers) relay falls back to all peers.
	// Default 20, min 10.
	TxReduceRelayMinPeers int

	// TxRelayPercentage is the percentage of the active enabled peers above
	// the minimum that a transaction is relayed to in full; the remainder
	// learn of it via the TMHaveTransactions announce. Default 25,
	// range 10-100.
	TxRelayPercentage int

	// LocalValidatorPubKey is the compressed secp256k1 public key (33
	// bytes) of the local validator identity, when this node is acting
	// as a validator. Nil/empty for observer nodes. Used by
	// handleSquelchMessage to drop inbound TMSquelch frames that target
	// our own validator — otherwise a hostile peer could silence our
	// own proposals/validations on the RelayFromValidator path.
	LocalValidatorPubKey []byte

	// ClusterNodes lists base58-encoded node public keys (with an
	// optional trailing comment as the human-readable name) for peers
	// that should be treated as cluster members — the analog of the
	// [cluster_nodes] section in rippled.cfg. Parsed by
	// cluster.Registry.Load at construction time; a malformed entry
	// fails Overlay startup.
	ClusterNodes []string

	// ServerDomain populates the Server-Domain header; "" suppresses it.
	ServerDomain string
	// PublicIP populates Local-IP and gates the Remote-IP consistency
	// check; nil suppresses both.
	PublicIP net.IP

	// VerifyEndpoints validates addresses advertised in inbound
	// TMEndpoints gossip: when true (the default), non-public / loopback
	// / port-0 addresses are silently dropped before reaching Discovery,
	// mirroring rippled PeerFinder is_valid_address. Setting it false
	// (the [overlay] verify_endpoints = 0 knob) accepts them, for local
	// dev networks — a security risk on a public network.
	VerifyEndpoints bool

	// Clock function for testing
	Clock func() time.Time
}

// DefaultConfig returns the default configuration.
func DefaultConfig() Config {
	return Config{
		ListenAddr:  DefaultListenAddr,
		UserAgent:   DefaultUserAgent,
		MaxPeers:    DefaultMaxPeers,
		MaxInbound:  DefaultMaxInbound,
		MaxOutbound: DefaultMaxOutbound,
		IPLimit:     0,

		ConnectTimeout:   DefaultConnectTimeout,
		HandshakeTimeout: DefaultHandshakeTimeout,

		EventBufferSize:       DefaultEventBufferSize,
		MessageBufferSize:     DefaultMessageBufferSize,
		MaxTransactions:       DefaultMaxTransactions,
		InboundRetainedBytes:  DefaultInboundRetainedBytes,
		OutboundRetainedBytes: DefaultOutboundRetainedBytes,

		// Reduce-relay is opt-in. Leaving these zero-valued avoids
		// advertising vprr/txrr on a stock rippled network where peers
		// don't reciprocate.
		EnableReduceRelay:   false,
		EnableVPReduceRelay: false,
		EnableTxReduceRelay: false,
		EnableCompression:   true,
		EnableLedgerReplay:  true,

		TxReduceRelayMinPeers: DefaultTxReduceRelayMinPeers,
		TxRelayPercentage:     DefaultTxRelayPercentage,

		// Endpoint verification is on by default; only a local dev
		// network (verify_endpoints = 0) turns it off.
		VerifyEndpoints: true,

		Clock: time.Now,
	}
}

// Option is a functional option for configuring the overlay.
type Option func(*Config)

// WithListenAddr sets the listen address for incoming connections.
func WithListenAddr(addr string) Option {
	return func(c *Config) {
		c.ListenAddr = addr
	}
}

func WithSSLCiphers(cipherList string) Option {
	return func(c *Config) {
		c.SSLCiphers = cipherList
	}
}

// WithNetworkID sets the network ID for peer validation.
func WithNetworkID(id uint32) Option {
	return func(c *Config) {
		c.NetworkID = id
	}
}

// WithMaxPeers sets the maximum total number of peers.
func WithMaxPeers(n int) Option {
	return func(c *Config) {
		c.MaxPeers = n
	}
}

// WithMaxInbound sets the maximum number of inbound connections.
func WithMaxInbound(n int) Option {
	return func(c *Config) {
		c.MaxInbound = n
	}
}

// WithMaxOutbound sets the maximum number of outbound connections.
func WithMaxOutbound(n int) Option {
	return func(c *Config) {
		c.MaxOutbound = n
	}
}

func WithIPLimit(n int) Option {
	return func(c *Config) {
		c.IPLimit = n
	}
}

func WithOutboundRetainedBytes(bytes int64) Option {
	return func(c *Config) {
		c.OutboundRetainedBytes = bytes
	}
}

// WithInboundRetainedBytes sets the shared inbound bulk-payload budget.
func WithInboundRetainedBytes(bytes int64) Option {
	return func(c *Config) {
		c.InboundRetainedBytes = bytes
	}
}

// WithBootstrapPeers sets the initial peers to connect to.
func WithBootstrapPeers(peers ...string) Option {
	peers = append([]string(nil), peers...)
	return func(c *Config) {
		c.BootstrapPeers = append([]string(nil), peers...)
	}
}

// WithFixedPeers sets peers that should always be connected.
func WithFixedPeers(peers ...string) Option {
	peers = append([]string(nil), peers...)
	return func(c *Config) {
		c.FixedPeers = append([]string(nil), peers...)
	}
}

// WithPrivateMode limits automatic connections to fixed peers and suppresses
// sharing our address.
func WithPrivateMode(enabled bool) Option {
	return func(c *Config) {
		c.PrivateMode = enabled
	}
}

// WithDataDir sets the data directory for persistent storage.
func WithDataDir(path string) Option {
	return func(c *Config) {
		c.DataDir = path
	}
}

// WithConnectTimeout sets the connection timeout.
func WithConnectTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.ConnectTimeout = d
	}
}

// WithHandshakeTimeout sets the handshake timeout.
func WithHandshakeTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.HandshakeTimeout = d
	}
}

// WithReduceRelay enables or disables reduce-relay optimization.
// Reduce-relay is opt-in and defaults to false. Setting this to true
// activates both vprr and txrr via the Validate() cascade; callers who
// need one without the other should set EnableVPReduceRelay or
// EnableTxReduceRelay directly on the Config instead.
func WithReduceRelay(enabled bool) Option {
	return func(c *Config) {
		c.EnableReduceRelay = enabled
	}
}

// WithCompression enables or disables message compression.
func WithCompression(enabled bool) Option {
	return func(c *Config) {
		c.EnableCompression = enabled
	}
}

// WithLedgerReplay enables or disables the ledgerreplay X-Protocol-Ctl
// feature. When disabled we won't advertise replay support, so peers
// won't offer us mtREPLAY_DELTA_RESPONSE and won't accept replay
// requests from us — the catchup path falls back to legacy GetLedger.
func WithLedgerReplay(enabled bool) Option {
	return func(c *Config) {
		c.EnableLedgerReplay = enabled
	}
}

// WithClock sets the clock function (for testing).
func WithClock(clock func() time.Time) Option {
	return func(c *Config) {
		c.Clock = clock
	}
}

// WithLocalValidatorPubKey sets the compressed secp256k1 public key
// (33 bytes) of the local validator identity, so inbound TMSquelch
// frames targeting our own validator can be dropped. Observer nodes
// should omit this option (the filter becomes a no-op).
func WithLocalValidatorPubKey(key []byte) Option {
	return func(c *Config) {
		if len(key) == 0 {
			c.LocalValidatorPubKey = nil
			return
		}
		// Defensive copy so callers cannot mutate config state after
		// construction.
		c.LocalValidatorPubKey = append([]byte(nil), key...)
	}
}

// WithClusterNodes sets the [cluster_nodes] entries (base58 node
// pubkey + optional trailing comment used as the human-readable
// name). Each entry is parsed by cluster.Registry.Load at Overlay
// construction; a malformed value fails startup.
func WithClusterNodes(entries ...string) Option {
	return func(c *Config) {
		c.ClusterNodes = append([]string(nil), entries...)
	}
}

// WithServerDomain sets the operator domain emitted in the
// `Server-Domain` handshake header. An empty value suppresses the
// header.
func WithServerDomain(domain string) Option {
	return func(c *Config) {
		c.ServerDomain = domain
	}
}

// WithPublicIP sets the node's observed public address. Used to emit
// the `Local-IP` handshake header and to validate the peer's
// `Remote-IP` self-report. A nil or unspecified IP suppresses both.
func WithPublicIP(ip net.IP) Option {
	ip = append(net.IP(nil), ip...)
	return func(c *Config) {
		c.PublicIP = append(net.IP(nil), ip...)
	}
}

// WithVerifyEndpoints toggles validation of addresses advertised in
// inbound TMEndpoints gossip (the [overlay] verify_endpoints knob).
// Defaults to true; passing false accepts non-public/loopback/port-0
// addresses for local dev networks.
func WithVerifyEndpoints(enabled bool) Option {
	return func(c *Config) {
		c.VerifyEndpoints = enabled
	}
}

// WithEventBufferSize sets the internal event channel buffer size.
func WithEventBufferSize(size int) Option {
	return func(c *Config) {
		c.EventBufferSize = size
	}
}

// WithMessageBufferSize sets the inbound message channel buffer size.
func WithMessageBufferSize(size int) Option {
	return func(c *Config) {
		c.MessageBufferSize = size
	}
}

// WithMaxTransactions sets the capacity of the overlay's dedicated
// inbound TMTransaction lane. Non-positive falls back to the default
// (250).
func WithMaxTransactions(n int) Option {
	return func(c *Config) {
		c.MaxTransactions = n
	}
}

// Validate checks the configuration for invalid values.
func (c *Config) Validate() error {
	if c.MaxPeers <= 0 {
		return errors.New("MaxPeers must be positive")
	}
	if c.MaxInbound < 0 {
		return errors.New("MaxInbound cannot be negative")
	}
	if c.MaxOutbound < 0 {
		return errors.New("MaxOutbound cannot be negative")
	}
	if c.IPLimit < 0 {
		return errors.New("IPLimit cannot be negative")
	}
	if c.MaxInbound+c.MaxOutbound > c.MaxPeers {
		return errors.New("MaxInbound + MaxOutbound cannot exceed MaxPeers")
	}
	if int64(c.MaxPeers) > maxOutboundReservedPeers {
		return errors.New("MaxPeers is too large for outbound critical reservations")
	}
	if c.OutboundRetainedBytes == 0 {
		c.OutboundRetainedBytes = DefaultOutboundRetainedBytes
	}
	if c.InboundRetainedBytes == 0 {
		c.InboundRetainedBytes = DefaultInboundRetainedBytes
	}
	if c.InboundRetainedBytes < 3*int64(message.MaxMessageSize) {
		return fmt.Errorf("InboundRetainedBytes must be at least %d", 3*int64(message.MaxMessageSize))
	}
	minimumOutboundBytes := MinimumOutboundRetainedBytes(c.MaxPeers)
	if c.OutboundRetainedBytes < minimumOutboundBytes {
		return fmt.Errorf("OutboundRetainedBytes must be at least %d for MaxPeers=%d",
			minimumOutboundBytes, c.MaxPeers)
	}
	limit := c.IPLimit
	if limit == 0 {
		limit = 2
		if c.MaxInbound > peerIPLimitStep {
			limit += min(5, c.MaxInbound/peerIPLimitStep)
		}
	}
	c.IPLimit = max(1, min(limit, c.MaxInbound/2))
	if c.ConnectTimeout <= 0 {
		return errors.New("ConnectTimeout must be positive")
	}
	if c.HandshakeTimeout <= 0 {
		return errors.New("HandshakeTimeout must be positive")
	}
	if c.Clock == nil {
		return errors.New("Clock function cannot be nil")
	}
	if c.ServerDomain != "" && !protocol.IsProperlyFormedTomlDomain(c.ServerDomain) {
		return errors.New("invalid ServerDomain: the domain name does not appear to meet the requirements")
	}
	// Legacy EnableReduceRelay propagates to both specific flags when
	// the caller hasn't set them independently — enabling
	// "reduce-relay" as a whole turns on both vprr and txrr. The
	// default is false (see DefaultConfig), so this
	// cascade only fires when an operator explicitly opts into
	// reduce-relay via the legacy omnibus flag (either in the config
	// file or via WithReduceRelay(true)). It remains load-bearing for
	// that opt-in path.
	if c.EnableReduceRelay {
		c.EnableVPReduceRelay = true
		c.EnableTxReduceRelay = true
	}

	// Reduce-relay tuning: a zero value means "unset" and takes the
	// default. Out-of-range values are rejected
	// (tx_relay_percentage 10-100, tx_min_peers >= 10).
	if c.TxReduceRelayMinPeers == 0 {
		c.TxReduceRelayMinPeers = DefaultTxReduceRelayMinPeers
	}
	if c.TxRelayPercentage == 0 {
		c.TxRelayPercentage = DefaultTxRelayPercentage
	}
	if c.TxRelayPercentage < 10 || c.TxRelayPercentage > 100 || c.TxReduceRelayMinPeers < 10 {
		return fmt.Errorf("invalid reduce-relay tuning: tx_relay_percentage=%d (must be 10-100), tx_min_peers=%d (must be >= 10)",
			c.TxRelayPercentage, c.TxReduceRelayMinPeers)
	}
	return nil
}
