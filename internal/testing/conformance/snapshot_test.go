package conformance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
)

func minimalSnapshotFixture() snapshotFixture {
	return snapshotFixture{
		FixtureVersion:   snapshotFixtureVersion,
		OracleRepository: snapshotOracleRepo,
		OracleTag:        snapshotOracleTag,
		OracleCommit:     snapshotOracleCommit,
		Suite:            "app/Payment",
		Testcase:         "strict input",
		Family:           "Payment",
		Profile:          "c0-l0-b0-f0",
		TxBlob:           "00",
		TxQConfig:        snapshotTxQConfigFromConfig(txq.DefaultConfig()),
		Submit:           snapshotSubmit{Boundary: snapshotSubmitBoundary, EngineResult: ter.TesSUCCESS.String(), EngineResultCode: int(ter.TesSUCCESS), Applied: true, PostSubmitSLE: []snapshotEntry{}},
		CloseInput:       snapshotCloseInput{CloseTimeResolution: 2, TxBlobs: []string{}},
		Parent:           minimalSnapshotLedger(),
		Closed:           minimalSnapshotLedger(),
	}
}

func minimalSnapshotLedger() snapshotLedger {
	return snapshotLedger{
		Header:       "00",
		Rules:        []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		Fees:         snapshotFees{Base: "10", Reserve: "10000000", Increment: "2000000"},
		State:        []snapshotEntry{},
		Transactions: []snapshotTransaction{},
	}
}

func snapshotTxQConfigFromConfig(c txq.Config) snapshotTxQConfig {
	return snapshotTxQConfig{
		LedgersInQueue:                 c.LedgersInQueue,
		QueueSizeMin:                   c.QueueSizeMin,
		RetrySequencePercent:           c.RetrySequencePercent,
		MinimumEscalationMultiplier:    c.MinimumEscalationMultiplier,
		MinimumTxnInLedger:             c.MinimumTxnInLedger,
		MinimumTxnInLedgerStandalone:   c.MinimumTxnInLedgerStandalone,
		TargetTxnInLedger:              c.TargetTxnInLedger,
		MaximumTxnInLedger:             c.MaximumTxnInLedger,
		MaximumTxnInLedgerSet:          c.MaximumTxnInLedgerSet,
		NormalConsensusIncreasePercent: c.NormalConsensusIncreasePercent,
		SlowConsensusDecreasePercent:   c.SlowConsensusDecreasePercent,
		MaximumTxnPerAccount:           c.MaximumTxnPerAccount,
		MinimumLastLedgerBuffer:        c.MinimumLastLedgerBuffer,
		Standalone:                     c.Standalone,
	}
}

func TestDecodeSnapshotFixtureRejectsUnknownAndMissingFields(t *testing.T) {
	fixture := minimalSnapshotFixture()
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["depends_on"] = "other.json"
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotFixture(data); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown field accepted: %v", err)
	}

	delete(object, "tx_blob")
	delete(object, "depends_on")
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotFixture(data); err == nil || !strings.Contains(err.Error(), "tx_blob is missing") {
		t.Fatalf("missing required field accepted: %v", err)
	}

	fixture = minimalSnapshotFixture()
	data, err = json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	object["network_id"] = nil
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotFixture(data); err == nil || !strings.Contains(err.Error(), "network_id must not be null") {
		t.Fatalf("null required field accepted: %v", err)
	}
}

func TestSnapshotQueueJSONRequiresCompleteMetricsAndAllowsNullMaxSize(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil {
		t.Fatal(err)
	}
	submit := object["submit"].(map[string]any)
	metrics := map[string]any{
		"tx_count":                 1,
		"max_size":                 nil,
		"tx_in_ledger":             1,
		"tx_per_ledger":            1,
		"reference_fee_level":      256,
		"min_processing_fee_level": 256,
		"med_fee_level":            128000,
		"open_ledger_fee_level":    256,
	}
	submit["queue"] = map[string]any{
		"tx_blobs": []string{fixture.TxBlob},
		"metrics":  metrics,
	}
	marshal := func(value map[string]any) []byte {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	if _, err := decodeSnapshotFixture(marshal(object)); err != nil {
		t.Fatalf("valid queue with null max_size rejected: %v", err)
	}

	missingMax := map[string]any{}
	for key, value := range metrics {
		missingMax[key] = value
	}
	delete(missingMax, "max_size")
	submit["queue"].(map[string]any)["metrics"] = missingMax
	if _, err := decodeSnapshotFixture(marshal(object)); err == nil || !strings.Contains(err.Error(), "max_size is missing") {
		t.Fatalf("missing max_size accepted: %v", err)
	}

	negativeMax := map[string]any{}
	for key, value := range metrics {
		negativeMax[key] = value
	}
	negativeMax["max_size"] = -1
	submit["queue"].(map[string]any)["metrics"] = negativeMax
	if _, err := decodeSnapshotFixture(marshal(object)); err == nil || !strings.Contains(err.Error(), "non-negative integer") {
		t.Fatalf("negative max_size accepted: %v", err)
	}

	unknown := map[string]any{}
	for key, value := range metrics {
		unknown[key] = value
	}
	submit["queue"].(map[string]any)["metrics"] = unknown
	submit["queue"].(map[string]any)["unexpected"] = true
	if _, err := decodeSnapshotFixture(marshal(object)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown queue field accepted: %v", err)
	}
}

func TestSnapshotSubmitComparesNumericTERAppliedQueuedAndFee(t *testing.T) {
	want := snapshotSubmit{
		Boundary:         snapshotSubmitBoundary,
		EngineResult:     ter.TesSUCCESS.String(),
		EngineResultCode: int(ter.TesSUCCESS),
		Applied:          true,
		Fee:              10,
	}
	out := openledger.SubmitOutcome{
		Result:  ter.TesSUCCESS,
		Applied: true,
		Changed: true,
		Fee:     10,
	}
	if err := assertSnapshotSubmit(out, want); err != nil {
		t.Fatalf("matching submit rejected: %v", err)
	}
	for name, mutate := range map[string]func(*snapshotSubmit){
		"numeric TER": func(value *snapshotSubmit) { value.EngineResultCode = 1 },
		"applied":     func(value *snapshotSubmit) { value.Applied = false },
		"fee":         func(value *snapshotSubmit) { value.Fee = 11 },
	} {
		t.Run(name, func(t *testing.T) {
			bad := want
			mutate(&bad)
			if err := assertSnapshotSubmit(out, bad); err == nil {
				t.Fatal("accepted mismatched submit result")
			}
		})
	}
}

func TestSnapshotMapComparatorRejectsStateByteMismatch(t *testing.T) {
	want := shamap.New(shamap.TypeState)
	got := shamap.New(shamap.TypeState)
	var index [32]byte
	index[0] = 1
	if err := want.Put(index, make([]byte, 12)); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 12)
	data[0] = 1
	if err := got.Put(index, data); err != nil {
		t.Fatal(err)
	}
	if err := assertSnapshotMap(want, got); err == nil {
		t.Fatal("accepted state byte mismatch")
	}
}

func TestSnapshotMapComparatorRejectsMetadataByteMismatch(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	closed, err := loadSnapshotLedger(fixture.Closed)
	if err != nil {
		t.Fatal(err)
	}
	items, err := snapshotMapItems(closed.Txs)
	if err != nil {
		t.Fatal(err)
	}
	got := shamap.New(shamap.TypeTransaction)
	mutated := false
	for index, data := range items {
		copyData := append([]byte(nil), data...)
		if !mutated {
			copyData[0] ^= 1
			mutated = true
		}
		if err := got.PutWithNodeType(index, copyData, shamap.NodeTypeTransactionWithMeta); err != nil {
			t.Fatal(err)
		}
	}
	if err := assertSnapshotMap(closed.Txs, got); err == nil {
		t.Fatal("accepted metadata byte mismatch")
	}
}

func TestSnapshotRejectsSignatureSkipAndUnsupportedProfile(t *testing.T) {
	fixture := minimalSnapshotFixture()
	fixture.SkipSignatureVerification = true
	if err := validateSnapshotFixture(&fixture); err == nil || !strings.Contains(err.Error(), "skip_signature") {
		t.Fatalf("signature skip accepted: %v", err)
	}
	fixture = minimalSnapshotFixture()
	fixture.Profile = "legacy"
	if err := validateSnapshotFixture(&fixture); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("unsupported profile accepted: %v", err)
	}
}

func TestSnapshotOpenLedgerMutationAuthenticatesAndStaysOpen(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b0-f0-Payment-valid.json")
	if len(fixture.Parent.State) == 0 {
		t.Fatal("fixture parent has no state entry for transient mutation test")
	}
	parent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		t.Fatal(err)
	}
	view, err := openledger.New(parent.Ledger, openledger.Config{Rules: parent.EffectiveRules})
	if err != nil {
		t.Fatal(err)
	}
	entry := fixture.Parent.State[0]
	if err := applySnapshotOpenLedger(view, nil, []snapshotEntry{entry}); err != nil {
		t.Fatalf("authenticated erase: %v", err)
	}
	index, err := decodeSnapshotHash("entry.index", entry.Index)
	if err != nil {
		t.Fatal(err)
	}
	if data, err := view.Current().Read(keylet.Child(index)); err != nil {
		t.Fatal(err)
	} else if data != nil {
		t.Fatal("erased entry remained in open ledger")
	}
	if !view.Current().IsOpen() {
		t.Fatal("transient erase closed the open ledger")
	}
	if err := applySnapshotOpenLedger(view, []snapshotEntry{entry}, nil); err != nil {
		t.Fatalf("authenticated insert: %v", err)
	}
	if data, err := view.Current().Read(keylet.Child(index)); err != nil {
		t.Fatal(err)
	} else if !bytes.Equal(data, mustDecodeSnapshotTestHex(t, entry.Data)) {
		t.Fatal("inserted entry bytes differ")
	}
	wrong := entry
	wrongBytes := mustDecodeSnapshotTestHex(t, entry.Data)
	wrongBytes[len(wrongBytes)-1] ^= 1
	wrong.Data = strings.ToUpper(hex.EncodeToString(wrongBytes))
	if err := applySnapshotOpenLedger(view, nil, []snapshotEntry{wrong}); err == nil || !strings.Contains(err.Error(), "bytes differ") {
		t.Fatalf("unauthenticated erase accepted: %v", err)
	}
}

func loadSnapshotV4Fixture(t *testing.T, name string) snapshotFixture {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	path := filepath.Join(filepath.Dir(source), "testdata", "rippled-3.4.1-v4", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read v4 fixture %s: %v", path, err)
	}
	fixture, err := decodeSnapshotFixture(data)
	if err != nil {
		t.Fatalf("decode v4 fixture %s: %v", path, err)
	}
	return fixture
}

func TestSnapshotRunsRealSignedBatchFixtures(t *testing.T) {
	for _, name := range []string{
		"c0-l0-b1-f0-Batch-canonical.json",
		"c0-l0-b1-f1-Batch-poisoned-created-node-wrapper.json",
	} {
		t.Run(name, func(t *testing.T) {
			fixture := loadSnapshotV4Fixture(t, name)
			if err := runSnapshotFixture(fixture); err != nil {
				t.Fatalf("signed v4 fixture did not replay: %v", err)
			}
		})
	}
}

func TestSnapshotRejectsFamilyMismatch(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	fixture.Family = "Payment"
	if err := runSnapshotFixture(fixture); err == nil || !strings.Contains(err.Error(), "does not match tx_blob type") {
		t.Fatalf("family mismatch accepted: %v", err)
	}
}

func TestSnapshotFamilyAliasesAreExplicit(t *testing.T) {
	tests := []struct {
		family string
		txType string
		want   bool
	}{
		{family: "NFTokenAuth", txType: "NFTokenCreateOffer", want: true},
		{family: "NFTokenAuth", txType: "NFTokenAcceptOffer", want: true},
		{family: "NFTokenAuth", txType: "NFTokenCancelOffer", want: false},
		{family: "EscrowToken", txType: "EscrowCancel", want: true},
		{family: "EscrowToken", txType: "EscrowFinish", want: true},
		{family: "EscrowToken", txType: "EscrowCreate", want: false},
	}
	for _, test := range tests {
		t.Run(test.family+"/"+test.txType, func(t *testing.T) {
			if got := snapshotFamilyMatchesTxType(test.family, test.txType); got != test.want {
				t.Fatalf("snapshotFamilyMatchesTxType(%q, %q) = %t, want %t", test.family, test.txType, got, test.want)
			}
		})
	}
}

func TestSnapshotExecutesAndComparesPriorSubmissions(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c1-l1-b1-f1-AccountSet-require-destination.json")
	fixture.PreSubmit = []snapshotSubmission{{TxBlob: fixture.TxBlob, Submit: fixture.Submit}}
	if err := runSnapshotFixture(fixture); err == nil || !strings.HasPrefix(err.Error(), "engine_result=") {
		t.Fatalf("duplicate primary submission did not fail after pre_submit consumed its sequence: %v", err)
	}
	fixture.PreSubmit[0].Submit.EngineResult = ter.TemMALFORMED.String()
	fixture.PreSubmit[0].Submit.EngineResultCode = int(ter.TemMALFORMED)
	fixture.PreSubmit[0].Submit.Applied = false
	fixture.PreSubmit[0].Submit.Fee = 0
	if err := runSnapshotFixture(fixture); err == nil || !strings.HasPrefix(err.Error(), "pre_submit[0]: engine_result=") {
		t.Fatalf("prior submission result was not compared: %v", err)
	}
}

func TestSnapshotRejectsIncompletePriorSubmissionEvidence(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c1-l1-b1-f1-AccountSet-require-destination.json")
	fixture.PreSubmit = []snapshotSubmission{{TxBlob: fixture.TxBlob, Submit: fixture.Submit}}
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"tx_blob", "engine_result_code", "post_submit_sle"} {
		t.Run(field, func(t *testing.T) {
			var object map[string]any
			if err := json.Unmarshal(data, &object); err != nil {
				t.Fatal(err)
			}
			prior := object["pre_submit"].([]any)[0].(map[string]any)
			if field == "tx_blob" {
				delete(prior, field)
			} else {
				delete(prior["submit"].(map[string]any), field)
			}
			modified, err := json.Marshal(object)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := decodeSnapshotFixture(modified); err == nil || !strings.Contains(err.Error(), field+" is missing") {
				t.Fatalf("missing prior submission evidence accepted: %v", err)
			}
		})
	}
}

func TestSnapshotRejectsChangedClosedRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		feature [32]byte
		enable  bool
	}{
		{name: "unexpected amendment", feature: amendment.FeatureFixBatchV1_2, enable: true},
		{name: "missing amendment", feature: amendment.FeatureBatchV1_1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
			id := strings.ToUpper(hex.EncodeToString(tc.feature[:]))
			if tc.enable {
				fixture.Closed.Rules = append(fixture.Closed.Rules, id)
			} else {
				rules := make([]string, 0, len(fixture.Closed.Rules))
				for _, rule := range fixture.Closed.Rules {
					if !strings.EqualFold(rule, id) {
						rules = append(rules, rule)
					}
				}
				fixture.Closed.Rules = rules
			}
			if err := runSnapshotFixture(fixture); err == nil || !strings.Contains(err.Error(), "rules") {
				t.Fatalf("changed closed rules were not rejected: %v", err)
			}
		})
	}
}

func TestSnapshotRejectsFeesContradictingLedgerState(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b0-f0-Payment-valid.json")
	fixture.Parent.Fees.Reserve = "1"
	fixture.Closed.Fees.Reserve = "1"
	if err := runSnapshotFixture(fixture); err == nil || !strings.Contains(err.Error(), "fees.reserve disagrees with FeeSettings") {
		t.Fatalf("contradictory fee snapshots were not rejected: %v", err)
	}
}

func TestSnapshotFeeSettingsPresence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]any
		rules  *amendment.Rules
		fees   drops.Fees
		bad    bool
	}{
		{name: "absent", fees: drops.Fees{Base: 12, Reserve: 34, Increment: 56}},
		{name: "partial legacy", fields: map[string]any{"BaseFee": "A"}, fees: drops.Fees{Base: 10, Reserve: 34, Increment: 56}},
		{name: "legacy mismatch", fields: map[string]any{"ReserveBase": uint32(34)}, fees: drops.Fees{Reserve: 35}, bad: true},
		{name: "explicit zero", fields: map[string]any{"BaseFee": "0"}, fees: drops.Fees{Base: 10}, bad: true},
		{name: "partial modern", fields: map[string]any{"ReserveIncrementDrops": "56"}, rules: amendment.NewRules([][32]byte{amendment.FeatureXRPFees}), fees: drops.Fees{Base: 12, Reserve: 34, Increment: 56}},
		{name: "modern mismatch", fields: map[string]any{"BaseFeeDrops": "12"}, rules: amendment.NewRules([][32]byte{amendment.FeatureXRPFees}), fees: drops.Fees{Base: 10}, bad: true},
		{name: "modern before amendment", fields: map[string]any{"BaseFeeDrops": "12"}, fees: drops.Fees{Base: 12}, bad: true},
		{name: "mixed fields", fields: map[string]any{"BaseFee": "C", "BaseFeeDrops": "12"}, rules: amendment.NewRules([][32]byte{amendment.FeatureXRPFees}), fees: drops.Fees{Base: 12}, bad: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stateMap := shamap.New(shamap.TypeState)
			if tc.fields != nil {
				tc.fields["LedgerEntryType"] = "FeeSettings"
				tc.fields["Flags"] = uint32(0)
				data, err := binarycodec.EncodeBytes(tc.fields)
				if err != nil {
					t.Fatal(err)
				}
				if err := stateMap.Put(keylet.Fees().Key, data); err != nil {
					t.Fatal(err)
				}
			}
			if tc.rules == nil {
				tc.rules = amendment.EmptyRules()
			}
			if err := validateSnapshotFees(stateMap, tc.rules, tc.fees); (err != nil) != tc.bad {
				t.Fatalf("fee validation error = %v, want error %t", err, tc.bad)
			}
		})
	}
}

func TestSnapshotRejectsModifiedSignatureAndExpectedBytes(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	invalidSignature := fixture
	invalidSignature.CloseInput.TxBlobs = append([]string(nil), fixture.CloseInput.TxBlobs...)
	parsed, err := parseSnapshotPending("tx_blob", mustDecodeSnapshotTestHex(t, fixture.TxBlob))
	if err != nil {
		t.Fatal(err)
	}
	fields, err := parsed.Parsed.Flatten()
	if err != nil {
		t.Fatal(err)
	}
	signature, ok := fields["TxnSignature"].(string)
	if !ok || signature == "" {
		t.Fatal("recorded fixture has no top-level signature")
	}
	signatureBytes, err := hex.DecodeString(signature)
	if err != nil || len(signatureBytes) == 0 {
		t.Fatalf("decode signature: %v", err)
	}
	signatureBytes[len(signatureBytes)-1] ^= 1
	fields["TxnSignature"] = strings.ToUpper(hex.EncodeToString(signatureBytes))
	mutated, err := binarycodec.EncodeBytes(fields)
	if err != nil {
		t.Fatalf("encode modified signature: %v", err)
	}
	invalidSignature.TxBlob = strings.ToUpper(hex.EncodeToString(mutated))
	invalidSignature.CloseInput.TxBlobs[0] = invalidSignature.TxBlob
	if err := runSnapshotFixture(invalidSignature); err == nil || !strings.Contains(err.Error(), `engine_result="temINVALID"`) {
		t.Fatalf("modified signature was not rejected at submission: %v", err)
	}

	metadataBytes := fixture
	metadataBytes.Closed.Transactions = append([]snapshotTransaction(nil), fixture.Closed.Transactions...)
	metadataBytes.Closed.Transactions[0].MetaBlob = mutateSnapshotTestHex(t, metadataBytes.Closed.Transactions[0].MetaBlob)
	if err := runSnapshotFixture(metadataBytes); err == nil {
		t.Fatal("modified closed metadata was accepted")
	}

	stateBytes := fixture
	stateBytes.Closed.State = append([]snapshotEntry(nil), fixture.Closed.State...)
	stateBytes.Closed.State[0].Data = mutateSnapshotTestHex(t, stateBytes.Closed.State[0].Data)
	if err := runSnapshotFixture(stateBytes); err == nil {
		t.Fatal("modified closed state was accepted")
	}

	missingPostState := fixture
	missingPostState.Submit.PostSubmitSLE = append([]snapshotEntry(nil), fixture.Submit.PostSubmitSLE[:len(fixture.Submit.PostSubmitSLE)-1]...)
	if err := runSnapshotFixture(missingPostState); err == nil {
		t.Fatal("incomplete post-submit state was accepted")
	}
}

func TestDecodeSnapshotRejectsMalformedExcludedExpectedState(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	fixture.Submit.PostSubmitSLE = append([]snapshotEntry(nil), fixture.Submit.PostSubmitSLE...)
	fixture.Submit.PostSubmitSLE[0].Data = "00"
	data, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotFixture(data); err == nil || !strings.Contains(err.Error(), "valid ledger entry") {
		t.Fatalf("malformed expected state was accepted during decode: %v", err)
	}
}

func mustDecodeSnapshotTestHex(t *testing.T, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}

func mutateSnapshotTestHex(t *testing.T, value string) string {
	t.Helper()
	decoded := mustDecodeSnapshotTestHex(t, value)
	if len(decoded) == 0 {
		t.Fatal("cannot mutate empty hex value")
	}
	decoded[0] ^= 1
	return strings.ToUpper(hex.EncodeToString(decoded))
}

func TestSnapshotRequiresLedgerRuleEquality(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f0-Batch-canonical.json")
	parent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, feature := range amendment.AllFeatures() {
		if parent.EffectiveRules.Enabled(feature.ID) {
			continue
		}
		fixture.Parent.Rules = append(fixture.Parent.Rules, strings.ToUpper(hex.EncodeToString(feature.ID[:])))
		if _, err := loadSnapshotLedger(fixture.Parent); err == nil || !strings.Contains(err.Error(), "explicit rules differ") {
			t.Fatalf("accepted rule absent from authenticated parent: %v", err)
		}
		return
	}
	t.Fatal("fixture has no disabled amendment for the negative control")
}

func TestSnapshotAppliedTransactionRequiresStoredBytes(t *testing.T) {
	fixture := loadSnapshotV4Fixture(t, "c0-l0-b1-f1-AccountSet-require-destination.json")
	parent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		t.Fatal(err)
	}
	current, err := ledger.NewOpen(parent.Ledger, parent.Header.CloseTime)
	if err != nil {
		t.Fatal(err)
	}
	all.RegisterAll()
	blob, err := hex.DecodeString(fixture.TxBlob)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := parseSnapshotPending("tx_blob", blob)
	if err != nil {
		t.Fatal(err)
	}
	metadata := &tx.Metadata{TransactionResult: ter.TesSUCCESS}
	if err := assertSnapshotAppliedTransaction(current, pending, metadata); err == nil || !strings.Contains(err.Error(), "absent") {
		t.Fatalf("accepted missing applied transaction: %v", err)
	}
	metaBlob, err := tx.SerializeMetadata(metadata)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := snapshotTxLeaf(blob, metaBlob)
	if err != nil {
		t.Fatal(err)
	}
	if err := current.AddTransactionWithMeta(pending.Hash, leaf); err != nil {
		t.Fatal(err)
	}
	if err := assertSnapshotAppliedTransaction(current, pending, metadata); err != nil {
		t.Fatal(err)
	}
	metadata.TransactionResult = ter.TecNO_PERMISSION
	if err := assertSnapshotAppliedTransaction(current, pending, metadata); err == nil || !strings.Contains(err.Error(), "metadata differs") {
		t.Fatalf("accepted different returned metadata: %v", err)
	}
	pending.Blob = append([]byte(nil), pending.Blob...)
	pending.Blob[len(pending.Blob)-1] ^= 1
	if err := assertSnapshotAppliedTransaction(current, pending, metadata); err == nil || !strings.Contains(err.Error(), "bytes differ") {
		t.Fatalf("accepted different submitted bytes: %v", err)
	}
}
