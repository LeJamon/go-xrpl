package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPersistedValidatedTipRecoversAlreadyMissingProofAtEpochZero(t *testing.T) {
	f := newStateBaseRecertificationFixture(t)
	f.svc.validatedStateBaseMu.Lock()
	f.svc.validatedStateBaseProof = nil
	f.svc.validatedStateBaseCandidate = nil
	epoch := f.svc.stateBaseMutationEpoch
	f.svc.validatedStateBaseMu.Unlock()
	require.Zero(t, epoch)

	require.NoError(t, f.svc.persistValidatedTip(t.Context(), f.validated))
	require.Eventually(t, func() bool {
		proof, found := f.svc.currentValidatedStateBaseProof()
		return found && proof.ledgerHash == f.validated.Hash()
	}, 2*time.Second, 5*time.Millisecond)
}
