package peermanagement

import "github.com/LeJamon/go-xrpl/internal/peermanagement/message"

// inboundLane is the per-peer queue a received frame enters before the
// overlay event loops consume it. Every lane but laneGeneral applies
// backpressure to the peer read loop; laneGeneral drops and counts when
// full. A lane whose channel is not wired falls back to laneGeneral.
type inboundLane uint8

const (
	laneGeneral inboundLane = iota
	laneManifests
	laneConsensus
	laneConsensusControl
	laneAcquisition
)

// overlayRoute is where the overlay forwards a message after its own
// handler, if any, has run.
type overlayRoute uint8

const (
	// routeGeneral is the best-effort o.messages lane.
	routeGeneral overlayRoute = iota
	// routeNone means the overlay's own handler consumes the message.
	routeNone
	routeTransaction
	routeLedgerData
	routeConsensus
	routeConsensusControl
)

// featureGate refuses a message from a peer that did not negotiate feature
// (and, when localTxReduceRelay is set, also when tx reduce-relay is off
// locally). A refused message is dropped and charged FeeMalformedRequest
// under reason before any handler runs.
type featureGate struct {
	feature            Feature
	localTxReduceRelay bool
	reason             string
	// immediate charges the peer directly instead of raising the
	// per-message fee.
	immediate bool
}

// messageDescriptor declares the per-type dispatch policy of one wire
// message type. The wire codec, payload cap and collection limits live in
// the message package's codec table.
type messageDescriptor struct {
	lane     inboundLane
	route    overlayRoute
	gate     *featureGate
	outbound OutboundSendClass
	traffic  TrafficCategory
}

// unlistedMessage is the policy for a type code without a descriptor. The
// peer read loop already discards frames of types the codec table does not
// know, so this only applies to events injected past that check.
var unlistedMessage = messageDescriptor{
	lane:     laneGeneral,
	route:    routeGeneral,
	outbound: OutboundClassOrdinary,
	traffic:  CategoryUnknown,
}

func ledgerReplayGate(reason string) *featureGate {
	return &featureGate{feature: FeatureLedgerReplay, reason: reason}
}

// messageDescriptors is indexed by message type; a nil slot is unlisted.
var messageDescriptors = [...]*messageDescriptor{
	message.TypeManifests: {
		lane:     laneManifests,
		route:    routeGeneral,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryManifests,
	},
	message.TypePing: {
		lane:     laneGeneral,
		route:    routeNone,
		outbound: OutboundClassControl,
		traffic:  CategoryBase,
	},
	message.TypeCluster: {
		lane:     laneGeneral,
		route:    routeNone,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryCluster,
	},
	message.TypeEndpoints: {
		lane:     laneGeneral,
		route:    routeNone,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryOverlay,
	},
	message.TypeTransaction: {
		lane:     laneGeneral,
		route:    routeTransaction,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryTransaction,
	},
	message.TypeGetLedger: {
		lane:     laneGeneral,
		route:    routeGeneral,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryLedgerData,
	},
	message.TypeLedgerData: {
		lane:     laneAcquisition,
		route:    routeLedgerData,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryLedgerData,
	},
	message.TypeProposeLedger: {
		lane:     laneConsensus,
		route:    routeConsensus,
		outbound: OutboundClassConsensus,
		traffic:  CategoryProposal,
	},
	// The overlay refreshes per-peer ledger state and peer_status before the
	// router sees the same frame.
	message.TypeStatusChange: {
		lane:     laneConsensusControl,
		route:    routeConsensusControl,
		outbound: OutboundClassControl,
		traffic:  CategoryUnknown,
	},
	message.TypeHaveSet: {
		lane:     laneConsensusControl,
		route:    routeConsensusControl,
		outbound: OutboundClassControl,
		traffic:  CategoryUnknown,
	},
	message.TypeValidation: {
		lane:     laneConsensus,
		route:    routeConsensus,
		outbound: OutboundClassConsensus,
		traffic:  CategoryValidation,
	},
	message.TypeGetObjects: {
		lane:     laneAcquisition,
		route:    routeNone,
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	// Accepted without a vpReduceRelay gate, as rippled does: the feature
	// governs what we send, and applying a squelch only suppresses our own
	// relaying.
	message.TypeSquelch: {
		lane:     laneGeneral,
		route:    routeNone,
		outbound: OutboundClassControl,
		traffic:  CategorySquelch,
	},
	message.TypeValidatorListCollection: {
		lane:     laneConsensus,
		route:    routeConsensus,
		outbound: OutboundClassConsensus,
		traffic:  CategoryValidatorList,
	},
	message.TypeProofPathReq: {
		lane:     laneGeneral,
		route:    routeNone,
		gate:     ledgerReplayGate("proof path request disabled"),
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	message.TypeProofPathResponse: {
		lane:     laneAcquisition,
		route:    routeLedgerData,
		gate:     ledgerReplayGate("proof path response disabled"),
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	message.TypeReplayDeltaReq: {
		lane:     laneGeneral,
		route:    routeNone,
		gate:     ledgerReplayGate("replay delta request disabled"),
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	message.TypeReplayDeltaResponse: {
		lane:     laneAcquisition,
		route:    routeLedgerData,
		gate:     ledgerReplayGate("replay delta response disabled"),
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	message.TypeHaveTransactions: {
		lane:  laneGeneral,
		route: routeNone,
		gate: &featureGate{
			feature:            FeatureTxReduceRelay,
			localTxReduceRelay: true,
			reason:             "have-transactions-unnegotiated",
			immediate:          true,
		},
		outbound: OutboundClassOrdinary,
		traffic:  CategoryUnknown,
	},
	message.TypeTransactions: {
		lane:  laneGeneral,
		route: routeNone,
		gate: &featureGate{
			feature:            FeatureTxReduceRelay,
			localTxReduceRelay: true,
			reason:             "transactions-batch-unnegotiated",
		},
		outbound: OutboundClassOrdinary,
		traffic:  CategoryTransaction,
	},
}

func describeMessage(t message.MessageType) *messageDescriptor {
	if int(t) < len(messageDescriptors) {
		if d := messageDescriptors[t]; d != nil {
			return d
		}
	}
	return &unlistedMessage
}
