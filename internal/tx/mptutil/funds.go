package mptutil

import (
	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// LPTokenFreezeStatus reports the outcome of probing whether a token issuer is
// an AMM pseudo-account and, if so, whether either underlying pool asset is
// frozen for the holder.
type LPTokenFreezeStatus int

const (
	LPTokenIssuerNotAMM LPTokenFreezeStatus = iota
	LPTokenNotFrozen
	LPTokenFrozen
	LPTokenAMMUnresolvable
)

// LPTokenFrozenForIssuer determines whether an AMM-issued LP token is frozen
// for accountID because either pool asset is frozen. Callers gate this helper on
// the fixFrozenLPTokenTransfer amendment.
func LPTokenFrozenForIssuer(view state.ReadOnlyLedgerView, accountID, issuerID [20]byte) LPTokenFreezeStatus {
	account, err := tx.ReadAccountRoot(view, issuerID)
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
	if IsAssetFrozen(view, asset, accountID) || IsAssetFrozen(view, asset2, accountID) {
		return LPTokenFrozen
	}
	return LPTokenNotFrozen
}

// decodeAMMPoolAssets extracts the sfAsset and sfAsset2 issues from a
// serialized AMM ledger entry without depending on the amm package (which
// would form an import cycle).
func decodeAMMPoolAssets(data []byte) (tx.Asset, tx.Asset, bool) {
	typ, err := state.DecodeType(data)
	if err != nil || typ != entry.TypeAMM {
		return tx.Asset{}, tx.Asset{}, false
	}
	decoded := entry.New(typ)
	if decoded == nil {
		return tx.Asset{}, tx.Asset{}, false
	}
	if err := decoded.Decode(data); err != nil {
		return tx.Asset{}, tx.Asset{}, false
	}
	amm, ok := decoded.(*entry.AMM)
	if !ok {
		return tx.Asset{}, tx.Asset{}, false
	}
	assetValue, err := amm.GetAsset()
	if err != nil {
		return tx.Asset{}, tx.Asset{}, false
	}
	asset2Value, err := amm.GetAsset2()
	if err != nil {
		return tx.Asset{}, tx.Asset{}, false
	}
	asset, ok1 := issueFromField(assetValue)
	asset2, ok2 := issueFromField(asset2Value)
	if !ok1 || !ok2 {
		return tx.Asset{}, tx.Asset{}, false
	}
	return asset, asset2, true
}

func issueFromField(field entry.IssueValue) (tx.Asset, bool) {
	if field.MPTIssuanceID != "" {
		return tx.Asset{MPTIssuanceID: field.MPTIssuanceID}, true
	}
	if field.Currency == "" {
		return tx.Asset{}, false
	}
	return tx.Asset{Currency: field.Currency, Issuer: field.Issuer}, true
}

// AccountFunds returns the amount of funds an account has available.
// If fhZeroIfFrozen is true, returns zero if the asset is frozen.
// For XRP, returns balance minus reserve (xrpLiquid). reserveBase and
// reserveIncrement are required for XRP reserve calculation.
// Reference: rippled ledger/View.h accountFunds()
func AccountFunds(view tx.ReadOnlyLedgerView, accountID [20]byte, amount tx.Amount, fhZeroIfFrozen bool, reserveBase, reserveIncrement uint64) tx.Amount {
	if amount.IsNative() {
		return tx.XRPLiquid(view, accountID, 0, reserveBase, reserveIncrement)
	}

	issuerID, err := state.DecodeAccountID(amount.Issuer)
	if err != nil {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}
	// If account is issuer, they have unlimited funds.
	if accountID == issuerID {
		return tx.NewIssuedAmount(state.MaxMantissa, 0, amount.Currency, amount.Issuer)
	}

	if fhZeroIfFrozen {
		// Reference: rippled View.cpp accountHolds() — checks isFrozen || isDeepFrozen.
		if tx.IsGlobalFrozen(view, amount.Issuer) {
			return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// Check individual trustline freeze.
		if tx.IsTrustlineFrozen(view, accountID, issuerID, amount.Currency) {
			return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// Check deep freeze — either side of the trust line can deep-freeze it.
		if tx.IsDeepFrozen(view, accountID, issuerID, amount.Currency) {
			return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
		// LP tokens count as zero funds when the underlying AMM assets are frozen.
		// An unresolvable AMM SLE also zeroes funds here, mirroring rippled's
		// `!sleAmm || isLPTokenFrozen(...)` → return false.
		// Reference: rippled View.cpp accountHolds() lines 415-439.
		if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureFixFrozenLPTokenTransfer) {
			switch LPTokenFrozenForIssuer(view, accountID, issuerID) {
			case LPTokenFrozen, LPTokenAMMUnresolvable:
				return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
			}
		}
		if CanTransferLPToken(view, accountID, accountID, issuerID) != ter.TesSUCCESS {
			return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
		}
	}

	line, err := tx.ReadRippleState(view, accountID, issuerID, amount.Currency)
	if err != nil || line == nil {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}
	balance := line.Balance
	if state.CompareAccountIDs(accountID, issuerID) >= 0 {
		balance = balance.Negate()
	}
	if balance.Signum() <= 0 {
		return tx.NewIssuedAmount(0, 0, amount.Currency, amount.Issuer)
	}
	return state.NewIssuedAmountFromValue(balance.IOU().Mantissa(), balance.IOU().Exponent(), amount.Currency, amount.Issuer)
}
