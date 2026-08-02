package node

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/ledger/cleaner"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
	txcore "github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
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

func TestBuildLedgerCloseEventUsesSourceLedgerAndWirePresence(t *testing.T) {
	for _, test := range []struct {
		name       string
		xrpFees    bool
		wantFeeRef bool
	}{
		{name: "legacy", wantFeeRef: true},
		{name: "xrp fees", xrpFees: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			genesisConfig := genesis.DefaultConfig()
			if test.xrpFees {
				genesisConfig.Amendments = append(genesisConfig.Amendments, amendment.FeatureXRPFees)
			}
			svc, err := service.New(service.Config{
				Standalone:    true,
				Startup:       service.StartupConfig{Mode: service.StartupFresh},
				GenesisConfig: genesisConfig,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := svc.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(svc.Stop)
			source := svc.GetValidatedLedger()
			if source == nil {
				t.Fatal("missing validated source ledger")
			}
			hash := source.Hash()
			event := &service.LedgerAcceptedEvent{
				Ledger: source,
				LedgerInfo: &service.LedgerInfo{
					Sequence:  source.Sequence(),
					Hash:      hash,
					CloseTime: source.CloseTime(),
				},
				TransactionResults: make([]service.TransactionResultEvent, 2),
			}
			got := buildLedgerCloseEvent(event, service.ServerInfo{
				ServerState:     "full",
				CompleteLedgers: "1-2,4",
				NetworkID:       0,
			})
			if got == nil {
				t.Fatal("nil ledger event")
			}
			base, reserve, increment := service.FeesFromLedger(source)
			if got.FeeBase != base || got.ReserveBase != reserve || got.ReserveInc != increment {
				t.Fatalf("fees = (%d,%d,%d), want source ledger (%d,%d,%d)", got.FeeBase, got.ReserveBase, got.ReserveInc, base, reserve, increment)
			}
			if (got.FeeRef != nil) != test.wantFeeRef {
				t.Fatalf("fee_ref presence = %t, want %t", got.FeeRef != nil, test.wantFeeRef)
			}
			if got.FeeRef != nil && *got.FeeRef != deprecatedFeeReferenceUnits {
				t.Fatalf("fee_ref = %d, want %d", *got.FeeRef, deprecatedFeeReferenceUnits)
			}
			if got.NetworkID != 0 || got.ValidatedLedgers != "1-2,4" || got.TxnCount != 2 {
				t.Fatalf("event fields = %+v", got)
			}

			subscribeInfo := (&ledgerInfoAdapter{ledgerService: svc}).GetCurrentLedgerInfo()
			if subscribeInfo == nil {
				t.Fatal("nil ledger subscribe info")
			}
			if subscribeInfo.FeeBase != base || subscribeInfo.ReserveBase != reserve || subscribeInfo.ReserveInc != increment {
				t.Fatalf("subscribe fees = (%d,%d,%d), want validated ledger (%d,%d,%d)", subscribeInfo.FeeBase, subscribeInfo.ReserveBase, subscribeInfo.ReserveInc, base, reserve, increment)
			}
			if subscribeInfo.FeeRef != deprecatedFeeReferenceUnits || subscribeInfo.XRPFeesEnabled != test.xrpFees {
				t.Fatalf("subscribe fee gate = ref %d, XRPFees %t", subscribeInfo.FeeRef, subscribeInfo.XRPFeesEnabled)
			}
		})
	}
}

func TestServerPublishesValidatedRange(t *testing.T) {
	for _, state := range []string{"syncing", "tracking", "full", "proposing", "validating"} {
		if !serverPublishesValidatedRange(state) {
			t.Errorf("%s must publish validated range", state)
		}
	}
	for _, state := range []string{"", "disconnected", "connected"} {
		if serverPublishesValidatedRange(state) {
			t.Errorf("%s must omit validated range", state)
		}
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
	mpt := map[string]interface{}{"mpt_issuance_id": "ABCDEF", "value": "10"}
	if got := currencySpecFromAmount(mpt); got.MPTIssuanceID != "ABCDEF" || got.Currency != "" || got.Issuer != "" {
		t.Errorf("mpt amount = %+v", got)
	}
	// Anything else is empty.
	if got := currencySpecFromAmount(nil); got.Currency != "" || got.Issuer != "" {
		t.Errorf("nil amount = %+v, want empty", got)
	}
}

func TestDecodeTxWithMetaToJSON(t *testing.T) {
	data := validatedPaymentData(t, 2_048, 7)
	txJSON, metaJSON, err := decodeTxWithMetaToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(txJSON), `"TransactionType":"Payment"`) {
		t.Fatalf("transaction not decoded: %s", txJSON)
	}
	if !strings.Contains(string(metaJSON), `"TransactionResult":"tecUNFUNDED_PAYMENT"`) {
		t.Fatalf("metadata not decoded: %s", metaJSON)
	}

	txBlob, metaBlob, err := txcore.SplitTxWithMetaBlobStrict(data)
	if err != nil {
		t.Fatal(err)
	}
	txField, err := txcore.EncodeWithVL(txBlob)
	if err != nil {
		t.Fatal(err)
	}

	unknownResult := append([]byte(nil), metaBlob...)
	if len(unknownResult) < 3 || !bytes.Equal(unknownResult[len(unknownResult)-3:len(unknownResult)-1], []byte{0x03, 0x10}) {
		t.Fatalf("unexpected TransactionResult encoding: %X", unknownResult)
	}
	unknownResult[len(unknownResult)-1] = 106
	unknownResultField, err := txcore.EncodeWithVL(unknownResult)
	if err != nil {
		t.Fatal(err)
	}
	leafWithMeta := func(meta map[string]any) []byte {
		metaHex, err := binarycodec.Encode(meta)
		if err != nil {
			t.Fatal(err)
		}
		metaBlob, err := hex.DecodeString(metaHex)
		if err != nil {
			t.Fatal(err)
		}
		metaField, err := txcore.EncodeWithVL(metaBlob)
		if err != nil {
			t.Fatal(err)
		}
		return append(append([]byte(nil), txField...), metaField...)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "nil"},
		{name: "reserved variable length", data: []byte{0xff}},
		{name: "truncated transaction", data: []byte{2, 0x12}},
		{name: "missing metadata", data: txField},
		{name: "empty metadata", data: append(append([]byte(nil), txField...), 0)},
		{name: "truncated metadata", data: append(append([]byte(nil), txField...), 2, 0x12)},
		{name: "trailing data", data: append(append([]byte(nil), data...), 0)},
		{name: "invalid transaction", data: []byte{1, 0, 1, 0}},
		{name: "invalid metadata", data: append(append([]byte(nil), txField...), 1, 0)},
		{name: "missing affected nodes", data: leafWithMeta(map[string]any{"TransactionIndex": uint32(7), "TransactionResult": "tesSUCCESS"})},
		{name: "missing transaction index", data: leafWithMeta(map[string]any{"AffectedNodes": []any{}, "TransactionResult": "tesSUCCESS"})},
		{name: "missing transaction result", data: leafWithMeta(map[string]any{"AffectedNodes": []any{}, "TransactionIndex": uint32(7)})},
		{name: "unknown transaction result", data: append(append([]byte(nil), txField...), unknownResultField...)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			txJSON, metaJSON, err := decodeTxWithMetaToJSON(tc.data)
			if err == nil {
				t.Fatalf("decode succeeded: tx=%s meta=%s", txJSON, metaJSON)
			}
			if txJSON != nil || metaJSON != nil {
				t.Fatalf("corrupt leaf returned JSON: tx=%s meta=%s", txJSON, metaJSON)
			}
		})
	}
}

func TestMetaTransactionResultRejectsInvalidMetadata(t *testing.T) {
	tests := []json.RawMessage{
		nil,
		json.RawMessage(`not json`),
		json.RawMessage(`{}`),
		json.RawMessage(`{"TransactionResult":""}`),
		json.RawMessage(`{"TransactionResult":"tecFUTURE"}`),
	}
	for _, metadata := range tests {
		if result, err := metaTransactionResult(metadata); err == nil {
			t.Fatalf("metadata %s returned %s", metadata, result)
		}
	}
	result, err := metaTransactionResult(json.RawMessage(`{"TransactionResult":"tecUNFUNDED_PAYMENT"}`))
	if err != nil {
		t.Fatal(err)
	}
	if result != ter.TecUNFUNDED_PAYMENT {
		t.Fatalf("result = %s", result)
	}
}

func TestExtractBookPairsFromMetadataUsesAffectedNodeFields(t *testing.T) {
	const (
		issuer = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
		mptID  = "00000001C4F149B6F2A4B6A4C4A01C1570C4A040A3D9B221"
	)
	domain := strings.Repeat("A", 64)
	meta := []byte(`{"AffectedNodes":[` +
		`{"ModifiedNode":{"LedgerEntryType":"Offer","PreviousFields":{"TakerGets":{"mpt_issuance_id":"` + mptID + `","value":"10"},"TakerPays":"20","DomainID":"` + domain + `"},"FinalFields":{"TakerGets":{"currency":"BAD","issuer":"` + issuer + `","value":"1"},"TakerPays":"1"}}},` +
		`{"CreatedNode":{"LedgerEntryType":"Offer","NewFields":{"TakerGets":{"currency":"USD","issuer":"` + issuer + `","value":"2"},"TakerPays":"3"},"FinalFields":{"TakerGets":{"currency":"BAD","issuer":"` + issuer + `","value":"1"},"TakerPays":"1"}}},` +
		`{"DeletedNode":{"LedgerEntryType":"Offer","NewFields":{"TakerGets":{"currency":"BAD","issuer":"` + issuer + `","value":"1"},"TakerPays":"1"},"FinalFields":{"TakerGets":"4","TakerPays":{"currency":"EUR","issuer":"` + issuer + `","value":"5"}}}}` +
		`]}`)

	books := extractBookPairsFromMetadata(meta)
	if len(books) != 3 {
		t.Fatalf("got %d books, want 3: %+v", len(books), books)
	}
	if books[0].TakerGets.MPTIssuanceID != mptID || books[0].TakerPays.Currency != "XRP" || books[0].Domain != domain {
		t.Errorf("modified book = %+v", books[0])
	}
	if books[1].TakerGets.Currency != "USD" || books[1].TakerGets.Issuer != issuer || books[1].TakerPays.Currency != "XRP" {
		t.Errorf("created book = %+v", books[1])
	}
	if books[2].TakerGets.Currency != "XRP" || books[2].TakerPays.Currency != "EUR" || books[2].TakerPays.Issuer != issuer {
		t.Errorf("deleted book = %+v", books[2])
	}
}

func TestToCleanerStatus(t *testing.T) {
	in := cleaner.Status{
		State:          "running",
		MinLedger:      10,
		MaxLedger:      20,
		CheckNodes:     true,
		FixTxns:        true,
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
		FixTxns:        true,
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
	if string(ev.Transaction) != "{}" {
		t.Errorf("no-blob event should carry empty tx: %+v", ev)
	}
}

func TestBuildProposedTxEventCarriesHashAndOwnerFunds(t *testing.T) {
	blobHex, err := binarycodec.Encode(map[string]any{
		"TransactionType": "OfferCreate",
		"Account":         "r9cZA1mLK5R5Am25ArfXFmqgNwjZgnfk59",
		"TakerGets":       "1",
		"TakerPays":       "2",
	})
	if err != nil {
		t.Fatal(err)
	}
	blob, err := hex.DecodeString(blobHex)
	if err != nil {
		t.Fatal(err)
	}
	txHash := [32]byte{0xAB, 0xCD}
	event := buildProposedTxEvent(service.SubmittedTxEvent{
		RawBlob:    blob,
		TxHash:     txHash,
		OwnerFunds: "123",
	})

	if event.Hash != strings.ToUpper(hex.EncodeToString(txHash[:])) {
		t.Fatalf("hash = %q", event.Hash)
	}
	var txJSON map[string]any
	if err := json.Unmarshal(event.Transaction, &txJSON); err != nil {
		t.Fatal(err)
	}
	if txJSON["owner_funds"] != "123" {
		t.Fatalf("owner_funds = %v", txJSON["owner_funds"])
	}
}

func TestBuildManifestEvent_Nil(t *testing.T) {
	if ev := buildManifestEvent(nil); ev != nil {
		t.Errorf("nil manifest should yield nil event, got %+v", ev)
	}
}

type manifestPublisherSpy struct {
	subscribers int
	stream      types.SubscriptionType
	events      []*rpc.ManifestEvent
}

func unverifiedManifest(t testing.TB, sequence uint32) *manifest.Manifest {
	t.Helper()
	encoded, err := binarycodec.Encode(map[string]any{
		"PublicKey":       "ED" + strings.Repeat("00", 32),
		"SigningPubKey":   "ED" + strings.Repeat("01", 32),
		"Sequence":        sequence,
		"MasterSignature": "00",
		"Signature":       "00",
	})
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	wire, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	parsed, err := manifest.Deserialize(wire)
	if err != nil {
		t.Fatalf("deserialize manifest: %v", err)
	}
	return parsed
}

func (p *manifestPublisherSpy) PublishManifest(event *rpc.ManifestEvent) {
	p.events = append(p.events, event)
}

func (p *manifestPublisherSpy) GetSubscriberCount(stream types.SubscriptionType) int {
	p.stream = stream
	return p.subscribers
}

func TestPublishManifestIfSubscribed(t *testing.T) {
	m := unverifiedManifest(t, 7)

	withoutSubscribers := &manifestPublisherSpy{}
	publishManifestIfSubscribed(withoutSubscribers, m)
	if withoutSubscribers.stream != types.SubManifests {
		t.Fatalf("subscriber stream = %q, want %q", withoutSubscribers.stream, types.SubManifests)
	}
	if len(withoutSubscribers.events) != 0 {
		t.Fatalf("published %d events without subscribers", len(withoutSubscribers.events))
	}

	withSubscriber := &manifestPublisherSpy{subscribers: 1}
	publishManifestIfSubscribed(withSubscriber, m)
	if len(withSubscriber.events) != 1 {
		t.Fatalf("published %d events, want 1", len(withSubscriber.events))
	}
	if withSubscriber.events[0] == nil || withSubscriber.events[0].Sequence != m.Sequence() {
		t.Fatalf("published event = %+v", withSubscriber.events[0])
	}
}

func BenchmarkPublishManifestWithoutSubscribers(b *testing.B) {
	publisher := &manifestPublisherSpy{}
	m := unverifiedManifest(b, 7)
	b.ReportAllocs()
	for b.Loop() {
		publishManifestIfSubscribed(publisher, m)
	}
}

func TestUpperHex(t *testing.T) {
	// Mirrors rippled strHex/to_string (boost::algorithm::hex): hex
	// digits A-F are uppercase so stream fields agree with the RPC
	// layer and with other nodes' streams (#787).
	if got := upperHex([]byte{0xde, 0xad, 0xbe, 0xef}); got != "DEADBEEF" {
		t.Errorf("upperHex = %q, want DEADBEEF", got)
	}
	if got := upperHex(nil); got != "" {
		t.Errorf("upperHex(nil) = %q, want empty", got)
	}
}

func mustBuildValidationEvent(t *testing.T, v *consensus.Validation, networkID uint32) *rpc.ValidationEvent {
	t.Helper()
	event, err := buildValidationEvent(&consensus.ValidationReceivedEvent{Validation: v}, nil, networkID)
	if err != nil {
		t.Fatalf("build validation event: %v", err)
	}
	return event
}

func TestBuildValidationEvent_UppercaseHexFields(t *testing.T) {
	// Bytes chosen so every hex field contains A-F digits, exposing any
	// lowercase formatting in the stream layer (#787).
	v := &consensus.Validation{
		LedgerID:      consensus.LedgerID{0xab, 0xcd},
		LedgerSeq:     42,
		Signature:     []byte{0xde, 0xad, 0xbe, 0xef},
		Amendments:    [][32]byte{{0xfe, 0xed}, {0xba, 0xbe}},
		ValidatedHash: [32]byte{0xca, 0xfe},
	}
	ev := mustBuildValidationEvent(t, v, 0)
	if ev == nil {
		t.Fatal("nil event")
	}
	for name, got := range map[string]string{
		"ledger_hash":    ev.LedgerHash,
		"signature":      ev.Signature,
		"data":           ev.Data,
		"validated_hash": ev.ValidatedHash,
	} {
		if got == "" {
			t.Errorf("%s is empty", name)
		}
		if got != strings.ToUpper(got) {
			t.Errorf("%s = %q is not uppercase", name, got)
		}
	}
	for i, a := range ev.Amendments {
		if a != strings.ToUpper(a) {
			t.Errorf("amendments[%d] = %q is not uppercase", i, a)
		}
	}
	if want := strings.ToUpper(hex.EncodeToString(v.LedgerID[:])); ev.LedgerHash != want {
		t.Errorf("ledger_hash = %q want %q", ev.LedgerHash, want)
	}
}

func TestBuildValidationEvent_RequiredFieldsAndCloseTimePresence(t *testing.T) {
	withoutClose := mustBuildValidationEvent(t, &consensus.Validation{LedgerSeq: 42}, 0)
	encoded, err := json.Marshal(withoutClose)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if got, ok := fields["network_id"]; !ok || string(got) != "0" {
		t.Fatalf("network_id = %s, present=%t; want explicit 0", got, ok)
	}
	if got, ok := fields["data"]; !ok || string(got) == `""` {
		t.Fatalf("data = %s, present=%t; want canonical validation bytes", got, ok)
	}
	if _, ok := fields["close_time"]; ok {
		t.Fatalf("close_time must be absent: %s", encoded)
	}

	withEpochClose := mustBuildValidationEvent(t, &consensus.Validation{
		LedgerSeq: 42,
		CloseTime: time.Unix(protocol.RippleEpochUnix, 0).UTC(),
	}, 0)
	encoded, err = json.Marshal(withEpochClose)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	if got, ok := fields["close_time"]; !ok || string(got) != "0" {
		t.Fatalf("close_time = %s, present=%t; want explicit 0", got, ok)
	}
}

func TestBuildValidationEventRejectsInvalidRawData(t *testing.T) {
	_, err := buildValidationEvent(&consensus.ValidationReceivedEvent{
		Validation: &consensus.Validation{Raw: []byte{0xff}},
	}, nil, 0)
	if err == nil {
		t.Fatal("invalid raw validation must not be published")
	}
}

func TestBuildValidationEvent_CookieDecimal(t *testing.T) {
	// rippled emits cookie as std::to_string(*cookie) — base-10 decimal
	// (NetworkOPs.cpp:2429), unlike the hash/sig/blob fields which are
	// hex. 0xAB = 171: "171" (decimal) ≠ "ab"/"AB" (hex).
	v := &consensus.Validation{Cookie: 0xAB}
	ev := mustBuildValidationEvent(t, v, 0)
	if ev.Cookie != "171" {
		t.Errorf("cookie = %q, want decimal \"171\"", ev.Cookie)
	}

	// Cookie 0 is the absent proxy → field omitted.
	v0 := &consensus.Validation{}
	if ev0 := mustBuildValidationEvent(t, v0, 0); ev0.Cookie != "" {
		t.Errorf("cookie = %q, want empty when absent", ev0.Cookie)
	}
}

func TestBuildValidationEvent_ServerVersionDecimal(t *testing.T) {
	// rippled emits server_version as std::to_string(*version) — base-10
	// decimal (NetworkOPs.cpp:2426). The go-xrpl server version tag
	// 0x4000_0000_0000_0000 = 4611686018427387904 decimal.
	v := &consensus.Validation{ServerVersion: 0x4000_0000_0000_0000}
	ev := mustBuildValidationEvent(t, v, 0)
	if ev.ServerVersion != "4611686018427387904" {
		t.Errorf("server_version = %q, want decimal \"4611686018427387904\"", ev.ServerVersion)
	}

	// ServerVersion 0 is the absent proxy → field omitted.
	v0 := &consensus.Validation{}
	if ev0 := mustBuildValidationEvent(t, v0, 0); ev0.ServerVersion != "" {
		t.Errorf("server_version = %q, want empty when absent", ev0.ServerVersion)
	}
}

func TestBuildValidationEvent_LoadFeePresence(t *testing.T) {
	explicitZero := &consensus.Validation{}
	explicitZero.SetLoadFee(0)
	tests := []struct {
		name       string
		validation *consensus.Validation
		wantJSON   string
	}{
		{name: "absent", validation: &consensus.Validation{}},
		{name: "explicit zero", validation: explicitZero, wantJSON: "0"},
		{name: "legacy non-zero literal", validation: &consensus.Validation{LoadFee: 320}, wantJSON: "320"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := mustBuildValidationEvent(t, test.validation, 0)
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal validation event: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("decode validation event: %v", err)
			}

			loadFee, present := fields["load_fee"]
			if test.wantJSON == "" {
				if event.LoadFee != nil || present {
					t.Fatalf("load_fee must be omitted: event=%v json=%s", event.LoadFee, encoded)
				}
				return
			}
			if event.LoadFee == nil || !present {
				t.Fatalf("load_fee missing: event=%v json=%s", event.LoadFee, encoded)
			}
			if got := string(loadFee); got != test.wantJSON {
				t.Fatalf("load_fee JSON = %s; want %s", got, test.wantJSON)
			}
		})
	}
}

func TestBuildValidationEvent_FeeVotePresenceAndSign(t *testing.T) {
	legacyZero := &consensus.Validation{}
	legacyZero.SetBaseFee(0)
	legacyZero.SetReserveBase(0)
	legacyZero.SetReserveIncrement(0)

	modern := &consensus.Validation{}
	modern.SetBaseFeeDrops(drops.XRPAmount(-15))
	modern.SetReserveBaseDrops(0)
	modern.SetReserveIncrementDrops(20)

	nonNative := &consensus.Validation{}
	nonNative.SetBaseFeeDropsNonNative()

	legacyWithNonNative := &consensus.Validation{}
	legacyWithNonNative.SetBaseFee(10)
	legacyWithNonNative.SetBaseFeeDropsNonNative()

	both := &consensus.Validation{}
	both.SetBaseFee(10)
	both.SetReserveBase(11)
	both.SetReserveIncrement(12)
	both.SetBaseFeeDrops(-15)
	both.SetReserveBaseDrops(0)
	both.SetReserveIncrementDrops(20)

	clipped := &consensus.Validation{}
	clipped.SetBaseFeeDrops(-drops.MaxDrops)
	clipped.SetReserveBaseDrops(drops.MaxDrops)

	tests := []struct {
		name       string
		validation *consensus.Validation
		want       map[string]string
	}{
		{name: "absent", validation: &consensus.Validation{}, want: map[string]string{}},
		{
			name:       "legacy explicit zero",
			validation: legacyZero,
			want:       map[string]string{"base_fee": "0", "reserve_base": "0", "reserve_inc": "0"},
		},
		{
			name:       "modern signed and zero",
			validation: modern,
			want:       map[string]string{"base_fee": "-15", "reserve_base": "0", "reserve_inc": "20"},
		},
		{
			name:       "modern fields override legacy",
			validation: both,
			want:       map[string]string{"base_fee": "-15", "reserve_base": "0", "reserve_inc": "20"},
		},
		{
			name:       "modern values are clipped to JSON integers",
			validation: clipped,
			want:       map[string]string{"base_fee": "-2147483648", "reserve_base": "2147483647"},
		},
		{name: "non native omitted", validation: nonNative, want: map[string]string{}},
		{
			name:       "non native modern field does not replace legacy",
			validation: legacyWithNonNative,
			want:       map[string]string{"base_fee": "10"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := mustBuildValidationEvent(t, test.validation, 0)
			encoded, err := json.Marshal(event)
			if err != nil {
				t.Fatalf("marshal validation event: %v", err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &fields); err != nil {
				t.Fatalf("decode validation event: %v", err)
			}
			for _, field := range []string{"base_fee", "reserve_base", "reserve_inc"} {
				got, present := fields[field]
				want, expected := test.want[field]
				if present != expected {
					t.Fatalf("%s presence = %t, want %t: %s", field, present, expected, encoded)
				}
				if expected && string(got) != want {
					t.Fatalf("%s = %s, want %s", field, got, want)
				}
			}
		})
	}
}

func TestAcceptedLedgerView_Nil(t *testing.T) {
	v := newAcceptedLedgerView(nil, nil)
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
	v := newAcceptedLedgerView(event, event.TransactionResults)
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

func TestAcceptedLedgerViewUsesPublishableTransactions(t *testing.T) {
	event := &service.LedgerAcceptedEvent{
		LedgerInfo: &service.LedgerInfo{Sequence: 42},
		TransactionResults: []service.TransactionResultEvent{
			{TxHash: [32]byte{1}, TxData: []byte("corrupt")},
			{TxHash: [32]byte{2}, TxData: []byte("valid")},
		},
	}
	v := newAcceptedLedgerView(event, event.TransactionResults[1:])
	var hashes [][32]byte
	if err := v.ForEachTransaction(func(hash [32]byte, _ []byte) bool {
		hashes = append(hashes, hash)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if len(hashes) != 1 || hashes[0] != ([32]byte{2}) {
		t.Fatalf("visited hashes = %v, want only publishable transaction", hashes)
	}
}

// TestNodeStoreCacheParams guards the node_db cache_size / cache_age
// wiring into the node-object cache.
func TestNodeStoreCacheParams(t *testing.T) {
	size, age := nodeStoreCacheParams(config.NodeDBConfig{}, "")
	if size != defaultNodeCacheSize || age != defaultNodeCacheAge {
		t.Errorf("defaults = (%d, %v), want (%d, %v)", size, age, defaultNodeCacheSize, defaultNodeCacheAge)
	}

	size, age = nodeStoreCacheParams(config.NodeDBConfig{CacheSize: 16384, CacheAge: 5}, "tiny")
	if size != 16384 {
		t.Errorf("cache size = %d, want 16384", size)
	}
	if age != 5*time.Minute {
		t.Errorf("cache age = %v, want 5m", age)
	}

	size, age = nodeStoreCacheParams(config.NodeDBConfig{}, "huge")
	if size != 8_388_608 || age != 900*time.Minute {
		t.Errorf("huge profile = (%d, %v)", size, age)
	}
}

func TestPebbleStoreOptions(t *testing.T) {
	options, err := pebbleStoreOptions(config.NodeDBConfig{})
	if err != nil {
		t.Fatalf("defaults: %v", err)
	}
	if options.BlockCacheBytes != kvpebble.DefaultBlockCacheBytes {
		t.Errorf("default block cache = %d, want %d", options.BlockCacheBytes, kvpebble.DefaultBlockCacheBytes)
	}
	if options.MaxOpenFiles != kvpebble.DefaultMaxOpenFiles {
		t.Errorf("default open files = %d, want %d", options.MaxOpenFiles, kvpebble.DefaultMaxOpenFiles)
	}

	options, err = pebbleStoreOptions(config.NodeDBConfig{CacheMB: 2048, OpenFiles: 1000})
	if err != nil {
		t.Fatalf("custom options: %v", err)
	}
	if options.BlockCacheBytes != 2048*(1<<20) {
		t.Errorf("block cache = %d, want %d", options.BlockCacheBytes, int64(2048*(1<<20)))
	}
	if options.MaxOpenFiles != 1000 {
		t.Errorf("open files = %d, want 1000", options.MaxOpenFiles)
	}

	for _, invalid := range []config.NodeDBConfig{
		{CacheMB: -1},
		{CacheMB: math.MaxInt64/(1<<20) + 1},
		{OpenFiles: -1},
	} {
		if _, err := pebbleStoreOptions(invalid); err == nil {
			t.Errorf("pebbleStoreOptions(%+v) succeeded, want error", invalid)
		}
	}
}
