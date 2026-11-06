package offer

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

func TestOfferCancelTestnet21981990(t *testing.T) {
	all.RegisterAll()
	data, err := os.ReadFile("testdata/cancel_testnet21981990.json")
	require.NoError(t, err)
	var fixture struct {
		ParentHash          string `json:"parent_hash"`
		ParentCloseTime     uint32 `json:"parent_close_time"`
		LedgerIndex         uint32 `json:"ledger_index"`
		TotalCoins          uint64 `json:"total_coins"`
		TransactionHash     string `json:"transaction_hash"`
		TargetTotalCoins    uint64 `json:"target_total_coins"`
		CloseTime           uint32 `json:"target_close_time"`
		CloseTimeResolution uint8  `json:"target_close_time_resolution"`
		Transactions        []struct {
			Index         uint32 `json:"index"`
			Hash          string `json:"hash"`
			Blob          string `json:"tx_blob"`
			Metadata      string `json:"meta_blob"`
			ParentBatchID string `json:"parent_batch_id"`
		} `json:"transactions"`
		Entries []struct {
			Index string `json:"index"`
			Data  string `json:"data"`
		} `json:"entries"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	decode := func(value string) []byte {
		t.Helper()
		decoded, err := hex.DecodeString(value)
		require.NoError(t, err)
		return decoded
	}
	stateMap := shamap.New(shamap.TypeState)
	for _, entry := range fixture.Entries {
		index, err := protocol.Hash256FromHex(entry.Index)
		require.NoError(t, err)
		require.NoError(t, stateMap.Put(index, decode(entry.Data)))
	}
	feeEntry, found, err := stateMap.Get(keylet.Fees().Key)
	require.NoError(t, err)
	require.True(t, found)
	feeSettings, err := state.ParseFeeSettings(feeEntry.Data())
	require.NoError(t, err)
	fees := feeSettings.Fees()
	parentHash, err := protocol.Hash256FromHex(fixture.ParentHash)
	require.NoError(t, err)
	closeTime := ledger.ApplicationViewCloseTime(protocol.FromRippleTime(fixture.ParentCloseTime), protocol.FromRippleTime(fixture.CloseTime), uint32(fixture.CloseTimeResolution))
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{
		CloseTime:           closeTime,
		CloseTimeResolution: fixture.CloseTimeResolution,
		LedgerIndex:         fixture.LedgerIndex,
		ParentHash:          parentHash,
		ParentCloseTime:     protocol.FromRippleTime(fixture.ParentCloseTime),
		Drops:               fixture.TotalCoins,
	}, stateMap, shamap.New(shamap.TypeTransaction), fees)
	require.NoError(t, err)
	engine := txengine.NewEngine(view, tx.EngineConfig{
		BaseFee:                 uint64(fees.Base),
		ReserveBase:             uint64(fees.Reserve),
		ReserveIncrement:        uint64(fees.Increment),
		LedgerSequence:          fixture.LedgerIndex,
		ParentHash:              parentHash,
		ParentCloseTime:         fixture.ParentCloseTime,
		ApplicationCloseTime:    protocol.ToRippleTime(closeTime),
		ApplicationCloseTimeSet: true,
		NetworkID:               1,
		Rules:                   view.Rules(),
	})
	processor := txengine.NewBlockProcessor(engine)
	require.Len(t, fixture.Transactions, 56)
	var totalFees uint64
	var outerCount int
	for index, transaction := range fixture.Transactions {
		if !t.Run(fmt.Sprintf("transaction_%d", index), func(t *testing.T) {
			require.Equal(t, uint32(index), transaction.Index)
			blob := decode(transaction.Blob)
			expectedHash, err := protocol.Hash256FromHex(transaction.Hash)
			require.NoError(t, err)
			if transaction.ParentBatchID == "" {
				prepared, err := txengine.ParseAndPrepare(blob)
				require.NoError(t, err)
				result, err := processor.ApplyLedgerTransaction(prepared.Transaction, blob)
				require.NoError(t, err)
				require.Equal(t, expectedHash, result.Hash)
				require.True(t, result.ApplyResult.Applied, "%s: %s", result.ApplyResult.Result, result.ApplyResult.Message)
				totalFees += result.ApplyResult.Fee
				outerCount++
			}
			leaf, found, err := view.GetTransaction(expectedHash)
			require.NoError(t, err)
			require.True(t, found)
			actualTx, metadata, err := tx.SplitTxWithMetaBlobStrict(leaf)
			require.NoError(t, err)
			require.Equal(t, blob, actualTx)
			require.Equal(t, decode(transaction.Metadata), metadata)
		}) {
			return
		}
	}
	require.Equal(t, 48, outerCount)
	root, err := view.TxMapHash()
	require.NoError(t, err)
	expectedRoot, err := protocol.Hash256FromHex(fixture.TransactionHash)
	require.NoError(t, err)
	require.Equal(t, expectedRoot, root)
	require.Equal(t, fixture.TargetTotalCoins, fixture.TotalCoins-totalFees)
}
