package tx

import (
	"encoding/hex"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
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

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	// Check if the ISSUER has frozen this trust line.
	// Reference: rippled View.cpp isFrozen() - checks the issuer's freeze flag:
	//   (issuer > account) ? lsfHighFreeze : lsfLowFreeze
	issuerIsHigh := state.CompareAccountIDsForLine(issuerID, accountID) > 0
	if issuerIsHigh {
		return (rs.Flags & state.LsfHighFreeze) != 0
	}
	return (rs.Flags & state.LsfLowFreeze) != 0
}

// IsIndividualFrozen checks if a specific account is individually frozen for an asset.
// This checks if the issuer has frozen the account's side of the trustline.
// Reference: rippled ledger/View.cpp isIndividualFrozen
func IsIndividualFrozen(view LedgerView, accountID [20]byte, asset Asset) bool {
	// XRP cannot be frozen
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
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

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	// Check if the issuer has frozen the trust line.
	// Reference: rippled View.cpp isFrozen() line 264:
	//   sle->isFlag((issuer > account) ? lsfHighFreeze : lsfLowFreeze)
	// The freeze flag is on the ISSUER's side of the trust line.
	issuerIsHigh := state.CompareAccountIDsForLine(issuerID, accountID) > 0
	if issuerIsHigh {
		return (rs.Flags & state.LsfHighFreeze) != 0
	}
	return (rs.Flags & state.LsfLowFreeze) != 0
}

// TransferRateParity is the transfer-rate value (1e9) that means "no fee".
// Reference: rippled basics/Rate.h parityRate.
const TransferRateParity uint32 = 1_000_000_000

// GetTransferRate returns the issuer's transfer rate. Returns TransferRateParity
// (1e9 = no fee) for unset or unknown issuers, and for the empty address.
// Reference: rippled ledger/View.cpp transferRate(view, account).
//
// rippled tests sfTransferRate field presence; go-xrpl relies on the AccountRoot
// serializer at internal/ledger/state/account_root.go only writing
// TransferRate when the value is nonzero, so a zero-value field is
// indistinguishable from an absent field on disk.
func GetTransferRate(view LedgerView, issuerAddress string) uint32 {
	if issuerAddress == "" {
		return TransferRateParity
	}
	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return TransferRateParity
	}
	accountKey := keylet.Account(issuerID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return TransferRateParity
	}
	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return TransferRateParity
	}
	if account.TransferRate == 0 {
		return TransferRateParity
	}
	return account.TransferRate
}

// IsGlobalFrozen checks if an issuer has globally frozen assets.
// Reference: rippled ledger/View.h isGlobalFrozen()
func IsGlobalFrozen(view LedgerView, issuerAddress string) bool {
	if issuerAddress == "" {
		return false
	}

	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return false
	}

	accountKey := keylet.Account(issuerID)
	data, err := view.Read(accountKey)
	if err != nil || data == nil {
		return false
	}

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return false
	}

	return (account.Flags & state.LsfGlobalFreeze) != 0
}

// IsDeepFrozen checks if a trust line between account and issuer is deep-frozen.
// Deep freeze can be set by either side of the trust line. Unlike regular freeze
// (which only checks the issuer's side), deep freeze checks both lsfLowDeepFreeze
// and lsfHighDeepFreeze.
// Reference: rippled ledger/View.cpp isDeepFrozen()
func IsDeepFrozen(view LedgerView, accountID, issuerID [20]byte, currency string) bool {
	// XRP cannot be frozen
	if currency == "" || currency == "XRP" {
		return false
	}

	// If account is issuer, not frozen
	if accountID == issuerID {
		return false
	}

	trustLineKey := keylet.Line(accountID, issuerID, currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return false
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return false
	}

	return (rs.Flags & (state.LsfLowDeepFreeze | state.LsfHighDeepFreeze)) != 0
}

// isFrozenForLPToken reports whether account cannot spend the given asset because
// the issuer globally froze it or individually froze the account's trust line.
// This is the IOU overload of rippled's isFrozen (global freeze + issuer-side
// individual freeze); deep freeze is intentionally not consulted, matching the
// overload used by isLPTokenFrozen.
// Reference: rippled ledger/View.cpp isFrozen().
func isFrozenForLPToken(view LedgerView, accountID [20]byte, asset Asset) bool {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}
	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}
	if accountID == issuerID {
		return false
	}
	if IsGlobalFrozen(view, asset.Issuer) {
		return true
	}
	return IsTrustlineFrozen(view, accountID, issuerID, asset.Currency)
}

// IsLPTokenFrozen reports whether either of an AMM pool's underlying assets is
// frozen for the holder, in which case the holder's LP tokens must count as zero
// funds. The caller resolves the pool assets from the LP-token issuer's AMM SLE.
// Reference: rippled ledger/View.cpp isLPTokenFrozen().
func IsLPTokenFrozen(view LedgerView, accountID [20]byte, asset, asset2 Asset) bool {
	return isFrozenForLPToken(view, accountID, asset) ||
		isFrozenForLPToken(view, accountID, asset2)
}

// decodeAMMPoolAssets extracts the sfAsset and sfAsset2 issues from a serialized
// AMM ledger entry without depending on the amm package (which would form an
// import cycle).
func decodeAMMPoolAssets(data []byte) (Asset, Asset, bool) {
	fields, err := binarycodec.Decode(hex.EncodeToString(data))
	if err != nil {
		return Asset{}, Asset{}, false
	}
	asset, ok1 := issueFromField(fields["Asset"])
	asset2, ok2 := issueFromField(fields["Asset2"])
	if !ok1 || !ok2 {
		return Asset{}, Asset{}, false
	}
	return asset, asset2, true
}

// issueFromField converts a decoded binary-codec Issue map into a tx.Asset.
func issueFromField(field any) (Asset, bool) {
	m, ok := field.(map[string]any)
	if !ok {
		return Asset{}, false
	}
	asset := Asset{}
	if currency, ok := m["currency"].(string); ok {
		asset.Currency = currency
	}
	if issuer, ok := m["issuer"].(string); ok {
		asset.Issuer = issuer
	}
	return asset, true
}

// LPTokenFrozenForIssuer determines, for a token whose issuer is issuerID,
// whether that issuer is an AMM pseudo-account (isAMM) and, if so, whether the
// AMM's underlying assets are frozen for accountID (frozen). When the issuer is
// an AMM pseudo-account but its AMM SLE cannot be resolved, frozen is reported
// true, matching rippled's `!sleAmm` branch which zeroes the balance.
// Callers must gate this on the fixFrozenLPTokenTransfer amendment.
// Reference: rippled ledger/View.cpp accountHolds()/checkFreeze() LP-token arm.
func LPTokenFrozenForIssuer(view LedgerView, accountID, issuerID [20]byte) (frozen, isAMM bool) {
	acctData, err := view.Read(keylet.Account(issuerID))
	if err != nil || acctData == nil {
		return false, false
	}
	account, err := state.ParseAccountRoot(acctData)
	if err != nil || !account.HasAMMID() {
		return false, false
	}
	ammData, err := view.Read(keylet.AMMByID(account.AMMID))
	if err != nil || ammData == nil {
		return true, true
	}
	asset, asset2, ok := decodeAMMPoolAssets(ammData)
	if !ok {
		return true, true
	}
	return IsLPTokenFrozen(view, accountID, asset, asset2), true
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

	account, err := state.ParseAccountRoot(data)
	if err != nil {
		return NewXRPAmount(0)
	}

	ownerCount := max(int64(account.OwnerCount)+ownerCountAdj, 0)
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
	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	// If account is issuer, they have unlimited funds
	if accountID == issuerID {
		// Return a very large amount (10^15 with exponent 0)
		return NewIssuedAmount(state.MaxMantissa, 0, amount.Currency, amount.Issuer)
	}

	// Check for frozen if requested
	// Reference: rippled View.cpp accountHolds() — checks isFrozen || isDeepFrozen
	if fhZeroIfFrozen {
		if IsGlobalFrozen(view, amount.Issuer) {
			return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// Check individual trustline freeze
		if IsTrustlineFrozen(view, accountID, issuerID, amount.Currency) {
			return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// Check deep freeze — either side of the trust line can deep-freeze it
		if IsDeepFrozen(view, accountID, issuerID, amount.Currency) {
			return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// LP tokens count as zero funds when the underlying AMM assets are frozen.
		// Reference: rippled View.cpp accountHolds() lines 415-439.
		if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureFixFrozenLPTokenTransfer) {
			if frozen, isAMM := LPTokenFrozenForIssuer(view, accountID, issuerID); isAMM && frozen {
				return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
			}
		}
	}

	// Read trustline balance
	trustLineKey := keylet.Line(accountID, issuerID, amount.Currency)
	trustLineData, err := view.Read(trustLineKey)
	if err != nil || trustLineData == nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	// Determine balance based on canonical ordering
	accountIsLow := state.CompareAccountIDsForLine(accountID, issuerID) < 0
	balance := rs.Balance
	if !accountIsLow {
		balance = balance.Negate()
	}

	// Only return positive balance as available funds
	if balance.Signum() <= 0 {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}

	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, amount.Issuer)
}
