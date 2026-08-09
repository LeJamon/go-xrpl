package subscription

import (
	"sync"
	"sync/atomic"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type Manager struct {
	connections               map[string]*connectionRecord
	streamIndex               map[types.SubscriptionType]map[*connectionRecord]struct{}
	accountIndex              map[types.SubscriptionType]map[string]map[*connectionRecord]struct{}
	bookIndex                 map[book]map[*connectionRecord]struct{}
	limits                    Limits
	items                     int
	requestLimitRejections    uint64
	connectionLimitRejections uint64
	globalLimitRejections     uint64
	nextGeneration            uint64
	deliveriesQueued          atomic.Uint64
	deliveriesDropped         atomic.Uint64
	deliveryDisconnects       atomic.Uint64
	mu                        sync.RWMutex
}

func NewManager() *Manager {
	return newManager(defaultLimits())
}

func newManager(limits Limits) *Manager {
	return &Manager{
		connections:  make(map[string]*connectionRecord),
		streamIndex:  make(map[types.SubscriptionType]map[*connectionRecord]struct{}),
		accountIndex: make(map[types.SubscriptionType]map[string]map[*connectionRecord]struct{}),
		bookIndex:    make(map[book]map[*connectionRecord]struct{}),
		limits:       limits,
	}
}

func (sm *Manager) Attach(conn *Connection) (*Registration, bool) {
	if conn == nil || conn.ID() == "" {
		return nil, false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.connections[conn.ID()] != nil {
		return nil, false
	}
	sm.nextGeneration++
	record := newConnectionRecord(conn, sm.nextGeneration)
	registration := &Registration{manager: sm, record: record, generation: record.generation}
	sm.connections[conn.ID()] = record
	return registration, true
}

func (sm *Manager) recordDelivery(outcome sendOutcome) {
	if outcome.queued {
		sm.deliveriesQueued.Add(1)
	} else if outcome.dropped {
		sm.deliveriesDropped.Add(1)
	}
	if outcome.disconnectedTransition {
		sm.deliveryDisconnects.Add(1)
	}
}

func (sm *Manager) Metrics() types.SubscriptionMetrics {
	sm.mu.RLock()
	metrics := types.SubscriptionMetrics{
		Connections:               uint64(len(sm.connections)),
		Items:                     uint64(sm.items),
		RequestLimitRejections:    sm.requestLimitRejections,
		ConnectionLimitRejections: sm.connectionLimitRejections,
		GlobalLimitRejections:     sm.globalLimitRejections,
	}
	sm.mu.RUnlock()
	metrics.DeliveriesQueued = sm.deliveriesQueued.Load()
	metrics.DeliveriesDropped = sm.deliveriesDropped.Load()
	metrics.DeliveryDisconnects = sm.deliveryDisconnects.Load()
	return metrics
}

func (sm *Manager) Detach(registration *Registration) bool {
	return sm.detachRecord(registration)
}

func (sm *Manager) HandleSubscribe(registration *Registration, request types.SubscriptionRequest, isAdmin bool) *rpcerrors.RpcError {
	return sm.HandleSubscribeScoped(registration, sm.NewRequestScope(), request, isAdmin)
}

func (sm *Manager) HandleSubscribeScoped(registration *Registration, scope *RequestScope, request types.SubscriptionRequest, isAdmin bool) *rpcerrors.RpcError {
	if scope == nil || scope.manager != sm {
		return rpcerrors.RpcErrorInternal()
	}
	sm.mu.RLock()
	record := sm.recordLocked(registration)
	sm.mu.RUnlock()
	if record == nil {
		return rpcerrors.RpcErrorInternal()
	}
	w := request.WireArrays()
	_, streams, streamsErr := resolveStreams(w.Present, w.Streams, request.Streams, scope)
	record.conn.SetAPIVersion(request.ApiVersion)
	for _, stream := range streams {
		if !validStreams[stream] {
			return rpcerrors.RpcErrorMalformedStream()
		}
		if stream == types.SubPeerStatus && !isAdmin {
			return rpcerrors.RpcErrorNoPermission("subscribe")
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(1); rpcErr != nil {
				return rpcErr
			}
		}
		key := stream
		if key == "rt_transactions" {
			key = types.SubTransactionsProposed
		}
		if rpcErr := scope.add(requestEdge{kind: 1, stream: key}); rpcErr != nil {
			return rpcErr
		}
		if rpcErr := sm.addStream(registration, key); rpcErr != nil {
			return rpcErr
		}
	}
	if streamsErr != nil {
		return streamsErr
	}

	proposedRaw, proposedTyped := w.RTAccounts, request.RTAccounts
	if w.AccountsProposed != nil || (!w.Present && request.AccountsProposed != nil) {
		proposedRaw, proposedTyped = w.AccountsProposed, request.AccountsProposed
	}
	proposedPresent, proposed, rpcErr := resolveAccounts(w.Present, proposedRaw, proposedTyped, scope)
	if rpcErr != nil {
		return rpcErr
	}
	if proposedPresent {
		if len(proposed) == 0 {
			return rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range proposed {
			if !isValidXRPLAddress(acc) {
				return rpcerrors.RpcErrorActMalformed("Account malformed.")
			}
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(len(proposed)); rpcErr != nil {
				return rpcErr
			}
		}
		proposed = uniqueStrings(proposed)
		for _, account := range proposed {
			if rpcErr := scope.add(requestEdge{kind: 2, stream: types.SubAccountsProposed, account: account}); rpcErr != nil {
				return rpcErr
			}
		}
		if rpcErr := sm.addAccounts(registration, types.SubAccountsProposed, proposed); rpcErr != nil {
			return rpcErr
		}
	}

	accountsPresent, accounts, rpcErr := resolveAccounts(w.Present, w.Accounts, request.Accounts, scope)
	if rpcErr != nil {
		return rpcErr
	}
	if accountsPresent {
		if len(accounts) == 0 {
			return rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range accounts {
			if !isValidXRPLAddress(acc) {
				return rpcerrors.RpcErrorActMalformed("Account malformed.")
			}
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(len(accounts)); rpcErr != nil {
				return rpcErr
			}
		}
		accounts = uniqueStrings(accounts)
		for _, account := range accounts {
			if rpcErr := scope.add(requestEdge{kind: 2, stream: types.SubAccounts, account: account}); rpcErr != nil {
				return rpcErr
			}
		}
		if rpcErr := sm.addAccounts(registration, types.SubAccounts, accounts); rpcErr != nil {
			return rpcErr
		}
	}

	booksPresent, books, booksErr := resolveBooks(w.Present, w.Books, request.Books, scope)
	if booksPresent {
		for _, requestBook := range books {
			canonical, rpcErr := parseBookRequest(requestBook, true)
			if rpcErr != nil {
				return rpcErr
			}
			if !w.Present {
				if rpcErr := scope.consumeRaw(1); rpcErr != nil {
					return rpcErr
				}
			}
			canonicalBooks := []book{canonical}
			if requestBook.Both || requestBook.BothSides {
				canonicalBooks = append(canonicalBooks, canonical.reversed())
			}
			edges := make([]requestEdge, 0, len(canonicalBooks))
			for _, value := range canonicalBooks {
				edges = append(edges, requestEdge{kind: 3, book: value})
			}
			if rpcErr := scope.addMany(edges); rpcErr != nil {
				return rpcErr
			}
			if rpcErr := sm.addBooks(registration, canonicalBooks); rpcErr != nil {
				return rpcErr
			}
		}
	}
	if booksErr != nil {
		return booksErr
	}

	return nil
}

func (sm *Manager) HandleUnsubscribe(registration *Registration, request types.SubscriptionRequest) *rpcerrors.RpcError {
	return sm.HandleUnsubscribeScoped(registration, sm.NewRequestScope(), request)
}

func (sm *Manager) HandleUnsubscribeScoped(registration *Registration, scope *RequestScope, request types.SubscriptionRequest) *rpcerrors.RpcError {
	if scope == nil || scope.manager != sm {
		return rpcerrors.RpcErrorInternal()
	}
	sm.mu.RLock()
	record := sm.recordLocked(registration)
	sm.mu.RUnlock()
	if record == nil {
		return rpcerrors.RpcErrorInternal()
	}
	w := request.WireArrays()

	_, streams, streamsErr := resolveStreams(w.Present, w.Streams, request.Streams, scope)
	for _, stream := range streams {
		if !validUnsubscribeStreams[stream] {
			return rpcerrors.RpcErrorMalformedStream()
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(1); rpcErr != nil {
				return rpcErr
			}
		}
		key := stream
		if key == "rt_transactions" {
			key = types.SubTransactionsProposed
		}
		if rpcErr := scope.add(requestEdge{kind: 1, stream: key}); rpcErr != nil {
			return rpcErr
		}
		sm.removeStream(registration, key)
	}
	if streamsErr != nil {
		return streamsErr
	}

	proposedRaw, proposedTyped := w.RTAccounts, request.RTAccounts
	if w.AccountsProposed != nil || (!w.Present && request.AccountsProposed != nil) {
		proposedRaw, proposedTyped = w.AccountsProposed, request.AccountsProposed
	}
	proposedPresent, proposed, rpcErr := resolveAccounts(w.Present, proposedRaw, proposedTyped, scope)
	if rpcErr != nil {
		return rpcErr
	}
	if proposedPresent {
		if len(proposed) == 0 {
			return rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range proposed {
			if !isValidXRPLAddress(acc) {
				return rpcerrors.RpcErrorActMalformed("Account malformed.")
			}
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(len(proposed)); rpcErr != nil {
				return rpcErr
			}
		}
		proposed = uniqueStrings(proposed)
		for _, account := range proposed {
			if rpcErr := scope.add(requestEdge{kind: 2, stream: types.SubAccountsProposed, account: account}); rpcErr != nil {
				return rpcErr
			}
		}
		sm.removeAccounts(registration, types.SubAccountsProposed, proposed)
	}

	accountsPresent, accounts, rpcErr := resolveAccounts(w.Present, w.Accounts, request.Accounts, scope)
	if rpcErr != nil {
		return rpcErr
	}
	if accountsPresent {
		if len(accounts) == 0 {
			return rpcerrors.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range accounts {
			if !isValidXRPLAddress(acc) {
				return rpcerrors.RpcErrorActMalformed("Account malformed.")
			}
		}
		if !w.Present {
			if rpcErr := scope.consumeRaw(len(accounts)); rpcErr != nil {
				return rpcErr
			}
		}
		accounts = uniqueStrings(accounts)
		for _, account := range accounts {
			if rpcErr := scope.add(requestEdge{kind: 2, stream: types.SubAccounts, account: account}); rpcErr != nil {
				return rpcErr
			}
		}
		sm.removeAccounts(registration, types.SubAccounts, accounts)
	}

	booksPresent, books, booksErr := resolveBooks(w.Present, w.Books, request.Books, scope)
	if booksPresent {
		for _, requestBook := range books {
			canonical, rpcErr := parseBookRequest(requestBook, false)
			if rpcErr != nil {
				return rpcErr
			}
			if !w.Present {
				if rpcErr := scope.consumeRaw(1); rpcErr != nil {
					return rpcErr
				}
			}
			canonicalBooks := []book{canonical}
			if requestBook.Both || requestBook.BothSides {
				canonicalBooks = append(canonicalBooks, canonical.reversed())
			}
			edges := make([]requestEdge, 0, len(canonicalBooks))
			for _, value := range canonicalBooks {
				edges = append(edges, requestEdge{kind: 3, book: value})
			}
			if rpcErr := scope.addMany(edges); rpcErr != nil {
				return rpcErr
			}
			sm.removeBooks(registration, canonicalBooks)
		}
	}
	if booksErr != nil {
		return booksErr
	}

	return nil
}

func (sm *Manager) HasStreamSubscriptions(registration *Registration) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return false
	}
	for key := range record.streams {
		if validStreams[key] {
			return true
		}
	}
	return false
}

func (sm *Manager) GetSubscriberCount(streamType types.SubscriptionType) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	return len(sm.streamIndex[streamType])
}
