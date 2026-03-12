package pathfinder

import (
	"sync"

	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
)

// LineDirection indicates whether to include all trust lines or only those
// where rippling is not disabled on the account's side.
// Reference: rippled RippleLineCache.h LineDirection
type LineDirection bool

const (
	// LineDirectionOutgoing includes all trust lines (for accounts that can send).
	LineDirectionOutgoing LineDirection = true
	// LineDirectionIncoming includes only trust lines where no-ripple is NOT set
	// on the account's side (for accounts that can receive via rippling).
	LineDirectionIncoming LineDirection = false
)

// PathFindTrustLine wraps a trust line from the perspective of a specific account.
// All fields are oriented relative to the viewing account.
// Reference: rippled PathFindTrustLine / TrustLineBase
type PathFindTrustLine struct {
	// Balance from this account's perspective.
	// Positive means this account owes the peer (peer has credit).
	// Negative means peer owes this account (account has credit).
	Balance state.Amount

	// Limit is the trust line limit set by this account (how much peer credit to accept).
	Limit state.Amount

	// LimitPeer is the trust line limit set by the peer.
	LimitPeer state.Amount

	// AccountID is this account (the viewing account).
	AccountID [20]byte

	// AccountIDPeer is the other account on the trust line.
	AccountIDPeer [20]byte

	// NoRipple is true if no-ripple is set on this account's side.
	NoRipple bool

	// NoRipplePeer is true if no-ripple is set on the peer's side.
	NoRipplePeer bool

	// Freeze is true if this account has frozen the peer.
	Freeze bool

	// FreezePeer is true if the peer has frozen this account.
	FreezePeer bool

	// Auth is true if this account has authorized the trust line.
	Auth bool

	// AuthPeer is true if the peer has authorized the trust line.
	AuthPeer bool

	// Currency is the trust line currency code.
	Currency string
}

// accountKey is the cache key for RippleLineCache.
type accountKey struct {
	Account   [20]byte
	Direction LineDirection
}

// RippleLineCache caches trust lines per (account, direction) to avoid
// repeated ledger lookups during a pathfinding session.
// Reference: rippled RippleLineCache
type RippleLineCache struct {
	ledger tx.LedgerView
	mu     sync.RWMutex
	lines  map[accountKey][]PathFindTrustLine
}

// NewRippleLineCache creates a new cache backed by the given ledger view.
func NewRippleLineCache(ledger tx.LedgerView) *RippleLineCache {
	return &RippleLineCache{
		ledger: ledger,
		lines:  make(map[accountKey][]PathFindTrustLine),
	}
}

// GetLedger returns the underlying ledger view.
func (c *RippleLineCache) GetLedger() tx.LedgerView {
	return c.ledger
}

// GetRippleLines returns trust lines for the given account and direction.
// Results are cached. If incoming is requested after outgoing was cached,
// the outgoing set is returned (it's a superset).
// Reference: rippled RippleLineCache::getRippleLines
func (c *RippleLineCache) GetRippleLines(account [20]byte, direction LineDirection) []PathFindTrustLine {
	key := accountKey{Account: account, Direction: direction}

	c.mu.RLock()
	if cached, ok := c.lines[key]; ok {
		c.mu.RUnlock()
		return cached
	}
	c.mu.RUnlock()

	// Check if the opposite direction is already cached.
	// Outgoing is a superset of incoming, so reuse if available.
	if direction == LineDirectionIncoming {
		oppositeKey := accountKey{Account: account, Direction: LineDirectionOutgoing}
		c.mu.RLock()
		if cached, ok := c.lines[oppositeKey]; ok {
			c.mu.RUnlock()
			// Store under incoming key too for future lookups
			c.mu.Lock()
			c.lines[key] = cached
			c.mu.Unlock()
			return cached
		}
		c.mu.RUnlock()
	}

	// Build trust lines from ledger
	lines := c.buildTrustLines(account, direction)

	c.mu.Lock()
	c.lines[key] = lines
	c.mu.Unlock()

	return lines
}

// buildTrustLines walks an account's owner directory and extracts all
// RippleState entries, oriented from the account's perspective.
func (c *RippleLineCache) buildTrustLines(account [20]byte, direction LineDirection) []PathFindTrustLine {
	var lines []PathFindTrustLine

	dirKey := keylet.OwnerDir(account)
	_ = state.DirForEach(c.ledger, dirKey, func(itemKey [32]byte) error {
		k := keylet.Keylet{Key: itemKey}
		data, err := c.ledger.Read(k)
		if err != nil || data == nil {
			return nil // skip unreadable entries
		}

		rs, err := state.ParseRippleState(data)
		if err != nil {
			return nil // not a RippleState or parse error, skip
		}

		line := buildPathFindTrustLine(rs, account)

		// For incoming direction, only include lines where no-ripple is NOT set
		// on the viewing account's side. This filters to lines where rippling is allowed.
		if direction == LineDirectionIncoming && line.NoRipple {
			return nil
		}

		lines = append(lines, line)
		return nil
	})

	return lines
}

// buildPathFindTrustLine creates a PathFindTrustLine from a RippleState,
// oriented from the perspective of the given viewAccount.
// Reference: rippled TrustLineBase constructor
func buildPathFindTrustLine(rs *state.RippleState, viewAccount [20]byte) PathFindTrustLine {
	// Determine which side of the trust line this account is on.
	// LowLimit.Issuer is the "low" account, HighLimit.Issuer is the "high" account.
	lowAccount, _ := state.DecodeAccountID(rs.LowLimit.Issuer)
	highAccount, _ := state.DecodeAccountID(rs.HighLimit.Issuer)
	viewIsLow := lowAccount == viewAccount

	line := PathFindTrustLine{}

	// Get the currency from the balance
	currency := rs.Balance.Currency
	if currency == "" {
		currency = rs.LowLimit.Currency
	}
	if currency == "" {
		currency = rs.HighLimit.Currency
	}
	line.Currency = currency

	if viewIsLow {
		// Viewing from low account's perspective
		line.AccountID = lowAccount
		line.AccountIDPeer = highAccount
		line.Balance = rs.Balance
		// Negate: rippled negates balance when viewIsLow is false.
		// But in rippled, balance is positive when low owes high.
		// When viewing as low, balance stays as-is.
		line.Limit = rs.LowLimit
		line.LimitPeer = rs.HighLimit
		line.NoRipple = rs.Flags&state.LsfLowNoRipple != 0
		line.NoRipplePeer = rs.Flags&state.LsfHighNoRipple != 0
		line.Freeze = rs.Flags&state.LsfLowFreeze != 0
		line.FreezePeer = rs.Flags&state.LsfHighFreeze != 0
		line.Auth = rs.Flags&state.LsfLowAuth != 0
		line.AuthPeer = rs.Flags&state.LsfHighAuth != 0
	} else {
		// Viewing from high account's perspective
		line.AccountID = highAccount
		line.AccountIDPeer = lowAccount
		// Negate balance for high account perspective
		line.Balance = rs.Balance.Negate()
		line.Limit = rs.HighLimit
		line.LimitPeer = rs.LowLimit
		line.NoRipple = rs.Flags&state.LsfHighNoRipple != 0
		line.NoRipplePeer = rs.Flags&state.LsfLowNoRipple != 0
		line.Freeze = rs.Flags&state.LsfHighFreeze != 0
		line.FreezePeer = rs.Flags&state.LsfLowFreeze != 0
		line.Auth = rs.Flags&state.LsfHighAuth != 0
		line.AuthPeer = rs.Flags&state.LsfLowAuth != 0
	}

	return line
}
