package service

import (
	"testing"

	"github.com/LeJamon/go-xrpl/storage/relationaldb"
)

// TestAddMetaAffectedAccounts pins the affected-account set account_tx indexes:
// every STAccount field plus the issuer of Low/HighLimit/Taker amounts, and
// nothing else — mirroring rippled's TxMeta::getAffectedAccounts.
func TestAddMetaAffectedAccounts(t *testing.T) {
	senderAddr, senderID := addressFromBytes(t, 0x01)
	makerAddr, makerID := addressFromBytes(t, 0x02)
	payIssuerAddr, payIssuerID := addressFromBytes(t, 0x03)
	lowAddr, lowID := addressFromBytes(t, 0x04)
	highAddr, highID := addressFromBytes(t, 0x05)
	balanceIssuerAddr, balanceIssuerID := addressFromBytes(t, 0x06)

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
		},
	}

	got := map[relationaldb.AccountID]struct{}{}
	addMetaAffectedAccounts(meta, got)

	want := []relationaldb.AccountID{
		relationaldb.AccountID(senderID),
		relationaldb.AccountID(makerID),
		relationaldb.AccountID(payIssuerID),
		relationaldb.AccountID(lowID),
		relationaldb.AccountID(highID),
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
