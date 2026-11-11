package vault

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/tx"
	ledgerfields "github.com/LeJamon/go-xrpl/ledger/entry"
)

// vaultData is the parsed form of an ltVAULT ledger entry.
//
// The NUMBER fields (AssetsTotal/AssetsAvailable/AssetsMaximum/LossUnrealized)
// are held as their canonical decimal/scientific string; an empty string means
// the field is absent (soeDEFAULT zero).
type vaultData struct {
	Owner            [20]byte
	Account          [20]byte // pseudo-account
	Sequence         uint32
	OwnerNode        uint64
	ShareMPTID       [24]byte
	Asset            tx.Asset
	AssetIsMPT       bool
	AssetMPTID       [24]byte
	WithdrawalPolicy uint8
	LEVersion        uint8
	VaultKind        uint8
	SubscriptionDate *uint32
	RedemptionDate   *uint32
	Scale            uint8
	Flags            uint32
	Data             string // hex-encoded Blob
	DataPresent      bool   // distinguishes an absent field from a present empty Blob
	AssetsTotal      string
	AssetsAvailable  string
	AssetsMaximum    string
	LossUnrealized   string

	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

func (v *vaultData) assetToIssueValue() ledgerfields.IssueValue {
	if v.AssetIsMPT {
		return ledgerfields.IssueValue{MPTIssuanceID: strings.ToUpper(hex.EncodeToString(v.AssetMPTID[:]))}
	}
	if isNativeAsset(v.Asset) {
		return ledgerfields.IssueValue{Currency: "XRP"}
	}
	return ledgerfields.IssueValue{Currency: v.Asset.Currency, Issuer: v.Asset.Issuer}
}

// serializeVault encodes a vault ledger entry to its canonical binary form.
func serializeVault(v *vaultData) ([]byte, error) {
	return serializeVaultForRules(v, nil)
}

func serializeVaultForRules(v *vaultData, rules *amendment.Rules) ([]byte, error) {
	entry := &ledgerfields.Vault{}
	entry.SetFlags(v.Flags)
	entry.SetSequence(v.Sequence)
	entry.SetOwnerNodeValue(v.OwnerNode)
	if err := entry.SetOwnerValue(v.Owner); err != nil {
		return nil, fmt.Errorf("encode owner: %w", err)
	}
	if err := entry.SetAccountValue(v.Account); err != nil {
		return nil, fmt.Errorf("encode pseudo account: %w", err)
	}
	if err := entry.SetAssetValue(v.assetToIssueValue()); err != nil {
		return nil, fmt.Errorf("encode asset: %w", err)
	}
	numbers, err := tx.WireNumbers(vaultNumberScale(rules), v.AssetsTotal, v.AssetsAvailable, v.AssetsMaximum, v.LossUnrealized)
	if err != nil {
		return nil, fmt.Errorf("encode vault Number: %w", err)
	}
	if err := entry.SetAssetsTotalValue(numbers[0]); err != nil {
		return nil, fmt.Errorf("encode assets total: %w", err)
	}
	if err := entry.SetAssetsAvailableValue(numbers[1]); err != nil {
		return nil, fmt.Errorf("encode assets available: %w", err)
	}
	if err := entry.SetAssetsMaximumValue(numbers[2]); err != nil {
		return nil, fmt.Errorf("encode assets maximum: %w", err)
	}
	if err := entry.SetLossUnrealizedValue(numbers[3]); err != nil {
		return nil, fmt.Errorf("encode unrealized loss: %w", err)
	}
	entry.SetShareMPTIDValue(v.ShareMPTID)
	entry.SetWithdrawalPolicy(v.WithdrawalPolicy)
	entry.SetScale(v.Scale)
	entry.SetLEVersion(v.LEVersion)
	entry.SetVaultKind(v.VaultKind)
	if v.SubscriptionDate != nil {
		entry.SetSubscriptionDate(*v.SubscriptionDate)
	}
	if v.RedemptionDate != nil {
		entry.SetRedemptionDate(*v.RedemptionDate)
	}
	if v.DataPresent || v.Data != "" {
		entry.SetData(strings.ToUpper(v.Data))
	}

	entry.SetPreviousTxnIDValue(v.PreviousTxnID)
	entry.SetPreviousTxnLgrSeq(v.PreviousTxnLgrSeq)

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode vault: %w", err)
	}
	return data, nil
}

// parseVault decodes a vault ledger entry via the canonical ledgerfields
// decoder and maps it onto vaultData.
func parseVault(data []byte) (*vaultData, error) {
	lv := &ledgerfields.Vault{}
	if err := lv.Decode(data); err != nil {
		return nil, err
	}
	owner, err := lv.GetOwner()
	if err != nil {
		return nil, err
	}
	account, err := lv.GetAccount()
	if err != nil {
		return nil, err
	}
	ownerNode, err := lv.GetOwnerNode()
	if err != nil {
		return nil, err
	}
	shareMPTID, err := lv.GetShareMPTID()
	if err != nil {
		return nil, err
	}
	previousTxnID, err := lv.GetPreviousTxnID()
	if err != nil {
		return nil, err
	}
	asset, err := lv.GetAsset()
	if err != nil {
		return nil, err
	}
	sequence, err := lv.GetSequence()
	if err != nil {
		return nil, err
	}
	leVersion, err := lv.GetLEVersion()
	if err != nil {
		return nil, err
	}
	vaultKind, err := lv.GetVaultKind()
	if err != nil {
		return nil, err
	}
	withdrawalPolicy, err := lv.GetWithdrawalPolicy()
	if err != nil {
		return nil, err
	}
	scale, err := lv.GetScale()
	if err != nil {
		return nil, err
	}
	flags, err := lv.GetFlags()
	if err != nil {
		return nil, err
	}
	previousTxnLgrSeq, err := lv.GetPreviousTxnLgrSeq()
	if err != nil {
		return nil, err
	}

	vd := &vaultData{
		Sequence:         sequence,
		LEVersion:        leVersion,
		VaultKind:        vaultKind,
		WithdrawalPolicy: withdrawalPolicy,
		Scale:            scale,
		Flags:            flags,
		Owner:            owner,
		Account:          account,
		OwnerNode:        ownerNode,
		ShareMPTID:       shareMPTID,
		PreviousTxnID:    previousTxnID,
	}

	if lv.HasData() {
		vd.DataPresent = true
		vd.Data = lv.Data
	}
	if lv.HasSubscriptionDate() {
		value, err := lv.GetSubscriptionDate()
		if err != nil {
			return nil, err
		}
		vd.SubscriptionDate = &value
	}
	if lv.HasRedemptionDate() {
		value, err := lv.GetRedemptionDate()
		if err != nil {
			return nil, err
		}
		vd.RedemptionDate = &value
	}
	vd.PreviousTxnLgrSeq = previousTxnLgrSeq

	if asset.MPTIssuanceID != "" {
		vd.AssetIsMPT = true
		decoded, err := hex.DecodeString(asset.MPTIssuanceID)
		if err != nil {
			return nil, fmt.Errorf("decode Asset mpt issuance ID: %w", err)
		}
		if len(decoded) != len(vd.AssetMPTID) {
			return nil, fmt.Errorf("decode Asset mpt issuance ID: expected %d bytes, got %d", len(vd.AssetMPTID), len(decoded))
		}
		copy(vd.AssetMPTID[:], decoded)
	} else {
		vd.Asset = tx.Asset{Currency: asset.Currency, Issuer: asset.Issuer}
	}

	for _, field := range []struct {
		name string
		has  func() bool
		get  func() (ledgerfields.NumberValue, error)
		dst  *string
	}{
		{"AssetsTotal", lv.HasAssetsTotal, lv.GetAssetsTotal, &vd.AssetsTotal},
		{"AssetsAvailable", lv.HasAssetsAvailable, lv.GetAssetsAvailable, &vd.AssetsAvailable},
		{"AssetsMaximum", lv.HasAssetsMaximum, lv.GetAssetsMaximum, &vd.AssetsMaximum},
		{"LossUnrealized", lv.HasLossUnrealized, lv.GetLossUnrealized, &vd.LossUnrealized},
	} {
		if !field.has() {
			continue
		}
		value, err := field.get()
		if err != nil {
			return nil, fmt.Errorf("decode %s: %w", field.name, err)
		}
		*field.dst = value
	}

	return vd, nil
}
