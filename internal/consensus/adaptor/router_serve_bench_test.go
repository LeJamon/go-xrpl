package adaptor

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/LeJamon/go-xrpl/internal/ledger/genesis"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	jtx "github.com/LeJamon/go-xrpl/internal/testing"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/LeJamon/go-xrpl/shamap"
)

const (
	ledgerReplyBenchmarkLeaves       = 1024
	ledgerReplyBenchmarkSmallPayload = 32
	ledgerReplyBenchmarkLargePayload = 4 << 10
	ledgerReplyBenchmarkRootCap      = 32
	ledgerReplyBenchmarkLeafCap      = 128
	ledgerReplyBenchmarkSampleLimit  = 1024
)

var (
	benchmarkReplySinkBytes atomic.Uint64
	benchmarkReplySinkNodes atomic.Uint64
)

// ledgerReplyBenchmarkFamily stores serialized SHAMap nodes without a lower
// cache. The optional CleanResidency is the only reuse layer, so clearing it
// gives a repeatable cold peer read while retaining the backed map's root.
type ledgerReplyBenchmarkFamily struct {
	mu         sync.RWMutex
	nodes      map[[32]byte][]byte
	residency  *shamap.CleanResidency
	fetches    atomic.Uint64
	fetchBytes atomic.Uint64
}

type ledgerReplyBenchmarkPolicy struct {
	name     string
	rootSoft int
	rootHard int
	leafSoft int
	leafHard int
}

var ledgerReplyBenchmarkPolicies = []ledgerReplyBenchmarkPolicy{
	{name: "default"},
	{name: "smallcap32", rootSoft: ledgerReplyBenchmarkRootCap, rootHard: ledgerReplyBenchmarkRootCap, leafSoft: ledgerReplyBenchmarkLeafCap, leafHard: ledgerReplyBenchmarkLeafCap},
}

func newLedgerReplyBenchmarkFamily() *ledgerReplyBenchmarkFamily {
	return &ledgerReplyBenchmarkFamily{
		nodes:     make(map[[32]byte][]byte),
		residency: shamap.NewCleanResidency(256<<20, nil),
	}
}

func (f *ledgerReplyBenchmarkFamily) Fetch(ctx context.Context, hash [32]byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.RLock()
	data := append([]byte(nil), f.nodes[hash]...)
	f.mu.RUnlock()
	if len(data) != 0 {
		f.fetches.Add(1)
		f.fetchBytes.Add(uint64(len(data)))
	}
	return data, nil
}

// FetchBounded mirrors the strict backend capability used by peer serving.
// The oversized result is reported before cloning the stored payload, so a
// small reply cap does not turn a rejected record into a temporary allocation.
func (f *ledgerReplyBenchmarkFamily) FetchBounded(ctx context.Context, hash [32]byte, maxBytes int) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	f.mu.RLock()
	stored, ok := f.nodes[hash]
	if !ok {
		f.mu.RUnlock()
		return nil, false, nil
	}
	if maxBytes < 0 || len(stored) > maxBytes {
		f.mu.RUnlock()
		return nil, true, nil
	}
	data := append([]byte(nil), stored...)
	f.mu.RUnlock()
	f.fetches.Add(1)
	f.fetchBytes.Add(uint64(len(data)))
	return data, false, nil
}

func (f *ledgerReplyBenchmarkFamily) FetchWithPolicy(ctx context.Context, hash [32]byte, _ shamap.ReadPolicy) ([]byte, error) {
	return f.Fetch(ctx, hash)
}

func (f *ledgerReplyBenchmarkFamily) StoreBatch(ctx context.Context, entries []shamap.FlushEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	for _, entry := range entries {
		f.nodes[entry.Hash] = append([]byte(nil), entry.Data...)
	}
	f.mu.Unlock()
	return nil
}

func (f *ledgerReplyBenchmarkFamily) SHAMapCleanResidency() *shamap.CleanResidency {
	return f.residency
}

func (f *ledgerReplyBenchmarkFamily) clearPeerResidency() {
	release := f.residency.BeginMutation()
	release()
}

func (f *ledgerReplyBenchmarkFamily) resetFetchStats() {
	f.fetches.Store(0)
	f.fetchBytes.Store(0)
}

type ledgerReplyBenchmarkFixture struct {
	root    [32]byte
	tree    *shamap.SHAMap
	family  *ledgerReplyBenchmarkFamily
	leafIDs [][]byte
}

func newLedgerReplyBenchmarkFixture(b *testing.B, mapType shamap.Type, payloadBytes int) *ledgerReplyBenchmarkFixture {
	b.Helper()
	family := newLedgerReplyBenchmarkFamily()
	source := shamap.New(mapType)
	for i := 0; i < ledgerReplyBenchmarkLeaves; i++ {
		data := benchmarkMapValue(i, payloadBytes)
		if mapType == shamap.TypeTransaction {
			data = benchmarkTransactionValue(i, payloadBytes)
		}
		key := benchmarkMapKey(mapType, i, data)
		if err := source.Put(key, data); err != nil {
			b.Fatal(err)
		}
	}
	// Keep a shallow branch with leaves at the normal query-depth boundary.
	// This makes payload size visible in ledger-node replies while the broad
	// hashed population still exercises a large root subtree.
	if mapType == shamap.TypeState {
		for i := 0; i < shamap.BranchFactor; i++ {
			var key [32]byte
			key[1] = byte(i)
			key[31] = 0xD0
			if err := source.Put(key, benchmarkMapValue(ledgerReplyBenchmarkLeaves+i, payloadBytes)); err != nil {
				b.Fatal(err)
			}
		}
	}

	root, err := source.Hash()
	if err != nil {
		b.Fatal(err)
	}
	if err := source.StoreDirty(func(entries []shamap.FlushEntry) error {
		return family.StoreBatch(context.Background(), entries)
	}); err != nil {
		b.Fatal(err)
	}
	backed, err := shamap.NewFromRootHash(mapType, root, family)
	if err != nil {
		b.Fatal(err)
	}
	rootData, err := source.SerializeRoot()
	if err != nil {
		b.Fatal(err)
	}
	rootReader, err := shamap.DeserializeFromWire(rootData)
	if err != nil {
		b.Fatal(err)
	}
	rootHash := rootReader.Hash()
	if rootReader.Type() != shamap.NodeTypeInner || rootHash != root {
		b.Fatalf("invalid root fixture: type=%s hash=%x want=%x", rootReader.Type(), rootHash[:8], root[:8])
	}

	wireNodes, err := source.WalkWireNodes()
	if err != nil {
		b.Fatal(err)
	}
	leafIDs := make([][]byte, 0, 16)
	for _, node := range wireNodes {
		reader, err := shamap.DeserializeFromWire(node.Data)
		if err != nil {
			b.Fatal(err)
		}
		if reader.Hash() == ([32]byte{}) || reader.Type() == shamap.NodeTypeInner {
			continue
		}
		if _, err := shamap.ParseNodeID(node.NodeID); err != nil {
			b.Fatal(err)
		}
		leafIDs = append(leafIDs, append([]byte(nil), node.NodeID...))
		if len(leafIDs) == 16 {
			break
		}
	}
	if len(leafIDs) == 0 {
		b.Fatal("fixture has no leaf node IDs")
	}
	if got, err := backed.Hash(); err != nil || got != root {
		b.Fatalf("backed root hash = %x, %v; want %x", got, err, root)
	}
	family.resetFetchStats()
	return &ledgerReplyBenchmarkFixture{
		root:    root,
		tree:    backed,
		family:  family,
		leafIDs: leafIDs,
	}
}

func benchmarkMapKey(mapType shamap.Type, i int, data []byte) [32]byte {
	if mapType == shamap.TypeTransaction {
		return sha512half.Sum(protocol.HashPrefixTransactionID().Bytes(), data)
	}
	var ordinal [8]byte
	binary.BigEndian.PutUint64(ordinal[:], uint64(i))
	return sha512half.Sum([]byte("go-xrpl/issue-2079/ledger-reply"), ordinal[:])
}

func benchmarkMapValue(i, size int) []byte {
	data := make([]byte, size)
	for j := range data {
		data[j] = byte(i*31 + j*17)
	}
	return data
}

func benchmarkTransactionValue(i, size int) []byte {
	data := benchmarkMapValue(i, size)
	if len(data) >= 8 {
		binary.BigEndian.PutUint64(data[:8], uint64(i))
	}
	return data
}

type ledgerReplyBenchmarkRequest struct {
	nodeIDs    [][]byte
	queryDepth int
	softCap    int
	hardCap    int
}

func (f *ledgerReplyBenchmarkFixture) request(shape string, policy ledgerReplyBenchmarkPolicy) ledgerReplyBenchmarkRequest {
	softCap, hardCap := 0, 0
	if shape == "leaf" {
		softCap, hardCap = policy.leafSoft, policy.leafHard
	} else {
		softCap, hardCap = policy.rootSoft, policy.rootHard
	}
	if shape == "leaf" {
		ids := make([][]byte, len(f.leafIDs))
		for i := range f.leafIDs {
			ids[i] = append([]byte(nil), f.leafIDs[i]...)
		}
		return ledgerReplyBenchmarkRequest{
			nodeIDs: ids,
			softCap: softCap,
			hardCap: hardCap,
		}
	}
	return ledgerReplyBenchmarkRequest{
		nodeIDs:    [][]byte{rootNodeID()},
		queryDepth: 3,
		softCap:    softCap,
		hardCap:    hardCap,
	}
}

type ledgerReplyBenchmarkNetwork struct{}

func (ledgerReplyBenchmarkNetwork) PeerSupportsNodeDepth(uint64) bool { return false }
func (ledgerReplyBenchmarkNetwork) SendToPeer(uint64, []byte) error   { return nil }
func (ledgerReplyBenchmarkNetwork) ShouldShedLedgerRequest(uint64, bool) bool {
	return false
}
func (ledgerReplyBenchmarkNetwork) PeerWithLedger([32]byte, uint32, uint64) (uint64, bool) {
	return 0, false
}
func (ledgerReplyBenchmarkNetwork) PeerLatency(uint64) (time.Duration, bool) {
	return 0, false
}
func (ledgerReplyBenchmarkNetwork) IncPeerBadData(uint64, string) {}

func newLedgerReplyBenchmarkRouter() *Router {
	return &Router{
		logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		serve:           ledgerReplyBenchmarkNetwork{},
		serveMemory:     newLedgerServeMemory(),
		serveReplyBytes: ledgerServeReplyBytes,
		lifecycleCtx:    context.Background(),
	}
}

func (f *ledgerReplyBenchmarkFixture) buildAndEncodeReply(ctx context.Context, router *Router, req ledgerReplyBenchmarkRequest, infoType message.LedgerInfoType, fatLeaves bool) ([]message.LedgerNode, []byte, bool, error) {
	workspace := router.reserveLedgerServeWorkspace(ctx, false)
	if workspace == nil {
		return nil, nil, false, nil
	}
	defer workspace.close()

	request := &message.GetLedger{
		InfoType:      infoType,
		LedgerHash:    append([]byte(nil), f.root[:]...),
		NodeIDs:       req.nodeIDs,
		QueryDepth:    uint32(req.queryDepth),
		QueryDepthSet: req.queryDepth != 0,
	}
	var nodes []message.LedgerNode
	var err error
	if infoType == message.LedgerInfoTsCandidate {
		nodes, err = buildShaMapReplyNodes(ctx, workspace.walkBudget(), f.tree, request.NodeIDs, req.queryDepth, fatLeaves, silentLogger{}, 1, "benchmark")
	} else {
		nodes = router.serveLedgerMapNodes(ctx, workspace, func(context.Context) (*shamap.SHAMap, error) { return f.tree, nil }, request, 1, "benchmark")
	}
	if err != nil {
		return nil, nil, true, err
	}
	frame, err := message.EncodeLedgerDataFrame(&message.LedgerData{
		LedgerHash: f.root[:],
		InfoType:   infoType,
		Nodes:      nodes,
	}, workspace.wire)
	if err != nil {
		return nil, nil, true, err
	}
	if err := router.serve.SendToPeer(1, frame); err != nil {
		return nil, nil, true, err
	}
	return nodes, frame, true, nil
}

func setLedgerReplyBenchmarkCaps(req ledgerReplyBenchmarkRequest) func() {
	if req.softCap <= 0 || req.hardCap <= 0 {
		return func() {}
	}
	oldSoft, oldHard := txSetReplyCapsForTest()
	setTxSetReplyCapsForTest(req.softCap, req.hardCap)
	return func() { setTxSetReplyCapsForTest(oldSoft, oldHard) }
}

func validateLedgerReplyNodes(root [32]byte, nodes []message.LedgerNode, rootRequest bool) error {
	if len(nodes) == 0 {
		return fmt.Errorf("reply contains no nodes")
	}
	for i, node := range nodes {
		nodeID, err := shamap.ParseNodeID(node.NodeID)
		if err != nil {
			return fmt.Errorf("reply node %d has invalid NodeID: %w", i, err)
		}
		reader, err := shamap.DeserializeFromWire(node.NodeData)
		if err != nil {
			return fmt.Errorf("reply node %d has invalid wire data: %w", i, err)
		}
		hash := reader.Hash()
		if hash == ([32]byte{}) {
			return fmt.Errorf("reply node %d has zero hash", i)
		}
		if rootRequest && i == 0 && hash != root {
			return fmt.Errorf("reply root hash = %x, want %x", hash[:8], root[:8])
		}
		if leaf, ok := reader.(shamap.LeafReader); ok && !nodeID.IsRoot() {
			want, err := shamap.NewNodeID(nodeID.Depth(), leaf.Item().Key())
			if err != nil {
				return fmt.Errorf("reply node %d cannot derive leaf path: %w", i, err)
			}
			gotID, wantID := nodeID.ID(), want.ID()
			if wantID != gotID {
				return fmt.Errorf("reply node %d has NodeID path %x, want leaf prefix %x", i, gotID[:4], wantID[:4])
			}
		}
	}
	return nil
}

func benchmarkRouterReply(b *testing.B, fixture *ledgerReplyBenchmarkFixture, policy ledgerReplyBenchmarkPolicy, shape string, infoType message.LedgerInfoType, fatLeaves bool, cold bool) {
	b.Helper()
	request := fixture.request(shape, policy)
	router := newLedgerReplyBenchmarkRouter()
	defer setLedgerReplyBenchmarkCaps(request)()
	if !cold {
		_, _, admitted, err := fixture.buildAndEncodeReply(context.Background(), router, request, infoType, fatLeaves)
		if err != nil {
			b.Fatal(err)
		}
		if !admitted {
			b.Fatal("workspace reservation denied during warm-up")
		}
	}
	fixture.family.resetFetchStats()
	var totalFrameBytes, totalNodeBytes, totalNodes, denied uint64
	validated := false
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if cold {
			b.StopTimer()
			fixture.family.clearPeerResidency()
			b.StartTimer()
		}
		nodes, frame, admitted, err := fixture.buildAndEncodeReply(context.Background(), router, request, infoType, fatLeaves)
		if err != nil {
			b.Fatal(err)
		}
		if !admitted {
			denied++
			continue
		}
		if !validated {
			b.StopTimer()
			if err := validateLedgerReplyNodes(fixture.root, nodes, shape == "root"); err != nil {
				b.Fatal(err)
			}
			b.StartTimer()
			validated = true
		}
		totalFrameBytes += uint64(len(frame))
		totalNodes += uint64(len(nodes))
		for _, node := range nodes {
			totalNodeBytes += uint64(len(node.NodeID) + len(node.ID) + len(node.NodeData))
		}
	}
	b.StopTimer()
	benchmarkReplySinkBytes.Add(totalFrameBytes)
	benchmarkReplySinkNodes.Add(totalNodes)
	workspaceSnapshot := router.serveMemory.Snapshot()
	b.ReportMetric(float64(workspaceSnapshot.Peak), "managed-workspace-peak-bytes")
	b.ReportMetric(float64(workspaceSnapshot.Denied), "managed-workspace-denied")
	b.ReportMetric(float64(denied), "workspace-denied")
	if b.N != 0 {
		denom := float64(b.N)
		b.ReportMetric(float64(totalFrameBytes)/denom, "frame-bytes/op")
		b.ReportMetric(float64(totalNodeBytes)/denom, "node-bytes/op")
		b.ReportMetric(float64(totalNodes)/denom, "nodes/op")
		b.ReportMetric(float64(fixture.family.fetchBytes.Load())/denom, "family-fetch-bytes/op")
		b.ReportMetric(float64(fixture.family.fetches.Load())/denom, "family-fetches/op")
	}
}

func BenchmarkRouterLedgerReply(b *testing.B) {
	for _, policy := range ledgerReplyBenchmarkPolicies {
		for _, mapCase := range []struct {
			name    string
			mapType shamap.Type
			info    message.LedgerInfoType
		}{
			{name: "state", mapType: shamap.TypeState, info: message.LedgerInfoAsNode},
			{name: "transaction", mapType: shamap.TypeTransaction, info: message.LedgerInfoTxNode},
		} {
			for _, payloadBytes := range []int{ledgerReplyBenchmarkSmallPayload, ledgerReplyBenchmarkLargePayload} {
				fixture := newLedgerReplyBenchmarkFixture(b, mapCase.mapType, payloadBytes)
				for _, shape := range []string{"root", "leaf"} {
					for _, cold := range []bool{false, true} {
						mode := "hot"
						if cold {
							mode = "cold"
						}
						name := fmt.Sprintf("policy=%s/map=%s/payload=%d/shape=%s/%s", policy.name, mapCase.name, payloadBytes, shape, mode)
						b.Run(name, func(b *testing.B) {
							benchmarkRouterReply(b, fixture, policy, shape, mapCase.info, true, cold)
						})
					}
				}
			}
		}
	}
}

func BenchmarkRouterTxSetReply(b *testing.B) {
	for _, policy := range ledgerReplyBenchmarkPolicies {
		for _, payloadBytes := range []int{ledgerReplyBenchmarkSmallPayload, ledgerReplyBenchmarkLargePayload} {
			fixture := newLedgerReplyBenchmarkFixture(b, shamap.TypeTransaction, payloadBytes)
			for _, shape := range []string{"root", "leaf"} {
				for _, cold := range []bool{false, true} {
					mode := "hot"
					if cold {
						mode = "cold"
					}
					name := fmt.Sprintf("policy=%s/payload=%d/shape=%s/%s", policy.name, payloadBytes, shape, mode)
					b.Run(name, func(b *testing.B) {
						benchmarkRouterReply(b, fixture, policy, shape, message.LedgerInfoTsCandidate, false, cold)
					})
				}
			}
		}
	}
}

func BenchmarkRouterLedgerCloseUnderReplyLoad(b *testing.B) {
	for _, policy := range ledgerReplyBenchmarkPolicies {
		for _, workers := range []int{0, 4} {
			b.Run(fmt.Sprintf("policy=%s/serve-workers=%d", policy.name, workers), func(b *testing.B) {
				benchmarkRouterLedgerCloseUnderReplyLoad(b, policy, workers)
			})
		}
	}
}

func benchmarkRouterLedgerCloseUnderReplyLoad(b *testing.B, policy ledgerReplyBenchmarkPolicy, workers int) {
	b.Helper()
	fixture := newLedgerReplyBenchmarkFixture(b, shamap.TypeState, 256)
	cfg := genesis.DefaultConfig()
	for i := 0; i < 256; i++ {
		cfg.InitialAccounts = append(cfg.InitialAccounts, genesis.InitialAccount{
			Address:  jtx.NewAccount(fmt.Sprintf("issue-2079-close-%d", i)).Address,
			Balance:  100_000_000,
			Sequence: 1,
		})
	}
	svc, err := service.New(service.Config{Standalone: true, GenesisConfig: cfg})
	if err != nil {
		b.Fatal(err)
	}
	if err := svc.Start(); err != nil {
		b.Fatal(err)
	}
	defer svc.Stop()

	router := newLedgerReplyBenchmarkRouter()
	request := fixture.request("root", policy)
	defer setLedgerReplyBenchmarkCaps(request)()
	var serveReplies atomic.Uint64
	var serveFailures atomic.Uint64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				nodes, frame, admitted, err := fixture.buildAndEncodeReply(context.Background(), router, request, message.LedgerInfoAsNode, true)
				if err != nil {
					serveFailures.Add(1)
					return
				}
				if !admitted {
					continue
				}
				_ = nodes
				_ = frame
				serveReplies.Add(1)
			}
		}()
	}

	latencies := make([]time.Duration, 0, min(b.N, ledgerReplyBenchmarkSampleLimit))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		started := time.Now()
		if _, err := svc.AcceptLedger(context.Background()); err != nil {
			b.Fatal(err)
		}
		if len(latencies) < ledgerReplyBenchmarkSampleLimit {
			latencies = append(latencies, time.Since(started))
		}
	}
	b.StopTimer()
	close(stop)
	wg.Wait()
	if serveFailures.Load() != 0 {
		b.Fatalf("serve workers failed %d times", serveFailures.Load())
	}
	reportLedgerReplyBenchmarkQuantiles(b, "ledger-close", latencies)
	b.ReportMetric(float64(serveReplies.Load()), "serve-replies")
	b.ReportMetric(float64(fixture.family.fetchBytes.Load()), "serve-family-fetch-bytes")
	workspaceSnapshot := router.serveMemory.Snapshot()
	b.ReportMetric(float64(workspaceSnapshot.Peak), "managed-workspace-peak-bytes")
	b.ReportMetric(float64(workspaceSnapshot.Denied), "managed-workspace-denied")
}

func reportLedgerReplyBenchmarkQuantiles(b *testing.B, name string, samples []time.Duration) {
	b.Helper()
	if len(samples) == 0 {
		return
	}
	ordered := append([]time.Duration(nil), samples...)
	slices.Sort(ordered)
	for _, percentile := range []int{50, 95, 99} {
		index := (len(ordered)*percentile + 99) / 100
		if index < 1 {
			index = 1
		}
		if index > len(ordered) {
			index = len(ordered)
		}
		b.ReportMetric(float64(ordered[index-1].Nanoseconds()), fmt.Sprintf("%s-p%d-ns", name, percentile))
	}
	b.ReportMetric(float64(ordered[len(ordered)-1].Nanoseconds()), name+"-max-ns")
	b.ReportMetric(float64(len(ordered)), name+"-samples")
}
