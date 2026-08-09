package subscription

import "github.com/LeJamon/go-xrpl/internal/rpc/types"

func (sm *Manager) BroadcastToStream(stream types.SubscriptionType, data []byte) int {
	targets := sm.collectStreamTargets(stream)
	sm.deliver(targets, data)
	return len(targets)
}

func (sm *Manager) BroadcastToStreamVersioned(stream types.SubscriptionType, v1, v2 []byte) {
	sm.deliverVersioned(sm.collectStreamTargets(stream), v1, v2)
}

func (sm *Manager) collectStreamTargets(stream types.SubscriptionType) []*connectionRecord {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	targets := make([]*connectionRecord, 0, len(sm.streamIndex[stream]))
	for record := range sm.streamIndex[stream] {
		if !record.detaching {
			targets = append(targets, record)
		}
	}
	return targets
}

func (sm *Manager) deliver(targets []*connectionRecord, data []byte) {
	for _, record := range targets {
		record.deliveryMu.RLock()
		if record.active {
			sm.recordDelivery(record.conn.trySend(data))
		}
		record.deliveryMu.RUnlock()
	}
}

func (sm *Manager) deliverVersioned(targets []*connectionRecord, v1, v2 []byte) {
	for _, record := range targets {
		record.deliveryMu.RLock()
		if !record.active {
			record.deliveryMu.RUnlock()
			continue
		}
		data := v2
		if record.conn.APIVersion() == types.ApiVersion1 {
			data = v1
		}
		sm.recordDelivery(record.conn.trySend(data))
		record.deliveryMu.RUnlock()
	}
}

func (sm *Manager) BroadcastToAccountsVersioned(v1, v2 []byte, accounts []string) {
	sm.deliverVersioned(sm.collectAccountTargets(types.SubAccounts, accounts), v1, v2)
}

func (sm *Manager) BroadcastToAccountsProposedVersioned(v1, v2 []byte, accounts []string) {
	sm.deliverVersioned(sm.collectAccountTargets(types.SubAccountsProposed, accounts), v1, v2)
}

func (sm *Manager) BroadcastToAcceptedAccountsVersioned(v1, v2 []byte, accounts []string) {
	sm.deliverVersioned(sm.collectAccountTargetsForStreams([]types.SubscriptionType{
		types.SubAccounts,
		types.SubAccountsProposed,
	}, accounts), v1, v2)
}

func (sm *Manager) collectAccountTargets(stream types.SubscriptionType, accounts []string) []*connectionRecord {
	return sm.collectAccountTargetsForStreams([]types.SubscriptionType{stream}, accounts)
}

func (sm *Manager) collectAccountTargetsForStreams(streams []types.SubscriptionType, accounts []string) []*connectionRecord {
	if len(accounts) == 0 {
		return nil
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	set := make(map[*connectionRecord]struct{})
	for _, stream := range streams {
		for _, account := range accounts {
			for record := range sm.accountIndex[stream][account] {
				if !record.detaching {
					set[record] = struct{}{}
				}
			}
		}
	}
	targets := make([]*connectionRecord, 0, len(set))
	for record := range set {
		targets = append(targets, record)
	}
	return targets
}

func (sm *Manager) BroadcastToOrderBooksVersioned(v1, v2 []byte, books []types.OrderBookSpec) {
	sm.deliverVersioned(sm.collectOrderBookTargets(books), v1, v2)
}

func (sm *Manager) collectOrderBookTargets(books []types.OrderBookSpec) []*connectionRecord {
	canonical := make([]book, 0, len(books))
	for _, spec := range books {
		if book, ok := bookFromSpec(spec); ok {
			canonical = append(canonical, book)
		}
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	set := make(map[*connectionRecord]struct{})
	for _, book := range canonical {
		for record := range sm.bookIndex[book] {
			if !record.detaching {
				set[record] = struct{}{}
			}
		}
	}
	targets := make([]*connectionRecord, 0, len(set))
	for record := range set {
		targets = append(targets, record)
	}
	return targets
}
