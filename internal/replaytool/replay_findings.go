package replaytool

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	rtdebug "runtime/debug"
)

// findingSchema versions the findings record so the lab can evolve the format.
const findingSchema = "goxrpl.replay.finding/v1"

type finding struct {
	Schema                   string             `json:"schema"`
	GoXRPLCommit             string             `json:"goxrpl_commit"`
	LedgerIndex              uint32             `json:"ledger_index"`
	ParentLedgerHash         string             `json:"parent_ledger_hash"`
	TxCount                  int                `json:"tx_count"`
	Hashes                   findingHashes      `json:"hashes"`
	ReconstructionVerified   bool               `json:"reconstruction_verified"`
	DivergingObjects         *[]divergingObject `json:"diverging_objects,omitempty"`
	DivergingObjectsComplete bool               `json:"diverging_objects_complete"`
	DivergingObjectsLimit    int                `json:"diverging_objects_limit"`
	TxSet                    []findingTx        `json:"tx_set"`
	Errors                   []string           `json:"errors,omitempty"`
}

type findingHashes struct {
	LedgerGot           string `json:"ledger_got"`
	LedgerExpected      string `json:"ledger_expected"`
	AccountGot          string `json:"account_got"`
	AccountExpected     string `json:"account_expected"`
	TransactionGot      string `json:"transaction_got"`
	TransactionExpected string `json:"transaction_expected"`
	TotalCoinsGot       uint64 `json:"total_coins_got"`
	TotalCoinsExpected  uint64 `json:"total_coins_expected"`
}

// divergingObject is a state object whose goXRPL value differs from mainnet's.
// goXRPL/Mainnet hold the hex-encoded serialized SLE on each side; an empty
// string means the object is absent on that side. Decoded carries the JSON
// view of the mainnet object for readability.
type divergingObject struct {
	Index          string         `json:"index"`
	GoXRPL         string         `json:"goxrpl,omitempty"`
	Mainnet        string         `json:"mainnet,omitempty"`
	GoXRPLDecoded  map[string]any `json:"goxrpl_decoded,omitempty"`
	MainnetDecoded map[string]any `json:"mainnet_decoded,omitempty"`
}

type findingTx struct {
	Index  int    `json:"index"`
	Hash   string `json:"hash"`
	Type   string `json:"type,omitempty"`
	Result string `json:"result,omitempty"`
}

// findingsWriter appends findings to a file as JSON Lines (one record per
// line), so a long-running survey streams findings without buffering them all.
type findingsWriter struct {
	path   string
	tmp    *os.File
	buffer *bufio.Writer
	enc    *json.Encoder
	failed error
	closed bool
}

func newFindingsWriter(path string) (*findingsWriter, error) {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return nil, fmt.Errorf("opening findings file %s: %w", path, err)
	}
	cleanup := func(loadErr error) (*findingsWriter, error) {
		return nil, errors.Join(loadErr, tmp.Close(), os.Remove(tmp.Name()))
	}
	if err := tmp.Chmod(0o644); err != nil {
		return cleanup(fmt.Errorf("setting findings mode: %w", err))
	}
	previous, err := os.Open(path)
	if err == nil {
		stat, statErr := previous.Stat()
		if statErr != nil {
			return cleanup(errors.Join(fmt.Errorf("stating prior findings: %w", statErr), previous.Close()))
		}
		if _, err := io.Copy(tmp, previous); err != nil {
			return cleanup(errors.Join(fmt.Errorf("copying prior findings: %w", err), previous.Close()))
		}
		if stat.Size() > 0 {
			var last [1]byte
			if _, err := previous.ReadAt(last[:], stat.Size()-1); err != nil {
				return cleanup(errors.Join(fmt.Errorf("reading prior findings boundary: %w", err), previous.Close()))
			}
			if last[0] != '\n' {
				if _, err := tmp.Write([]byte{'\n'}); err != nil {
					return cleanup(errors.Join(fmt.Errorf("separating prior findings: %w", err), previous.Close()))
				}
			}
		}
		if err := previous.Close(); err != nil {
			return cleanup(fmt.Errorf("closing prior findings: %w", err))
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cleanup(fmt.Errorf("opening prior findings: %w", err))
	}
	buffer := bufio.NewWriter(tmp)
	return &findingsWriter{path: path, tmp: tmp, buffer: buffer, enc: json.NewEncoder(buffer)}, nil
}

func (w *findingsWriter) Write(finding *finding) error {
	if w.closed {
		return errors.New("findings writer is closed")
	}
	if w.failed != nil {
		return w.failed
	}
	if err := w.enc.Encode(finding); err != nil {
		w.failed = err
	}
	return w.failed
}

func (w *findingsWriter) Close() (err error) {
	if w.closed {
		return nil
	}
	w.closed = true
	tmpName := w.tmp.Name()
	committed := false
	defer func() {
		if !committed {
			err = errors.Join(err, os.Remove(tmpName))
		}
	}()
	if w.failed != nil {
		return errors.Join(w.failed, w.tmp.Close())
	}
	if err := w.buffer.Flush(); err != nil {
		return errors.Join(fmt.Errorf("flushing findings: %w", err), w.tmp.Close())
	}
	if err := w.tmp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("syncing findings: %w", err), w.tmp.Close())
	}
	if err := w.tmp.Close(); err != nil {
		return fmt.Errorf("closing findings: %w", err)
	}
	if err := os.Rename(tmpName, w.path); err != nil {
		return fmt.Errorf("publishing findings: %w", err)
	}
	committed = true
	if err := syncDirectory(filepath.Dir(w.path)); err != nil {
		return fmt.Errorf("syncing findings directory: %w", err)
	}
	return nil
}

// goxrplCommit resolves the commit tag stamped onto every finding so a run and
// its findings are reproducible against a specific build. An explicit override
// wins; otherwise it reads the VCS revision embedded by the Go toolchain.
func goxrplCommit(override string) string {
	if override != "" {
		return override
	}
	if info, ok := rtdebug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			if setting.Key == "vcs.revision" {
				rev := setting.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
				return rev
			}
		}
	}
	return "unknown"
}

// buildFinding assembles a finding from a divergent block result and the
// reconstructed mainnet post-state. diverging holds the exact set of objects
// that differ between goXRPL's post-state and a verified mainnet reconstruction;
// unverified candidates never carry object-level diagnoses.
func buildFinding(commit string, ledgerIndex uint32, parentHash [32]byte, result *blockResult, reconstructionVerified bool, diverging []divergingObject, completeness ...bool) *finding {
	hexOf := func(b [32]byte) string { return hex.EncodeToString(b[:]) }
	var findingDiverging *[]divergingObject
	if reconstructionVerified {
		if diverging == nil {
			diverging = []divergingObject{}
		}
		findingDiverging = &diverging
	}
	complete := true
	if len(completeness) > 0 {
		complete = completeness[0]
	}

	txSet := make([]findingTx, 0, len(result.TxResults))
	for _, t := range result.TxResults {
		txSet = append(txSet, findingTx{
			Index:  t.Index,
			Hash:   t.Hash,
			Type:   t.TxType,
			Result: t.Result,
		})
	}

	return &finding{
		Schema:           findingSchema,
		GoXRPLCommit:     commit,
		LedgerIndex:      ledgerIndex,
		ParentLedgerHash: hexOf(parentHash),
		TxCount:          result.TxCount,
		Hashes: findingHashes{
			LedgerGot:           hexOf(result.LedgerHash),
			LedgerExpected:      hexOf(result.ExpectedLedgerHash),
			AccountGot:          hexOf(result.AccountHash),
			AccountExpected:     hexOf(result.ExpectedAccountHash),
			TransactionGot:      hexOf(result.TransactionHash),
			TransactionExpected: hexOf(result.ExpectedTransactionHash),
			TotalCoinsGot:       result.TotalCoins,
			TotalCoinsExpected:  result.ExpectedTotalCoins,
		},
		ReconstructionVerified:   reconstructionVerified,
		DivergingObjects:         findingDiverging,
		DivergingObjectsComplete: complete,
		DivergingObjectsLimit:    maxDiagnosticObjects,
		TxSet:                    txSet,
		Errors:                   append([]string(nil), result.Errors...),
	}
}
