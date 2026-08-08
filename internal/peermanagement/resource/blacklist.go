package resource

// BlacklistEntry is one endpoint's resource reputation, mirroring an entry in
// rippled's Resource::Logic::getJson output (Logic.h:208-254).
type BlacklistEntry struct {
	Address string
	Local   int64
	Remote  int64
	Type    string
}

// Snapshot returns every active endpoint whose combined (local + remote)
// balance is at or above threshold, mirroring rippled
// Resource::Logic::getJson(threshold). Higher balances indicate heavier
// accrued load; the black_list RPC's default threshold is WarningThreshold.
//
// Released endpoints with no active references are skipped: rippled's release() moves an
// entry out of the inbound_/outbound_/admin_ lists into inactive_ the moment
// its last Consumer drops (Logic.h:420-447), and getJson only iterates the
// active lists, so a disconnected endpoint disappears immediately rather than
// lingering until expiry. Imported entries remain active until their snapshot expires.
func (m *Manager) Snapshot(threshold int64) []BlacklistEntry {
	now := m.clock()
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]BlacklistEntry, 0, len(m.entries))
	for _, e := range m.entries {
		if e.localRefs == 0 && e.importRefs == 0 {
			continue
		}
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

func (m *Manager) BlacklistJSON(threshold *int) map[string]any {
	t := int64(WarningThreshold)
	if threshold != nil {
		t = int64(*threshold)
	}
	result := make(map[string]any)
	for _, entry := range m.Snapshot(t) {
		result[entry.Address] = map[string]any{
			"local":  entry.Local,
			"remote": entry.Remote,
			"type":   entry.Type,
		}
	}
	return result
}

// blacklistType maps a Kind to rippled's getJson "type" label. KindUnlimited
// corresponds to rippled's admin_ table.
func blacklistType(k Kind) string {
	if k == KindUnlimited {
		return "admin"
	}
	return k.String()
}
