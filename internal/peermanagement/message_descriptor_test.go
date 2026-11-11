package peermanagement

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/cluster"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/proto"
	"github.com/LeJamon/go-xrpl/internal/peermanagement/resource"
)

// Message types the classification pins cover: every wire type plus codes
// no descriptor knows about.
var (
	unlistedMessageTypes = []message.MessageType{0, 1, 4, 99, 65535}
	allWireMessageTypes  = []message.MessageType{
		message.TypeManifests,
		message.TypePing,
		message.TypeCluster,
		message.TypeEndpoints,
		message.TypeTransaction,
		message.TypeGetLedger,
		message.TypeLedgerData,
		message.TypeProposeLedger,
		message.TypeStatusChange,
		message.TypeHaveSet,
		message.TypeValidation,
		message.TypeGetObjects,
		message.TypeSquelch,
		message.TypeValidatorListCollection,
		message.TypeProofPathReq,
		message.TypeProofPathResponse,
		message.TypeReplayDeltaReq,
		message.TypeReplayDeltaResponse,
		message.TypeHaveTransactions,
		message.TypeTransactions,
	}
)

func classifiedMessageTypes() []message.MessageType {
	return append(append([]message.MessageType(nil), allWireMessageTypes...), unlistedMessageTypes...)
}

type messageClassification struct {
	lane     string
	outbound OutboundSendClass
	traffic  TrafficCategory
}

var wantMessageClassification = map[message.MessageType]messageClassification{
	message.TypeManifests:               {"manifests", OutboundClassOrdinary, CategoryManifests},
	message.TypePing:                    {"general", OutboundClassControl, CategoryBase},
	message.TypeCluster:                 {"general", OutboundClassOrdinary, CategoryCluster},
	message.TypeEndpoints:               {"general", OutboundClassOrdinary, CategoryOverlay},
	message.TypeTransaction:             {"general", OutboundClassOrdinary, CategoryTransaction},
	message.TypeGetLedger:               {"general", OutboundClassOrdinary, CategoryLedgerData},
	message.TypeLedgerData:              {"acquisition", OutboundClassOrdinary, CategoryLedgerData},
	message.TypeProposeLedger:           {"consensus", OutboundClassConsensus, CategoryProposal},
	message.TypeStatusChange:            {"consensus-control", OutboundClassControl, CategoryUnknown},
	message.TypeHaveSet:                 {"consensus-control", OutboundClassControl, CategoryUnknown},
	message.TypeValidation:              {"consensus", OutboundClassConsensus, CategoryValidation},
	message.TypeGetObjects:              {"acquisition", OutboundClassOrdinary, CategoryUnknown},
	message.TypeSquelch:                 {"general", OutboundClassControl, CategorySquelch},
	message.TypeValidatorListCollection: {"consensus", OutboundClassConsensus, CategoryValidatorList},
	message.TypeProofPathReq:            {"general", OutboundClassOrdinary, CategoryUnknown},
	message.TypeProofPathResponse:       {"acquisition", OutboundClassOrdinary, CategoryUnknown},
	message.TypeReplayDeltaReq:          {"general", OutboundClassOrdinary, CategoryUnknown},
	message.TypeReplayDeltaResponse:     {"acquisition", OutboundClassOrdinary, CategoryUnknown},
	message.TypeHaveTransactions:        {"general", OutboundClassOrdinary, CategoryUnknown},
	message.TypeTransactions:            {"general", OutboundClassOrdinary, CategoryTransaction},
}

var unlistedMessageClassification = messageClassification{"general", OutboundClassOrdinary, CategoryUnknown}

func expectedClassification(t message.MessageType) messageClassification {
	if want, ok := wantMessageClassification[t]; ok {
		return want
	}
	return unlistedMessageClassification
}

// TestMessageClassificationPinned pins the per-type peer lane, outbound
// send class and traffic category for every wire type and for unlisted
// type codes.
func TestMessageClassificationPinned(t *testing.T) {
	for _, msgType := range classifiedMessageTypes() {
		t.Run(fmt.Sprintf("%s/%d", msgType, msgType), func(t *testing.T) {
			want := expectedClassification(msgType)
			assert.Equal(t, want.lane, observePeerLane(t, msgType), "peer lane")

			frame, err := message.BuildWireMessage(msgType, nil)
			require.NoError(t, err)
			assert.Equal(t, want.outbound, outboundClassForFrame(frame), "outbound class")
			assert.Equal(t, want.traffic, CategorizeMessage(msgType), "traffic category")
		})
	}
	assert.Equal(t, OutboundClassOrdinary, outboundClassForFrame([]byte{0xff}), "undecodable frame")
}

func observePeerLane(t *testing.T, msgType message.MessageType) string {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)
	events := make(chan Event, 1)
	manifests := make(chan *InboundMessage, 1)
	consensus := make(chan Event, 1)
	control := make(chan Event, 1)
	acquisition := make(chan Event, 1)
	peer := NewPeer(PeerID(1), Endpoint{Host: "127.0.0.1", Port: 1}, false, id, events)
	peer.SetManifestMessages(manifests)
	peer.SetConsensusEvents(consensus)
	peer.SetConsensusControlEvents(control)
	peer.SetAcquisitionEvents(acquisition)

	require.True(t, peer.dispatchEvent(Event{Type: EventMessageReceived, PeerID: 1, MessageType: msgType}))
	lanes := map[string]int{
		"general":           len(events),
		"manifests":         len(manifests),
		"consensus":         len(consensus),
		"consensus-control": len(control),
		"acquisition":       len(acquisition),
	}
	var hit []string
	for name, n := range lanes {
		if n > 0 {
			hit = append(hit, name)
		}
	}
	require.Len(t, hit, 1, "exactly one lane must receive the event")
	return hit[0]
}

// routingScenario is one combination of local configuration and peer
// handshake features under which inbound overlay dispatch is pinned.
type routingScenario struct {
	name      string
	localTxrr bool
	features  []Feature
}

var routingScenarios = []routingScenario{
	{name: "bare"},
	{name: "ledgerreplay", features: []Feature{FeatureLedgerReplay}},
	{name: "txrr-peer-only", features: []Feature{FeatureTxReduceRelay}},
	{name: "txrr-local-only", localTxrr: true},
	{name: "txrr", localTxrr: true, features: []Feature{FeatureTxReduceRelay}},
	{name: "vprr", features: []Feature{FeatureVpReduceRelay}},
	{name: "all", localTxrr: true, features: []Feature{
		FeatureLedgerReplay, FeatureCompression, FeatureVpReduceRelay, FeatureTxReduceRelay, FeatureTransactionBatching,
	}},
}

// overlayRouting is the observed result of inbound overlay dispatch for an
// undecodable payload: the overlay lane the message is forwarded to ("-"
// when handled inside the overlay) followed by the resource charges applied,
// in order. byScenario overrides def for the named scenarios.
type overlayRouting struct {
	def        string
	byScenario map[string]string
}

const trivial = "trivial peer request ($1)"

func gatedRouting(feature []string, admitted, refused string) overlayRouting {
	r := overlayRouting{def: refused, byScenario: make(map[string]string, len(feature))}
	for _, sc := range feature {
		r.byScenario[sc] = admitted
	}
	return r
}

var (
	ledgerReplayScenarios = []string{"ledgerreplay", "all"}
	txReduceScenarios     = []string{"txrr", "all"}
)

var wantOverlayRouting = map[message.MessageType]overlayRouting{
	message.TypeManifests:               {def: "general [" + trivial + "(mtMANIFESTS)]"},
	message.TypePing:                    {def: "- [" + trivial + "(mtPING)]"},
	message.TypeCluster:                 {def: "- [useless data ($150)(cluster-no-pubkey); " + trivial + "(mtCLUSTER)]"},
	message.TypeEndpoints:               {def: "- [" + trivial + "(mtENDPOINTS)]"},
	message.TypeTransaction:             {def: "transaction [" + trivial + "(mtTRANSACTION)]"},
	message.TypeGetLedger:               {def: "general [" + trivial + "(mtGET_LEDGER)]"},
	message.TypeLedgerData:              {def: "ledger-data [" + trivial + "(mtLEDGER_DATA)]"},
	message.TypeProposeLedger:           {def: "consensus [" + trivial + "(mtPROPOSE_LEDGER)]"},
	message.TypeStatusChange:            {def: "consensus-control [" + trivial + "(mtSTATUS_CHANGE)]"},
	message.TypeHaveSet:                 {def: "consensus-control [" + trivial + "(mtHAVE_SET)]"},
	message.TypeValidation:              {def: "consensus [" + trivial + "(mtVALIDATION)]"},
	message.TypeGetObjects:              {def: "- [invalid data ($400)(get-objects-decode); " + trivial + "(mtGET_OBJECTS)]"},
	message.TypeSquelch:                 {def: "- [invalid data ($400)(mtSQUELCH squelch-malformed-pubkey)]"},
	message.TypeValidatorListCollection: {def: "consensus [" + trivial + "(mtVALIDATOR_LIST_COLLECTION)]"},
	message.TypeProofPathReq: gatedRouting(ledgerReplayScenarios,
		"- [malformed request ($200)(mtPROOF_PATH_REQ proof-path-req-decode)]",
		"- [malformed request ($200)(mtPROOF_PATH_REQ proof path request disabled)]"),
	message.TypeProofPathResponse: gatedRouting(ledgerReplayScenarios,
		"ledger-data ["+trivial+"(mtPROOF_PATH_RESPONSE)]",
		"- [malformed request ($200)(mtPROOF_PATH_RESPONSE proof path response disabled)]"),
	message.TypeReplayDeltaReq: gatedRouting(ledgerReplayScenarios,
		"- [malformed request ($200)(mtREPLAY_DELTA_REQ replay-delta-req-decode)]",
		"- [malformed request ($200)(mtREPLAY_DELTA_REQ replay delta request disabled)]"),
	message.TypeReplayDeltaResponse: gatedRouting(ledgerReplayScenarios,
		"ledger-data ["+trivial+"(mtREPLAY_DELTA_RESPONSE)]",
		"- [malformed request ($200)(mtREPLAY_DELTA_RESPONSE replay delta response disabled)]"),
	message.TypeHaveTransactions: gatedRouting(txReduceScenarios,
		"- [invalid data ($400)(have-transactions-decode); "+trivial+"(mtHAVE_TRANSACTIONS)]",
		"- [malformed request ($200)(have-transactions-unnegotiated); "+trivial+"(mtHAVE_TRANSACTIONS)]"),
	message.TypeTransactions: gatedRouting(txReduceScenarios,
		"- [invalid data ($400)(mtTRANSACTIONS transactions-batch-decode)]",
		"- [malformed request ($200)(mtTRANSACTIONS transactions-batch-unnegotiated)]"),
}

var unlistedOverlayRouting = overlayRouting{def: "general [" + trivial + "(mtUNKNOWN)]"}

// TestOverlayInboundRoutingPinned pins, for every wire type and unlisted
// type codes, which overlay lane a message reaches and how the peer is
// charged under each combination of local config and negotiated features.
func TestOverlayInboundRoutingPinned(t *testing.T) {
	for _, msgType := range classifiedMessageTypes() {
		want, ok := wantOverlayRouting[msgType]
		if !ok {
			want = unlistedOverlayRouting
		}
		for _, sc := range routingScenarios {
			expected := want.def
			if v, ok := want.byScenario[sc.name]; ok {
				expected = v
			}
			assert.Equal(t, expected, observeOverlayRouting(t, msgType, sc),
				"%s(%d)/%s", msgType, msgType, sc.name)
		}
	}
}

func observeOverlayRouting(t *testing.T, msgType message.MessageType, sc routingScenario) string {
	t.Helper()
	id, err := NewIdentity()
	require.NoError(t, err)

	cfg := DefaultConfig()
	cfg.EnableTxReduceRelay = sc.localTxrr
	lanes := map[string]chan *InboundMessage{
		"general":           make(chan *InboundMessage, 1),
		"transaction":       make(chan *InboundMessage, 1),
		"ledger-data":       make(chan *InboundMessage, 1),
		"consensus":         make(chan *InboundMessage, 1),
		"consensus-control": make(chan *InboundMessage, 1),
	}
	o := &Overlay{
		cfg:                      cfg,
		peers:                    make(map[PeerID]*Peer),
		events:                   make(chan Event, 8),
		cluster:                  cluster.New(),
		messages:                 lanes["general"],
		txMessages:               lanes["transaction"],
		ledgerData:               lanes["ledger-data"],
		consensusMessages:        lanes["consensus"],
		consensusControlMessages: lanes["consensus-control"],
		stopCh:                   make(chan struct{}),
	}

	peer := NewPeer(PeerID(7), Endpoint{Host: "127.0.0.1", Port: 51235}, false, id, make(chan Event, 1))
	peer.setState(PeerStateConnected)
	caps := NewPeerCapabilities()
	for _, f := range sc.features {
		caps.Features.Enable(f)
	}
	peer.capabilities = caps
	charges := &chargeRecorder{}
	clock := time.Unix(1_700_000_000, 0)
	peer.usage = resource.NewManager(func() time.Time { return clock }, slog.New(charges)).
		NewInboundEndpoint(peer.endpoint.String())
	peer.onDropDisconnect = func() {}
	o.peers[peer.ID()] = peer

	o.onMessageReceived(Event{
		Type:        EventMessageReceived,
		PeerID:      peer.ID(),
		MessageType: msgType,
		Payload:     []byte{0xff},
	})

	forwarded := "-"
	for name, lane := range lanes {
		select {
		case msg := <-lane:
			require.Equal(t, "-", forwarded, "message forwarded to more than one lane")
			forwarded = name
			require.NoError(t, msg.Close())
		default:
		}
	}
	return forwarded + " " + charges.String()
}

type chargeRecorder struct {
	mu      sync.Mutex
	charges []string
}

func (r *chargeRecorder) Enabled(context.Context, slog.Level) bool { return true }

func (r *chargeRecorder) Handle(_ context.Context, rec slog.Record) error {
	var fee, ctx string
	rec.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "fee":
			fee = a.Value.String()
		case "context":
			ctx = a.Value.String()
		}
		return true
	})
	r.mu.Lock()
	r.charges = append(r.charges, fee+"("+ctx+")")
	r.mu.Unlock()
	return nil
}

func (r *chargeRecorder) WithAttrs([]slog.Attr) slog.Handler { return r }
func (r *chargeRecorder) WithGroup(string) slog.Handler      { return r }

func (r *chargeRecorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return "[" + strings.Join(r.charges, "; ") + "]"
}

// TestWireMessageTypesMatchProtoEnum guards the classification pins against
// a wire type being added to the protobuf enum without being covered here.
func TestWireMessageTypesMatchProtoEnum(t *testing.T) {
	var fromProto []int
	for code := range proto.MessageType_name {
		fromProto = append(fromProto, int(code))
	}
	var pinned []int
	for _, msgType := range allWireMessageTypes {
		pinned = append(pinned, int(msgType))
	}
	sort.Ints(fromProto)
	sort.Ints(pinned)
	assert.Equal(t, fromProto, pinned)
}

// TestMessageDescriptorsComplete requires exactly one descriptor per wire
// type the codec table knows, an explicit routing pin for each, and a gate
// reason wherever a gate is declared.
func TestMessageDescriptorsComplete(t *testing.T) {
	for code, d := range messageDescriptors {
		msgType := message.MessageType(code)
		if d == nil {
			assert.False(t, message.IsKnownMessageType(msgType), "wire type %s has no descriptor", msgType)
			continue
		}
		assert.True(t, message.IsKnownMessageType(msgType), "descriptor for %d has no codec", code)
		_, pinned := wantOverlayRouting[msgType]
		assert.True(t, pinned, "%s has no routing pin", msgType)
		if d.gate != nil {
			assert.NotEmpty(t, d.gate.reason, msgType.String())
		}
	}
	for _, msgType := range allWireMessageTypes {
		assert.NotSame(t, &unlistedMessage, describeMessage(msgType), msgType.String())
	}
	for _, msgType := range unlistedMessageTypes {
		assert.Same(t, &unlistedMessage, describeMessage(msgType), msgType.String())
	}
}

var benchClassifySink int

func BenchmarkInboundMessageClassification(b *testing.B) {
	types := classifiedMessageTypes()
	b.ReportAllocs()
	for i := 0; b.Loop(); i++ {
		d := describeMessage(types[i%len(types)])
		n := int(d.traffic)
		switch d.lane {
		case laneConsensus:
			n++
		case laneConsensusControl:
			n += 2
		case laneAcquisition:
			n += 3
		}
		benchClassifySink += n
	}
}
