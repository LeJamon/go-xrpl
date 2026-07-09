package invariants

// vault.go — ValidVault invariant, ported from rippled InvariantCheck.cpp
// (ValidVault) at the 3.1.3 state.
//
// It reconciles the balance movements of a single-asset-vault transaction: a
// deposit trades assets for freshly minted shares, a withdrawal and a clawback
// trade shares back for assets, and a set touches neither. The checker verifies
// that the asset movement, the share issuance movement, and the vault's own
// AssetsTotal / AssetsAvailable / LossUnrealized totals all agree, that the
// vault's immutable data never changes, and that only loan transactions may move
// LossUnrealized. Balance deltas are rounded to the coarsest asset scale before
// comparison so IOU dust cannot trip the check.
//
// Enforcement mirrors rippled: the numeric machinery always runs when a vault
// object is touched, but a detected violation only fails the transaction when
// featureSingleAssetVault is enabled (`return !enforce`). Vault objects can only
// exist under SingleAssetVault, so in practice enforce is always true here.
//
// Asset-balance reconciliation scope. rippled looks up a vault asset movement at
// keylet::line(account, assetIssuer) for an IOU — the trust line between the
// account and the asset's issuer, because rippled's vault moves the asset by
// rippling through the issuer (accountSend → rippleSend). go-xrpl's vault
// transactors instead credit a direct account↔pseudo trust line, so an IOU
// movement lands at a different ledger key. That is a pre-existing divergence in
// the vault asset-movement path, not in this checker; until it is aligned, the
// asset-balance delta reconciliation here runs only for integral assets (native
// XRP and MPT), where go-xrpl and rippled agree on the ledger key. The
// structural, bounds, and share-issuance invariants run for every asset type.

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
)

// Loan transaction type codes not yet named in the protocol package.
const (
	ttLoanSet    TxType = 80
	ttLoanManage TxType = 82
	ttLoanPay    TxType = 84
)

// vvMaxMPTokenAmount is the default share-supply cap (2^63-1) when a share
// issuance omits MaximumAmount.
const vvMaxMPTokenAmount uint64 = 0x7FFFFFFFFFFFFFFF

// vvAsset is a vault's underlying asset: native XRP, an MPT, or an IOU.
type vvAsset struct {
	isXRP    bool
	isMPT    bool
	mptID    [24]byte
	currency string
	issuer   [20]byte // IOU issuer
}

func (a vvAsset) integral() bool { return a.isXRP || a.isMPT }

// issuerID returns the asset's issuer: the IOU issuer, or the account embedded in
// an MPT issuance ID. Native XRP has no issuer (zero value).
func (a vvAsset) issuerID() [20]byte {
	if a.isMPT {
		var id [20]byte
		copy(id[:], a.mptID[4:])
		return id
	}
	return a.issuer
}

func (a vvAsset) equal(b vvAsset) bool {
	return a.isXRP == b.isXRP && a.isMPT == b.isMPT &&
		a.mptID == b.mptID && a.currency == b.currency && a.issuer == b.issuer
}

// scaleOf returns the decimal scale of n for this asset (0 for integral assets).
func (a vvAsset) scaleOf(n state.XRPLNumber) int {
	return n.AssetExponent(a.integral(), state.RoundToNearest)
}

// round snaps n to this asset's precision at the given decimal scale.
func (a vvAsset) round(n state.XRPLNumber, scale int) state.XRPLNumber {
	return n.RoundToAssetScale(a.integral(), scale, state.RoundToNearest)
}

func (a vvAsset) makeDelta(before, after state.XRPLNumber) vvDelta {
	return vvDelta{delta: after.Sub(before), scale: max(a.scaleOf(after), a.scaleOf(before))}
}

// vvVault is the checker's view of a Vault ledger entry.
type vvVault struct {
	key             [32]byte
	asset           vvAsset
	pseudoID        [20]byte
	owner           [20]byte
	shareMPTID      [24]byte
	assetsTotal     state.XRPLNumber
	assetsAvailable state.XRPLNumber
	assetsMaximum   state.XRPLNumber
	lossUnrealized  state.XRPLNumber
}

func (v vvVault) holdsNoAssets() bool {
	return v.assetsAvailable.IsZero() && v.assetsTotal.IsZero()
}

// vvShares is the checker's view of a share MPTokenIssuance.
type vvShares struct {
	shareMPTID  [24]byte
	sharesTotal uint64
	sharesMax   uint64
}

// vvDelta is a balance change and the coarsest decimal scale it should round to.
type vvDelta struct {
	delta state.XRPLNumber
	scale int
}

// checkValidVault is the ValidVault entry point.
func checkValidVault(tx Transaction, result Result, fee uint64, entries []InvariantEntry, view ReadView, rules *amendment.Rules) *InvariantViolation {
	if result != TesSUCCESS {
		return nil
	}

	c := &vvChecker{deltas: map[[32]byte]vvDelta{}, view: view, fee: fee, txType: tx.TxType()}
	if flat, err := tx.Flatten(); err == nil {
		c.flat = flat
	}
	c.txAccountID, _ = state.DecodeAccountID(tx.TxAccount())
	c.visit(entries)

	msg := c.finalize()
	if msg == "" {
		return nil
	}
	// return !enforce: a violation only fails the tx when SingleAssetVault is on.
	if rules == nil || !rules.Enabled(amendment.FeatureSingleAssetVault) {
		return nil
	}
	return &InvariantViolation{Name: "ValidVault", Message: msg}
}

type vvChecker struct {
	beforeVault  []vvVault
	afterVault   []vvVault
	beforeShares []vvShares
	afterShares  []vvShares
	deltas       map[[32]byte]vvDelta

	view        ReadView
	fee         uint64
	txType      TxType
	flat        map[string]any
	txAccountID [20]byte
}

// visit is the visitEntry phase: it classifies every modified entry and records
// the vault objects, share issuances, and balance deltas the finalize phase
// reconciles.
func (c *vvChecker) visit(entries []InvariantEntry) {
	for _, e := range entries {
		delta := vvZero()
		scale := 0
		sign := 0

		if e.Before != nil {
			if bf, err := decodeEntry(e.Before); err == nil {
				switch e.EntryType {
				case "Vault":
					if v, ok := vvMakeVault(bf, e.Key); ok {
						c.beforeVault = append(c.beforeVault, v)
					}
				case "MPTokenIssuance":
					c.beforeShares = append(c.beforeShares, vvMakeShares(bf))
					delta = vvNumFromU64(vvU64(bf, "OutstandingAmount"))
					scale, sign = 0, 1
				case "MPToken":
					delta = vvNumFromU64(vvU64(bf, "MPTAmount"))
					scale, sign = 0, -1
				case "AccountRoot":
					delta = vvNumFromI64(vvI64(bf, "Balance"))
					scale, sign = 0, -1
				case "RippleState":
					amt := vvBalanceAmount(bf)
					delta = vvNumFromAmount(amt)
					scale, sign = amt.Exponent(), -1
				}
			}
		}

		if !e.IsDelete && e.After != nil {
			if af, err := decodeEntry(e.After); err == nil {
				switch e.EntryType {
				case "Vault":
					if v, ok := vvMakeVault(af, e.Key); ok {
						c.afterVault = append(c.afterVault, v)
					}
				case "MPTokenIssuance":
					c.afterShares = append(c.afterShares, vvMakeShares(af))
					delta = delta.Sub(vvNumFromU64(vvU64(af, "OutstandingAmount")))
					scale, sign = 0, 1
				case "MPToken":
					delta = delta.Sub(vvNumFromU64(vvU64(af, "MPTAmount")))
					scale, sign = 0, -1
				case "AccountRoot":
					delta = delta.Sub(vvNumFromI64(vvI64(af, "Balance")))
					scale, sign = 0, -1
				case "RippleState":
					amt := vvBalanceAmount(af)
					delta = delta.Sub(vvNumFromAmount(amt))
					if amt.Exponent() > scale {
						scale = amt.Exponent()
					}
					sign = -1
				}
			}
		}

		if sign != 0 {
			if sign < 0 {
				delta = delta.Negate()
			}
			c.deltas[e.Key] = vvDelta{delta: delta, scale: scale}
		}
	}
}

// deltaAssets returns the change in id's holding of vaultAsset, if any.
func (c *vvChecker) deltaAssets(vaultAsset vvAsset, id [20]byte) (vvDelta, bool) {
	if vaultAsset.isXRP {
		d, ok := c.deltas[keylet.Account(id).Key]
		return d, ok
	}
	if vaultAsset.isMPT {
		d, ok := c.deltas[keylet.MPTokenByID(vaultAsset.mptID, id).Key]
		return d, ok
	}
	d, ok := c.deltas[keylet.Line(id, vaultAsset.issuer, vaultAsset.currency).Key]
	if !ok {
		return vvDelta{}, false
	}
	// The RippleState balance is stored from the low account's perspective; flip
	// it to id's perspective when id is the high account.
	if bytes.Compare(id[:], vaultAsset.issuer[:]) > 0 {
		d.delta = d.delta.Negate()
	}
	return d, true
}

// deltaAssetsTxAccount returns the tx account's asset delta, adding the fee back
// for a native asset (the fee left the balance but is not a vault movement).
func (c *vvChecker) deltaAssetsTxAccount(vaultAsset vvAsset) (vvDelta, bool) {
	ret, ok := c.deltaAssets(vaultAsset, c.txAccountID)
	if !ok || !vaultAsset.isXRP {
		return ret, ok
	}
	if d, present := c.flatAccountID("Delegate"); present && d != c.txAccountID {
		return ret, ok
	}
	ret.delta = ret.delta.Add(vvNumFromI64(int64(c.fee)))
	if ret.delta.IsZero() {
		return vvDelta{}, false
	}
	return ret, true
}

// deltaShares returns the change in id's share holding for the vault, reading the
// share issuance's OutstandingAmount for the pseudo-account and a holder MPToken
// otherwise.
func (c *vvChecker) deltaShares(afterVault vvVault, id [20]byte) (vvDelta, bool) {
	var key [32]byte
	if id == afterVault.pseudoID {
		key = keylet.MPTIssuance(afterVault.shareMPTID).Key
	} else {
		key = keylet.MPTokenByID(afterVault.shareMPTID, id).Key
	}
	d, ok := c.deltas[key]
	return d, ok
}

func (c *vvChecker) flatStr(key string) (string, bool) {
	v, ok := c.flat[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok
}

func (c *vvChecker) flatAccountID(key string) ([20]byte, bool) {
	s, ok := c.flatStr(key)
	if !ok || s == "" {
		return [20]byte{}, false
	}
	id, err := state.DecodeAccountID(s)
	if err != nil {
		return [20]byte{}, false
	}
	return id, true
}

func (c *vvChecker) flatAmount(key string) (state.Amount, bool) {
	v, ok := c.flat[key]
	if !ok {
		return state.Amount{}, false
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return state.Amount{}, false
	}
	amt, err := state.AmountFromJSON(raw)
	if err != nil {
		return state.Amount{}, false
	}
	return amt, true
}

// finalize is the finalize phase; it returns "" when every invariant holds, or
// the first violation message otherwise. Every rippled failure path funnels
// through `return !enforce`, so reporting the first failure is equivalent to
// rippled's accumulate-then-fail for the transaction result.
func (c *vvChecker) finalize() string {
	if len(c.afterVault) == 0 && len(c.beforeVault) == 0 {
		if hasPrivilege(c.txType, mustModifyVault) {
			return "vault operation succeeded without modifying a vault"
		}
		return "" // not a vault operation
	} else if !(hasPrivilege(c.txType, mustModifyVault) || hasPrivilege(c.txType, mayModifyVault)) {
		return "vault updated by a wrong transaction type"
	}

	if len(c.beforeVault) > 1 || len(c.afterVault) > 1 {
		return "vault operation updated more than a single vault"
	}

	// ttVAULT_DELETE is the only vault-modifying transaction without an "after"
	// vault, so it is handled first.
	if len(c.afterVault) == 0 {
		return c.finalizeDelete()
	} else if c.txType == protocol.TxTypeVaultDelete {
		return "vault deletion succeeded without deleting a vault"
	}

	return c.finalizeUpdate()
}

func (c *vvChecker) finalizeDelete() string {
	beforeVault := c.beforeVault[0]

	deleted, ok := c.findShares(c.beforeShares, beforeVault.shareMPTID)
	if !ok {
		return "deleted vault must also delete shares"
	}
	if deleted.sharesTotal != 0 {
		return "deleted vault must have no shares outstanding"
	}
	if !beforeVault.assetsTotal.IsZero() {
		return "deleted vault must have no assets outstanding"
	}
	if !beforeVault.assetsAvailable.IsZero() {
		return "deleted vault must have no assets available"
	}
	return ""
}

func (c *vvChecker) finalizeUpdate() string {
	afterVault := c.afterVault[0]

	updatedShares, hasUpdated := c.findShares(c.afterShares, afterVault.shareMPTID)
	if !hasUpdated {
		if data, err := c.view.Read(keylet.MPTIssuance(afterVault.shareMPTID)); err == nil && data != nil {
			if m, derr := decodeEntry(data); derr == nil {
				updatedShares, hasUpdated = vvMakeShares(m), true
			}
		}
	}

	// Universal checks.
	if len(c.beforeVault) != 0 {
		bv := c.beforeVault[0]
		if !afterVault.asset.equal(bv.asset) || afterVault.pseudoID != bv.pseudoID ||
			afterVault.shareMPTID != bv.shareMPTID {
			return "violation of vault immutable data"
		}
	}

	if !hasUpdated {
		return "updated vault must have shares"
	}

	if updatedShares.sharesTotal == 0 {
		if !afterVault.assetsTotal.IsZero() {
			return "updated zero sized vault must have no assets outstanding"
		}
		if !afterVault.assetsAvailable.IsZero() {
			return "updated zero sized vault must have no assets available"
		}
	} else if updatedShares.sharesTotal > updatedShares.sharesMax {
		return "updated shares must not exceed maximum"
	}

	if afterVault.assetsAvailable.Signum() < 0 {
		return "assets available must be positive"
	}
	if afterVault.assetsAvailable.Cmp(afterVault.assetsTotal) > 0 {
		return "assets available must not be greater than assets outstanding"
	} else if afterVault.lossUnrealized.Cmp(afterVault.assetsTotal.Sub(afterVault.assetsAvailable)) > 0 {
		return "loss unrealized must not exceed the difference between assets outstanding and available"
	}
	if afterVault.assetsTotal.Signum() < 0 {
		return "assets outstanding must be positive"
	}
	if afterVault.assetsMaximum.Signum() < 0 {
		return "assets maximum must be positive"
	}

	if len(c.beforeVault) == 0 && c.txType != protocol.TxTypeVaultCreate {
		return "vault created by a wrong transaction type"
	}

	if len(c.beforeVault) != 0 &&
		afterVault.lossUnrealized.Cmp(c.beforeVault[0].lossUnrealized) != 0 &&
		c.txType != ttLoanManage && c.txType != ttLoanPay {
		return "vault transaction must not change loss unrealized"
	}

	var beforeShares *vvShares
	if len(c.beforeVault) != 0 {
		if s, ok := c.findShares(c.beforeShares, c.beforeVault[0].shareMPTID); ok {
			beforeShares = &s
		}
	}

	if beforeShares == nil &&
		(c.txType == protocol.TxTypeVaultDeposit || c.txType == protocol.TxTypeVaultWithdraw || c.txType == protocol.TxTypeVaultClawback) {
		return "vault operation succeeded without updating shares"
	}

	switch c.txType {
	case protocol.TxTypeVaultCreate:
		return c.finalizeCreate(afterVault, updatedShares)
	case protocol.TxTypeVaultSet:
		return c.finalizeSet(afterVault, updatedShares, beforeShares)
	case protocol.TxTypeVaultDeposit:
		return c.finalizeDeposit(afterVault, updatedShares)
	case protocol.TxTypeVaultWithdraw:
		return c.finalizeWithdraw(afterVault)
	case protocol.TxTypeVaultClawback:
		return c.finalizeClawback(afterVault, beforeShares)
	case ttLoanSet, ttLoanManage, ttLoanPay:
		return "" // reconciliation TBD in rippled
	default:
		return ""
	}
}

func (c *vvChecker) finalizeCreate(afterVault vvVault, updatedShares vvShares) string {
	if len(c.beforeVault) != 0 {
		return "create operation must not have updated a vault"
	}
	if !afterVault.assetsAvailable.IsZero() || !afterVault.assetsTotal.IsZero() ||
		!afterVault.lossUnrealized.IsZero() || updatedShares.sharesTotal != 0 {
		return "created vault must be empty"
	}
	sharesIssuer := vvMPTIDIssuer(updatedShares.shareMPTID)
	if afterVault.pseudoID != sharesIssuer {
		return "shares issuer and vault pseudo-account must be the same"
	}
	data, err := c.view.Read(keylet.Account(sharesIssuer))
	if err != nil || data == nil {
		return "shares issuer must exist"
	}
	ar, perr := state.ParseAccountRoot(data)
	if perr != nil || ar == nil {
		return "shares issuer must exist"
	}
	if !ar.IsPseudoAccount() {
		return "shares issuer must be a pseudo-account"
	}
	if !ar.HasVaultID() || ar.VaultID != afterVault.key {
		return "shares issuer pseudo-account must point back to the vault"
	}
	return ""
}

func (c *vvChecker) finalizeSet(afterVault vvVault, updatedShares vvShares, beforeShares *vvShares) string {
	beforeVault := c.beforeVault[0]
	vaultAsset := afterVault.asset

	if vaultAsset.integral() {
		if _, moved := c.deltaAssets(vaultAsset, afterVault.pseudoID); moved {
			return "set must not change vault balance"
		}
	}
	if beforeVault.assetsTotal.Cmp(afterVault.assetsTotal) != 0 {
		return "set must not change assets outstanding"
	}
	if afterVault.assetsMaximum.Signum() > 0 && afterVault.assetsTotal.Cmp(afterVault.assetsMaximum) > 0 {
		return "set assets outstanding must not exceed assets maximum"
	}
	if beforeVault.assetsAvailable.Cmp(afterVault.assetsAvailable) != 0 {
		return "set must not change assets available"
	}
	if beforeShares != nil && beforeShares.sharesTotal != updatedShares.sharesTotal {
		return "set must not change shares outstanding"
	}
	return ""
}

func (c *vvChecker) finalizeDeposit(afterVault vvVault, updatedShares vvShares) string {
	beforeVault := c.beforeVault[0]
	vaultAsset := afterVault.asset

	if vaultAsset.integral() {
		if msg := c.reconcileDepositAssets(beforeVault, afterVault, vaultAsset); msg != "" {
			return msg
		}
	}

	if afterVault.assetsMaximum.Signum() > 0 && afterVault.assetsTotal.Cmp(afterVault.assetsMaximum) > 0 {
		return "deposit assets outstanding must not exceed assets maximum"
	}

	maybeAccDeltaShares, sok := c.deltaShares(afterVault, c.txAccountID)
	if !sok {
		return "deposit must change depositor shares"
	}
	if maybeAccDeltaShares.delta.Signum() <= 0 {
		return "deposit must increase depositor shares"
	}

	maybeVaultDeltaShares, vok := c.deltaShares(afterVault, afterVault.pseudoID)
	if !vok || maybeVaultDeltaShares.delta.IsZero() {
		return "deposit must change vault shares"
	}
	if maybeVaultDeltaShares.delta.Negate().Cmp(maybeAccDeltaShares.delta) != 0 {
		return "deposit must change depositor and vault shares by equal amount"
	}
	return ""
}

func (c *vvChecker) reconcileDepositAssets(beforeVault, afterVault vvVault, vaultAsset vvAsset) string {
	maybeVaultDeltaAssets, ok := c.deltaAssets(vaultAsset, afterVault.pseudoID)
	if !ok {
		return "deposit must change vault balance"
	}

	totalDelta := vaultAsset.makeDelta(beforeVault.assetsTotal, afterVault.assetsTotal)
	availableDelta := vaultAsset.makeDelta(beforeVault.assetsAvailable, afterVault.assetsAvailable)
	minScale := vvCoarsestScale(maybeVaultDeltaAssets, totalDelta, availableDelta)

	vaultDeltaAssets := vaultAsset.round(maybeVaultDeltaAssets.delta, minScale)
	txAmt, _ := c.flatAmount("Amount")
	txAmount := vaultAsset.round(vvNumFromAmount(txAmt), minScale)

	if vaultDeltaAssets.Cmp(txAmount) > 0 {
		return "deposit must not change vault balance by more than deposited amount"
	}
	if vaultDeltaAssets.Signum() <= 0 {
		return "deposit must increase vault balance"
	}

	issuerDeposit := !vaultAsset.isXRP && c.txAccountID == vaultAsset.issuerID()
	if !issuerDeposit {
		maybeAccDeltaAssets, aok := c.deltaAssetsTxAccount(vaultAsset)
		if !aok {
			return "deposit must change depositor balance"
		}
		localMinScale := max(minScale, vvCoarsestScale(maybeAccDeltaAssets))
		accountDeltaAssets := vaultAsset.round(maybeAccDeltaAssets.delta, localMinScale)
		localVaultDeltaAssets := vaultAsset.round(vaultDeltaAssets, localMinScale)
		if accountDeltaAssets.Signum() >= 0 {
			return "deposit must decrease depositor balance"
		}
		if localVaultDeltaAssets.Negate().Cmp(accountDeltaAssets) != 0 {
			return "deposit must change vault and depositor balance by equal amount"
		}
	}

	assetTotalDelta := vaultAsset.round(afterVault.assetsTotal.Sub(beforeVault.assetsTotal), minScale)
	if assetTotalDelta.Cmp(vaultDeltaAssets) != 0 {
		return "deposit and assets outstanding must add up"
	}
	assetAvailableDelta := vaultAsset.round(afterVault.assetsAvailable.Sub(beforeVault.assetsAvailable), minScale)
	if assetAvailableDelta.Cmp(vaultDeltaAssets) != 0 {
		return "deposit and assets available must add up"
	}
	return ""
}

func (c *vvChecker) finalizeWithdraw(afterVault vvVault) string {
	beforeVault := c.beforeVault[0]
	vaultAsset := afterVault.asset

	if vaultAsset.integral() {
		if msg := c.reconcileWithdrawAssets(beforeVault, afterVault, vaultAsset); msg != "" {
			return msg
		}
	}

	accountDeltaShares, sok := c.deltaShares(afterVault, c.txAccountID)
	if !sok {
		return "withdrawal must change depositor shares"
	}
	if accountDeltaShares.delta.Signum() >= 0 {
		return "withdrawal must decrease depositor shares"
	}

	vaultDeltaShares, vok := c.deltaShares(afterVault, afterVault.pseudoID)
	if !vok || vaultDeltaShares.delta.IsZero() {
		return "withdrawal must change vault shares"
	}
	if vaultDeltaShares.delta.Negate().Cmp(accountDeltaShares.delta) != 0 {
		return "withdrawal must change depositor and vault shares by equal amount"
	}
	return ""
}

func (c *vvChecker) reconcileWithdrawAssets(beforeVault, afterVault vvVault, vaultAsset vvAsset) string {
	maybeVaultDeltaAssets, ok := c.deltaAssets(vaultAsset, afterVault.pseudoID)
	if !ok {
		return "withdrawal must change vault balance"
	}

	totalDelta := vaultAsset.makeDelta(beforeVault.assetsTotal, afterVault.assetsTotal)
	availableDelta := vaultAsset.makeDelta(beforeVault.assetsAvailable, afterVault.assetsAvailable)
	minScale := vvCoarsestScale(maybeVaultDeltaAssets, totalDelta, availableDelta)

	vaultPseudoDeltaAssets := vaultAsset.round(maybeVaultDeltaAssets.delta, minScale)
	if vaultPseudoDeltaAssets.Signum() >= 0 {
		return "withdrawal must decrease vault balance"
	}

	dest := c.txAccountID
	destOverride, hasDest := c.flatAccountID("Destination")
	if hasDest {
		dest = destOverride
	}
	issuerWithdrawal := !vaultAsset.isXRP && dest == vaultAsset.issuerID()

	if !issuerWithdrawal {
		maybeAccDelta, accOK := c.deltaAssetsTxAccount(vaultAsset)
		var maybeOther vvDelta
		otherOK := false
		if hasDest && destOverride != c.txAccountID {
			maybeOther, otherOK = c.deltaAssets(vaultAsset, destOverride)
		}
		if accOK == otherOK {
			return "withdrawal must change one destination balance"
		}
		destinationDelta := maybeAccDelta
		if !accOK {
			destinationDelta = maybeOther
		}
		localMinScale := max(minScale, vvCoarsestScale(destinationDelta))
		roundedDestinationDelta := vaultAsset.round(destinationDelta.delta, localMinScale)
		if roundedDestinationDelta.Signum() <= 0 {
			return "withdrawal must increase destination balance"
		}
		localPseudoDeltaAssets := vaultAsset.round(vaultPseudoDeltaAssets, localMinScale)
		if localPseudoDeltaAssets.Negate().Cmp(roundedDestinationDelta) != 0 {
			return "withdrawal must change vault and destination balance by equal amount"
		}
	}

	assetTotalDelta := vaultAsset.round(afterVault.assetsTotal.Sub(beforeVault.assetsTotal), minScale)
	if assetTotalDelta.Cmp(vaultPseudoDeltaAssets) != 0 {
		return "withdrawal and assets outstanding must add up"
	}
	assetAvailableDelta := vaultAsset.round(afterVault.assetsAvailable.Sub(beforeVault.assetsAvailable), minScale)
	if assetAvailableDelta.Cmp(vaultPseudoDeltaAssets) != 0 {
		return "withdrawal and assets available must add up"
	}
	return ""
}

func (c *vvChecker) finalizeClawback(afterVault vvVault, beforeShares *vvShares) string {
	beforeVault := c.beforeVault[0]
	vaultAsset := afterVault.asset

	if vaultAsset.isXRP || vaultAsset.issuerID() != c.txAccountID {
		// The owner may force-burn shares of an empty vault with shares still out.
		if !(beforeShares != nil && beforeShares.sharesTotal > 0 &&
			beforeVault.holdsNoAssets() && beforeVault.owner == c.txAccountID) {
			return "clawback may only be performed by the asset issuer, or by the vault owner of an empty vault"
		}
	}

	if vaultAsset.integral() {
		if msg := c.reconcileClawbackAssets(beforeVault, afterVault, vaultAsset); msg != "" {
			return msg
		}
	}

	holderID, _ := c.flatAccountID("Holder")
	maybeAccountDeltaShares, hok := c.deltaShares(afterVault, holderID)
	if !hok {
		return "clawback must change holder shares"
	}
	if maybeAccountDeltaShares.delta.Signum() >= 0 {
		return "clawback must decrease holder shares"
	}

	vaultDeltaShares, vok := c.deltaShares(afterVault, afterVault.pseudoID)
	if !vok || vaultDeltaShares.delta.IsZero() {
		return "clawback must change vault shares"
	}
	if vaultDeltaShares.delta.Negate().Cmp(maybeAccountDeltaShares.delta) != 0 {
		return "clawback must change holder and vault shares by equal amount"
	}
	return ""
}

func (c *vvChecker) reconcileClawbackAssets(beforeVault, afterVault vvVault, vaultAsset vvAsset) string {
	maybeVaultDeltaAssets, ok := c.deltaAssets(vaultAsset, afterVault.pseudoID)
	if ok {
		totalDelta := vaultAsset.makeDelta(beforeVault.assetsTotal, afterVault.assetsTotal)
		availableDelta := vaultAsset.makeDelta(beforeVault.assetsAvailable, afterVault.assetsAvailable)
		minScale := vvCoarsestScale(maybeVaultDeltaAssets, totalDelta, availableDelta)
		vaultDeltaAssets := vaultAsset.round(maybeVaultDeltaAssets.delta, minScale)
		if vaultDeltaAssets.Signum() >= 0 {
			return "clawback must decrease vault balance"
		}
		assetsTotalDelta := vaultAsset.round(afterVault.assetsTotal.Sub(beforeVault.assetsTotal), minScale)
		if assetsTotalDelta.Cmp(vaultDeltaAssets) != 0 {
			return "clawback and assets outstanding must add up"
		}
		assetAvailableDelta := vaultAsset.round(afterVault.assetsAvailable.Sub(beforeVault.assetsAvailable), minScale)
		if assetAvailableDelta.Cmp(vaultDeltaAssets) != 0 {
			return "clawback and assets available must add up"
		}
	} else if !beforeVault.holdsNoAssets() {
		return "clawback must change vault balance"
	}
	return ""
}

func (c *vvChecker) findShares(list []vvShares, shareMPTID [24]byte) (vvShares, bool) {
	for _, s := range list {
		if s.shareMPTID == shareMPTID {
			return s, true
		}
	}
	return vvShares{}, false
}

// --- decode + numeric helpers ---

func vvZero() state.XRPLNumber {
	return state.NewXRPLNumberScaled(0, 0, state.MantissaScaleLarge, state.RoundToNearest)
}

func vvNumFromI64(v int64) state.XRPLNumber {
	return state.NewXRPLNumberScaled(v, 0, state.MantissaScaleLarge, state.RoundToNearest)
}

func vvNumFromU64(v uint64) state.XRPLNumber {
	return vvNumFromI64(int64(v))
}

func vvNumFromAmount(a state.Amount) state.XRPLNumber {
	if a.IsNative() {
		return vvNumFromI64(a.Drops())
	}
	if a.IsMPT() {
		n, _ := strconv.ParseInt(a.Value(), 10, 64)
		return vvNumFromI64(n)
	}
	return state.NewXRPLNumberScaled(a.Mantissa(), a.Exponent(), state.MantissaScaleLarge, state.RoundToNearest)
}

// vvCoarsestScale returns the largest scale among the deltas, or 0 when none.
func vvCoarsestScale(ds ...vvDelta) int {
	if len(ds) == 0 {
		return 0
	}
	m := ds[0].scale
	for _, d := range ds[1:] {
		if d.scale > m {
			m = d.scale
		}
	}
	return m
}

func vvMPTIDIssuer(id [24]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return issuer
}

func vvMakeVault(m map[string]any, key [32]byte) (vvVault, bool) {
	v := vvVault{key: key}
	am, ok := m["Asset"].(map[string]any)
	if !ok {
		return v, false
	}
	v.asset = vvAssetFromMap(am)
	pseudo, err := state.DecodeAccountID(vvStr(m, "Account"))
	if err != nil {
		return v, false
	}
	v.pseudoID = pseudo
	if owner, oerr := state.DecodeAccountID(vvStr(m, "Owner")); oerr == nil {
		v.owner = owner
	}
	if b, herr := hex.DecodeString(vvStr(m, "ShareMPTID")); herr == nil && len(b) == 24 {
		copy(v.shareMPTID[:], b)
	}
	v.assetsTotal = vvNumber(m, "AssetsTotal")
	v.assetsAvailable = vvNumber(m, "AssetsAvailable")
	v.assetsMaximum = vvNumber(m, "AssetsMaximum")
	v.lossUnrealized = vvNumber(m, "LossUnrealized")
	return v, true
}

func vvAssetFromMap(m map[string]any) vvAsset {
	if mptID, ok := m["mpt_issuance_id"].(string); ok {
		a := vvAsset{isMPT: true}
		if b, err := hex.DecodeString(mptID); err == nil && len(b) == 24 {
			copy(a.mptID[:], b)
		}
		return a
	}
	cur, _ := m["currency"].(string)
	iss, _ := m["issuer"].(string)
	if isNativeXRPCurrency(cur) && iss == "" {
		return vvAsset{isXRP: true}
	}
	a := vvAsset{currency: cur}
	if id, err := state.DecodeAccountID(iss); err == nil {
		a.issuer = id
	}
	return a
}

func vvMakeShares(m map[string]any) vvShares {
	seq := u32Field(m, "Sequence")
	issuer, _ := state.DecodeAccountID(vvStr(m, "Issuer"))
	s := vvShares{
		shareMPTID:  keylet.MakeMPTID(seq, issuer),
		sharesTotal: vvU64(m, "OutstandingAmount"),
		sharesMax:   vvMaxMPTokenAmount,
	}
	if maxAmt, ok := vvU64Present(m, "MaximumAmount"); ok {
		s.sharesMax = maxAmt
	}
	return s
}

// vvBalanceAmount parses a RippleState Balance (an IOU Amount) from its decoded
// map form.
func vvBalanceAmount(m map[string]any) state.Amount {
	v, ok := m["Balance"]
	if !ok {
		return state.Amount{}
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return state.Amount{}
	}
	amt, err := state.AmountFromJSON(raw)
	if err != nil {
		return state.Amount{}
	}
	return amt
}

func vvStr(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

// vvU64 reads a decimal (sMD_BaseTen) UInt64 field, 0 when absent.
func vvU64(m map[string]any, key string) uint64 {
	v, _ := vvU64Present(m, key)
	return v
}

func vvU64Present(m map[string]any, key string) (uint64, bool) {
	switch v := m[key].(type) {
	case string:
		if v == "" {
			return 0, false
		}
		n, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	case float64:
		return uint64(v), true
	case uint64:
		return v, true
	case int:
		return uint64(v), true
	}
	return 0, false
}

// vvI64 reads a native XRP drops field (rendered as a decimal string), 0 when
// absent.
func vvI64(m map[string]any, key string) int64 {
	switch v := m[key].(type) {
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	}
	return 0
}

func vvNumber(m map[string]any, key string) state.XRPLNumber {
	return vvParseNumber(vvStr(m, key))
}

func vvParseNumber(s string) state.XRPLNumber {
	if s == "" || s == "0" {
		return vvZero()
	}
	num := &types.Number{}
	b, err := num.FromJSON(s)
	if err != nil || len(b) < 12 {
		return vvZero()
	}
	mant := int64(binary.BigEndian.Uint64(b[:8]))
	exp := int32(binary.BigEndian.Uint32(b[8:12]))
	return state.NewXRPLNumberScaled(mant, int(exp), state.MantissaScaleLarge, state.RoundToNearest)
}
