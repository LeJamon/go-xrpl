package consensus

import (
	"testing"
	"time"

	consensus "github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClusterCreation(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	assert.Len(t, cluster.Nodes, 3)

	// Each node should have a unique identity
	keys := make(map[string]bool)
	for _, node := range cluster.Nodes {
		assert.NotNil(t, node.Identity)
		assert.NotNil(t, node.Service)
		assert.NotNil(t, node.Engine)
		assert.NotNil(t, node.Adaptor)
		assert.False(t, keys[node.NodeKey], "duplicate node key")
		keys[node.NodeKey] = true
	}
}

func TestClusterNodesHaveSameInitialLedgerSequence(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	for i, node := range cluster.Nodes {
		assert.Equal(t, uint32(2), node.Service.GetClosedLedgerIndex(), "node %d initial closed ledger", i)
		assert.Zero(t, node.Service.GetValidatedLedgerIndex(), "node %d initial validated ledger", i)
	}
}

func TestClusterMutualTrust(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	// Each node should trust all other nodes
	for i, node := range cluster.Nodes {
		for j, other := range cluster.Nodes {
			trusted := node.Adaptor.IsTrusted(other.Identity.NodeID)
			assert.True(t, trusted, "node %d should trust node %d", i, j)
		}

		// Quorum for 3 validators: ceil(3 * 0.8) = 3
		assert.Equal(t, 3, node.Adaptor.GetQuorum(), "node %d quorum", i)
	}
}

func TestClusterOperatingMode(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	// All nodes should be set to Full mode
	for i, node := range cluster.Nodes {
		mode := node.Adaptor.GetOperatingMode()
		assert.Equal(t, consensus.OpModeFull, mode, "node %d should be Full", i)
	}
}

func TestClusterValidatorIdentities(t *testing.T) {
	cluster := NewTestCluster(t, 5)
	defer cluster.Stop()

	assert.Len(t, cluster.Nodes, 5)

	for i, node := range cluster.Nodes {
		assert.True(t, node.Adaptor.IsValidator(), "node %d should be validator", i)

		key, err := node.Adaptor.GetValidatorKey()
		require.NoError(t, err, "node %d GetValidatorKey", i)
		assert.Equal(t, node.Identity.NodeID, key, "node %d key mismatch", i)
	}
}

func TestClusterProposalSignVerifyAcrossNodes(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	// Node 0 signs a proposal
	proposal := &consensus.Proposal{
		Round: consensus.RoundID{
			Seq:        3,
			ParentHash: [32]byte{0x01},
		},
		NodeID:         cluster.Nodes[0].Identity.NodeID,
		Position:       0,
		TxSet:          consensus.TxSetID{0x02},
		CloseTime:      time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		PreviousLedger: consensus.LedgerID{0x01},
	}

	err := cluster.Nodes[0].Adaptor.SignProposal(proposal)
	require.NoError(t, err)

	// Node 1 should be able to verify it
	err = cluster.Nodes[1].Adaptor.VerifyProposal(proposal)
	assert.NoError(t, err)

	// Node 2 should also verify it
	err = cluster.Nodes[2].Adaptor.VerifyProposal(proposal)
	assert.NoError(t, err)
}

func TestClusterValidationSignVerifyAcrossNodes(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	// Node 1 signs a validation
	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0xAA},
		LedgerSeq: 5,
		NodeID:    cluster.Nodes[1].Identity.NodeID,
		SignTime:  time.Date(2025, 6, 1, 12, 0, 0, 0, time.UTC),
		Full:      true,
	}

	err := cluster.Nodes[1].Adaptor.SignValidation(validation)
	require.NoError(t, err)

	// Node 0 can verify
	err = cluster.Nodes[0].Adaptor.VerifyValidation(validation)
	assert.NoError(t, err)
}

func TestClusterTxSetSharing(t *testing.T) {
	cluster := NewTestCluster(t, 2)
	defer cluster.Stop()

	// Node 0 builds a tx set
	blobs := [][]byte{
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C},
		{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C},
	}
	ts, err := cluster.Nodes[0].Adaptor.BuildTxSet(blobs)
	require.NoError(t, err)

	// Node 1 can build the same tx set and get the same ID
	ts2, err := cluster.Nodes[1].Adaptor.BuildTxSet(blobs)
	require.NoError(t, err)
	assert.Equal(t, ts.ID(), ts2.ID(), "same blobs should produce same tx set ID")
}

func TestWrongLedgerModeDemotion(t *testing.T) {
	cluster := NewTestCluster(t, 3)
	defer cluster.Stop()

	cluster.Nodes[0].Adaptor.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, cluster.Nodes[0].Adaptor.GetOperatingMode())

	cluster.Nodes[0].Adaptor.OnModeChange(consensus.ModeProposing, consensus.ModeWrongLedger)
	assert.Equal(t, consensus.OpModeConnected, cluster.Nodes[0].Adaptor.GetOperatingMode())
}
