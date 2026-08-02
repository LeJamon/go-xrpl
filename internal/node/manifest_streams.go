package node

import (
	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

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

// serverStatusSnapshot is the diff key for the pubServer emit gate.
// Two snapshots being equal means none of the fields rippled keys on
// (NetworkOPs.cpp:2278-2295 ServerFeeSummary::operator==) have moved,
// so the corresponding serverStatus event is suppressed.
