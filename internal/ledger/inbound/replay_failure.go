package inbound

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
)

// ReplayFailure is a structured replay divergence. Unwrap preserves the
// sentinel used for classification while the exported fields retain the
// bytes and values needed to diagnose a failure after the acquisition has
// moved to StateFailed.
//
// Metadata byte slices are encoded as base64 by encoding/json. Hashes and
// roots use their native [32]byte representation so callers can compare them
// without reparsing a textual form.
type ReplayFailure struct {
	Kind    string `json:"kind"`
	Stage   string `json:"stage,omitempty"`
	Message string `json:"message,omitempty"`

	TxIndex uint32   `json:"tx_index,omitempty"`
	TxHash  [32]byte `json:"tx_hash,omitempty"`

	ExpectedMetadata []byte `json:"expected_metadata,omitempty"`
	ActualMetadata   []byte `json:"actual_metadata,omitempty"`
	ExpectedResult   string `json:"expected_result,omitempty"`
	ActualResult     string `json:"actual_result,omitempty"`

	ExpectedRoot [32]byte `json:"expected_root,omitempty"`
	ActualRoot   [32]byte `json:"actual_root,omitempty"`
	ExpectedHash [32]byte `json:"expected_hash,omitempty"`
	ActualHash   [32]byte `json:"actual_hash,omitempty"`

	cause error `json:"-"`
}

func newReplayFailure(cause error, stage, format string, args ...any) *ReplayFailure {
	message := fmt.Sprintf(format, args...)
	kind := ""
	if cause != nil {
		kind = cause.Error()
	}
	return &ReplayFailure{
		Kind:    kind,
		Stage:   stage,
		Message: message,
		cause:   cause,
	}
}

// Error implements error and retains the sentinel text in the message so
// logs remain useful even when callers do not inspect the typed fields.
func (f *ReplayFailure) Error() string {
	if f == nil {
		return "<nil replay failure>"
	}
	if f.Kind == "" {
		return f.Message
	}
	if f.Message == "" {
		return f.Kind
	}
	return f.Kind + ": " + f.Message
}

// Unwrap lets errors.Is classify a failure by its replay sentinel.
func (f *ReplayFailure) Unwrap() error {
	if f == nil {
		return nil
	}
	return f.cause
}

// ReplayEvidence is the immutable forensic record available from a replay
// acquisition. It includes the authenticated target header, parent identity,
// and every original transaction, metadata blob, and transaction-tree leaf.
// The parent state contents are intentionally collected by the service layer,
// which can stream and certify them without copying the entire map here.
type ReplayEvidence struct {
	Hash           [32]byte            `json:"hash"`
	PeerID         uint64              `json:"peer_id"`
	ParentHash     [32]byte            `json:"parent_hash"`
	ParentSequence uint32              `json:"parent_sequence"`
	TargetHeader   header.LedgerHeader `json:"target_header"`
	Transactions   []DecodedTx         `json:"transactions"`
	Failure        *ReplayFailure      `json:"failure,omitempty"`
	Error          string              `json:"error,omitempty"`
}

// Evidence returns a deep copy of the replay inputs and current failure. It
// remains available after Apply fails, allowing a caller to persist evidence
// before certifying the parent or invoking Retry.
func (r *ReplayDelta) Evidence() ReplayEvidence {
	r.mu.Lock()
	defer r.mu.Unlock()

	evidence := ReplayEvidence{
		Hash:         r.hash,
		PeerID:       r.peerID,
		Transactions: cloneDecodedTxs(r.txs),
	}
	if r.parent != nil {
		evidence.ParentHash = r.parent.Hash()
		evidence.ParentSequence = r.parent.Sequence()
	}
	if r.result != nil {
		evidence.TargetHeader = r.result.Header()
	}
	if r.err != nil {
		evidence.Error = r.err.Error()
		var failure *ReplayFailure
		if ok := asReplayFailure(r.err, &failure); ok {
			evidence.Failure = cloneReplayFailure(failure)
		}
	}
	return evidence
}

// EvidenceJSON serializes the same deep-copied evidence returned by Evidence
// for durable diagnostic storage.
func (r *ReplayDelta) EvidenceJSON() ([]byte, error) {
	return json.Marshal(r.Evidence())
}

func cloneDecodedTxs(txs []DecodedTx) []DecodedTx {
	if len(txs) == 0 {
		return nil
	}
	out := make([]DecodedTx, len(txs))
	for i, dtx := range txs {
		out[i] = dtx
		out[i].TxBytes = append([]byte(nil), dtx.TxBytes...)
		out[i].MetaBytes = append([]byte(nil), dtx.MetaBytes...)
		out[i].LeafBlob = append([]byte(nil), dtx.LeafBlob...)
	}
	return out
}

func cloneReplayFailure(failure *ReplayFailure) *ReplayFailure {
	if failure == nil {
		return nil
	}
	copyFailure := *failure
	copyFailure.ExpectedMetadata = append([]byte(nil), failure.ExpectedMetadata...)
	copyFailure.ActualMetadata = append([]byte(nil), failure.ActualMetadata...)
	return &copyFailure
}

// asReplayFailure is kept local so Evidence can preserve wrapped failures if
// a future apply step adds context around the typed error.
func asReplayFailure(err error, target **ReplayFailure) bool {
	return errors.As(err, target)
}

// transactionResultFromMetadata is best-effort forensic context. GotResponse
// already authenticated the metadata bytes; a malformed map simply leaves the
// expected result empty while the exact bytes remain available in evidence.
func transactionResultFromMetadata(metaBytes []byte) string {
	if len(metaBytes) == 0 {
		return ""
	}
	metadata, err := binarycodec.Decode(fmt.Sprintf("%x", metaBytes))
	if err != nil {
		return ""
	}
	result, _ := metadata["TransactionResult"].(string)
	return result
}
