package adaptor

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"database/sql"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/stretchr/testify/require"
)

type recordingManifestStore struct {
	stored     manifest.StoredManifests
	events     []string
	replaceErr error
	closeErr   error
	onReplace  func()
}

func (s *recordingManifestStore) Load(context.Context) (manifest.StoredManifests, error) {
	return manifest.StoredManifests{}, nil
}

func (s *recordingManifestStore) Replace(_ context.Context, stored manifest.StoredManifests) error {
	s.events = append(s.events, "replace")
	if s.onReplace != nil {
		s.onReplace()
	}
	s.stored = stored
	return s.replaceErr
}

func (s *recordingManifestStore) Close() error {
	s.events = append(s.events, "close")
	return s.closeErr
}

func TestComponentsStopPersistsRoleFilteredManifestsAndReportsFailures(t *testing.T) {
	validatorCache := manifest.NewCache()
	publisherCache := manifest.NewCache()

	unlisted := applyTokenManifest(t, validatorCache, 0x41, 0x42, 1)
	validatorRevocation := revokedManifest(t, 0x43)
	require.Equal(t, manifest.Accepted, validatorCache.ApplyManifest(validatorRevocation))

	configured := applyTokenManifest(t, publisherCache, 0x44, 0x45, 1)
	_ = applyTokenManifest(t, publisherCache, 0x46, 0x47, 1)
	publisherRevocation := revokedManifest(t, 0x48)
	require.Equal(t, manifest.Accepted, publisherCache.ApplyManifest(publisherRevocation))

	saveErr := errors.New("save failed")
	closeErr := errors.New("close failed")
	engineStopped := false
	store := &recordingManifestStore{
		replaceErr: saveErr,
		closeErr:   closeErr,
		onReplace: func() {
			require.True(t, engineStopped, "manifest snapshot ran before the engine stopped")
		},
	}
	engine := &lifecycleTestEngine{stopCheck: func() { engineStopped = true }}
	components := &Components{
		Engine:                  engine,
		ValidatorManifests:      validatorCache,
		PublisherManifests:      publisherCache,
		configuredPublisherKeys: [][33]byte{configured},
		manifestStore:           store,
	}

	err := components.Stop()
	require.ErrorIs(t, err, saveErr)
	require.ErrorIs(t, err, closeErr)
	require.Equal(t, []string{"replace", "close"}, store.events)
	require.NotContains(t, manifestMasters(t, store.stored.Validators), unlisted)
	require.Contains(t, manifestMasters(t, store.stored.Validators), validatorRevocation.MasterKey())
	require.Contains(t, manifestMasters(t, store.stored.Publishers), configured)
	require.Contains(t, manifestMasters(t, store.stored.Publishers), publisherRevocation.MasterKey())
	require.Len(t, store.stored.Publishers, 2)

	require.ErrorIs(t, components.Stop(), saveErr)
	require.Equal(t, []string{"replace", "close"}, store.events)
}

func TestRestoreManifestsRejectsInvalidRowsAndKeepsRevocation(t *testing.T) {
	revocation := revokedManifest(t, 0x49)
	cache := manifest.NewCache()
	restoreManifests("validator", cache, [][]byte{{0xff}, revocation.Serialized()})
	require.True(t, cache.Revoked(revocation.MasterKey()))
}

func TestSeedLocalManifestToleratesPersistedKeyCollisions(t *testing.T) {
	tests := []struct {
		name      string
		persisted []byte
		local     []byte
		want      manifest.Disposition
	}{
		{
			name:      "master already used as signing key",
			persisted: buildWireManifest(t, 1, 0x51, 0x52),
			local:     buildWireManifest(t, 1, 0x52, 0x53),
			want:      manifest.BadMasterKey,
		},
		{
			name:      "signing key already used",
			persisted: buildWireManifest(t, 1, 0x54, 0x55),
			local:     buildWireManifest(t, 1, 0x56, 0x55),
			want:      manifest.BadEphemeralKey,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			persisted, err := manifest.Deserialize(test.persisted)
			require.NoError(t, err)
			local, err := manifest.Deserialize(test.local)
			require.NoError(t, err)

			cache := manifest.NewCache()
			require.Equal(t, manifest.Accepted, cache.ApplyManifest(persisted))
			require.Equal(t, test.want, cache.ApplyManifest(local))
			require.NoError(t, seedLocalManifest(cache, local))
		})
	}

	invalidWire := buildWireManifest(t, 1, 0x57, 0x58)
	invalidWire[len(invalidWire)-1] ^= 1
	invalid, err := manifest.Deserialize(invalidWire)
	require.NoError(t, err)
	require.ErrorContains(t, seedLocalManifest(manifest.NewCache(), invalid), "disposition=invalid")
}

func TestNewFromConfigFailsWhenManifestStoreCannotLoad(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "manifests.db"))
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE ValidatorManifests (WrongColumn BLOB NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, db.Close())

	components, err := NewFromConfig(context.Background(), &config.Config{DatabasePath: dir}, nil, nil, nil)
	require.Nil(t, components)
	require.ErrorContains(t, err, "load manifest store")
	require.ErrorContains(t, err, "RawData")
}

func TestNewFromConfigRejectsInvalidManifestCacheLimit(t *testing.T) {
	value := 49
	components, err := NewFromConfig(context.Background(), &config.Config{
		DatabasePath: t.TempDir(),
		Overlay:      config.OverlayConfig{MaxUntrustedCount: &value},
	}, nil, nil, nil)
	require.Nil(t, components)
	require.ErrorContains(t, err, "validate overlay config")
	require.ErrorContains(t, err, "max_untrusted_count")
}

func applyTokenManifest(t *testing.T, cache *manifest.Cache, masterSeed, signingSeed byte, sequence uint32) [33]byte {
	t.Helper()
	fixture := newTokenFixtureWithSeeds(t, masterSeed, signingSeed, sequence)
	identity, err := newValidatorIdentityFromToken(fixture.tokenBlock)
	require.NoError(t, err)
	require.Equal(t, manifest.Accepted, cache.ApplyManifest(identity.Manifest))
	return identity.MasterKey
}

func revokedManifest(t *testing.T, seed byte) *manifest.Manifest {
	t.Helper()
	private := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
	public := private.Public().(ed25519.PublicKey)
	master := append([]byte{0xed}, public...)
	fields := map[string]any{
		"PublicKey": hex.EncodeToString(master),
		"Sequence":  manifest.RevokedSequence,
	}
	fields["MasterSignature"] = hex.EncodeToString(ed25519.Sign(private, manifestSigningPreimage(t, fields)))
	wireHex, err := binarycodec.Encode(fields)
	require.NoError(t, err)
	wire, err := hex.DecodeString(wireHex)
	require.NoError(t, err)
	parsed, err := manifest.Deserialize(wire)
	require.NoError(t, err)
	return parsed
}

func manifestMasters(t *testing.T, rows [][]byte) [][33]byte {
	t.Helper()
	out := make([][33]byte, 0, len(rows))
	for _, row := range rows {
		parsed, err := manifest.Deserialize(row)
		require.NoError(t, err)
		out = append(out, parsed.MasterKey())
	}
	return out
}
