package state

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"testing"
)

type adapterCorpus struct {
	Vectors []struct {
		LedgerEntryType string `json:"ledger_entry_type"`
		Coverage        string `json:"coverage"`
		Hex             string `json:"hex"`
	} `json:"vectors"`
}

func TestGeneratedSerializerAdaptersMatchRippledCorpus(t *testing.T) {
	raw, err := os.ReadFile("../../../ledger/entry/testdata/sle-corpus.json")
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var corpus adapterCorpus
	if err := json.Unmarshal(raw, &corpus); err != nil {
		t.Fatalf("decode corpus: %v", err)
	}

	// Include adapters whose runtime structs can represent every field in the
	// corpus vector without selecting between mutually exclusive ledger shapes.
	tests := map[string]func([]byte) ([]byte, error){
		"AccountRoot": func(data []byte) ([]byte, error) {
			entry, err := ParseAccountRoot(data)
			if err != nil {
				return nil, err
			}
			return SerializeAccountRoot(entry)
		},
		"Check": func(data []byte) ([]byte, error) {
			entry, err := ParseCheck(data)
			if err != nil {
				return nil, err
			}
			return SerializeCheckFromData(entry)
		},
		"DID": func(data []byte) ([]byte, error) {
			entry, err := ParseDID(data)
			if err != nil {
				return nil, err
			}
			account, err := EncodeAccountID(entry.Account)
			if err != nil {
				return nil, err
			}
			return SerializeDID(entry, account)
		},
		"Delegate": func(data []byte) ([]byte, error) {
			entry, err := ParseDelegate(data)
			if err != nil {
				return nil, err
			}
			var destinationNode *uint64
			if entry.HasDestinationNode {
				destinationNode = &entry.DestinationNode
			}
			return SerializeDelegate(entry.Account, entry.Authorize, entry.Permissions, entry.OwnerNode, destinationNode, entry.PreviousTxnID, entry.PreviousTxnLgrSeq)
		},
		"MPTokenIssuance": func(data []byte) ([]byte, error) {
			entry, err := ParseMPTokenIssuance(data)
			if err != nil {
				return nil, err
			}
			return SerializeMPTokenIssuance(entry)
		},
		"MPToken": func(data []byte) ([]byte, error) {
			entry, err := ParseMPToken(data)
			if err != nil {
				return nil, err
			}
			return SerializeMPToken(entry)
		},
		"NFTokenPage": func(data []byte) ([]byte, error) {
			entry, err := ParseNFTokenPage(data)
			if err != nil {
				return nil, err
			}
			return SerializeNFTokenPage(entry)
		},
		"Offer": func(data []byte) ([]byte, error) {
			entry, err := ParseLedgerOffer(data)
			if err != nil {
				return nil, err
			}
			return SerializeLedgerOffer(entry)
		},
		"Oracle": func(data []byte) ([]byte, error) {
			entry, err := ParseOracle(data)
			if err != nil {
				return nil, err
			}
			return SerializeOracle(entry)
		},
		"PayChannel": func(data []byte) ([]byte, error) {
			entry, err := ParsePayChannel(data)
			if err != nil {
				return nil, err
			}
			return SerializePayChannelFromData(entry)
		},
		"PermissionedDomain": func(data []byte) ([]byte, error) {
			entry, err := ParsePermissionedDomain(data)
			if err != nil {
				return nil, err
			}
			owner, err := EncodeAccountID(entry.Owner)
			if err != nil {
				return nil, err
			}
			return SerializePermissionedDomain(entry, owner)
		},
	}

	seen := make(map[string]bool, len(tests))
	for _, vector := range corpus.Vectors {
		serialize, ok := tests[vector.LedgerEntryType]
		if !ok || vector.Coverage != "full" {
			continue
		}
		seen[vector.LedgerEntryType] = true
		t.Run(vector.LedgerEntryType, func(t *testing.T) {
			want, err := hex.DecodeString(vector.Hex)
			if err != nil {
				t.Fatalf("decode vector: %v", err)
			}
			got, err := serialize(want)
			if err != nil {
				t.Fatalf("adapter round trip: %v", err)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("adapter bytes differ:\nwant %X\n got %X", want, got)
			}
		})
	}
	for entryType := range tests {
		if !seen[entryType] {
			t.Errorf("missing full corpus vector for %s", entryType)
		}
	}
}
