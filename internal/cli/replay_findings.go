package cli

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	rtdebug "runtime/debug"
)

// findingSchema versions the findings record so the lab can evolve the format.
const findingSchema = "goxrpl.replay.finding/v1"

// Finding is one replay divergence, recorded in a structured, commit-tagged
// form so the parallel fleet can survey every divergence in a single pass and
// dedup by root cause downstream.
type Finding struct {
	Schema                 string            `json:"schema"`
	GoXRPLCommit           string            `json:"goxrpl_commit"`
	LedgerIndex            uint32            `json:"ledger_index"`
	ParentLedgerHash       string            `json:"parent_ledger_hash"`
	TxCount                int               `json:"tx_count"`
	Hashes                 findingHashes     `json:"hashes"`
	ReconstructionVerified bool              `json:"reconstruction_verified"`
	DivergingObjects       []divergingObject `json:"diverging_objects"`
	TxSet                  []findingTx       `json:"tx_set"`
	Errors                 []string          `json:"errors,omitempty"`
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

// findingsWriter appends Findings to a file as JSON Lines (one record per
// line), so a long-running survey streams findings without buffering them all.
type findingsWriter struct {
	f   *os.File
	enc *json.Encoder
}

func newFindingsWriter(path string) (*findingsWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("opening findings file %s: %w", path, err)
	}
	return &findingsWriter{f: f, enc: json.NewEncoder(f)}, nil
}

func (w *findingsWriter) Write(finding *Finding) error {
	return w.enc.Encode(finding)
}

func (w *findingsWriter) Close() error {
	return w.f.Close()
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

// buildFinding assembles a Finding from a divergent block result and the
// reconstructed mainnet post-state. diverging holds the exact set of objects
// that differ between goXRPL's post-state and mainnet's.
func buildFinding(commit string, ledgerIndex uint32, parentHash [32]byte, result *BlockResult, reconstructionVerified bool, diverging []divergingObject) *Finding {
	hexOf := func(b [32]byte) string { return hex.EncodeToString(b[:]) }

	txSet := make([]findingTx, 0, len(result.TxResults))
	for _, t := range result.TxResults {
		txSet = append(txSet, findingTx{
			Index:  t.Index,
			Hash:   t.Hash,
			Type:   t.TxType,
			Result: t.Result,
		})
	}

	return &Finding{
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
		ReconstructionVerified: reconstructionVerified,
		DivergingObjects:       diverging,
		TxSet:                  txSet,
		Errors:                 result.Errors,
	}
}
