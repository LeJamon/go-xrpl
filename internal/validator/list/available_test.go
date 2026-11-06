package list_test

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestAvailableListUnavailableAndVersionErrors(t *testing.T) {
	pub := newPublisher(t, 0x91, 0x92)
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
	})
	require.NoError(t, err)

	key := hex.EncodeToString(pub.masterPub[:])
	got, ok := agg.AvailableList(key, 1)
	require.False(t, ok)
	require.Nil(t, got)

	got, ok = agg.AvailableList("not-hex", 1)
	require.False(t, ok)
	require.Nil(t, got)

	now := fixedClock()()
	validator := derivedValidatorKey(0x96)
	blob, signature := pub.signList(t, 1, 0, now.Add(time.Hour).Unix(), [][33]byte{validator})
	require.Equal(t, list.Accepted, mustApply(t, agg, pub.manifestB64, blob, signature))

	got, ok = agg.AvailableList(key, 3)
	require.True(t, ok)
	require.Nil(t, got)

	unknown := derivedValidatorKey(0x93)
	unknownKey := hex.EncodeToString(unknown[:])
	got, ok = agg.AvailableList(unknownKey, 3)
	require.False(t, ok)
	require.Nil(t, got)

	expiredBlob, expiredSignature := pub.signList(t, 2, 0, now.Add(-time.Hour).Unix(), [][33]byte{validator})
	require.Equal(t, list.Expired, mustApply(t, agg, pub.manifestB64, expiredBlob, expiredSignature))
	got, ok = agg.AvailableList(key, 1)
	require.False(t, ok)
	require.Nil(t, got)

	revokedManifest := base64.StdEncoding.EncodeToString(buildRevocation(t, pub.masterPub, pub.masterPriv))
	disposition, _, _ := agg.ApplyList([]byte(revokedManifest), []byte("ignored"), []byte("ignored"), 1, "test://")
	require.Equal(t, list.Untrusted, disposition)
	got, ok = agg.AvailableList(key, 1)
	require.False(t, ok)
	require.Nil(t, got)
}

func TestAvailableListCurrentAndPendingWireShapes(t *testing.T) {
	pub := newPublisher(t, 0x94, 0x95)
	now := fixedClock()()
	mutableNow := now
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              func() time.Time { return mutableNow },
	})
	require.NoError(t, err)

	key := hex.EncodeToString(pub.masterPub[:])
	validator := derivedValidatorKey(0x97)
	currentBlob, currentSignature := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{validator})
	require.Equal(t, list.Accepted, mustApply(t, agg, pub.manifestB64, currentBlob, currentSignature))

	body, ok := agg.AvailableList(key, 1)
	require.True(t, ok)
	require.Equal(t, key, body["public_key"])
	require.Equal(t, uint32(1), body["version"])
	require.Equal(t, string(pub.manifestB64), body["manifest"])
	require.Equal(t, string(currentBlob), body["blob"])
	require.Equal(t, string(currentSignature), body["signature"])
	require.NotContains(t, body, "blobs_v2")

	futureBlob, futureSignature := pub.signList(t, 2, now.Add(time.Hour).Unix(), now.Add(48*time.Hour).Unix(), [][33]byte{validator})
	require.Equal(t, list.Pending, mustApplyVersion(t, agg, pub.manifestB64, futureBlob, futureSignature, 2))

	v1, ok := agg.AvailableList(key, 1)
	require.True(t, ok)
	require.Equal(t, uint32(1), v1["version"])
	require.Equal(t, string(currentBlob), v1["blob"])
	require.Equal(t, string(currentSignature), v1["signature"])
	require.NotContains(t, v1, "blobs_v2")

	v2, ok := agg.AvailableList(key, 2)
	require.True(t, ok)
	require.Equal(t, uint32(2), v2["version"])
	require.NotContains(t, v2, "blob")
	require.NotContains(t, v2, "signature")
	blobs, ok := v2["blobs_v2"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, blobs, 2)
	require.Equal(t, string(currentBlob), blobs[0]["blob"])
	require.Equal(t, string(currentSignature), blobs[0]["signature"])
	require.Equal(t, string(futureBlob), blobs[1]["blob"])
	require.Equal(t, string(futureSignature), blobs[1]["signature"])
	require.NotContains(t, blobs[0], "manifest")
	require.NotContains(t, blobs[1], "manifest")

	// Each result is a detached snapshot; mutations by the HTTP encoder or
	// another caller cannot change the retained publisher state.
	blobs[0]["blob"] = "mutated"
	v2["manifest"] = "mutated"
	v2Again, ok := agg.AvailableList(key, 2)
	require.True(t, ok)
	blobsAgain := v2Again["blobs_v2"].([]map[string]any)
	require.Equal(t, string(currentBlob), blobsAgain[0]["blob"])
	require.Equal(t, string(pub.manifestB64), v2Again["manifest"])

	mutableNow = now.Add(time.Hour + time.Minute)
	agg.Tick()
	rotated, ok := agg.AvailableList(key, 2)
	require.True(t, ok)
	blobs, ok = rotated["blobs_v2"].([]map[string]any)
	require.True(t, ok)
	require.Len(t, blobs, 1)
	require.Equal(t, string(futureBlob), blobs[0]["blob"])
	require.Equal(t, string(futureSignature), blobs[0]["signature"])
}

func TestAvailableListPreservesDistinctBlobManifest(t *testing.T) {
	pub := newPublisher(t, 0xA1, 0xA2)
	now := fixedClock()()
	agg, err := list.New(list.Config{
		PublisherKeys:      []list.PublisherKey{list.PublisherKey(pub.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              fixedClock(),
	})
	require.NoError(t, err)
	validator := derivedValidatorKey(0xA3)
	currentBlob, currentSignature := pub.signList(t, 1, 0, now.Add(24*time.Hour).Unix(), [][33]byte{validator})
	require.Equal(t, list.Accepted, mustApply(t, agg, pub.manifestB64, currentBlob, currentSignature))

	ephPub32, ephPriv := deterministicKey(0xA4)
	var ephPub [33]byte
	copy(ephPub[:], append([]byte{0xED}, ephPub32...))
	rotatedManifest := base64.StdEncoding.EncodeToString(
		buildManifest(t, pub.masterPub, pub.masterPriv, ephPub, ephPriv, 2),
	)
	rotatedBlob, rotatedSignature := signListWithEphemeral(
		t, ephPriv, 2, now.Unix(), now.Add(48*time.Hour).Unix(), validator)
	dispositions, _, _ := agg.ApplyCollection(&message.ValidatorListCollection{
		Version:  2,
		Manifest: pub.manifestB64,
		Blobs: []message.ValidatorBlobInfo{{
			Manifest:  []byte(rotatedManifest),
			Blob:      rotatedBlob,
			Signature: rotatedSignature,
		}},
	}, "peer://")
	require.Equal(t, []list.Disposition{list.Accepted}, dispositions)

	key := hex.EncodeToString(pub.masterPub[:])
	v1, ok := agg.AvailableList(key, 1)
	require.True(t, ok)
	require.Equal(t, rotatedManifest, v1["manifest"])

	v2, ok := agg.AvailableList(key, 2)
	require.True(t, ok)
	require.Equal(t, string(pub.manifestB64), v2["manifest"])
	blobs := v2["blobs_v2"].([]map[string]any)
	require.Len(t, blobs, 1)
	require.Equal(t, rotatedManifest, blobs[0]["manifest"])
}

func signListWithEphemeral(t *testing.T, private ed25519.PrivateKey, sequence uint32, effective, expiration int64, validator [33]byte) ([]byte, []byte) {
	t.Helper()
	body := map[string]any{
		"sequence":   sequence,
		"expiration": uint32(expiration - protocol.RippleEpochUnix),
		"effective":  uint32(effective - protocol.RippleEpochUnix),
		"validators": []map[string]string{{"validation_public_key": hex.EncodeToString(validator[:])}},
	}
	jsonBytes, err := json.Marshal(body)
	require.NoError(t, err)
	return []byte(base64.StdEncoding.EncodeToString(jsonBytes)), []byte(hex.EncodeToString(ed25519.Sign(private, jsonBytes)))
}

func mustApplyVersion(t *testing.T, agg *list.Aggregator, publisherManifest, blob, signature []byte, version uint32) list.Disposition {
	t.Helper()
	disposition, _, _ := agg.ApplyList(publisherManifest, blob, signature, version, "test://")
	return disposition
}
