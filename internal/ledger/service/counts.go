package service

// Counts is a snapshot of the runtime counters surfaced by the get_counts RPC.
// It deliberately includes only counters goXRPL actually tracks — a subset of
// rippled's GetCounts.cpp (node-store I/O and cache, locally-held
// transactions). Object-type counts, SLE/accepted-ledger caches and uptime have
// no goXRPL equivalent and are omitted rather than fabricated.
type Counts struct {
	Standalone bool
	LocalTxs   int
	NodeStore  *NodeStoreCounts
}

// NodeStoreCounts holds the node store's I/O and cache statistics.
type NodeStoreCounts struct {
	BackendName  string
	Reads        uint64
	Writes       uint64
	ReadBytes    uint64
	WriteBytes   uint64
	CacheHits    uint64
	CacheMisses  uint64
	CacheSize    uint64
	CacheMaxSize uint64
}

// GetCounts returns a snapshot of the node's runtime counters for the
// get_counts RPC. Node-store statistics are present only when a persistent
// node store is configured (nil in pure in-memory / standalone setups).
func (s *Service) GetCounts() Counts {
	info := s.GetServerInfo()

	s.mu.RLock()
	pending := len(s.pendingTxs)
	s.mu.RUnlock()

	c := Counts{
		Standalone: info.Standalone,
		LocalTxs:   pending,
	}

	if s.nodeStore != nil {
		st := s.nodeStore.Stats()
		c.NodeStore = &NodeStoreCounts{
			BackendName:  st.BackendName,
			Reads:        st.Reads,
			Writes:       st.Writes,
			ReadBytes:    st.ReadBytes,
			WriteBytes:   st.WriteBytes,
			CacheHits:    st.CacheHits,
			CacheMisses:  st.CacheMisses,
			CacheSize:    st.CacheSize,
			CacheMaxSize: st.CacheMaxSize,
		}
	}
	return c
}
