package replaytool

import (
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/keylet"
	ledgerentry "github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/offer_rate_large_xrp.json
var largeOfferRateFixture []byte

// The fixture contains relevant parent entries, so this checks metadata and
// affected state bytes without asserting the complete historical AccountHash.
func TestIssue2003HistoricalOfferRateMatchesPeer(t *testing.T) {
	all.RegisterAll()
	var fixture struct {
		Parent struct {
			LedgerIndex  uint32              `json:"ledger_index"`
			LedgerHash   string              `json:"ledger_hash"`
			CloseTime    uint32              `json:"close_time"`
			TotalCoins   uint64              `json:"total_coins,string"`
			StateEntries []fixtureStateEntry `json:"state_entries"`
		} `json:"parent"`
		Target struct {
			LedgerIndex         uint32              `json:"ledger_index"`
			ParentHash          string              `json:"parent_hash"`
			CloseTime           uint32              `json:"close_time"`
			CloseTimeResolution uint8               `json:"close_time_resolution"`
			PostStateEntries    []fixtureStateEntry `json:"post_state_entries"`
		} `json:"target"`
		Transaction struct {
			Hash             string `json:"hash"`
			Blob             string `json:"blob"`
			TransactionIndex uint32 `json:"transaction_index"`
			PeerResult       string `json:"peer_result"`
			PeerMetadata     string `json:"peer_metadata"`
		} `json:"transaction"`
	}
	require.NoError(t, json.Unmarshal(largeOfferRateFixture, &fixture))
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
	require.True(t, rules.Enabled(amendment.FeatureFixUniversalNumber))
	require.True(t, rules.Enabled(amendment.FeatureFixCleanup3_3_0))
	fees, err := feesFromStateMap(stateMap)
	require.NoError(t, err)
	parentHash, err := protocol.Hash256FromHex(fixture.Parent.LedgerHash)
	require.NoError(t, err)
	require.Equal(t, fixture.Parent.LedgerHash, fixture.Target.ParentHash)
	require.Equal(t, fixture.Parent.LedgerIndex+1, fixture.Target.LedgerIndex)
	view, err := ledger.NewOpenWithHeader(header.LedgerHeader{
		LedgerIndex:         fixture.Target.LedgerIndex,
		ParentHash:          parentHash,
		ParentCloseTime:     protocol.FromRippleTime(fixture.Parent.CloseTime),
		CloseTime:           protocol.FromRippleTime(fixture.Target.CloseTime),
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
		ApplicationCloseTime:    fixture.Target.CloseTime,
		ApplicationCloseTimeSet: true,
		NetworkID:               1,
		Standalone:              true,
		Rules:                   rules,
	})
	engine.SetBaseTxCount(fixture.Transaction.TransactionIndex)
	blob, err := hex.DecodeString(fixture.Transaction.Blob)
	require.NoError(t, err)
	txn, err := tx.ParseFromBinary(blob)
	require.NoError(t, err)
	require.NoError(t, sign.VerifySignature(txn, true))
	wantHash, err := protocol.Hash256FromHex(fixture.Transaction.Hash)
	require.NoError(t, err)
	result, err := txengine.NewBlockProcessor(engine).ApplyLedgerTransaction(txn, blob)
	require.NoError(t, err)
	require.Equal(t, wantHash, result.Hash)
	require.True(t, result.ApplyResult.Applied)
	require.Equal(t, fixture.Transaction.PeerResult, result.ApplyResult.Result.String())
	metadata, err := tx.SerializeMetadata(result.ApplyResult.Metadata)
	require.NoError(t, err)
	wantMetadata, err := hex.DecodeString(fixture.Transaction.PeerMetadata)
	require.NoError(t, err)
	require.Equal(t, wantMetadata, metadata)

	require.Len(t, fixture.Target.PostStateEntries, 4)
	for _, entry := range fixture.Target.PostStateEntries {
		key, err := protocol.Hash256FromHex(entry.Index)
		require.NoError(t, err)
		wantData, err := hex.DecodeString(entry.Data)
		require.NoError(t, err)
		data, err := view.Read(keylet.Keylet{Type: ledgerentry.TypeAny, Key: key})
		require.NoError(t, err)
		require.Equal(t, wantData, data, "post-state %s", entry.Index)
	}
}
