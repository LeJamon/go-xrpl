package list

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/protocol"
)

func TestApplyListCommitRejectsManifestStateChangedDuringVerification(t *testing.T) {
	t.Run("revocation", func(t *testing.T) {
		wireRaw, master, signing, masterPriv, signingPriv := stateTestManifest(t, 0x81, 0x82, 1, false)
		wire := []byte(base64.StdEncoding.EncodeToString(wireRaw))
		parsedInitial, err := manifest.Deserialize(wireRaw)
		if err != nil {
			t.Fatal(err)
		}
		if err := parsedInitial.Verify(); err != nil {
			t.Fatal(err)
		}
		revocation, _, _, _, _ := stateTestManifest(t, 0x81, 0, manifest.RevokedSequence, true)
		publisherCache := manifest.NewCache()
		agg, err := New(Config{
			PublisherKeys:      []PublisherKey{PublisherKey(master)},
			Threshold:          1,
			ValidatorManifests: manifest.NewCache(),
			PublisherManifests: publisherCache,
			Clock:              func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		})
		if err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		agg.beforeListCommit = func() {
			close(entered)
			<-release
		}
		parsedRevocation, err := manifest.Deserialize(revocation)
		if err != nil {
			t.Fatal(err)
		}
		blob, signature := stateTestList(t, signingPriv, 1, masterPriv, 0x90)
		if err := verifyBlobSignature(signing, blob, signature); err != nil {
			t.Fatal(err)
		}
		if _, d, err := parseBlob(blob); err != nil {
			t.Fatalf("parse blob: %s %v", d, err)
		}
		result := make(chan Disposition, 1)
		go func() {
			d, _, _ := agg.ApplyList(wire, blob, signature, 1, "test://")
			result <- d
		}()
		<-entered
		got := publisherCache.ApplyManifest(parsedRevocation)
		close(release)
		if got != manifest.Accepted {
			t.Fatalf("revocation ApplyManifest: got %s", got)
		}
		d := <-result
		if d != Untrusted {
			t.Fatalf("disposition: got %s want untrusted", d)
		}
		snapshot := agg.PublisherSnapshot()
		if snapshot[0].Sequence != 0 {
			t.Fatalf("revocation race resurrected sequence %d", snapshot[0].Sequence)
		}
	})

	t.Run("rotation", func(t *testing.T) {
		wireRaw, master, _, masterPriv, signingPriv := stateTestManifest(t, 0x83, 0x84, 1, false)
		wire := []byte(base64.StdEncoding.EncodeToString(wireRaw))
		rotated, _, _, _, _ := stateTestManifest(t, 0x83, 0x85, 2, false)
		publisherCache := manifest.NewCache()
		agg, err := New(Config{
			PublisherKeys:      []PublisherKey{PublisherKey(master)},
			Threshold:          1,
			ValidatorManifests: manifest.NewCache(),
			PublisherManifests: publisherCache,
			Clock:              func() time.Time { return time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC) },
		})
		if err != nil {
			t.Fatal(err)
		}
		entered := make(chan struct{})
		release := make(chan struct{})
		agg.beforeListCommit = func() {
			close(entered)
			<-release
		}
		parsedRotation, err := manifest.Deserialize(rotated)
		if err != nil {
			t.Fatal(err)
		}
		blob, signature := stateTestList(t, signingPriv, 1, masterPriv, 0x91)
		result := make(chan Disposition, 1)
		go func() {
			d, _, _ := agg.ApplyList(wire, blob, signature, 1, "test://")
			result <- d
		}()
		<-entered
		got := publisherCache.ApplyManifest(parsedRotation)
		close(release)
		if got != manifest.Accepted {
			t.Fatalf("rotation ApplyManifest: got %s", got)
		}
		d := <-result
		if d != Untrusted {
			t.Fatalf("disposition: got %s want untrusted", d)
		}
		snapshot := agg.PublisherSnapshot()
		if snapshot[0].Sequence != 0 {
			t.Fatalf("rotation race committed stale sequence %d", snapshot[0].Sequence)
		}
	})
}

func stateTestManifest(t *testing.T, masterSeed, signingSeed byte, sequence uint32, revoked bool) ([]byte, [33]byte, [33]byte, ed25519.PrivateKey, ed25519.PrivateKey) {
	t.Helper()
	masterPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{masterSeed}, ed25519.SeedSize))
	master32 := masterPriv.Public().(ed25519.PublicKey)
	var master [33]byte
	copy(master[:], append([]byte{0xED}, master32...))
	var signing [33]byte
	var signingPriv ed25519.PrivateKey
	if !revoked {
		signingPriv = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{signingSeed}, ed25519.SeedSize))
		signing32 := signingPriv.Public().(ed25519.PublicKey)
		copy(signing[:], append([]byte{0xED}, signing32...))
	}
	obj := map[string]any{
		"PublicKey": hex.EncodeToString(master[:]),
		"Sequence":  sequence,
	}
	if !revoked {
		obj["SigningPubKey"] = hex.EncodeToString(signing[:])
	}
	preimage := stateTestManifestPreimage(t, obj)
	if !revoked {
		obj["Signature"] = hex.EncodeToString(ed25519.Sign(signingPriv, preimage))
	}
	obj["MasterSignature"] = hex.EncodeToString(ed25519.Sign(masterPriv, preimage))
	encoded, err := binarycodec.Encode(obj)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return raw, master, signing, masterPriv, signingPriv
}

func stateTestManifestPreimage(t *testing.T, src map[string]any) []byte {
	t.Helper()
	filtered := make(map[string]any, len(src))
	for key, value := range src {
		if key != "Signature" && key != "MasterSignature" {
			filtered[key] = value
		}
	}
	encoded, err := binarycodec.Encode(filtered)
	if err != nil {
		t.Fatal(err)
	}
	body, err := hex.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	prefix := protocol.HashPrefixManifest()
	return append(append([]byte(nil), prefix[:]...), body...)
}

func stateTestList(t *testing.T, signingPriv ed25519.PrivateKey, sequence uint32, _ ed25519.PrivateKey, validatorSeed byte) ([]byte, []byte) {
	t.Helper()
	validatorPriv := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{validatorSeed}, ed25519.SeedSize))
	validator32 := validatorPriv.Public().(ed25519.PublicKey)
	var validator [33]byte
	copy(validator[:], append([]byte{0xED}, validator32...))
	body := struct {
		Sequence   uint32              `json:"sequence"`
		Expiration uint32              `json:"expiration"`
		Validators []map[string]string `json:"validators"`
	}{
		Sequence:   sequence,
		Expiration: uint32(time.Date(2026, 5, 3, 12, 0, 0, 0, time.UTC).Unix() - protocol.RippleEpochUnix),
		Validators: []map[string]string{{"validation_public_key": hex.EncodeToString(validator[:])}},
	}
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	blob := []byte(base64.StdEncoding.EncodeToString(jsonBytes))
	return blob, []byte(hex.EncodeToString(ed25519.Sign(signingPriv, jsonBytes)))
}
