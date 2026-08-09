package peermanagement

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/LeJamon/go-xrpl/internal/peermanagement/message"
)

// PeerInfo is a read-only snapshot of peer state.
type PeerInfo struct {
	ID             PeerID
	Endpoint       Endpoint
	Inbound        bool
	State          PeerState
	PublicKey      string
	PublicKeyBytes []byte
	ConnectedAt    time.Time
	MessagesIn     uint64
	MessagesOut    uint64

	ServerDomain    string
	NetworkID       string
	Version         string
	ClosedLedger    string
	CompleteLedgers string
	Tracking        PeerTracking
	Load            int64

	Latency    time.Duration
	HasLatency bool

	Protocol string

	Status message.NodeStatus

	TotalBytesRecv uint64
	TotalBytesSent uint64
	AvgBpsRecv     uint64
	AvgBpsSent     uint64

	SendDrops            uint64
	SendDropsControl     uint64
	SendDropsConsensus   uint64
	SendDropsAcquisition uint64
	SendDropsOrdinary    uint64
	SendDropsBulk        uint64
}

func (p *Peer) Info() PeerInfo {
	p.mu.RLock()
	defer p.mu.RUnlock()

	var (
		pubKey      string
		pubKeyBytes []byte
	)
	if p.remotePubKey != nil {
		pubKey = p.remotePubKey.Encode()
		pubKeyBytes = p.remotePubKey.Bytes()
	}

	stats := p.traffic.TotalStats()

	var closedLedger string
	if p.hasClosedLedger {
		closedLedger = strings.ToUpper(hex.EncodeToString(p.closedLedger[:]))
	}

	var completeLedgers string
	if p.firstLedgerSeq != 0 || p.lastLedgerSeq != 0 {
		completeLedgers = fmt.Sprintf("%d - %d", p.firstLedgerSeq, p.lastLedgerSeq)
	}

	latency, hasLatency := p.Latency()

	return PeerInfo{
		ID:                   p.id,
		Endpoint:             p.endpoint,
		Inbound:              p.inbound,
		State:                p.state,
		PublicKey:            pubKey,
		PublicKeyBytes:       pubKeyBytes,
		ConnectedAt:          p.createdAt,
		MessagesIn:           stats.MessagesIn,
		MessagesOut:          stats.MessagesOut,
		ServerDomain:         p.serverDomain,
		NetworkID:            p.networkID,
		Version:              p.userAgent,
		ClosedLedger:         closedLedger,
		CompleteLedgers:      completeLedgers,
		Tracking:             PeerTracking(p.tracking.Load()),
		Load:                 p.Load(),
		Latency:              latency,
		HasLatency:           hasLatency,
		Protocol:             p.protocolVersion,
		Status:               p.lastStatus,
		TotalBytesRecv:       p.metrics.recv.totalBytesSnapshot(),
		TotalBytesSent:       p.metrics.sent.totalBytesSnapshot(),
		AvgBpsRecv:           p.metrics.recv.averageBytes(),
		AvgBpsSent:           p.metrics.sent.averageBytes(),
		SendDrops:            p.sendDrops.Load(),
		SendDropsControl:     p.SendDropsByClass(OutboundClassControl),
		SendDropsConsensus:   p.SendDropsByClass(OutboundClassConsensus),
		SendDropsAcquisition: p.SendDropsByClass(OutboundClassAcquisition),
		SendDropsOrdinary:    p.SendDropsByClass(OutboundClassOrdinary),
		SendDropsBulk:        p.SendDropsByClass(OutboundClassBulk),
	}
}
