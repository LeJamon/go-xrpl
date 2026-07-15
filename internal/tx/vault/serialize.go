package vault

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/serdes"
	"github.com/LeJamon/go-xrpl/codec/binarycodec/types"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ledgerfields"
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
// soeDEFAULT fields (the NUMBER totals, Scale, Data) are omitted when zero to
// match rippled's STObject serialization.
func serializeVault(v *vaultData) ([]byte, error) {
	return serializeVaultForRules(v, nil)
}

func serializeVaultForRules(v *vaultData, rules *amendment.Rules) ([]byte, error) {
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
	entry.SetWithdrawalPolicy(v.WithdrawalPolicy)
	entry.SetScale(v.Scale)
	if v.Data != "" {
		entry.SetData(strings.ToUpper(v.Data))
	}

	entry.SetPreviousTxnID(strings.ToUpper(hex.EncodeToString(v.PreviousTxnID[:])))
	entry.SetPreviousTxnLgrSeq(v.PreviousTxnLgrSeq)

	return encodeVaultObject(entry.ToMap(), vaultNumberScale(rules))
}

func vaultWireNumber(value string) string {
	if value == "" {
		return "0"
	}
	return value
}

func encodeVaultObject(obj map[string]any, scale state.MantissaScale) ([]byte, error) {
	defs := definitions.Get()
	fields := make([]*definitions.FieldInstance, 0, len(obj))
	for name := range obj {
		field, err := defs.FieldInstanceByName(name)
		if err != nil {
			return nil, fmt.Errorf("encode vault field %s: %w", name, err)
		}
		fields = append(fields, field)
	}
	sort.Slice(fields, func(i, j int) bool { return fields[i].Ordinal < fields[j].Ordinal })

	serializer := serdes.NewBinarySerializer(serdes.DefaultFieldIDCodec())
	for _, field := range fields {
		if !field.IsSerialized {
			continue
		}
		value := obj[field.FieldName]
		var encoded []byte
		var err error
		if field.Type == "Number" {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("encode vault field %s: expected Number string", field.FieldName)
			}
			encoded, err = encodeVaultNumber(s, scale)
		} else {
			serializedType := types.SerializedTypeFor(field.Type)
			if serializedType == nil {
				return nil, fmt.Errorf("encode vault field %s: unknown type %s", field.FieldName, field.Type)
			}
			if field.FieldName == "LedgerEntryType" {
				name, ok := value.(string)
				if !ok {
					return nil, fmt.Errorf("encode vault field %s: expected ledger entry type string", field.FieldName)
				}
				code, err := defs.LedgerEntryTypeCode(name)
				if err != nil {
					return nil, fmt.Errorf("encode vault field %s: %w", field.FieldName, err)
				}
				value = int(code)
			}
			encoded, err = serializedType.FromJSON(value)
		}
		if err != nil {
			return nil, fmt.Errorf("encode vault field %s: %w", field.FieldName, err)
		}
		if err := serializer.WriteFieldAndValue(*field, encoded); err != nil {
			return nil, fmt.Errorf("encode vault field %s: %w", field.FieldName, err)
		}
	}
	return serializer.Bytes(), nil
}

func encodeVaultNumber(s string, scale state.MantissaScale) ([]byte, error) {
	number, err := state.ParseXRPLNumber(s, scale, state.RoundToNearest)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, 12)
	binary.BigEndian.PutUint64(encoded[:8], uint64(number.Mantissa()))
	binary.BigEndian.PutUint32(encoded[8:], uint32(int32(number.Exponent())))
	return encoded, nil
}

// parseVault decodes a vault ledger entry via the canonical ledgerfields
// decoder and maps it onto vaultData.
func parseVault(data []byte) (*vaultData, error) {
	numberFields, err := readVaultNumberFields(data)
	if err != nil {
		return nil, err
	}
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
		AssetsTotal:      numberFields["AssetsTotal"],
		AssetsAvailable:  numberFields["AssetsAvailable"],
		AssetsMaximum:    numberFields["AssetsMaximum"],
		LossUnrealized:   numberFields["LossUnrealized"],
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

func readVaultNumberFields(data []byte) (map[string]string, error) {
	parser := serdes.NewBinaryParser(data, definitions.Get())
	fields := make(map[string]string, 4)
	for parser.HasMore() {
		field, err := parser.ReadField()
		if err != nil {
			return nil, fmt.Errorf("decode vault field: %w", err)
		}
		if field.Type == "Number" {
			encoded, err := parser.ReadBytes(12)
			if err != nil {
				return nil, fmt.Errorf("decode vault field %s: %w", field.FieldName, err)
			}
			mantissa := int64(binary.BigEndian.Uint64(encoded[:8]))
			if mantissa != 0 {
				exponent := int(int32(binary.BigEndian.Uint32(encoded[8:])))
				fields[field.FieldName] = fmt.Sprintf("%de%d", mantissa, exponent)
			}
			continue
		}

		serializedType := types.SerializedTypeFor(field.Type)
		if serializedType == nil {
			return nil, fmt.Errorf("decode vault field %s: unknown type %s", field.FieldName, field.Type)
		}
		if field.IsVLEncoded {
			length, err := parser.ReadVariableLength()
			if err != nil {
				return nil, fmt.Errorf("decode vault field %s length: %w", field.FieldName, err)
			}
			if _, err := serializedType.ToJSON(parser, length); err != nil {
				return nil, fmt.Errorf("decode vault field %s: %w", field.FieldName, err)
			}
			continue
		}
		if _, err := serializedType.ToJSON(parser); err != nil {
			return nil, fmt.Errorf("decode vault field %s: %w", field.FieldName, err)
		}
	}
	return fields, nil
}
