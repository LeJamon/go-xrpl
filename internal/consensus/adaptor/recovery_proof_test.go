package adaptor

import (
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/consensus"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func recoveryProofFixture(t *testing.T) (*Adaptor, consensus.LedgerID, uint32) {
	t.Helper()
	a := newTestAdaptor(t)
	const seq = uint32(77)
	target := consensus.LedgerID{0x4a, 0x6f, 0x9d}
	validation := &consensus.Validation{
		LedgerID:  target,
		LedgerSeq: seq,
		Full:      true,
		SignTime:  time.Unix(1_700_000_000, 0).UTC(),
	}
	require.NoError(t, a.identity.SignValidation(validation))
	raw, err := serializeSTValidationChecked(validation)
	require.NoError(t, err)
	parsed, err := parseSTValidation(raw)
	require.NoError(t, err)
	parsed.NodeID = validation.NodeID
	parsed.Raw = append([]byte(nil), raw...)
	a.SetValidationHistorian(&recheckingHistorian{
		stubHistorian: &stubHistorian{},
		validations:   []*consensus.Validation{parsed},
		quorum:        1,
		accepted:      true,
	})
	return a, target, seq
}

func TestCaptureValidationQuorumProofRechecksSignedEvidence(t *testing.T) {
	a, target, seq := recoveryProofFixture(t)

	proof, err := a.CaptureValidationQuorumProof(seq, [32]byte(target))
	require.NoError(t, err)
	require.Equal(t, consensus.ValidationQuorumProofVersion, proof.Version)
	require.Len(t, proof.Validations, 1)
	require.NotZero(t, proof.Validations[0].MasterPubKey)
	require.NoError(t, a.RecheckValidationQuorumProof(proof))

	raw, err := a.CaptureValidationQuorumEvidence(seq, [32]byte(target))
	require.NoError(t, err)
	require.NoError(t, a.AuthenticateValidationQuorumEvidence(seq, [32]byte(target), raw))
}

func TestValidationProofTrustContextKeepsMasterKeyPairsAtomic(t *testing.T) {
	type trustSet struct {
		nodes   []consensus.NodeID
		masters [][33]byte
	}
	sets := []trustSet{
		{
			nodes:   []consensus.NodeID{{0x01}, {0x02}},
			masters: [][33]byte{{0x11}, {0x12}},
		},
		{
			nodes:   []consensus.NodeID{{0x03}, {0x04}, {0x05}},
			masters: [][33]byte{{0x21}, {0x22}, {0x23}},
		},
	}
	a := New(Config{Validators: sets[0].nodes, ValidatorMasterKeys: sets[0].masters})

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		for i := 0; i < 256; i++ {
			set := sets[i%len(sets)]
			a.SetTrustedValidators(set.nodes, set.masters)
		}
	}()

	for i := 0; i < 256; i++ {
		nodes, masters, _, _ := a.validationProofTrustContext()
		matches := false
		for _, set := range sets {
			if sameMasterSet(nodes, masters, set.nodes, set.masters) {
				matches = true
				break
			}
		}
		require.True(t, matches, "trust snapshot mixed validator and master-key generations")
	}
	<-finished
}

func TestSameMasterSetRejectsUnalignedPersistedSnapshot(t *testing.T) {
	nodes := []consensus.NodeID{{0x01}}
	masters := [][33]byte{{0x11}, {0x12}}
	require.NotPanics(t, func() {
		require.False(t, sameMasterSet(nodes, masters, nodes, masters))
	})
}

func TestCaptureValidationQuorumProofPreservesUnavailableQuorumGate(t *testing.T) {
	a, target, seq := recoveryProofFixture(t)
	a.SetQuorumUnavailableFunc(func() bool { return true })

	_, err := a.CaptureValidationQuorumProof(seq, [32]byte(target))
	require.ErrorIs(t, err, ErrRecoveryProofUnavailable)
}

func TestCaptureValidationQuorumProofRejectsZeroNetworkSigningTime(t *testing.T) {
	a := newTestAdaptor(t)
	const seq = uint32(77)
	target := consensus.LedgerID{0x4a, 0x6f, 0x9d}
	validation := &consensus.Validation{
		LedgerID:  target,
		LedgerSeq: seq,
		Full:      true,
		SignTime:  time.Unix(protocol.RippleEpochUnix, 0).UTC(),
	}
	require.NoError(t, a.identity.SignValidation(validation))
	raw, err := serializeSTValidationChecked(validation)
	require.NoError(t, err)
	parsed, err := parseSTValidation(raw)
	require.NoError(t, err)
	parsed.NodeID = validation.NodeID
	parsed.Raw = append([]byte(nil), raw...)
	a.SetValidationHistorian(&recheckingHistorian{
		stubHistorian: &stubHistorian{},
		validations:   []*consensus.Validation{parsed},
		quorum:        1,
		accepted:      true,
	})

	_, err = a.CaptureValidationQuorumProof(seq, [32]byte(target))
	require.Error(t, err)
}

func TestRecheckValidationQuorumProofRejectsTamperingAndRetargeting(t *testing.T) {
	a, target, seq := recoveryProofFixture(t)
	proof, err := a.CaptureValidationQuorumProof(seq, [32]byte(target))
	require.NoError(t, err)

	t.Run("signature bytes", func(t *testing.T) {
		copyOfProof := proof
		copyOfProof.Validations = append([]consensus.SignedValidationEvidence(nil), proof.Validations...)
		copyOfProof.Validations[0].Raw = append([]byte(nil), proof.Validations[0].Raw...)
		copyOfProof.Validations[0].Raw[len(copyOfProof.Validations[0].Raw)-1] ^= 0x01
		require.ErrorIs(t, a.RecheckValidationQuorumProof(copyOfProof), ErrRecoveryProofInvalid)
	})

	t.Run("master identity", func(t *testing.T) {
		copyOfProof := proof
		copyOfProof.Validations = append([]consensus.SignedValidationEvidence(nil), proof.Validations...)
		copyOfProof.Validations[0].MasterPubKey = [33]byte{0x01}
		require.ErrorIs(t, a.RecheckValidationQuorumProof(copyOfProof), ErrRecoveryProofInvalid)
	})

	t.Run("duplicate signer", func(t *testing.T) {
		copyOfProof := proof
		copyOfProof.Validations = append([]consensus.SignedValidationEvidence(nil), proof.Validations[0], proof.Validations[0])
		require.ErrorIs(t, a.RecheckValidationQuorumProof(copyOfProof), ErrRecoveryProofInvalid)
	})

	t.Run("target binding", func(t *testing.T) {
		raw, marshalErr := a.CaptureValidationQuorumEvidence(seq, [32]byte(target))
		require.NoError(t, marshalErr)
		require.ErrorIs(t, a.AuthenticateValidationQuorumEvidence(seq+1, [32]byte(target), raw), ErrRecoveryProofInvalid)
	})

	t.Run("network binding", func(t *testing.T) {
		copyOfProof := proof
		copyOfProof.NetworkID++
		require.ErrorIs(t, a.RecheckValidationQuorumProof(copyOfProof), ErrRecoveryProofInvalid)
	})

	t.Run("trust transition", func(t *testing.T) {
		a.SetTrustedValidators([]consensus.NodeID{{0x01}}, [][33]byte{{0x02}})
		require.ErrorIs(t, a.RecheckValidationQuorumProof(proof), ErrRecoveryProofInvalid)
	})
}

func TestRecheckValidationQuorumProofUsesPersistedManifest(t *testing.T) {
	fixture := newTokenFixture(t, 0x55, 3)
	identity, err := newValidatorIdentityFromToken(fixture.tokenBlock)
	require.NoError(t, err)
	require.NotNil(t, identity)

	parsedManifest, err := manifest.Deserialize(identity.SerializedMfst)
	require.NoError(t, err)
	cache := manifest.NewCache()
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(parsedManifest))
	a := New(Config{
		LedgerService:       newTestLedgerService(t),
		Identity:            identity,
		Validators:          []consensus.NodeID{identity.NodeID},
		ValidatorMasterKeys: [][33]byte{identity.MasterKey},
	})
	a.SetValidationSignerResolver(func(signing consensus.SigningPubKey) (consensus.NodeID, [33]byte, []byte, bool) {
		master := cache.GetMasterKey([33]byte(signing))
		wire, ok := cache.GetManifest(master)
		return consensus.CalcNodeID(master), master, wire, ok
	})

	const seq = uint32(88)
	target := consensus.LedgerID{0x10, 0x20, 0x30}
	validation := &consensus.Validation{
		LedgerID:  target,
		LedgerSeq: seq,
		Full:      true,
		SignTime:  time.Unix(1_700_000_000, 0).UTC(),
	}
	require.NoError(t, identity.SignValidation(validation))
	raw, err := serializeSTValidationChecked(validation)
	require.NoError(t, err)
	parsed, err := parseSTValidation(raw)
	require.NoError(t, err)
	parsed.NodeID = validation.NodeID
	parsed.Raw = append([]byte(nil), raw...)
	a.SetValidationHistorian(&recheckingHistorian{
		stubHistorian: &stubHistorian{},
		validations:   []*consensus.Validation{parsed},
		quorum:        1,
		accepted:      true,
	})

	proof, err := a.CaptureValidationQuorumProof(seq, [32]byte(target))
	require.NoError(t, err)
	require.NotEmpty(t, proof.Validations[0].Manifest)

	// A restarted node may have no live mapping for the old ephemeral key after
	// rotation; the signed manifest retained in the proof remains sufficient.
	a.SetValidationSignerResolver(nil)
	require.NoError(t, a.RecheckValidationQuorumProof(proof))
}
