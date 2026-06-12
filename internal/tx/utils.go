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

// ReadAccountRoot reads and parses the AccountRoot for accountID. It preserves
// the missing-vs-error distinction: a genuinely absent account returns
// (nil, nil), while a real storage or parse failure returns (nil, err). Callers
// that must distinguish the two route err != nil to TefINTERNAL and nil data to
// their own not-found code; callers that legitimately treat both the same can
// test (root == nil) after checking err.
func ReadAccountRoot(view LedgerView, accountID [20]byte) (*state.AccountRoot, error) {
	data, err := view.Read(keylet.Account(accountID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return state.ParseAccountRoot(data)
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

// HasExpired reports whether an optional expiration has passed relative to the
// parent ledger's close time. A nil expiration is never expired. Mirrors
// rippled's hasExpired(view, std::optional<uint32>): present && parentCloseTime
// >= *exp, expressed here as *exp <= parentCloseTime.
// Reference: rippled ledger/detail/View.cpp hasExpired().
func HasExpired(expiration *uint32, parentCloseTime uint32) bool {
	return expiration != nil && *expiration <= parentCloseTime
}

// HasExpiredField is the SLE-field form of HasExpired for stored expiration
// values where zero encodes "no expiration" (sfExpiration is absent rather than
// zero on disk). Equivalent to HasExpired(&v, pct) once a zero is treated as
// not-present. Reference: rippled ledger/detail/View.cpp hasExpired().
func HasExpiredField(expiration uint32, parentCloseTime uint32) bool {
	return expiration != 0 && expiration <= parentCloseTime
}

// IsFrozen reports whether account cannot spend the given asset because the
// issuer globally froze it or individually froze the account's trust line. This
// is the Issue overload of rippled's isFrozen (global freeze OR issuer-side
// individual freeze); deep freeze is not consulted. XRP is never frozen.
// Reference: rippled ledger/View.cpp isFrozen(view, account, currency, issuer)
// and the inline Issue overload in View.h.
func IsFrozen(view LedgerView, accountID [20]byte, asset Asset) bool {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}
	return IsGlobalFrozen(view, asset.Issuer) || IsIndividualFrozen(view, accountID, asset)
}

// RequireAuth reports whether account is authorized to hold the asset, using the
// legacy (weak) auth semantics: a missing trust line is only fatal when the
// issuer has lsfRequireAuth set. Returns TecNO_LINE when auth is required but no
// trust line exists, TecNO_AUTH when the line exists but lacks the issuer's auth
// flag, and TesSUCCESS otherwise (including XRP, self-issued, or a missing issuer
// account). Reference: rippled ledger/View.cpp requireAuth(view, Issue, account)
// with AuthType::Legacy.
func RequireAuth(view LedgerView, asset Asset, accountID [20]byte) Result {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return TesSUCCESS
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return TesSUCCESS
	}
	if accountID == issuerID {
		return TesSUCCESS
	}

	trustLineData, _ := view.Read(keylet.Line(accountID, issuerID, asset.Currency))

	issuerAccount, err := ReadAccountRoot(view, issuerID)
	if err != nil || issuerAccount == nil {
		return TesSUCCESS
	}
	if (issuerAccount.Flags & state.LsfRequireAuth) == 0 {
		return TesSUCCESS
	}

	if trustLineData == nil {
		return TecNO_LINE
	}
	rs, err := state.ParseRippleState(trustLineData)
	if err != nil {
		return TecNO_AUTH
	}

	// (account > issuer) ? lsfLowAuth : lsfHighAuth
	if state.CompareAccountIDsForLine(accountID, issuerID) > 0 {
		if (rs.Flags & state.LsfLowAuth) == 0 {
			return TecNO_AUTH
		}
	} else {
		if (rs.Flags & state.LsfHighAuth) == 0 {
			return TecNO_AUTH
		}
	}
	return TesSUCCESS
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
	account, err := ReadAccountRoot(view, issuerID)
	if err != nil || account == nil {
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

	account, err := ReadAccountRoot(view, issuerID)
	if err != nil || account == nil {
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

// LPTokenFreezeStatus reports the outcome of probing whether a token's issuer is
// an AMM pseudo-account and, if so, whether its underlying pool assets are frozen
// for the holder.
type LPTokenFreezeStatus int

const (
	// LPTokenIssuerNotAMM means the issuer is not an AMM pseudo-account, so the
	// LP-token freeze rules do not apply.
	LPTokenIssuerNotAMM LPTokenFreezeStatus = iota
	// LPTokenNotFrozen means the issuer is an AMM whose underlying assets are not
	// frozen for the holder.
	LPTokenNotFrozen
	// LPTokenFrozen means the issuer is an AMM whose underlying assets are frozen
	// for the holder.
	LPTokenFrozen
	// LPTokenAMMUnresolvable means the issuer carries sfAMMID but its AMM SLE
	// cannot be read or decoded — a corrupt-ledger invariant violation. rippled's
	// accountHolds zeroes funds here (`!sleAmm` → false); checkFreeze returns
	// tecINTERNAL.
	LPTokenAMMUnresolvable
)

// LPTokenFrozenForIssuer determines, for a token whose issuer is issuerID,
// whether that issuer is an AMM pseudo-account and, if so, whether the AMM's
// underlying assets are frozen for accountID. The missing/undecodable AMM SLE
// case is reported distinctly (LPTokenAMMUnresolvable) so the accountHolds path
// can treat it as zero funds while the checkFreeze path returns tecINTERNAL,
// matching rippled exactly.
// Callers must gate this on the fixFrozenLPTokenTransfer amendment.
// Reference: rippled ledger/View.cpp accountHolds() / paths StepChecks.h
// checkFreeze() LP-token arm.
func LPTokenFrozenForIssuer(view LedgerView, accountID, issuerID [20]byte) LPTokenFreezeStatus {
	account, err := ReadAccountRoot(view, issuerID)
	if err != nil || account == nil || !account.HasAMMID() {
		return LPTokenIssuerNotAMM
	}
	ammData, err := view.Read(keylet.AMMByID(account.AMMID))
	if err != nil || ammData == nil {
		return LPTokenAMMUnresolvable
	}
	asset, asset2, ok := decodeAMMPoolAssets(ammData)
	if !ok {
		return LPTokenAMMUnresolvable
	}
	if IsLPTokenFrozen(view, accountID, asset, asset2) {
		return LPTokenFrozen
	}
	return LPTokenNotFrozen
}

// XRPLiquid returns the amount of XRP an account can spend (balance minus reserve).
// Reference: rippled ledger/View.cpp xrpLiquid()
// ownerCountAdj allows adjusting the owner count (e.g., +1 to account for a pending new object).
func XRPLiquid(view LedgerView, accountID [20]byte, ownerCountAdj int64, reserveBase, reserveIncrement uint64) Amount {
	account, err := ReadAccountRoot(view, accountID)
	if err != nil || account == nil {
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
		// An unresolvable AMM SLE also zeroes funds here, mirroring rippled's
		// `!sleAmm || isLPTokenFrozen(...)` → return false.
		// Reference: rippled View.cpp accountHolds() lines 415-439.
		if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureFixFrozenLPTokenTransfer) {
			switch LPTokenFrozenForIssuer(view, accountID, issuerID) {
			case LPTokenFrozen, LPTokenAMMUnresolvable:
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
