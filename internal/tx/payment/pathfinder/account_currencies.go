package pathfinder

import (
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
)

// AccountSourceCurrencies returns the set of Issues (currency+issuer) that
// the given account can send. XRP is always included.
// Reference: rippled accountSourceCurrencies()
func AccountSourceCurrencies(account [20]byte, cache *RippleLineCache) map[payment.Issue]bool {
	currencies := make(map[payment.Issue]bool)

	// XRP is always sendable
	currencies[payment.Issue{Currency: "XRP"}] = true

	lines := cache.GetRippleLines(account, LineDirectionOutgoing)
	for _, line := range lines {
		// Include if:
		// 1. Balance > 0 (account holds IOUs it can send), OR
		// 2. Peer extends credit AND there's room to use it
		//    (peer's limit > 0 and account hasn't used all of it)
		balSig := line.Balance.Signum()
		if balSig > 0 {
			// Account has positive balance (peer owes us) — we can send this currency
			currencies[payment.Issue{Currency: line.Currency, Issuer: line.AccountIDPeer}] = true
		} else if !line.LimitPeer.IsZero() && !line.LimitPeer.IsNegative() {
			// Peer extends credit to us
			negBalance := line.Balance.Negate()
			if negBalance.Compare(line.LimitPeer) < 0 {
				// We haven't exhausted the credit line — can issue more
				currencies[payment.Issue{Currency: line.Currency, Issuer: line.AccountIDPeer}] = true
			}
		}
	}

	return currencies
}

// AccountDestCurrencies returns the set of Issues (currency+issuer) that
// the given account can receive. XRP is always included.
// Reference: rippled accountDestCurrencies()
func AccountDestCurrencies(account [20]byte, cache *RippleLineCache) map[payment.Issue]bool {
	currencies := make(map[payment.Issue]bool)

	// XRP is always receivable
	currencies[payment.Issue{Currency: "XRP"}] = true

	lines := cache.GetRippleLines(account, LineDirectionOutgoing)
	for _, line := range lines {
		// Include if balance < limit (can accept more of this currency)
		if line.Balance.Compare(line.Limit) < 0 {
			currencies[payment.Issue{Currency: line.Currency, Issuer: line.AccountIDPeer}] = true
		}
	}

	return currencies
}
