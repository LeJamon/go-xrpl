package adaptor

import (
	"bytes"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/sha512half"
	"github.com/stretchr/testify/require"
)

const testValidatorSeed = "snoPBrXtMeMyMHUVTgbuqAfg1SUTb"

func TestValidatorIdentityCloseErasesOwnedSecret(t *testing.T) {
	tests := []struct {
		name string
		new  func(*testing.T) *ValidatorIdentity
	}{
		{
			name: "seed",
			new: func(t *testing.T) *ValidatorIdentity {
				identity, err := NewValidatorIdentity(testValidatorSeed)
				require.NoError(t, err)
				return identity
			},
		},
		{
			name: "token",
			new: func(t *testing.T) *ValidatorIdentity {
				fixture := newTokenFixture(t, 0x42, 7)
				identity, err := newValidatorIdentityFromToken(fixture.tokenBlock)
				require.NoError(t, err)
				return identity
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			identity := test.new(t)
			require.NotNil(t, identity.signingSecret)
			owned := identity.signingSecret.privateKey
			require.Len(t, owned, 32)
			require.False(t, allZero(owned))

			digest := sha512half.Sum([]byte("validator identity lifecycle"))
			signature, err := identity.Sign(digest[:])
			require.NoError(t, err)
			require.True(t, verify(identity.SigningKey[:], digest[:], signature))

			copyOfIdentity := *identity
			require.NoError(t, copyOfIdentity.Close())
			require.True(t, allZero(owned))
			require.Nil(t, identity.signingSecret.privateKey)
			_, err = identity.Sign(digest[:])
			require.ErrorIs(t, err, errNoValidatorKey)
			require.NoError(t, identity.Close())
		})
	}
}

func TestValidatorIdentityReplacementErasesOldSecret(t *testing.T) {
	identity, err := NewValidatorIdentity(testValidatorSeed)
	require.NoError(t, err)
	oldSecret := identity.signingSecret.privateKey

	replacement := append([]byte(nil), oldSecret...)
	require.NoError(t, identity.replaceSigningPrivateKey(replacement))
	require.True(t, allZero(oldSecret))

	ownedReplacement := identity.signingSecret.privateKey
	replacement[0] ^= 0xff
	digest := sha512half.Sum([]byte("replacement"))
	signature, err := identity.Sign(digest[:])
	require.NoError(t, err)
	require.True(t, verify(identity.SigningKey[:], digest[:], signature))

	require.Error(t, identity.replaceSigningPrivateKey(make([]byte, 32)))
	mismatched := make([]byte, 32)
	mismatched[31] = 2
	require.ErrorIs(t, identity.replaceSigningPrivateKey(mismatched), errSigningKeyMismatch)
	require.False(t, allZero(ownedReplacement))
	require.NoError(t, identity.Close())
	require.True(t, allZero(ownedReplacement))
}

func TestValidatorIdentitySignAndCloseAreRaceSafe(t *testing.T) {
	identity, err := NewValidatorIdentity(testValidatorSeed)
	require.NoError(t, err)
	owned := identity.signingSecret.privateKey
	digest := sha512half.Sum([]byte("concurrent close"))

	const goroutines = 16
	start := make(chan struct{})
	errs := make(chan error, goroutines)
	var workers sync.WaitGroup
	for range goroutines {
		workers.Add(1)
		go func() {
			defer workers.Done()
			<-start
			for range 64 {
				_, signErr := identity.Sign(digest[:])
				if signErr != nil && !errors.Is(signErr, errNoValidatorKey) {
					errs <- signErr
					return
				}
			}
		}()
	}

	close(start)
	require.NoError(t, identity.Close())
	workers.Wait()
	close(errs)
	for signErr := range errs {
		require.NoError(t, signErr)
	}
	require.True(t, allZero(owned))
}

func TestValidatorIdentityCloseWaitsForSigningReaders(t *testing.T) {
	identity, err := NewValidatorIdentity(testValidatorSeed)
	require.NoError(t, err)
	state := identity.signingSecret
	owned := state.privateKey
	state.mu.RLock()
	closed := make(chan error, 1)
	go func() {
		closed <- identity.Close()
	}()

	require.Eventually(t, func() bool {
		if state.mu.TryRLock() {
			state.mu.RUnlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond)
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a signing reader was active: %v", err)
	default:
	}

	state.mu.RUnlock()
	require.NoError(t, <-closed)
	require.True(t, allZero(owned))
}

func TestComponentsStopErasesIdentityAfterEngineStop(t *testing.T) {
	identity, err := NewValidatorIdentity(testValidatorSeed)
	require.NoError(t, err)
	owned := identity.signingSecret.privateKey
	digest := sha512half.Sum([]byte("component stop ordering"))
	stopErr := errors.New("engine stop failed")
	engine := &identityStopEngine{stopErr: stopErr}
	engine.stopCheck = func() error {
		_, signErr := identity.Sign(digest[:])
		return signErr
	}
	components := &Components{Engine: engine, identity: identity}

	err = components.Stop()
	require.ErrorIs(t, err, stopErr)
	require.NoError(t, engine.checkErr)
	require.True(t, allZero(owned))
	_, err = identity.Sign(digest[:])
	require.ErrorIs(t, err, errNoValidatorKey)
	require.ErrorIs(t, components.Stop(), stopErr)
}

type identityStopEngine struct {
	mockEngine
	stopCheck func() error
	checkErr  error
	stopErr   error
}

func (e *identityStopEngine) Stop() error {
	if e.stopCheck != nil {
		e.checkErr = e.stopCheck()
	}
	return e.stopErr
}

func allZero(value []byte) bool {
	return bytes.Equal(value, make([]byte, len(value)))
}
