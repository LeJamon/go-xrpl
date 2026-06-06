package cli

import (
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestConsensusPhaseName(t *testing.T) {
	cases := []struct {
		phase consensus.Phase
		want  string
	}{
		{consensus.PhaseOpen, rpc.ConsensusPhaseOpen},
		{consensus.PhaseEstablish, rpc.ConsensusPhaseEstablish},
		{consensus.PhaseAccepted, rpc.ConsensusPhaseAccepted},
	}
	for _, tc := range cases {
		if got := consensusPhaseName(tc.phase); got != tc.want {
			t.Errorf("consensusPhaseName(%v) = %q want %q", tc.phase, got, tc.want)
		}
	}
	// An out-of-range phase falls through to Phase.String().
	if got := consensusPhaseName(consensus.Phase(99)); got == "" {
		t.Error("default phase name should be non-empty")
	}
}

func TestCurrencySpecFromAmount(t *testing.T) {
	// A string amount is XRP.
	if got := currencySpecFromAmount("1000000"); got.Currency != "XRP" || got.Issuer != "" {
		t.Errorf("string amount = %+v, want XRP", got)
	}
	// An object amount carries currency + issuer.
	iou := map[string]interface{}{"currency": "USD", "issuer": "rIssuer", "value": "10"}
	if got := currencySpecFromAmount(iou); got.Currency != "USD" || got.Issuer != "rIssuer" {
		t.Errorf("iou amount = %+v", got)
	}
	// Anything else is empty.
	if got := currencySpecFromAmount(nil); got.Currency != "" || got.Issuer != "" {
		t.Errorf("nil amount = %+v, want empty", got)
	}
}

func TestParseVLLength(t *testing.T) {
	cases := []struct {
		name     string
		data     []byte
		wantLen  int
		wantPfix int
	}{
		{"empty", nil, 0, 0},
		{"single byte", []byte{100}, 100, 1},
		{"boundary 192", []byte{192}, 192, 1},
		{"two byte", []byte{193, 0}, 193, 2},
		{"two byte truncated", []byte{200}, 0, 0},
		{"three byte", []byte{241, 0, 0}, 12481, 3},
		{"three byte truncated", []byte{250, 0}, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotLen, gotPfix := parseVLLength(tc.data)
			if gotLen != tc.wantLen || gotPfix != tc.wantPfix {
				t.Errorf("parseVLLength(%v) = (%d,%d) want (%d,%d)", tc.data, gotLen, gotPfix, tc.wantLen, tc.wantPfix)
			}
		})
	}
}

func TestMetaTransactionResult(t *testing.T) {
	if got := metaTransactionResult(nil); got != "tesSUCCESS" {
		t.Errorf("nil meta = %q, want default tesSUCCESS", got)
	}
	if got := metaTransactionResult(json.RawMessage(`{"TransactionResult":"tecUNFUNDED"}`)); got != "tecUNFUNDED" {
		t.Errorf("explicit result = %q", got)
	}
	if got := metaTransactionResult(json.RawMessage(`{"Other":1}`)); got != "tesSUCCESS" {
		t.Errorf("missing field = %q, want default", got)
	}
	if got := metaTransactionResult(json.RawMessage(`not json`)); got != "tesSUCCESS" {
		t.Errorf("invalid json = %q, want default", got)
	}
}

func TestDecodeTxWithMetaToJSON(t *testing.T) {
	// Empty input yields empty JSON objects.
	txJSON, metaJSON := decodeTxWithMetaToJSON(nil)
	if string(txJSON) != "{}" || string(metaJSON) != "{}" {
		t.Fatalf("empty input = (%s,%s)", txJSON, metaJSON)
	}

	blob, err := hex.DecodeString(feeSettingsHex(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(blob) > 192 {
		t.Fatalf("blob too long for single-byte VL framing: %d", len(blob))
	}

	// Tx field only (single-byte VL prefix == length).
	data := append([]byte{byte(len(blob))}, blob...)
	txJSON, metaJSON = decodeTxWithMetaToJSON(data)
	if !strings.Contains(string(txJSON), "FeeSettings") {
		t.Errorf("tx not decoded: %s", txJSON)
	}
	if string(metaJSON) != "{}" {
		t.Errorf("expected empty meta, got %s", metaJSON)
	}

	// Tx + meta fields.
	withMeta := append(data, byte(len(blob)))
	withMeta = append(withMeta, blob...)
	txJSON, metaJSON = decodeTxWithMetaToJSON(withMeta)
	if !strings.Contains(string(txJSON), "FeeSettings") || !strings.Contains(string(metaJSON), "FeeSettings") {
		t.Errorf("tx+meta not decoded: tx=%s meta=%s", txJSON, metaJSON)
	}
}

func TestExtractBookPairsFromTxData(t *testing.T) {
	// No data → no pairs; a blob with no Offer-bearing metadata → no pairs.
	if got := extractBookPairsFromTxData(nil); got != nil {
		t.Errorf("nil data = %+v, want nil", got)
	}
	blob, _ := hex.DecodeString(feeSettingsHex(t))
	data := append([]byte{byte(len(blob))}, blob...)
	if got := extractBookPairsFromTxData(data); got != nil {
		t.Errorf("non-offer meta = %+v, want nil", got)
	}
}

func TestToCleanerStatus(t *testing.T) {
	in := cleaner.Status{
		State:          "running",
		MinLedger:      10,
		MaxLedger:      20,
		CheckNodes:     true,
		Failures:       2,
		LedgersChecked: 5,
		NodesChecked:   100,
		MissingNodes:   1,
		LastError:      "boom",
	}
	got := toCleanerStatus(in)
	want := types.LedgerCleanerStatus{
		State:          "running",
		MinLedger:      10,
		MaxLedger:      20,
		CheckNodes:     true,
		Failures:       2,
		LedgersChecked: 5,
		NodesChecked:   100,
		MissingNodes:   1,
		LastError:      "boom",
	}
	if got != want {
		t.Errorf("toCleanerStatus = %+v want %+v", got, want)
	}
}

func TestBuildProposedTxEvent_NoBlob(t *testing.T) {
	ev := buildProposedTxEvent(service.SubmittedTxEvent{
		CurrentLedger: 7,
		Result:        service.Result{Code: 0, Name: "tesSUCCESS", Message: "The transaction was applied."},
	})
	if ev == nil {
		t.Fatal("nil event")
	}
	if ev.EngineResult != "tesSUCCESS" || ev.LedgerCurrentIndex != 7 {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.Account != "" || string(ev.Transaction) != "{}" {
		t.Errorf("no-blob event should carry empty account/tx: %+v", ev)
	}
}

func TestBuildManifestEvent_Nil(t *testing.T) {
	if ev := buildManifestEvent(nil); ev != nil {
		t.Errorf("nil manifest should yield nil event, got %+v", ev)
	}
}

func TestAcceptedLedgerView_Nil(t *testing.T) {
	v := newAcceptedLedgerView(nil)
	if v.Sequence() != 0 || v.Hash() != ([32]byte{}) || v.CloseTime() != 0 || v.IsValidated() {
		t.Error("nil view should return zero values")
	}
	// ForEachTransaction must not panic and must visit nothing.
	if err := v.ForEachTransaction(func([32]byte, []byte) bool { t.Fatal("callback ran on nil view"); return true }); err != nil {
		t.Errorf("ForEachTransaction on nil view: %v", err)
	}
}

func TestAcceptedLedgerView_Populated(t *testing.T) {
	closeTime := time.Unix(protocol.RippleEpochUnix+1000, 0).UTC()
	event := &service.LedgerAcceptedEvent{
		LedgerInfo: &service.LedgerInfo{
			Sequence:  42,
			Hash:      [32]byte{0xAB},
			CloseTime: closeTime,
			Validated: true,
		},
		TransactionResults: []service.TransactionResultEvent{
			{TxHash: [32]byte{0x01}, TxData: []byte{0xDE, 0xAD}},
			{TxHash: [32]byte{0x02}, TxData: []byte{0xBE, 0xEF}},
		},
	}
	v := newAcceptedLedgerView(event)
	if v.Sequence() != 42 {
		t.Errorf("Sequence = %d", v.Sequence())
	}
	if v.Hash() != ([32]byte{0xAB}) {
		t.Errorf("Hash = %x", v.Hash())
	}
	if v.CloseTime() != 1000 {
		t.Errorf("CloseTime = %d want 1000", v.CloseTime())
	}
	if !v.IsValidated() {
		t.Error("IsValidated = false")
	}

	var visited int
	if err := v.ForEachTransaction(func(h [32]byte, d []byte) bool { visited++; return true }); err != nil {
		t.Fatalf("ForEachTransaction: %v", err)
	}
	if visited != 2 {
		t.Errorf("visited %d transactions, want 2", visited)
	}

	// Returning false from the callback stops iteration early.
	visited = 0
	_ = v.ForEachTransaction(func(h [32]byte, d []byte) bool { visited++; return false })
	if visited != 1 {
		t.Errorf("early-stop visited %d, want 1", visited)
	}
}
