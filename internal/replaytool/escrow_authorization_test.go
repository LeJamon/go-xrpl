package replaytool

import (
	"bytes"
	"embed"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/escrow"
	"github.com/LeJamon/go-xrpl/internal/tx/sign"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/require"
)

//go:embed testdata/mpt_escrow_no_auth.json
var issue1993Fixture embed.FS

type mptEscrowReplayFixture struct {
	Parent      mptEscrowParent      `json:"parent"`
	Target      mptEscrowTarget      `json:"target"`
	Transaction mptEscrowTransaction `json:"transaction"`
}

type mptEscrowStateEntry struct {
	Index string `json:"index"`
	Data  string `json:"data"`
}

type mptEscrowParent struct {
	LedgerIndex         uint32                `json:"ledger_index"`
	LedgerHash          string                `json:"ledger_hash"`
	AccountHash         string                `json:"account_hash"`
	ParentHash          string                `json:"parent_hash"`
	CloseTime           uint32                `json:"close_time"`
	CloseTimeResolution uint8                 `json:"close_time_resolution"`
	TotalCoins          uint64                `json:"total_coins,string"`
	StateEntries        []mptEscrowStateEntry `json:"state_entries"`
}

type mptEscrowTarget struct {
	LedgerIndex         uint32 `json:"ledger_index"`
	LedgerHash          string `json:"ledger_hash"`
	AccountHash         string `json:"account_hash"`
	ParentHash          string `json:"parent_hash"`
	CloseTime           uint32 `json:"close_time"`
	CloseTimeResolution uint8  `json:"close_time_resolution"`
}

type mptEscrowTransaction struct {
	Hash             string `json:"hash"`
	Blob             string `json:"blob"`
	TransactionIndex uint32 `json:"transaction_index"`
	PeerResult       string `json:"peer_result"`
	PeerMetadata     string `json:"peer_metadata"`
	Account          string `json:"account"`
	Destination      string `json:"destination"`
	MPTIssuanceID    string `json:"mpt_issuance_id"`
	Amount           string `json:"amount"`
	Sequence         uint32 `json:"sequence"`
	Fee              string `json:"fee"`
	FinishAfter      uint32 `json:"finish_after"`
}

// The fixture carries the relevant parent entries rather than the complete
// parent SHAMap, so this test proves transaction and metadata parity without
// asserting the historical ledger AccountHash.
func TestIssue1993HistoricalMPTEscrowMatchesPeerAuthorization(t *testing.T) {
	all.RegisterAll()

	fixtureBytes, err := issue1993Fixture.ReadFile("testdata/mpt_escrow_no_auth.json")
	require.NoError(t, err)
	var fixture mptEscrowReplayFixture
	require.NoError(t, json.Unmarshal(fixtureBytes, &fixture))

	stateMap := shamap.New(shamap.TypeState)
	for _, entry := range fixture.Parent.StateEntries {
		key, decodeErr := protocol.Hash256FromHex(entry.Index)
		require.NoError(t, decodeErr)
		data, decodeErr := hex.DecodeString(entry.Data)
		require.NoError(t, decodeErr)
		require.NoError(t, stateMap.Put(key, data))
	}

	rules, err := loadRulesFromState(stateMap)
	require.NoError(t, err)
	require.True(t, rules.Enabled(amendment.FeatureMPTokensV1))
	require.True(t, rules.Enabled(amendment.FeatureFixCleanup3_3_0))
	fees, err := feesFromStateMap(stateMap)
	require.NoError(t, err)
	require.Equal(t, uint64(10), uint64(fees.Base))
	require.Equal(t, uint64(1_000_000), uint64(fees.Reserve))
	require.Equal(t, uint64(200_000), uint64(fees.Increment))

	parentHash, err := protocol.Hash256FromHex(fixture.Parent.LedgerHash)
	require.NoError(t, err)
	parentHashFromTarget, err := protocol.Hash256FromHex(fixture.Target.ParentHash)
	require.NoError(t, err)
	require.Equal(t, parentHash, parentHashFromTarget)

	view, err := ledger.NewOpenWithHeader(
		header.LedgerHeader{
			LedgerIndex:         fixture.Target.LedgerIndex,
			ParentHash:          parentHash,
			ParentCloseTime:     protocol.FromRippleTime(fixture.Parent.CloseTime),
			CloseTime:           protocol.FromRippleTime(fixture.Target.CloseTime),
			CloseTimeResolution: fixture.Target.CloseTimeResolution,
			Drops:               fixture.Parent.TotalCoins,
		},
		stateMap,
		shamap.New(shamap.TypeTransaction),
		fees,
	)
	require.NoError(t, err)

	txBlob, err := hex.DecodeString(fixture.Transaction.Blob)
	require.NoError(t, err)
	require.Len(t, txBlob, 213)
	txn, err := tx.ParseFromBinary(txBlob)
	require.NoError(t, err)
	require.Equal(t, txBlob, txn.GetRawBytes())

	wantHash, err := protocol.Hash256FromHex(fixture.Transaction.Hash)
	require.NoError(t, err)
	gotHash, err := tx.ComputeTransactionHash(txn)
	require.NoError(t, err)
	require.Equal(t, wantHash, gotHash)
	require.NoError(t, sign.VerifySignature(txn, true))

	common := txn.GetCommon()
	require.Equal(t, fixture.Transaction.Account, common.Account)
	require.Equal(t, "EscrowCreate", common.TransactionType)
	require.Equal(t, fixture.Transaction.Sequence, common.GetSequence())
	require.Equal(t, fixture.Transaction.Fee, common.Fee)
	escrowTx, ok := txn.(*escrow.EscrowCreate)
	require.True(t, ok)
	require.Equal(t, fixture.Transaction.Destination, escrowTx.Destination)
	require.Equal(t, fixture.Transaction.MPTIssuanceID, escrowTx.Amount.MPTIssuanceID())
	require.Equal(t, fixture.Transaction.Amount, escrowTx.Amount.Value())
	require.NotNil(t, escrowTx.FinishAfter)
	require.Equal(t, fixture.Transaction.FinishAfter, *escrowTx.FinishAfter)

	engine := txengine.NewEngine(view, tx.EngineConfig{
		BaseFee:                   uint64(fees.Base),
		ReserveBase:               uint64(fees.Reserve),
		ReserveIncrement:          uint64(fees.Increment),
		LedgerSequence:            fixture.Target.LedgerIndex,
		ParentHash:                parentHash,
		ParentCloseTime:           fixture.Parent.CloseTime,
		ApplicationCloseTime:      fixture.Target.CloseTime,
		ApplicationCloseTimeSet:   true,
		SkipSignatureVerification: false,
		NetworkID:                 1,
		Standalone:                true,
		Rules:                     rules,
	})
	engine.SetBaseTxCount(fixture.Transaction.TransactionIndex)
	result, err := txengine.NewBlockProcessor(engine).ApplyLedgerTransaction(txn, txBlob)
	require.NoError(t, err)
	require.Equal(t, wantHash, result.Hash)
	require.True(t, result.ApplyResult.Applied)
	require.NotNil(t, result.ApplyResult.Metadata)
	require.Equal(t, fixture.Transaction.TransactionIndex, result.ApplyResult.Metadata.TransactionIndex)

	peerMetadata, err := hex.DecodeString(fixture.Transaction.PeerMetadata)
	require.NoError(t, err)
	require.Len(t, peerMetadata, 151)
	engineMetadata, err := tx.SerializeMetadata(result.ApplyResult.Metadata)
	require.NoError(t, err)
	t.Logf("engine result=%s engine metadata=%d bytes peer metadata=%d bytes equal=%t",
		result.ApplyResult.Result,
		len(engineMetadata),
		len(peerMetadata),
		bytes.Equal(peerMetadata, engineMetadata))
	require.Equal(t, fixture.Transaction.PeerResult, result.ApplyResult.Result.String())
	require.True(t, bytes.Equal(peerMetadata, engineMetadata),
		"engine metadata differs from canonical peer metadata: engine=%d bytes peer=%d bytes",
		len(engineMetadata), len(peerMetadata))
}
