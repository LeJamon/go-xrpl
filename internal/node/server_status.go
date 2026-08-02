package node

import (
	"math"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/rpc"
	"github.com/LeJamon/go-xrpl/internal/rpc/handlers"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type serverStatusSnapshot struct {
	baseFee                 uint64
	loadBase                uint64
	loadFactor              uint64
	loadFactorFeeEscalation uint64
	loadFactorFeeQueue      uint64
	loadFactorFeeReference  uint64
	loadFactorServer        uint64
	serverStatus            string
}

type serverStatusEventPublisher interface {
	PublishServerStatus(*rpc.ServerStatusEvent) bool
}

type serverStatusPublisher struct {
	mu        sync.Mutex
	services  *types.ServiceContainer
	publisher serverStatusEventPublisher
	haveLast  bool
	last      serverStatusSnapshot
	haveMode  bool
	mode      string
}

func newServerStatusPublisher(services *types.ServiceContainer, publisher serverStatusEventPublisher) *serverStatusPublisher {
	return &serverStatusPublisher{services: services, publisher: publisher}
}

func (p *serverStatusPublisher) publish(mode *string) {
	snapshot, event := p.capture(mode)
	p.publishCaptured(snapshot, event)
}

func (p *serverStatusPublisher) statusPublication(mode *string) service.ServerStatusPublication {
	snapshot, event := p.capture(mode)
	if event == nil {
		return nil
	}
	return func() {
		p.publishCaptured(snapshot, event)
	}
}

func (p *serverStatusPublisher) modePublication(mode string) service.ServerStatusPublication {
	return p.statusPublication(&mode)
}

func (p *serverStatusPublisher) capture(mode *string) (serverStatusSnapshot, *rpc.ServerStatusEvent) {
	if p == nil || p.services == nil || p.services.Ledger == nil || p.publisher == nil {
		return serverStatusSnapshot{}, nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()

	serverStatus := "full"
	if mode != nil {
		serverStatus = *mode
		p.mode = serverStatus
		p.haveMode = true
	} else if p.haveMode {
		serverStatus = p.mode
	} else {
		if info := p.services.Ledger.GetServerInfo(); info.ServerState != "" {
			serverStatus = info.ServerState
		}
		p.mode = serverStatus
		p.haveMode = true
	}
	baseFee, _, _ := p.services.Ledger.GetCurrentFees()
	load := handlers.ComputeServerLoad(p.services)
	snapshot := serverStatusSnapshot{
		baseFee:                 baseFee,
		loadBase:                load.LoadBase,
		loadFactor:              load.LoadFactor,
		loadFactorFeeEscalation: load.LoadFactorFeeEscalation,
		loadFactorFeeQueue:      load.LoadFactorFeeQueue,
		loadFactorFeeReference:  load.LoadFactorFeeReference,
		loadFactorServer:        load.LoadFactorServer,
		serverStatus:            serverStatus,
	}
	return snapshot, &rpc.ServerStatusEvent{
		Type:                    "serverStatus",
		BaseFee:                 jsonClippedXRPAmount(int64(baseFee)),
		LoadBase:                clipServerLoad(load.LoadBase),
		LoadFactor:              clipServerLoad(load.LoadFactor),
		LoadFactorFeeEscalation: clipServerLoad(load.LoadFactorFeeEscalation),
		LoadFactorFeeQueue:      clipServerLoad(load.LoadFactorFeeQueue),
		LoadFactorFeeReference:  clipServerLoad(load.LoadFactorFeeReference),
		LoadFactorServer:        clipServerLoad(load.LoadFactorServer),
		ServerStatus:            serverStatus,
	}
}

func (p *serverStatusPublisher) publishCaptured(snapshot serverStatusSnapshot, event *rpc.ServerStatusEvent) {
	if event == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.haveLast && snapshot == p.last {
		return
	}
	if p.publisher.PublishServerStatus(event) {
		p.last = snapshot
		p.haveLast = true
	}
}

func clipServerLoad(value uint64) uint32 {
	if value > math.MaxUint32 {
		return math.MaxUint32
	}
	return uint32(value)
}
