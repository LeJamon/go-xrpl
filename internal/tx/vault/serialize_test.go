package vault

import (
	"bytes"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

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
			"LedgerEntryType":  "Vault",
			"Flags":            uint32(0),
			"Sequence":         uint32(0),
			"OwnerNode":        "0",
			"Owner":            ownerAddr,
			"Account":          accountAddr,
			"Asset":            map[string]any{"currency": "XRP"},
			"ShareMPTID":       strings.Repeat("0", 48),
			"WithdrawalPolicy": 0,
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
