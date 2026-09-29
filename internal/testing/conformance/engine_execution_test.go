package conformance

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
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
	fixture.Parent.State = slices.Clone(fixture.Parent.State)
	fixture.Parent.Transactions = slices.Clone(fixture.Parent.Transactions)
	fixture.Parent.Rules = slices.Clone(fixture.Parent.Rules)
	fixture.CloseInput.TxBlobs = slices.Clone(fixture.CloseInput.TxBlobs)
	fixture.Closed.Transactions = slices.Clone(fixture.Closed.Transactions)
	random.Shuffle(len(fixture.Parent.State), func(i, j int) {
		fixture.Parent.State[i], fixture.Parent.State[j] = fixture.Parent.State[j], fixture.Parent.State[i]
	})
	random.Shuffle(len(fixture.Parent.Transactions), func(i, j int) {
		fixture.Parent.Transactions[i], fixture.Parent.Transactions[j] = fixture.Parent.Transactions[j], fixture.Parent.Transactions[i]
	})
	random.Shuffle(len(fixture.Parent.Rules), func(i, j int) {
		fixture.Parent.Rules[i], fixture.Parent.Rules[j] = fixture.Parent.Rules[j], fixture.Parent.Rules[i]
	})
	random.Shuffle(len(fixture.CloseInput.TxBlobs), func(i, j int) {
		fixture.CloseInput.TxBlobs[i], fixture.CloseInput.TxBlobs[j] = fixture.CloseInput.TxBlobs[j], fixture.CloseInput.TxBlobs[i]
	})
	random.Shuffle(len(fixture.Closed.Transactions), func(i, j int) {
		fixture.Closed.Transactions[i], fixture.Closed.Transactions[j] = fixture.Closed.Transactions[j], fixture.Closed.Transactions[i]
	})
	return fixture
}

func TestEngineExecutionOrder(t *testing.T) {
	corpus, err := resolvePinnedCorpus(conformanceCorpusRequired())
	if err != nil {
		t.Fatal(err)
	}
	seeds := []uint64{0, 1, 2016, ^uint64(0)}
	report := struct {
		OracleCommit    string         `json:"oracle_commit"`
		ManifestSHA256  string         `json:"manifest_sha256"`
		TestedSHA       string         `json:"tested_sha"`
		Dirty           bool           `json:"dirty"`
		Seeds           []uint64       `json:"seeds"`
		DurationSeconds float64        `json:"duration_seconds"`
		Discovered      int            `json:"discovered"`
		Executed        int            `json:"executed"`
		Passed          int            `json:"passed"`
		Failed          int            `json:"failed"`
		Excluded        int            `json:"excluded"`
		Profiles        map[string]int `json:"executed_profiles"`
		Families        map[string]int `json:"executed_families"`
		CompletedStages map[string]int `json:"completed_stages"`
		CompletedCases  map[string]int `json:"completed_cases"`
		SubmittedTypes  map[string]int `json:"completed_submission_types"`
		ClosedTypes     map[string]int `json:"completed_closed_transaction_types"`
		Results         map[string]int `json:"completed_submission_results"`
		PresentFields   map[string]int `json:"completed_submission_common_fields_present"`
		EnabledRules    map[string]int `json:"completed_parent_rules_enabled"`
		Submissions     int            `json:"completed_submissions"`
		CloseInputs     int            `json:"completed_close_inputs"`
		ReplayLeaves    int            `json:"completed_replay_leaves"`
		SoakSeed        uint64         `json:"soak_seed"`
		SoakSeconds     int            `json:"requested_soak_seconds"`
		SoakExecuted    int            `json:"soak_executed"`
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
		report.Executed++
		report.Profiles[c.Fixture.Profile]++
		report.Families[c.Fixture.Family]++
		if t.Run(fmt.Sprintf("%s/seed-%d", c.Name, seed), func(t *testing.T) {
			if err := runSnapshotFixture(shuffledSnapshot(c.Fixture, seed)); err != nil {
				t.Fatal(err)
			}
		}) {
			report.Passed++
			report.CompletedCases[c.Name]++
			submissions := append(slices.Clone(c.Fixture.PreSubmit), snapshotSubmission{TxBlob: c.Fixture.TxBlob, Submit: c.Fixture.Submit})
			report.Submissions += len(submissions)
			for _, submission := range submissions {
				submitted := decode(submission.TxBlob)
				report.SubmittedTypes[fmt.Sprint(submitted["TransactionType"])]++
				report.Results[submission.Submit.EngineResult]++
				for _, field := range tx.FormatCommonFields() {
					if _, present := submitted[field.Name]; present {
						report.PresentFields[field.Name]++
					}
				}
			}
			for _, rule := range c.Fixture.Parent.Rules {
				report.EnabledRules[rule]++
			}
			for _, transaction := range c.Fixture.Closed.Transactions {
				fields := decode(transaction.TxBlob)
				report.ClosedTypes[fmt.Sprint(fields["TransactionType"])]++
			}
			for _, stage := range []string{"signed_submission", "closed_ledger", "inbound_replay"} {
				report.CompletedStages[stage]++
			}
			report.CloseInputs += len(c.Fixture.CloseInput.TxBlobs)
			report.ReplayLeaves += len(c.Fixture.Closed.Transactions)
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
	if report.SoakSeconds > 0 && len(runnable) > 0 && report.Failed == 0 {
		random := rand.New(rand.NewPCG(report.SoakSeed, 0x5852504c))
		deadline := time.Now().Add(time.Duration(report.SoakSeconds) * time.Second)
		for time.Now().Before(deadline) && report.Failed == 0 {
			execute(runnable[random.IntN(len(runnable))], random.Uint64())
			report.SoakExecuted++
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
