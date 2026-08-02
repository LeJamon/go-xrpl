package node

import (
	"strconv"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/consensus"
	consensusadaptor "github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/manifest"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	xrpllog "github.com/LeJamon/go-xrpl/log"
	"github.com/LeJamon/go-xrpl/protocol"
)

type rpcEventBridge struct {
	publisher rpc.EventPublisher
	manifests *manifest.Cache
	networkID uint32
}

func (b *rpcEventBridge) OnEvent(event consensus.Event) {
	if b == nil || b.publisher == nil {
		return
	}
	switch e := event.(type) {
	case *consensus.ValidationReceivedEvent:
		if e == nil || e.Validation == nil {
			return
		}
		validationEvent, err := buildValidationEvent(e, b.manifests, b.networkID)
		if err != nil {
			xrpllog.Named(xrpllog.PartitionRPC).Error("Skipping invalid validation event", "err", err)
			return
		}
		b.publisher.PublishValidation(validationEvent)
	case *consensus.PhaseChangedEvent:
		if e == nil {
			return
		}
		b.publisher.PublishConsensusPhase(consensusPhaseName(e.NewPhase))
	}
}

func consensusPhaseName(p consensus.Phase) string {
	switch p {
	case consensus.PhaseOpen:
		return rpc.ConsensusPhaseOpen
	case consensus.PhaseEstablish:
		return rpc.ConsensusPhaseEstablish
	case consensus.PhaseAccepted:
		return rpc.ConsensusPhaseAccepted
	default:
		return p.String()
	}
}

// buildValidationEvent renders a rippled-shape validationReceived event
// from a ValidationReceivedEvent. master_key is emitted only when the
// manifest cache resolves a master distinct from the signing key
// (NetworkOPs.cpp:2434-2438); validation_public_key carries the signing
// (ephemeral) key in every case. Canonical STValidation bytes are
// surfaced via the `data` field and network_id
// from the local config (NetworkOPs.cpp:2423).
func buildValidationEvent(e *consensus.ValidationReceivedEvent, manifests *manifest.Cache, networkID uint32) (*rpc.ValidationEvent, error) {
	v := e.Validation
	canonical, err := consensusadaptor.CanonicalSTValidation(v)
	if err != nil {
		return nil, err
	}
	signingEnc, _ := addresscodec.EncodeNodePublicKey(v.SigningPubKey[:])
	ev := rpc.NewValidationEvent(
		upperHex(v.LedgerID[:]),
		v.LedgerSeq,
		signingEnc,
		upperHex(v.Signature),
		protocol.ToRippleTime(v.SignTime),
		v.Flags,
		v.Full,
	)
	ev.Data = upperHex(canonical)
	ev.NetworkID = networkID
	if !v.CloseTime.IsZero() {
		closeTime := protocol.ToRippleTime(v.CloseTime)
		ev.CloseTime = &closeTime
	}
	if manifests != nil {
		master := manifests.GetMasterKey(v.SigningPubKey)
		if master != v.SigningPubKey {
			if enc, err := addresscodec.EncodeNodePublicKey(master[:]); err == nil {
				ev.MasterKey = enc
			}
		}
	}
	if v.Cookie != 0 {
		ev.Cookie = strconv.FormatUint(v.Cookie, 10)
	}
	if v.HasLoadFee() {
		loadFee := v.LoadFee
		ev.LoadFee = &loadFee
	}
	if v.ServerVersion != 0 {
		ev.ServerVersion = strconv.FormatUint(v.ServerVersion, 10)
	}
	if v.HasBaseFee() {
		ev.BaseFee = float64(v.BaseFee)
	}
	if amount, ok := v.BaseFeeDropsVote(); ok {
		ev.BaseFee = jsonClippedXRPAmount(amount.Drops())
	}
	if v.HasReserveBase() {
		ev.ReserveBase = v.ReserveBase
	}
	if amount, ok := v.ReserveBaseDropsVote(); ok {
		ev.ReserveBase = jsonClippedXRPAmount(amount.Drops())
	}
	if v.HasReserveIncrement() {
		ev.ReserveInc = v.ReserveIncrement
	}
	if amount, ok := v.ReserveIncrementDropsVote(); ok {
		ev.ReserveInc = jsonClippedXRPAmount(amount.Drops())
	}
	if len(v.Amendments) > 0 {
		ev.Amendments = make([]string, len(v.Amendments))
		for i, a := range v.Amendments {
			ev.Amendments[i] = upperHex(a[:])
		}
	}
	if v.ValidatedHash != [32]byte{} {
		ev.ValidatedHash = upperHex(v.ValidatedHash[:])
	}
	return ev, nil
}
