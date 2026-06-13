package subscription

import (
	"encoding/hex"
	"encoding/json"
	"maps"
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
// caller passes its current role so we can mirror the admin-only gate
// rippled applies to the peer_status stream (Subscribe.cpp:161-166);
// non-admin callers requesting `peer_status` are rejected with
// rpcNO_PERMISSION. The url (RPCSub) branch is resolved by the caller
// before reaching the manager: url requests are routed to the
// URLSubscriptionRegistry, whose per-url connection is what gets
// subscribed here.
func (sm *Manager) HandleSubscribe(conn *types.Connection, request types.SubscriptionRequest, isAdmin bool) *types.RpcError {
	w := request.WireArrays()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	// Streams. "rt_transactions" is the deprecated alias rippled keeps around
	// (Subscribe.cpp:151-156); we fold it into the canonical
	// "transactions_proposed" key so broadcasts consider one entry.
	_, streams, rpcErr := resolveStreams(w.Present, w.Streams, request.Streams)
	if rpcErr != nil {
		return rpcErr
	}
	for _, stream := range streams {
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

	// accounts (Subscribe.cpp:192-200): a present-but-empty array, a non-string
	// id, or an unparseable id all make parseAccountIds return an empty set →
	// rpcACT_MALFORMED.
	accountsPresent, accounts, rpcErr := resolveAccounts(w.Present, w.Accounts, request.Accounts)
	if rpcErr != nil {
		return rpcErr
	}
	if accountsPresent {
		if len(accounts) == 0 {
			return types.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range accounts {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		// Accumulate onto the existing subscription rather than replacing it.
		existing := conn.Subscriptions[types.SubAccounts]
		conn.Subscriptions[types.SubAccounts] = types.SubscriptionConfig{
			Accounts: mergeAccounts(existing.Accounts, accounts),
		}
	}

	// accounts_proposed (Subscribe.cpp:181-189), same semantics as accounts.
	proposedPresent, proposed, rpcErr := resolveAccounts(w.Present, w.AccountsProposed, request.AccountsProposed)
	if rpcErr != nil {
		return rpcErr
	}
	if proposedPresent {
		if len(proposed) == 0 {
			return types.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range proposed {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		// Accumulate, mirroring the accounts branch above (rippled's
		// subAccount with rt=true).
		existing := conn.Subscriptions[types.SubAccountsProposed]
		conn.Subscriptions[types.SubAccountsProposed] = types.SubscriptionConfig{
			Accounts: mergeAccounts(existing.Accounts, proposed),
		}
	}

	// books (Subscribe.cpp:231-336): an empty array subscribes nothing. When
	// `both:true` is set we append the reversed pair too, mirroring the second
	// subBook call (Subscribe.cpp:330-337) so one subscriber sees either side.
	// Snapshot delivery is done by the WebSocket layer (websocket.go
	// handleSubscribe); the per-book Snapshot flag is preserved on each entry.
	booksPresent, books, rpcErr := resolveBooks(w.Present, w.Books, request.Books)
	if rpcErr != nil {
		return rpcErr
	}
	if booksPresent {
		var normalised []types.BookRequest
		for _, book := range books {
			if rpcErr := validateBook(book, true); rpcErr != nil {
				return rpcErr
			}
			normalised = append(normalised, book)
			if book.Both {
				normalised = append(normalised, reverseBook(book))
			}
		}
		if len(normalised) > 0 {
			existing := conn.Subscriptions[types.SubBook]
			conn.Subscriptions[types.SubBook] = types.SubscriptionConfig{
				Books: mergeBooks(existing.Books, normalised),
			}
		}
	}

	return nil
}

// isValidXRPLAddress checks if a string is a valid XRPL address
func isValidXRPLAddress(addr string) bool {
	return addresscodec.IsValidClassicAddress(addr)
}

// mergeAccounts accumulates incoming account ids onto the existing set,
// preserving order and skipping ids already present. rippled's subAccount
// inserts into the connection's existing listener set across repeated
// subscribe calls rather than replacing it, so a later subscribe must not
// drop accounts subscribed earlier.
func mergeAccounts(existing, incoming []string) []string {
	merged := append([]string(nil), existing...)
	seen := make(map[string]bool, len(existing))
	for _, acc := range existing {
		seen[acc] = true
	}
	for _, acc := range incoming {
		if !seen[acc] {
			seen[acc] = true
			merged = append(merged, acc)
		}
	}
	return merged
}

// reverseBook swaps a book's pays/gets sides, used to register (and
// unregister) the opposite side of a both:true subscription.
func reverseBook(b types.BookRequest) types.BookRequest {
	return types.BookRequest{
		TakerPays: b.TakerGets,
		TakerGets: b.TakerPays,
		Snapshot:  b.Snapshot,
		Both:      false,
		Taker:     b.Taker,
		Domain:    b.Domain,
	}
}

// bookKey identifies a book subscription by its parsed currency pair and
// domain — the fields that decide which broadcasts it receives. Parsing to the
// canonical 160-bit currency/issuer (rather than comparing the raw request
// bytes) folds a currency and its 40-hex form, and the various XRP spellings,
// onto a single key, matching rippled's Book{in,out,domain} identity
// (Book.h:79-84). Without it a re-subscribe or unsubscribe that spells the
// same market differently would slip past dedup/removal. Snapshot and taker
// are per-request and excluded.
func bookKey(b types.BookRequest) string {
	paysCur, paysIsr, paysOK := bookSideIDs(b.TakerPays, true)
	getsCur, getsIsr, getsOK := bookSideIDs(b.TakerGets, false)
	if paysOK && getsOK {
		return string(paysCur[:]) + string(paysIsr[:]) +
			string(getsCur[:]) + string(getsIsr[:]) + "\x00" + b.Domain
	}
	// Fallback for inputs that fail to parse (should not occur once
	// validateBook has accepted the entry): compare the raw request bytes.
	return string(b.TakerPays) + "\x00" + string(b.TakerGets) + "\x00" + b.Domain
}

// bookSideIDs parses one side of a book entry into its canonical 160-bit
// currency and issuer ids, reporting ok=false when the side is missing or
// malformed.
func bookSideIDs(raw json.RawMessage, isPays bool) (currencyID, issuerID [20]byte, ok bool) {
	side, rpcErr := bookSideObject(raw)
	if rpcErr != nil {
		return currencyID, issuerID, false
	}
	currencyID, issuerID, rpcErr = parseBookSide(side, isPays)
	if rpcErr != nil {
		return currencyID, issuerID, false
	}
	return currencyID, issuerID, true
}

// mergeBooks accumulates incoming book subscriptions onto the existing set,
// skipping markets already subscribed. rippled calls subBook once per entry
// rather than replacing the whole set, so a second subscribe must not wipe
// an earlier book.
func mergeBooks(existing, incoming []types.BookRequest) []types.BookRequest {
	merged := append([]types.BookRequest(nil), existing...)
	seen := make(map[string]bool, len(existing))
	for _, b := range existing {
		seen[bookKey(b)] = true
	}
	for _, b := range incoming {
		k := bookKey(b)
		if !seen[k] {
			seen[k] = true
			merged = append(merged, b)
		}
	}
	return merged
}

// removeBooks returns existing minus every market named in remove (matched
// by bookKey), mirroring rippled's per-book unsubBook — unsubscribing a
// market leaves the connection's other book subscriptions intact.
func removeBooks(existing, remove []types.BookRequest) []types.BookRequest {
	removeSet := make(map[string]bool, len(remove))
	for _, b := range remove {
		removeSet[bookKey(b)] = true
	}
	var remaining []types.BookRequest
	for _, b := range existing {
		if !removeSet[bookKey(b)] {
			remaining = append(remaining, b)
		}
	}
	return remaining
}

// jsonIsArray reports whether a raw JSON value (already valid JSON) is an
// array. rippled rejects every non-array shape — null, number, string,
// bool, object — that a typed Go slice would silently collapse to nil.
func jsonIsArray(raw json.RawMessage) bool {
	for _, b := range raw {
		switch b {
		case ' ', '\t', '\n', '\r':
			continue
		case '[':
			return true
		default:
			return false
		}
	}
	return false
}

// wireArrayElements gives rippled's isMember/isArray view of a wire field:
// present is false for an absent field; isArray is false for a null or
// non-array value (the caller maps that to rpcINVALID_PARAMS); otherwise the
// raw elements, possibly empty.
func wireArrayElements(raw json.RawMessage) (present, isArray bool, elements []json.RawMessage) {
	if raw == nil {
		return false, false, nil
	}
	if !jsonIsArray(raw) {
		return true, false, nil
	}
	_ = json.Unmarshal(raw, &elements)
	return true, true, elements
}

// resolveStreams resolves the streams field against rippled's checks: a
// non-array value is rpcINVALID_PARAMS (Subscribe.cpp:118-122), a non-string
// entry rpcSTREAM_MALFORMED (Subscribe.cpp:126-127). When the request was
// built directly in Go (not wire-decoded) the typed slice is used as-is.
func resolveStreams(wireDecoded bool, raw json.RawMessage, typed []types.SubscriptionType) (present bool, streams []types.SubscriptionType, rpcErr *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements := wireArrayElements(raw)
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	streams = make([]types.SubscriptionType, 0, len(elements))
	for _, el := range elements {
		var s string
		if json.Unmarshal(el, &s) != nil {
			return true, nil, types.RpcErrorMalformedStream()
		}
		streams = append(streams, types.SubscriptionType(s))
	}
	return true, streams, nil
}

// resolveAccounts resolves an accounts / accounts_proposed field to rippled's
// parseAccountIds view (Subscribe.cpp:181-200, Unsubscribe.cpp:113-136): a
// null or non-array value is rpcINVALID_PARAMS; a non-string element collapses
// the set to empty (returned as a nil ids slice), which the caller — together
// with the empty-array and bad-id cases — reports as rpcACT_MALFORMED.
func resolveAccounts(wireDecoded bool, raw json.RawMessage, typed []string) (present bool, ids []string, rpcErr *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements := wireArrayElements(raw)
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	ids = make([]string, 0, len(elements))
	for _, el := range elements {
		var s string
		if json.Unmarshal(el, &s) != nil {
			return true, nil, nil
		}
		ids = append(ids, s)
	}
	return true, ids, nil
}

// resolveBooks resolves the books field: a non-array value or a non-object
// entry is rpcINVALID_PARAMS (Subscribe.cpp:233-242); an empty array yields no
// subscriptions. Per-entry currency/issuer/market checks run in validateBook.
func resolveBooks(wireDecoded bool, raw json.RawMessage, typed []types.BookRequest) (present bool, books []types.BookRequest, rpcErr *types.RpcError) {
	if !wireDecoded {
		return typed != nil, typed, nil
	}
	present, isArray, elements := wireArrayElements(raw)
	if !present {
		return false, nil, nil
	}
	if !isArray {
		return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
	}
	books = make([]types.BookRequest, 0, len(elements))
	for _, el := range elements {
		var b types.BookRequest
		if json.Unmarshal(el, &b) != nil {
			return true, nil, types.RpcErrorInvalidParams("Invalid parameters.")
		}
		books = append(books, b)
	}
	return true, books, nil
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

	paysCur, paysIssuer, rpcErr := parseBookSide(paysSide, true)
	if rpcErr != nil {
		return rpcErr
	}
	getsCur, getsIssuer, rpcErr := parseBookSide(getsSide, false)
	if rpcErr != nil {
		return rpcErr
	}

	// Same asset on both sides is not a market (Subscribe.cpp:292-297). The
	// comparison is on the parsed 160-bit currency and issuer, like rippled's
	// book.in == book.out, so a currency and its 40-hex encoding match.
	if paysCur == getsCur && paysIssuer == getsIssuer {
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

// parseBookSide validates one side of a book entry and returns its parsed
// 160-bit currency and issuer. taker_pays maps to rippled's "source" (src*)
// error codes, taker_gets to "destination" (dst*); messages are the rpcError
// defaults from ErrorCodes.cpp since Subscribe.cpp returns bare rpcError(code)
// at every site.
func parseBookSide(side map[string]json.RawMessage, isPays bool) (currencyID [20]byte, issuerID [20]byte, _ *types.RpcError) {
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
		return currencyID, issuerID, curMalformed()
	}
	var currency string
	if err := json.Unmarshal(rawCurrency, &currency); err != nil {
		return currencyID, issuerID, curMalformed()
	}
	currencyID, ok = currencyToID(currency)
	if !ok {
		return currencyID, issuerID, curMalformed()
	}

	// Optional issuer plus the illegal-issuer cross-checks: XRP must not carry
	// an issuer, IOUs must, and the noAccount() sentinel is never allowed
	// (Subscribe.cpp:257-268 / :279-290). XRP-ness is the all-zero 160-bit
	// value, mirroring rippled's (!book.in.currency != !book.in.account).
	hasIssuer := false
	if rawIssuer, ok := side["issuer"]; ok {
		var issuer string
		if err := json.Unmarshal(rawIssuer, &issuer); err != nil {
			return currencyID, issuerID, isrMalformed()
		}
		_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuer)
		if err != nil {
			return currencyID, issuerID, isrMalformed()
		}
		copy(issuerID[:], idBytes)
		if issuerID == noAccountID {
			return currencyID, issuerID, isrMalformed()
		}
		hasIssuer = true
	}
	isXRPCurrency := currencyID == [20]byte{}
	isXRPIssuer := !hasIssuer || issuerID == xrpAccountID
	if isXRPCurrency != isXRPIssuer {
		return currencyID, issuerID, isrMalformed()
	}

	return currencyID, issuerID, nil
}

// currencyToID parses a currency code the way rippled's to_currency does
// (UintTypes.cpp:83-107) into its 160-bit form: "", "XRP", and a 40-hex of
// zeroes are the all-zero XRP currency; a 3-char ISO code is packed at bytes
// 12-14; a 40-hex string is taken verbatim. ok is false for anything
// to_currency rejects. Parsing to the 160-bit value (rather than comparing raw
// strings) lets the XRP-ness and same-asset checks fold a currency and its
// 40-hex encoding together, matching rippled.
func currencyToID(currency string) (id [20]byte, ok bool) {
	if currency == "" || currency == "XRP" {
		return id, true
	}
	switch len(currency) {
	case 3:
		for i := range 3 {
			if !isIsoCurrencyChar(rune(currency[i])) {
				return [20]byte{}, false
			}
			id[12+i] = currency[i]
		}
		return id, true
	case 40:
		if _, err := hex.Decode(id[:], []byte(currency)); err != nil {
			return [20]byte{}, false
		}
		return id, true
	default:
		return [20]byte{}, false
	}
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
// The deprecated `rt_transactions` stream name is normalised to
// `transactions_proposed` so a client that subscribed with the alias
// can also unsubscribe with the alias (Unsubscribe.cpp:88-93). Like
// HandleSubscribe, the url (RPCSub) branch is resolved by the caller.
func (sm *Manager) HandleUnsubscribe(conn *types.Connection, request types.SubscriptionRequest, isAdmin bool) *types.RpcError {
	w := request.WireArrays()

	sm.mu.Lock()
	defer sm.mu.Unlock()

	_, streams, rpcErr := resolveStreams(w.Present, w.Streams, request.Streams)
	if rpcErr != nil {
		return rpcErr
	}
	for _, stream := range streams {
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

	// accounts (Unsubscribe.cpp:127-135): empty array / non-string / bad id →
	// rpcACT_MALFORMED; null or non-array → rpcINVALID_PARAMS.
	accountsPresent, accounts, rpcErr := resolveAccounts(w.Present, w.Accounts, request.Accounts)
	if rpcErr != nil {
		return rpcErr
	}
	if accountsPresent {
		if len(accounts) == 0 {
			return types.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range accounts {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		if existing, ok := conn.Subscriptions[types.SubAccounts]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range accounts {
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

	// accounts_proposed (Unsubscribe.cpp:116-124), same semantics as accounts.
	proposedPresent, proposed, rpcErr := resolveAccounts(w.Present, w.AccountsProposed, request.AccountsProposed)
	if rpcErr != nil {
		return rpcErr
	}
	if proposedPresent {
		if len(proposed) == 0 {
			return types.RpcErrorActMalformed("Account malformed.")
		}
		for _, acc := range proposed {
			if !isValidXRPLAddress(acc) {
				return types.RpcErrorActMalformed("Account malformed.")
			}
		}
		if existing, ok := conn.Subscriptions[types.SubAccountsProposed]; ok {
			accountsToRemove := make(map[string]bool)
			for _, acc := range proposed {
				accountsToRemove[acc] = true
			}
			var remainingAccounts []string
			for _, acc := range existing.Accounts {
				if !accountsToRemove[acc] {
					remainingAccounts = append(remainingAccounts, acc)
				}
			}
			if len(remainingAccounts) > 0 {
				conn.Subscriptions[types.SubAccountsProposed] = types.SubscriptionConfig{
					Accounts: remainingAccounts,
				}
			} else {
				delete(conn.Subscriptions, types.SubAccountsProposed)
			}
		}
	}

	// books run the same validation as subscribe minus the taker field, which
	// unsubscribe does not carry (Unsubscribe.cpp:167-245); an empty array
	// unsubscribes nothing.
	booksPresent, books, rpcErr := resolveBooks(w.Present, w.Books, request.Books)
	if rpcErr != nil {
		return rpcErr
	}
	if booksPresent {
		var toRemove []types.BookRequest
		for _, book := range books {
			if rpcErr := validateBook(book, false); rpcErr != nil {
				return rpcErr
			}
			toRemove = append(toRemove, book)
			if book.Both {
				toRemove = append(toRemove, reverseBook(book))
			}
		}
		if len(toRemove) > 0 {
			if existing, ok := conn.Subscriptions[types.SubBook]; ok {
				remaining := removeBooks(existing.Books, toRemove)
				if len(remaining) > 0 {
					conn.Subscriptions[types.SubBook] = types.SubscriptionConfig{Books: remaining}
				} else {
					delete(conn.Subscriptions, types.SubBook)
				}
			}
		}
	}

	return nil
}

// HasStreamSubscriptions reports whether the connection still holds any
// stream subscription. Account and book subscriptions don't count — this
// mirrors NetworkOPs::tryRemoveRpcSub, which only scans the stream maps
// when deciding whether a url subscription's registry entry can be dropped.
func (sm *Manager) HasStreamSubscriptions(connID string) bool {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return false
	}
	for key := range conn.Subscriptions {
		if validStreams[key] {
			return true
		}
	}
	return false
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
	deliver(sm.collectAccountTargets(types.SubAccountsProposed, accounts), data)
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

// GetConnectionSubscriptions returns a copy of the subscriptions for a
// connection. A copy (not the live map) so the caller can iterate without
// holding sm.mu while HandleSubscribe / HandleUnsubscribe mutate the original.
func (sm *Manager) GetConnectionSubscriptions(connID string) map[types.SubscriptionType]types.SubscriptionConfig {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	conn := sm.Connections[connID]
	if conn == nil {
		return nil
	}
	return maps.Clone(conn.Subscriptions)
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
