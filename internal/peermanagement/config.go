package peermanagement

import (
	"errors"
	"net"
	"time"

	"github.com/LeJamon/goXRPLd/internal/peermanagement/cluster"
)

// Default configuration values.
const (
	DefaultListenAddr  = ":51235"
	DefaultMaxPeers    = 50
	DefaultMaxInbound  = 25
	DefaultMaxOutbound = 25

	DefaultConnectTimeout   = 10 * time.Second
	DefaultHandshakeTimeout = 5 * time.Second
	DefaultPingInterval     = 30 * time.Second
	DefaultIdleTimeout      = 2 * time.Minute

	DefaultEventBufferSize   = 256
	DefaultMessageBufferSize = 256
	DefaultSendBufferSize    = 64

	DefaultUserAgent = "goXRPL/0.1.0"
)

// Config holds the configuration for the overlay network.
type Config struct {
	// Network settings
	ListenAddr string
	NetworkID  uint32
	UserAgent  string

	// Peer limits
	MaxPeers    int
	MaxInbound  int
	MaxOutbound int

	// Bootstrap peers
	BootstrapPeers []string
	FixedPeers     []string

	// Privacy
	PrivateMode bool // Don't share our address with peers

	// Storage
	DataDir string // For boot cache persistence

	// Timeouts
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration
	PingInterval     time.Duration
	IdleTimeout      time.Duration

	// Buffer sizes
	EventBufferSize   int
	MessageBufferSize int
	SendBufferSize    int

	// Features — advertised via X-Protocol-Ctl during handshake so
	// peers know which optional protocol extensions we speak. Matches
	// rippled's compr / vprr / txrr / ledgerreplay feature toggles.
	//
	// All three reduce-relay flags default to false to match rippled
	// (Config.h:248 sets VP_REDUCE_RELAY_BASE_SQUELCH_ENABLE=false,
	// Config.h:258 sets TX_REDUCE_RELAY_ENABLE=false, Config.cpp:755-762
	// preserves false when the cfg omits the section). Reduce-relay
	// is opt-in: an operator must explicitly set one of these flags
	// (or WithReduceRelay(true)) to advertise vprr/txrr and activate
	// the slot-squelching engine. Shipping with the flags on would
	// cause a stock goXRPL node to squelch traffic on a stock rippled
	// network where the peer majority does not reciprocate.
	//
	// EnableReduceRelay is a legacy alias that enables BOTH vprr and
	// txrr at once. New code should set EnableVPReduceRelay and
	// EnableTxReduceRelay independently so an operator can run one
	// without the other — matches rippled's Handshake.cpp handling of
	// the two features as independent toggles. When EnableReduceRelay
	// is set, it is propagated to both at Validate() time if either
	// specific toggle is still false.
	EnableReduceRelay   bool
	EnableVPReduceRelay bool
	EnableTxReduceRelay bool
	EnableCompression   bool
	EnableLedgerReplay  bool

	// LocalValidatorPubKey is the compressed secp256k1 public key (33
	// bytes) of the local validator identity, when this node is acting
	// as a validator. Nil/empty for observer nodes. Used by
	// handleSquelchMessage to drop inbound TMSquelch frames that target
	// our own validator — otherwise a hostile peer could silence our
	// own proposals/validations on the RelayFromValidator path.
	// Matches rippled PeerImp.cpp:2715-2721.
	LocalValidatorPubKey []byte

	// ClusterNodes lists base58-encoded node public keys (with an
	// optional trailing comment as the human-readable name) for peers
	// that should be treated as cluster members. Mirrors the
	// [cluster_nodes] section in rippled.cfg. Parsed by
	// cluster.Registry.Load at construction time; a malformed entry
	// fails Overlay startup, matching rippled Application init which
	// aborts when Cluster::load returns false.
	ClusterNodes []string

	// ServerDomain populates the Server-Domain header; "" suppresses it.
	ServerDomain string
	// PublicIP populates Local-IP and gates the Remote-IP consistency
	// check; nil suppresses both.
	PublicIP net.IP

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

		ConnectTimeout:   DefaultConnectTimeout,
		HandshakeTimeout: DefaultHandshakeTimeout,
		PingInterval:     DefaultPingInterval,
		IdleTimeout:      DefaultIdleTimeout,

		EventBufferSize:   DefaultEventBufferSize,
		MessageBufferSize: DefaultMessageBufferSize,
		SendBufferSize:    DefaultSendBufferSize,

		// Reduce-relay is opt-in; rippled defaults all three to false
		// (Config.h:248, Config.h:258, Config.cpp:755-762). Leaving
		// these zero-valued avoids advertising vprr/txrr on a stock
		// rippled network where peers don't reciprocate.
		EnableReduceRelay:   false,
		EnableVPReduceRelay: false,
		EnableTxReduceRelay: false,
		EnableCompression:   true,
		EnableLedgerReplay:  true,

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

// WithBootstrapPeers sets the initial peers to connect to.
func WithBootstrapPeers(peers ...string) Option {
	return func(c *Config) {
		c.BootstrapPeers = peers
	}
}

// WithFixedPeers sets peers that should always be connected.
func WithFixedPeers(peers ...string) Option {
	return func(c *Config) {
		c.FixedPeers = peers
	}
}

// WithPrivateMode enables private mode (don't share our address).
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

// WithPingInterval sets the ping interval for keepalive.
func WithPingInterval(d time.Duration) Option {
	return func(c *Config) {
		c.PingInterval = d
	}
}

// WithIdleTimeout sets the idle timeout before disconnecting a peer.
func WithIdleTimeout(d time.Duration) Option {
	return func(c *Config) {
		c.IdleTimeout = d
	}
}

// WithReduceRelay enables or disables reduce-relay optimization.
// Reduce-relay is opt-in and defaults to false to match rippled
// (Config.h:248, Config.cpp:755-762). Setting this to true activates
// both vprr and txrr via the Validate() cascade; callers who need
// one without the other should set EnableVPReduceRelay or
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
// should omit this option (the filter becomes a no-op). Matches
// rippled PeerImp.cpp:2715-2721 which ignores TMSquelch addressed to
// app_.getValidationPublicKey().
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
// construction; a malformed value fails startup. Mirrors rippled's
// behavior in Application init where Cluster::load failure aborts the
// node.
func WithClusterNodes(entries ...string) Option {
	return func(c *Config) {
		c.ClusterNodes = append([]string(nil), entries...)
	}
}

// Cluster type alias re-exports the registry pointer so that
// downstream callers (e.g. RPC wiring) don't need to import the
// cluster sub-package directly when they already depend on
// peermanagement.
type Cluster = cluster.Registry

// WithServerDomain sets the operator domain emitted in the
// `Server-Domain` handshake header. An empty value suppresses the
// header (matching rippled's behavior when no domain is configured).
func WithServerDomain(domain string) Option {
	return func(c *Config) {
		c.ServerDomain = domain
	}
}

// WithPublicIP sets the node's observed public address. Used to emit
// the `Local-IP` handshake header and to validate the peer's
// `Remote-IP` self-report. A nil or unspecified IP suppresses both.
func WithPublicIP(ip net.IP) Option {
	return func(c *Config) {
		c.PublicIP = ip
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
	if c.MaxInbound+c.MaxOutbound > c.MaxPeers {
		return errors.New("MaxInbound + MaxOutbound cannot exceed MaxPeers")
	}
	if c.ConnectTimeout <= 0 {
		return errors.New("ConnectTimeout must be positive")
	}
	if c.HandshakeTimeout <= 0 {
		return errors.New("HandshakeTimeout must be positive")
	}
	if c.Clock == nil {
		return errors.New("Clock function cannot be nil")
	}
	// Legacy EnableReduceRelay propagates to both specific flags when
	// the caller hasn't set them independently. Matches rippled's
	// behavior where enabling "reduce-relay" as a whole turns on both
	// vprr and txrr. The default is false (see DefaultConfig), so this
	// cascade only fires when an operator explicitly opts into
	// reduce-relay via the legacy omnibus flag (either in the config
	// file or via WithReduceRelay(true)). It remains load-bearing for
	// that opt-in path.
	if c.EnableReduceRelay {
		c.EnableVPReduceRelay = true
		c.EnableTxReduceRelay = true
	}
	return nil
}
