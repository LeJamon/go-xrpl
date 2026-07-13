package vault

import (
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
)

// vaultData is the parsed form of an ltVAULT ledger entry.
//
// The NUMBER fields (AssetsTotal/AssetsAvailable/AssetsMaximum/LossUnrealized)
// are held as their canonical decimal/scientific string; an empty string means
// the field is absent (soeDEFAULT zero). This is the local Number seam noted in
// the PR body — a shared state.Number helper can replace the string carriage
// later without touching call sites.
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
	Scale            uint8
	Flags            uint32
	Data             string // hex-encoded Blob
	AssetsTotal      string
	AssetsAvailable  string
	AssetsMaximum    string
	LossUnrealized   string

	PreviousTxnID     [32]byte
	PreviousTxnLgrSeq uint32
}

// assetToIssueMap renders the vault asset as a binary-codec Issue value.
func (v *vaultData) assetToIssueMap() map[string]any {
	if v.AssetIsMPT {
		return map[string]any{"mpt_issuance_id": strings.ToUpper(hex.EncodeToString(v.AssetMPTID[:]))}
	}
	if isNativeAsset(v.Asset) {
		return map[string]any{"currency": "XRP"}
	}
	return map[string]any{"currency": v.Asset.Currency, "issuer": v.Asset.Issuer}
}

// serializeVault encodes a vault ledger entry to its canonical binary form.
func serializeVault(v *vaultData) ([]byte, error) {
	ownerAddr, err := state.EncodeAccountID(v.Owner)
	if err != nil {
		return nil, fmt.Errorf("encode owner: %w", err)
	}
	pseudoAddr, err := state.EncodeAccountID(v.Account)
	if err != nil {
		return nil, fmt.Errorf("encode pseudo account: %w", err)
	}

	entry := &ledgerfields.Vault{}
	entry.SetFlags(v.Flags)
	entry.SetSequence(v.Sequence)
	entry.SetOwnerNode(fmt.Sprintf("%X", v.OwnerNode))
	entry.SetOwner(ownerAddr)
	entry.SetAccount(pseudoAddr)
	entry.SetAsset(v.assetToIssueMap())
	entry.SetAssetsTotal(vaultWireNumber(v.AssetsTotal))
	entry.SetAssetsAvailable(vaultWireNumber(v.AssetsAvailable))
	entry.SetAssetsMaximum(vaultWireNumber(v.AssetsMaximum))
	entry.SetLossUnrealized(vaultWireNumber(v.LossUnrealized))
	entry.SetShareMPTID(strings.ToUpper(hex.EncodeToString(v.ShareMPTID[:])))
	entry.SetWithdrawalPolicy(int(v.WithdrawalPolicy))
	entry.SetScale(int(v.Scale))
	if v.Data != "" {
		entry.SetData(strings.ToUpper(v.Data))
	}

	var zeroHash [32]byte
	if v.PreviousTxnID != zeroHash {
		entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(v.PreviousTxnID[:])))
		entry.SetPreviousTxnLgrSeq(v.PreviousTxnLgrSeq)
	}

	data, err := entry.Encode()
	if err != nil {
		return nil, fmt.Errorf("encode vault: %w", err)
	}
	return data, nil
}

func vaultWireNumber(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

// parseVault decodes a vault ledger entry via the canonical ledgerfields
// decoder and maps it onto vaultData.
func parseVault(data []byte) (*vaultData, error) {
	lv := &ledgerfields.Vault{}
	if err := lv.Decode(data); err != nil {
		return nil, err
	}

	vd := &vaultData{
		Sequence:         lv.Sequence,
		WithdrawalPolicy: uint8(lv.WithdrawalPolicy),
		Scale:            uint8(lv.Scale),
		Flags:            lv.Flags,
		Data:             lv.Data,
		AssetsTotal:      normalizeNumberString(lv.AssetsTotal),
		AssetsAvailable:  normalizeNumberString(lv.AssetsAvailable),
		AssetsMaximum:    normalizeNumberString(lv.AssetsMaximum),
		LossUnrealized:   normalizeNumberString(lv.LossUnrealized),
	}

	if id, err := state.DecodeAccountID(lv.Owner); err == nil {
		vd.Owner = id
	}
	if id, err := state.DecodeAccountID(lv.Account); err == nil {
		vd.Account = id
	}
	if n, err := strconv.ParseUint(lv.OwnerNode, 16, 64); err == nil {
		vd.OwnerNode = n
	}
	if b, err := hex.DecodeString(lv.ShareMPTID); err == nil && len(b) == 24 {
		copy(vd.ShareMPTID[:], b)
	}
	if b, err := hex.DecodeString(lv.PreviousTxnID); err == nil && len(b) == 32 {
		copy(vd.PreviousTxnID[:], b)
	}
	vd.PreviousTxnLgrSeq = lv.PreviousTxnLgrSeq

	if m, ok := lv.Asset.(map[string]any); ok {
		if mptID, ok := m["mpt_issuance_id"].(string); ok {
			vd.AssetIsMPT = true
			if b, err := hex.DecodeString(mptID); err == nil && len(b) == 24 {
				copy(vd.AssetMPTID[:], b)
			}
		} else {
			cur, _ := m["currency"].(string)
			iss, _ := m["issuer"].(string)
			vd.Asset = tx.Asset{Currency: cur, Issuer: iss}
		}
	}

	return vd, nil
}

// normalizeNumberString coerces a decoded NUMBER value ("0" or a decimal /
// scientific string) into vaultData's convention: "" for zero, else the string.
func normalizeNumberString(v any) string {
	s, ok := v.(string)
	if !ok || s == "" || s == "0" {
		return ""
	}
	return s
}
