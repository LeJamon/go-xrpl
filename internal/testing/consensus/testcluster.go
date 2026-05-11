// Package consensus provides integration test utilities for multi-node
// consensus testing.
package consensus

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/consensus/adaptor"
	"github.com/LeJamon/goXRPLd/internal/consensus/rcl"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
)

// TestNode represents a single node in the test cluster.
type TestNode struct {
	Service  *service.Service
	Adaptor  *adaptor.Adaptor
	Engine   consensus.Engine
	Identity *adaptor.ValidatorIdentity
	NodeKey  string // base58-encoded node public key
	started  bool
}

// TestCluster manages a group of in-process nodes for integration testing.
type TestCluster struct {
	Nodes []*TestNode
	t     *testing.T
}

// validatorSeeds are deterministic secp256k1 seeds for test validators.
var validatorSeeds = []string{
	"snoPBrXtMeMyMHUVTgbuqAfg1SUTb",
	"spqPaiDYkYJ2H7cpziSk9XWyAeCPE",
	"spiByapWt2LvpmbrB7374eS9dbNVk",
	"spizqKqVpcPE8hZy4nFUbmMSaMZWx",
	"sp1o6ZeTweRbXMYAY6VvFtGcwpERb",
}

// NewTestCluster creates a cluster of n nodes. Each node:
//   - Has its own ledger service with the same genesis
//   - Has a unique validator identity
//   - Trusts all other nodes in the cluster (mutual UNL)
//   - Uses a noop network sender (no real TCP connections)
//
// For real network testing, use TestClusterWithOverlay (future).
func NewTestCluster(t *testing.T, n int) *TestCluster {
	t.Helper()

	if n > len(validatorSeeds) {
		t.Fatalf("max %d nodes supported, got %d", len(validatorSeeds), n)
	}

	// Create validator identities and collect their public keys
	identities := make([]*adaptor.ValidatorIdentity, n)
	nodeKeys := make([]string, n)
	validatorNodeIDs := make([]consensus.NodeID, n)

	for i := 0; i < n; i++ {
		id, err := adaptor.NewValidatorIdentity(validatorSeeds[i])
		if err != nil {
			t.Fatalf("create validator identity %d: %v", i, err)
		}
		identities[i] = id
		validatorNodeIDs[i] = id.NodeID

		key, err := addresscodec.EncodeNodePublicKey(id.SigningPubKey())
		if err != nil {
			t.Fatalf("encode node key %d: %v", i, err)
		}
		nodeKeys[i] = key
	}

	// Create nodes, each with the same genesis and all validators in UNL
	nodes := make([]*TestNode, n)
	for i := 0; i < n; i++ {
		svc := createTestService(t)

		a := adaptor.New(adaptor.Config{
			LedgerService: svc,
			Identity:      identities[i],
			Validators:    validatorNodeIDs,
		})

		// Set to Full mode so the engine will start rounds
		a.SetOperatingMode(consensus.OpModeFull)

		engine := rcl.NewEngine(a, rcl.DefaultConfig())

		nodes[i] = &TestNode{
			Service:  svc,
			Adaptor:  a,
			Engine:   engine,
			Identity: identities[i],
			NodeKey:  nodeKeys[i],
		}
	}

	return &TestCluster{
		Nodes: nodes,
		t:     t,
	}
}

// Start starts all consensus engines in the cluster.
func (c *TestCluster) Start() {
	for i, node := range c.Nodes {
		if err := node.Engine.Start(nil); err != nil {
			c.t.Fatalf("start engine %d: %v", i, err)
		}
		node.started = true
	}
}

// Stop stops all consensus engines that were started.
func (c *TestCluster) Stop() {
	for _, node := range c.Nodes {
		if node.started {
			_ = node.Engine.Stop()
		}
	}
}

// GetLedgerSeq returns the closed ledger sequence for a node.
func (c *TestCluster) GetLedgerSeq(nodeIdx int) uint32 {
	return c.Nodes[nodeIdx].Service.GetClosedLedgerIndex()
}

// WaitForLedger waits until the specified node reaches the given ledger sequence.
func (c *TestCluster) WaitForLedger(nodeIdx int, seq uint32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.GetLedgerSeq(nodeIdx) >= seq {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// AllNodesAtLedger checks if all nodes have reached the given ledger sequence.
func (c *TestCluster) AllNodesAtLedger(seq uint32) bool {
	for i := range c.Nodes {
		if c.GetLedgerSeq(i) < seq {
			return false
		}
	}
	return true
}

func createTestService(t *testing.T) *service.Service {
	t.Helper()
	cfg := service.Config{
		Standalone:    false,
		GenesisConfig: genesis.DefaultConfig(),
	}
	svc, err := service.New(cfg)
	if err != nil {
		t.Fatalf("create service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start service: %v", err)
	}
	return svc
}
