package service

import (
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// TestAddMetaAffectedAccounts pins the affected-account set account_tx indexes:
// every STAccount field, the issuer of Low/HighLimit/Taker amounts, and the
// issuer encoded in an MPTokenIssuanceID — and nothing else — mirroring
// rippled's TxMeta::getAffectedAccounts.
func TestAddMetaAffectedAccounts(t *testing.T) {
	senderAddr, senderID := addressFromBytes(t, 0x01)
	makerAddr, makerID := addressFromBytes(t, 0x02)
	payIssuerAddr, payIssuerID := addressFromBytes(t, 0x03)
	lowAddr, lowID := addressFromBytes(t, 0x04)
	highAddr, highID := addressFromBytes(t, 0x05)
	balanceIssuerAddr, balanceIssuerID := addressFromBytes(t, 0x06)
	_, mptIssuerID := addressFromBytes(t, 0x07)

	// MPTokenIssuanceID = 4-byte sequence ++ 20-byte issuer (rippled makeMptID).
	mptIssuanceID := "00000005" + hex.EncodeToString(mptIssuerID[:])

	meta := map[string]any{
		"AffectedNodes": []any{
			map[string]any{"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{"Account": senderAddr},
			}},
			map[string]any{"CreatedNode": map[string]any{
				"NewFields": map[string]any{
					"Account":   makerAddr,
					"TakerPays": map[string]any{"currency": "USD", "issuer": payIssuerAddr, "value": "10"},
					"TakerGets": "1000000", // XRP — no issuer, must not match
				},
			}},
			map[string]any{"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{
					"LowLimit":  map[string]any{"currency": "USD", "issuer": lowAddr, "value": "0"},
					"HighLimit": map[string]any{"currency": "USD", "issuer": highAddr, "value": "100"},
					// Balance carries an issuer too, but rippled does NOT index it.
					"Balance": map[string]any{"currency": "USD", "issuer": balanceIssuerAddr, "value": "5"},
				},
			}},
			map[string]any{"CreatedNode": map[string]any{
				"NewFields": map[string]any{
					"MPTokenIssuanceID": mptIssuanceID,
				},
			}},
		},
	}

	got := map[relationaldb.AccountID]struct{}{}
	if err := addMetaAffectedAccounts(meta, got); err != nil {
		t.Fatal(err)
	}

	want := []relationaldb.AccountID{
		relationaldb.AccountID(senderID),
		relationaldb.AccountID(makerID),
		relationaldb.AccountID(payIssuerID),
		relationaldb.AccountID(lowID),
		relationaldb.AccountID(highID),
		relationaldb.AccountID(mptIssuerID),
	}
	for _, w := range want {
		if _, ok := got[w]; !ok {
			t.Errorf("missing affected account %x", w)
		}
	}
	if _, ok := got[relationaldb.AccountID(balanceIssuerID)]; ok {
		t.Error("Balance issuer must not be indexed (only Low/HighLimit/Taker issuers are affected)")
	}
	if len(got) != len(want) {
		t.Errorf("got %d affected accounts, want %d", len(got), len(want))
	}
}

func TestAddMetaAffectedAccountsAllowsBareModifiedNode(t *testing.T) {
	meta := map[string]any{
		"AffectedNodes": []any{
			map[string]any{"ModifiedNode": map[string]any{
				"LedgerEntryType": "DirectoryNode",
				"LedgerIndex":     "E8D6E0FB6A3F7D12F3A7A1648661FD2E39C10161755816AAB198A530DB4BC23B",
			}},
		},
	}
	got := map[relationaldb.AccountID]struct{}{}

	if err := addMetaAffectedAccounts(meta, got); err != nil {
		t.Fatalf("addMetaAffectedAccounts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("affected accounts = %d, want 0", len(got))
	}
}

func TestAddMetaAffectedAccountsIndexesMPTAmountIssuers(t *testing.T) {
	issuers := make([]relationaldb.AccountID, 6)
	for i := range issuers {
		_, raw := addressFromBytes(t, byte(0x10+i))
		issuers[i] = relationaldb.AccountID(raw)
	}
	issuanceID := func(sequence byte, issuer relationaldb.AccountID) string {
		id := make([]byte, 24)
		id[3] = sequence
		copy(id[4:], issuer[:])
		return hex.EncodeToString(id)
	}
	amount := func(id string) map[string]any {
		return map[string]any{"mpt_issuance_id": id, "value": "10"}
	}

	tests := []struct {
		name string
		node map[string]any
		want relationaldb.AccountID
	}{
		{
			name: "created taker pays",
			node: map[string]any{"CreatedNode": map[string]any{
				"NewFields": map[string]any{"TakerPays": amount(issuanceID(1, issuers[0]))},
			}},
			want: issuers[0],
		},
		{
			name: "created taker gets",
			node: map[string]any{"CreatedNode": map[string]any{
				"NewFields": map[string]any{"TakerGets": amount(issuanceID(2, issuers[1]))},
			}},
			want: issuers[1],
		},
		{
			name: "modified taker pays",
			node: map[string]any{"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{"TakerPays": amount(issuanceID(3, issuers[2]))},
			}},
			want: issuers[2],
		},
		{
			name: "modified taker gets",
			node: map[string]any{"ModifiedNode": map[string]any{
				"FinalFields": map[string]any{"TakerGets": amount(issuanceID(4, issuers[3]))},
			}},
			want: issuers[3],
		},
		{
			name: "deleted taker pays",
			node: map[string]any{"DeletedNode": map[string]any{
				"FinalFields": map[string]any{"TakerPays": amount(issuanceID(5, issuers[4]))},
			}},
			want: issuers[4],
		},
		{
			name: "deleted taker gets",
			node: map[string]any{"DeletedNode": map[string]any{
				"FinalFields": map[string]any{"TakerGets": amount(issuanceID(6, issuers[5]))},
			}},
			want: issuers[5],
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := map[relationaldb.AccountID]struct{}{}
			meta := map[string]any{"AffectedNodes": []any{tt.node}}
			if err := addMetaAffectedAccounts(meta, got); err != nil {
				t.Fatalf("addMetaAffectedAccounts: %v", err)
			}
			if _, ok := got[tt.want]; !ok {
				t.Fatalf("missing MPT issuer %x in affected accounts", tt.want)
			}
			if len(got) != 1 {
				t.Fatalf("affected accounts = %d, want 1", len(got))
			}
		})
	}
}

func TestAddMetaAffectedAccountsMPTAmountFieldSelection(t *testing.T) {
	_, finalIssuerBytes := addressFromBytes(t, 0x21)
	_, previousIssuerBytes := addressFromBytes(t, 0x22)
	finalIssuer := relationaldb.AccountID(finalIssuerBytes)
	previousIssuer := relationaldb.AccountID(previousIssuerBytes)
	finalID := "00000001" + hex.EncodeToString(finalIssuer[:])
	previousID := "00000002" + hex.EncodeToString(previousIssuer[:])

	meta := map[string]any{
		"AffectedNodes": []any{map[string]any{"ModifiedNode": map[string]any{
			"FinalFields": map[string]any{
				"TakerPays": map[string]any{"mpt_issuance_id": finalID, "value": "1"},
			},
			"PreviousFields": map[string]any{
				"TakerGets": map[string]any{"mpt_issuance_id": previousID, "value": "1"},
			},
		}}},
	}
	got := map[relationaldb.AccountID]struct{}{}
	if err := addMetaAffectedAccounts(meta, got); err != nil {
		t.Fatalf("addMetaAffectedAccounts: %v", err)
	}
	if _, ok := got[finalIssuer]; !ok {
		t.Fatalf("missing FinalFields issuer %x", finalIssuer)
	}
	if _, ok := got[previousIssuer]; ok {
		t.Fatalf("PreviousFields issuer %x must not be indexed", previousIssuer)
	}
}

func TestAddMetaAffectedAccountsRejectsMalformedMPTAmount(t *testing.T) {
	meta := map[string]any{
		"AffectedNodes": []any{map[string]any{"CreatedNode": map[string]any{
			"NewFields": map[string]any{
				"TakerPays": map[string]any{"mpt_issuance_id": "not-an-issuance-id", "value": "1"},
			},
		}}},
	}
	got := map[relationaldb.AccountID]struct{}{}
	if err := addMetaAffectedAccounts(meta, got); err == nil {
		t.Fatal("addMetaAffectedAccounts accepted malformed MPT amount")
	}
	if len(got) != 0 {
		t.Fatalf("affected accounts = %d after malformed MPT amount, want 0", len(got))
	}
}

func TestAddMetaAffectedAccountsSkipsZeroMPTIssuer(t *testing.T) {
	meta := map[string]any{
		"AffectedNodes": []any{map[string]any{"CreatedNode": map[string]any{
			"NewFields": map[string]any{
				"TakerGets": map[string]any{
					"mpt_issuance_id": "000000010000000000000000000000000000000000000000",
					"value":           "1",
				},
			},
		}}},
	}
	got := map[relationaldb.AccountID]struct{}{}
	if err := addMetaAffectedAccounts(meta, got); err != nil {
		t.Fatalf("addMetaAffectedAccounts: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("affected accounts = %d for zero MPT issuer, want 0", len(got))
	}
}
