package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"runtime"
	rmetrics "runtime/metrics"
	"strings"
	"sync"
	"testing"
	"time"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	ledgerpkg "github.com/LeJamon/go-xrpl/internal/ledger"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	batchtest "github.com/LeJamon/go-xrpl/internal/testing/batch"
	nfttest "github.com/LeJamon/go-xrpl/internal/testing/nft"
	"github.com/LeJamon/go-xrpl/internal/tx"
	batchtx "github.com/LeJamon/go-xrpl/internal/tx/batch"
	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/shamap"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
	"github.com/LeJamon/go-xrpl/storage/nodestore"
	"github.com/stretchr/testify/require"
)

// BenchmarkService_Issue2082NFTBatchAcceptance is a synthetic incident-shaped
// workload: one signed Batch contains two mint/sell/buy/brokered-accept cycles,
// for eight real inner NFT transactions. Thirty NFTs are preloaded on the
// seller so the acceptance path exercises a saturated NFTokenPage as well as
// offer-directory updates. The incident ledgers named by issue #2082 are not
// present in this checkout, so this fixture is intentionally labeled synthetic.
func BenchmarkService_Issue2082NFTBatchAcceptance(b *testing.B) {
	for _, refresh := range []bool{false, true} {
		name := "arm=isolated"
		if refresh {
			name = "arm=online-delete-refresh"
		}
		b.Run(name, func(b *testing.B) {
			benchmarkIssue2082NFTBatchAcceptance(b, refresh)
		})
	}
}

type issue2082NFTBatchWorkload struct {
	agreed       [][]byte
	agreedHash   [][32]byte
	innerRaw     [][]byte
	innerHash    [][32]byte
	innerParents [][32]byte
	workloadHash [32]byte
}

const (
	issue2082WantLedgerHash = "F350CF78F6344DADCA107BF0C6C277747E8350A52DF4DB27491C8A3555D845B9"
	issue2082WantStateRoot  = "CADF52511D6879C5C58F3D133D51824278E3EC909228D5D2732EE21D3C784670"
	issue2082WantTxRoot     = "DFBD24515E1B3F1C0D635E198272D8FF49D6FE7C82137B72E6E48BD4F32170A5"
	issue2082WantMetadata   = "DF9802B3C458671CE51735EE3A0FBD4BD82DD976030260E0FC03CC87D88D0D94"
	issue2082WantWorkload   = "DF7D211F2D8D38528AE36CB3C7FDF7652DCCFE9335EA774C8A95A6A01FE9A58E"
)

func benchmarkIssue2082NFTBatchAcceptance(b *testing.B, withRefresh bool) {
	b.Helper()
	b.StopTimer()
	fixture, workload := newIssue2082NFTBatchFixture(b)
	bytesPerRound := 0
	for _, blob := range workload.agreed {
		bytesPerRound += len(blob)
	}
	b.SetBytes(int64(bytesPerRound))
	b.ReportAllocs()

	runtime.GC()
	accept := make([]time.Duration, 0, b.N)
	var (
		gcCPUSeconds   float64
		gcAllocBytes   uint64
		acceptWallTime time.Duration
	)
	var (
		wantLedgerHash [32]byte
		wantStateRoot  [32]byte
		wantTxRoot     [32]byte
		wantMetadata   [32]byte
		haveFixture    bool
	)
	var activeService *Service
	b.Cleanup(func() {
		if activeService != nil {
			activeService.Stop()
		}
	})
	b.ResetTimer()
	for round := range b.N {
		b.StopTimer()
		logger := &batchAcceptanceBenchmarkLogger{}
		svc, parent := newBatchConsensusAcceptanceService(b, fixture, logger)
		activeService = svc
		promotedBefore := fixture.db.promotedNodes.Load()

		var refreshDone <-chan error
		if withRefresh {
			fixture.db.promotionStart = make(chan struct{})
			fixture.db.promotionOnce = sync.Once{}
			fixture.db.promotionDelay = 100 * time.Microsecond
			done := make(chan error, 1)
			refreshDone = done
			go func() {
				done <- svc.refreshGenerationStateWithBatch(
					b.Context(),
					fixture.root,
					fixture.seq,
					fixture.db,
					nil,
					1,
					storedSHAMapPromotionBatchNodes,
					storedSHAMapPromotionBatchBytes,
				)
			}()
			select {
			case <-fixture.db.promotionStart:
			case err := <-done:
				svc.Stop()
				b.Fatalf("refresh ended before first promotion: %v", err)
			case <-time.After(5 * time.Second):
				svc.Stop()
				b.Fatal("refresh did not reach first promotion")
			}
		}

		gcBefore := issue2082GCStats()
		b.StartTimer()
		start := time.Now()
		_, err := svc.AcceptConsensusResult(
			b.Context(),
			parent,
			workload.agreed,
			nil,
			parent.CloseTime().Add(10*time.Second),
			true,
		)
		acceptElapsed := time.Since(start)
		b.StopTimer()
		gcAfter := issue2082GCStats()
		gcCPUSeconds += gcAfter.cpuSeconds - gcBefore.cpuSeconds
		gcAllocBytes += gcAfter.allocBytes - gcBefore.allocBytes
		acceptWallTime += acceptElapsed
		require.NoError(b, err)
		closed := svc.GetClosedLedger()
		require.NotNil(b, closed)
		if refreshDone != nil {
			require.NoError(b, <-refreshDone)
		}
		fixture.db.promotionDelay = 0
		promotedNodes := fixture.db.promotedNodes.Load() - promotedBefore
		if withRefresh {
			require.Positive(b, promotedNodes, "online-delete refresh did not promote parent state")
		}

		sample, ok := logger.latest()
		if !ok {
			svc.Stop()
			activeService = nil
			b.Fatal("acceptance did not emit consensus acceptance timings")
		}
		accept = append(accept, acceptElapsed)
		metadataDigest, nftNodeCount := assertIssue2082NFTBatchEffects(b, closed, workload)
		if !haveFixture {
			require.Equal(b, issue2082WantLedgerHash, fmt.Sprintf("%X", closed.Hash()))
			require.Equal(b, issue2082WantStateRoot, fmt.Sprintf("%X", closed.Header().AccountHash))
			require.Equal(b, issue2082WantTxRoot, fmt.Sprintf("%X", closed.Header().TxHash))
			require.Equal(b, issue2082WantMetadata, fmt.Sprintf("%X", metadataDigest))
			require.Equal(b, issue2082WantWorkload, fmt.Sprintf("%X", workload.workloadHash))
			wantLedgerHash = closed.Hash()
			wantStateRoot = closed.Header().AccountHash
			wantTxRoot = closed.Header().TxHash
			wantMetadata = metadataDigest
			haveFixture = true
		} else {
			require.Equal(b, wantLedgerHash, closed.Hash(), "round %d changed ledger hash", round)
			require.Equal(b, wantStateRoot, closed.Header().AccountHash, "round %d changed state root", round)
			require.Equal(b, wantTxRoot, closed.Header().TxHash, "round %d changed transaction root", round)
			require.Equal(b, wantMetadata, metadataDigest, "round %d changed metadata bytes", round)
		}
		if round == 0 {
			b.Logf("acceptance=%s logger-total=%s promoted-nodes=%d ledger=%X state=%X tx=%X metadata=%X workload=%X",
				acceptElapsed,
				sample.total,
				promotedNodes,
				closed.Hash(),
				closed.Header().AccountHash,
				closed.Header().TxHash,
				metadataDigest,
				workload.workloadHash,
			)
		}
		require.GreaterOrEqual(b, nftNodeCount, 4, "NFT-heavy Batch metadata did not include page and offer changes")
		svc.Stop()
		activeService = nil
		if round+1 < b.N {
			prepareIssue2082ParentForRotation(b, fixture, withRefresh)
			fixture.rotateAndReopen(b)
		}
	}
	b.StopTimer()
	reportConsensusAcceptanceQuantiles(b, "acceptance", accept)
	if b.N > 0 && acceptWallTime > 0 {
		b.ReportMetric(gcCPUSeconds/float64(b.N), "gc-cpu-sec/op")
		b.ReportMetric(float64(gcAllocBytes)/acceptWallTime.Seconds(), "gc-alloc-bytes/s")
	}
}

func prepareIssue2082ParentForRotation(b testing.TB, fixture *benchmarkRefreshFixture, stateReady bool) {
	b.Helper()
	ctx := b.Context()
	stateRoot, err := fixture.template.StateMapHash()
	require.NoError(b, err)
	if !stateReady {
		promotedBefore := fixture.db.promotedNodes.Load()
		require.NoError(b, fixture.svc.refreshGenerationStateWithBatch(
			ctx,
			stateRoot,
			fixture.seq,
			fixture.db,
			nil,
			1,
			storedSHAMapPromotionBatchNodes,
			storedSHAMapPromotionBatchBytes,
		))
		require.Positive(b, fixture.db.promotedNodes.Load()-promotedBefore,
			"isolated reset did not promote parent state before rotation")
	}
	txRoot, err := fixture.template.TxMapHash()
	require.NoError(b, err)
	if txRoot != ([32]byte{}) {
		require.NoError(b, fixture.svc.walkStoredSHAMapWithFetch(
			ctx,
			txRoot,
			shamap.TypeTransaction,
			fixture.db.FetchForPromotion,
			nil,
		))
	}
	require.NoError(b, fixture.db.Store(ctx, &nodestore.Node{
		Type:      nodestore.NodeLedger,
		Hash:      nodestore.Hash256(fixture.template.Hash()),
		Data:      fixture.template.SerializeHeader(),
		LedgerSeq: fixture.seq,
	}))
	require.NoError(b, fixture.db.Sync(ctx))
}

type issue2082GCMeasurement struct {
	cpuSeconds float64
	allocBytes uint64
}

func issue2082GCStats() issue2082GCMeasurement {
	samples := []rmetrics.Sample{
		{Name: "/cpu/classes/gc/total:cpu-seconds"},
		{Name: "/gc/heap/allocs:bytes"},
	}
	rmetrics.Read(samples)
	return issue2082GCMeasurement{
		cpuSeconds: samples[0].Value.Float64(),
		allocBytes: samples[1].Value.Uint64(),
	}
}

func newIssue2082NFTBatchFixture(b *testing.B) (*benchmarkRefreshFixture, issue2082NFTBatchWorkload) {
	b.Helper()
	env := jtx.NewTestEnvWithConfig(b, batchBenchmarkGenesisConfig())
	env.SetVerifySignatures(true)
	seller := jtx.NewAccount("issue2082-nft-seller")
	buyer := jtx.NewAccount("issue2082-nft-buyer")
	broker := jtx.NewAccount("issue2082-nft-broker")
	env.Fund(seller, buyer, broker)

	const preloadCount = 30
	const taxon = uint32(2082)
	const transferFee = uint16(0)
	for i := range preloadCount {
		id := nfttest.GetNextNFTokenID(env, seller, taxon, nftoken.NFTokenFlagTransferable, transferFee)
		result := env.Submit(nfttest.NFTokenMint(seller, taxon).
			Transferable().
			URI(issue2082NFTURI(64, i)).
			Build())
		jtx.RequireTxSuccess(b, result)
		if id == "" {
			b.Fatal("preload generated an empty NFTokenID")
		}
	}

	// Add deterministic non-NFT leaves so the online-delete refresh arm walks
	// multiple persisted SHAMap promotion batches while acceptance is running.
	validated := env.Ledger()
	for i := range 4_096 {
		var key [32]byte
		binary.BigEndian.PutUint32(key[28:], uint32(i+1))
		data := make([]byte, 12)
		binary.BigEndian.PutUint32(data[8:], uint32(i+1))
		require.NoError(b, validated.Insert(keylet.Keylet{Key: key}, data))
	}
	env.Close()

	fixture := &benchmarkRefreshFixture{
		path: filepath.Join(b.TempDir(), "nodes"),
		options: kvpebble.Options{
			BlockCacheBytes: 256 << 10,
			MaxOpenFiles:    200,
		},
	}
	backend, err := kvpebble.NewRotating(fixture.path, fixture.options)
	require.NoError(b, err)
	base, err := nodestore.NewRotatingKVDatabase(backend, nodestore.DatabaseConfig{})
	require.NoError(b, err)
	db := &countingGenerationDatabase{Database: base, generation: base}
	parent := env.LastClosedLedger()
	writer := &Service{nodeStore: base}
	require.NoError(b, writer.persistToNodeStore(b.Context(), parent, parent.Sequence()))
	fixture.root, err = parent.StateMapHash()
	require.NoError(b, err)
	fixture.seq = parent.Sequence()
	fixture.rotation = fixture.seq
	fixture.template, err = parent.Snapshot()
	require.NoError(b, err)
	committed, err := db.RotateGeneration(b.Context(), fixture.rotation, 1)
	require.True(b, committed)
	require.NoError(b, err)
	require.NoError(b, db.Close())
	fixture.db = db
	fixture.base = base
	fixture.open(b)
	b.Cleanup(func() { fixture.close(b) })

	workload := buildIssue2082NFTBatchWorkload(b, env, seller, buyer, broker, taxon, transferFee)
	return fixture, workload
}

func buildIssue2082NFTBatchWorkload(
	b testing.TB,
	env *jtx.TestEnv,
	seller, buyer, broker *jtx.Account,
	taxon uint32,
	transferFee uint16,
) issue2082NFTBatchWorkload {
	b.Helper()
	outerSequence := env.Seq(seller)
	buyerSequence := env.Seq(buyer)
	brokerSequence := env.Seq(broker)
	tokenSequence := nfttest.GetNFTokenSeq(env, seller)
	const mintFlags = nftoken.NFTokenFlagTransferable
	tokenIDs := [2][32]byte{
		nftoken.GenerateNFTokenID(seller.ID, taxon, tokenSequence, mintFlags, transferFee),
		nftoken.GenerateNFTokenID(seller.ID, taxon, tokenSequence+1, mintFlags, transferFee),
	}

	sellAmount := jtx.XRPTxAmount(jtx.XRP(25))
	buyAmount := jtx.XRPTxAmount(jtx.XRP(30))
	brokerFee := jtx.XRPTxAmount(jtx.XRP(1))
	builder := batchtest.NewBatchBuilder(
		seller,
		outerSequence,
		batchtest.CalcBatchFee(env.BaseFee(), 2, 8),
		batchtx.BatchFlagAllOrNothing,
	)
	for i := range tokenIDs {
		mint := nfttest.NFTokenMint(seller, taxon).
			Transferable().
			URI(issue2082NFTURI(256, i)).
			Fee(0).
			Sequence(outerSequence + 1 + uint32(i*2)).
			Build()
		markIssue2082Inner(mint)

		sellSequence := outerSequence + 2 + uint32(i*2)
		sell := nfttest.NFTokenCreateSellOffer(seller, hex.EncodeToString(tokenIDs[i][:]), sellAmount).
			Fee(0).
			Sequence(sellSequence).
			Build()
		markIssue2082Inner(sell)

		buy := nfttest.NFTokenCreateBuyOffer(buyer, hex.EncodeToString(tokenIDs[i][:]), buyAmount, seller).
			Fee(0).
			Sequence(buyerSequence + uint32(i)).
			Build()
		markIssue2082Inner(buy)

		sellID := keylet.NFTokenOffer(seller.ID, sellSequence)
		buyID := keylet.NFTokenOffer(buyer.ID, buyerSequence+uint32(i))
		accept := nfttest.NFTokenBrokeredSale(broker, stringsToHex(sellID.Key), stringsToHex(buyID.Key)).
			BrokerFee(brokerFee).
			Fee(0).
			Sequence(brokerSequence + uint32(i)).
			Build()
		markIssue2082Inner(accept)

		builder.AddInnerTx(mint).AddInnerTx(sell).AddInnerTx(buy).AddInnerTx(accept)
	}
	batch := builder.AddSigner(buyer).AddSigner(broker).MustBuild()
	env.SignWith(batch, seller)

	outerBlob, err := tx.SerializeTransaction(batch)
	require.NoError(b, err)
	outerHash, err := tx.ComputeTransactionHash(batch)
	require.NoError(b, err)
	workload := issue2082NFTBatchWorkload{
		agreed:       [][]byte{outerBlob},
		agreedHash:   [][32]byte{outerHash},
		innerRaw:     make([][]byte, 0, len(batch.RawTransactions)),
		innerHash:    make([][32]byte, 0, len(batch.RawTransactions)),
		innerParents: make([][32]byte, 0, len(batch.RawTransactions)),
	}
	for _, raw := range batch.RawTransactions {
		inner := raw.RawTransaction.InnerTx
		innerBlob, innerErr := tx.SerializeTransaction(inner)
		require.NoError(b, innerErr)
		innerHash, hashErr := tx.ComputeTransactionHash(inner)
		require.NoError(b, hashErr)
		workload.innerRaw = append(workload.innerRaw, innerBlob)
		workload.innerHash = append(workload.innerHash, innerHash)
		workload.innerParents = append(workload.innerParents, outerHash)
	}
	h := sha256.New()
	for _, blob := range workload.agreed {
		_, _ = h.Write(blob)
	}
	for _, blob := range workload.innerRaw {
		_, _ = h.Write(blob)
	}
	copy(workload.workloadHash[:], h.Sum(nil))
	return workload
}

func markIssue2082Inner(transaction tx.Transaction) {
	common := transaction.GetCommon()
	common.Fee = "0"
	common.SigningPubKey = ""
	common.TxnSignature = ""
	common.SetFlags(common.GetFlags() | tx.TfInnerBatchTxn)
}

func issue2082NFTURI(size, ordinal int) string {
	const prefix = "https://example.invalid/xrpl/issue-2082/"
	uri := make([]byte, size)
	for i := range uri {
		uri[i] = prefix[(i+ordinal)%len(prefix)]
	}
	return string(uri)
}

func stringsToHex(value [32]byte) string {
	return hex.EncodeToString(value[:])
}

func assertIssue2082NFTBatchEffects(
	b testing.TB,
	closed *ledgerpkg.Ledger,
	workload issue2082NFTBatchWorkload,
) ([32]byte, int) {
	b.Helper()
	// A successor ledger's transaction map contains the transactions accepted in
	// that ledger; the parent's transaction map is retained by its own ledger.
	require.Equal(b, uint32(len(workload.agreed)+len(workload.innerHash)), closed.TxCount())
	digest := sha256.New()
	nftNodeCount := 0
	for i, hash := range workload.agreedHash {
		stored, found, err := closed.GetTransaction(hash)
		require.NoError(b, err)
		require.True(b, found, "agreed transaction %d missing", i)
		raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(b, err)
		require.True(b, bytes.Equal(workload.agreed[i], raw), "agreed transaction %d changed on disk", i)
		nodeCount, err := validateIssue2082Metadata(b, metadata, "agreed", i, nil)
		require.NoError(b, err)
		nftNodeCount += nodeCount
		_, _ = digest.Write(metadata)
	}
	for i, hash := range workload.innerHash {
		stored, found, err := closed.GetTransaction(hash)
		require.NoError(b, err)
		require.True(b, found, "inner transaction %d missing", i)
		raw, metadata, err := tx.SplitTxWithMetaBlobStrict(stored)
		require.NoError(b, err)
		require.True(b, bytes.Equal(workload.innerRaw[i], raw), "inner transaction %d changed on disk", i)
		nodeCount, err := validateIssue2082Metadata(b, metadata, "inner", i, &workload.innerParents[i])
		require.NoError(b, err)
		nftNodeCount += nodeCount
		_, _ = digest.Write(metadata)
		exists, err := closed.TxExists(hash)
		require.NoError(b, err)
		require.True(b, exists, "inner transaction %d missing from transaction map", i)
	}
	var result [32]byte
	copy(result[:], digest.Sum(nil))
	return result, nftNodeCount
}

func validateIssue2082Metadata(
	b testing.TB,
	metadata []byte,
	kind string,
	index int,
	parentBatchID *[32]byte,
) (int, error) {
	b.Helper()
	decoded, err := binarycodec.DecodeBytes(metadata)
	if err != nil {
		return 0, err
	}
	if decoded["TransactionResult"] != "tesSUCCESS" {
		return 0, fmt.Errorf("%s transaction %d result = %v, want tesSUCCESS", kind, index, decoded["TransactionResult"])
	}
	if parentBatchID != nil {
		wantParent := stringsToHex(*parentBatchID)
		if got, _ := decoded["ParentBatchID"].(string); !strings.EqualFold(got, wantParent) {
			return 0, fmt.Errorf("%s transaction %d parent = %q, want %q", kind, index, got, wantParent)
		}
	}
	return countIssue2082NFTLedgerNodes(decoded["AffectedNodes"]), nil
}

func countIssue2082NFTLedgerNodes(value any) int {
	switch typed := value.(type) {
	case map[string]any:
		count := 0
		if entryType, ok := typed["LedgerEntryType"].(string); ok &&
			(entryType == "NFTokenPage" || entryType == "NFTokenOffer") {
			count++
		}
		for _, child := range typed {
			count += countIssue2082NFTLedgerNodes(child)
		}
		return count
	case []any:
		count := 0
		for _, child := range typed {
			count += countIssue2082NFTLedgerNodes(child)
		}
		return count
	default:
		return 0
	}
}
