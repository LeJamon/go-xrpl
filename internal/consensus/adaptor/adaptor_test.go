package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/internal/consensus"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newTestLedgerService(t *testing.T) *service.Service {
	t.Helper()
	cfg := service.Config{
		Standalone:    true, // standalone for tests that expect seq 2
		GenesisConfig: genesis.DefaultConfig(),
	}
	svc, err := service.New(cfg)
	require.NoError(t, err)
	require.NoError(t, svc.Start())
	return svc
}

func newTestAdaptor(t *testing.T) *Adaptor {
	t.Helper()
	svc := newTestLedgerService(t)

	// Create a validator identity from a known test seed
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)
	require.NotNil(t, identity)

	validators := []consensus.NodeID{identity.NodeID}

	return New(Config{
		LedgerService: svc,
		Identity:      identity,
		Validators:    validators,
	})
}

func TestAdaptorCreation(t *testing.T) {
	a := newTestAdaptor(t)
	require.NotNil(t, a)

	// Should be a validator
	assert.True(t, a.IsValidator())

	// Should have a validator key
	key, err := a.GetValidatorKey()
	assert.NoError(t, err)
	assert.NotEqual(t, consensus.NodeID{}, key)

	// Quorum for 1 validator should be 1
	assert.Equal(t, 1, a.GetQuorum())

	// Initial operating mode should be disconnected
	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())

	// The single validator should be trusted
	assert.True(t, a.IsTrusted(key))

	// A random node should not be trusted
	assert.False(t, a.IsTrusted(consensus.NodeID{0x99}))

	// GetTrustedValidators should return our validator
	validators := a.GetTrustedValidators()
	assert.Len(t, validators, 1)
	assert.Equal(t, key, validators[0])
}

func TestAdaptorNonValidator(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{
		LedgerService: svc,
		Identity:      nil, // no validator identity
	})

	assert.False(t, a.IsValidator())

	_, err := a.GetValidatorKey()
	assert.ErrorIs(t, err, ErrNoValidatorKey)
}

func TestAdaptorOperatingMode(t *testing.T) {
	a := newTestAdaptor(t)

	assert.Equal(t, consensus.OpModeDisconnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeConnected)
	assert.Equal(t, consensus.OpModeConnected, a.GetOperatingMode())

	a.SetOperatingMode(consensus.OpModeFull)
	assert.Equal(t, consensus.OpModeFull, a.GetOperatingMode())
}

func TestAdaptorGetLastClosedLedger(t *testing.T) {
	a := newTestAdaptor(t)

	lcl, err := a.GetLastClosedLedger()
	require.NoError(t, err)
	require.NotNil(t, lcl)

	// After Start(), the LCL should be sequence 2
	assert.Equal(t, uint32(2), lcl.Seq())
	assert.NotEqual(t, consensus.LedgerID{}, lcl.ID())
}

func TestAdaptorPendingTxs(t *testing.T) {
	a := newTestAdaptor(t)

	// Initially empty
	pending := a.GetPendingTxs()
	assert.Empty(t, pending)

	// Add some tx blobs
	blob1 := []byte{0x01, 0x02, 0x03}
	blob2 := []byte{0x04, 0x05, 0x06}
	a.AddPendingTx(blob1)
	a.AddPendingTx(blob2)

	pending = a.GetPendingTxs()
	assert.Len(t, pending, 2)

	// HasTx should work
	txID1 := computeTxID(blob1)
	assert.True(t, a.HasTx(txID1))

	// GetTx should work
	got, err := a.GetTx(txID1)
	assert.NoError(t, err)
	assert.Equal(t, blob1, got)

	// Clear
	a.ClearPendingTxs()
	pending = a.GetPendingTxs()
	assert.Empty(t, pending)
	assert.False(t, a.HasTx(txID1))
}

func TestAdaptorQuorumCalculation(t *testing.T) {
	svc := newTestLedgerService(t)

	tests := []struct {
		numValidators  int
		expectedQuorum int
	}{
		{1, 1},
		{2, 2},
		{3, 3},
		{4, 4},
		{5, 4},
		{10, 8},
		{20, 16},
		{100, 80},
	}

	for _, tt := range tests {
		validators := make([]consensus.NodeID, tt.numValidators)
		for i := range validators {
			validators[i] = consensus.NodeID{byte(i)}
		}
		a := New(Config{
			LedgerService: svc,
			Validators:    validators,
		})
		assert.Equal(t, tt.expectedQuorum, a.GetQuorum(),
			"quorum for %d validators", tt.numValidators)
	}
}

func TestTxSetCreateAndLookup(t *testing.T) {
	a := newTestAdaptor(t)

	// Blobs must be >= 12 bytes (SHAMap transaction leaf minimum)
	blobs := [][]byte{
		{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C},
		{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C},
	}

	ts, err := a.BuildTxSet(blobs)
	require.NoError(t, err)
	assert.Equal(t, 2, ts.Size())
	assert.NotEqual(t, consensus.TxSetID{}, ts.ID())

	// Should be retrievable from cache
	retrieved, err := a.GetTxSet(ts.ID())
	require.NoError(t, err)
	assert.Equal(t, ts.ID(), retrieved.ID())

	// Unknown ID should error
	_, err = a.GetTxSet(consensus.TxSetID{0xFF})
	assert.Error(t, err)
}

func TestTxSetContains(t *testing.T) {
	blob1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C}
	blob2 := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C}
	blob3 := []byte{0x21, 0x22, 0x23, 0x24, 0x25, 0x26, 0x27, 0x28, 0x29, 0x2A, 0x2B, 0x2C}

	ts := NewTxSet([][]byte{blob1, blob2})

	id1 := computeTxID(blob1)
	id2 := computeTxID(blob2)
	id3 := computeTxID(blob3)

	assert.True(t, ts.Contains(id1))
	assert.True(t, ts.Contains(id2))
	assert.False(t, ts.Contains(id3))
}

func TestTxSetAddRemove(t *testing.T) {
	blob1 := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0A, 0x0B, 0x0C}
	blob2 := []byte{0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C}

	ts := NewTxSet([][]byte{blob1})
	assert.Equal(t, 1, ts.Size())

	originalID := ts.ID()

	// Add
	err := ts.Add(blob2)
	require.NoError(t, err)
	assert.Equal(t, 2, ts.Size())
	assert.NotEqual(t, originalID, ts.ID()) // ID should change

	// Remove
	id1 := computeTxID(blob1)
	err = ts.Remove(id1)
	require.NoError(t, err)
	assert.Equal(t, 1, ts.Size())
	assert.False(t, ts.Contains(id1))
}

func TestProposalSignVerify(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	proposal := &consensus.Proposal{
		Round: consensus.RoundID{
			Seq:        3,
			ParentHash: [32]byte{0x01},
		},
		NodeID:         identity.NodeID,
		Position:       0,
		TxSet:          consensus.TxSetID{0x02},
		CloseTime:      time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		PreviousLedger: consensus.LedgerID{0x03},
		Timestamp:      time.Now(),
	}

	// Sign
	err = identity.SignProposal(proposal)
	require.NoError(t, err)
	assert.NotEmpty(t, proposal.Signature)

	// Verify
	err = VerifyProposal(proposal)
	assert.NoError(t, err)

	// Tamper and verify fails
	proposal.Position = 99
	err = VerifyProposal(proposal)
	assert.Error(t, err)
}

func TestValidationSignVerify(t *testing.T) {
	identity, err := NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	require.NoError(t, err)

	validation := &consensus.Validation{
		LedgerID:  consensus.LedgerID{0x01},
		LedgerSeq: 5,
		NodeID:    identity.NodeID,
		SignTime:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		Full:      true,
		Cookie:    12345,
		LoadFee:   256,
	}

	// Sign
	err = identity.SignValidation(validation)
	require.NoError(t, err)
	assert.NotEmpty(t, validation.Signature)

	// Verify
	err = VerifyValidation(validation)
	assert.NoError(t, err)

	// Tamper and verify fails
	validation.LedgerSeq = 99
	err = VerifyValidation(validation)
	assert.Error(t, err)
}

func TestValidatorIdentityFromSeed(t *testing.T) {
	// Nil seed should return nil identity
	identity, err := NewValidatorIdentity("")
	assert.NoError(t, err)
	assert.Nil(t, identity)

	// Valid seed should produce an identity
	identity, err = NewValidatorIdentity("snoPBrXtMeMyMHUVTgbuqAfg1SUTb")
	assert.NoError(t, err)
	assert.NotNil(t, identity)
	assert.Len(t, identity.PublicKey, 33) // compressed secp256k1
	assert.NotEqual(t, consensus.NodeID{}, identity.NodeID)
}

func TestLedgerWrapper(t *testing.T) {
	svc := newTestLedgerService(t)

	l := svc.GetClosedLedger()
	require.NotNil(t, l)

	wrapper := WrapLedger(l)
	assert.Equal(t, uint32(2), wrapper.Seq())
	assert.NotEqual(t, consensus.LedgerID{}, wrapper.ID())
	assert.NotNil(t, wrapper.Bytes())
	assert.Equal(t, l, wrapper.Unwrap())
}

func TestNetworkSenderNoopDefault(t *testing.T) {
	svc := newTestLedgerService(t)
	a := New(Config{
		LedgerService: svc,
	})

	// Network operations should not panic with noop sender
	assert.NoError(t, a.BroadcastProposal(&consensus.Proposal{}))
	assert.NoError(t, a.BroadcastValidation(&consensus.Validation{}))
	assert.NoError(t, a.RelayProposal(&consensus.Proposal{}))
	assert.NoError(t, a.RequestTxSet(consensus.TxSetID{}))
	assert.NoError(t, a.RequestLedger(consensus.LedgerID{}))
}
