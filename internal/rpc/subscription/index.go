package subscription

import (
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/LeJamon/go-xrpl/internal/rpc/rpcerrors"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

type Limits struct {
	MaxItemsPerRequest    int
	MaxItemsPerConnection int
	MaxItemsGlobal        int
}

const (
	defaultMaxItemsPerRequest    = 2_048
	defaultMaxItemsPerConnection = 8_192
	defaultMaxItemsGlobal        = 1_048_576
)

func defaultLimits() Limits {
	return Limits{
		MaxItemsPerRequest:    defaultMaxItemsPerRequest,
		MaxItemsPerConnection: defaultMaxItemsPerConnection,
		MaxItemsGlobal:        defaultMaxItemsGlobal,
	}
}

type Registration struct {
	manager    *Manager
	record     *connectionRecord
	generation uint64
}

type requestEdge struct {
	kind    uint8
	stream  types.SubscriptionType
	account string
	book    book
}

type RequestScope struct {
	manager *Manager
	seen    map[requestEdge]struct{}
	raw     int
}

type Snapshot struct {
	streams  map[types.SubscriptionType]struct{}
	accounts map[types.SubscriptionType][]string
	books    int
	items    int
}

func (s Snapshot) Has(stream types.SubscriptionType) bool {
	if stream == types.SubBook {
		return s.books != 0
	}
	if _, ok := s.streams[stream]; ok {
		return true
	}
	return len(s.accounts[stream]) != 0
}

func (s Snapshot) Accounts(stream types.SubscriptionType) []string {
	return append([]string(nil), s.accounts[stream]...)
}

func (s Snapshot) BookCount() int { return s.books }
func (s Snapshot) ItemCount() int { return s.items }

func (r *Registration) Snapshot() Snapshot {
	if r == nil || r.manager == nil {
		return Snapshot{}
	}
	return r.manager.snapshot(r)
}

type connectionRecord struct {
	conn       *Connection
	generation uint64
	streams    map[types.SubscriptionType]struct{}
	accounts   map[types.SubscriptionType]map[string]struct{}
	books      map[book]struct{}
	items      int
	detaching  bool
	deliveryMu sync.RWMutex
	active     bool
}

func newConnectionRecord(conn *Connection, generation uint64) *connectionRecord {
	return &connectionRecord{
		conn:       conn,
		generation: generation,
		streams:    make(map[types.SubscriptionType]struct{}),
		accounts:   make(map[types.SubscriptionType]map[string]struct{}),
		books:      make(map[book]struct{}),
		active:     true,
	}
}

func validateLimits(limits Limits) error {
	if limits.MaxItemsPerRequest <= 0 || limits.MaxItemsPerConnection <= 0 || limits.MaxItemsGlobal <= 0 {
		return errors.New("subscription limits must be positive")
	}
	if limits.MaxItemsPerRequest > limits.MaxItemsPerConnection || limits.MaxItemsPerConnection > limits.MaxItemsGlobal {
		return errors.New("subscription limits must be monotonically nondecreasing")
	}
	return nil
}

func NewManagerWithLimits(limits Limits) (*Manager, error) {
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	return newManager(limits), nil
}

func (sm *Manager) NewRequestScope() *RequestScope {
	return &RequestScope{manager: sm, seen: make(map[requestEdge]struct{})}
}

func (scope *RequestScope) add(edge requestEdge) *rpcerrors.RpcError {
	return scope.addMany([]requestEdge{edge})
}

func (scope *RequestScope) addMany(edges []requestEdge) *rpcerrors.RpcError {
	if scope == nil || scope.manager == nil {
		return rpcerrors.RpcErrorInternal()
	}
	additions := make([]requestEdge, 0, len(edges))
	pending := make(map[requestEdge]struct{}, len(edges))
	for _, edge := range edges {
		if _, exists := scope.seen[edge]; exists {
			continue
		}
		if _, exists := pending[edge]; exists {
			continue
		}
		pending[edge] = struct{}{}
		additions = append(additions, edge)
	}
	if len(additions) > scope.manager.limits.MaxItemsPerRequest-len(scope.seen) {
		scope.manager.mu.Lock()
		scope.manager.recordLimitRejectionLocked("request")
		scope.manager.mu.Unlock()
		return rpcerrors.RpcErrorTooBusy()
	}
	for _, edge := range additions {
		scope.seen[edge] = struct{}{}
	}
	return nil
}

func (scope *RequestScope) consumeRaw(count int) *rpcerrors.RpcError {
	if scope == nil || scope.manager == nil || count < 0 {
		return rpcerrors.RpcErrorInternal()
	}
	if count > scope.manager.limits.MaxItemsPerRequest-scope.raw {
		scope.manager.mu.Lock()
		scope.manager.recordLimitRejectionLocked("request")
		scope.manager.mu.Unlock()
		return rpcerrors.RpcErrorTooBusy()
	}
	scope.raw += count
	return nil
}

func (sm *Manager) recordLocked(registration *Registration) *connectionRecord {
	if registration == nil || registration.manager != sm || registration.record == nil {
		return nil
	}
	record := registration.record
	current := sm.connections[record.conn.ID()]
	if current != record || record.generation != registration.generation || record.detaching {
		return nil
	}
	return record
}

func (sm *Manager) reserveLocked(record *connectionRecord, delta int) *rpcerrors.RpcError {
	if delta <= 0 {
		return nil
	}
	if record.items+delta > sm.limits.MaxItemsPerConnection {
		sm.recordLimitRejectionLocked("connection")
		return rpcerrors.RpcErrorTooBusy()
	}
	if sm.items+delta > sm.limits.MaxItemsGlobal {
		sm.recordLimitRejectionLocked("global")
		return rpcerrors.RpcErrorTooBusy()
	}
	return nil
}

func (sm *Manager) recordLimitRejectionLocked(kind string) {
	var value *uint64
	switch kind {
	case "request":
		value = &sm.requestLimitRejections
	case "connection":
		value = &sm.connectionLimitRejections
	default:
		value = &sm.globalLimitRejections
	}
	(*value)++
	count := *value
	if count&(count-1) == 0 {
		slog.Warn("subscription capacity rejected", "kind", kind, "count", count, "connections", len(sm.connections), "items", sm.items)
	}
}

func (sm *Manager) addStream(registration *Registration, stream types.SubscriptionType) *rpcerrors.RpcError {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return rpcerrors.RpcErrorInternal()
	}
	if _, exists := record.streams[stream]; exists {
		return nil
	}
	if rpcErr := sm.reserveLocked(record, 1); rpcErr != nil {
		return rpcErr
	}
	record.streams[stream] = struct{}{}
	listeners := sm.streamIndex[stream]
	if listeners == nil {
		listeners = make(map[*connectionRecord]struct{})
		sm.streamIndex[stream] = listeners
	}
	listeners[record] = struct{}{}
	record.items++
	sm.items++
	return nil
}

func (sm *Manager) removeStream(registration *Registration, stream types.SubscriptionType) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return
	}
	if _, exists := record.streams[stream]; !exists {
		return
	}
	delete(record.streams, stream)
	delete(sm.streamIndex[stream], record)
	if len(sm.streamIndex[stream]) == 0 {
		delete(sm.streamIndex, stream)
	}
	record.items--
	sm.items--
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func (sm *Manager) addAccounts(registration *Registration, stream types.SubscriptionType, accounts []string) *rpcerrors.RpcError {
	accounts = uniqueStrings(accounts)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return rpcerrors.RpcErrorInternal()
	}
	owned := record.accounts[stream]
	if owned == nil {
		owned = make(map[string]struct{})
	}
	delta := 0
	for _, account := range accounts {
		if _, exists := owned[account]; !exists {
			delta++
		}
	}
	if rpcErr := sm.reserveLocked(record, delta); rpcErr != nil {
		return rpcErr
	}
	for _, account := range accounts {
		if _, exists := owned[account]; exists {
			continue
		}
		owned[account] = struct{}{}
		byAccount := sm.accountIndex[stream]
		if byAccount == nil {
			byAccount = make(map[string]map[*connectionRecord]struct{})
			sm.accountIndex[stream] = byAccount
		}
		listeners := byAccount[account]
		if listeners == nil {
			listeners = make(map[*connectionRecord]struct{})
			byAccount[account] = listeners
		}
		listeners[record] = struct{}{}
	}
	record.accounts[stream] = owned
	record.items += delta
	sm.items += delta
	return nil
}

func (sm *Manager) removeAccounts(registration *Registration, stream types.SubscriptionType, accounts []string) {
	accounts = uniqueStrings(accounts)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return
	}
	owned := record.accounts[stream]
	for _, account := range accounts {
		if _, exists := owned[account]; !exists {
			continue
		}
		delete(owned, account)
		listeners := sm.accountIndex[stream][account]
		delete(listeners, record)
		if len(listeners) == 0 {
			delete(sm.accountIndex[stream], account)
		}
		record.items--
		sm.items--
	}
	if len(sm.accountIndex[stream]) == 0 {
		delete(sm.accountIndex, stream)
	}
	if len(owned) == 0 {
		delete(record.accounts, stream)
	}
}

func uniqueBooks(books []book) []book {
	seen := make(map[book]struct{}, len(books))
	unique := make([]book, 0, len(books))
	for _, value := range books {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	return unique
}

func (sm *Manager) addBooks(registration *Registration, books []book) *rpcerrors.RpcError {
	books = uniqueBooks(books)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return rpcerrors.RpcErrorInternal()
	}
	delta := 0
	for _, value := range books {
		if _, exists := record.books[value]; !exists {
			delta++
		}
	}
	if rpcErr := sm.reserveLocked(record, delta); rpcErr != nil {
		return rpcErr
	}
	for _, value := range books {
		if _, exists := record.books[value]; exists {
			continue
		}
		record.books[value] = struct{}{}
		listeners := sm.bookIndex[value]
		if listeners == nil {
			listeners = make(map[*connectionRecord]struct{})
			sm.bookIndex[value] = listeners
		}
		listeners[record] = struct{}{}
	}
	record.items += delta
	sm.items += delta
	return nil
}

func (sm *Manager) removeBooks(registration *Registration, books []book) {
	books = uniqueBooks(books)
	sm.mu.Lock()
	defer sm.mu.Unlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return
	}
	removed := 0
	for _, value := range books {
		if _, exists := record.books[value]; !exists {
			continue
		}
		delete(record.books, value)
		delete(sm.bookIndex[value], record)
		if len(sm.bookIndex[value]) == 0 {
			delete(sm.bookIndex, value)
		}
		removed++
	}
	record.items -= removed
	sm.items -= removed
}

func (sm *Manager) snapshot(registration *Registration) Snapshot {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	record := sm.recordLocked(registration)
	if record == nil {
		return Snapshot{}
	}
	snapshot := Snapshot{
		streams:  make(map[types.SubscriptionType]struct{}, len(record.streams)),
		accounts: make(map[types.SubscriptionType][]string, len(record.accounts)),
		books:    len(record.books),
		items:    record.items,
	}
	for stream := range record.streams {
		snapshot.streams[stream] = struct{}{}
	}
	for stream, accounts := range record.accounts {
		for account := range accounts {
			snapshot.accounts[stream] = append(snapshot.accounts[stream], account)
		}
	}
	return snapshot
}

func (sm *Manager) detachIndexesLocked(record *connectionRecord) {
	for stream := range record.streams {
		delete(sm.streamIndex[stream], record)
		if len(sm.streamIndex[stream]) == 0 {
			delete(sm.streamIndex, stream)
		}
	}
	for stream, accounts := range record.accounts {
		for account := range accounts {
			listeners := sm.accountIndex[stream][account]
			delete(listeners, record)
			if len(listeners) == 0 {
				delete(sm.accountIndex[stream], account)
			}
		}
		if len(sm.accountIndex[stream]) == 0 {
			delete(sm.accountIndex, stream)
		}
	}
	for book := range record.books {
		delete(sm.bookIndex[book], record)
		if len(sm.bookIndex[book]) == 0 {
			delete(sm.bookIndex, book)
		}
	}
	sm.items -= record.items
}

func (sm *Manager) detachRecord(registration *Registration) bool {
	sm.mu.Lock()
	record := sm.recordLocked(registration)
	if record == nil {
		sm.mu.Unlock()
		return false
	}
	record.detaching = true
	sm.detachIndexesLocked(record)
	sm.mu.Unlock()

	record.deliveryMu.Lock()
	record.active = false
	record.deliveryMu.Unlock()

	sm.mu.Lock()
	if current := sm.connections[record.conn.ID()]; current == record && current.generation == registration.generation {
		delete(sm.connections, record.conn.ID())
	}
	sm.mu.Unlock()
	return true
}

func (sm *Manager) checkInvariants() error {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	total := 0
	for id, record := range sm.connections {
		if record == nil || record.conn == nil || record.conn.ID() != id {
			return fmt.Errorf("invalid connection record for %q", id)
		}
		if record.detaching {
			return fmt.Errorf("detaching connection remains registered: %q", id)
		}
		items := len(record.streams) + len(record.books)
		for _, accounts := range record.accounts {
			items += len(accounts)
		}
		if items != record.items {
			return fmt.Errorf("connection %q items=%d, indexed=%d", id, record.items, items)
		}
		total += items
		for stream := range record.streams {
			if _, ok := sm.streamIndex[stream][record]; !ok {
				return fmt.Errorf("connection %q missing stream reverse edge %q", id, stream)
			}
		}
		for stream, accounts := range record.accounts {
			for account := range accounts {
				if _, ok := sm.accountIndex[stream][account][record]; !ok {
					return fmt.Errorf("connection %q missing account reverse edge %q/%q", id, stream, account)
				}
			}
		}
		for value := range record.books {
			if _, ok := sm.bookIndex[value][record]; !ok {
				return fmt.Errorf("connection %q missing book reverse edge", id)
			}
		}
	}
	if total != sm.items {
		return fmt.Errorf("manager items=%d, indexed=%d", sm.items, total)
	}
	for stream, listeners := range sm.streamIndex {
		if len(listeners) == 0 {
			return fmt.Errorf("empty stream index %q", stream)
		}
		for record := range listeners {
			if current := sm.connections[record.conn.ID()]; current != record {
				return fmt.Errorf("stream index %q contains unregistered record", stream)
			}
			if _, ok := record.streams[stream]; !ok {
				return fmt.Errorf("stream index %q missing connection edge", stream)
			}
		}
	}
	for stream, accounts := range sm.accountIndex {
		if len(accounts) == 0 {
			return fmt.Errorf("empty account stream index %q", stream)
		}
		for account, listeners := range accounts {
			if len(listeners) == 0 {
				return fmt.Errorf("empty account index %q/%q", stream, account)
			}
			for record := range listeners {
				if current := sm.connections[record.conn.ID()]; current != record {
					return fmt.Errorf("account index %q/%q contains unregistered record", stream, account)
				}
				if _, ok := record.accounts[stream][account]; !ok {
					return fmt.Errorf("account index %q/%q missing connection edge", stream, account)
				}
			}
		}
	}
	for value, listeners := range sm.bookIndex {
		if len(listeners) == 0 {
			return errors.New("empty book index")
		}
		for record := range listeners {
			if current := sm.connections[record.conn.ID()]; current != record {
				return errors.New("book index contains unregistered record")
			}
			if _, ok := record.books[value]; !ok {
				return errors.New("book index missing connection edge")
			}
		}
	}
	return nil
}
