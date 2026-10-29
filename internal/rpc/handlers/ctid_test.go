package handlers

import "testing"

func TestEncodeCTIDBoundaries(t *testing.T) {
	tests := []struct {
		name                          string
		ledgerSeq, txIndex, networkID uint32
		want                          string
		ok                            bool
	}{
		{name: "zero", want: "C000000000000000", ok: true},
		{name: "ordinary", ledgerSeq: 1, txIndex: 2, networkID: 3, want: "C000000100020003", ok: true},
		{name: "inclusive maxima", ledgerSeq: ctidMaxLedgerSeq, txIndex: ctidMaxComponent, networkID: ctidMaxComponent, want: "CFFFFFFFFFFFFFFF", ok: true},
		{name: "ledger overflow", ledgerSeq: ctidMaxLedgerSeq + 1},
		{name: "transaction overflow", txIndex: ctidMaxComponent + 1},
		{name: "network overflow", networkID: ctidMaxComponent + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := encodeCTID(test.ledgerSeq, test.txIndex, test.networkID)
			if ok != test.ok || got != test.want {
				t.Fatalf("encodeCTID(%d, %d, %d) = %q, %v; want %q, %v", test.ledgerSeq, test.txIndex, test.networkID, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestEncodeTxResponseCTIDBoundaries(t *testing.T) {
	for _, test := range []struct {
		name                          string
		ledgerSeq, txIndex, networkID uint32
		ok                            bool
	}{
		{name: "below strict maxima", ledgerSeq: ctidMaxLedgerSeq - 1, txIndex: ctidMaxComponent, networkID: ctidMaxComponent - 1, ok: true},
		{name: "ledger maximum omitted", ledgerSeq: ctidMaxLedgerSeq, ok: false},
		{name: "network maximum omitted", ledgerSeq: 1, networkID: ctidMaxComponent, ok: false},
		{name: "transaction maximum included", ledgerSeq: 1, txIndex: ctidMaxComponent, ok: true},
		{name: "transaction overflow omitted", ledgerSeq: 1, txIndex: ctidMaxComponent + 1, ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, ok := encodeTxResponseCTID(test.ledgerSeq, test.txIndex, test.networkID)
			if ok != test.ok {
				t.Fatalf("encodeTxResponseCTID(%d, %d, %d) ok = %v; want %v", test.ledgerSeq, test.txIndex, test.networkID, ok, test.ok)
			}
		})
	}
}

func TestParseCTID(t *testing.T) {
	for _, value := range []string{"C0CA2AA7326FC045", "c0ca2aa7326fc045"} {
		ledgerSeq, txIndex, networkID, err := parseCTID(value)
		if err != nil {
			t.Fatalf("parseCTID(%q): %v", value, err)
		}
		if ledgerSeq != 13249191 || txIndex != 12911 || networkID != 49221 {
			t.Fatalf("parseCTID(%q) = %d, %d, %d", value, ledgerSeq, txIndex, networkID)
		}
	}
	for _, value := range []string{"C003FFFFFFFFFFF", "C003FFFFFFFFFFFG", "FFFFFFFFFFFFFFFF"} {
		if _, _, _, err := parseCTID(value); err == nil {
			t.Fatalf("parseCTID(%q) succeeded", value)
		}
	}
}

func TestAddExpandedTransactionCTID(t *testing.T) {
	tests := []struct {
		name   string
		tx     map[string]any
		meta   map[string]any
		want   string
		absent bool
	}{
		{name: "node network", tx: map[string]any{}, meta: map[string]any{"TransactionIndex": uint32(2)}, want: "C000000700020015"},
		{name: "transaction network", tx: map[string]any{"NetworkID": uint32(1025)}, meta: map[string]any{"TransactionIndex": uint32(2)}, want: "C000000700020401"},
		{name: "no metadata", tx: map[string]any{}, absent: true},
		{name: "network overflow", tx: map[string]any{"NetworkID": uint32(65536)}, meta: map[string]any{"TransactionIndex": uint32(2)}, absent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			entry := map[string]any{}
			addExpandedTransactionCTID(entry, StoredTransaction{TxJSON: test.tx, Meta: test.meta}, 7, 21)
			got, ok := entry["ctid"]
			if test.absent {
				if ok {
					t.Fatalf("ctid = %v, want absent", got)
				}
				return
			}
			if got != test.want {
				t.Fatalf("ctid = %v, want %s", got, test.want)
			}
		})
	}
}
