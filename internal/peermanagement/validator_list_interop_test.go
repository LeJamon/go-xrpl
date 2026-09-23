//go:build cgo && docker

package peermanagement

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/peertls"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

func TestValidatorListCollection_Interop_RippledDocker(t *testing.T) {
	if os.Getenv("PEERTLS_DOCKER_INTEROP") == "" || os.Getenv("PEERTLS_VALIDATOR_LIST_INTEROP") == "" {
		t.Skip("PEERTLS_DOCKER_INTEROP and PEERTLS_VALIDATOR_LIST_INTEROP not set")
	}

	publisher := newInteropValidatorPublisher(t)
	config := rippledInteropConfig + "\n[validator_list_keys]\n" +
		hex.EncodeToString(publisher.masterPub[:]) + "\n"
	node := startRippledInteropWithConfig(t, config)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	sender, senderEvents, senderDone := connectInteropPeer(t, ctx, node.addr, 1)
	t.Cleanup(func() {
		_ = sender.Close()
		waitInteropPeer(t, senderDone)
		drainInteropEvents(senderEvents)
	})
	witness, witnessEvents, witnessDone := connectInteropPeer(t, ctx, node.addr, 2)
	t.Cleanup(func() {
		_ = witness.Close()
		waitInteropPeer(t, witnessDone)
		drainInteropEvents(witnessEvents)
	})

	blob, signature := publisher.signList(t, 7, time.Now().Add(24*time.Hour))
	collection := &message.ValidatorListCollection{
		Version:  2,
		Manifest: append([]byte(nil), publisher.manifestB64...),
		Blobs: []message.ValidatorBlobInfo{{
			Manifest:  append([]byte(nil), publisher.manifestB64...),
			Blob:      blob,
			Signature: signature,
		}},
	}
	wire, err := message.EncodeFrame(collection)
	require.NoError(t, err)
	require.NoError(t, sender.Send(wire))

	received := waitForValidatorCollection(t, witnessEvents, 30*time.Second, publisher.manifestB64, blob)
	require.Equal(t, uint32(2), received.Version)
	require.Equal(t, publisher.manifestB64, received.Manifest)
	require.Len(t, received.Blobs, 1)
	require.True(t, received.Blobs[0].HasManifest())
	require.Equal(t, publisher.manifestB64, received.Blobs[0].Manifest)
	require.Equal(t, blob, received.Blobs[0].Blob)
	require.Equal(t, signature, received.Blobs[0].Signature)

	// Apply the exact relayed collection through the Go trust and signature
	// state machine. This checks the signed v1 list document carried by the
	// v2 wire envelope, rather than only checking protobuf field equality.
	agg, err := validatorlist.New(validatorlist.Config{
		PublisherKeys:      []validatorlist.PublisherKey{validatorlist.PublisherKey(publisher.masterPub)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
	})
	require.NoError(t, err)
	dispositions, publisherKey, maxSequence := agg.ApplyCollection(received, "interop://rippled")
	require.Equal(t, []validatorlist.Disposition{validatorlist.Accepted}, dispositions)
	require.Equal(t, validatorlist.PublisherKey(publisher.masterPub), publisherKey)
	require.Equal(t, uint32(7), maxSequence)

	// A fresh sequence with a corrupted blob signature must not be relayed by
	// the rc1 peer. Using a new sequence keeps duplicate suppression from
	// explaining the negative result.
	blob8, signature8 := publisher.signList(t, 8, time.Now().Add(24*time.Hour))
	correct8 := cloneValidatorListCollection(received)
	correct8.Blobs[0].Blob = blob8
	correct8.Blobs[0].Signature = signature8
	bad := cloneValidatorListCollection(correct8)
	sigBytes, err := hex.DecodeString(string(bad.Blobs[0].Signature))
	require.NoError(t, err)
	sigBytes[0] ^= 0xff
	bad.Blobs[0].Signature = []byte(hex.EncodeToString(sigBytes))
	badWire, err := message.EncodeFrame(bad)
	require.NoError(t, err)
	require.NoError(t, sender.Send(badWire))
	waitForNoValidatorCollection(t, witnessEvents, 8*time.Second, bad)

	// The valid sequence 8 must still be accepted and relayed after the bad
	// attempt. This proves the malformed signature did not poison rc1 state.
	correctWire, err := message.EncodeFrame(correct8)
	require.NoError(t, err)
	require.NoError(t, sender.Send(correctWire))
	received8 := waitForValidatorCollection(t, witnessEvents, 30*time.Second, publisher.manifestB64, blob8)
	require.Equal(t, correct8.Version, received8.Version)
	require.Equal(t, correct8.Manifest, received8.Manifest)
	require.Len(t, received8.Blobs, 1)
	require.True(t, received8.Blobs[0].HasManifest())
	require.Equal(t, correct8.Blobs[0].Manifest, received8.Blobs[0].Manifest)
	require.Equal(t, blob8, received8.Blobs[0].Blob)
	require.Equal(t, signature8, received8.Blobs[0].Signature)
	dispositions, publisherKey, maxSequence = agg.ApplyCollection(received8, "interop://rippled")
	require.Equal(t, []validatorlist.Disposition{validatorlist.Accepted}, dispositions)
	require.Equal(t, validatorlist.PublisherKey(publisher.masterPub), publisherKey)
	require.Equal(t, uint32(8), maxSequence)
}

type interopValidatorPublisher struct {
	masterPub   [33]byte
	ephPriv     ed25519.PrivateKey
	manifestB64 []byte
}

func newInteropValidatorPublisher(t *testing.T) interopValidatorPublisher {
	t.Helper()
	const masterIndex = 0x19060001
	const signingIndex = 0x19060002
	masterPublic, _ := interopKeypair("master", masterIndex)
	_, signingPrivate := interopKeypair("signing", signingIndex)
	var masterPub [33]byte
	copy(masterPub[:], masterPublic)
	raw := buildInteropManifest(t, 1, masterIndex, signingIndex, false)
	return interopValidatorPublisher{
		masterPub:   masterPub,
		ephPriv:     signingPrivate,
		manifestB64: []byte(base64.StdEncoding.EncodeToString(raw)),
	}
}

func (p interopValidatorPublisher) signList(t *testing.T, sequence uint32, expiration time.Time) ([]byte, []byte) {
	t.Helper()
	validator := interopValidatorKey(0xC3)
	type validatorEntry struct {
		ValidationPublicKey string `json:"validation_public_key"`
	}
	type listBody struct {
		Sequence   uint32           `json:"sequence"`
		Expiration uint32           `json:"expiration"`
		Validators []validatorEntry `json:"validators"`
	}
	body := listBody{
		Sequence:   sequence,
		Expiration: uint32(expiration.Unix() - protocol.RippleEpochUnix),
		Validators: []validatorEntry{{ValidationPublicKey: hex.EncodeToString(validator[:])}},
	}
	jsonBytes, err := json.Marshal(body)
	require.NoError(t, err)
	return []byte(base64.StdEncoding.EncodeToString(jsonBytes)), []byte(hex.EncodeToString(ed25519.Sign(p.ephPriv, jsonBytes)))
}

func interopValidatorKey(seed byte) [33]byte {
	pub, _ := interopKeypair("validator-list", uint32(seed))
	var key [33]byte
	copy(key[:], pub)
	return key
}

func startRippledInteropWithConfig(t *testing.T, config string) *rippledInteropNode {
	t.Helper()
	cid := startRippledInteropContainer(t, config, "-p", "0:51235")
	portOutput, err := exec.Command("docker", "port", cid, "51235").Output()
	require.NoError(t, err)
	host, port := parseDockerPort(t, string(portOutput))
	addr := net.JoinHostPort(host, port)
	waitForRippledPeerPort(t, cid, addr)
	return &rippledInteropNode{cid: cid, addr: addr}
}

func connectInteropPeer(t *testing.T, ctx context.Context, addr string, id PeerID) (*Peer, chan Event, <-chan error) {
	t.Helper()
	identity, err := NewIdentity()
	require.NoError(t, err)
	cert, key, err := identity.TLSCertificatePEM()
	require.NoError(t, err)
	endpoint, err := ParseEndpoint(addr)
	require.NoError(t, err)
	events := make(chan Event, 128)
	peer := NewPeer(id, endpoint, false, identity, events)
	peer.handshakeCfg = DefaultHandshakeConfig()
	peer.handshakeCfg.NetworkID = 1
	require.NoError(t, peer.Connect(ctx, PeerConfig{PeerTLSConfig: &peertls.Config{CertPEM: cert, KeyPEM: key}}))
	require.Equal(t, "XRPL/2.3", peer.ProtocolVersion())
	done := make(chan error, 1)
	go func() { done <- peer.Run(ctx) }()
	return peer, events, done
}

func waitInteropPeer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Error("interop peer did not stop")
	}
}

func drainInteropEvents(events <-chan Event) {
	for {
		select {
		case event := <-events:
			event.release()
		default:
			return
		}
	}
}

func waitForValidatorCollection(t *testing.T, events <-chan Event, timeout time.Duration, wantManifest, wantBlob []byte) *message.ValidatorListCollection {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.MessageType != message.TypeValidatorListCollection {
				event.release()
				continue
			}
			decoded, err := message.Decode(event.MessageType, event.Payload)
			event.release()
			require.NoError(t, err)
			collection, ok := decoded.(*message.ValidatorListCollection)
			require.True(t, ok)
			if !bytes.Equal(collection.Manifest, wantManifest) || len(collection.Blobs) == 0 ||
				!bytes.Equal(collection.Blobs[0].Blob, wantBlob) {
				continue
			}
			return collection
		case <-timer.C:
			t.Fatal("timed out waiting for relayed validator-list collection")
			return nil
		}
	}
}

func waitForNoValidatorCollection(t *testing.T, events <-chan Event, timeout time.Duration, bad *message.ValidatorListCollection) {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case event := <-events:
			if event.MessageType == message.TypeValidatorListCollection {
				decoded, err := message.Decode(event.MessageType, event.Payload)
				event.release()
				if err == nil {
					if collection, ok := decoded.(*message.ValidatorListCollection); ok && validatorListCollectionPayloadEqual(collection, bad) {
						t.Fatal("rippled relayed a collection with an invalid blob signature")
					}
				}
				continue
			}
			event.release()
		case <-timer.C:
			return
		}
	}
}

func validatorListCollectionPayloadEqual(left, right *message.ValidatorListCollection) bool {
	if left == nil || right == nil || left.Version != right.Version ||
		!bytes.Equal(left.Manifest, right.Manifest) || len(left.Blobs) != len(right.Blobs) {
		return false
	}
	for i := range left.Blobs {
		if left.Blobs[i].HasManifest() != right.Blobs[i].HasManifest() ||
			!bytes.Equal(left.Blobs[i].Manifest, right.Blobs[i].Manifest) ||
			!bytes.Equal(left.Blobs[i].Blob, right.Blobs[i].Blob) {
			return false
		}
	}
	return true
}

func cloneValidatorListCollection(source *message.ValidatorListCollection) *message.ValidatorListCollection {
	clone := &message.ValidatorListCollection{
		Version:  source.Version,
		Manifest: append([]byte(nil), source.Manifest...),
		Blobs:    make([]message.ValidatorBlobInfo, len(source.Blobs)),
	}
	for i, blob := range source.Blobs {
		clone.Blobs[i] = message.ValidatorBlobInfo{
			Manifest:  append([]byte(nil), blob.Manifest...),
			Blob:      append([]byte(nil), blob.Blob...),
			Signature: append([]byte(nil), blob.Signature...),
		}
	}
	return clone
}
