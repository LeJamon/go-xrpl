package inbound

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/header"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/tx"
	accounttx "github.com/LeJamon/go-xrpl/internal/tx/account"
	"github.com/LeJamon/go-xrpl/internal/tx/all"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	txengine "github.com/LeJamon/go-xrpl/internal/tx/engine"
	"github.com/LeJamon/go-xrpl/internal/tx/ter"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type replayTestLeaf struct {
	txBytes []byte
	meta    *tx.Metadata
	hash    [32]byte
	blob    []byte
}

type ordinaryReplayFixture struct {
	parent *ledger.Ledger
	header header.LedgerHeader
	leaves []replayTestLeaf
	cfg    tx.EngineConfig
}

type batchReplayFixture struct {
	ordinaryReplayFixture
	outer   replayTestLeaf
	inners  []replayTestLeaf
	account string
}

func replayRules() *amendment.Rules {
	return amendment.NewRulesBuilder().
		FromPreset(amendment.PresetAllSupported).
		Enable(amendment.FeatureBatchV1_1).
		Build()
}

func replayConfig(parent *ledger.Ledger, closeTime time.Time, rules *amendment.Rules) tx.EngineConfig {
	return tx.EngineConfig{
		BaseFee:                   10,
		ReserveBase:               200_000_000,
		ReserveIncrement:          50_000_000,
		SkipSignatureVerification: true,
		Rules:                     rules,
		LedgerSequence:            parent.Sequence() + 1,
		ParentCloseTime:           protocol.ToRippleTime(parent.CloseTime()),
		ApplicationCloseTime:      protocol.ToRippleTime(closeTime),
		ApplicationCloseTimeSet:   true,
		ParentHash:                parent.Hash(),
		ApplyFlags:                tx.TapNONE,
		OpenLedger:                false,
		ViewOpen:                  false,
		EnforceLoadFee:            false,
	}
}

func buildOrdinaryReplayFixture(t *testing.T) ordinaryReplayFixture {
	t.Helper()
	all.RegisterAll()
	parent := makeGenesisLedger(t)
	_, account, err := genesis.GenerateGenesisAccountID()
	require.NoError(t, err)

	transaction := accounttx.NewAccountSet(account)
	transaction.GetCommon().Fee = "10"
	transaction.GetCommon().SigningPubKey = ""
	transaction.GetCommon().SetSequence(1)
	txBytes, err := tx.SerializeTransaction(transaction)
	require.NoError(t, err)
	transaction.SetRawBytes(txBytes)
	txHash, err := tx.ComputeTransactionHash(transaction)
	require.NoError(t, err)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	rules := replayRules()
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	cfg := replayConfig(parent, closeTime, rules)
	result := txengine.NewEngine(open, cfg).Apply(transaction)
	require.True(t, result.Result.IsApplied(), result.Result.String())
	require.NotNil(t, result.Metadata)
	leaf, err := tx.CreateTxWithMetaBlob(txBytes, result.Metadata)
	require.NoError(t, err)
	require.NoError(t, open.AddTransactionWithMeta(txHash, leaf))
	require.NoError(t, open.Close(closeTime, 0))

	return ordinaryReplayFixture{
		parent: parent,
		header: open.Header(),
		leaves: []replayTestLeaf{{
			txBytes: txBytes,
			meta:    result.Metadata,
			hash:    txHash,
			blob:    leaf,
		}},
		cfg: cfg,
	}
}

func buildBatchReplayFixture(t *testing.T) batchReplayFixture {
	t.Helper()
	all.RegisterAll()
	parent := makeGenesisLedger(t)
	_, account, err := genesis.GenerateGenesisAccountID()
	require.NoError(t, err)

	inner1 := accounttx.NewAccountSet(account)
	inner1.GetCommon().Fee = "0"
	inner1.GetCommon().SigningPubKey = ""
	inner1.GetCommon().SetSequence(2)
	inner1.GetCommon().SetFlags(tx.TfInnerBatchTxn)
	inner2 := accounttx.NewAccountSet(account)
	inner2.GetCommon().Fee = "0"
	inner2.GetCommon().SigningPubKey = ""
	inner2.GetCommon().SetSequence(3)
	inner2.GetCommon().SetFlags(tx.TfInnerBatchTxn)
	outer := batchtx.NewBatch(account)
	outer.GetCommon().Fee = "40"
	outer.GetCommon().SigningPubKey = ""
	outer.GetCommon().SetSequence(1)
	outer.GetCommon().SetFlags(batchtx.BatchFlagAllOrNothing)
	outer.AddInnerTransaction(inner1)
	outer.AddInnerTransaction(inner2)
	outerBytes, err := tx.SerializeTransaction(outer)
	require.NoError(t, err)
	outer.SetRawBytes(outerBytes)
	outerHash, err := tx.ComputeTransactionHash(outer)
	require.NoError(t, err)

	closeTime := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	rules := replayRules()
	open, err := ledger.NewOpen(parent, closeTime)
	require.NoError(t, err)
	cfg := replayConfig(parent, closeTime, rules)
	engine := txengine.NewEngine(open, cfg)
	result := engine.Apply(outer)
	result = engine.ApplyBatchInnerTransactions(context.Background(), outer, result)
	require.True(t, result.Result.IsApplied(), result.Result.String())
	require.Len(t, result.AppliedInnerTransactions, 2)

	outerLeaf, err := tx.CreateTxWithMetaBlob(outerBytes, result.Metadata)
	require.NoError(t, err)
	require.NoError(t, open.AddTransactionWithMeta(outerHash, outerLeaf))
	outerRecord := replayTestLeaf{
		txBytes: outerBytes,
		meta:    result.Metadata,
		hash:    outerHash,
		blob:    outerLeaf,
	}
	innerRecords := make([]replayTestLeaf, 0, len(result.AppliedInnerTransactions))
	for _, applied := range result.AppliedInnerTransactions {
		require.NotNil(t, applied.Metadata)
		innerBytes, innerErr := tx.SerializeTransaction(applied.Transaction)
		require.NoError(t, innerErr)
		innerHash, innerErr := tx.ComputeTransactionHash(applied.Transaction)
		require.NoError(t, innerErr)
		innerLeaf, innerErr := tx.CreateTxWithMetaBlob(innerBytes, applied.Metadata)
		require.NoError(t, innerErr)
		require.NoError(t, open.AddTransactionWithMeta(innerHash, innerLeaf))
		innerRecords = append(innerRecords, replayTestLeaf{
			txBytes: innerBytes,
			meta:    applied.Metadata,
			hash:    innerHash,
			blob:    innerLeaf,
		})
	}
	require.NoError(t, open.Close(closeTime, 0))

	leaves := append([]replayTestLeaf{outerRecord}, innerRecords...)
	return batchReplayFixture{
		ordinaryReplayFixture: ordinaryReplayFixture{
			parent: parent,
			header: open.Header(),
			leaves: leaves,
			cfg:    cfg,
		},
		outer:   outerRecord,
		inners:  innerRecords,
		account: account,
	}
}

func buildReplayResponseFromLeaves(
	t *testing.T,
	target header.LedgerHeader,
	leaves []replayTestLeaf,
) (*message.ReplayDeltaResponse, header.LedgerHeader) {
	t.Helper()
	txMap := shamap.New(shamap.TypeTransaction)
	blobs := make([][]byte, 0, len(leaves))
	for _, leaf := range leaves {
		require.NoError(t, txMap.PutWithNodeType(leaf.hash, leaf.blob, shamap.NodeTypeTransactionWithMeta))
		blobs = append(blobs, leaf.blob)
	}
	require.NoError(t, txMap.SetImmutable())
	txRoot, err := txMap.Hash()
	require.NoError(t, err)
	target.TxHash = txRoot
	headerBytes := header.AddRaw(target, false)
	target.Hash = computeWireHeaderHash(headerBytes)
	return &message.ReplayDeltaResponse{
		LedgerHash:   target.Hash[:],
		LedgerHeader: headerBytes,
		Transactions: blobs,
	}, target
}

func cloneReplayLeaf(t *testing.T, original replayTestLeaf, mutate func(*tx.Metadata)) replayTestLeaf {
	t.Helper()
	require.NotNil(t, original.meta)
	metadata := *original.meta
	metadata.AffectedNodes = append([]tx.AffectedNode(nil), original.meta.AffectedNodes...)
	mutate(&metadata)
	blob, err := tx.CreateTxWithMetaBlob(original.txBytes, &metadata)
	require.NoError(t, err)
	return replayTestLeaf{
		txBytes: original.txBytes,
		meta:    &metadata,
		hash:    original.hash,
		blob:    blob,
	}
}

func alteredLedgerIndex(index string) string {
	if len(index) == 0 {
		return "0"
	}
	result := []byte(index)
	if result[0] == '0' {
		result[0] = '1'
	} else {
		result[0] = '0'
	}
	return string(result)
}

type replayLedgerRoots struct {
	hash      [32]byte
	stateRoot [32]byte
	txRoot    [32]byte
}

func captureReplayLedgerRoots(t *testing.T, l *ledger.Ledger) replayLedgerRoots {
	t.Helper()
	stateRoot, err := l.StateMapHash()
	require.NoError(t, err)
	txRoot, err := l.TxMapHash()
	require.NoError(t, err)
	return replayLedgerRoots{hash: l.Hash(), stateRoot: stateRoot, txRoot: txRoot}
}

func assertReplayLedgerRootsUnchanged(t *testing.T, l *ledger.Ledger, before replayLedgerRoots) {
	t.Helper()
	after := captureReplayLedgerRoots(t, l)
	assert.Equal(t, before, after, "replay rejection must not mutate its parent ledger")
}

func assertReplayApplyRejected(
	t *testing.T,
	replay *ReplayDelta,
	parent *ledger.Ledger,
	parentBefore replayLedgerRoots,
	err error,
	expected error,
) {
	t.Helper()
	require.Error(t, err)
	assert.ErrorIs(t, err, expected)
	assert.Equal(t, StateFailed, replay.State())
	assert.False(t, replay.IsComplete())
	result, resultErr := replay.Result()
	assert.Nil(t, result)
	assert.Error(t, resultErr)
	assert.ErrorIs(t, replay.Err(), expected)
	assertReplayLedgerRootsUnchanged(t, parent, parentBefore)
}

func assertReplayRoots(t *testing.T, target header.LedgerHeader, derived *ledger.Ledger) {
	t.Helper()
	require.NotNil(t, derived)
	assert.Equal(t, target.Hash, derived.Hash())
	stateRoot, err := derived.StateMapHash()
	require.NoError(t, err)
	assert.Equal(t, target.AccountHash, stateRoot)
	txRoot, err := derived.TxMapHash()
	require.NoError(t, err)
	assert.Equal(t, target.TxHash, txRoot)
}

func TestReplayDelta_Apply_OrdinaryFixtureSuccess(t *testing.T) {
	fixture := buildOrdinaryReplayFixture(t)
	response, target := buildReplayResponseFromLeaves(t, fixture.header, fixture.leaves)
	replay := armReplayDeltaWith(t, fixture.parent, response, target)

	derived, err := replay.Apply(fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, StateComplete, replay.State())
	assertReplayRoots(t, target, derived)

	ordered := replay.OrderedTxs()
	require.Len(t, ordered, 1)
	assert.Equal(t, fixture.parent.Hash(), target.ParentHash)
	assert.Equal(t, fixture.parent.Sequence()+1, target.LedgerIndex)
	assert.Equal(t, fixture.leaves[0].txBytes, ordered[0].TxBytes, "replay must use the exact transaction bytes from the verified leaf")
	expectedMeta, err := tx.SerializeMetadata(fixture.leaves[0].meta)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(expectedMeta, ordered[0].MetaBytes), "ordinary metadata must be carried byte-for-byte")
	result, err := replay.Result()
	require.NoError(t, err)
	assertReplayRoots(t, target, result)
}

func TestReplayDelta_Apply_RejectsOrdinaryMetadataTampering(t *testing.T) {
	cases := []struct {
		name         string
		mutate       func(*tx.Metadata)
		wantExpected string
		wantActual   string
	}{
		{
			name: "result-only",
			mutate: func(meta *tx.Metadata) {
				meta.TransactionResult = ter.TecCLAIM
			},
			wantExpected: ter.TecCLAIM.String(),
			wantActual:   ter.TesSUCCESS.String(),
		},
		{
			name: "affected-nodes-only",
			mutate: func(meta *tx.Metadata) {
				if len(meta.AffectedNodes) > 0 {
					meta.AffectedNodes[0].LedgerIndex = alteredLedgerIndex(meta.AffectedNodes[0].LedgerIndex)
				}
			},
			wantExpected: ter.TesSUCCESS.String(),
			wantActual:   ter.TesSUCCESS.String(),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := buildOrdinaryReplayFixture(t)
			parentBefore := captureReplayLedgerRoots(t, fixture.parent)
			require.NotEmpty(t, fixture.leaves[0].meta.AffectedNodes)
			tampered := cloneReplayLeaf(t, fixture.leaves[0], tc.mutate)
			response, target := buildReplayResponseFromLeaves(t, fixture.header, []replayTestLeaf{tampered})
			assert.Equal(t, fixture.header.AccountHash, target.AccountHash, "metadata tampering must preserve the target state root")
			assert.NotEqual(t, fixture.header.TxHash, target.TxHash, "tampering must update the authenticated tx root")
			replay := armReplayDeltaWith(t, fixture.parent, response, target)
			_, err := replay.Apply(fixture.cfg)
			assertReplayApplyRejected(t, replay, fixture.parent, parentBefore, err, ErrReplayMetadataDiverged)

			var failure *ReplayFailure
			require.ErrorAs(t, err, &failure)
			assert.Equal(t, tc.wantExpected, failure.ExpectedResult)
			assert.Equal(t, tc.wantActual, failure.ActualResult)
			assert.NotEmpty(t, failure.ExpectedMetadata)
			assert.NotEmpty(t, failure.ActualMetadata)
			assert.NotEqual(t, failure.ExpectedMetadata, failure.ActualMetadata)
		})
	}
}

func TestReplayDelta_GotResponse_RejectsMissingOrdinaryMetadata(t *testing.T) {
	fixture := buildOrdinaryReplayFixture(t)
	parentBefore := captureReplayLedgerRoots(t, fixture.parent)
	txVL, err := tx.EncodeVL(len(fixture.leaves[0].txBytes))
	require.NoError(t, err)
	missingMeta := append([]byte(nil), txVL...)
	missingMeta = append(missingMeta, fixture.leaves[0].txBytes...)
	metaVL, err := tx.EncodeVL(0)
	require.NoError(t, err)
	missingMeta = append(missingMeta, metaVL...)
	missing := fixture.leaves[0]
	missing.blob = missingMeta
	response, target := buildReplayResponseFromLeaves(t, fixture.header, []replayTestLeaf{missing})
	assert.Equal(t, fixture.header.AccountHash, target.AccountHash, "missing metadata must preserve the target state root")
	assert.NotEqual(t, fixture.header.TxHash, target.TxHash, "missing metadata must update the authenticated tx root")
	replay := NewReplayDelta(target.Hash, 7, fixture.parent, nil)

	err = replay.GotResponse(response)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "metadata missing")
	assert.Equal(t, StateFailed, replay.State())
	assert.False(t, replay.IsComplete())
	result, resultErr := replay.Result()
	assert.Nil(t, result)
	assert.Error(t, resultErr)
	assertReplayLedgerRootsUnchanged(t, fixture.parent, parentBefore)
}

func TestReplayDelta_Apply_BatchFixtureSuccess(t *testing.T) {
	fixture := buildBatchReplayFixture(t)
	response, target := buildReplayResponseFromLeaves(t, fixture.header, fixture.leaves)
	replay := armReplayDeltaWith(t, fixture.parent, response, target)

	derived, err := replay.Apply(fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, StateComplete, replay.State())
	assertReplayRoots(t, target, derived)
	assert.Len(t, replay.OrderedTxs(), 3)
}

func TestReplayDelta_Apply_RejectsBatchTampering(t *testing.T) {
	cases := []struct {
		name   string
		leaves func(*testing.T, batchReplayFixture) []replayTestLeaf
		want   error
	}{
		{
			name: "inner-result-only",
			leaves: func(t *testing.T, fixture batchReplayFixture) []replayTestLeaf {
				tampered := cloneReplayLeaf(t, fixture.inners[0], func(meta *tx.Metadata) {
					meta.TransactionResult = ter.TecCLAIM
				})
				return []replayTestLeaf{fixture.outer, tampered, fixture.inners[1]}
			},
			want: ErrReplayMetadataDiverged,
		},
		{
			name: "inner-affected-nodes-only",
			leaves: func(t *testing.T, fixture batchReplayFixture) []replayTestLeaf {
				require.NotEmpty(t, fixture.inners[0].meta.AffectedNodes)
				tampered := cloneReplayLeaf(t, fixture.inners[0], func(meta *tx.Metadata) {
					meta.AffectedNodes[0].LedgerIndex = alteredLedgerIndex(meta.AffectedNodes[0].LedgerIndex)
				})
				return []replayTestLeaf{fixture.outer, tampered, fixture.inners[1]}
			},
			want: ErrReplayMetadataDiverged,
		},
		{
			name: "missing-inner",
			leaves: func(_ *testing.T, fixture batchReplayFixture) []replayTestLeaf {
				return []replayTestLeaf{fixture.outer, fixture.inners[0]}
			},
			want: ErrReplayTxDiverged,
		},
		{
			name: "unexpected-inner",
			leaves: func(t *testing.T, fixture batchReplayFixture) []replayTestLeaf {
				unexpected := accounttx.NewAccountSet(fixture.account)
				unexpected.GetCommon().Fee = "0"
				unexpected.GetCommon().SigningPubKey = ""
				unexpected.GetCommon().SetSequence(4)
				unexpected.GetCommon().SetFlags(tx.TfInnerBatchTxn)
				unexpectedBytes, err := tx.SerializeTransaction(unexpected)
				require.NoError(t, err)
				unexpected.SetRawBytes(unexpectedBytes)
				unexpectedHash, err := tx.ComputeTransactionHash(unexpected)
				require.NoError(t, err)
				unexpectedMeta := &tx.Metadata{
					TransactionIndex:  3,
					TransactionResult: ter.TesSUCCESS,
				}
				unexpectedLeaf, err := tx.CreateTxWithMetaBlob(unexpectedBytes, unexpectedMeta)
				require.NoError(t, err)
				return []replayTestLeaf{
					fixture.outer,
					fixture.inners[0],
					fixture.inners[1],
					{txBytes: unexpectedBytes, meta: unexpectedMeta, hash: unexpectedHash, blob: unexpectedLeaf},
				}
			},
			want: ErrReplayTxDiverged,
		},
		{
			name: "reordered-inners",
			leaves: func(t *testing.T, fixture batchReplayFixture) []replayTestLeaf {
				first := cloneReplayLeaf(t, fixture.inners[0], func(meta *tx.Metadata) {
					meta.TransactionIndex = fixture.inners[1].meta.TransactionIndex
				})
				second := cloneReplayLeaf(t, fixture.inners[1], func(meta *tx.Metadata) {
					meta.TransactionIndex = fixture.inners[0].meta.TransactionIndex
				})
				return []replayTestLeaf{fixture.outer, first, second}
			},
			want: ErrReplayTxDiverged,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fixture := buildBatchReplayFixture(t)
			parentBefore := captureReplayLedgerRoots(t, fixture.parent)
			leaves := tc.leaves(t, fixture)
			response, target := buildReplayResponseFromLeaves(t, fixture.header, leaves)
			assert.Equal(t, fixture.header.AccountHash, target.AccountHash, "inner tampering must preserve the target state root")
			assert.NotEqual(t, fixture.header.TxHash, target.TxHash, "tampering must update the authenticated tx root")
			replay := armReplayDeltaWith(t, fixture.parent, response, target)
			_, err := replay.Apply(fixture.cfg)
			assertReplayApplyRejected(t, replay, fixture.parent, parentBefore, err, tc.want)
		})
	}
}

func TestReplayDelta_RetryDiscardsFailedWorkingEvidence(t *testing.T) {
	fixture := buildOrdinaryReplayFixture(t)
	parentBefore := captureReplayLedgerRoots(t, fixture.parent)
	response, target := buildReplayResponseFromLeaves(t, fixture.header, fixture.leaves)
	replay := armReplayDeltaWith(t, fixture.parent, response, target)

	// The authenticated target remains intact; only the mutable decoded metadata
	// used by the first apply attempt is corrupted after verification.
	replay.mu.Lock()
	replay.txs[0].MetaBytes = []byte{0xff}
	replay.mu.Unlock()
	_, err := replay.Apply(fixture.cfg)
	assertReplayApplyRejected(t, replay, fixture.parent, parentBefore, err, ErrReplayMetadataDiverged)

	retry, err := replay.Retry(fixture.parent)
	require.NoError(t, err)
	assert.Equal(t, StateReplayReady, retry.State())
	derived, err := retry.Apply(fixture.cfg)
	require.NoError(t, err)
	assert.Equal(t, StateComplete, retry.State())
	assertReplayRoots(t, target, derived)
	assertReplayLedgerRootsUnchanged(t, fixture.parent, parentBefore)
	_, err = replay.Result()
	assert.Error(t, err, "failed replay must never promote a derived result")
}
