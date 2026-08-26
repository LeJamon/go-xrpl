package xchain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	codecTypes "github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	rootcrypto "github.com/LeJamon/go-xrpl/crypto"
	"github.com/LeJamon/go-xrpl/crypto/ed25519"
	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

const (
	rootAccount                   = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	maxNativeDrops         int64  = 100_000_000_000_000_000
	maxAccountCreateClaims uint64 = 128
)

type chainType uint8

const (
	lockingChain chainType = iota
	issuingChain
)

func otherChain(chain chainType) chainType {
	if chain == lockingChain {
		return issuingChain
	}
	return lockingChain
}

func sourceChain(wasLockingChainSend bool) chainType {
	if wasLockingChainSend {
		return lockingChain
	}
	return issuingChain
}

func destinationChain(wasLockingChainSend bool) chainType {
	return otherChain(sourceChain(wasLockingChainSend))
}

func (b XChainBridge) door(chain chainType) string {
	if chain == lockingChain {
		return b.LockingChainDoor
	}
	return b.IssuingChainDoor
}

func (b XChainBridge) issue(chain chainType) tx.Asset {
	if chain == lockingChain {
		return b.LockingChainIssue
	}
	return b.IssuingChainIssue
}

func normalizedAsset(asset tx.Asset) tx.Asset {
	if asset.IsMPT() {
		return asset
	}
	currency, err := keylet.ParseCurrency(asset.Currency)
	if err != nil {
		return asset
	}
	if currency == ([20]byte{}) {
		return tx.Asset{Currency: "XRP"}
	}
	currencyCode, err := codecTypes.DecodeCurrencyCode(currency[:])
	if err != nil {
		return asset
	}
	return tx.Asset{Currency: currencyCode, Issuer: asset.Issuer}
}

func bridgeAssetIsNative(asset tx.Asset) bool {
	if asset.IsMPT() || asset.Issuer != "" {
		return false
	}
	currency, err := keylet.ParseCurrency(asset.Currency)
	return err == nil && currency == ([20]byte{})
}

func assetEqual(a, b tx.Asset) bool {
	return normalizedAsset(a) == normalizedAsset(b)
}

func assetOf(amount tx.Amount) tx.Asset {
	if amount.IsNative() {
		return tx.Asset{Currency: "XRP"}
	}
	return tx.Asset{Currency: amount.Currency, Issuer: amount.Issuer, MPTIssuanceID: amount.MPTIssuanceID()}
}

func amountWithAsset(amount tx.Amount, asset tx.Asset) tx.Amount {
	if bridgeAssetIsNative(asset) {
		return tx.NewXRPAmount(amount.Drops())
	}
	return tx.NewIssuedAmount(amount.Mantissa(), amount.Exponent(), asset.Currency, asset.Issuer)
}

func isLegalNet(amount tx.Amount) bool {
	return !amount.IsNative() || (amount.Drops() >= -maxNativeDrops && amount.Drops() <= maxNativeDrops)
}

func validateBridgeFields(bridge XChainBridge) error {
	if _, err := state.DecodeAccountID(bridge.LockingChainDoor); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "invalid locking chain door")
	}
	if _, err := state.DecodeAccountID(bridge.IssuingChainDoor); err != nil {
		return ter.Errorf(ter.TemMALFORMED, "invalid issuing chain door")
	}
	for _, issue := range []tx.Asset{bridge.LockingChainIssue, bridge.IssuingChainIssue} {
		if issue.IsMPT() {
			return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "MPT issues are not supported by XChain bridges")
		}
		currency, err := keylet.ParseCurrency(issue.Currency)
		if err != nil {
			return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "invalid bridge currency")
		}
		if currency == ([20]byte{}) {
			if issue.Issuer != "" {
				return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "native bridge issue cannot have an issuer")
			}
		} else {
			if issue.Issuer == "" {
				return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "issued bridge currency requires an issuer")
			}
			issuer, err := state.DecodeAccountID(issue.Issuer)
			if err != nil || issuer == ([20]byte{}) || issuer == ([20]byte{19: 1}) {
				return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "invalid bridge issuer")
			}
		}
	}
	return nil
}

func validateCreateBridge(account string, bridge XChainBridge, reward tx.Amount, minCreate *tx.Amount) error {
	if err := validateBridgeFields(bridge); err != nil {
		return err
	}
	if bridge.LockingChainDoor == bridge.IssuingChainDoor {
		return ter.Errorf(ter.TemXCHAIN_EQUAL_DOOR_ACCOUNTS, "bridge doors must differ")
	}
	if account != bridge.LockingChainDoor && account != bridge.IssuingChainDoor {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_NONDOOR_OWNER, "bridge owner is not a door")
	}
	if bridgeAssetIsNative(bridge.LockingChainIssue) != bridgeAssetIsNative(bridge.IssuingChainIssue) {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "bridge issues must both be XRP or both be issued")
	}
	if !reward.IsNative() || reward.IsNegative() {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT, "invalid bridge reward")
	}
	if minCreate != nil && (!minCreate.IsNative() || minCreate.Signum() <= 0 ||
		!bridgeAssetIsNative(bridge.LockingChainIssue) || !bridgeAssetIsNative(bridge.IssuingChainIssue)) {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT, "invalid minimum account-create amount")
	}
	if bridgeAssetIsNative(bridge.IssuingChainIssue) {
		if bridge.IssuingChainDoor != rootAccount {
			return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "XRP issuing door must be the root account")
		}
	} else if bridge.IssuingChainDoor != bridge.IssuingChainIssue.Issuer {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "issuing door must issue the wrapped asset")
	}
	if !bridgeAssetIsNative(bridge.LockingChainIssue) && bridge.LockingChainDoor == bridge.LockingChainIssue.Issuer {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_ISSUES, "locking door cannot lock its own asset")
	}
	return nil
}

func validateModifyBridge(account string, bridge XChainBridge, reward, minCreate *tx.Amount, flags uint32) error {
	if err := validateBridgeFields(bridge); err != nil {
		return err
	}
	clearCreate := flags&tfClearAccountCreateAmount != 0
	if reward == nil && minCreate == nil && !clearCreate {
		return ter.Errorf(ter.TemMALFORMED, "bridge modification changes nothing")
	}
	if minCreate != nil && clearCreate {
		return ter.Errorf(ter.TemMALFORMED, "minimum account-create amount cannot be set and cleared")
	}
	if account != bridge.LockingChainDoor && account != bridge.IssuingChainDoor {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_NONDOOR_OWNER, "bridge owner is not a door")
	}
	if reward != nil && (!reward.IsNative() || reward.IsNegative()) {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_REWARD_AMOUNT, "invalid bridge reward")
	}
	if minCreate != nil && (!minCreate.IsNative() || minCreate.Signum() <= 0 ||
		!bridgeAssetIsNative(bridge.LockingChainIssue) || !bridgeAssetIsNative(bridge.IssuingChainIssue)) {
		return ter.Errorf(ter.TemXCHAIN_BRIDGE_BAD_MIN_ACCOUNT_CREATE_AMOUNT, "invalid minimum account-create amount")
	}
	return nil
}

func bridgeMap(bridge XChainBridge) map[string]any {
	return map[string]any{
		"LockingChainDoor":  bridge.LockingChainDoor,
		"LockingChainIssue": assetMap(bridge.LockingChainIssue),
		"IssuingChainDoor":  bridge.IssuingChainDoor,
		"IssuingChainIssue": assetMap(bridge.IssuingChainIssue),
	}
}

func flattenXChain(transaction tx.Transaction, bridge XChainBridge) (map[string]any, error) {
	fields, err := tx.ReflectFlatten(transaction)
	if err != nil {
		return nil, err
	}
	fields["XChainBridge"] = bridgeMap(bridge)
	return fields, nil
}

func assetMap(asset tx.Asset) map[string]any {
	asset = normalizedAsset(asset)
	out := map[string]any{"currency": asset.Currency}
	if !bridgeAssetIsNative(asset) {
		out["issuer"] = asset.Issuer
	}
	return out
}

func amountAny(amount tx.Amount) (any, error) {
	raw, err := json.Marshal(amount)
	if err != nil {
		return nil, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	return value, nil
}

func amountFromAny(value any) (tx.Amount, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return tx.Amount{}, err
	}
	var amount tx.Amount
	if err := json.Unmarshal(raw, &amount); err != nil {
		return tx.Amount{}, err
	}
	return amount, nil
}

func bridgeKeylet(bridge XChainBridge, chain chainType) (keylet.Keylet, error) {
	door, err := state.DecodeAccountID(bridge.door(chain))
	if err != nil {
		return keylet.Keylet{}, err
	}
	currency, err := keylet.ParseCurrency(normalizedAsset(bridge.issue(chain)).Currency)
	if err != nil {
		return keylet.Keylet{}, err
	}
	return keylet.Bridge(door, currency), nil
}

func claimBridgeKeylet(bridge XChainBridge) (keylet.XChainBridge, error) {
	lockingDoor, err := state.DecodeAccountID(bridge.LockingChainDoor)
	if err != nil {
		return keylet.XChainBridge{}, err
	}
	issuingDoor, err := state.DecodeAccountID(bridge.IssuingChainDoor)
	if err != nil {
		return keylet.XChainBridge{}, err
	}
	lockingCurrency, err := keylet.ParseCurrency(normalizedAsset(bridge.LockingChainIssue).Currency)
	if err != nil {
		return keylet.XChainBridge{}, err
	}
	issuingCurrency, err := keylet.ParseCurrency(normalizedAsset(bridge.IssuingChainIssue).Currency)
	if err != nil {
		return keylet.XChainBridge{}, err
	}
	var lockingIssuer, issuingIssuer [20]byte
	if !bridgeAssetIsNative(bridge.LockingChainIssue) {
		lockingIssuer, err = state.DecodeAccountID(bridge.LockingChainIssue.Issuer)
		if err != nil {
			return keylet.XChainBridge{}, err
		}
	}
	if !bridgeAssetIsNative(bridge.IssuingChainIssue) {
		issuingIssuer, err = state.DecodeAccountID(bridge.IssuingChainIssue.Issuer)
		if err != nil {
			return keylet.XChainBridge{}, err
		}
	}
	return keylet.XChainBridge{
		LockingDoor: lockingDoor, LockingCurrency: lockingCurrency, LockingIssuer: lockingIssuer,
		IssuingDoor: issuingDoor, IssuingCurrency: issuingCurrency, IssuingIssuer: issuingIssuer,
	}, nil
}

func readBridge(view tx.LedgerView, bridge XChainBridge) (*entry.Bridge, keylet.Keylet, error) {
	want := bridgeMap(bridge)
	for _, chain := range []chainType{lockingChain, issuingChain} {
		k, err := bridgeKeylet(bridge, chain)
		if err != nil {
			return nil, keylet.Keylet{}, err
		}
		data, err := view.Read(k)
		if err != nil {
			return nil, keylet.Keylet{}, err
		}
		if data == nil {
			continue
		}
		var sle entry.Bridge
		if err := sle.Decode(data); err != nil {
			return nil, keylet.Keylet{}, err
		}
		if reflect.DeepEqual(sle.XChainBridge, want) {
			return &sle, k, nil
		}
	}
	return nil, keylet.Keylet{}, nil
}

func parseHexUint(value string) (uint64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 16, 64)
}

func encodeEntry(value interface{ Encode() ([]byte, error) }) ([]byte, ter.Result) {
	data, err := value.Encode()
	if err != nil {
		return nil, ter.TefINTERNAL
	}
	return data, ter.TesSUCCESS
}

func attestationMessage(fields map[string]any) ([]byte, error) {
	return binarycodec.EncodeBytes(fields)
}

func verifyAttestationSignature(publicKeyHex, signatureHex string, message []byte) bool {
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return false
	}
	signature, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	switch rootcrypto.PublicKeyType(publicKey) {
	case rootcrypto.KeyTypeEd25519:
		return ed25519.Algorithm{}.ValidateBytes(message, publicKey, signature)
	case rootcrypto.KeyTypeSecp256k1:
		return secp256k1.Algorithm{}.ValidateBytes(message, publicKey, signature)
	default:
		return false
	}
}

func claimAttestationMessage(x *XChainAddClaimAttestation) ([]byte, error) {
	amount, err := amountAny(x.Amount)
	if err != nil {
		return nil, err
	}
	fields := map[string]any{
		"XChainClaimID":            strconv.FormatUint(x.XChainClaimID, 16),
		"Amount":                   amount,
		"OtherChainSource":         x.OtherChainSource,
		"AttestationRewardAccount": x.AttestationRewardAccount,
		"WasLockingChainSend":      boolInt(x.WasLockingChainSend != 0),
		"XChainBridge":             bridgeMap(x.XChainBridge),
	}
	if x.Destination != "" {
		fields["Destination"] = x.Destination
	}
	return attestationMessage(fields)
}

func createAccountAttestationMessage(x *XChainAddAccountCreateAttestation) ([]byte, error) {
	amount, err := amountAny(x.Amount)
	if err != nil {
		return nil, err
	}
	reward, err := amountAny(x.SignatureReward)
	if err != nil {
		return nil, err
	}
	return attestationMessage(map[string]any{
		"XChainAccountCreateCount": strconv.FormatUint(x.XChainAccountCreateCount, 16),
		"Amount":                   amount,
		"SignatureReward":          reward,
		"Destination":              x.Destination,
		"OtherChainSource":         x.OtherChainSource,
		"AttestationRewardAccount": x.AttestationRewardAccount,
		"WasLockingChainSend":      boolInt(x.WasLockingChainSend != 0),
		"XChainBridge":             bridgeMap(x.XChainBridge),
	})
}

func boolInt(value bool) uint8 {
	if value {
		return 1
	}
	return 0
}

func validateClaimAttestation(x *XChainAddClaimAttestation) error {
	publicKey, err := hex.DecodeString(x.PublicKey)
	if err != nil || rootcrypto.PublicKeyType(publicKey) == rootcrypto.KeyTypeUnknown {
		return ter.Errorf(ter.TemMALFORMED, "invalid attestation public key")
	}
	message, err := claimAttestationMessage(x)
	if err != nil || !verifyAttestationSignature(x.PublicKey, x.Signature, message) ||
		!isLegalNet(x.Amount) || x.Amount.Signum() <= 0 ||
		!assetEqual(assetOf(x.Amount), x.XChainBridge.issue(sourceChain(x.WasLockingChainSend != 0))) {
		return ter.Errorf(ter.TemXCHAIN_BAD_PROOF, "invalid claim attestation proof")
	}
	return nil
}

func validateCreateAccountAttestation(x *XChainAddAccountCreateAttestation) error {
	if x.AttestationRewardAccount == "" || x.AttestationSignerAccount == "" || x.PublicKey == "" || x.Signature == "" {
		return ter.Errorf(ter.TemMALFORMED, "missing account-create attestation field")
	}
	publicKey, err := hex.DecodeString(x.PublicKey)
	if err != nil || rootcrypto.PublicKeyType(publicKey) == rootcrypto.KeyTypeUnknown {
		return ter.Errorf(ter.TemMALFORMED, "invalid attestation public key")
	}
	message, err := createAccountAttestationMessage(x)
	if err != nil || !verifyAttestationSignature(x.PublicKey, x.Signature, message) ||
		!isLegalNet(x.Amount) || !isLegalNet(x.SignatureReward) || x.Amount.Signum() <= 0 ||
		!assetEqual(assetOf(x.Amount), x.XChainBridge.issue(sourceChain(x.WasLockingChainSend != 0))) {
		return ter.Errorf(ter.TemXCHAIN_BAD_PROOF, "invalid account-create attestation proof")
	}
	return nil
}

func publicKeyAccount(publicKeyHex string) (string, error) {
	publicKey, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return "", err
	}
	return addresscodec.EncodeClassicAddressFromPublicKey(publicKey)
}

func mustAmountAny(amount tx.Amount) any {
	value, err := amountAny(amount)
	if err != nil {
		panic(fmt.Sprintf("xchain amount serialization failed: %v", err))
	}
	return value
}
