package vault

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func TestVaultLifecycleFieldSerialization(t *testing.T) {
	for _, dates := range []bool{false, true} {
		v := &vaultData{Owner: [20]byte{1}, Account: [20]byte{2}, Asset: tx.Asset{Currency: "XRP"}, LEVersion: 1, VaultKind: 1}
		if dates {
			subscription, redemption := uint32(0), ^uint32(0)
			v.SubscriptionDate, v.RedemptionDate = &subscription, &redemption
		}
		data, err := serializeVault(v)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := parseVault(data)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.LEVersion != 1 || parsed.VaultKind != 1 || (parsed.SubscriptionDate != nil) != dates || (parsed.RedemptionDate != nil) != dates {
			t.Fatalf("lost lifecycle fields: %+v", parsed)
		}
		if dates && (*parsed.SubscriptionDate != 0 || *parsed.RedemptionDate != ^uint32(0)) {
			t.Fatal("changed dates")
		}
		again, err := serializeVault(parsed)
		if err != nil || !bytes.Equal(data, again) {
			t.Fatalf("round trip: %v", err)
		}
		fields, err := binarycodec.Decode(hex.EncodeToString(data))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"LEVersion", "VaultKind"} {
			original := fields[name]
			fields[name] = 0
			malformed, err := binarycodec.EncodeBytes(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := new(entry.Vault).Decode(malformed); err == nil {
				t.Errorf("accepted explicit default %s", name)
			}
			fields[name] = original
		}
	}
}

func TestSerializeVaultCanonicalFieldStyles(t *testing.T) {
	var owner, account [20]byte
	for i := range owner {
		owner[i] = byte(i + 1)
		account[i] = byte(i + 21)
	}
	ownerAddr, err := state.EncodeAccountID(owner)
	if err != nil {
		t.Fatalf("encode owner: %v", err)
	}
	accountAddr, err := state.EncodeAccountID(account)
	if err != nil {
		t.Fatalf("encode account: %v", err)
	}

	t.Run("defaults omitted and required zeros retained", func(t *testing.T) {
		got, err := serializeVault(&vaultData{
			Owner:   owner,
			Account: account,
			Asset:   tx.Asset{Currency: "XRP"},
		})
		if err != nil {
			t.Fatalf("serialize vault: %v", err)
		}

		assertVaultEncoding(t, got, map[string]any{
			"LedgerEntryType":   "Vault",
			"Flags":             uint32(0),
			"Sequence":          uint32(0),
			"OwnerNode":         "0",
			"Owner":             ownerAddr,
			"Account":           accountAddr,
			"Asset":             map[string]any{"currency": "XRP"},
			"ShareMPTID":        strings.Repeat("0", 48),
			"WithdrawalPolicy":  0,
			"PreviousTxnID":     strings.Repeat("0", 64),
			"PreviousTxnLgrSeq": uint32(0),
		})
	})

	t.Run("nondefaults and deferred threading retained", func(t *testing.T) {
		var shareID [24]byte
		var previousTxnID [32]byte
		for i := range shareID {
			shareID[i] = 0xA5
		}
		for i := range previousTxnID {
			previousTxnID[i] = 0xB6
		}

		got, err := serializeVault(&vaultData{
			Owner:             owner,
			Account:           account,
			Sequence:          7,
			OwnerNode:         0xAB,
			ShareMPTID:        shareID,
			Asset:             tx.Asset{Currency: "USD", Issuer: ownerAddr},
			WithdrawalPolicy:  1,
			Scale:             6,
			Flags:             2,
			Data:              "abcd",
			AssetsTotal:       "1000",
			AssetsAvailable:   "900",
			AssetsMaximum:     "2000",
			LossUnrealized:    "5",
			PreviousTxnID:     previousTxnID,
			PreviousTxnLgrSeq: 11,
		})
		if err != nil {
			t.Fatalf("serialize vault: %v", err)
		}

		assertVaultEncoding(t, got, map[string]any{
			"LedgerEntryType":   "Vault",
			"Flags":             uint32(2),
			"Sequence":          uint32(7),
			"OwnerNode":         "AB",
			"Owner":             ownerAddr,
			"Account":           accountAddr,
			"Data":              "ABCD",
			"Asset":             map[string]any{"currency": "USD", "issuer": ownerAddr},
			"AssetsTotal":       "1000",
			"AssetsAvailable":   "900",
			"AssetsMaximum":     "2000",
			"LossUnrealized":    "5",
			"ShareMPTID":        strings.Repeat("A5", 24),
			"WithdrawalPolicy":  1,
			"Scale":             6,
			"PreviousTxnID":     strings.Repeat("B6", 32),
			"PreviousTxnLgrSeq": uint32(11),
		})
	})
}

func assertVaultEncoding(t *testing.T, got []byte, fields map[string]any) {
	t.Helper()
	want, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("encode expected vault: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding mismatch\n got: %s\nwant: %s", hex.EncodeToString(got), hex.EncodeToString(want))
	}
}
