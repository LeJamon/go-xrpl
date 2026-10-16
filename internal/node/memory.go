package node

import (
	"fmt"

	"github.com/LeJamon/go-xrpl/config"
	"github.com/LeJamon/go-xrpl/internal/consensus/adaptor"
	"github.com/LeJamon/go-xrpl/internal/ledger/service"
	"github.com/LeJamon/go-xrpl/internal/membudget"
	"github.com/LeJamon/go-xrpl/internal/observability"
	"github.com/LeJamon/go-xrpl/internal/peermanagement"
	"github.com/LeJamon/go-xrpl/shamap"
	kvpebble "github.com/LeJamon/go-xrpl/storage/kvstore/pebble"
)

const (
	speculativeMemoryMinimum = 64 << 20
	speculativeMemoryMaximum = 256 << 20
)

type nodeMemory struct {
	pool               *membudget.Pool
	acquisition        *membudget.Account
	nodes              *membudget.Account
	cleanNodes         *membudget.Account
	fullBelow          *membudget.Account
	relay              *membudget.Account
	persistence        *membudget.Account
	speculative        *membudget.Account
	replayVerification *membudget.Account
	inbound            *membudget.Account
	outbound           *membudget.Account
	outboundCritical   *membudget.Account
	storage            *membudget.Account
	storageBytes       uint64
	releaseStorage     func()
	headroom           membudget.Headroom
}

func newNodeMemory(cfg *config.Config, standalone bool, probe func() (membudget.Headroom, error)) (*nodeMemory, error) {
	if err := cfg.Acquisition.Validate(); err != nil {
		return nil, fmt.Errorf("acquisition: %w", err)
	}
	m := &nodeMemory{headroom: membudget.Headroom{Source: "configured"}}
	if cfg.Resources.MemoryMB == 0 {
		var err error
		m.headroom, err = probe()
		if err != nil {
			return nil, fmt.Errorf("resources: detect available memory: %w; configure resources.memory_mb explicitly", err)
		}
	}
	limit, err := cfg.Resources.Resolve(m.headroom.Available)
	if err != nil {
		return nil, err
	}
	m.pool = membudget.New(limit)
	account := func(name string, minimum, maximum uint64) (*membudget.Account, error) {
		a, err := m.pool.NewAccount(name, minimum, maximum)
		if err != nil {
			return nil, fmt.Errorf("resources: %s cannot fit in the %d MiB shared allowance: %w; increase resources.memory_mb or reduce configured capacities", name, limit>>20, err)
		}
		return a, nil
	}
	if cfg.NodeDB.Path != "" {
		options, err := pebbleStoreOptions(cfg.NodeDB)
		if err != nil {
			return nil, err
		}
		rotating, err := kvpebble.HasRotationState(cfg.NodeDB.Path)
		if err != nil {
			return nil, fmt.Errorf("resources: inspect rotation state: %w", err)
		}
		generations := 1
		if rotating || cfg.NodeDB.IsOnlineDeleteEnabled() {
			generations = 3 // Rotation can open a successor while old-generation snapshots drain.
		}
		m.storageBytes, err = options.MemoryCommitment(generations)
		if err != nil {
			return nil, err
		}
		m.storage, err = account("storage", m.storageBytes, m.storageBytes)
		if err != nil {
			return nil, err
		}
		m.persistence, err = account("persistence", shamap.DirtyWorkspaceBytes, shamap.DirtyWorkspaceBytes)
		if err != nil {
			return nil, err
		}
		m.speculative, err = account("speculative", speculativeMemoryMinimum, speculativeMemoryMaximum)
		if err != nil {
			return nil, err
		}
		// Cold verification must never occupy persistence's progress reserve.
		// Account for two bounded readers inside the existing global allowance.
		verificationBytes := uint64(2 * service.ReplayVerificationWorkspaceBytes)
		m.replayVerification, err = account("replay_verification", verificationBytes, verificationBytes)
		if err != nil {
			return nil, err
		}
		m.fullBelow, err = account("full_below", shamap.FullBelowCacheMemoryBytes, shamap.FullBelowCacheMemoryBytes)
		if err != nil {
			return nil, err
		}
	}
	if !standalone {
		acquisitionLimit := limit
		if cfg.Acquisition.MemoryMB > 0 {
			acquisitionLimit = cfg.Acquisition.ResolvedMemoryBytes()
		}
		m.acquisition, err = account("acquisition", config.MinAcquisitionMemoryMB<<20, acquisitionLimit)
		if err != nil {
			return nil, err
		}
		peerConfig := peermanagement.DefaultConfig()
		for _, option := range adaptor.OverlayOptionsFromConfig(cfg) {
			option(&peerConfig)
		}
		if err := peerConfig.Validate(); err != nil {
			return nil, fmt.Errorf("resources: peer configuration: %w", err)
		}
		generalMinimum := uint64(peermanagement.MinimumOutboundRetainedBytes(0))
		criticalMinimum := uint64(peermanagement.MinimumOutboundRetainedBytes(peerConfig.MaxPeers)) - generalMinimum
		m.inbound, err = account("peer_inbound", uint64(peermanagement.DefaultInboundRetainedBytes), uint64(peerConfig.InboundRetainedBytes))
		if err != nil {
			return nil, err
		}
		m.outboundCritical, err = account("peer_critical", criticalMinimum, criticalMinimum)
		if err != nil {
			return nil, err
		}
		m.outbound, err = account("peer_outbound", generalMinimum, uint64(peerConfig.OutboundRetainedBytes)-criticalMinimum)
		if err != nil {
			return nil, err
		}
	}
	// Cache shares are derived only after every progress reserve has been
	// registered. This leaves those reserves protected while bounding each
	// elastic cache independently.
	residual := m.pool.Snapshot().SharedAvailable
	for _, item := range []struct {
		name         string
		dest         **membudget.Account
		minimumShare uint64
		maximumShare uint64
	}{
		{"node_cache", &m.nodes, residual / 8, boundedCacheMaximum(limit, residual, 4)},
		{"shamap_cache", &m.cleanNodes, residual / 4, boundedCacheMaximum(limit, residual, 2)},
		{"relay_cache", &m.relay, 0, boundedCacheMaximum(limit, residual, 16)},
	} {
		*item.dest, err = account(item.name, item.minimumShare, item.maximumShare)
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

// boundedCacheMaximum keeps a zero share an explicit cap. membudget uses zero
// as the sentinel for the parent pool limit, which would make a tiny residual
// unbounded and defeat the per-cache admission policy.
func boundedCacheMaximum(limit, residual, divisor uint64) uint64 {
	maximum := residual / divisor
	if maximum == 0 && limit != 0 {
		return 1
	}
	return maximum
}

func (r *nodeRuntime) configureMemory() error {
	memory, err := newNodeMemory(r.appConfig, r.standalone, membudget.Probe)
	if err != nil {
		return err
	}
	r.memory = memory
	r.stopMemoryMetrics = observability.SetMemoryPool(memory.pool)
	r.serverLog.Info("Shared memory allowance configured",
		"memory_mb", memory.pool.Snapshot().Limit>>20,
		"available_bytes", memory.headroom.Available,
		"source", memory.headroom.Source,
		"automatic", r.appConfig.Resources.MemoryMB == 0,
		"storage_reserved_bytes", memory.storageBytes,
	)
	for _, account := range memory.pool.Snapshot().Accounts {
		r.serverLog.Info("Memory consumer configured", "consumer", account.Name,
			"reserved_bytes", account.Reserved, "ceiling_bytes", account.Maximum)
	}
	return nil
}
