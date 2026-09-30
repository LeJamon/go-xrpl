package conformance

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	ledgerstate "github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	snapshotFixtureVersion = "v4"
	snapshotOracleRepo     = "XRPLF/xrpld-private"
	snapshotOracleTag      = "3.4.1"
	snapshotOracleCommit   = "d147fccf54a500fce586522f28d6044c37fd8d29"
	snapshotSubmitBoundary = "open_ledger"
)

// snapshotFixture is the self-contained v4 differential fixture. The input
// transaction and close_input are deliberately independent from the expected
// submit and closed-ledger values.
type snapshotFixture struct {
	FixtureVersion            string               `json:"fixture_version"`
	OracleRepository          string               `json:"oracle_repository"`
	OracleTag                 string               `json:"oracle_tag"`
	OracleCommit              string               `json:"oracle_commit"`
	Suite                     string               `json:"suite"`
	Testcase                  string               `json:"testcase"`
	Family                    string               `json:"family"`
	Profile                   string               `json:"profile"`
	NetworkID                 uint32               `json:"network_id"`
	ApplyFlags                uint32               `json:"apply_flags"`
	SkipSignatureVerification bool                 `json:"skip_signature_verification"`
	TxQConfig                 snapshotTxQConfig    `json:"txq_config"`
	TxBlob                    string               `json:"tx_blob"`
	PreSubmit                 []snapshotSubmission `json:"pre_submit,omitempty"`
	OpenLedgerInject          []snapshotEntry      `json:"open_ledger_inject,omitempty"`
	OpenLedgerErase           []snapshotEntry      `json:"open_ledger_erase,omitempty"`
	History                   []snapshotHistory    `json:"history,omitempty"`
	Parent                    snapshotLedger       `json:"parent"`
	CloseInput                snapshotCloseInput   `json:"close_input"`
	Submit                    snapshotSubmit       `json:"submit"`
	Closed                    snapshotLedger       `json:"closed"`
}

type snapshotSubmission struct {
	TxBlob           string          `json:"tx_blob"`
	Submit           snapshotSubmit  `json:"submit"`
	OpenLedgerInject []snapshotEntry `json:"open_ledger_inject,omitempty"`
	OpenLedgerErase  []snapshotEntry `json:"open_ledger_erase,omitempty"`
}

type snapshotSubmit struct {
	Boundary         string          `json:"boundary"`
	EngineResult     string          `json:"engine_result"`
	EngineResultCode int             `json:"engine_result_code"`
	Applied          bool            `json:"applied"`
	Queued           bool            `json:"queued"`
	Fee              uint64          `json:"fee"`
	PostSubmitSLE    []snapshotEntry `json:"post_submit_sle"`
	Queue            *snapshotQueue  `json:"queue,omitempty"`
}

// snapshotQueue authenticates the complete queue disposition after a
// submission. The order is the fee order exposed by TxQ::getTxs/AllTxs;
// retaining the signed bytes catches both eviction and same-fee ordering
// differences while metrics cover the default threshold calculation.
type snapshotQueue struct {
	TxBlobs []string             `json:"tx_blobs"`
	Metrics snapshotQueueMetrics `json:"metrics"`
}

type snapshotQueueMetrics struct {
	TxCount               uint64  `json:"tx_count"`
	MaxSize               *uint64 `json:"max_size"`
	TxInLedger            uint64  `json:"tx_in_ledger"`
	TxPerLedger           uint64  `json:"tx_per_ledger"`
	ReferenceFeeLevel     uint64  `json:"reference_fee_level"`
	MinProcessingFeeLevel uint64  `json:"min_processing_fee_level"`
	MedFeeLevel           uint64  `json:"med_fee_level"`
	OpenLedgerFeeLevel    uint64  `json:"open_ledger_fee_level"`
}

// snapshotHistory records one authenticated closed-ledger transition that
// establishes the queue's fee history before the main fixture parent.
type snapshotHistory struct {
	Parent     snapshotLedger       `json:"parent"`
	PreSubmit  []snapshotSubmission `json:"pre_submit,omitempty"`
	CloseInput snapshotCloseInput   `json:"close_input"`
	Closed     snapshotLedger       `json:"closed"`
	Queue      *snapshotQueue       `json:"queue,omitempty"`
}

// snapshotCloseInput contains only values captured before closing the oracle
// ledger. In particular, close timing and transaction-set bytes never come
// from Closed.
type snapshotCloseInput struct {
	ParentCloseTime     uint32   `json:"parent_close_time"`
	CloseTime           uint32   `json:"close_time"`
	LedgerSequence      uint32   `json:"ledger_sequence"`
	CloseTimeResolution uint8    `json:"close_time_resolution"`
	CloseFlags          uint8    `json:"close_flags"`
	TxBlobs             []string `json:"tx_blobs"`
}

type snapshotTxQConfig struct {
	LedgersInQueue                 uint32 `json:"ledgers_in_queue"`
	QueueSizeMin                   uint32 `json:"queue_size_min"`
	RetrySequencePercent           uint32 `json:"retry_sequence_percent"`
	MinimumEscalationMultiplier    uint64 `json:"minimum_escalation_multiplier"`
	MinimumTxnInLedger             uint32 `json:"minimum_txn_in_ledger"`
	MinimumTxnInLedgerStandalone   uint32 `json:"minimum_txn_in_ledger_standalone"`
	TargetTxnInLedger              uint32 `json:"target_txn_in_ledger"`
	MaximumTxnInLedger             uint32 `json:"maximum_txn_in_ledger"`
	MaximumTxnInLedgerSet          bool   `json:"maximum_txn_in_ledger_set"`
	NormalConsensusIncreasePercent uint32 `json:"normal_consensus_increase_percent"`
	SlowConsensusDecreasePercent   uint32 `json:"slow_consensus_decrease_percent"`
	MaximumTxnPerAccount           uint32 `json:"maximum_txn_per_account"`
	MinimumLastLedgerBuffer        uint32 `json:"minimum_last_ledger_buffer"`
	Standalone                     bool   `json:"standalone"`
}

type snapshotLedger struct {
	Header       string                `json:"header"`
	Rules        []string              `json:"rules"`
	Fees         snapshotFees          `json:"fees"`
	State        []snapshotEntry       `json:"state"`
	Transactions []snapshotTransaction `json:"transactions"`
}

type snapshotFees struct {
	Base      string `json:"base"`
	Reserve   string `json:"reserve"`
	Increment string `json:"increment"`
}

type snapshotEntry struct {
	Index string `json:"index"`
	Data  string `json:"data"`
}

type snapshotTransaction struct {
	Hash     string `json:"hash"`
	TxBlob   string `json:"tx_blob"`
	MetaBlob string `json:"meta_blob"`
}

type loadedSnapshotLedger struct {
	*ledger.Ledger
	Header         ledgerheader.LedgerHeader
	EffectiveRules *amendment.Rules
	Fees           drops.Fees
	State          *shamap.SHAMap
	Txs            *shamap.SHAMap
}

// decodeSnapshotFixture decodes exactly one v4 fixture and rejects unknown
// fields at every level. The explicit shape pass also rejects omitted required
// fields whose Go zero value could otherwise look valid (for example TER 0).
func decodeSnapshotFixture(data []byte) (snapshotFixture, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return snapshotFixture{}, errors.New("snapshot fixture is empty")
	}
	if err := validateSnapshotJSONShape(data); err != nil {
		return snapshotFixture{}, err
	}
	var fixture snapshotFixture
	if err := decodeStrictJSON(data, &fixture); err != nil {
		return snapshotFixture{}, fmt.Errorf("decode snapshot fixture: %w", err)
	}
	if err := validateSnapshotFixture(&fixture); err != nil {
		return snapshotFixture{}, err
	}
	if err := validateSnapshotSemanticInputs(&fixture); err != nil {
		return snapshotFixture{}, err
	}
	return fixture, nil
}

// validateSnapshotFixture validates provenance and all boundary/configuration
// values that are independent of the ledger maps. Map roots, signatures and
// exact transaction effects are checked while running the fixture.
func validateSnapshotFixture(fixture *snapshotFixture) error {
	if fixture == nil {
		return errors.New("snapshot fixture is nil")
	}
	if fixture.FixtureVersion != snapshotFixtureVersion {
		return fmt.Errorf("fixture_version=%q, want %q", fixture.FixtureVersion, snapshotFixtureVersion)
	}
	if fixture.OracleRepository != snapshotOracleRepo {
		return fmt.Errorf("oracle_repository=%q, want %q", fixture.OracleRepository, snapshotOracleRepo)
	}
	if fixture.OracleTag != snapshotOracleTag {
		return fmt.Errorf("oracle_tag=%q, want %q", fixture.OracleTag, snapshotOracleTag)
	}
	if fixture.OracleCommit != snapshotOracleCommit {
		return fmt.Errorf("oracle_commit=%q, want %q", fixture.OracleCommit, snapshotOracleCommit)
	}
	for name, value := range map[string]string{
		"suite": fixture.Suite, "testcase": fixture.Testcase,
		"family": fixture.Family, "profile": fixture.Profile,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is empty", name)
		}
	}
	if !validSnapshotProfile(fixture.Profile) {
		return fmt.Errorf("unsupported snapshot profile %q", fixture.Profile)
	}
	if fixture.TxBlob == "" {
		return errors.New("tx_blob is empty")
	}
	if fixture.SkipSignatureVerification {
		return errors.New("skip_signature_verification must be false for signed snapshots")
	}
	if fixture.ApplyFlags != uint32(tx.TapNONE) {
		return fmt.Errorf("unsupported apply_flags=0x%x", fixture.ApplyFlags)
	}
	if err := validateSnapshotTxQConfig(fixture.TxQConfig); err != nil {
		return err
	}
	if err := validateSnapshotSubmit(fixture.Submit); err != nil {
		return err
	}
	if err := validateSnapshotOpenLedger("open_ledger", fixture.OpenLedgerInject, fixture.OpenLedgerErase); err != nil {
		return err
	}
	if err := validateSnapshotHistory(fixture.History); err != nil {
		return err
	}
	for i, prior := range fixture.PreSubmit {
		if prior.TxBlob == "" {
			return fmt.Errorf("pre_submit[%d].tx_blob is empty", i)
		}
		if err := validateSnapshotSubmit(prior.Submit); err != nil {
			return fmt.Errorf("pre_submit[%d]: %w", i, err)
		}
		if err := validateSnapshotOpenLedger(fmt.Sprintf("pre_submit[%d].open_ledger", i), prior.OpenLedgerInject, prior.OpenLedgerErase); err != nil {
			return err
		}
	}
	if fixture.CloseInput.CloseTimeResolution < 2 || fixture.CloseInput.CloseTimeResolution > 120 {
		return fmt.Errorf("close_input.close_time_resolution=%d is outside XRPL range", fixture.CloseInput.CloseTimeResolution)
	}
	if fixture.CloseInput.TxBlobs == nil {
		return errors.New("close_input.tx_blobs is missing")
	}
	if err := validateSnapshotLedgerShape("parent", fixture.Parent); err != nil {
		return err
	}
	if err := validateSnapshotLedgerShape("closed", fixture.Closed); err != nil {
		return err
	}
	return nil
}

func validateSnapshotHistory(history []snapshotHistory) error {
	if history == nil {
		return nil
	}
	if len(history) == 0 {
		return errors.New("history must not be empty when present")
	}
	for i, item := range history {
		name := fmt.Sprintf("history[%d]", i)
		if err := validateSnapshotLedgerShape(name+".parent", item.Parent); err != nil {
			return err
		}
		if err := validateSnapshotLedgerShape(name+".closed", item.Closed); err != nil {
			return err
		}
		if err := validateSnapshotQueue(item.Queue); err != nil {
			return fmt.Errorf("%s.queue: %w", name, err)
		}
		if item.CloseInput.CloseTimeResolution < 2 || item.CloseInput.CloseTimeResolution > 120 {
			return fmt.Errorf("%s.close_input.close_time_resolution=%d is outside XRPL range", name, item.CloseInput.CloseTimeResolution)
		}
		if item.CloseInput.TxBlobs == nil {
			return fmt.Errorf("%s.close_input.tx_blobs is missing", name)
		}
		for j, prior := range item.PreSubmit {
			if prior.TxBlob == "" {
				return fmt.Errorf("%s.pre_submit[%d].tx_blob is empty", name, j)
			}
			if err := validateSnapshotSubmit(prior.Submit); err != nil {
				return fmt.Errorf("%s.pre_submit[%d]: %w", name, j, err)
			}
			if err := validateSnapshotOpenLedger(fmt.Sprintf("%s.pre_submit[%d].open_ledger", name, j), prior.OpenLedgerInject, prior.OpenLedgerErase); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSnapshotSubmit(submit snapshotSubmit) error {
	if submit.Boundary != snapshotSubmitBoundary {
		return fmt.Errorf("submit.boundary=%q, want %q", submit.Boundary, snapshotSubmitBoundary)
	}
	if submit.EngineResult == "" {
		return errors.New("submit.engine_result is empty")
	}
	result := ter.Result(submit.EngineResultCode)
	if result.String() == "-" {
		return fmt.Errorf("unknown submit TER code %d", submit.EngineResultCode)
	}
	if result.String() != submit.EngineResult {
		return fmt.Errorf("submit TER %q does not match numeric code %d", submit.EngineResult, submit.EngineResultCode)
	}
	if submit.Applied && submit.Queued {
		return errors.New("submit.applied and submit.queued cannot both be true")
	}
	if submit.Queued != (result == ter.TerQUEUED) {
		return fmt.Errorf("submit.queued does not match TER %q", submit.EngineResult)
	}
	if (result.IsSuccess() && !submit.Applied) || (submit.Applied && !result.IsSuccess() && !result.IsTec()) {
		return fmt.Errorf("submit.applied does not match TER %q", submit.EngineResult)
	}
	if !submit.Applied && submit.Fee != 0 {
		return errors.New("submit.fee must be zero when submit.applied is false")
	}
	if err := validateSnapshotEntries("submit.post_submit_sle", submit.PostSubmitSLE); err != nil {
		return err
	}
	return validateSnapshotQueue(submit.Queue)
}

func validateSnapshotQueue(queue *snapshotQueue) error {
	if queue == nil {
		return nil
	}
	if queue.TxBlobs == nil {
		return errors.New("submit.queue.tx_blobs is missing")
	}
	if queue.Metrics.TxCount != uint64(len(queue.TxBlobs)) {
		return fmt.Errorf("submit.queue.metrics.tx_count=%d, want %d", queue.Metrics.TxCount, len(queue.TxBlobs))
	}
	seen := make(map[[32]byte]struct{}, len(queue.TxBlobs))
	for i, encoded := range queue.TxBlobs {
		blob, err := decodeSnapshotBytes(fmt.Sprintf("submit.queue.tx_blobs[%d]", i), encoded)
		if err != nil {
			return err
		}
		pending, err := parseSnapshotPending(fmt.Sprintf("submit.queue.tx_blobs[%d]", i), blob)
		if err != nil {
			return err
		}
		if _, duplicate := seen[pending.Hash]; duplicate {
			return fmt.Errorf("submit.queue.tx_blobs[%d] duplicates transaction %x", i, pending.Hash)
		}
		seen[pending.Hash] = struct{}{}
	}
	if queue.Metrics.MaxSize != nil && *queue.Metrics.MaxSize == 0 {
		return errors.New("submit.queue.metrics.max_size must be positive when present")
	}
	return nil
}

// validateSnapshotSemanticInputs validates every input needed to execute a
// fixture before corpus selection can exclude it. Expected output comparison
// still happens in runSnapshotFixture, but malformed snapshots must never be
// hidden behind an exclusion or an unsupported profile.
func validateSnapshotSemanticInputs(fixture *snapshotFixture) error {
	if fixture == nil {
		return errors.New("snapshot fixture is nil")
	}
	all.RegisterAll()
	parent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		return fmt.Errorf("parent snapshot: %w", err)
	}
	closed, err := loadSnapshotLedger(fixture.Closed)
	if err != nil {
		return fmt.Errorf("closed snapshot: %w", err)
	}
	if err := validateSnapshotLineage(parent, closed, fixture.CloseInput); err != nil {
		return fmt.Errorf("snapshot lineage: %w", err)
	}

	txBlob, err := decodeSnapshotBytes("tx_blob", fixture.TxBlob)
	if err != nil {
		return err
	}
	pending, err := parseSnapshotPending("tx_blob", txBlob)
	if err != nil {
		return err
	}
	if got := pending.Parsed.TxType().String(); fixture.Family != got {
		return fmt.Errorf("family=%q does not match tx_blob type %q", fixture.Family, got)
	}
	closePending, err := parseSnapshotCloseSet(fixture.CloseInput.TxBlobs)
	if err != nil {
		return err
	}
	if fixture.Submit.Applied && !snapshotPendingContains(closePending, pending) {
		return errors.New("applied tx_blob is absent from close_input.tx_blobs")
	}
	if err := validateSnapshotEntriesBytes("submit.post_submit_sle", fixture.Submit.PostSubmitSLE); err != nil {
		return err
	}
	for i, prior := range fixture.PreSubmit {
		blob, err := decodeSnapshotBytes("pre_submit.tx_blob", prior.TxBlob)
		if err != nil {
			return fmt.Errorf("pre_submit[%d]: %w", i, err)
		}
		priorPending, err := parseSnapshotPending("pre_submit.tx_blob", blob)
		if err != nil {
			return fmt.Errorf("pre_submit[%d]: %w", i, err)
		}
		if prior.Submit.Applied && !snapshotPendingContains(closePending, priorPending) {
			return fmt.Errorf("applied pre_submit[%d] is absent from close_input.tx_blobs", i)
		}
		if err := validateSnapshotEntriesBytes("submit.post_submit_sle", prior.Submit.PostSubmitSLE); err != nil {
			return fmt.Errorf("pre_submit[%d]: %w", i, err)
		}
	}
	if err := validateSnapshotHistorySemantic(fixture); err != nil {
		return err
	}
	return nil
}

func validateSnapshotHistorySemantic(fixture *snapshotFixture) error {
	if fixture == nil || fixture.History == nil {
		return nil
	}
	var previous *loadedSnapshotLedger
	for i, item := range fixture.History {
		name := fmt.Sprintf("history[%d]", i)
		parent, err := loadSnapshotLedger(item.Parent)
		if err != nil {
			return fmt.Errorf("%s parent snapshot: %w", name, err)
		}
		closed, err := loadSnapshotLedger(item.Closed)
		if err != nil {
			return fmt.Errorf("%s closed snapshot: %w", name, err)
		}
		if err := validateSnapshotLineage(parent, closed, item.CloseInput); err != nil {
			return fmt.Errorf("%s lineage: %w", name, err)
		}
		if previous != nil && parent.Header.Hash != previous.Header.Hash {
			return fmt.Errorf("%s parent hash does not follow history[%d].closed", name, i-1)
		}
		closePending, err := parseSnapshotCloseSet(item.CloseInput.TxBlobs)
		if err != nil {
			return fmt.Errorf("%s close_input: %w", name, err)
		}
		for j, prior := range item.PreSubmit {
			blob, err := decodeSnapshotBytes(fmt.Sprintf("%s.pre_submit[%d].tx_blob", name, j), prior.TxBlob)
			if err != nil {
				return err
			}
			pending, err := parseSnapshotPending(fmt.Sprintf("%s.pre_submit[%d].tx_blob", name, j), blob)
			if err != nil {
				return err
			}
			if prior.Submit.Applied && !snapshotPendingContains(closePending, pending) {
				return fmt.Errorf("%s applied pre_submit[%d] is absent from close_input.tx_blobs", name, j)
			}
			if err := validateSnapshotEntriesBytes(fmt.Sprintf("%s.pre_submit[%d].submit.post_submit_sle", name, j), prior.Submit.PostSubmitSLE); err != nil {
				return err
			}
		}
		previous = &closed
	}
	mainParent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		return fmt.Errorf("parent snapshot after history: %w", err)
	}
	if previous != nil && mainParent.Header.Hash != previous.Header.Hash {
		return errors.New("fixture parent does not follow the last history closed ledger")
	}
	return nil
}

func validSnapshotProfile(profile string) bool {
	if len(profile) != len("c0-l0-b0-f0") {
		return false
	}
	return (profile[0] == 'c' && (profile[1] == '0' || profile[1] == '1') &&
		profile[2] == '-' && profile[3] == 'l' && (profile[4] == '0' || profile[4] == '1') &&
		profile[5] == '-' && profile[6] == 'b' && (profile[7] == '0' || profile[7] == '1') &&
		profile[8] == '-' && profile[9] == 'f' && (profile[10] == '0' || profile[10] == '1'))
}

func (c snapshotTxQConfig) toConfig() txq.Config {
	return txq.Config{
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

func validateSnapshotTxQConfig(c snapshotTxQConfig) error {
	if err := c.toConfig().Validate(); err != nil {
		return fmt.Errorf("invalid txq_config: %w", err)
	}
	return nil
}

func validateSnapshotLedgerShape(name string, snapshot snapshotLedger) error {
	if snapshot.Header == "" {
		return fmt.Errorf("%s.header is empty", name)
	}
	if len(snapshot.Rules) == 0 {
		return fmt.Errorf("%s.rules is empty", name)
	}
	if snapshot.Fees.Base == "" || snapshot.Fees.Reserve == "" || snapshot.Fees.Increment == "" {
		return fmt.Errorf("%s.fees must include base, reserve, and increment", name)
	}
	if err := validateSnapshotEntries(name+".state", snapshot.State); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(snapshot.Transactions))
	for i, entry := range snapshot.Transactions {
		if entry.Hash == "" || entry.TxBlob == "" || entry.MetaBlob == "" {
			return fmt.Errorf("%s.transactions[%d] has an empty field", name, i)
		}
		if _, duplicate := seen[entry.Hash]; duplicate {
			return fmt.Errorf("%s.transactions[%d] duplicates hash %q", name, i, entry.Hash)
		}
		seen[entry.Hash] = struct{}{}
	}
	return nil
}

func validateSnapshotEntries(name string, entries []snapshotEntry) error {
	seen := make(map[string]struct{}, len(entries))
	for i, entry := range entries {
		if entry.Index == "" || entry.Data == "" {
			return fmt.Errorf("%s[%d] has an empty index or data", name, i)
		}
		if _, duplicate := seen[entry.Index]; duplicate {
			return fmt.Errorf("%s[%d] duplicates index %q", name, i, entry.Index)
		}
		seen[entry.Index] = struct{}{}
	}
	return nil
}

func validateSnapshotEntriesBytes(name string, entries []snapshotEntry) error {
	for i, entry := range entries {
		data, err := decodeSnapshotBytes(fmt.Sprintf("%s[%d].data", name, i), entry.Data)
		if err != nil {
			return err
		}
		if err := validateSnapshotSLEBytes(fmt.Sprintf("%s[%d].data", name, i), data); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotOpenLedger(name string, inject, erase []snapshotEntry) error {
	if err := validateSnapshotEntries(name+"_inject", inject); err != nil {
		return err
	}
	if err := validateSnapshotEntries(name+"_erase", erase); err != nil {
		return err
	}
	if err := validateSnapshotEntriesBytes(name+"_inject", inject); err != nil {
		return err
	}
	if err := validateSnapshotEntriesBytes(name+"_erase", erase); err != nil {
		return err
	}
	return nil
}

func validateSnapshotSLEBytes(name string, data []byte) error {
	if _, err := ledgerstate.DecodeType(data); err != nil {
		return fmt.Errorf("%s is not a valid ledger entry: %w", name, err)
	}
	if _, err := binarycodec.DecodeBytes(data); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	return nil
}

// runSnapshotFixture executes one fixture exactly once. It does not construct
// setup state, repair sequences, rewrite blobs, infer close inputs, or retry a
// submission based on its result.
func runSnapshotFixture(fixture snapshotFixture) error {
	if err := validateSnapshotFixture(&fixture); err != nil {
		return err
	}
	if err := validateSnapshotSemanticInputs(&fixture); err != nil {
		return err
	}
	all.RegisterAll()

	parent, err := loadSnapshotLedger(fixture.Parent)
	if err != nil {
		return fmt.Errorf("load parent ledger: %w", err)
	}
	closed, err := loadSnapshotLedger(fixture.Closed)
	if err != nil {
		return fmt.Errorf("load closed ledger: %w", err)
	}
	if err := validateSnapshotLineage(parent, closed, fixture.CloseInput); err != nil {
		return err
	}

	txBlob, err := decodeSnapshotBytes("tx_blob", fixture.TxBlob)
	if err != nil {
		return err
	}
	pending, err := parseSnapshotPending("tx_blob", txBlob)
	if err != nil {
		return err
	}
	closePending, err := parseSnapshotCloseSet(fixture.CloseInput.TxBlobs)
	if err != nil {
		return err
	}
	if fixture.Submit.Applied && !snapshotPendingContains(closePending, pending) {
		return errors.New("applied tx_blob is absent from close_input.tx_blobs")
	}

	submitApply := snapshotApplyConfig(parent, fixture.NetworkID, tx.ApplyFlags(fixture.ApplyFlags))
	queue, err := txq.New(fixture.TxQConfig.toConfig())
	if err != nil {
		return fmt.Errorf("create transaction queue: %w", err)
	}
	historyReplay, err := replaySnapshotHistory(
		fixture.History,
		queue,
		fixture.NetworkID,
		tx.ApplyFlags(fixture.ApplyFlags),
	)
	if err != nil {
		return fmt.Errorf("replay ledger history: %w", err)
	}
	var view *openledger.OpenLedger
	if historyReplay != nil {
		if err := assertSnapshotLedger(historyReplay.Closed, parent); err != nil {
			return fmt.Errorf("history latest parent mismatch: %w", err)
		}
		parent.Ledger = historyReplay.Closed
		parent.Header = historyReplay.Closed.Header()
		view = historyReplay.Open
	} else {
		view, err = openledger.New(parent.Ledger, openledger.Config{Rules: parent.EffectiveRules})
		if err != nil {
			return fmt.Errorf("create open ledger: %w", err)
		}
	}
	for i, prior := range fixture.PreSubmit {
		blob, err := decodeSnapshotBytes("pre_submit.tx_blob", prior.TxBlob)
		if err != nil {
			return err
		}
		priorPending, err := parseSnapshotPending("pre_submit.tx_blob", blob)
		if err != nil {
			return err
		}
		if err := applySnapshotOpenLedger(view, prior.OpenLedgerInject, prior.OpenLedgerErase); err != nil {
			return fmt.Errorf("pre_submit[%d] open-ledger mutation: %w", i, err)
		}
		if err := submitSnapshot(view, priorPending, submitApply, queue, prior.Submit); err != nil {
			return fmt.Errorf("pre_submit[%d]: %w", i, err)
		}
	}
	if err := applySnapshotOpenLedger(view, fixture.OpenLedgerInject, fixture.OpenLedgerErase); err != nil {
		return fmt.Errorf("open-ledger mutation: %w", err)
	}
	if err := submitSnapshot(view, pending, submitApply, queue, fixture.Submit); err != nil {
		return err
	}

	built, err := openledger.BuildClosedLedger(parent.Ledger, closePending, openledger.BuildConfig{
		CloseTime:  protocol.FromRippleTime(fixture.CloseInput.CloseTime),
		CloseFlags: fixture.CloseInput.CloseFlags,
		Apply:      snapshotApplyConfig(parent, fixture.NetworkID, tx.ApplyFlags(fixture.ApplyFlags)),
	})
	if err != nil {
		return fmt.Errorf("build closed ledger from close_input: %w", err)
	}
	if len(built.Retries) != 0 {
		return fmt.Errorf("closed ledger left %d retriable transactions", len(built.Retries))
	}
	if err := assertSnapshotLedger(built.Ledger, closed); err != nil {
		return fmt.Errorf("closed ledger mismatch: %w", err)
	}
	if err := runSnapshotReplay(fixture, parent, closed); err != nil {
		return fmt.Errorf("inbound replay mismatch: %w", err)
	}
	return nil
}

// applySnapshotOpenLedger applies authenticated raw state mutations to the
// current open view. Erases run before inserts so a recorder can authenticate
// and replace one SLE as two primitive operations. The mutations are
// intentionally kept outside the close input: they model test-only state that
// exists while the view is open and is never carried into the closed-ledger
// snapshot.
func applySnapshotOpenLedger(view *openledger.OpenLedger, inject, erase []snapshotEntry) error {
	if view == nil {
		return errors.New("open-ledger mutation received nil view")
	}
	if len(inject) == 0 && len(erase) == 0 {
		return nil
	}
	if err := validateSnapshotOpenLedger("open_ledger", inject, erase); err != nil {
		return err
	}
	var mutationErr error
	if !view.Modify(func(current *ledger.Ledger) bool {
		for i, entry := range erase {
			index, err := decodeSnapshotHash(fmt.Sprintf("open_ledger_erase[%d].index", i), entry.Index)
			if err != nil {
				mutationErr = err
				return false
			}
			want, err := decodeSnapshotBytes(fmt.Sprintf("open_ledger_erase[%d].data", i), entry.Data)
			if err != nil {
				mutationErr = err
				return false
			}
			got, err := current.Read(keylet.Child(index))
			if err != nil {
				mutationErr = fmt.Errorf("read erase target %x: %w", index, err)
				return false
			}
			if got == nil {
				mutationErr = fmt.Errorf("erase target %x is absent", index)
				return false
			}
			if !bytes.Equal(got, want) {
				mutationErr = fmt.Errorf("erase target %x bytes differ from authenticated data", index)
				return false
			}
			if err := current.Erase(keylet.Child(index)); err != nil {
				mutationErr = fmt.Errorf("erase target %x: %w", index, err)
				return false
			}
		}
		for i, entry := range inject {
			index, err := decodeSnapshotHash(fmt.Sprintf("open_ledger_inject[%d].index", i), entry.Index)
			if err != nil {
				mutationErr = err
				return false
			}
			data, err := decodeSnapshotBytes(fmt.Sprintf("open_ledger_inject[%d].data", i), entry.Data)
			if err != nil {
				mutationErr = err
				return false
			}
			if existing, err := current.Read(keylet.Child(index)); err != nil {
				mutationErr = fmt.Errorf("read insert target %x: %w", index, err)
				return false
			} else if existing != nil {
				mutationErr = fmt.Errorf("insert target %x already exists", index)
				return false
			}
			if err := current.Insert(keylet.Child(index), data); err != nil {
				mutationErr = fmt.Errorf("insert target %x: %w", index, err)
				return false
			}
		}
		return true
	}) {
		if mutationErr != nil {
			return mutationErr
		}
		return errors.New("open-ledger mutation was not published")
	}
	return nil
}

func submitSnapshot(view *openledger.OpenLedger, pending openledger.PendingTx, apply openledger.ApplyConfig, queue *txq.TxQ, expected snapshotSubmit) error {
	beforeState, err := view.Current().StateMapHash()
	if err != nil {
		return fmt.Errorf("hash open state before submit: %w", err)
	}
	beforeTxs, err := view.Current().TxMapHash()
	if err != nil {
		return fmt.Errorf("hash open transactions before submit: %w", err)
	}
	out := view.SubmitDetailed(pending, apply, queue)
	if err := assertSnapshotSubmit(out, expected); err != nil {
		return err
	}

	if expected.Queue != nil {
		if err := assertSnapshotQueue(queue, view.Current(), expected.Queue); err != nil {
			return err
		}
	} else {
		expectedQueueSize := 0
		if expected.Queued {
			expectedQueueSize = 1
		}
		if queue.Size() != expectedQueueSize {
			return fmt.Errorf("submission queue size=%d, want %d for the empty-start queue", queue.Size(), expectedQueueSize)
		}
		queuedBlob, queued := queue.GetTxBlob(pending.Hash)
		if queued != out.Queued || (queued && !bytes.Equal(queuedBlob, pending.Blob)) {
			return errors.New("submission queue membership or signed bytes differ")
		}
	}
	if out.Applied {
		if err := assertSnapshotAppliedTransaction(view.Current(), pending, out.Metadata); err != nil {
			return err
		}
	}
	if !out.Applied {
		if out.Changed {
			return errors.New("rejected or queued snapshot submission changed the open ledger")
		}
		afterState, hashErr := view.Current().StateMapHash()
		if hashErr != nil {
			return fmt.Errorf("hash open state after non-applied submit: %w", hashErr)
		}
		afterTxs, hashErr := view.Current().TxMapHash()
		if hashErr != nil {
			return fmt.Errorf("hash open transactions after non-applied submit: %w", hashErr)
		}
		if beforeState != afterState || beforeTxs != afterTxs {
			return errors.New("rejected or queued snapshot submission mutated ledger state")
		}
	}
	if err := assertSnapshotPostSubmitState(view.Current(), expected.PostSubmitSLE); err != nil {
		return err
	}
	return nil
}

func assertSnapshotQueue(queue *txq.TxQ, current *ledger.Ledger, expected *snapshotQueue) error {
	if queue == nil || current == nil || expected == nil {
		return errors.New("queue comparison received nil input")
	}
	return assertSnapshotQueueDetails(queue.AllTxs(), queue.Metrics(current.TxCount()), expected)
}

func assertSnapshotQueueDetails(details []*txq.CandidateDetails, metrics txq.Metrics, expected *snapshotQueue) error {
	if expected == nil {
		return errors.New("queue comparison received nil expected state")
	}
	if len(details) != len(expected.TxBlobs) {
		return fmt.Errorf("submission queue size=%d, want %d", len(details), len(expected.TxBlobs))
	}
	for i, detail := range details {
		if detail == nil {
			return fmt.Errorf("submission queue item %d is nil", i)
		}
		actual := strings.ToUpper(hex.EncodeToString(detail.TxBlob))
		if actual != strings.ToUpper(expected.TxBlobs[i]) {
			return fmt.Errorf("submission queue item %d signed bytes differ", i)
		}
	}
	actualMetrics := snapshotQueueMetrics{
		TxCount:               metrics.TxCount,
		TxInLedger:            metrics.TxInLedger,
		TxPerLedger:           metrics.TxPerLedger,
		ReferenceFeeLevel:     metrics.ReferenceFeeLevel,
		MinProcessingFeeLevel: metrics.MinProcessingFeeLevel,
		MedFeeLevel:           metrics.MedFeeLevel,
		OpenLedgerFeeLevel:    metrics.OpenLedgerFeeLevel,
	}
	if metrics.TxQMaxSize != nil {
		maxSize := *metrics.TxQMaxSize
		actualMetrics.MaxSize = &maxSize
	}
	if actualMetrics.TxCount != expected.Metrics.TxCount ||
		actualMetrics.TxInLedger != expected.Metrics.TxInLedger ||
		actualMetrics.TxPerLedger != expected.Metrics.TxPerLedger ||
		actualMetrics.ReferenceFeeLevel != expected.Metrics.ReferenceFeeLevel ||
		actualMetrics.MinProcessingFeeLevel != expected.Metrics.MinProcessingFeeLevel ||
		actualMetrics.MedFeeLevel != expected.Metrics.MedFeeLevel ||
		actualMetrics.OpenLedgerFeeLevel != expected.Metrics.OpenLedgerFeeLevel {
		return fmt.Errorf("submission queue fee metrics differ: got %+v want %+v", actualMetrics, expected.Metrics)
	}
	if (actualMetrics.MaxSize == nil) != (expected.Metrics.MaxSize == nil) ||
		(actualMetrics.MaxSize != nil && *actualMetrics.MaxSize != *expected.Metrics.MaxSize) {
		return fmt.Errorf("submission queue max size differs: got %v want %v", actualMetrics.MaxSize, expected.Metrics.MaxSize)
	}
	return nil
}

func assertSnapshotAppliedTransaction(current *ledger.Ledger, pending openledger.PendingTx, metadata *tx.Metadata) error {
	leaf, found, err := current.GetTransaction(pending.Hash)
	if err != nil {
		return fmt.Errorf("read applied transaction: %w", err)
	}
	if !found {
		return errors.New("applied submission is absent from the open transaction map")
	}
	blob, storedMetadata, err := tx.SplitTxWithMetaBlob(leaf)
	if err != nil {
		return fmt.Errorf("decode applied transaction leaf: %w", err)
	}
	if !bytes.Equal(blob, pending.Blob) {
		return errors.New("open transaction bytes differ from the signed submission")
	}
	if metadata == nil {
		return errors.New("applied submission returned no metadata")
	}
	returnedMetadata, err := tx.SerializeMetadata(metadata)
	if err != nil {
		return fmt.Errorf("serialize submission metadata: %w", err)
	}
	if !bytes.Equal(storedMetadata, returnedMetadata) {
		return errors.New("open transaction metadata differs from the submission result")
	}
	return nil
}

func validateSnapshotLineage(parent, closed loadedSnapshotLedger, input snapshotCloseInput) error {
	if closed.Header.ParentHash != parent.Header.Hash {
		return errors.New("closed ledger parent hash does not match parent ledger hash")
	}
	if closed.Header.LedgerIndex != parent.Header.LedgerIndex+1 {
		return fmt.Errorf("closed ledger sequence=%d, want %d", closed.Header.LedgerIndex, parent.Header.LedgerIndex+1)
	}
	if closed.Header.ParentCloseTime != parent.Header.CloseTime {
		return errors.New("closed ledger parent close time does not match parent close time")
	}
	if input.ParentCloseTime != protocol.ToRippleTime(parent.Header.CloseTime) {
		return fmt.Errorf("close_input.parent_close_time=%d, want %d", input.ParentCloseTime, protocol.ToRippleTime(parent.Header.CloseTime))
	}
	if input.CloseTime != protocol.ToRippleTime(closed.Header.CloseTime) {
		return fmt.Errorf("close_input.close_time=%d disagrees with closed header", input.CloseTime)
	}
	if input.LedgerSequence != closed.Header.LedgerIndex {
		return fmt.Errorf("close_input.ledger_sequence=%d disagrees with closed header", input.LedgerSequence)
	}
	if input.CloseTimeResolution != closed.Header.CloseTimeResolution {
		return fmt.Errorf("close_input.close_time_resolution=%d disagrees with closed header", input.CloseTimeResolution)
	}
	if input.CloseFlags != closed.Header.CloseFlags {
		return fmt.Errorf("close_input.close_flags=%d disagrees with closed header", input.CloseFlags)
	}
	return nil
}

func snapshotApplyConfig(parent loadedSnapshotLedger, networkID uint32, flags tx.ApplyFlags) openledger.ApplyConfig {
	fees := parent.Fees
	return openledger.ApplyConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            parent.Header.LedgerIndex + 1,
		NetworkID:                 networkID,
		ParentCloseTime:           protocol.ToRippleTime(parent.Header.CloseTime),
		SkipSignatureVerification: false,
		ApplyFlags:                flags,
		Rules:                     parent.EffectiveRules,
	}
}

type snapshotClosedLedgerContext struct {
	ledger    *ledger.Ledger
	feeLevels []txq.FeeLevel
}

func (c snapshotClosedLedgerContext) GetLedgerSequence() uint32 {
	if c.ledger == nil {
		return 0
	}
	return c.ledger.Sequence()
}

func (c snapshotClosedLedgerContext) GetTransactionCount() uint32 {
	if c.ledger == nil {
		return 0
	}
	return c.ledger.TxCount()
}

func (c snapshotClosedLedgerContext) GetTransactionFeeLevels() []txq.FeeLevel {
	return append([]txq.FeeLevel(nil), c.feeLevels...)
}

func snapshotClosedLedgerFeeLevels(closed *ledger.Ledger) ([]txq.FeeLevel, error) {
	if closed == nil {
		return nil, errors.New("closed ledger is nil")
	}
	baseFee := uint64(closed.Fees().Base)
	levels := make([]txq.FeeLevel, 0, closed.TxCount())
	var callbackErr error
	if err := closed.ForEachTransaction(func(_ [32]byte, data []byte) bool {
		raw, _, err := tx.SplitTxWithMetaBlob(data)
		if err != nil {
			callbackErr = err
			return false
		}
		parsed, err := tx.ParseFromBinary(raw)
		if err != nil {
			callbackErr = err
			return false
		}
		common := parsed.GetCommon()
		if common == nil {
			callbackErr = errors.New("closed transaction has no common fields")
			return false
		}
		fee, err := strconv.ParseUint(common.Fee, 10, 64)
		if err != nil {
			callbackErr = fmt.Errorf("parse closed transaction fee: %w", err)
			return false
		}
		levels = append(levels, txq.ToFeeLevel(fee, baseFee))
		return true
	}); err != nil {
		return nil, err
	}
	if callbackErr != nil {
		return nil, callbackErr
	}
	return levels, nil
}

type snapshotHistoryReplay struct {
	Open   *openledger.OpenLedger
	Closed *ledger.Ledger
}

func replaySnapshotHistory(
	history []snapshotHistory,
	queue *txq.TxQ,
	networkID uint32,
	flags tx.ApplyFlags,
) (*snapshotHistoryReplay, error) {
	if len(history) == 0 {
		return nil, nil
	}
	var previous *ledger.Ledger
	var view *openledger.OpenLedger
	for i, item := range history {
		name := fmt.Sprintf("history[%d]", i)
		parent, err := loadSnapshotLedger(item.Parent)
		if err != nil {
			return nil, fmt.Errorf("%s parent snapshot: %w", name, err)
		}
		if previous != nil && parent.Header.Hash != previous.Hash() {
			return nil, fmt.Errorf("%s parent does not follow previous closed ledger", name)
		}
		if view == nil {
			view, err = openledger.New(parent.Ledger, openledger.Config{Rules: parent.EffectiveRules})
			if err != nil {
				return nil, fmt.Errorf("%s open ledger: %w", name, err)
			}
		} else if view.Current().ParentHash() != parent.Header.Hash {
			return nil, fmt.Errorf("%s open ledger does not build on its parent", name)
		}
		apply := snapshotApplyConfig(parent, networkID, flags)
		for j, prior := range item.PreSubmit {
			blob, err := decodeSnapshotBytes(fmt.Sprintf("%s.pre_submit[%d].tx_blob", name, j), prior.TxBlob)
			if err != nil {
				return nil, err
			}
			pending, err := parseSnapshotPending(fmt.Sprintf("%s.pre_submit[%d].tx_blob", name, j), blob)
			if err != nil {
				return nil, err
			}
			if err := applySnapshotOpenLedger(view, prior.OpenLedgerInject, prior.OpenLedgerErase); err != nil {
				return nil, fmt.Errorf("%s.pre_submit[%d] open-ledger mutation: %w", name, j, err)
			}
			if err := submitSnapshot(view, pending, apply, queue, prior.Submit); err != nil {
				return nil, fmt.Errorf("%s.pre_submit[%d]: %w", name, j, err)
			}
		}
		closePending, err := parseSnapshotCloseSet(item.CloseInput.TxBlobs)
		if err != nil {
			return nil, fmt.Errorf("%s close input: %w", name, err)
		}
		built, err := openledger.BuildClosedLedger(parent.Ledger, closePending, openledger.BuildConfig{
			CloseTime:  protocol.FromRippleTime(item.CloseInput.CloseTime),
			CloseFlags: item.CloseInput.CloseFlags,
			Apply:      apply,
		})
		if err != nil {
			return nil, fmt.Errorf("%s build closed ledger: %w", name, err)
		}
		if len(built.Retries) != 0 {
			return nil, fmt.Errorf("%s closed ledger left %d retriable transactions", name, len(built.Retries))
		}
		closed, err := loadSnapshotLedger(item.Closed)
		if err != nil {
			return nil, fmt.Errorf("%s closed snapshot: %w", name, err)
		}
		if err := assertSnapshotLedger(built.Ledger, closed); err != nil {
			return nil, fmt.Errorf("%s closed ledger mismatch: %w", name, err)
		}
		if err := runSnapshotReplayTransactions(networkID, parent, closed, item.Closed.Transactions); err != nil {
			return nil, fmt.Errorf("%s inbound replay mismatch: %w", name, err)
		}
		closedApply := snapshotApplyConfig(closed, networkID, flags)
		levels, err := snapshotClosedLedgerFeeLevels(built.Ledger)
		if err != nil {
			return nil, fmt.Errorf("%s fee history: %w", name, err)
		}
		var retries []openledger.PendingTx
		processClosed := func() {
			queue.ProcessClosedLedger(snapshotClosedLedgerContext{ledger: built.Ledger, feeLevels: levels}, false)
		}
		modifier := func(next *ledger.Ledger) {
			adapter := openledger.NewTxqAdapter(next, closedApply)
			queue.Accept(adapter)
		}
		if err := view.AcceptWithPrecommit(
			built.Ledger, nil, false, &retries, closedApply, queue, processClosed, modifier, nil, nil,
		); err != nil {
			return nil, fmt.Errorf("%s next open ledger: %w", name, err)
		}
		if len(retries) != 0 {
			return nil, fmt.Errorf("%s next open ledger left %d retries", name, len(retries))
		}
		if item.Queue != nil {
			if err := assertSnapshotQueue(queue, view.Current(), item.Queue); err != nil {
				return nil, fmt.Errorf("%s post-close queue: %w", name, err)
			}
		}
		previous = built.Ledger
	}
	return &snapshotHistoryReplay{Open: view, Closed: previous}, nil
}

func assertSnapshotSubmit(out openledger.SubmitOutcome, want snapshotSubmit) error {
	if out.Result.String() != want.EngineResult {
		return fmt.Errorf("engine_result=%q, want %q (%s)", out.Result.String(), want.EngineResult, out.Message)
	}
	if int(out.Result) != want.EngineResultCode {
		return fmt.Errorf("engine_result_code=%d, want %d", out.Result, want.EngineResultCode)
	}
	if out.Applied != want.Applied {
		return fmt.Errorf("applied=%t, want %t", out.Applied, want.Applied)
	}
	if out.Queued != want.Queued {
		return fmt.Errorf("queued=%t, want %t", out.Queued, want.Queued)
	}
	if out.Fee != want.Fee {
		return fmt.Errorf("fee=%d, want %d", out.Fee, want.Fee)
	}
	return nil
}

func assertSnapshotPostSubmitState(got *ledger.Ledger, want []snapshotEntry) error {
	if got == nil {
		return errors.New("post-submit state comparison received nil ledger")
	}
	after, err := got.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot post-submit state: %w", err)
	}
	wantItems, err := snapshotEntryMap(want)
	if err != nil {
		return fmt.Errorf("post_submit_sle: %w", err)
	}
	afterItems, err := snapshotMapItems(after)
	if err != nil {
		return fmt.Errorf("read post-submit state: %w", err)
	}
	if len(wantItems) != len(afterItems) {
		return fmt.Errorf("post_submit_sle item count=%d, want %d", len(wantItems), len(afterItems))
	}
	for index, data := range wantItems {
		actual, found := afterItems[index]
		if !found {
			return fmt.Errorf("post_submit_sle missing state item %x", index)
		}
		if !bytes.Equal(data, actual) {
			return fmt.Errorf("post_submit_sle item %x differs: got %X want %X", index, actual, data)
		}
	}
	return nil
}

func loadSnapshotLedger(snapshot snapshotLedger) (loadedSnapshotLedger, error) {
	hdrBytes, err := decodeSnapshotBytes("ledger header", snapshot.Header)
	if err != nil {
		return loadedSnapshotLedger{}, err
	}
	if len(hdrBytes) != ledgerheader.SizeWithHash {
		return loadedSnapshotLedger{}, fmt.Errorf("ledger header has %d bytes, want %d", len(hdrBytes), ledgerheader.SizeWithHash)
	}
	hdr, err := ledgerheader.DeserializeHeader(hdrBytes, true)
	if err != nil {
		return loadedSnapshotLedger{}, fmt.Errorf("decode ledger header: %w", err)
	}
	if hdr.Hash != ledgerheader.CalculateHash(*hdr) {
		return loadedSnapshotLedger{}, errors.New("ledger header hash does not match its body")
	}
	hdr.Validated = true

	state := shamap.New(shamap.TypeState)
	seenState := make(map[[32]byte]struct{}, len(snapshot.State))
	for i, entry := range snapshot.State {
		index, err := decodeSnapshotHash(fmt.Sprintf("state[%d].index", i), entry.Index)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		if _, duplicate := seenState[index]; duplicate {
			return loadedSnapshotLedger{}, fmt.Errorf("duplicate state index %x", index)
		}
		seenState[index] = struct{}{}
		data, err := decodeSnapshotBytes(fmt.Sprintf("state[%d].data", i), entry.Data)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		if err := validateSnapshotSLEBytes(fmt.Sprintf("state[%d].data", i), data); err != nil {
			return loadedSnapshotLedger{}, err
		}
		if err := state.Put(index, data); err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("insert state item %x: %w", index, err)
		}
	}

	txs := shamap.New(shamap.TypeTransaction)
	seenTxs := make(map[[32]byte]struct{}, len(snapshot.Transactions))
	for i, entry := range snapshot.Transactions {
		hash, err := decodeSnapshotHash(fmt.Sprintf("transactions[%d].hash", i), entry.Hash)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		if _, duplicate := seenTxs[hash]; duplicate {
			return loadedSnapshotLedger{}, fmt.Errorf("duplicate transaction hash %x", hash)
		}
		seenTxs[hash] = struct{}{}
		txBlob, err := decodeSnapshotBytes(fmt.Sprintf("transactions[%d].tx_blob", i), entry.TxBlob)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		metaBlob, err := decodeSnapshotBytes(fmt.Sprintf("transactions[%d].meta_blob", i), entry.MetaBlob)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		parsed, err := tx.ParseFromBinary(txBlob)
		if err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("parse transactions[%d].tx_blob: %w", i, err)
		}
		if !bytes.Equal(txBlob, parsed.GetRawBytes()) {
			return loadedSnapshotLedger{}, fmt.Errorf("transactions[%d].tx_blob is not canonical", i)
		}
		computed, err := tx.ComputeTransactionHash(parsed)
		if err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("hash transactions[%d].tx_blob: %w", i, err)
		}
		if computed != hash {
			return loadedSnapshotLedger{}, fmt.Errorf("transactions[%d] hash does not match tx_blob", i)
		}
		if _, ok := tx.TransactionIndexFromMetadata(metaBlob); !ok {
			return loadedSnapshotLedger{}, fmt.Errorf("transactions[%d].meta_blob lacks TransactionIndex", i)
		}
		if _, err := binarycodec.DecodeBytes(metaBlob); err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("decode transactions[%d].meta_blob: %w", i, err)
		}
		leaf, err := snapshotTxLeaf(txBlob, metaBlob)
		if err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("encode transactions[%d] leaf: %w", i, err)
		}
		if err := txs.PutWithNodeType(hash, leaf, shamap.NodeTypeTransactionWithMeta); err != nil {
			return loadedSnapshotLedger{}, fmt.Errorf("insert transaction %x: %w", hash, err)
		}
	}

	stateHash, err := state.Hash()
	if err != nil {
		return loadedSnapshotLedger{}, fmt.Errorf("hash state map: %w", err)
	}
	if hdr.AccountHash != stateHash {
		return loadedSnapshotLedger{}, errors.New("ledger AccountHash does not match state map")
	}
	txHash, err := txs.Hash()
	if err != nil {
		return loadedSnapshotLedger{}, fmt.Errorf("hash transaction map: %w", err)
	}
	if hdr.TxHash != txHash {
		return loadedSnapshotLedger{}, errors.New("ledger TxHash does not match transaction map")
	}

	ids := make([][32]byte, len(snapshot.Rules))
	for i, value := range snapshot.Rules {
		id, err := decodeSnapshotHash(fmt.Sprintf("rules[%d]", i), value)
		if err != nil {
			return loadedSnapshotLedger{}, err
		}
		if amendment.FeatureByID(id) == nil {
			return loadedSnapshotLedger{}, fmt.Errorf("unknown amendment ID %x", id)
		}
		ids[i] = id
	}
	explicitRules := amendment.NewRules(ids)
	if explicitRules.EnabledCount() != len(ids) {
		return loadedSnapshotLedger{}, errors.New("duplicate amendment IDs")
	}
	fees, err := parseSnapshotFees(snapshot.Fees)
	if err != nil {
		return loadedSnapshotLedger{}, err
	}
	if err := validateSnapshotFees(state, explicitRules, fees); err != nil {
		return loadedSnapshotLedger{}, err
	}
	l, err := ledger.NewFromHeader(*hdr, state, txs, fees)
	if err != nil {
		return loadedSnapshotLedger{}, fmt.Errorf("construct ledger: %w", err)
	}
	loadedRules := l.Rules()
	if loadedRules.EnabledCount() != explicitRules.EnabledCount() {
		return loadedSnapshotLedger{}, errors.New("explicit rules differ from ledger amendments")
	}
	for _, id := range loadedRules.EnabledIDs() {
		if !explicitRules.Enabled(id) {
			return loadedSnapshotLedger{}, fmt.Errorf("ledger amendment %x is absent from explicit rules", id)
		}
	}
	return loadedSnapshotLedger{Ledger: l, Header: *hdr, EffectiveRules: explicitRules, Fees: fees, State: state, Txs: txs}, nil
}

func validateSnapshotFees(stateMap *shamap.SHAMap, rules *amendment.Rules, fees drops.Fees) error {
	item, found, err := stateMap.Get(keylet.Fees().Key)
	if err != nil {
		return fmt.Errorf("read snapshot FeeSettings: %w", err)
	}
	if !found {
		return nil
	}
	settings, err := ledgerstate.ParseFeeSettings(item.Data())
	if err != nil {
		return fmt.Errorf("parse snapshot FeeSettings: %w", err)
	}
	if settings.IsUsingModernFees() && !rules.Enabled(amendment.FeatureXRPFees) {
		return errors.New("snapshot FeeSettings uses XRPFees fields before the amendment is enabled")
	}
	for _, field := range []struct {
		name     string
		present  bool
		amount   uint64
		recorded drops.XRPAmount
	}{
		{"base", settings.HasBaseFee || settings.HasBaseFeeDrops, settings.GetBaseFee(), fees.Base},
		{"reserve", settings.HasReserveBase || settings.HasReserveBaseDrops, settings.GetReserveBase(), fees.Reserve},
		{"increment", settings.HasReserveIncrement || settings.HasReserveIncrementDrops, settings.GetReserveIncrement(), fees.Increment},
	} {
		if field.present && uint64(field.recorded) != field.amount {
			return fmt.Errorf("snapshot fees.%s disagrees with FeeSettings: recorded=%d, ledger=%d", field.name, field.recorded, field.amount)
		}
	}
	return nil
}

func assertSnapshotLedger(got *ledger.Ledger, want loadedSnapshotLedger) error {
	if got == nil {
		return errors.New("got nil ledger")
	}
	if !bytes.Equal(ledgerheader.AddRaw(want.Header, true), ledgerheader.AddRaw(got.Header(), true)) {
		return errors.New("raw ledger header differs")
	}
	if got.Hash() != want.Header.Hash {
		return errors.New("ledger hash differs")
	}
	gotHeader := got.Header()
	if gotHeader.AccountHash != want.Header.AccountHash || gotHeader.TxHash != want.Header.TxHash {
		return errors.New("ledger state or transaction root differs")
	}
	if got.TotalDrops() != want.Header.Drops {
		return fmt.Errorf("ledger drops=%d, want %d", got.TotalDrops(), want.Header.Drops)
	}
	if got.Fees() != want.Fees {
		return errors.New("ledger fees differ")
	}
	gotRules := got.Rules()
	for _, feature := range amendment.AllFeatures() {
		enabled := gotRules.Enabled(feature.ID)
		if enabled != want.EffectiveRules.Enabled(feature.ID) {
			return fmt.Errorf("ledger rules differ for %s: enabled=%t, want %t", feature.Name, enabled, want.EffectiveRules.Enabled(feature.ID))
		}
	}
	stateRoot, err := got.StateMapHash()
	if err != nil {
		return fmt.Errorf("hash got state map: %w", err)
	}
	if stateRoot != want.Header.AccountHash {
		return errors.New("got state root differs from expected header")
	}
	txRoot, err := got.TxMapHash()
	if err != nil {
		return fmt.Errorf("hash got transaction map: %w", err)
	}
	if txRoot != want.Header.TxHash {
		return errors.New("got transaction root differs from expected header")
	}
	gotState, err := got.StateMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot got state map: %w", err)
	}
	gotTxs, err := got.TxMapSnapshot()
	if err != nil {
		return fmt.Errorf("snapshot got transaction map: %w", err)
	}
	if err := assertSnapshotMap(want.State, gotState); err != nil {
		return fmt.Errorf("state map: %w", err)
	}
	if err := assertSnapshotMap(want.Txs, gotTxs); err != nil {
		return fmt.Errorf("transaction map: %w", err)
	}
	return nil
}

func assertSnapshotMap(want, got *shamap.SHAMap) error {
	if want == nil || got == nil {
		return errors.New("nil SHAMap")
	}
	wantItems, err := snapshotMapItems(want)
	if err != nil {
		return fmt.Errorf("read expected map: %w", err)
	}
	gotItems, err := snapshotMapItems(got)
	if err != nil {
		return fmt.Errorf("read actual map: %w", err)
	}
	if len(wantItems) != len(gotItems) {
		return fmt.Errorf("item count=%d, want %d", len(gotItems), len(wantItems))
	}
	for index, wantData := range wantItems {
		gotData, ok := gotItems[index]
		if !ok {
			return fmt.Errorf("missing item %x", index)
		}
		if !bytes.Equal(wantData, gotData) {
			return fmt.Errorf("item %x bytes differ", index)
		}
	}
	return nil
}

func parseSnapshotCloseSet(values []string) ([]openledger.PendingTx, error) {
	result := make([]openledger.PendingTx, 0, len(values))
	seen := make(map[[32]byte]struct{}, len(values))
	for i, value := range values {
		blob, err := decodeSnapshotBytes(fmt.Sprintf("close_input.tx_blobs[%d]", i), value)
		if err != nil {
			return nil, err
		}
		pending, err := parseSnapshotPending(fmt.Sprintf("close_input.tx_blobs[%d]", i), blob)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[pending.Hash]; duplicate {
			return nil, fmt.Errorf("close_input.tx_blobs[%d] duplicates transaction hash %x", i, pending.Hash)
		}
		seen[pending.Hash] = struct{}{}
		result = append(result, pending)
	}
	return result, nil
}

func parseSnapshotPending(name string, blob []byte) (openledger.PendingTx, error) {
	parsed, err := tx.ParseFromBinary(blob)
	if err != nil {
		return openledger.PendingTx{}, fmt.Errorf("parse %s: %w", name, err)
	}
	if !bytes.Equal(blob, parsed.GetRawBytes()) {
		return openledger.PendingTx{}, fmt.Errorf("%s is not canonical", name)
	}
	pending, err := openledger.ParsePendingTx(blob)
	if err != nil {
		return openledger.PendingTx{}, fmt.Errorf("prepare %s: %w", name, err)
	}
	if !bytes.Equal(blob, pending.Blob) {
		return openledger.PendingTx{}, fmt.Errorf("%s changed while preparing pending transaction", name)
	}
	return pending, nil
}

func snapshotPendingContains(values []openledger.PendingTx, want openledger.PendingTx) bool {
	for _, value := range values {
		if value.Hash == want.Hash && bytes.Equal(value.Blob, want.Blob) {
			return true
		}
	}
	return false
}

func snapshotEntryMap(entries []snapshotEntry) (map[[32]byte][]byte, error) {
	result := make(map[[32]byte][]byte, len(entries))
	for i, entry := range entries {
		index, err := decodeSnapshotHash(fmt.Sprintf("entry[%d].index", i), entry.Index)
		if err != nil {
			return nil, err
		}
		data, err := decodeSnapshotBytes(fmt.Sprintf("entry[%d].data", i), entry.Data)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[index]; duplicate {
			return nil, fmt.Errorf("duplicate index %x", index)
		}
		result[index] = data
	}
	return result, nil
}

func snapshotMapItems(shamapValue *shamap.SHAMap) (map[[32]byte][]byte, error) {
	result := make(map[[32]byte][]byte)
	err := shamapValue.ForEach(func(item *shamap.Item) bool {
		data := append([]byte(nil), item.Data()...)
		result[item.Key()] = data
		return true
	})
	return result, err
}

func parseSnapshotFees(fees snapshotFees) (drops.Fees, error) {
	base, err := parseSnapshotUint("fees.base", fees.Base)
	if err != nil {
		return drops.Fees{}, err
	}
	reserve, err := parseSnapshotUint("fees.reserve", fees.Reserve)
	if err != nil {
		return drops.Fees{}, err
	}
	increment, err := parseSnapshotUint("fees.increment", fees.Increment)
	if err != nil {
		return drops.Fees{}, err
	}
	return drops.Fees{Base: drops.XRPAmount(base), Reserve: drops.XRPAmount(reserve), Increment: drops.XRPAmount(increment)}, nil
}

func parseSnapshotUint(name, value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("%s is empty", name)
	}
	if strings.TrimSpace(value) != value || strings.HasPrefix(value, "+") || strings.HasPrefix(value, "-") {
		return 0, fmt.Errorf("%s must be an unsigned decimal integer", name)
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed > math.MaxInt64 {
		return 0, fmt.Errorf("%s is not a valid XRP drop amount", name)
	}
	return parsed, nil
}

func decodeSnapshotBytes(name, value string) ([]byte, error) {
	if value == "" {
		return nil, fmt.Errorf("%s is empty", name)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s is not hexadecimal: %w", name, err)
	}
	return decoded, nil
}

func decodeSnapshotHash(name, value string) ([32]byte, error) {
	decoded, err := decodeSnapshotBytes(name, value)
	if err != nil {
		return [32]byte{}, err
	}
	if len(decoded) != 32 {
		return [32]byte{}, fmt.Errorf("%s has %d bytes, want 32", name, len(decoded))
	}
	var result [32]byte
	copy(result[:], decoded)
	return result, nil
}

func snapshotTxLeaf(txBlob, metaBlob []byte) ([]byte, error) {
	txPart, err := tx.EncodeWithVL(txBlob)
	if err != nil {
		return nil, err
	}
	metaPart, err := tx.EncodeWithVL(metaBlob)
	if err != nil {
		return nil, err
	}
	return append(txPart, metaPart...), nil
}

// validateSnapshotJSONShape checks required keys recursively. Decoder
// DisallowUnknownFields performs the corresponding unknown-key check.
func validateSnapshotJSONShape(data []byte) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("inspect snapshot fixture JSON: %w", err)
	}
	if root == nil {
		return errors.New("snapshot fixture must be a JSON object")
	}
	if raw, present := root["pre_submit"]; present {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return errors.New("pre_submit must be an array")
		}
		var submissions []map[string]json.RawMessage
		if err := json.Unmarshal(raw, &submissions); err != nil {
			return fmt.Errorf("pre_submit must be an array: %w", err)
		}
		for i, submission := range submissions {
			name := fmt.Sprintf("pre_submit[%d]", i)
			if err := requireSnapshotKeysWithOptional(name, submission, []string{"tx_blob", "submit"}, []string{"open_ledger_inject", "open_ledger_erase"}); err != nil {
				return err
			}
			if err := validateSnapshotSubmitJSON(name+".submit", submission["submit"]); err != nil {
				return err
			}
			if err := validateOptionalSnapshotEntriesJSON(name+".open_ledger_inject", submission["open_ledger_inject"]); err != nil {
				return err
			}
			if err := validateOptionalSnapshotEntriesJSON(name+".open_ledger_erase", submission["open_ledger_erase"]); err != nil {
				return err
			}
		}
		delete(root, "pre_submit")
	}
	for _, key := range []string{"open_ledger_inject", "open_ledger_erase"} {
		if raw, present := root[key]; present {
			if err := validateOptionalSnapshotEntriesJSON("fixture."+key, raw); err != nil {
				return err
			}
			delete(root, key)
		}
	}
	if rawHistory, present := root["history"]; present {
		var history []map[string]json.RawMessage
		if err := json.Unmarshal(rawHistory, &history); err != nil || history == nil {
			return errors.New("history must be an array")
		}
		for i, item := range history {
			name := fmt.Sprintf("history[%d]", i)
			if err := requireSnapshotKeysWithOptional(name, item, []string{"parent", "close_input", "closed"}, []string{"pre_submit", "queue"}); err != nil {
				return err
			}
			if err := validateSnapshotLedgerJSON(name+".parent", item["parent"]); err != nil {
				return err
			}
			if err := validateSnapshotLedgerJSON(name+".closed", item["closed"]); err != nil {
				return err
			}
			if err := validateSnapshotCloseInputJSON(name+".close_input", item["close_input"]); err != nil {
				return err
			}
			if rawQueue, exists := item["queue"]; exists {
				if err := validateSnapshotQueueJSON(name+".queue", rawQueue); err != nil {
					return err
				}
			}
			if rawPrior, exists := item["pre_submit"]; exists {
				if err := validateSnapshotPreSubmitJSON(name+".pre_submit", rawPrior); err != nil {
					return err
				}
			}
		}
		delete(root, "history")
	}
	if err := requireSnapshotKeys("fixture", root, "fixture_version", "oracle_repository", "oracle_tag", "oracle_commit", "suite", "testcase", "family", "profile", "network_id", "apply_flags", "skip_signature_verification", "txq_config", "tx_blob", "parent", "close_input", "submit", "closed"); err != nil {
		return err
	}
	var txqConfig map[string]json.RawMessage
	if err := decodeSnapshotObject("txq_config", root["txq_config"], &txqConfig); err != nil {
		return err
	}
	if err := requireSnapshotKeys("txq_config", txqConfig,
		"ledgers_in_queue", "queue_size_min", "retry_sequence_percent",
		"minimum_escalation_multiplier", "minimum_txn_in_ledger",
		"minimum_txn_in_ledger_standalone", "target_txn_in_ledger",
		"maximum_txn_in_ledger", "maximum_txn_in_ledger_set",
		"normal_consensus_increase_percent", "slow_consensus_decrease_percent",
		"maximum_txn_per_account", "minimum_last_ledger_buffer", "standalone"); err != nil {
		return err
	}
	if err := validateSnapshotLedgerJSON("parent", root["parent"]); err != nil {
		return err
	}
	if err := validateSnapshotLedgerJSON("closed", root["closed"]); err != nil {
		return err
	}
	if err := validateSnapshotCloseInputJSON("close_input", root["close_input"]); err != nil {
		return err
	}
	return validateSnapshotSubmitJSON("submit", root["submit"])
}

func validateSnapshotPreSubmitJSON(name string, raw json.RawMessage) error {
	var submissions []map[string]json.RawMessage
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &submissions) != nil {
		return fmt.Errorf("%s must be an array", name)
	}
	for i, submission := range submissions {
		itemName := fmt.Sprintf("%s[%d]", name, i)
		if err := requireSnapshotKeysWithOptional(itemName, submission, []string{"tx_blob", "submit"}, []string{"open_ledger_inject", "open_ledger_erase"}); err != nil {
			return err
		}
		if err := validateSnapshotSubmitJSON(itemName+".submit", submission["submit"]); err != nil {
			return err
		}
		if err := validateOptionalSnapshotEntriesJSON(itemName+".open_ledger_inject", submission["open_ledger_inject"]); err != nil {
			return err
		}
		if err := validateOptionalSnapshotEntriesJSON(itemName+".open_ledger_erase", submission["open_ledger_erase"]); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotCloseInputJSON(name string, raw json.RawMessage) error {
	var closeInput map[string]json.RawMessage
	if err := decodeSnapshotObject(name, raw, &closeInput); err != nil {
		return err
	}
	if err := requireSnapshotKeys(name, closeInput, "parent_close_time", "close_time", "ledger_sequence", "close_time_resolution", "close_flags", "tx_blobs"); err != nil {
		return err
	}
	return nil
}

func validateSnapshotSubmitJSON(name string, raw json.RawMessage) error {
	var submit map[string]json.RawMessage
	if err := decodeSnapshotObject(name, raw, &submit); err != nil {
		return err
	}
	if err := requireSnapshotKeysWithOptional(name, submit, []string{"boundary", "engine_result", "engine_result_code", "applied", "queued", "fee", "post_submit_sle"}, []string{"queue"}); err != nil {
		return err
	}
	if err := validateSnapshotEntriesJSON(name+".post_submit_sle", submit["post_submit_sle"]); err != nil {
		return err
	}
	if rawQueue, present := submit["queue"]; present {
		if err := validateSnapshotQueueJSON(name+".queue", rawQueue); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotQueueJSON(name string, raw json.RawMessage) error {
	var queue map[string]json.RawMessage
	if err := decodeSnapshotObject(name, raw, &queue); err != nil {
		return err
	}
	if err := requireSnapshotKeys(name, queue, "tx_blobs", "metrics"); err != nil {
		return err
	}
	var blobs []string
	if err := json.Unmarshal(queue["tx_blobs"], &blobs); err != nil || blobs == nil {
		return fmt.Errorf("%s.tx_blobs must be an array of strings", name)
	}
	for i, blob := range blobs {
		if strings.TrimSpace(blob) == "" {
			return fmt.Errorf("%s.tx_blobs[%d] is empty", name, i)
		}
	}
	var metrics map[string]json.RawMessage
	if err := decodeSnapshotObject(name+".metrics", queue["metrics"], &metrics); err != nil {
		return err
	}
	if err := requireSnapshotKeysWithNullableOptional(name+".metrics", metrics, []string{
		"tx_count", "tx_in_ledger", "tx_per_ledger",
		"reference_fee_level", "min_processing_fee_level", "med_fee_level",
		"open_ledger_fee_level",
	}, []string{"max_size"}); err != nil {
		return err
	}
	if _, present := metrics["max_size"]; !present {
		return fmt.Errorf("%s.metrics.max_size is missing", name)
	}
	if rawMax := metrics["max_size"]; !bytes.Equal(bytes.TrimSpace(rawMax), []byte("null")) {
		var maxSize uint64
		if err := json.Unmarshal(rawMax, &maxSize); err != nil {
			return fmt.Errorf("%s.metrics.max_size must be a non-negative integer or null", name)
		}
	}
	return nil
}

func validateSnapshotLedgerJSON(name string, raw json.RawMessage) error {
	var object map[string]json.RawMessage
	if err := decodeSnapshotObject(name, raw, &object); err != nil {
		return err
	}
	if err := requireSnapshotKeys(name, object, "header", "rules", "fees", "state", "transactions"); err != nil {
		return err
	}
	var rules []json.RawMessage
	if err := json.Unmarshal(object["rules"], &rules); err != nil {
		return fmt.Errorf("%s.rules must be an array: %w", name, err)
	}
	for i, rawRule := range rules {
		if len(rawRule) == 0 || bytes.Equal(bytes.TrimSpace(rawRule), []byte("null")) {
			return fmt.Errorf("%s.rules[%d] must not be null", name, i)
		}
		var rule string
		if err := json.Unmarshal(rawRule, &rule); err != nil || rule == "" {
			return fmt.Errorf("%s.rules[%d] must be a non-empty string", name, i)
		}
	}
	var fees map[string]json.RawMessage
	if err := decodeSnapshotObject(name+".fees", object["fees"], &fees); err != nil {
		return err
	}
	if err := requireSnapshotKeys(name+".fees", fees, "base", "reserve", "increment"); err != nil {
		return err
	}
	if err := validateSnapshotEntriesJSON(name+".state", object["state"]); err != nil {
		return err
	}
	var transactions []json.RawMessage
	if err := json.Unmarshal(object["transactions"], &transactions); err != nil {
		return fmt.Errorf("%s.transactions must be an array: %w", name, err)
	}
	for i, rawTx := range transactions {
		var object map[string]json.RawMessage
		if err := decodeSnapshotObject(fmt.Sprintf("%s.transactions[%d]", name, i), rawTx, &object); err != nil {
			return err
		}
		if err := requireSnapshotKeys(fmt.Sprintf("%s.transactions[%d]", name, i), object, "hash", "tx_blob", "meta_blob"); err != nil {
			return err
		}
	}
	return nil
}

func validateSnapshotEntriesJSON(name string, raw json.RawMessage) error {
	var entries []json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil {
		return fmt.Errorf("%s must be an array: %w", name, err)
	}
	for i, rawEntry := range entries {
		var object map[string]json.RawMessage
		entryName := fmt.Sprintf("%s[%d]", name, i)
		if err := decodeSnapshotObject(entryName, rawEntry, &object); err != nil {
			return err
		}
		if err := requireSnapshotKeys(entryName, object, "index", "data"); err != nil {
			return err
		}
	}
	return nil
}

func validateOptionalSnapshotEntriesJSON(name string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s must be an array", name)
	}
	return validateSnapshotEntriesJSON(name, raw)
}

func decodeSnapshotObject(name string, raw json.RawMessage, target any) error {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return fmt.Errorf("%s must be an object", name)
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%s must be an object: %w", name, err)
	}
	return nil
}

func requireSnapshotKeys(name string, object map[string]json.RawMessage, keys ...string) error {
	return requireSnapshotKeysWithOptional(name, object, keys, nil)
}

func requireSnapshotKeysWithOptional(name string, object map[string]json.RawMessage, required, optional []string) error {
	return requireSnapshotKeysWithOptionalNullPolicy(name, object, required, optional, nil)
}

func requireSnapshotKeysWithNullableOptional(name string, object map[string]json.RawMessage, required, nullable []string) error {
	return requireSnapshotKeysWithOptionalNullPolicy(name, object, required, nil, nullable)
}

func requireSnapshotKeysWithOptionalNullPolicy(name string, object map[string]json.RawMessage, required, optional, nullable []string) error {
	allowed := make(map[string]struct{}, len(required)+len(optional)+len(nullable))
	for _, key := range append(append(append([]string(nil), required...), optional...), nullable...) {
		allowed[key] = struct{}{}
	}
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return fmt.Errorf("%s.%s is an unknown field", name, key)
		}
	}
	for _, key := range required {
		raw, ok := object[key]
		if !ok {
			return fmt.Errorf("%s.%s is missing", name, key)
		}
		if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return fmt.Errorf("%s.%s must not be null", name, key)
		}
	}
	for _, key := range optional {
		if raw, ok := object[key]; ok && (len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))) {
			return fmt.Errorf("%s.%s must not be null", name, key)
		}
	}
	return nil
}
