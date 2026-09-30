package conformance

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
)

func shuffledSnapshot(fixture snapshotFixture, seed uint64) snapshotFixture {
	random := rand.New(rand.NewPCG(seed, seed^0x5852504c))
	shuffleLedger := func(value snapshotLedger) snapshotLedger {
		value.State = slices.Clone(value.State)
		value.Transactions = slices.Clone(value.Transactions)
		value.Rules = slices.Clone(value.Rules)
		random.Shuffle(len(value.State), func(i, j int) {
			value.State[i], value.State[j] = value.State[j], value.State[i]
		})
		random.Shuffle(len(value.Transactions), func(i, j int) {
			value.Transactions[i], value.Transactions[j] = value.Transactions[j], value.Transactions[i]
		})
		random.Shuffle(len(value.Rules), func(i, j int) {
			value.Rules[i], value.Rules[j] = value.Rules[j], value.Rules[i]
		})
		return value
	}
	shuffleClose := func(value snapshotCloseInput) snapshotCloseInput {
		value.TxBlobs = slices.Clone(value.TxBlobs)
		random.Shuffle(len(value.TxBlobs), func(i, j int) {
			value.TxBlobs[i], value.TxBlobs[j] = value.TxBlobs[j], value.TxBlobs[i]
		})
		return value
	}
	fixture.Parent = shuffleLedger(fixture.Parent)
	fixture.Closed = shuffleLedger(fixture.Closed)
	fixture.CloseInput = shuffleClose(fixture.CloseInput)
	fixture.History = slices.Clone(fixture.History)
	for i := range fixture.History {
		item := &fixture.History[i]
		item.Parent = shuffleLedger(item.Parent)
		item.Closed = shuffleLedger(item.Closed)
		item.CloseInput = shuffleClose(item.CloseInput)
	}
	return fixture
}

func TestEngineExecutionOrder(t *testing.T) {
	corpus, err := resolvePinnedCorpus(conformanceCorpusRequired())
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{0, 1, 2016, ^uint64(0)}
	report := struct {
		OracleCommit        string         `json:"oracle_commit"`
		ManifestSHA256      string         `json:"manifest_sha256"`
		TestedSHA           string         `json:"tested_sha"`
		Dirty               bool           `json:"dirty"`
		Seeds               []uint64       `json:"seeds"`
		DurationSeconds     float64        `json:"duration_seconds"`
		Discovered          int            `json:"discovered"`
		Executed            int            `json:"executed"`
		Passed              int            `json:"passed"`
		Failed              int            `json:"failed"`
		Excluded            int            `json:"excluded"`
		Profiles            map[string]int `json:"executed_profiles"`
		Families            map[string]int `json:"executed_families"`
		CompletedStages     map[string]int `json:"completed_stages"`
		CompletedCases      map[string]int `json:"completed_cases"`
		SubmittedTypes      map[string]int `json:"completed_submission_types"`
		ClosedTypes         map[string]int `json:"completed_closed_transaction_types"`
		Results             map[string]int `json:"completed_submission_results"`
		PresentFields       map[string]int `json:"completed_submission_common_fields_present"`
		EnabledRules        map[string]int `json:"completed_parent_rules_enabled"`
		Submissions         int            `json:"completed_submissions"`
		CloseInputs         int            `json:"completed_close_inputs"`
		ReplayLeaves        int            `json:"completed_replay_leaves"`
		QueueChecks         int            `json:"completed_queue_checks"`
		HistoryTransitions  int            `json:"completed_history_transitions"`
		HistorySubmissions  int            `json:"completed_history_submissions"`
		HistoryCloseInputs  int            `json:"completed_history_close_inputs"`
		HistoryReplayLeaves int            `json:"completed_history_replay_leaves"`
		SoakSeed            uint64         `json:"soak_seed"`
		SoakSeconds         int            `json:"requested_soak_seconds"`
		SoakExecuted        int            `json:"soak_executed"`
	}{
		OracleCommit: corpus.Manifest.RippledCommit, ManifestSHA256: corpus.ManifestSHA256,
		Seeds: seeds, Discovered: len(corpus.Cases),
		Profiles: make(map[string]int), Families: make(map[string]int), CompletedStages: make(map[string]int),
		CompletedCases: make(map[string]int), SubmittedTypes: make(map[string]int), ClosedTypes: make(map[string]int),
		Results: make(map[string]int), PresentFields: make(map[string]int), EnabledRules: make(map[string]int),
	}
	git := func(args ...string) string {
		t.Helper()
		command := exec.CommandContext(t.Context(), "git", args...)
		command.Dir = conformanceRepositoryRoot()
		output, err := command.Output()
		if err != nil {
			t.Fatalf("record tested candidate: git %v: %v", args, err)
		}
		return strings.TrimSpace(string(output))
	}
	report.TestedSHA = git("rev-parse", "HEAD")
	report.Dirty = git("status", "--porcelain", "--untracked-files=all") != ""
	report.SoakSeed = 2016
	if value := os.Getenv("GOXRPL_ENGINE_SOAK_SECONDS"); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds < 1 || seconds > 300 {
			t.Fatal("GOXRPL_ENGINE_SOAK_SECONDS must be an integer from 1 to 300")
		}
		report.SoakSeconds = seconds
	}
	started := time.Now()
	decode := func(blob string) map[string]any {
		t.Helper()
		fields, err := binarycodec.Decode(blob)
		if err != nil {
			t.Fatalf("account for executed transaction fields: %v", err)
		}
		return fields
	}
	execute := func(c pinnedCase, seed uint64) {
		completed := false
		passed := t.Run(fmt.Sprintf("%s/seed-%d", c.Name, seed), func(t *testing.T) {
			report.Executed++
			report.Profiles[c.Fixture.Profile]++
			report.Families[c.Fixture.Family]++
			if err := runSnapshotFixture(shuffledSnapshot(c.Fixture, seed)); err != nil {
				t.Fatal(err)
			}
			completed = true
		})
		if passed && !completed {
			t.Errorf("required engine case %s seed=%d did not execute completely", c.Name, seed)
			return
		}
		if passed {
			report.Passed++
			report.CompletedCases[c.Name]++
			submissions := append(slices.Clone(c.Fixture.PreSubmit), snapshotSubmission{TxBlob: c.Fixture.TxBlob, Submit: c.Fixture.Submit})
			parents := []snapshotLedger{c.Fixture.Parent}
			closedLedgers := []snapshotLedger{c.Fixture.Closed}
			report.CloseInputs += len(c.Fixture.CloseInput.TxBlobs)
			report.ReplayLeaves += len(c.Fixture.Closed.Transactions)
			for _, history := range c.Fixture.History {
				submissions = append(submissions, history.PreSubmit...)
				parents = append(parents, history.Parent)
				closedLedgers = append(closedLedgers, history.Closed)
				report.HistoryTransitions++
				if history.Queue != nil {
					report.QueueChecks++
				}
				report.HistorySubmissions += len(history.PreSubmit)
				report.HistoryCloseInputs += len(history.CloseInput.TxBlobs)
				report.HistoryReplayLeaves += len(history.Closed.Transactions)
				report.CloseInputs += len(history.CloseInput.TxBlobs)
				report.ReplayLeaves += len(history.Closed.Transactions)
			}
			report.Submissions += len(submissions)
			for _, submission := range submissions {
				if submission.Submit.Queue != nil {
					report.QueueChecks++
				}
				submitted := decode(submission.TxBlob)
				report.SubmittedTypes[fmt.Sprint(submitted["TransactionType"])]++
				report.Results[submission.Submit.EngineResult]++
				for _, field := range tx.FormatCommonFields() {
					if _, present := submitted[field.Name]; present {
						report.PresentFields[field.Name]++
					}
				}
			}
			for _, parent := range parents {
				for _, rule := range parent.Rules {
					report.EnabledRules[rule]++
				}
			}
			for _, closed := range closedLedgers {
				for _, transaction := range closed.Transactions {
					fields := decode(transaction.TxBlob)
					report.ClosedTypes[fmt.Sprint(fields["TransactionType"])]++
				}
			}
			for _, stage := range []string{"signed_submission", "closed_ledger", "inbound_replay"} {
				report.CompletedStages[stage]++
			}
		} else {
			report.Failed++
		}
	}
	runnable := make([]pinnedCase, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		if c.Pin.ExcludeReason != "" {
			report.Excluded++
			t.Errorf("required engine case %s is excluded: %s", c.Name, c.Pin.ExcludeReason)
			continue
		}
		runnable = append(runnable, c)
		for _, seed := range seeds {
			execute(c, seed)
		}
	}
	if report.SoakSeconds > 0 && len(runnable) > 0 && !t.Failed() {
		random := rand.New(rand.NewPCG(report.SoakSeed, 0x5852504c))
		deadline := time.Now().Add(time.Duration(report.SoakSeconds) * time.Second)
		for time.Now().Before(deadline) && !t.Failed() {
			before := report.Executed
			execute(runnable[random.IntN(len(runnable))], random.Uint64())
			report.SoakExecuted += report.Executed - before
		}
	}
	report.DurationSeconds = time.Since(started).Seconds()
	if report.Executed == 0 || report.Executed != (report.Discovered-report.Excluded)*len(seeds)+report.SoakExecuted {
		t.Errorf("invalid execution denominator: %+v", report)
	}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("engine execution evidence: %s", data)
	if path := os.Getenv("GOXRPL_ENGINE_EVIDENCE_REPORT"); path != "" {
		if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzEngineDifferentialOrder(f *testing.F) {
	corpus, err := resolvePinnedCorpus(conformanceCorpusRequired())
	if err != nil {
		f.Fatal(err)
	}
	fixtures := make([]snapshotFixture, 0, len(corpus.Cases))
	for _, c := range corpus.Cases {
		if c.Pin.ExcludeReason != "" {
			f.Fatalf("required engine case %s is excluded: %s", c.Name, c.Pin.ExcludeReason)
		}
		f.Add(uint32(len(fixtures)), uint64(2016))
		fixtures = append(fixtures, c.Fixture)
	}
	if len(fixtures) == 0 {
		f.Fatal("no executable oracle cases")
	}
	f.Fuzz(func(t *testing.T, selection uint32, seed uint64) {
		fixture := fixtures[selection%uint32(len(fixtures))]
		if err := runSnapshotFixture(shuffledSnapshot(fixture, seed)); err != nil {
			t.Fatalf("%s/%s seed=%d: %v", fixture.Profile, fixture.Testcase, seed, err)
		}
	})
}

func TestEngineExecutionOrderRejectsFilteredCases(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "engine-report.json")
	cmd := exec.CommandContext(t.Context(), executable, "-test.run=^TestEngineExecutionOrder$/^does-not-exist$")
	cmd.Env = append(os.Environ(),
		"GOXRPL_FIXTURES_DIR=internal/testing/conformance/testdata/rippled-3.4.1-v4",
		"GOXRPL_CONFORMANCE_REQUIRED=1", "GOXRPL_ENGINE_SOAK_SECONDS=",
		"GOXRPL_ENGINE_EVIDENCE_REPORT="+path)
	output, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "did not execute completely") {
		t.Fatalf("filtered engine cases accepted: %v\n%s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var report struct {
		Discovered      int            `json:"discovered"`
		Executed        int            `json:"executed"`
		Passed          int            `json:"passed"`
		CompletedStages map[string]int `json:"completed_stages"`
		CompletedCases  map[string]int `json:"completed_cases"`
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if report.Discovered == 0 || report.Executed != 0 || report.Passed != 0 || len(report.CompletedStages) != 0 || len(report.CompletedCases) != 0 {
		t.Fatalf("filtered run reported completed engine execution: %+v", report)
	}
}
