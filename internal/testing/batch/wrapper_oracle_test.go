package batch

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	ledgerheader "github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/openledger"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/txq"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const batchOracleCommit = "d147fccf54a500fce586522f28d6044c37fd8d29"

var batchOracleWrappers = []string{
	"RawTransaction", "CreatedNode", "ModifiedNode", "DeletedNode", "TemplateEntry",
	"EmitDetails", "Memo", "FinalFields", "NewFields", "PreviousFields", "TransactionMetaData",
}

type batchOracleFixture struct {
	OracleCommit string            `json:"oracle_commit"`
	Wrapper      string            `json:"wrapper"`
	Enabled      bool              `json:"enabled"`
	TxBlob       string            `json:"tx_blob"`
	Submit       batchOracleSubmit `json:"submit"`
	Parent       batchOracleLedger `json:"parent"`
	Closed       batchOracleLedger `json:"closed"`
}

type batchOracleSubmit struct {
	EngineResult string `json:"engine_result"`
	Applied      bool   `json:"applied"`
}

type batchOracleLedger struct {
	Header       string                   `json:"header"`
	Rules        []string                 `json:"rules"`
	Fees         batchOracleFees          `json:"fees"`
	State        []batchOracleEntry       `json:"state"`
	Transactions []batchOracleTransaction `json:"transactions"`
}

type batchOracleFees struct {
	Base      string `json:"base"`
	Reserve   string `json:"reserve"`
	Increment string `json:"increment"`
}

type batchOracleEntry struct {
	Index string `json:"index"`
	Data  string `json:"data"`
}

type batchOracleTransaction struct {
	Hash     string `json:"hash"`
	TxBlob   string `json:"tx_blob"`
	MetaBlob string `json:"meta_blob"`
}

type loadedBatchOracleLedger struct {
	*ledger.Ledger
	Header         ledgerheader.LedgerHeader
	EffectiveRules *amendment.Rules
	Fees           drops.Fees
	State          *shamap.SHAMap
	Txs            *shamap.SHAMap
}

func TestBatchWrapperOracleFixtures(t *testing.T) {
	all.RegisterAll()
	dir := os.Getenv("GOXRPL_BATCH_ORACLE_DIR")
	if dir == "" {
		dir = filepath.Join("testdata", "wrapper-oracle")
	}
	paths, err := filepath.Glob(filepath.Join(dir, "*.json"))
	require.NoError(t, err)
	require.NotEmpty(t, paths, "no oracle fixtures found in %s", dir)
	sort.Strings(paths)

	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		t.Run(filepath.Base(path), func(t *testing.T) {
			fixture := loadBatchOracleFixture(t, path)
			require.Equal(t, batchOracleCommit, strings.ToLower(fixture.OracleCommit))
			require.Contains(t, batchOracleWrappers, fixture.Wrapper)
			key := fmt.Sprintf("%s/%t", fixture.Wrapper, fixture.Enabled)
			require.NotContains(t, seen, key)
			seen[key] = struct{}{}
			runBatchOracleFixture(t, fixture)
		})
	}

	for _, wrapper := range batchOracleWrappers {
		for _, enabled := range []bool{false, true} {
			key := fmt.Sprintf("%s/%t", wrapper, enabled)
			require.Contains(t, seen, key, "missing oracle fixture")
		}
	}
}

func loadBatchOracleFixture(t *testing.T, path string) batchOracleFixture {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var fixture batchOracleFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	require.NotEmpty(t, fixture.TxBlob)
	require.NotEmpty(t, fixture.Parent.Header)
	require.NotEmpty(t, fixture.Closed.Header)
	return fixture
}

func runBatchOracleFixture(t *testing.T, fixture batchOracleFixture) {
	t.Helper()
	parent := loadBatchOracleLedger(t, fixture.Parent)
	closed := loadBatchOracleLedger(t, fixture.Closed)
	require.Equal(t, parent.Header.Hash, closed.Header.ParentHash)
	require.Equal(t, parent.Header.LedgerIndex+1, closed.Header.LedgerIndex)
	require.Equal(t, parent.Header.CloseTime, closed.Header.ParentCloseTime)
	require.ElementsMatch(t, parent.EffectiveRules.EnabledIDs(), closed.EffectiveRules.EnabledIDs())
	require.True(t, parent.EffectiveRules.Enabled(amendment.FeatureBatchV1_1))
	require.Equal(t, fixture.Enabled, parent.EffectiveRules.Enabled(amendment.FeatureFixBatchV1_2))

	txBlob := decodeBatchOracleBytes(t, "tx_blob", fixture.TxBlob)
	parsed, err := tx.ParseFromBinary(txBlob)
	require.NoError(t, err)
	require.Equal(t, txBlob, parsed.GetRawBytes())
	batch, ok := parsed.(*batchtx.Batch)
	require.True(t, ok, "fixture transaction is not a Batch")
	require.NotEmpty(t, batch.RawTransactions)
	for _, raw := range batch.RawTransactions {
		require.Equal(t, fixture.Wrapper, raw.Wrapper)
	}
	pending, err := openledger.ParsePendingTx(txBlob)
	require.NoError(t, err)
	require.Equal(t, txBlob, pending.Blob)

	beforeState, err := parent.StateMapHash()
	require.NoError(t, err)
	view, err := openledger.New(parent.Ledger, openledger.Config{Rules: parent.EffectiveRules})
	require.NoError(t, err)
	beforeTxs, err := view.Current().TxMapHash()
	require.NoError(t, err)
	apply := oracleApplyConfig(parent.Ledger, parent.EffectiveRules)
	queue, err := txq.New(txq.DefaultConfig())
	require.NoError(t, err)
	out := view.SubmitDetailed(pending, apply, queue)
	require.Equal(t, fixture.Submit.EngineResult, out.Result.String())
	require.Equal(t, fixture.Submit.Applied, out.Applied)
	require.GreaterOrEqual(t, parent.Header.Drops, closed.Header.Drops)
	require.Equal(t, parent.Header.Drops-closed.Header.Drops, out.Fee)

	if !fixture.Submit.Applied {
		require.False(t, out.Changed)
		require.False(t, out.Queued)
		afterState, hashErr := view.Current().StateMapHash()
		require.NoError(t, hashErr)
		afterTxs, hashErr := view.Current().TxMapHash()
		require.NoError(t, hashErr)
		require.Equal(t, beforeState, afterState)
		require.Equal(t, beforeTxs, afterTxs)
		require.Zero(t, out.Fee)
		require.Nil(t, out.Metadata)
	}

	var pendingTxs []openledger.PendingTx
	if out.Applied {
		pendingTxs = []openledger.PendingTx{pending}
	}
	built, err := openledger.BuildClosedLedger(parent.Ledger, pendingTxs, openledger.BuildConfig{
		CloseTime:  closed.Header.CloseTime,
		CloseFlags: closed.Header.CloseFlags,
		Apply:      apply,
	})
	require.NoError(t, err)
	assertBatchOracleLedger(t, built.Ledger, closed)
}

func oracleApplyConfig(parent *ledger.Ledger, rules *amendment.Rules) openledger.ApplyConfig {
	fees := parent.Fees()
	return openledger.ApplyConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            parent.Header().LedgerIndex + 1,
		ParentCloseTime:           protocol.ToRippleTime(parent.CloseTime()),
		SkipSignatureVerification: false,
		Rules:                     rules,
	}
}

func loadBatchOracleLedger(t *testing.T, snapshot batchOracleLedger) loadedBatchOracleLedger {
	t.Helper()
	hdrBytes := decodeBatchOracleBytes(t, "header", snapshot.Header)
	require.Len(t, hdrBytes, ledgerheader.SizeWithHash)
	hdr, err := ledgerheader.DeserializeHeader(hdrBytes, true)
	require.NoError(t, err)
	require.Equal(t, hdr.Hash, ledgerheader.CalculateHash(*hdr))
	hdr.Validated = true

	state := shamap.New(shamap.TypeState)
	seenState := make(map[[32]byte]struct{}, len(snapshot.State))
	for _, entry := range snapshot.State {
		index := decodeBatchOracleHash(t, "state index", entry.Index)
		_, duplicate := seenState[index]
		require.False(t, duplicate, "duplicate state index %x", index)
		seenState[index] = struct{}{}
		data := decodeBatchOracleBytes(t, "state data", entry.Data)
		require.NoError(t, state.Put(index, data))
	}

	txs := shamap.New(shamap.TypeTransaction)
	seenTxs := make(map[[32]byte]struct{}, len(snapshot.Transactions))
	for _, entry := range snapshot.Transactions {
		hash := decodeBatchOracleHash(t, "transaction hash", entry.Hash)
		_, duplicate := seenTxs[hash]
		require.False(t, duplicate, "duplicate transaction hash %x", hash)
		seenTxs[hash] = struct{}{}
		txBlob := decodeBatchOracleBytes(t, "transaction blob", entry.TxBlob)
		metaBlob := decodeBatchOracleBytes(t, "metadata blob", entry.MetaBlob)
		parsed, parseErr := tx.ParseFromBinary(txBlob)
		require.NoError(t, parseErr)
		computed, hashErr := tx.ComputeTransactionHash(parsed)
		require.NoError(t, hashErr)
		require.Equal(t, hash, computed)
		_, hasIndex := tx.TransactionIndexFromMetadata(metaBlob)
		require.True(t, hasIndex)
		leaf := batchOracleTxLeaf(t, txBlob, metaBlob)
		require.NoError(t, txs.PutWithNodeType(hash, leaf, shamap.NodeTypeTransactionWithMeta))
	}

	stateHash, err := state.Hash()
	require.NoError(t, err)
	require.Equal(t, hdr.AccountHash, stateHash)
	txHash, err := txs.Hash()
	require.NoError(t, err)
	require.Equal(t, hdr.TxHash, txHash)
	require.NotEmpty(t, snapshot.Rules)
	ids := make([][32]byte, len(snapshot.Rules))
	for i, id := range snapshot.Rules {
		ids[i] = decodeBatchOracleHash(t, "amendment ID", id)
	}
	rules := amendment.NewRules(ids)
	require.Equal(t, len(ids), rules.EnabledCount(), "duplicate amendment IDs")
	fees := parseBatchOracleFees(t, snapshot.Fees)
	l, err := ledger.NewFromHeader(*hdr, state, txs, fees)
	require.NoError(t, err)
	for _, id := range l.Rules().EnabledIDs() {
		require.True(t, rules.Enabled(id), "ledger amendment missing from effective rules: %x", id)
	}
	return loadedBatchOracleLedger{Ledger: l, Header: *hdr, EffectiveRules: rules, Fees: fees, State: state, Txs: txs}
}

func assertBatchOracleLedger(t *testing.T, got *ledger.Ledger, want loadedBatchOracleLedger) {
	t.Helper()
	gotHeader := got.Header()
	require.Equal(t, ledgerheader.AddRaw(want.Header, true), ledgerheader.AddRaw(gotHeader, true))
	require.Equal(t, want.Header.Hash, got.Hash())
	require.Equal(t, want.Header.AccountHash, gotHeader.AccountHash)
	require.Equal(t, want.Header.TxHash, gotHeader.TxHash)
	require.Equal(t, want.Header.Drops, got.TotalDrops())
	require.Equal(t, want.Fees, got.Fees())
	stateRoot, err := got.StateMapHash()
	require.NoError(t, err)
	require.Equal(t, want.Header.AccountHash, stateRoot)
	txRoot, err := got.TxMapHash()
	require.NoError(t, err)
	require.Equal(t, want.Header.TxHash, txRoot)
	gotState, err := got.StateMapSnapshot()
	require.NoError(t, err)
	gotTxs, err := got.TxMapSnapshot()
	require.NoError(t, err)
	assertBatchOracleMap(t, want.State, gotState)
	assertBatchOracleMap(t, want.Txs, gotTxs)
}

func assertBatchOracleMap(t *testing.T, want, got *shamap.SHAMap) {
	t.Helper()
	wantCount, gotCount := 0, 0
	require.NoError(t, want.ForEach(func(item *shamap.Item) bool {
		wantCount++
		actual, found, err := got.Get(item.Key())
		require.NoError(t, err)
		require.True(t, found, "missing map item %x", item.Key())
		require.True(t, bytes.Equal(item.Data(), actual.Data()), "map item %x differs", item.Key())
		return true
	}))
	require.NoError(t, got.ForEach(func(*shamap.Item) bool {
		gotCount++
		return true
	}))
	require.Equal(t, wantCount, gotCount)
}

func batchOracleTxLeaf(t *testing.T, txBlob, metaBlob []byte) []byte {
	t.Helper()
	txPart, err := tx.EncodeWithVL(txBlob)
	require.NoError(t, err)
	metaPart, err := tx.EncodeWithVL(metaBlob)
	require.NoError(t, err)
	return append(txPart, metaPart...)
}

func parseBatchOracleFees(t *testing.T, fees batchOracleFees) drops.Fees {
	t.Helper()
	return drops.Fees{
		Base:      drops.XRPAmount(parseBatchOracleUint(t, fees.Base)),
		Reserve:   drops.XRPAmount(parseBatchOracleUint(t, fees.Reserve)),
		Increment: drops.XRPAmount(parseBatchOracleUint(t, fees.Increment)),
	}
}

func parseBatchOracleUint(t *testing.T, value string) uint64 {
	t.Helper()
	parsed, err := strconv.ParseUint(value, 10, 64)
	require.NoError(t, err)
	return parsed
}

func decodeBatchOracleBytes(t *testing.T, name, value string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(value)
	require.NoError(t, err, "%s", name)
	return decoded
}

func decodeBatchOracleHash(t *testing.T, name, value string) [32]byte {
	t.Helper()
	decoded := decodeBatchOracleBytes(t, name, value)
	require.Len(t, decoded, 32, "%s", name)
	var hash [32]byte
	copy(hash[:], decoded)
	return hash
}
