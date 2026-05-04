package config

import (
	"fmt"
	"net"
	"strings"
	"time"
)

// Default WebSocket connection limits and timeouts. These mirror the values
// hardcoded in internal/rpc/websocket.go before WebSocketConfig was introduced
// and preserve historical behavior when the [websocket] section is omitted.
const (
	DefaultWebSocketMaxReadSize  int64         = 512 * 1024
	DefaultWebSocketReadTimeout  time.Duration = 90 * time.Second
	DefaultWebSocketWriteTimeout time.Duration = 10 * time.Second
	DefaultWebSocketPingInterval time.Duration = 30 * time.Second
	DefaultWebSocketPongTimeout  time.Duration = 90 * time.Second
)

// minWebSocketDuration is the lower bound enforced by Validate on every
// duration field. It guards against unit typos (e.g., "30ns" instead of
// "30s") that would otherwise pin the read/write/ping loops at fractions
// of a millisecond and burn CPU.
const minWebSocketDuration = 100 * time.Millisecond

// minWebSocketReadSize is the lower bound enforced by Validate on
// MaxReadSize. It guards against trivially-broken values (e.g., a
// single-digit byte cap) that would reject every realistic XRPL command
// frame and render the server unreachable.
const minWebSocketReadSize int64 = 1024

// WebSocketConfig holds tunable limits and timeouts applied to every
// WebSocket connection. Zero-valued fields fall back to the matching
// Default* constants via WithDefaults, so existing deployments that omit
// the [websocket] section behave identically to before this struct existed.
type WebSocketConfig struct {
	MaxReadSize  int64         `toml:"max_read_size" mapstructure:"max_read_size"`
	ReadTimeout  time.Duration `toml:"read_timeout" mapstructure:"read_timeout"`
	WriteTimeout time.Duration `toml:"write_timeout" mapstructure:"write_timeout"`
	PingInterval time.Duration `toml:"ping_interval" mapstructure:"ping_interval"`
	PongTimeout  time.Duration `toml:"pong_timeout" mapstructure:"pong_timeout"`
}

// WithDefaults returns a copy of cfg with any zero-valued field replaced by
// the matching Default* constant. Callers should use the returned value
// instead of the original to ensure every limit is set.
func (cfg WebSocketConfig) WithDefaults() WebSocketConfig {
	if cfg.MaxReadSize <= 0 {
		cfg.MaxReadSize = DefaultWebSocketMaxReadSize
	}
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = DefaultWebSocketReadTimeout
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWebSocketWriteTimeout
	}
	if cfg.PingInterval <= 0 {
		cfg.PingInterval = DefaultWebSocketPingInterval
	}
	if cfg.PongTimeout <= 0 {
		cfg.PongTimeout = DefaultWebSocketPongTimeout
	}
	return cfg
}

// Validate rejects values that cannot reasonably drive the WebSocket
// server. Sizes and durations must either be zero (use the default) or
// at least their respective minimum. The zero/positive split keeps
// "[websocket] omitted" valid while catching unit typos and
// trivially-broken values that would silently break the server at runtime.
func (cfg WebSocketConfig) Validate() error {
	if cfg.MaxReadSize < 0 {
		return fmt.Errorf("websocket.max_read_size must be non-negative, got %d", cfg.MaxReadSize)
	}
	if cfg.MaxReadSize > 0 && cfg.MaxReadSize < minWebSocketReadSize {
		return fmt.Errorf("websocket.max_read_size must be >= %d bytes (likely unit typo), got %d", minWebSocketReadSize, cfg.MaxReadSize)
	}
	if err := validateWebSocketDuration("read_timeout", cfg.ReadTimeout); err != nil {
		return err
	}
	if err := validateWebSocketDuration("write_timeout", cfg.WriteTimeout); err != nil {
		return err
	}
	if err := validateWebSocketDuration("ping_interval", cfg.PingInterval); err != nil {
		return err
	}
	if err := validateWebSocketDuration("pong_timeout", cfg.PongTimeout); err != nil {
		return err
	}
	return nil
}

func validateWebSocketDuration(field string, d time.Duration) error {
	if d == 0 {
		return nil
	}
	if d < 0 {
		return fmt.Errorf("websocket.%s must be non-negative, got %s", field, d)
	}
	if d < minWebSocketDuration {
		return fmt.Errorf("websocket.%s must be >= %s (likely unit typo), got %s", field, minWebSocketDuration, d)
	}
	return nil
}

// ServerConfig represents the [server] section
// This defines the ports that the server will listen on and default values
type ServerConfig struct {
	Ports    []string `toml:"ports" mapstructure:"ports"`       // List of port names to enable
	Port     int      `toml:"port" mapstructure:"port"`         // Default port number
	IP       string   `toml:"ip" mapstructure:"ip"`             // Default IP address
	Protocol string   `toml:"protocol" mapstructure:"protocol"` // Default protocol
	Limit    int      `toml:"limit" mapstructure:"limit"`       // Default connection limit
	User     string   `toml:"user" mapstructure:"user"`         // Default HTTP basic auth user
	Password string   `toml:"password" mapstructure:"password"` // Default HTTP basic auth password
}

// PortConfig represents individual port configurations like [port_rpc_admin_local]
// Each port section in the config becomes one of these structs
type PortConfig struct {
	// Basic port settings
	Port     int    `toml:"port" mapstructure:"port"`
	IP       string `toml:"ip" mapstructure:"ip"`
	Protocol string `toml:"protocol" mapstructure:"protocol"`
	Limit    int    `toml:"limit" mapstructure:"limit"`

	// HTTP Basic Authentication
	User     string `toml:"user" mapstructure:"user"`
	Password string `toml:"password" mapstructure:"password"`

	// Administrative access control
	Admin         []string `toml:"admin" mapstructure:"admin"`
	AdminUser     string   `toml:"admin_user" mapstructure:"admin_user"`
	AdminPassword string   `toml:"admin_password" mapstructure:"admin_password"`

	// Secure gateway (for proxies)
	SecureGateway []string `toml:"secure_gateway" mapstructure:"secure_gateway"`

	// SSL/TLS configuration
	SSLKey     string `toml:"ssl_key" mapstructure:"ssl_key"`
	SSLCert    string `toml:"ssl_cert" mapstructure:"ssl_cert"`
	SSLChain   string `toml:"ssl_chain" mapstructure:"ssl_chain"`
	SSLCiphers string `toml:"ssl_ciphers" mapstructure:"ssl_ciphers"`

	// WebSocket specific settings
	SendQueueLimit int `toml:"send_queue_limit" mapstructure:"send_queue_limit"`

	// WebSocket permessage-deflate extension options
	PermessageDeflate       bool `toml:"permessage_deflate" mapstructure:"permessage_deflate"`
	ClientMaxWindowBits     int  `toml:"client_max_window_bits" mapstructure:"client_max_window_bits"`
	ServerMaxWindowBits     int  `toml:"server_max_window_bits" mapstructure:"server_max_window_bits"`
	ClientNoContextTakeover bool `toml:"client_no_context_takeover" mapstructure:"client_no_context_takeover"`
	ServerNoContextTakeover bool `toml:"server_no_context_takeover" mapstructure:"server_no_context_takeover"`
	CompressLevel           int  `toml:"compress_level" mapstructure:"compress_level"`
	MemoryLevel             int  `toml:"memory_level" mapstructure:"memory_level"`
}

// IsSecure returns true if the port is configured for SSL/TLS
func (p *PortConfig) IsSecure() bool {
	return containsProtocol(p.Protocol, "https") || containsProtocol(p.Protocol, "wss")
}

// HasHTTP returns true if the port supports HTTP protocol
func (p *PortConfig) HasHTTP() bool {
	return containsProtocol(p.Protocol, "http")
}

// HasHTTPS returns true if the port supports HTTPS protocol
func (p *PortConfig) HasHTTPS() bool {
	return containsProtocol(p.Protocol, "https")
}

// HasWebSocket returns true if the port supports WebSocket protocol
func (p *PortConfig) HasWebSocket() bool {
	return containsProtocol(p.Protocol, "ws")
}

// HasSecureWebSocket returns true if the port supports secure WebSocket protocol
func (p *PortConfig) HasSecureWebSocket() bool {
	return containsProtocol(p.Protocol, "wss")
}

// HasPeer returns true if the port supports peer protocol
func (p *PortConfig) HasPeer() bool {
	return containsProtocol(p.Protocol, "peer")
}

// HasGRPC returns true if the port supports gRPC protocol
func (p *PortConfig) HasGRPC() bool {
	return containsProtocol(p.Protocol, "grpc")
}

// IsAdminPort returns true if the port has administrative access configured
func (p *PortConfig) IsAdminPort() bool {
	return len(p.Admin) > 0 || p.AdminUser != ""
}

// HasBasicAuth returns true if the port has HTTP basic authentication configured
func (p *PortConfig) HasBasicAuth() bool {
	return p.User != "" && p.Password != ""
}

// HasAdminAuth returns true if the port has admin authentication configured
func (p *PortConfig) HasAdminAuth() bool {
	return p.AdminUser != "" && p.AdminPassword != ""
}

// HasSecureGateway returns true if the port has secure gateway configured
func (p *PortConfig) HasSecureGateway() bool {
	return len(p.SecureGateway) > 0
}

// HasSSLConfig returns true if SSL certificate files are configured
func (p *PortConfig) HasSSLConfig() bool {
	return p.SSLKey != "" && (p.SSLCert != "" || p.SSLChain != "")
}

// GetBindAddress returns the full bind address (IP:Port)
func (p *PortConfig) GetBindAddress() string {
	if p.IP == "" {
		return ":0" // Invalid, but will be caught by validation
	}
	if p.Port == 0 {
		return p.IP + ":0"
	}
	return fmt.Sprintf("%s:%d", p.IP, p.Port)
}

// Validate performs validation on the port configuration
func (p *PortConfig) Validate() error {
	if p.Port == 0 {
		return fmt.Errorf("port number is required")
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port number must be between 1 and 65535, got %d", p.Port)
	}
	if p.IP == "" {
		return fmt.Errorf("IP address is required")
	}
	if p.Protocol == "" {
		return fmt.Errorf("protocol is required")
	}

	// Validate protocol combinations
	if err := p.validateProtocols(); err != nil {
		return err
	}

	// Validate SSL configuration
	if p.IsSecure() && !p.HasSSLConfig() {
		// This is allowed - rippled will generate self-signed certificates
		// No error, just a note for the user
	}

	// Validate compression settings
	if p.CompressLevel < 0 || p.CompressLevel > 9 {
		return fmt.Errorf("compress_level must be between 0 and 9, got %d", p.CompressLevel)
	}
	if p.MemoryLevel != 0 && (p.MemoryLevel < 1 || p.MemoryLevel > 9) {
		return fmt.Errorf("memory_level must be between 1 and 9, got %d", p.MemoryLevel)
	}

	// Validate window bits
	if p.ClientMaxWindowBits != 0 && (p.ClientMaxWindowBits < 9 || p.ClientMaxWindowBits > 15) {
		return fmt.Errorf("client_max_window_bits must be between 9 and 15, got %d", p.ClientMaxWindowBits)
	}
	if p.ServerMaxWindowBits != 0 && (p.ServerMaxWindowBits < 9 || p.ServerMaxWindowBits > 15) {
		return fmt.Errorf("server_max_window_bits must be between 9 and 15, got %d", p.ServerMaxWindowBits)
	}

	return nil
}

// validateProtocols validates that protocol combinations are valid
func (p *PortConfig) validateProtocols() error {
	protocols := parseProtocols(p.Protocol)

	hasWebSocket := false
	hasNonWebSocket := false
	peerCount := 0

	for _, protocol := range protocols {
		switch protocol {
		case "ws", "wss":
			hasWebSocket = true
		case "http", "https":
			hasNonWebSocket = true
		case "peer":
			peerCount++
		case "grpc":
			hasNonWebSocket = true
		default:
			return fmt.Errorf("unknown protocol: %s", protocol)
		}
	}

	if hasWebSocket && hasNonWebSocket {
		return fmt.Errorf("websocket and non-websocket protocols cannot be combined on the same port")
	}

	if peerCount > 1 {
		return fmt.Errorf("only one peer protocol can be specified per port")
	}

	return nil
}

// ParseAdminNets parses the Admin field entries into net.IPNet values.
// Bare IPs (without CIDR suffix) get /32 for IPv4 or /128 for IPv6.
// This matches rippled's parse_Port() in Port.cpp.
func (p *PortConfig) ParseAdminNets() ([]net.IPNet, error) {
	var nets []net.IPNet
	for _, entry := range p.Admin {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// Append CIDR suffix if not present
		if !strings.Contains(entry, "/") {
			ip := net.ParseIP(entry)
			if ip == nil {
				return nil, fmt.Errorf("invalid admin IP: %s", entry)
			}
			if ip.To4() != nil {
				entry += "/32"
			} else {
				entry += "/128"
			}
		}
		_, ipNet, err := net.ParseCIDR(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid admin CIDR %q: %w", entry, err)
		}
		nets = append(nets, *ipNet)
	}
	return nets, nil
}

// IPInNets returns true if ip is contained in any of the given networks.
// Handles IPv4-mapped IPv6 addresses by normalizing to IPv4 first.
// This matches rippled's ipAllowed() in Role.cpp.
func IPInNets(ip net.IP, nets []net.IPNet) bool {
	// Normalize IPv4-mapped IPv6 (e.g. ::ffff:127.0.0.1) to plain IPv4
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// parseProtocols parses a comma-separated protocol string
func parseProtocols(protocolStr string) []string {
	if protocolStr == "" {
		return nil
	}

	protocols := make([]string, 0)
	current := ""

	for _, char := range protocolStr {
		if char == ',' || char == ' ' {
			if current != "" {
				protocols = append(protocols, strings.TrimSpace(current))
				current = ""
			}
		} else {
			current += string(char)
		}
	}

	if current != "" {
		protocols = append(protocols, strings.TrimSpace(current))
	}

	return protocols
}
