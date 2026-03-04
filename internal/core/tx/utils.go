package tx

import (
	"github.com/LeJamon/goXRPLd/internal/core/ledger/keylet"
	"github.com/LeJamon/goXRPLd/internal/core/tx/sle"
	"strconv"
)

// ParseUint64Hex parses a hex string as uint64
func ParseUint64Hex(s string) (uint64, error) {
	return strconv.ParseUint(s, 16, 64)
}

// FormatUint64Hex formats a uint64 as lowercase hex without leading zeros
func FormatUint64Hex(v uint64) string {
	return strconv.FormatUint(v, 16)
}

// IsTrustlineFrozen checks if a specific trustline is frozen.
func IsTrustlineFrozen(view LedgerView, accountID, issuerID [20]byte, currency string) bool {
	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return false
	}

	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	// Check if the ISSUER has frozen this trust line.
	// Reference: rippled View.cpp isFrozen() - checks the issuer's freeze flag:
	//   (issuer > account) ? lsfHighFreeze : lsfLowFreeze
	issuerIsHigh := sle.CompareAccountIDsForLine(issuerID, accountID) > 0
	if issuerIsHigh {
		return (rs.Flags & sle.LsfHighFreeze) != 0
	}
	return (rs.Flags & sle.LsfLowFreeze) != 0
}

// IsIndividualFrozen checks if a specific account is individually frozen for an asset.
// This checks if the issuer has frozen the account's side of the trustline.
// Reference: rippled ledger/View.cpp isIndividualFrozen
func IsIndividualFrozen(view LedgerView, accountID [20]byte, asset Asset) bool {
	// XRP cannot be frozen
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}

	issuerID, err := sle.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}

	// If account is issuer, not frozen
	if accountID == issuerID {
		return false
	}

	trustLineKey := keylet.Line(accountID, issuerID, asset.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return false
	}

	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	// Check if the issuer has frozen the account's side
	// The issuer is on the opposite side of the account in the trustline
	accountIsLow := sle.CompareAccountIDsForLine(accountID, issuerID) < 0
	if accountIsLow {
		// Account is low, issuer is high - check if high side froze low side
		return (rs.Flags & sle.LsfLowFreeze) != 0
	}
	// Account is high, issuer is low - check if low side froze high side
	return (rs.Flags & sle.LsfHighFreeze) != 0
}

// IsGlobalFrozen checks if an issuer has globally frozen assets.
// Reference: rippled ledger/View.h isGlobalFrozen()
func IsGlobalFrozen(view LedgerView, issuerAddress string) bool {
	if issuerAddress == "" {
		return false
	}

	issuerID, err := sle.DecodeAccountID(issuerAddress)
	if err != nil {
		return false
	}

	accountKey := keylet.Account(issuerID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return false
	}

	account, err := sle.ParseAccountRoot(data)
	if err != nil {
		return false
	}

	return (account.Flags & sle.LsfGlobalFreeze) != 0
}

// XRPLiquid returns the amount of XRP an account can spend (balance minus reserve).
// Reference: rippled ledger/View.cpp xrpLiquid()
// ownerCountAdj allows adjusting the owner count (e.g., +1 to account for a pending new object).
func XRPLiquid(view LedgerView, accountID [20]byte, ownerCountAdj int64, reserveBase, reserveIncrement uint64) Amount {
	accountKey := keylet.Account(accountID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return NewXRPAmount(0)
	}

	account, err := sle.ParseAccountRoot(data)
	if err != nil {
		return NewXRPAmount(0)
	}

	ownerCount := int64(account.OwnerCount) + ownerCountAdj
	if ownerCount < 0 {
		ownerCount = 0
	}
	reserve := reserveBase + uint64(ownerCount)*reserveIncrement
	if account.Balance > reserve {
		return NewXRPAmount(int64(account.Balance - reserve))
	}
	return NewXRPAmount(0)
}

// AccountFunds returns the amount of funds an account has available.
// If fhZeroIfFrozen is true, returns zero if the asset is frozen.
// For XRP, returns balance minus reserve (xrpLiquid). reserveBase and reserveIncrement
// are required for XRP reserve calculation.
// Reference: rippled ledger/View.h accountFunds()
func AccountFunds(view LedgerView, accountID [20]byte, amount Amount, fhZeroIfFrozen bool, reserveBase, reserveIncrement uint64) Amount {
	if amount.IsNative() {
		return XRPLiquid(view, accountID, 0, reserveBase, reserveIncrement)
	}

	// IOU balance
	issuerID, err := sle.DecodeAccountID(amount.Issuer)
	if err != nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	// If account is issuer, they have unlimited funds
	if accountID == issuerID {
		// Return a very large amount (10^15 with exponent 0)
		return NewIssuedAmount(sle.MaxMantissa, 0, amount.Currency, amount.Issuer)
	}

	// Check for frozen if requested
	if fhZeroIfFrozen {
		if IsGlobalFrozen(view, amount.Issuer) {
			return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// Check individual trustline freeze
		if IsTrustlineFrozen(view, accountID, issuerID, amount.Currency) {
			return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
	}

	// Read trustline balance
	trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	rs, err := sle.ParseRippleState(trustLineData)
	if err != nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	// Determine balance based on canonical ordering
	accountIsLow := sle.CompareAccountIDsForLine(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	// Only return positive balance as available funds
	if balance.Signum() <= 0 {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	return sle.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, amount.Issuer)
}
