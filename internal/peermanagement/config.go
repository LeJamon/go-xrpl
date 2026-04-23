package peermanagement

import (
	"errors"
	"time"
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

		EnableReduceRelay:  true,
		EnableCompression:  true,
		EnableLedgerReplay: true,

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
	// vprr and txrr.
	if c.EnableReduceRelay {
		c.EnableVPReduceRelay = true
		c.EnableTxReduceRelay = true
	}
	return nil
}
