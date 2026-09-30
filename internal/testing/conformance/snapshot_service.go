package conformance

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	"github.com/LeJamon/go-xrpl/internal/txq"
)

// SnapshotCase is the validated v4 input needed by a ledger service execution.
// Its parent and closed maps are loaded through the same pinned corpus loader
// used by the conformance runner.
type SnapshotCase struct {
	Name      string
	Pin       PinnedFixturePin
	Fixture   SnapshotFixture
	Parent    SnapshotLedger
	Closed    SnapshotLedger
	TxBlob    []byte
	TxHash    [32]byte
	CloseSet  [][]byte
	PreSubmit []SnapshotSubmission
	History   []SnapshotHistoryCase
}

// SnapshotSubmission is one signed transaction submitted before the primary
// fixture transaction. Its expected result and post-submit state are captured
// independently by the oracle.
type SnapshotSubmission struct {
	TxBlob           []byte
	TxHash           [32]byte
	Submit           SnapshotSubmit
	OpenLedgerInject []SnapshotEntry
	OpenLedgerErase  []SnapshotEntry
}

type SnapshotFixture = snapshotFixture
type SnapshotSubmit = snapshotSubmit
type SnapshotQueue = snapshotQueue
type SnapshotQueueMetrics = snapshotQueueMetrics
type SnapshotHistory = snapshotHistory
type SnapshotCloseInput = snapshotCloseInput
type SnapshotTxQConfig = snapshotTxQConfig
type SnapshotLedger = loadedSnapshotLedger
type SnapshotEntry = snapshotEntry

// SnapshotHistoryCase is one authenticated ledger transition replayed before
// the main fixture parent. CloseSet is the parsed close_input transaction set;
// Queue, when present, is the oracle's post-close open-ledger queue state.
type SnapshotHistoryCase struct {
	Parent     SnapshotLedger
	PreSubmit  []SnapshotSubmission
	CloseSet   [][]byte
	CloseInput SnapshotCloseInput
	Closed     SnapshotLedger
	Queue      *SnapshotQueue
}

type PinnedFixturePin = pinnedFixturePin

// LoadSnapshotCases resolves and validates the required pinned corpus, then
// prepares the signed submission and independent consensus close set.
func LoadSnapshotCases() ([]SnapshotCase, error) {
	corpus, err := resolvePinnedCorpus(false)
	if err != nil {
		return nil, err
	}
	all.RegisterAll()
	result := make([]SnapshotCase, 0, len(corpus.Cases))
	for _, pinned := range corpus.Cases {
		parent, err := loadSnapshotLedger(pinned.Fixture.Parent)
		if err != nil {
			return nil, fmt.Errorf("%s parent: %w", pinned.Name, err)
		}
		closed, err := loadSnapshotLedger(pinned.Fixture.Closed)
		if err != nil {
			return nil, fmt.Errorf("%s closed: %w", pinned.Name, err)
		}
		if err := validateSnapshotLineage(parent, closed, pinned.Fixture.CloseInput); err != nil {
			return nil, fmt.Errorf("%s lineage: %w", pinned.Name, err)
		}
		txBlob, err := decodeSnapshotBytes("tx_blob", pinned.Fixture.TxBlob)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pinned.Name, err)
		}
		pending, err := parseSnapshotPending("tx_blob", txBlob)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pinned.Name, err)
		}
		closePending, err := parseSnapshotCloseSet(pinned.Fixture.CloseInput.TxBlobs)
		if err != nil {
			return nil, fmt.Errorf("%s close_input: %w", pinned.Name, err)
		}
		if pinned.Fixture.Submit.Applied && !snapshotPendingContains(closePending, pending) {
			return nil, fmt.Errorf("%s applied tx_blob is absent from close_input.tx_blobs", pinned.Name)
		}
		closeSet := make([][]byte, len(closePending))
		for i := range closePending {
			closeSet[i] = append([]byte(nil), closePending[i].Blob...)
		}
		preSubmit := make([]SnapshotSubmission, 0, len(pinned.Fixture.PreSubmit))
		for i, prior := range pinned.Fixture.PreSubmit {
			priorBlob, err := decodeSnapshotBytes(fmt.Sprintf("pre_submit[%d].tx_blob", i), prior.TxBlob)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", pinned.Name, err)
			}
			priorPending, err := parseSnapshotPending(fmt.Sprintf("pre_submit[%d].tx_blob", i), priorBlob)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", pinned.Name, err)
			}
			preSubmit = append(preSubmit, SnapshotSubmission{
				TxBlob:           append([]byte(nil), priorBlob...),
				TxHash:           priorPending.Hash,
				Submit:           prior.Submit,
				OpenLedgerInject: append([]SnapshotEntry(nil), prior.OpenLedgerInject...),
				OpenLedgerErase:  append([]SnapshotEntry(nil), prior.OpenLedgerErase...),
			})
		}
		history := make([]SnapshotHistoryCase, 0, len(pinned.Fixture.History))
		for historyIndex, item := range pinned.Fixture.History {
			historyParent, err := loadSnapshotLedger(item.Parent)
			if err != nil {
				return nil, fmt.Errorf("%s history[%d] parent: %w", pinned.Name, historyIndex, err)
			}
			historyClosed, err := loadSnapshotLedger(item.Closed)
			if err != nil {
				return nil, fmt.Errorf("%s history[%d] closed: %w", pinned.Name, historyIndex, err)
			}
			historyClosePending, err := parseSnapshotCloseSet(item.CloseInput.TxBlobs)
			if err != nil {
				return nil, fmt.Errorf("%s history[%d] close_input: %w", pinned.Name, historyIndex, err)
			}
			historyCloseSet := make([][]byte, len(historyClosePending))
			for j := range historyClosePending {
				historyCloseSet[j] = append([]byte(nil), historyClosePending[j].Blob...)
			}
			historyPreSubmit := make([]SnapshotSubmission, 0, len(item.PreSubmit))
			for j, prior := range item.PreSubmit {
				priorBlob, err := decodeSnapshotBytes(fmt.Sprintf("history[%d].pre_submit[%d].tx_blob", historyIndex, j), prior.TxBlob)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", pinned.Name, err)
				}
				priorPending, err := parseSnapshotPending(fmt.Sprintf("history[%d].pre_submit[%d].tx_blob", historyIndex, j), priorBlob)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", pinned.Name, err)
				}
				historyPreSubmit = append(historyPreSubmit, SnapshotSubmission{
					TxBlob:           append([]byte(nil), priorBlob...),
					TxHash:           priorPending.Hash,
					Submit:           prior.Submit,
					OpenLedgerInject: append([]SnapshotEntry(nil), prior.OpenLedgerInject...),
					OpenLedgerErase:  append([]SnapshotEntry(nil), prior.OpenLedgerErase...),
				})
			}
			history = append(history, SnapshotHistoryCase{
				Parent:     historyParent,
				PreSubmit:  historyPreSubmit,
				CloseSet:   historyCloseSet,
				CloseInput: item.CloseInput,
				Closed:     historyClosed,
				Queue:      item.Queue,
			})
		}
		result = append(result, SnapshotCase{
			Name:      pinned.Name,
			Pin:       pinned.Pin,
			Fixture:   pinned.Fixture,
			Parent:    parent,
			Closed:    closed,
			TxBlob:    append([]byte(nil), txBlob...),
			TxHash:    pending.Hash,
			CloseSet:  closeSet,
			PreSubmit: preSubmit,
			History:   history,
		})
	}
	return result, nil
}

// AssertSnapshotLedger compares protocol header bytes, roots, fees, and every
// state/transaction leaf against a loaded oracle ledger.
func AssertSnapshotLedger(got *ledger.Ledger, want SnapshotLedger) error {
	return assertSnapshotLedger(got, want)
}

// AssertSnapshotSubmit compares the open-ledger result fields recorded by the
// oracle. Queue membership is checked by the service harness because the
// service result type exposes that state through QueueAllTxs.
func AssertSnapshotSubmit(got openledger.SubmitOutcome, want SnapshotSubmit) error {
	return assertSnapshotSubmit(got, want)
}

// AssertSnapshotQueue compares the fee-ordered queue candidates and metrics
// captured at an oracle submission boundary.
func AssertSnapshotQueue(
	got []*txq.CandidateDetails,
	metrics txq.Metrics,
	want *SnapshotQueue,
) error {
	return assertSnapshotQueueDetails(got, metrics, want)
}

// ApplySnapshotOpenLedgerChanges applies the authenticated raw state surgery
// captured for one submission while keeping the view open. It is shared by
// the engine and service harnesses so both replay paths enforce the same
// erase-byte and insert-absence checks.
func ApplySnapshotOpenLedgerChanges(
	view *openledger.OpenLedger,
	inject, erase []SnapshotEntry,
) error {
	return applySnapshotOpenLedger(view, inject, erase)
}

func AssertSnapshotPostSubmitState(got *ledger.Ledger, want []SnapshotEntry) error {
	return assertSnapshotPostSubmitState(got, want)
}
