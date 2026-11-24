package replaytool

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

// historicalLedgerFixture is a validated ledger's transactions with their
// canonical results and metadata, plus the subset of parent state entries they
// touch. It carries no complete parent SHAMap, so replaying it proves
// transaction and metadata parity without asserting the ledger AccountHash.
type historicalLedgerFixture struct {
	Parent struct {
		LedgerIndex  uint32              `json:"ledger_index"`
		LedgerHash   string              `json:"ledger_hash"`
		CloseTime    uint32              `json:"close_time"`
		TotalCoins   uint64              `json:"total_coins,string"`
		StateEntries []fixtureStateEntry `json:"state_entries"`
	} `json:"parent"`
	Target struct {
		LedgerIndex         uint32 `json:"ledger_index"`
		ParentHash          string `json:"parent_hash"`
		CloseTime           uint32 `json:"close_time"`
		CloseTimeResolution uint8  `json:"close_time_resolution"`
	} `json:"target"`
	Transactions []historicalLedgerTransaction `json:"transactions"`
}

type historicalLedgerTransaction struct {
	Hash             string `json:"hash"`
	TransactionType  string `json:"transaction_type"`
	TransactionIndex uint32 `json:"transaction_index"`
	Blob             string `json:"blob"`
	PeerResult       string `json:"peer_result"`
	PeerMetadata     string `json:"peer_metadata"`
}

func parseHistoricalLedgerFixture(t *testing.T, data []byte) historicalLedgerFixture {
	t.Helper()
	var fixture historicalLedgerFixture
	require.NoError(t, json.Unmarshal(data, &fixture))
	return fixture
}

func replayHistoricalLedgerFixture(t *testing.T, fixture historicalLedgerFixture) *amendment.Rules {
	t.Helper()
	all.RegisterAll()

	stateMap := shamap.New(shamap.TypeState)
	for _, entry := range fixture.Parent.StateEntries {
		key, err := protocol.Hash256FromHex(entry.Index)
		require.NoError(t, err)
		data, err := hex.DecodeString(entry.Data)
		require.NoError(t, err)
		require.NoError(t, stateMap.Put(key, data))
	}
	rules, err := loadRulesFromState(stateMap)
	require.NoError(t, err)
	fees, err := feesFromStateMap(context.Background(), stateMap, drops.DefaultFees())
	require.NoError(t, err)

	parentHash, err := protocol.Hash256FromHex(fixture.Parent.LedgerHash)
	require.NoError(t, err)
	require.Equal(t, fixture.Parent.LedgerHash, fixture.Target.ParentHash)
	require.Equal(t, fixture.Parent.LedgerIndex+1, fixture.Target.LedgerIndex)

	parentCloseTime := protocol.FromRippleTime(fixture.Parent.CloseTime)
	closeTime := ledger.ApplicationViewCloseTime(
		parentCloseTime,
		protocol.FromRippleTime(fixture.Target.CloseTime),
		uint32(fixture.Target.CloseTimeResolution),
	)
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{
		LedgerIndex:         fixture.Target.LedgerIndex,
		ParentHash:          parentHash,
		ParentCloseTime:     parentCloseTime,
		CloseTime:           closeTime,
		CloseTimeResolution: fixture.Target.CloseTimeResolution,
		Drops:               fixture.Parent.TotalCoins,
	}, stateMap, shamap.New(shamap.TypeTransaction), fees)
	require.NoError(t, err)
	engine := txengine.NewEngine(view, tx.EngineConfig{
		BaseFee:                 uint64(fees.Base),
		ReserveBase:             uint64(fees.Reserve),
		ReserveIncrement:        uint64(fees.Increment),
		LedgerSequence:          fixture.Target.LedgerIndex,
		ParentHash:              parentHash,
		ParentCloseTime:         fixture.Parent.CloseTime,
		ApplicationCloseTime:    protocol.ToRippleTime(closeTime),
		ApplicationCloseTimeSet: true,
		NetworkID:               1,
		Standalone:              true,
		Rules:                   rules,
	})
	engine.SetBaseTxCount(fixture.Transactions[0].TransactionIndex)
	processor := txengine.NewBlockProcessor(engine)

	var pendingInners []tx.AppliedInnerTransaction
	for _, record := range fixture.Transactions {
		blob, err := hex.DecodeString(record.Blob)
		require.NoError(t, err)
		txn, err := tx.ParseFromBinary(blob)
		require.NoError(t, err)
		require.Equal(t, record.TransactionType, txn.GetCommon().TransactionType)
		wantHash, err := protocol.Hash256FromHex(record.Hash)
		require.NoError(t, err)

		var (
			gotHash  [32]byte
			result   string
			metadata *tx.Metadata
		)
		if txn.GetCommon().GetFlags()&tx.TfInnerBatchTxn != 0 {
			require.NotEmpty(t, pendingInners, "tx %d: unexpected batch inner", record.TransactionIndex)
			inner := pendingInners[0]
			pendingInners = pendingInners[1:]
			innerBlob, err := tx.SerializeTransaction(inner.Transaction)
			require.NoError(t, err)
			require.Equal(t, blob, innerBlob, "tx %d: inner blob", record.TransactionIndex)
			gotHash, err = tx.ComputeTransactionHash(inner.Transaction)
			require.NoError(t, err)
			require.NotNil(t, inner.Metadata)
			require.NotNil(t, inner.Metadata.ParentBatchID)
			result = inner.Metadata.TransactionResult.String()
			metadata = inner.Metadata
		} else {
			require.Empty(t, pendingInners, "tx %d: outer applied before prior batch inners", record.TransactionIndex)
			require.NoError(t, sign.VerifySignature(txn, true))
			applied, err := processor.ApplyLedgerTransaction(txn, blob)
			require.NoError(t, err)
			require.True(t, applied.ApplyResult.Applied, "tx %d: %s", record.TransactionIndex, applied.ApplyResult.Result)
			gotHash = applied.Hash
			result = applied.ApplyResult.Result.String()
			metadata = applied.ApplyResult.Metadata
			pendingInners = applied.ApplyResult.AppliedInnerTransactions
		}

		require.Equal(t, wantHash, gotHash, "tx %d: hash", record.TransactionIndex)
		require.NotNil(t, metadata)
		require.Equal(t, record.TransactionIndex, metadata.TransactionIndex)
		gotMetadata, err := tx.SerializeMetadata(metadata)
		require.NoError(t, err)
		wantMetadata, err := hex.DecodeString(record.PeerMetadata)
		require.NoError(t, err)
		t.Logf("tx %d %s %s result=%s engine metadata=%d bytes peer metadata=%d bytes equal=%t",
			record.TransactionIndex, record.TransactionType, record.Hash, result,
			len(gotMetadata), len(wantMetadata), bytes.Equal(wantMetadata, gotMetadata))
		require.Equal(t, record.PeerResult, result, "tx %d: result", record.TransactionIndex)
		require.True(t, bytes.Equal(wantMetadata, gotMetadata),
			"tx %d %s: engine metadata differs from canonical peer metadata: engine=%d bytes peer=%d bytes\nengine=%X\npeer=%X",
			record.TransactionIndex, record.Hash, len(gotMetadata), len(wantMetadata), gotMetadata, wantMetadata)
	}
	require.Empty(t, pendingInners)
	return rules
}
