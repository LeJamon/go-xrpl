package service

// Counts is a snapshot of the runtime counters surfaced by the get_counts RPC.
// It deliberately includes only counters goXRPL actually tracks — a strict
// subset of rippled's GetCounts.cpp: node-store I/O counters and locally-held
// transactions. rippled's object-type counts, SLE/accepted-ledger caches,
// node-store caches and uptime have no surfaced goXRPL equivalent and are
// omitted rather than fabricated.
type Counts struct {
	Standalone bool
	LocalTxs   int
	NodeStore  *NodeStoreCounts
}

// NodeStoreCounts holds the node store's I/O counters. Fields map 1:1 onto the
// node_* keys rippled emits from NodeStore::Database::getCountsJson; goXRPL
// surfaces only the ones it has real data for.
type NodeStoreCounts struct {
	Reads      uint64 // node_reads_total  (rippled fetchTotalCount_)
	FetchHits  uint64 // node_reads_hit    (rippled fetchHitCount_: reads that found data)
	Writes     uint64 // node_writes       (rippled storeCount_)
	ReadBytes  uint64 // node_read_bytes   (rippled fetchSz_)
	WriteBytes uint64 // node_written_bytes (rippled storeSz_)
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
			Reads:      st.Reads,
			FetchHits:  st.FetchHits,
			Writes:     st.Writes,
			ReadBytes:  st.ReadBytes,
			WriteBytes: st.WriteBytes,
		}
	}
	return c
}
