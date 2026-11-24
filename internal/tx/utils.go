package tx

import (
	"fmt"
	"math"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// FormatUint64Hex formats a uint64 as lowercase hex without leading zeros
func FormatUint64Hex(v uint64) string {
	return strconv.FormatUint(v, 16)
}

type ledgerEntryReader interface {
	Read(keylet.Keylet) ([]byte, error)
}

// ReadAccountRoot reads and parses the AccountRoot for accountID. It preserves
// the missing-vs-error distinction: a genuinely absent account returns
// (nil, nil), while a real storage or parse failure returns (nil, err). Callers
// that must distinguish the two route err != nil to TefINTERNAL and nil data to
// their own not-found code; callers that legitimately treat both the same can
// test (root == nil) after checking err.
func ReadAccountRoot(view ledgerEntryReader, accountID [20]byte) (*state.AccountRoot, error) {
	data, err := view.Read(keylet.Account(accountID))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return state.ParseAccountRoot(data)
}

// ReadRippleState reads and parses the RippleState (trust line) between account
// and issuer for currency. It mirrors ReadAccountRoot's missing-vs-error
// contract: an absent line returns (nil, nil), while a storage or parse failure
// returns (nil, err). Callers that treat "absent" and "error" the same can test
// (rs == nil) after checking err.
func ReadRippleState(view ledgerEntryReader, accountID, issuerID [20]byte, currency string) (*state.RippleState, error) {
	data, err := view.Read(keylet.Line(accountID, issuerID, currency))
	if err != nil {
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	return state.ParseRippleState(data)
}

// IsRippleStateFrozenBy reports whether freezer set its regular-freeze flag on
// an already-parsed trust line with counterparty.
func IsRippleStateFrozenBy(line *state.RippleState, freezerID, counterpartyID [20]byte) bool {
	if line == nil || freezerID == counterpartyID {
		return false
	}
	if state.CompareAccountIDs(freezerID, counterpartyID) > 0 {
		return line.Flags&state.LsfHighFreeze != 0
	}
	return line.Flags&state.LsfLowFreeze != 0
}

// IsTrustlineFrozenBy reports whether freezer set its regular-freeze flag on
// the trust line with counterparty. A missing line is not frozen; read and
// parse errors are returned to callers that must preserve ledger-integrity
// failures.
func IsTrustlineFrozenBy(view state.ReadOnlyLedgerView, freezerID, counterpartyID [20]byte, currency string) (bool, error) {
	if currency == "" || currency == "XRP" || freezerID == counterpartyID {
		return false, nil
	}
	line, err := ReadRippleState(view, freezerID, counterpartyID, currency)
	if err != nil || line == nil {
		return false, err
	}
	return IsRippleStateFrozenBy(line, freezerID, counterpartyID), nil
}

// IsTrustlineFrozen reports whether issuer individually froze account's trust
// line. Read and parse failures retain the historical false result.
func IsTrustlineFrozen(view state.ReadOnlyLedgerView, accountID, issuerID [20]byte, currency string) bool {
	frozen, _ := IsTrustlineFrozenBy(view, issuerID, accountID, currency)
	return frozen
}

// IsIndividualFrozen checks if a specific account is individually frozen for an asset.
// This checks if the issuer has frozen the account's side of the trustline.
// Reference: rippled ledger/View.cpp isIndividualFrozen
func IsIndividualFrozen(view state.ReadOnlyLedgerView, accountID [20]byte, asset Asset) bool {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}

	issuerID, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}

	return IsTrustlineFrozen(view, accountID, issuerID, asset.Currency)
}

// IsIOUFrozen reports whether issuer globally froze its currency or
// individually froze account's trust line. Missing ledger entries are not
// frozen; read and parse errors are returned.
func IsIOUFrozen(view state.ReadOnlyLedgerView, accountID, issuerID [20]byte, currency string) (bool, error) {
	if currency == "" || currency == "XRP" {
		return false, nil
	}
	issuer, err := ReadAccountRoot(view, issuerID)
	if err != nil {
		return false, err
	}
	if IsAccountGlobalFrozen(issuer) {
		return true, nil
	}
	return IsTrustlineFrozenBy(view, issuerID, accountID, currency)
}

// IsAccountGlobalFrozen reports whether an already-read issuer AccountRoot set
// lsfGlobalFreeze. A missing account is not frozen.
func IsAccountGlobalFrozen(issuer *state.AccountRoot) bool {
	return issuer != nil && issuer.Flags&state.LsfGlobalFreeze != 0
}

// IsRippleStateDeepFrozen reports whether either side deep-froze an
// already-parsed trust line. A missing line is not deep-frozen.
func IsRippleStateDeepFrozen(line *state.RippleState) bool {
	return line != nil && line.Flags&(state.LsfLowDeepFreeze|state.LsfHighDeepFreeze) != 0
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
func IsFrozen(view state.ReadOnlyLedgerView, accountID [20]byte, asset Asset) bool {
	if asset.Currency == "" || asset.Currency == "XRP" {
		return false
	}
	return IsGlobalFrozen(view, asset.Issuer) || IsIndividualFrozen(view, accountID, asset)
}

// TransferRateParity is the transfer-rate value (1e9) that means "no fee".
// Reference: rippled basics/Rate.h parityRate.
const TransferRateParity uint32 = protocol.QualityOne

// GetTransferRate returns the issuer's transfer rate. Returns TransferRateParity
// (1e9 = no fee) for unset or unknown issuers, and for the empty address.
// Reference: rippled ledger/View.cpp transferRate(view, account).
//
// rippled tests sfTransferRate field presence; go-xrpl relies on the AccountRoot
// serializer at internal/ledger/state/account_root.go only writing
// TransferRate when the value is nonzero, so a zero-value field is
// indistinguishable from an absent field on disk.
func GetTransferRate(view ReadOnlyLedgerView, issuerAddress string) uint32 {
	if issuerAddress == "" {
		return TransferRateParity
	}
	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return TransferRateParity
	}
	return GetTransferRateByID(view, issuerID)
}

// GetTransferRateByID is GetTransferRate for callers that already hold the
// issuer's decoded [20]byte account ID. Read and parse failures report parity.
func GetTransferRateByID(view state.ReadOnlyLedgerView, issuerID [20]byte) uint32 {
	rate, _ := ReadTransferRate(view, issuerID)
	return rate
}

// ReadTransferRate implements rippled's transferRate(view, issuer) for callers
// that must fail closed: a missing issuer or unset rate is parity, while read
// and parse errors are returned alongside parity.
func ReadTransferRate(view ledgerEntryReader, issuerID [20]byte) (uint32, error) {
	account, err := ReadAccountRoot(view, issuerID)
	if err != nil || account == nil || account.TransferRate == 0 {
		return TransferRateParity, err
	}
	return account.TransferRate, nil
}

// IsGlobalFrozen checks if an issuer has globally frozen assets.
// Reference: rippled ledger/View.h isGlobalFrozen()
func IsGlobalFrozen(view state.ReadOnlyLedgerView, issuerAddress string) bool {
	if issuerAddress == "" {
		return false
	}

	issuerID, err := state.DecodeAccountID(issuerAddress)
	if err != nil {
		return false
	}

	account, err := ReadAccountRoot(view, issuerID)
	if err != nil {
		return false
	}
	return IsAccountGlobalFrozen(account)
}

// IsDeepFrozen implements rippled's isDeepFrozen for an IOU: either side of the
// trust line set a deep-freeze flag. Read and parse failures report false.
func IsDeepFrozen(view state.ReadOnlyLedgerView, accountID, issuerID [20]byte, currency string) bool {
	frozen, _ := IsIOUDeepFrozen(view, accountID, issuerID, currency)
	return frozen
}

// IsIOUDeepFrozen is IsDeepFrozen for callers that must fail closed: read and
// parse errors are returned. XRP, the issuer itself and a missing line are not
// deep-frozen.
func IsIOUDeepFrozen(view state.ReadOnlyLedgerView, accountID, issuerID [20]byte, currency string) (bool, error) {
	if currency == "" || currency == "XRP" || accountID == issuerID {
		return false, nil
	}
	line, err := ReadRippleState(view, accountID, issuerID, currency)
	if err != nil {
		return false, err
	}
	return IsRippleStateDeepFrozen(line), nil
}

// ownerCountReadHookView is the optional read-side owner-count hook a view may
// implement. It mirrors rippled's virtual ReadView::ownerCountHook, which is a
// no-op on the base view (returns the count unchanged) and is overridden by the
// PaymentSandbox to return the maximum owner count seen so far during a payment.
// This prevents an account from using reserve freed by objects it deleted
// earlier in the same transaction.
type ownerCountReadHookView interface {
	OwnerCountHook(account [20]byte, count uint32) uint32
}

type ownerCountsReadHookView interface {
	OwnerCountsHook(account [20]byte, counts OwnerCounts) OwnerCounts
}

type balanceReadHookView interface {
	BalanceHook(account, issuer [20]byte, amount Amount) Amount
}

func accountWithOwnerCountHook(view ReadOnlyLedgerView, accountID [20]byte, account *state.AccountRoot) state.AccountRoot {
	result := *account
	if hook, ok := view.(ownerCountsReadHookView); ok {
		counts := hook.OwnerCountsHook(accountID, ownerCounts(account))
		result.OwnerCount = counts.Owner
		result.SponsoredOwnerCount = counts.Sponsored
		result.SponsoringOwnerCount = counts.Sponsoring
	} else if hook, ok := view.(ownerCountReadHookView); ok {
		result.OwnerCount = hook.OwnerCountHook(accountID, account.OwnerCount)
	}
	return result
}

// XRPLiquid returns the amount of XRP an account can spend (balance minus reserve).
// Reference: rippled ledger/View.cpp xrpLiquid()
// ownerCountAdj allows adjusting the owner count (e.g., +1 to account for a pending new object).
func XRPLiquid(view ReadOnlyLedgerView, accountID [20]byte, ownerCountAdj int64, reserveBase, reserveIncrement uint64) Amount {
	account, err := ReadAccountRoot(view, accountID)
	if err != nil {
		return NewXRPAmount(0)
	}
	return XRPLiquidOf(view, accountID, account, ownerCountAdj, reserveBase, reserveIncrement)
}

// XRPLiquidOf is XRPLiquid for an AccountRoot the caller already read from
// view; account is not modified.
func XRPLiquidOf(view ReadOnlyLedgerView, accountID [20]byte, account *state.AccountRoot, ownerCountAdj int64, reserveBase, reserveIncrement uint64) Amount {
	if account == nil {
		return NewXRPAmount(0)
	}
	balance := NewXRPAmount(int64(account.Balance))
	if hook, ok := view.(balanceReadHookView); ok {
		balance = hook.BalanceHook(accountID, [20]byte{}, balance)
	}
	if account.IsPseudoAccount() {
		return balance
	}
	if ownerCountAdj > math.MaxInt || ownerCountAdj < math.MinInt {
		return NewXRPAmount(0)
	}
	hooked := accountWithOwnerCountHook(view, accountID, account)
	ownerCount, accountCount := hooked.OwnerCount, uint32(1)
	if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureSponsor) {
		if hooked.SponsoredOwnerCount > hooked.OwnerCount {
			return NewXRPAmount(0)
		}
		ownerCount = ownerCounts(&hooked).effective()
		accountCount = reserveAccountCount(&hooked, 0)
	}
	ownerCount = ConfineOwnerCount(ownerCount, int(ownerCountAdj))
	reserve, ok := checkedAccountReserve(EngineConfig{ReserveBase: reserveBase, ReserveIncrement: reserveIncrement}, ownerCount, accountCount)
	if !ok {
		return NewXRPAmount(0)
	}
	if spendable := balance.Drops(); spendable > 0 && uint64(spendable) > reserve {
		return NewXRPAmount(spendable - int64(reserve))
	}
	return NewXRPAmount(0)
}

// AccountFundsNoFreezeStrict returns owner funds without applying freeze
// rules. Missing account or trust-line entries are a zero balance; storage,
// decoding, and reserve-arithmetic failures are returned to the caller.
func AccountFundsNoFreezeStrict(view ReadOnlyLedgerView, accountID [20]byte, amount Amount, reserveBase, reserveIncrement uint64) (Amount, error) {
	if view == nil {
		return Amount{}, fmt.Errorf("account funds: nil ledger view")
	}
	if amount.IsNative() {
		account, err := ReadAccountRoot(view, accountID)
		if err != nil {
			return Amount{}, fmt.Errorf("account funds: read account root: %w", err)
		}
		if account == nil {
			return NewXRPAmount(0), nil
		}
		accountWithHook := accountWithOwnerCountHook(view, accountID, account)
		ownerCount := accountWithHook.OwnerCount
		if account.IsPseudoAccount() {
			return NewXRPAmount(int64(account.Balance)), nil
		}
		reserve, ok := AccountReserveForView(view, EngineConfig{
			ReserveBase: reserveBase, ReserveIncrement: reserveIncrement,
		}, &accountWithHook, ownerCount)
		if !ok {
			return Amount{}, fmt.Errorf("account funds: invalid reserve counters")
		}
		if account.Balance <= reserve {
			return NewXRPAmount(0), nil
		}
		liquid := account.Balance - reserve
		if liquid > math.MaxInt64 {
			return Amount{}, fmt.Errorf("account funds: liquid XRP exceeds int64")
		}
		return NewXRPAmount(int64(liquid)), nil
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return Amount{}, fmt.Errorf("account funds: decode issuer: %w", err)
	}
	if accountID == issuerID {
		return NewIssuedAmount(state.MaxMantissa, 0, amount.Currency, amount.Issuer), nil
	}
	line, err := ReadRippleState(view, accountID, issuerID, amount.Currency)
	if err != nil {
		return Amount{}, fmt.Errorf("account funds: read trust line: %w", err)
	}
	if line == nil {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer), nil
	}
	balance := line.Balance
	if state.CompareAccountIDs(accountID, issuerID) >= 0 {
		balance = balance.Negate()
	}
	if balance.Signum() <= 0 {
		return NewIssuedAmount(0, 0, amount.Currency, amount.Issuer), nil
	}
	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, amount.Issuer), nil
}
