package subscription

import (
	"encoding/hex"
	"encoding/json"
	"sync"

	addresscodec "github.com/LeJamon/go-xrpl/codec/addresscodec"
	"github.com/LeJamon/go-xrpl/internal/rpc/types"
)

// validStreams is the exact stream-name set doSubscribe accepts
// (Subscribe.cpp:130-174). Rippled accepts the legacy "rt_transactions"
// name as an alias for "transactions_proposed" (Subscribe.cpp:151-156);
// we accept it the same way and normalise it inside HandleSubscribe.
// Anything else — including internal subscription keys like "accounts",
// "book" and "path_find" — is rejected with rpcSTREAM_MALFORMED.
var validStreams = map[types.SubscriptionType]bool{
	types.SubLedger:               true,
	types.SubTransactions:         true,
	types.SubTransactionsProposed: true,
	"rt_transactions":             true,
	types.SubBookChanges:          true,
	types.SubValidations:          true,
	types.SubManifests:            true,
	types.SubPeerStatus:           true,
	types.SubServer:               true,
	types.SubConsensus:            true,
}

// validUnsubscribeStreams mirrors doUnsubscribe's stream switch
// (Unsubscribe.cpp:61-110): the subscribe set minus book_changes —
// rippled has no unsubBookChanges branch, so unsubscribing it yields
// rpcSTREAM_MALFORMED and the stream only drops with the connection.
var validUnsubscribeStreams = func() map[types.SubscriptionType]bool {
	m := make(map[types.SubscriptionType]bool, len(validStreams))
	for k := range validStreams {
		m[k] = true
	}
	delete(m, types.SubBookChanges)
	return m
}()

// xrpAccountID is the zero AccountID returned by rippled's xrpAccount();
// noAccountID is the noAccount() sentinel (AccountID.cpp:178/:185), an
// explicitly disallowed issuer.
var (
	xrpAccountID = [20]byte{}
	noAccountID  = [20]byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 1}
)

// Manager manages WebSocket subscriptions
type Manager struct {
	Connections map[string]*types.Connection
	mu          sync.RWMutex
}

// NewManager creates a new Manager
func NewManager() *Manager {
	return &Manager{
		Connections: make(map[string]*types.Connection),
	}
}

// AddConnection adds a connection to the subscription manager
func (sm *Manager) AddConnection(conn *types.Connection) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.Connections == nil {
		sm.Connections = make(map[string]*types.Connection)
	}
	sm.Connections[conn.ID] = conn
}

// RemoveConnection removes a connection from the subscription manager
func (sm *Manager) RemoveConnection(connID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.Connections, connID)
}

// HandleSubscribe handles a subscribe request for a connection. The
// caller passes its current role so we can mirror the admin-only gates
// rippled applies to URL-style server-to-server subscriptions
// (Subscribe.cpp:50-53) and to the peer_status stream
// (Subscribe.cpp:161-166). Non-admin callers passing `url` or
// requesting `peer_status` are rejected with rpcNO_PERMISSION.
func (sm *Manager) HandleSubscribe(conn *types.Connection, request types.SubscriptionRequest, isAdmin bool) *types.RpcError {
	if request.URL != "" && !isAdmin {
		return types.RpcErrorNoPermission("subscribe")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Validate and add stream subscriptions. "rt_transactions" is the
	// deprecated alias rippled keeps around (Subscribe.cpp:151-156); we
	// accept it and fold it into the canonical "transactions_proposed"
	// key so downstream broadcasts only need to consider one entry.
	for _, stream := range request.Streams {
		if !validStreams[stream] {
			return types.RpcErrorMalformedStream()
		}
		// peer_status is admin-only (Subscribe.cpp:161-166).
		if stream == types.SubPeerStatus && !isAdmin {
			return types.RpcErrorNoPermission("subscribe")
		}
		key := stream
		if key == "rt_transactions" {
			key = types.SubTransactionsProposed
		}
		conn.Subscriptions[key] = types.SubscriptionConfig{}
	}

	if len(request.Accounts) > 0 {
		// Any bad id fails the whole array with rpcACT_MALFORMED
		// (Subscribe.cpp:192-199, parseAccountIds).
		for _, acc := range request.Accounts {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}

		// Merge with existing accounts if already subscribed
		existing, ok := conn.Subscriptions[types.SubAccounts]
		accounts := request.Accounts
		if ok {
			// Append new accounts avoiding duplicates
			existingMap := make(map[string]bool)
			for _, acc := range existing.Accounts {
				existingMap[acc] = true
			}
			for _, acc := range request.Accounts {
				if !existingMap[acc] {
					accounts = append(accounts, acc)
				}
			}
		}
		conn.Subscriptions[types.SubAccounts] = types.SubscriptionConfig{
			Accounts: accounts,
		}
	}

	if len(request.AccountsProposed) > 0 {
		// rpcACT_MALFORMED on any bad id (Subscribe.cpp:181-188).
		for _, acc := range request.AccountsProposed {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		// Store in a separate subscription type (using accounts for now)
		conn.Subscriptions["accounts_proposed"] = types.SubscriptionConfig{
			Accounts: request.AccountsProposed,
		}
	}

	if len(request.Books) > 0 {
		// Normalised + validated entries get accumulated here. When
		// `both:true` is set on an entry, we additionally append the
		// reversed pair — mirroring rippled Subscribe.cpp:330-337 which
		// calls subBook twice (the request and its reverse) so a single
		// subscriber sees activity on either side of the market.
		var normalised []types.BookRequest

		for _, book := range request.Books {
			if rpcErr := validateBook(book, true); rpcErr != nil {
				return rpcErr
			}

			normalised = append(normalised, book)
			if book.Both {
				reversed := types.BookRequest{
					TakerPays: book.TakerGets,
					TakerGets: book.TakerPays,
					Snapshot:  book.Snapshot,
					Both:      false, // already added the partner
					Taker:     book.Taker,
					Domain:    book.Domain,
				}
				normalised = append(normalised, reversed)
			}
			// Snapshot:true delivery is handled inline by the WebSocket
			// layer (rpc/websocket.go: handleSubscribe → snapshotBook),
			// which has access to the ServiceContainer / LedgerService.
			// The per-book Snapshot flag is preserved on the BookRequest
			// itself, so multi-book subscribers don't collapse to a
			// single "last book" snapshot intent.
		}

		// Each book is recorded in Books — rippled Subscribe.cpp:328-336
		// stores one entry per call to netOps.subBook, with no
		// "primary book" concept. Multi-book subscribers' per-pair
		// state lives on each BookRequest, not in connection-level
		// scalars.
		conn.Subscriptions[types.SubBook] = types.SubscriptionConfig{
			Books: normalised,
		}
	}

	// Handle URL subscriptions
	if request.URL != "" {
		conn.URLSubscription = request.URL
	}

	return nil
}

// isValidXRPLAddress checks if a string is a valid XRPL address
func isValidXRPLAddress(addr string) bool {
	return addresscodec.IsValidClassicAddress(addr)
}

// validateBook runs the book checks rippled applies per entry
// (Subscribe.cpp:236-326, Unsubscribe.cpp:167-245), in rippled's order so
// a request that is malformed in several ways reports the same error both
// implementations would pick. includeTaker is false on the unsubscribe
// path, which carries no taker field. The final isConsistent recheck
// (Subscribe.cpp:322-326) is subsumed: the per-side issuer checks already
// guarantee each side is consistent, and in == out is exactly the
// same-asset comparison below.
func validateBook(book types.BookRequest, includeTaker bool) *types.RpcError {
	// Both sides must be present and an object or null before either is
	// parsed (Subscribe.cpp:238-242); a null side then fails the
	// mandatory-currency check below, like rippled's isMember(currency)
	// on a null value.
	paysSide, rpcErr := bookSideObject(book.TakerPays)
	if rpcErr != nil {
		return rpcErr
	}
	getsSide, rpcErr := bookSideObject(book.TakerGets)
	if rpcErr != nil {
		return rpcErr
	}

	pays, paysIssuer, rpcErr := parseBookSide(paysSide, true)
	if rpcErr != nil {
		return rpcErr
	}
	gets, getsIssuer, rpcErr := parseBookSide(getsSide, false)
	if rpcErr != nil {
		return rpcErr
	}

	// Same asset on both sides is not a market (Subscribe.cpp:292-297).
	if canonCurrency(pays.Currency) == canonCurrency(gets.Currency) && paysIssuer == getsIssuer {
		return types.RpcErrorBadMarket()
	}

	// Optional taker — an unparseable account is rpcBAD_ISSUER
	// (Subscribe.cpp:301-305).
	if includeTaker && book.Taker != "" && !isValidXRPLAddress(book.Taker) {
		return types.RpcErrorBadIssuer()
	}

	// Optional domain (Subscribe.cpp:308-315).
	if book.Domain != "" && !isValidDomainHex(book.Domain) {
		return types.RpcErrorDomainMalformed("")
	}

	return nil
}

// bookSideObject decodes one side of a book entry into its key/value
// form, reporting rpcINVALID_PARAMS for a missing or non-object value.
// A null side decodes to an empty map.
func bookSideObject(raw json.RawMessage) (map[string]json.RawMessage, *types.RpcError) {
	if raw == nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	var side map[string]json.RawMessage
	if err := json.Unmarshal(raw, &side); err != nil {
		return nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	return side, nil
}

// parseBookSide validates one side of a book entry. taker_pays maps to
// rippled's "source" (src*) error codes, taker_gets to "destination"
// (dst*); messages are the rpcError defaults from ErrorCodes.cpp since
// Subscribe.cpp returns bare rpcError(code) at every site.
func parseBookSide(side map[string]json.RawMessage, isPays bool) (spec types.CurrencySpec, issuerID [20]byte, _ *types.RpcError) {
	curMalformed := func() *types.RpcError {
		if isPays {
			return types.RpcErrorSrcCurMalformed("Source currency is malformed.")
		}
		return types.RpcErrorDstAmtMalformed("Destination amount/currency/issuer is malformed.")
	}
	isrMalformed := func() *types.RpcError {
		if isPays {
			return types.RpcErrorSrcIsrMalformed("Source issuer is malformed.")
		}
		return types.RpcErrorDstIsrMalformed("Destination issuer is malformed.")
	}

	// Mandatory currency (Subscribe.cpp:248-255 / :270-277).
	rawCurrency, ok := side["currency"]
	if !ok {
		return spec, issuerID, curMalformed()
	}
	if err := json.Unmarshal(rawCurrency, &spec.Currency); err != nil {
		return spec, issuerID, curMalformed()
	}
	if !isValidCurrencyCode(spec.Currency) {
		return spec, issuerID, curMalformed()
	}

	// Optional issuer plus the illegal-issuer cross-checks: XRP must not
	// carry an issuer, IOUs must, and the noAccount() sentinel is never
	// allowed (Subscribe.cpp:257-268 / :279-290).
	hasIssuer := false
	if rawIssuer, ok := side["issuer"]; ok {
		if err := json.Unmarshal(rawIssuer, &spec.Issuer); err != nil {
			return spec, issuerID, isrMalformed()
		}
		_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(spec.Issuer)
		if err != nil {
			return spec, issuerID, isrMalformed()
		}
		copy(issuerID[:], idBytes)
		if issuerID == noAccountID {
			return spec, issuerID, isrMalformed()
		}
		hasIssuer = true
	}
	isXRPCurrency := spec.Currency == "" || spec.Currency == "XRP"
	isXRPIssuer := !hasIssuer || issuerID == xrpAccountID
	if isXRPCurrency != isXRPIssuer {
		return spec, issuerID, isrMalformed()
	}

	return spec, issuerID, nil
}

// isValidCurrencyCode accepts what rippled's to_currency does: XRP (or
// empty, which to_currency reads as XRP), a 3-character ISO-style code,
// or a 40-hex Currency160.
func isValidCurrencyCode(currency string) bool {
	if currency == "" || currency == "XRP" {
		return true
	}
	if len(currency) == 3 {
		for _, c := range currency {
			if !isIsoCurrencyChar(c) {
				return false
			}
		}
		return true
	}
	if len(currency) == 40 {
		_, err := hex.DecodeString(currency)
		return err == nil
	}
	return false
}

func isIsoCurrencyChar(c rune) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '?' || c == '!' || c == '@' || c == '#' || c == '$' ||
		c == '%' || c == '^' || c == '&' || c == '*' || c == '<' ||
		c == '>' || c == '(' || c == ')' || c == '{' || c == '}' ||
		c == '[' || c == ']' || c == '|':
		return true
	}
	return false
}

// canonCurrency folds the empty currency into "XRP" — to_currency reads
// both as the XRP Currency160 — so the same-asset comparison treats them
// as equal.
func canonCurrency(c string) string {
	if c == "" {
		return "XRP"
	}
	return c
}

// isValidDomainHex mirrors uint256::parseHex acceptance the same way the
// book_offers handler does: the literal "0" or exactly 64 hex digits.
func isValidDomainHex(domain string) bool {
	if domain == "0" {
		return true
	}
	if len(domain) != 64 {
		return false
	}
	_, err := hex.DecodeString(domain)
	return err == nil
}

// HandleUnsubscribe handles an unsubscribe request for a connection.
// The caller supplies its current admin status so the URL-style gate
// in Unsubscribe.cpp:46-48 is honored symmetrically with the subscribe
// path. The deprecated `rt_transactions` stream name is normalised to
// `transactions_proposed` so a client that subscribed with the alias
// can also unsubscribe with the alias (Unsubscribe.cpp:88-93).
func (sm *Manager) HandleUnsubscribe(conn *types.Connection, request types.SubscriptionRequest, isAdmin bool) *types.RpcError {
	if request.URL != "" && !isAdmin {
		return types.RpcErrorNoPermission("unsubscribe")
	}

	sm.mu.Lock()
	defer sm.mu.Unlock()

	for _, stream := range request.Streams {
		// Unknown names are rpcSTREAM_MALFORMED; like rippled, streams
		// earlier in the array are already unsubscribed when a later one
		// fails (Unsubscribe.cpp:66-109).
		if !validUnsubscribeStreams[stream] {
			return types.RpcErrorMalformedStream()
		}
		key := stream
		if key == "rt_transactions" {
			key = types.SubTransactionsProposed
		}
		delete(conn.Subscriptions, key)
	}

	if len(request.Accounts) > 0 {
		// rpcACT_MALFORMED on any bad id (Unsubscribe.cpp:127-135).
		for _, acc := range request.Accounts {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		if existing, ok := conn.Subscriptions[types.SubAccounts]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range request.Accounts {
				accountsToRemove[acc] = true
			}
			var remainingAccounts []string
			for _, acc := range existing.Accounts {
				if !accountsToRemove[acc] {
					remainingAccounts = append(remainingAccounts, acc)
				}
			}
			if len(remainingAccounts) > 0 {
				conn.Subscriptions[types.SubAccounts] = types.SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, types.SubAccounts)
			}
		}
	}

	// Remove specific accounts_proposed subscriptions
	if len(request.AccountsProposed) > 0 {
		// rpcACT_MALFORMED on any bad id (Unsubscribe.cpp:116-124).
		for _, acc := range request.AccountsProposed {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		if existing, ok := conn.Subscriptions["accounts_proposed"]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range request.AccountsProposed {
				accountsToRemove[acc] = true
			}
			var remainingAccounts []string
			for _, acc := range existing.Accounts {
				if !accountsToRemove[acc] {
					remainingAccounts = append(remainingAccounts, acc)
				}
			}
			if len(remainingAccounts) > 0 {
				conn.Subscriptions["accounts_proposed"] = types.SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, "accounts_proposed")
			}
		}
	}

	if len(request.Books) > 0 {
		// Unsubscribe runs the same book validation as subscribe minus
		// the taker field, which it does not carry (Unsubscribe.cpp:
		// 167-245).
		for _, book := range request.Books {
			if rpcErr := validateBook(book, false); rpcErr != nil {
				return rpcErr
			}
		}
		delete(conn.Subscriptions, types.SubBook)
	}

	// Handle URL unsubscription
	if request.URL != "" {
		conn.URLSubscription = ""
	}

	return nil
}

// BroadcastToStream sends a message to every connection subscribed to a
// stream. Broadcasts snapshot subscriber connections under sm.mu, then
// send after the lock is released — a slow consumer never stalls
// HandleSubscribe / HandleUnsubscribe / RemoveConnection or other
// broadcasts (#428 race fix). Delivery uses types.Connection.TrySend so
// the per-connection consecutive-drop counter is updated and the
// connection is disconnected after MaxConsecutiveDrops back-to-back
// failures — unifies the slow-consumer policy across all outbound paths.
func (sm *Manager) BroadcastToStream(streamType types.SubscriptionType, data []byte, _ any) {
	deliver(sm.collectStreamTargets(streamType), data)
}

func (sm *Manager) collectStreamTargets(streamType types.SubscriptionType) []*types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	targets := make([]*types.Connection, 0, len(sm.Connections))
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			targets = append(targets, conn)
		}
	}
	return targets
}

func deliver(targets []*types.Connection, data []byte) {
	for _, c := range targets {
		c.TrySend(data)
	}
}

// BroadcastToAccounts sends a message to every connection subscribed to
// any of the named accounts on the SubAccounts stream.
func (sm *Manager) BroadcastToAccounts(data []byte, accounts []string) {
	deliver(sm.collectAccountTargets(types.SubAccounts, accounts), data)
}

// BroadcastToAccountsProposed sends a message to accounts_proposed
// subscribers.
func (sm *Manager) BroadcastToAccountsProposed(data []byte, accounts []string) {
	deliver(sm.collectAccountTargets("accounts_proposed", accounts), data)
}

func (sm *Manager) collectAccountTargets(stream types.SubscriptionType, accounts []string) []*types.Connection {
	if len(accounts) == 0 {
		return nil
	}
	accountSet := make(map[string]bool, len(accounts))
	for _, acc := range accounts {
		accountSet[acc] = true
	}
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	var targets []*types.Connection
	for _, conn := range sm.Connections {
		cfg, ok := conn.Subscriptions[stream]
		if !ok {
			continue
		}
		for _, subAcc := range cfg.Accounts {
			if accountSet[subAcc] {
				targets = append(targets, conn)
				break
			}
		}
	}
	return targets
}

// BroadcastToOrderBook sends a message to order book subscribers whose
// configured TakerGets/TakerPays match the broadcast's currency pair.
func (sm *Manager) BroadcastToOrderBook(data []byte, takerGets, takerPays types.CurrencySpec) {
	deliver(sm.collectOrderBookTargets(takerGets, takerPays), data)
}

func (sm *Manager) collectOrderBookTargets(takerGets, takerPays types.CurrencySpec) []*types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	if len(sm.Connections) == 0 {
		return nil
	}
	var targets []*types.Connection
	for _, conn := range sm.Connections {
		cfg, ok := conn.Subscriptions[types.SubBook]
		if !ok {
			continue
		}
		// Each entry in cfg.Books is a separate (taker_gets, taker_pays)
		// subscription registered by HandleSubscribe (including
		// `both:true` reverse-side expansion). The legacy scalar
		// TakerGets/TakerPays fallback was removed when multi-book
		// state collapsed to per-BookRequest storage.
		matched := false
		for _, b := range cfg.Books {
			if types.BookMatchesCurrency(b, takerGets, takerPays) {
				matched = true
				break
			}
		}
		if matched {
			targets = append(targets, conn)
		}
	}
	return targets
}

// GetSubscriberCount returns the number of subscribers for a stream type
func (sm *Manager) GetSubscriberCount(streamType types.SubscriptionType) int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	count := 0
	for _, conn := range sm.Connections {
		if _, ok := conn.Subscriptions[streamType]; ok {
			count++
		}
	}
	return count
}

// ConnectionCount returns the number of active connections
func (sm *Manager) ConnectionCount() int {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return len(sm.Connections)
}

// GetConnection returns a connection by ID
func (sm *Manager) GetConnection(connID string) *types.Connection {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	return sm.Connections[connID]
}

// IsSubscribed checks if a connection is subscribed to a stream type
func (sm *Manager) IsSubscribed(connID string, streamType types.SubscriptionType) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return false
	}
	_, ok := conn.Subscriptions[streamType]
	return ok
}

// GetConnectionSubscriptions returns the subscriptions for a connection
func (sm *Manager) GetConnectionSubscriptions(connID string) map[types.SubscriptionType]types.SubscriptionConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return nil
	}
	return conn.Subscriptions
}

// GetSubscribeResponse creates a subscribe confirmation response
func (sm *Manager) GetSubscribeResponse(ledgerIndex uint32, ledgerHash string, ledgerTime uint32, feeBase uint64, reserveBase uint64, reserveInc uint64) types.SubscribeResponse {
	return types.SubscribeResponse{
		Status:      "success",
		LedgerIndex: ledgerIndex,
		LedgerHash:  ledgerHash,
		LedgerTime:  ledgerTime,
		FeeBase:     feeBase,
		ReserveBase: reserveBase,
		ReserveInc:  reserveInc,
	}
}
