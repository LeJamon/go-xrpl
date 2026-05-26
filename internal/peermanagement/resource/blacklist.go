package resource

// BlacklistEntry is one endpoint's resource reputation, mirroring an entry in
// rippled's Resource::Logic::getJson output (Logic.h:208-254).
type BlacklistEntry struct {
	Address string
	Local   int
	Remote  int
	Type    string
}

// Snapshot returns every tracked endpoint whose combined (local + remote)
// balance is at or above threshold, mirroring rippled
// Resource::Logic::getJson(threshold). Higher balances indicate heavier
// accrued load; the black_list RPC's default threshold is WarningThreshold.
func (m *Manager) Snapshot(threshold int) []BlacklistEntry {
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]BlacklistEntry, 0, len(m.entries))
	for _, e := range m.entries {
		local := e.localBalance.valueAt(now)
		if local+e.remoteBalance < threshold {
			continue
		}
		out = append(out, BlacklistEntry{
			Address: e.k.addr,
			Local:   local,
			Remote:  e.remoteBalance,
			Type:    blacklistType(e.k.kind),
		})
	}
	return out
}

// blacklistType maps a Kind to rippled's getJson "type" label. KindUnlimited
// corresponds to rippled's admin_ table.
func blacklistType(k Kind) string {
	if k == KindUnlimited {
		return "admin"
	}
	return k.String()
}
