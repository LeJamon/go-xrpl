package conformance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
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
		Submit:           snapshotSubmit{Boundary: snapshotSubmitBoundary, EngineResult: ter.TesSUCCESS.String(), EngineResultCode: int(ter.TesSUCCESS)},
		CloseInput:       snapshotCloseInput{CloseTimeResolution: 2, TxBlobs: []string{}},
		Parent:           minimalSnapshotLedger(),
		Closed:           minimalSnapshotLedger(),
	}
}

func minimalSnapshotLedger() snapshotLedger {
	return snapshotLedger{
		Header: "00",
		Rules:  []string{"0000000000000000000000000000000000000000000000000000000000000000"},
		Fees:   snapshotFees{Base: "10", Reserve: "10000000", Increment: "2000000"},
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
	data, err = json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeSnapshotFixture(data); err == nil || !strings.Contains(err.Error(), "tx_blob is missing") {
		t.Fatalf("missing required field accepted: %v", err)
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
