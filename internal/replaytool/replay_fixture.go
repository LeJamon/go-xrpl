package replaytool

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/drops"
	ledgerstate "github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

type expectedTransaction struct {
	Index    int
	Hash     [32]byte
	MetaBlob []byte
}

type expectedBlock struct {
	LedgerHash      [32]byte
	AccountHash     [32]byte
	TransactionHash [32]byte
	TotalCoins      uint64
	Transactions    []expectedTransaction
}

type validatedFixture struct {
	State     *stateFixture
	Env       *envFixture
	Txs       *txsFixture
	Expected  *expectedFixture
	Execution blockExecution
	Want      expectedBlock
}

func loadValidatedFixture(ctx context.Context, dir string) (*validatedFixture, error) {
	state := &stateFixture{}
	if _, err := loadStrictJSON(filepath.Join(dir, "state.json"), state, "ledger_index", "account_hash", "entries"); err != nil {
		return nil, fmt.Errorf("loading state.json: %w", err)
	}
	env := &envFixture{}
	envFields, err := loadStrictJSON(
		filepath.Join(dir, "env.json"),
		env,
		"ledger_index", "parent_hash", "parent_close_time", "close_time",
		"close_time_resolution", "close_flags", "total_coins", "fees", "amendments",
	)
	if err != nil {
		return nil, fmt.Errorf("loading env.json: %w", err)
	}
	if err := requireNestedFields(envFields["fees"], "base_fee", "reserve_base", "reserve_increment"); err != nil {
		return nil, fmt.Errorf("loading env.json fees: %w", err)
	}
	txs := &txsFixture{}
	if _, err := loadStrictJSON(filepath.Join(dir, "txs.json"), txs, "transactions"); err != nil {
		return nil, fmt.Errorf("loading txs.json: %w", err)
	}
	expected := &expectedFixture{}
	if _, err := loadStrictJSON(
		filepath.Join(dir, "expected.json"),
		expected,
		"ledger_index", "ledger_hash", "account_hash", "transaction_hash", "total_coins", "transactions",
	); err != nil {
		return nil, fmt.Errorf("loading expected.json: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if state.LedgerIndex == math.MaxUint32 || state.LedgerIndex+1 != env.LedgerIndex {
		return nil, fmt.Errorf("fixture ledger linkage: state ledger %d must immediately precede env ledger %d", state.LedgerIndex, env.LedgerIndex)
	}
	if expected.LedgerIndex != env.LedgerIndex {
		return nil, fmt.Errorf("fixture ledger linkage: expected ledger %d does not match env ledger %d", expected.LedgerIndex, env.LedgerIndex)
	}

	stateHash, err := parseHash("state account_hash", state.AccountHash)
	if err != nil {
		return nil, err
	}
	parentHash, err := parseHash("env parent_hash", env.ParentHash)
	if err != nil {
		return nil, err
	}
	want := expectedBlock{}
	if want.LedgerHash, err = parseHash("expected ledger_hash", expected.LedgerHash); err != nil {
		return nil, err
	}
	if want.AccountHash, err = parseHash("expected account_hash", expected.AccountHash); err != nil {
		return nil, err
	}
	if want.TransactionHash, err = parseHash("expected transaction_hash", expected.TransactionHash); err != nil {
		return nil, err
	}
	if want.TotalCoins, err = parseDrops(expected.TotalCoins); err != nil {
		return nil, fmt.Errorf("expected total_coins: %w", err)
	}
	totalCoins, err := parseDrops(env.TotalCoins)
	if err != nil {
		return nil, fmt.Errorf("env total_coins: %w", err)
	}
	closeTime, err := replayCloseTime(env.CloseTime)
	if err != nil {
		return nil, fmt.Errorf("env close_time: %w", err)
	}
	parentCloseTime, err := replayCloseTime(env.ParentCloseTime)
	if err != nil {
		return nil, fmt.Errorf("env parent_close_time: %w", err)
	}
	if env.CloseTimeResolution > math.MaxUint8 {
		return nil, fmt.Errorf("env close_time_resolution %d exceeds uint8", env.CloseTimeResolution)
	}

	stateMap := shamap.New(shamap.TypeState)
	seenState := make(map[[32]byte]struct{}, len(state.Entries))
	for i, entry := range state.Entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key, err := parseHash(fmt.Sprintf("state entry %d index", i), entry.Index)
		if err != nil {
			return nil, err
		}
		if _, exists := seenState[key]; exists {
			return nil, fmt.Errorf("state entry %d duplicates index %x", i, key)
		}
		seenState[key] = struct{}{}
		data, err := hex.DecodeString(entry.Data)
		if err != nil {
			return nil, fmt.Errorf("state entry %d data: %w", i, err)
		}
		if _, err := binarycodec.DecodeBytes(data); err != nil {
			return nil, fmt.Errorf("state entry %d data is not a serialized ledger object: %w", i, err)
		}
		if err := stateMap.Put(key, data); err != nil {
			return nil, fmt.Errorf("state entry %d: %w", i, err)
		}
	}
	root, err := stateMap.Hash()
	if err != nil {
		return nil, fmt.Errorf("computing seed account_hash: %w", err)
	}
	if root != stateHash {
		return nil, fmt.Errorf("seed state account_hash mismatch: computed %x, fixture %x", root, stateHash)
	}
	fees, err := feesFromStateMap(stateMap)
	if err != nil {
		return nil, fmt.Errorf("loading fixture fees: %w", err)
	}
	if uint64(fees.Base) != env.Fees.BaseFee || uint64(fees.Reserve) != env.Fees.ReserveBase || uint64(fees.Increment) != env.Fees.ReserveIncrement {
		return nil, fmt.Errorf(
			"env fees (%d/%d/%d) do not match state fees (%d/%d/%d)",
			env.Fees.BaseFee, env.Fees.ReserveBase, env.Fees.ReserveIncrement,
			fees.Base, fees.Reserve, fees.Increment,
		)
	}
	declaredRules, err := buildRulesFromAmendments(env.Amendments)
	if err != nil {
		return nil, fmt.Errorf("env amendments: %w", err)
	}
	rules, err := loadRulesFromState(stateMap)
	if err != nil {
		return nil, fmt.Errorf("state amendments: %w", err)
	}
	if !sameRuleSet(declaredRules, rules) {
		return nil, errors.New("env amendments do not match the seed state's Amendments ledger entry")
	}
	transactions, expectedTransactions, err := validateFixtureTransactions(ctx, txs.Transactions, expected.Transactions)
	if err != nil {
		return nil, err
	}
	want.Transactions = expectedTransactions

	return &validatedFixture{
		State:    state,
		Env:      env,
		Txs:      txs,
		Expected: expected,
		Execution: blockExecution{
			StateMap:            stateMap,
			LedgerIndex:         env.LedgerIndex,
			ParentHash:          parentHash,
			ParentCloseTime:     parentCloseTime,
			CloseTime:           closeTime,
			CloseTimeResolution: uint8(env.CloseTimeResolution),
			CloseFlags:          env.CloseFlags,
			TotalCoins:          totalCoins,
			Fees:                fees,
			Rules:               rules,
			Transactions:        transactions,
			WantPostStateCount:  true,
		},
		Want: want,
	}, nil
}

func validateFixtureTransactions(
	ctx context.Context,
	entries []fixtureTxEntry,
	expected []expectedTxEntry,
) ([]blockTransaction, []expectedTransaction, error) {
	if len(entries) != len(expected) {
		return nil, nil, fmt.Errorf("fixture transaction count %d does not match expected count %d", len(entries), len(expected))
	}
	transactions := make([]blockTransaction, len(entries))
	want := make([]expectedTransaction, len(expected))
	for i := range entries {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		entry := entries[i]
		expectedEntry := expected[i]
		if entry.Index != i || expectedEntry.Index != i {
			return nil, nil, fmt.Errorf("transaction %d indices must be contiguous and ordered (tx=%d expected=%d)", i, entry.Index, expectedEntry.Index)
		}
		hash, err := parseHash(fmt.Sprintf("transaction %d hash", i), entry.Hash)
		if err != nil {
			return nil, nil, err
		}
		expectedHash, err := parseHash(fmt.Sprintf("expected transaction %d hash", i), expectedEntry.Hash)
		if err != nil {
			return nil, nil, err
		}
		if expectedHash != hash {
			return nil, nil, fmt.Errorf("expected transaction %d hash does not match input hash", i)
		}
		blob, err := hex.DecodeString(entry.TxBlob)
		if err != nil || len(blob) == 0 {
			return nil, nil, fmt.Errorf("transaction %d tx_blob: %w", i, errors.Join(err, errorIf(len(blob) == 0, "empty blob")))
		}
		parsed, err := txengine.ParseAndPrepare(blob)
		if err != nil {
			return nil, nil, fmt.Errorf("transaction %d tx_blob: %w", i, err)
		}
		computed, err := tx.ComputeTransactionHash(parsed.Transaction)
		if err != nil {
			return nil, nil, fmt.Errorf("transaction %d hash: %w", i, err)
		}
		if computed != hash {
			return nil, nil, fmt.Errorf("transaction %d declared hash %x does not match computed hash %x", i, hash, computed)
		}
		metaBlob, err := hex.DecodeString(expectedEntry.Meta)
		if err != nil || len(metaBlob) == 0 {
			return nil, nil, fmt.Errorf("expected transaction %d meta: %w", i, errors.Join(err, errorIf(len(metaBlob) == 0, "empty blob")))
		}
		decodedMeta, err := binarycodec.DecodeBytes(metaBlob)
		if err != nil {
			return nil, nil, fmt.Errorf("expected transaction %d meta: %w", i, err)
		}
		canonicalMeta, err := tx.CanonicalizeSerializedMetadata(decodedMeta)
		if err != nil {
			return nil, nil, fmt.Errorf("expected transaction %d metadata: %w", i, err)
		}
		canonicalMetaBlob, err := binarycodec.EncodeBytes(canonicalMeta)
		if err != nil {
			return nil, nil, fmt.Errorf("expected transaction %d canonical metadata: %w", i, err)
		}
		if !bytes.Equal(canonicalMetaBlob, metaBlob) {
			return nil, nil, fmt.Errorf("expected transaction %d meta is not canonically serialized", i)
		}
		metadataIndex, ok := tx.TransactionIndexFromMetadata(metaBlob)
		if !ok || metadataIndex != uint32(i) {
			return nil, nil, fmt.Errorf("expected transaction %d metadata TransactionIndex is %d (present=%t)", i, metadataIndex, ok)
		}
		transactions[i] = blockTransaction{Index: i, Hash: hash, Blob: blob, Transaction: parsed.Transaction}
		want[i] = expectedTransaction{Index: i, Hash: hash, MetaBlob: metaBlob}
	}
	return transactions, want, nil
}

func parseHash(name, value string) ([32]byte, error) {
	hash, err := protocol.Hash256FromHex(value)
	if err != nil {
		return [32]byte{}, fmt.Errorf("%s: %w", name, err)
	}
	return hash, nil
}

func errorIf(condition bool, message string) error {
	if condition {
		return errors.New(message)
	}
	return nil
}

func loadStrictJSON(path string, value any, required ...string) (map[string]json.RawMessage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := validateJSONValue(json.NewDecoder(bytes.NewReader(data))); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("trailing JSON: %w", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil, err
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return nil, fmt.Errorf("missing required field %q", name)
		}
	}
	return fields, nil
}

func validateJSONValue(decoder *json.Decoder) error {
	decoder.UseNumber()
	if err := validateNextJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func validateNextJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("null is not allowed")
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("object key has type %T", keyToken)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate object key %q", key)
			}
			seen[key] = struct{}{}
			if err := validateNextJSONValue(decoder); err != nil {
				return fmt.Errorf("key %q: %w", key, err)
			}
		}
	case '[':
		for i := 0; decoder.More(); i++ {
			if err := validateNextJSONValue(decoder); err != nil {
				return fmt.Errorf("element %d: %w", i, err)
			}
		}
	default:
		return fmt.Errorf("unexpected delimiter %q", delim)
	}
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	want := json.Delim('}')
	if delim == '[' {
		want = ']'
	}
	if closing != want {
		return fmt.Errorf("closing delimiter %v, want %v", closing, want)
	}
	return nil
}

func requireNestedFields(raw json.RawMessage, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	return nil
}

func buildRulesFromAmendments(amendments []string) (*amendment.Rules, error) {
	builder := amendment.NewRulesBuilder()
	for _, id := range amendment.PermanentlyEnabledIDs() {
		builder.Enable(id)
	}
	seen := make(map[[32]byte]struct{}, len(amendments))
	for i, value := range amendments {
		var id [32]byte
		if feature := amendment.FeatureByName(value); feature != nil {
			id = feature.ID
		} else {
			if len(value) != 64 {
				return nil, fmt.Errorf("amendment %d %q is neither a registered name nor a 64-character ID", i, value)
			}
			decoded, err := hex.DecodeString(value)
			if err != nil {
				return nil, fmt.Errorf("amendment %d ID: %w", i, err)
			}
			copy(id[:], decoded)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("amendment %d duplicates %x", i, id)
		}
		seen[id] = struct{}{}
		builder.Enable(id)
	}
	return builder.Build(), nil
}

func sameRuleSet(left, right *amendment.Rules) bool {
	if left.EnabledCount() != right.EnabledCount() {
		return false
	}
	for _, id := range left.EnabledIDs() {
		if !right.Enabled(id) {
			return false
		}
	}
	return true
}

func feesFromStateMap(stateMap *shamap.SHAMap) (drops.Fees, error) {
	item, found, err := stateMap.Get(keylet.Fees().Key)
	if err != nil {
		return drops.Fees{}, fmt.Errorf("reading FeeSettings: %w", err)
	}
	if !found || item == nil {
		return drops.DefaultFees(), nil
	}
	settings, err := ledgerstate.ParseFeeSettings(item.Data())
	if err != nil {
		return drops.Fees{}, fmt.Errorf("parsing FeeSettings: %w", err)
	}
	return settings.Fees(), nil
}
