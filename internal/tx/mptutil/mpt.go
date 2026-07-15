package mptutil

import (
	"encoding/hex"
	"errors"
	"math"
	"math/big"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	tx "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/credential"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const RateOne uint32 = 1_000_000_000

const maxAssetCheckDepth = 5

type AuthType uint8

const (
	StrongAuth AuthType = iota
	WeakAuth
	LegacyAuth
)

var ErrInvalidID = errors.New("invalid MPTokenIssuanceID")

type balanceHookMPT interface {
	BalanceHookMPT(account [20]byte, id [24]byte, amount int64) int64
}

type balanceHookSelfIssueMPT interface {
	BalanceHookSelfIssueMPT(id [24]byte, amount int64) int64
}

type creditHookMPT interface {
	CreditHookMPT(
		sender, receiver [20]byte,
		id [24]byte,
		amount, preCreditHolderBalance uint64,
		preCreditIssuerBalance int64,
	)
}

type issuerSelfDebitHookMPT interface {
	IssuerSelfDebitHookMPT(id [24]byte, amount uint64, origBalance int64)
}

type ownerCountHook interface {
	AdjustOwnerCount(account [20]byte, current, next uint32)
}

func DecodeID(value string) ([24]byte, error) {
	var id [24]byte
	b, err := hex.DecodeString(value)
	if err != nil || len(b) != len(id) {
		return id, ErrInvalidID
	}
	copy(id[:], b)
	return id, nil
}

func EncodeID(id [24]byte) string {
	return strings.ToUpper(hex.EncodeToString(id[:]))
}

func Issuer(id [24]byte) [20]byte {
	var issuer [20]byte
	copy(issuer[:], id[4:])
	return issuer
}

func ReadIssuance(view state.LedgerView, id [24]byte) (*state.MPTokenIssuanceData, keylet.Keylet, ter.Result) {
	k := keylet.MPTIssuance(id)
	raw, err := view.Read(k)
	if err != nil {
		return nil, k, ter.TefINTERNAL
	}
	if raw == nil {
		return nil, k, ter.TecOBJECT_NOT_FOUND
	}
	issuance, err := state.ParseMPTokenIssuance(raw)
	if err != nil {
		return nil, k, ter.TefINTERNAL
	}
	return issuance, k, ter.TesSUCCESS
}

func ReadHolding(view state.LedgerView, id [24]byte, account [20]byte) (*state.MPTokenData, keylet.Keylet, ter.Result) {
	k := keylet.MPTokenByID(id, account)
	raw, err := view.Read(k)
	if err != nil {
		return nil, k, ter.TefINTERNAL
	}
	if raw == nil {
		return nil, k, ter.TecNO_AUTH
	}
	token, err := state.ParseMPToken(raw)
	if err != nil {
		return nil, k, ter.TefINTERNAL
	}
	return token, k, ter.TesSUCCESS
}

func MaximumAmount(issuance *state.MPTokenIssuanceData) uint64 {
	if issuance.MaximumAmount != nil {
		return *issuance.MaximumAmount
	}
	return entry.MaxMPTokenAmount
}

func AvailableAmount(issuance *state.MPTokenIssuanceData) uint64 {
	maximum := MaximumAmount(issuance)
	if issuance.OutstandingAmount >= maximum {
		return 0
	}
	return maximum - issuance.OutstandingAmount
}

func IsGlobalFrozen(view state.LedgerView, id [24]byte) bool {
	issuance, _, result := ReadIssuance(view, id)
	return result == ter.TesSUCCESS && issuance.Flags&entry.LsfMPTLocked != 0
}

func IsIndividualFrozen(view state.LedgerView, id [24]byte, account [20]byte) bool {
	if account == Issuer(id) {
		return false
	}
	token, _, result := ReadHolding(view, id, account)
	return result == ter.TesSUCCESS && token.Flags&entry.LsfMPTLocked != 0
}

func IsFrozen(view state.LedgerView, id [24]byte, account [20]byte) bool {
	return isFrozen(view, id, account, 0)
}

func isFrozen(view state.LedgerView, id [24]byte, account [20]byte, depth uint8) bool {
	return IsGlobalFrozen(view, id) || IsIndividualFrozen(view, id, account) ||
		isVaultPseudoAccountFrozen(view, id, account, depth)
}

func isVaultPseudoAccountFrozen(view state.LedgerView, id [24]byte, account [20]byte, depth uint8) bool {
	rules := view.Rules()
	if rules == nil || !rules.Enabled(amendment.FeatureSingleAssetVault) {
		return false
	}
	if depth >= maxAssetCheckDepth {
		return true
	}

	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return false
	}
	if issuance.ReferenceHolding != nil {
		asset, result := referencedAsset(view, issuance)
		if result != ter.TesSUCCESS {
			return false
		}
		return isAnyAssetFrozen(view, asset, [][20]byte{issuance.Issuer, account}, depth+1)
	}

	issuerRaw, err := view.Read(keylet.Account(issuance.Issuer))
	if err != nil || issuerRaw == nil {
		return false
	}
	issuer, err := state.ParseAccountRoot(issuerRaw)
	if err != nil || !issuer.HasVaultID() {
		return false
	}
	asset, result := vaultAsset(view, issuer.VaultID)
	if result != ter.TesSUCCESS {
		return false
	}
	return isAnyAssetFrozen(view, asset, [][20]byte{issuance.Issuer, account}, depth+1)
}

func IsAssetFrozen(view state.LedgerView, asset tx.Asset, account [20]byte) bool {
	if asset.IsNative() {
		return false
	}
	if asset.IsMPT() {
		id, err := DecodeID(asset.MPTIssuanceID)
		return err == nil && IsFrozen(view, id, account)
	}
	return isIOUFrozen(view, account, asset)
}

func isAnyAssetFrozen(view state.LedgerView, asset tx.Asset, accounts [][20]byte, depth uint8) bool {
	if asset.IsMPT() {
		id, err := DecodeID(asset.MPTIssuanceID)
		if err != nil {
			return false
		}
		for _, account := range accounts {
			if isFrozen(view, id, account, depth) {
				return true
			}
		}
		return false
	}
	for _, account := range accounts {
		if isIOUFrozen(view, account, asset) {
			return true
		}
	}
	return false
}

func TransferRate(view state.LedgerView, id [24]byte) uint32 {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS || issuance.TransferFee == 0 {
		return RateOne
	}
	return RateOne + 10_000*uint32(issuance.TransferFee)
}

func RequireAuth(view state.LedgerView, id [24]byte, account [20]byte, strong bool) ter.Result {
	authType := WeakAuth
	if strong {
		authType = StrongAuth
	}
	return RequireAuthWithTypeAt(view, id, account, authType, 0)
}

func RequireAuthAt(view state.LedgerView, id [24]byte, account [20]byte, strong bool, parentCloseTime uint32) ter.Result {
	authType := WeakAuth
	if strong {
		authType = StrongAuth
	}
	return RequireAuthWithTypeAt(view, id, account, authType, parentCloseTime)
}

func RequireAuthWithType(view state.LedgerView, id [24]byte, account [20]byte, authType AuthType) ter.Result {
	return RequireAuthWithTypeAt(view, id, account, authType, 0)
}

func RequireAuthWithTypeAt(view state.LedgerView, id [24]byte, account [20]byte, authType AuthType, parentCloseTime uint32) ter.Result {
	return requireAuthAt(view, id, account, authType, parentCloseTime, 0)
}

func requireAuthAt(view state.LedgerView, id [24]byte, account [20]byte, authType AuthType, parentCloseTime uint32, depth uint8) ter.Result {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Issuer == account {
		return ter.TesSUCCESS
	}
	if rules := view.Rules(); rules != nil && rules.Enabled(amendment.FeatureSingleAssetVault) {
		if depth >= maxAssetCheckDepth {
			return ter.TecINTERNAL
		}
		issuerRaw, err := view.Read(keylet.Account(issuance.Issuer))
		if err != nil || issuerRaw == nil {
			return ter.TefINTERNAL
		}
		issuer, err := state.ParseAccountRoot(issuerRaw)
		if err != nil {
			return ter.TefINTERNAL
		}
		if issuer.HasVaultID() {
			asset, result := vaultAsset(view, issuer.VaultID)
			if result != ter.TesSUCCESS {
				return ter.TefINTERNAL
			}
			if result := requireAssetAuthWithTypeAt(view, asset, account, authType, parentCloseTime, depth+1); result != ter.TesSUCCESS {
				return result
			}
		}
	}

	token, _, tokenResult := ReadHolding(view, id, account)
	if tokenResult != ter.TesSUCCESS {
		if tokenResult != ter.TecNO_AUTH {
			return tokenResult
		}
		if authType == StrongAuth || authType == LegacyAuth {
			return ter.TecNO_AUTH
		}
	}
	if issuance.DomainID != nil {
		domainResult := ValidDomain(view, *issuance.DomainID, account, parentCloseTime)
		if domainResult == ter.TesSUCCESS {
			return ter.TesSUCCESS
		}
		if token == nil {
			return domainResult
		}
	}
	if issuance.Flags&entry.LsfMPTRequireAuth == 0 {
		return ter.TesSUCCESS
	}
	if token != nil && token.Flags&entry.LsfMPTAuthorized != 0 {
		return ter.TesSUCCESS
	}

	rules := view.Rules()
	if rules == nil || (!rules.Enabled(amendment.FeatureSingleAssetVault) && !rules.Enabled(amendment.FeatureMPTokensV2)) {
		return ter.TecNO_AUTH
	}
	accountRaw, err := view.Read(keylet.Account(account))
	if err != nil {
		return ter.TefINTERNAL
	}
	if accountRaw == nil {
		return ter.TecNO_AUTH
	}
	accountRoot, err := state.ParseAccountRoot(accountRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	if accountRoot.IsPseudoAccount() {
		return ter.TesSUCCESS
	}
	return ter.TecNO_AUTH
}

func RequireAssetAuthAt(view state.LedgerView, asset tx.Asset, account [20]byte, authType AuthType, parentCloseTime uint32) ter.Result {
	return requireAssetAuthWithTypeAt(view, asset, account, authType, parentCloseTime, 0)
}

func requireAssetAuthAt(view state.LedgerView, asset tx.Asset, account [20]byte, strong bool, parentCloseTime uint32, depth uint8) ter.Result {
	authType := WeakAuth
	if strong {
		authType = StrongAuth
	}
	return requireAssetAuthWithTypeAt(view, asset, account, authType, parentCloseTime, depth)
}

func requireAssetAuthWithTypeAt(view state.LedgerView, asset tx.Asset, account [20]byte, authType AuthType, parentCloseTime uint32, depth uint8) ter.Result {
	if asset.IsMPT() {
		id, err := DecodeID(asset.MPTIssuanceID)
		if err != nil {
			return ter.TefINTERNAL
		}
		return requireAuthAt(view, id, account, authType, parentCloseTime, depth)
	}
	if asset.IsNative() {
		return ter.TesSUCCESS
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if account == issuer {
		return ter.TesSUCCESS
	}
	lineRaw, err := view.Read(keylet.Line(account, issuer, asset.Currency))
	if err != nil {
		return ter.TefINTERNAL
	}
	if lineRaw == nil && authType == StrongAuth {
		return ter.TecNO_LINE
	}
	issuerRaw, err := view.Read(keylet.Account(issuer))
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerRaw == nil {
		return ter.TesSUCCESS
	}
	issuerAccount, err := state.ParseAccountRoot(issuerRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	if issuerAccount.Flags&state.LsfRequireAuth == 0 {
		return ter.TesSUCCESS
	}
	if lineRaw == nil {
		return ter.TecNO_LINE
	}
	line, err := state.ParseRippleState(lineRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	if state.CompareAccountIDs(account, issuer) > 0 {
		if line.Flags&state.LsfLowAuth == 0 {
			return ter.TecNO_AUTH
		}
	} else if line.Flags&state.LsfHighAuth == 0 {
		return ter.TecNO_AUTH
	}
	return ter.TesSUCCESS
}

func isIOUFrozen(view state.LedgerView, account [20]byte, asset tx.Asset) bool {
	if asset.IsNative() {
		return false
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return false
	}
	issuerRaw, err := view.Read(keylet.Account(issuer))
	if err == nil && issuerRaw != nil {
		if issuerAccount, parseErr := state.ParseAccountRoot(issuerRaw); parseErr == nil &&
			issuerAccount.Flags&state.LsfGlobalFreeze != 0 {
			return true
		}
	}
	if account == issuer {
		return false
	}
	lineRaw, err := view.Read(keylet.Line(account, issuer, asset.Currency))
	if err != nil || lineRaw == nil {
		return false
	}
	line, err := state.ParseRippleState(lineRaw)
	if err != nil {
		return false
	}
	if state.CompareAccountIDs(issuer, account) > 0 {
		return line.Flags&state.LsfHighFreeze != 0
	}
	return line.Flags&state.LsfLowFreeze != 0
}

func ValidDomain(view state.LedgerView, domainIDHex string, account [20]byte, parentCloseTime uint32) ter.Result {
	domainBytes, err := hex.DecodeString(domainIDHex)
	if err != nil || len(domainBytes) != 32 {
		return ter.TefINTERNAL
	}
	var domainID [32]byte
	copy(domainID[:], domainBytes)
	raw, err := view.Read(keylet.PermissionedDomainByID(domainID))
	if err != nil {
		return ter.TefINTERNAL
	}
	if raw == nil {
		return ter.TecOBJECT_NOT_FOUND
	}
	domain, err := state.ParsePermissionedDomain(raw)
	if err != nil {
		return ter.TefINTERNAL
	}
	foundExpired := false
	for _, accepted := range domain.AcceptedCredentials {
		credentialRaw, err := view.Read(keylet.Credential(account, accepted.Issuer, accepted.CredentialType))
		if err != nil {
			return ter.TefINTERNAL
		}
		if credentialRaw == nil {
			continue
		}
		credentialEntry, err := credential.ParseCredentialEntry(credentialRaw)
		if err != nil {
			return ter.TefINTERNAL
		}
		if credential.CheckCredentialExpired(credentialEntry, parentCloseTime) {
			foundExpired = true
			continue
		}
		if credentialEntry.IsAccepted() {
			return ter.TesSUCCESS
		}
	}
	if foundExpired {
		return ter.TecEXPIRED
	}
	return ter.TecNO_AUTH
}

func validDomain(view state.LedgerView, domainIDHex string, account [20]byte, parentCloseTime uint32) ter.Result {
	return ValidDomain(view, domainIDHex, account, parentCloseTime)
}

func CanTrade(view state.LedgerView, id [24]byte) ter.Result {
	return canTrade(view, id, 0)
}

func canTrade(view state.LedgerView, id [24]byte, depth uint8) ter.Result {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if issuance.Flags&entry.LsfMPTCanTrade == 0 {
		return ter.TecNO_PERMISSION
	}
	rules := view.Rules()
	if rules != nil && rules.FixCleanup3_2_0Enabled() && issuance.ReferenceHolding != nil {
		if depth >= maxAssetCheckDepth {
			return ter.TecINTERNAL
		}
		asset, result := referencedAsset(view, issuance)
		if result != ter.TesSUCCESS {
			return result
		}
		if asset.IsMPT() {
			underlyingID, err := DecodeID(asset.MPTIssuanceID)
			if err != nil {
				return ter.TefINTERNAL
			}
			return canTrade(view, underlyingID, depth+1)
		}
	}
	return ter.TesSUCCESS
}

func CanTransfer(view state.LedgerView, id [24]byte, from, to [20]byte) ter.Result {
	return canTransfer(view, id, from, to, false, 0)
}

func CanTransferWithWaive(view state.LedgerView, id [24]byte, from, to [20]byte, waiveMPTCanTransfer bool) ter.Result {
	return canTransfer(view, id, from, to, waiveMPTCanTransfer, 0)
}

func canTransfer(view state.LedgerView, id [24]byte, from, to [20]byte, waiveMPTCanTransfer bool, depth uint8) ter.Result {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if waiveMPTCanTransfer || from == issuance.Issuer || to == issuance.Issuer {
		return ter.TesSUCCESS
	}
	if issuance.Flags&entry.LsfMPTCanTransfer == 0 {
		return ter.TecNO_AUTH
	}
	rules := view.Rules()
	if rules != nil && rules.FixCleanup3_2_0Enabled() && issuance.ReferenceHolding != nil {
		if depth >= maxAssetCheckDepth {
			return ter.TecINTERNAL
		}
		asset, result := referencedAsset(view, issuance)
		if result != ter.TesSUCCESS {
			return result
		}
		return canTransferAssetWithWaive(view, asset, from, to, false, depth+1)
	}
	return ter.TesSUCCESS
}

func CanTransferAsset(view state.LedgerView, asset tx.Asset, from, to [20]byte, waiveMPTCanTransfer bool) ter.Result {
	return canTransferAssetWithWaive(view, asset, from, to, waiveMPTCanTransfer, 0)
}

func canTransferAsset(view state.LedgerView, asset tx.Asset, from, to [20]byte, depth uint8) ter.Result {
	return canTransferAssetWithWaive(view, asset, from, to, false, depth)
}

func canTransferAssetWithWaive(view state.LedgerView, asset tx.Asset, from, to [20]byte, waiveMPTCanTransfer bool, depth uint8) ter.Result {
	if asset.IsMPT() {
		id, err := DecodeID(asset.MPTIssuanceID)
		if err != nil {
			return ter.TefINTERNAL
		}
		return canTransfer(view, id, from, to, waiveMPTCanTransfer, depth)
	}
	if asset.IsNative() {
		return ter.TesSUCCESS
	}
	issuer, err := state.DecodeAccountID(asset.Issuer)
	if err != nil {
		return ter.TefINTERNAL
	}
	if from == issuer || to == issuer {
		return ter.TesSUCCESS
	}
	issuerRaw, err := view.Read(keylet.Account(issuer))
	if err != nil || issuerRaw == nil {
		return ter.TefINTERNAL
	}
	issuerAccount, err := state.ParseAccountRoot(issuerRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	isRippleDisabled := func(account [20]byte) (bool, ter.Result) {
		lineRaw, err := view.Read(keylet.Line(account, issuer, asset.Currency))
		if err != nil {
			return false, ter.TefINTERNAL
		}
		if lineRaw == nil {
			return issuerAccount.Flags&state.LsfDefaultRipple == 0, ter.TesSUCCESS
		}
		line, err := state.ParseRippleState(lineRaw)
		if err != nil {
			return false, ter.TefINTERNAL
		}
		if state.CompareAccountIDs(issuer, account) > 0 {
			return line.Flags&state.LsfHighNoRipple != 0, ter.TesSUCCESS
		}
		return line.Flags&state.LsfLowNoRipple != 0, ter.TesSUCCESS
	}
	fromDisabled, result := isRippleDisabled(from)
	if result != ter.TesSUCCESS {
		return result
	}
	if !fromDisabled {
		return ter.TesSUCCESS
	}
	toDisabled, result := isRippleDisabled(to)
	if result != ter.TesSUCCESS {
		return result
	}
	if toDisabled {
		return ter.TerNO_RIPPLE
	}
	return ter.TesSUCCESS
}

func referencedAsset(view state.LedgerView, issuance *state.MPTokenIssuanceData) (tx.Asset, ter.Result) {
	if issuance.ReferenceHolding == nil {
		return tx.Asset{}, ter.TefINTERNAL
	}
	reference, err := hex.DecodeString(*issuance.ReferenceHolding)
	if err != nil || len(reference) != 32 {
		return tx.Asset{}, ter.TefINTERNAL
	}
	var key [32]byte
	copy(key[:], reference)
	raw, err := view.Read(keylet.Keylet{Key: key})
	if err != nil || raw == nil {
		return tx.Asset{}, ter.TefINTERNAL
	}
	typeCode, err := state.GetLedgerEntryType(raw)
	if err != nil {
		return tx.Asset{}, ter.TefINTERNAL
	}
	switch entry.Type(typeCode) {
	case entry.TypeMPToken:
		holding, err := state.ParseMPToken(raw)
		if err != nil {
			return tx.Asset{}, ter.TefINTERNAL
		}
		return tx.Asset{MPTIssuanceID: EncodeID(holding.MPTokenIssuanceID)}, ter.TesSUCCESS
	case entry.TypeRippleState:
		holding, err := state.ParseRippleState(raw)
		if err != nil {
			return tx.Asset{}, ter.TefINTERNAL
		}
		vaultPseudo := state.EncodeAccountIDSafe(issuance.Issuer)
		issuer := holding.LowLimit.Issuer
		if issuer == vaultPseudo {
			issuer = holding.HighLimit.Issuer
		}
		return tx.Asset{Currency: holding.LowLimit.Currency, Issuer: issuer}, ter.TesSUCCESS
	default:
		return tx.Asset{}, ter.TefINTERNAL
	}
}

func vaultAsset(view state.LedgerView, vaultID [32]byte) (tx.Asset, ter.Result) {
	raw, err := view.Read(keylet.VaultByID(vaultID))
	if err != nil || raw == nil {
		return tx.Asset{}, ter.TefINTERNAL
	}
	decoded := new(ledgerfields.Vault)
	if err := decoded.Decode(raw); err != nil {
		return tx.Asset{}, ter.TefINTERNAL
	}
	issue, ok := decoded.Asset.(map[string]any)
	if !ok {
		return tx.Asset{}, ter.TefINTERNAL
	}
	if id, ok := issue["mpt_issuance_id"].(string); ok {
		if _, err := DecodeID(id); err != nil {
			return tx.Asset{}, ter.TefINTERNAL
		}
		return tx.Asset{MPTIssuanceID: id}, ter.TesSUCCESS
	}
	currency, ok := issue["currency"].(string)
	if !ok || currency == "" {
		return tx.Asset{}, ter.TefINTERNAL
	}
	issuer, _ := issue["issuer"].(string)
	return tx.Asset{Currency: currency, Issuer: issuer}, ter.TesSUCCESS
}

func Funds(view state.LedgerView, id [24]byte, account [20]byte, zeroIfFrozen bool) (int64, ter.Result) {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return 0, result
	}
	if account == issuance.Issuer {
		amount := int64(AvailableAmount(issuance))
		if mptokensV2Enabled(view) {
			if hook, ok := view.(balanceHookMPT); ok {
				amount = hook.BalanceHookMPT(account, id, amount)
			}
		}
		return amount, ter.TesSUCCESS
	}
	if zeroIfFrozen && IsFrozen(view, id, account) {
		return 0, ter.TesSUCCESS
	}
	token, _, result := ReadHolding(view, id, account)
	if result != ter.TesSUCCESS {
		return 0, result
	}
	if token.MPTAmount > math.MaxInt64 {
		return 0, ter.TefINTERNAL
	}
	amount := int64(token.MPTAmount)
	if mptokensV2Enabled(view) {
		if hook, ok := view.(balanceHookMPT); ok {
			amount = hook.BalanceHookMPT(account, id, amount)
		}
	}
	return amount, ter.TesSUCCESS
}

func IssuerFundsToSelfIssue(view state.LedgerView, id [24]byte) (int64, ter.Result) {
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return 0, result
	}
	amount := int64(AvailableAmount(issuance))
	if mptokensV2Enabled(view) {
		if hook, ok := view.(balanceHookSelfIssueMPT); ok {
			amount = hook.BalanceHookSelfIssueMPT(id, amount)
		}
	}
	return amount, ter.TesSUCCESS
}

func RecordIssuerSelfDebit(view state.LedgerView, id [24]byte, amount uint64) ter.Result {
	if amount == 0 || !mptokensV2Enabled(view) {
		return ter.TesSUCCESS
	}
	issuance, _, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	if hook, ok := view.(issuerSelfDebitHookMPT); ok {
		hook.IssuerSelfDebitHookMPT(id, amount, int64(AvailableAmount(issuance)))
	}
	return ter.TesSUCCESS
}

func EnsureHolding(view state.LedgerView, id [24]byte, holder [20]byte, flags uint32, adjustOwnerCount bool) ter.Result {
	if holder == Issuer(id) {
		return ter.TesSUCCESS
	}
	k := keylet.MPTokenByID(id, holder)
	exists, err := view.Exists(k)
	if err != nil {
		return ter.TefINTERNAL
	}
	if exists {
		return ter.TesSUCCESS
	}

	accountKey := keylet.Account(holder)
	accountRaw, err := view.Read(accountKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if accountRaw == nil {
		return ter.TecNO_DST
	}
	account, err := state.ParseAccountRoot(accountRaw)
	if err != nil {
		return ter.TefINTERNAL
	}

	dirResult, err := state.DirInsert(view, keylet.OwnerDir(holder), k.Key, false, func(dir *state.DirectoryNode) {
		dir.Owner = holder
	})
	if err != nil {
		return ter.TecDIR_FULL
	}
	token := &state.MPTokenData{
		Account:           holder,
		MPTokenIssuanceID: id,
		OwnerNode:         dirResult.Page,
		Flags:             flags,
	}
	data, err := state.SerializeMPToken(token)
	if err != nil || view.Insert(k, data) != nil {
		return ter.TefINTERNAL
	}
	if adjustOwnerCount {
		current := account.OwnerCount
		account.OwnerCount++
		if hook, ok := view.(ownerCountHook); ok {
			hook.AdjustOwnerCount(holder, current, account.OwnerCount)
		}
		data, err = state.SerializeAccountRoot(account)
		if err != nil || view.Update(accountKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	return ter.TesSUCCESS
}

func RemoveHolding(view state.LedgerView, id [24]byte, holder [20]byte, adjustOwnerCount bool) ter.Result {
	tokenKey := keylet.MPTokenByID(id, holder)
	raw, err := view.Read(tokenKey)
	if err != nil {
		return ter.TefINTERNAL
	}
	if raw == nil {
		if holder == Issuer(id) {
			return ter.TesSUCCESS
		}
		return ter.TecOBJECT_NOT_FOUND
	}
	token, err := state.ParseMPToken(raw)
	if err != nil {
		return ter.TefINTERNAL
	}
	rules := view.Rules()
	lockedObligation := rules != nil && rules.Enabled(amendment.FeatureFixCleanup3_1_3) &&
		token.LockedAmount != nil && *token.LockedAmount != 0
	if token.MPTAmount != 0 || lockedObligation {
		return ter.TecHAS_OBLIGATIONS
	}
	removed, err := state.DirRemove(view, keylet.OwnerDir(holder), token.OwnerNode, tokenKey.Key, false)
	if err != nil || !removed.Success {
		return ter.TefINTERNAL
	}
	if err := view.Erase(tokenKey); err != nil {
		return ter.TefINTERNAL
	}
	if !adjustOwnerCount {
		return ter.TesSUCCESS
	}

	accountKey := keylet.Account(holder)
	accountRaw, err := view.Read(accountKey)
	if err != nil || accountRaw == nil {
		return ter.TefINTERNAL
	}
	account, err := state.ParseAccountRoot(accountRaw)
	if err != nil {
		return ter.TefINTERNAL
	}
	current := account.OwnerCount
	if account.OwnerCount > 0 {
		account.OwnerCount--
	}
	if hook, ok := view.(ownerCountHook); ok {
		hook.AdjustOwnerCount(holder, current, account.OwnerCount)
	}
	data, err := state.SerializeAccountRoot(account)
	if err != nil || view.Update(accountKey, data) != nil {
		return ter.TefINTERNAL
	}
	return ter.TesSUCCESS
}

func Credit(view state.LedgerView, id [24]byte, sender, receiver [20]byte, amount int64, allowOverflow bool) ter.Result {
	if amount < 0 {
		return ter.TefINTERNAL
	}
	if amount == 0 || sender == receiver {
		return ter.TesSUCCESS
	}

	issuance, issuanceKey, result := ReadIssuance(view, id)
	if result != ter.TesSUCCESS {
		return result
	}
	issuer := issuance.Issuer
	value := uint64(amount)
	newOutstanding := issuance.OutstandingAmount
	available := int64(AvailableAmount(issuance))
	var hook creditHookMPT
	if mptokensV2Enabled(view) {
		hook, _ = view.(creditHookMPT)
	}

	var senderToken, receiverToken *state.MPTokenData
	var senderKey, receiverKey keylet.Keylet
	if sender == issuer {
		maximum := MaximumAmount(issuance)
		limit := maximum
		if allowOverflow {
			limit = math.MaxUint64
		}
		if value > maximum || newOutstanding > limit-value {
			return ter.TecPATH_DRY
		}
		newOutstanding += value
	} else {
		senderToken, senderKey, result = ReadHolding(view, id, sender)
		if result != ter.TesSUCCESS {
			return result
		}
		if senderToken.MPTAmount < value {
			return ter.TecINSUFFICIENT_FUNDS
		}
		if hook != nil {
			hook.CreditHookMPT(sender, receiver, id, value, senderToken.MPTAmount, available)
		}
	}

	if receiver == issuer {
		if newOutstanding < value {
			return ter.TefINTERNAL
		}
		newOutstanding -= value
	} else {
		receiverToken, receiverKey, result = ReadHolding(view, id, receiver)
		if result != ter.TesSUCCESS {
			return result
		}
		if receiverToken.MPTAmount > math.MaxUint64-value {
			return ter.TefINTERNAL
		}
		if hook != nil {
			hook.CreditHookMPT(sender, receiver, id, value, receiverToken.MPTAmount, available)
		}
	}

	if senderToken != nil {
		senderToken.MPTAmount -= value
		data, err := state.SerializeMPToken(senderToken)
		if err != nil || view.Update(senderKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	if receiverToken != nil {
		receiverToken.MPTAmount += value
		data, err := state.SerializeMPToken(receiverToken)
		if err != nil || view.Update(receiverKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	if newOutstanding != issuance.OutstandingAmount {
		issuance.OutstandingAmount = newOutstanding
		data, err := state.SerializeMPTokenIssuance(issuance)
		if err != nil || view.Update(issuanceKey, data) != nil {
			return ter.TefINTERNAL
		}
	}
	return ter.TesSUCCESS
}

func mptokensV2Enabled(view state.LedgerView) bool {
	rules := view.Rules()
	return rules != nil && rules.MPTokensV2Enabled()
}

// MultiplyRate applies an MPT transfer rate, rounding to nearest, even on a tie.
// The boolean is false when the result cannot be represented as an int64.
func MultiplyRate(amount int64, rate uint32) (int64, bool) {
	if amount == 0 || rate == RateOne {
		return amount, true
	}
	return scaleRate(amount, uint64(rate), uint64(RateOne))
}

// DivideRate removes an MPT transfer rate, rounding to nearest, even on a tie.
// The boolean is false when the result cannot be represented as an int64.
func DivideRate(amount int64, rate uint32) (int64, bool) {
	if amount == 0 || rate == RateOne {
		return amount, true
	}
	return scaleRate(amount, uint64(RateOne), uint64(rate))
}

func scaleRate(amount int64, numeratorFactor, denominatorValue uint64) (int64, bool) {
	if denominatorValue == 0 {
		return 0, false
	}
	numerator := new(big.Int).Mul(big.NewInt(amount), new(big.Int).SetUint64(numeratorFactor))
	denominator := new(big.Int).SetUint64(denominatorValue)
	quotient, remainder := new(big.Int), new(big.Int)
	quotient.QuoRem(numerator, denominator, remainder)
	if remainder.Sign() != 0 {
		twiceRemainder := new(big.Int).Lsh(new(big.Int).Abs(remainder), 1)
		comparison := twiceRemainder.Cmp(denominator)
		magnitude := new(big.Int).Abs(new(big.Int).Set(quotient))
		if comparison > 0 || comparison == 0 && magnitude.Bit(0) == 1 {
			if numerator.Sign() > 0 {
				quotient.Add(quotient, big.NewInt(1))
			} else {
				quotient.Sub(quotient, big.NewInt(1))
			}
		}
	}
	if !quotient.IsInt64() {
		return 0, false
	}
	return quotient.Int64(), true
}

func Send(view state.LedgerView, id [24]byte, sender, receiver [20]byte, amount int64, waiveFee, allowOverflow bool) (int64, ter.Result) {
	if sender == receiver || amount == 0 {
		return amount, ter.TesSUCCESS
	}
	issuer := Issuer(id)
	if sender == issuer || receiver == issuer {
		return amount, Credit(view, id, sender, receiver, amount, allowOverflow)
	}
	gross := amount
	if !waiveFee {
		var ok bool
		gross, ok = MultiplyRate(amount, TransferRate(view, id))
		if !ok {
			return 0, ter.TefEXCEPTION
		}
	}
	if result := Credit(view, id, issuer, receiver, amount, true); result != ter.TesSUCCESS {
		return 0, result
	}
	if result := Credit(view, id, sender, issuer, gross, allowOverflow); result != ter.TesSUCCESS {
		return 0, result
	}
	return gross, ter.TesSUCCESS
}
