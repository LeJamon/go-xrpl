package amm

import (
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

// isXRPAsset reports whether an asset names XRP. XRP is encoded as either an
// empty currency or the ISO code "XRP".
func isXRPAsset(asset tx.Asset) bool {
	return !asset.IsMPT() && (asset.Currency == "" || asset.Currency == "XRP")
}

// zeroIOU returns a zero-valued issued amount with no currency or issuer,
// the neutral element AMM Number arithmetic accumulates into.
func zeroIOU() tx.Amount {
	return state.NewIssuedAmountFromValue(0, -100, "", "")
}

// matchesAssetByIssue checks if two Assets represent the same issue.
// Handles XRP being represented as either "" or "XRP" for currency.
func matchesAssetByIssue(a, b tx.Asset) bool {
	if a.IsMPT() || b.IsMPT() {
		return a.IsMPT() && b.IsMPT() && strings.EqualFold(a.MPTIssuanceID, b.MPTIssuanceID)
	}
	if isXRPAsset(a) && isXRPAsset(b) {
		return true
	}
	return a.Currency == b.Currency && a.Issuer == b.Issuer
}

// matchesAsset checks if an Amount matches an Asset
// Handles XRP being represented as either "" or "XRP" for currency
func matchesAsset(amt *tx.Amount, asset tx.Asset) bool {
	if amt == nil {
		return false
	}
	if amt.IsMPT() || asset.IsMPT() {
		return amt.IsMPT() && asset.IsMPT() &&
			strings.EqualFold(amt.MPTIssuanceID(), asset.MPTIssuanceID)
	}
	// Check if both are XRP (currency empty or "XRP", no issuer)
	amtIsXRP := amt.IsNative() || amt.Currency == "" || amt.Currency == "XRP"
	if amtIsXRP && isXRPAsset(asset) {
		return true
	}
	// For IOUs, compare currency and issuer
	return amt.Currency == asset.Currency && amt.Issuer == asset.Issuer
}

// accountReserve returns the total XRP reserve for an account owning
// ownerCount objects: ReserveBase + ownerCount * ReserveIncrement.
func accountReserve(config tx.EngineConfig, ownerCount uint32) uint64 {
	return config.ReserveBase + uint64(ownerCount)*config.ReserveIncrement
}

// insufficientLPTokenReserve reports whether the account lacks the XRP reserve
// for one additional owned object (the LP token trust line). It mirrors
// rippled's xrpLiquid(view, account, 1) <= 0 guard: the liquid balance, after
// reserving for ownerCount+1 objects, must be strictly positive — equality
// fails. The account balance read from the unmodified Preclaim view is already
// the pre-fee balance, matching rippled's preclaim.
// Reference: rippled AMMCreate.cpp:145-159, AMMDeposit.cpp:353-362
func insufficientLPTokenReserve(account *state.AccountRoot, config tx.EngineConfig) bool {
	reserve := accountReserve(config, account.OwnerCount+1)
	return int64(account.Balance)-int64(reserve) <= 0
}

// mapAmountsToAssetOrder returns (amt1, amt2) reordered so amt1 carries the
// issue of asset1 and amt2 the other. Either input may be nil. When neither
// amount matches asset1, the original order is preserved.
func mapAmountsToAssetOrder(amtA, amtB *tx.Amount, asset1 tx.Asset) (*tx.Amount, *tx.Amount) {
	if amtA != nil && matchesAsset(amtA, asset1) {
		return amtA, amtB
	}
	if amtB != nil && matchesAsset(amtB, asset1) {
		return amtB, amtA
	}
	return amtA, amtB
}

// zeroAmount returns a zero amount for the given asset
func zeroAmount(asset tx.Asset) tx.Amount {
	if isXRPAsset(asset) {
		return state.NewXRPAmountFromInt(0)
	}
	if asset.IsMPT() {
		return mptAmount(asset, 0)
	}
	return state.NewIssuedAmountFromValue(0, -100, asset.Currency, asset.Issuer)
}

func mptAmount(asset tx.Asset, value int64) tx.Amount {
	issuer := asset.Issuer
	if issuer == "" {
		if id, err := mptutil.DecodeID(asset.MPTIssuanceID); err == nil {
			issuer, _ = state.EncodeAccountID(mptutil.Issuer(id))
		}
	}
	return state.NewMPTAmountWithIssuanceID(value, issuer, asset.MPTIssuanceID)
}

func mptValue(amount tx.Amount) (int64, bool) {
	if !amount.IsMPT() {
		return 0, false
	}
	return amount.MPTRaw()
}

func compareAmounts(a, b tx.Amount) int {
	if av, aok := mptValue(a); aok {
		if bv, bok := mptValue(b); bok {
			switch {
			case av < bv:
				return -1
			case av > bv:
				return 1
			default:
				return 0
			}
		}
	}
	return a.Compare(b)
}

// compareAccountIDs compares two account IDs lexicographically.
func compareAccountIDs(a, b [20]byte) int {
	return state.CompareAccountIDs(a, b)
}

// encodeAccountID encodes a 20-byte account ID to an XRPL address string.
func encodeAccountID(accountID [20]byte) (string, error) {
	return state.EncodeAccountID(accountID)
}

// minAmountIOU returns the smaller of two amounts compared in IOU space.
func minAmountIOU(a, b tx.Amount) tx.Amount {
	if compareAmounts(a, b) < 0 {
		return a
	}
	return b
}

// maxAmount returns the larger of two amounts.
// Assumes both amounts are of the same type (both XRP or same IOU).
func maxAmount(a, b tx.Amount) tx.Amount {
	if compareAmounts(a, b) > 0 {
		return a
	}
	return b
}

// isGreater returns true if a > b
func isGreater(a, b tx.Amount) bool {
	return compareAmounts(a, b) > 0
}

// isGreaterOrEqual returns true if a >= b
func isGreaterOrEqual(a, b tx.Amount) bool {
	return compareAmounts(a, b) >= 0
}

// isLessOrEqual returns true if a <= b
func isLessOrEqual(a, b tx.Amount) bool {
	return compareAmounts(a, b) <= 0
}

// withinRelativeDistance checks if two amounts are within relative distance dist.
// Returns true if calc == req, or (max - min) / max < dist.
// Reference: rippled AMMHelpers.h withinRelativeDistance
func withinRelativeDistance(math numberMath, calc, req tx.Amount, dist state.XRPLNumber) bool {
	if calc.Compare(req) == 0 {
		return true
	}

	var minAmount, maxAmount tx.Amount
	if calc.Compare(req) < 0 {
		minAmount = calc
		maxAmount = req
	} else {
		minAmount = req
		maxAmount = calc
	}

	diff, _ := maxAmount.Sub(minAmount)
	ratio := math.div(math.fromAmount(diff), math.fromAmount(maxAmount), state.RoundToNearest)
	return ratio.Cmp(dist) < 0
}

// isOnlyLiquidityProvider reports whether lpAccount is the sole liquidity
// provider of the AMM identified by (lptCurrency, ammAccountID). It walks the
// AMM pseudo-account's owner directory: the only provider holds exactly one
// LPToken trust line, and the directory must contain only the AMM object, that
// LPToken line, and the one or two asset trust lines. Any LPToken trust line to
// a different account means there is a second provider (false). A structurally
// impossible directory yields tecINTERNAL.
func isOnlyLiquidityProvider(view tx.LedgerView, lptCurrency string, ammAccountID, lpAccountID [20]byte) (bool, ter.Result) {
	ammAccountAddr, err := encodeAccountID(ammAccountID)
	if err != nil {
		return false, ter.TecINTERNAL
	}
	lpAccountAddr, err := encodeAccountID(lpAccountID)
	if err != nil {
		return false, ter.TecINTERNAL
	}

	var nLPTokenTrustLines, nIOUTrustLines, nMPT uint8
	hasAMM := false

	ownerDirKey := keylet.OwnerDir(ammAccountID)
	rootData, err := view.Read(ownerDirKey)
	if err != nil || rootData == nil {
		return false, ter.TecINTERNAL
	}
	currentPage, err := state.ParseDirectoryNode(rootData)
	if err != nil {
		return false, ter.TecINTERNAL
	}

	// At most three trust lines plus one AMM object, so ten pages is ample.
	for limit := 10; limit >= 1; limit-- {
		for _, key := range currentPage.Indexes {
			itemData, err := view.Read(keylet.Keylet{Key: key})
			if err != nil || itemData == nil {
				return false, ter.TecINTERNAL
			}
			entryType, err := state.GetLedgerEntryType(itemData)
			if err != nil {
				return false, ter.TecINTERNAL
			}

			if entry.Type(entryType) == entry.TypeAMM {
				if hasAMM {
					return false, ter.TecINTERNAL
				}
				hasAMM = true
				continue
			}
			if entry.Type(entryType) == entry.TypeMPToken {
				if nMPT++; nMPT > 2 {
					return false, ter.TecINTERNAL
				}
				continue
			}
			if entry.Type(entryType) != entry.TypeRippleState {
				return false, ter.TecINTERNAL
			}

			rs, err := state.ParseRippleState(itemData)
			if err != nil {
				return false, ter.TecINTERNAL
			}

			isLPTrustline := rs.LowLimit.Issuer == lpAccountAddr ||
				rs.HighLimit.Issuer == lpAccountAddr
			isLPTokenTrustline := isLPTokenIssue(rs.LowLimit, lptCurrency, ammAccountAddr) ||
				isLPTokenIssue(rs.HighLimit, lptCurrency, ammAccountAddr)

			switch {
			case isLPTrustline:
				if isLPTokenTrustline {
					if nLPTokenTrustLines++; nLPTokenTrustLines > 1 {
						return false, ter.TecINTERNAL
					}
				} else if nIOUTrustLines++; nIOUTrustLines > 2 {
					return false, ter.TecINTERNAL
				}
			case isLPTokenTrustline:
				return false, ter.TesSUCCESS
			default:
				if nIOUTrustLines++; nIOUTrustLines > 2 {
					return false, ter.TecINTERNAL
				}
			}
		}

		if currentPage.IndexNext == 0 {
			if nLPTokenTrustLines != 1 || nIOUTrustLines == 0 && nMPT == 0 ||
				nIOUTrustLines > 2 || nMPT > 2 || nIOUTrustLines+nMPT > 2 {
				return false, ter.TecINTERNAL
			}
			return true, ter.TesSUCCESS
		}

		pageData, err := view.Read(keylet.DirPage(ownerDirKey.Key, currentPage.IndexNext))
		if err != nil || pageData == nil {
			return false, ter.TecINTERNAL
		}
		currentPage, err = state.ParseDirectoryNode(pageData)
		if err != nil {
			return false, ter.TecINTERNAL
		}
	}
	return false, ter.TecINTERNAL
}

// isLPTokenIssue reports whether a trust-line limit's issue is the AMM's LP
// token issue (LP token currency issued by the AMM pseudo-account).
func isLPTokenIssue(limit state.Amount, lptCurrency, ammAccountAddr string) bool {
	return limit.Currency == lptCurrency && limit.Issuer == ammAccountAddr
}
