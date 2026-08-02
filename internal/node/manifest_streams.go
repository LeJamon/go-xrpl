package node

import (
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// buildManifestEvent renders a rippled-shape manifestReceived event.
// Mirrors NetworkOPs::pubManifest (NetworkOPs.cpp:2229-2265): the
// canonical serialized blob is emitted as `manifest`, with the master
// signature always present and signing_key/signature/domain conditional
// on manifest presence.
func buildManifestEvent(m *manifest.Manifest) *rpc.ManifestEvent {
	if m == nil {
		return nil
	}
	master := m.MasterKey()
	masterEnc, _ := addresscodec.EncodeNodePublicKey(master[:])
	var signingEnc string
	if !m.Revoked() {
		signing := m.SigningKey()
		signingEnc, _ = addresscodec.EncodeNodePublicKey(signing[:])
	}
	masterSig, sig := m.Signatures()
	return rpc.NewManifestEvent(
		masterEnc,
		signingEnc,
		masterSig,
		sig,
		m.Domain(),
		upperHex(m.Serialized()),
		m.Sequence(),
	)
}

type manifestEventPublisher interface {
	PublishManifest(*rpc.ManifestEvent)
	GetSubscriberCount(types.SubscriptionType) int
}

func publishManifestIfSubscribed(publisher manifestEventPublisher, m *manifest.Manifest) {
	if publisher == nil || publisher.GetSubscriberCount(types.SubManifests) == 0 {
		return
	}
	publisher.PublishManifest(buildManifestEvent(m))
}
