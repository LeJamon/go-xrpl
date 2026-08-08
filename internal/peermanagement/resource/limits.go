package resource

const (
	defaultMaxEntries             = 65_536
	defaultMaxImports             = 1_024
	defaultMaxGossipItems         = 1_024
	defaultMaxInflightPerConsumer = 16
	defaultMaxCleanupPerTick      = 256
	defaultMaxEndpointLength      = 64
	defaultMaxOriginLength        = 255
)

type Limits struct {
	MaxEntries             int
	MaxImports             int
	MaxGossipItems         int
	MaxInflightPerConsumer int
	MaxCleanupPerTick      int
	MaxEndpointLength      int
	MaxOriginLength        int
}

func DefaultLimits() Limits {
	return Limits{
		MaxEntries:             defaultMaxEntries,
		MaxImports:             defaultMaxImports,
		MaxGossipItems:         defaultMaxGossipItems,
		MaxInflightPerConsumer: defaultMaxInflightPerConsumer,
		MaxCleanupPerTick:      defaultMaxCleanupPerTick,
		MaxEndpointLength:      defaultMaxEndpointLength,
		MaxOriginLength:        defaultMaxOriginLength,
	}
}

func (l Limits) withDefaults() Limits {
	d := DefaultLimits()
	if l.MaxEntries > 0 {
		d.MaxEntries = l.MaxEntries
	}
	if l.MaxImports > 0 {
		d.MaxImports = l.MaxImports
	}
	if l.MaxGossipItems > 0 {
		d.MaxGossipItems = l.MaxGossipItems
	}
	if l.MaxInflightPerConsumer > 0 {
		d.MaxInflightPerConsumer = l.MaxInflightPerConsumer
	}
	if l.MaxCleanupPerTick > 0 {
		d.MaxCleanupPerTick = l.MaxCleanupPerTick
	}
	if l.MaxEndpointLength > 0 {
		d.MaxEndpointLength = l.MaxEndpointLength
	}
	if l.MaxOriginLength > 0 {
		d.MaxOriginLength = l.MaxOriginLength
	}
	return d
}

type Stats struct {
	Entries            int
	Imports            int
	Inflight           int
	Evictions          uint64
	EntryCapRejections uint64
	ImportRejections   uint64
	InflightRejections uint64
	Warnings           uint64
	Drops              uint64
}

type counters struct {
	evictions          uint64
	entryCapRejections uint64
	importRejections   uint64
	inflightRejections uint64
	warnings           uint64
	drops              uint64
}
