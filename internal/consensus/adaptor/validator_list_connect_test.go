package adaptor

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/LeJamon/go-xrpl/crypto/secp256k1"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	validatorlist "github.com/LeJamon/go-xrpl/internal/validator/list"
	"github.com/LeJamon/go-xrpl/protocol"
	"github.com/stretchr/testify/require"
)

type connectValidatorListSender struct {
	peers []uint64
}

func (*connectValidatorListSender) ActivePeers() []uint64 {
	return nil
}

func (s *connectValidatorListSender) SendCollection(peerID uint64, _ []byte, _ []validatorlist.BroadcastBlob, _ uint32) error {
	s.peers = append(s.peers, peerID)
	return nil
}

func TestRouterHandlePeerConnectSendsCachedListsOnlyToInboundPeers(t *testing.T) {
	peerSender := &fakeManifestSender{
		peers: []peermanagement.PeerInfo{
			{ID: 42, Inbound: true},
			{ID: 43, Inbound: false},
		},
	}
	router, _, _ := routerWithCache(t, peerSender, 0, 0)

	fixture := newTokenFixture(t, 0x78, 3)
	identity, err := newValidatorIdentityFromToken(fixture.tokenBlock)
	require.NoError(t, err)
	defer identity.Close()

	agg, err := validatorlist.New(validatorlist.Config{
		PublisherKeys:      []validatorlist.PublisherKey{validatorlist.PublisherKey(identity.MasterKey)},
		Threshold:          1,
		ValidatorManifests: manifest.NewCache(),
		PublisherManifests: manifest.NewCache(),
		Clock:              time.Now,
	})
	require.NoError(t, err)
	manifestB64 := []byte(base64.StdEncoding.EncodeToString(identity.SerializedMfst))
	var validator [33]byte
	validator[0] = 0xED
	validator[1] = 0x79
	blob, signature := signedConnectList(t, fixture, validator)
	disposition, _, _ := agg.ApplyList(manifestB64, blob, signature, 1, "site://")
	require.Equal(t, validatorlist.Accepted, disposition)

	listSender := &connectValidatorListSender{}
	agg.SetBroadcaster(listSender)
	router.validatorList = agg

	router.handlePeerConnect(42)
	router.handlePeerConnect(43)
	require.Equal(t, []uint64{42}, listSender.peers)
}

func signedConnectList(t *testing.T, fixture tokenFixture, validator [33]byte) ([]byte, []byte) {
	t.Helper()
	type validatorEntry struct {
		ValidationPublicKey string `json:"validation_public_key"`
	}
	type listBody struct {
		Sequence   uint32           `json:"sequence"`
		Expiration uint32           `json:"expiration"`
		Validators []validatorEntry `json:"validators"`
	}
	expiration := uint32(time.Now().Add(24*time.Hour).Unix() - protocol.RippleEpochUnix)
	body, err := json.Marshal(listBody{
		Sequence:   1,
		Expiration: expiration,
		Validators: []validatorEntry{{ValidationPublicKey: hex.EncodeToString(validator[:])}},
	})
	require.NoError(t, err)
	signature, err := (secp256k1.Algorithm{}).Sign(string(body), hex.EncodeToString(fixture.signingSec[:]))
	require.NoError(t, err)
	return []byte(base64.StdEncoding.EncodeToString(body)), []byte(signature)
}
