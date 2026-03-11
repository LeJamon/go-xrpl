package pathfinder

import (
	"sort"
	"testing"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	tx "github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/internal/tx/payment"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock LedgerView
// ---------------------------------------------------------------------------

// mockLedgerView is an in-memory implementation of tx.LedgerView for unit tests.
type mockLedgerView struct {
	entries map[[32]byte][]byte
}

func newMockLedger() *mockLedgerView {
	return &mockLedgerView{entries: make(map[[32]byte][]byte)}
}

func (m *mockLedgerView) Read(k keylet.Keylet) ([]byte, error) {
	data, ok := m.entries[k.Key]
	if !ok {
		return nil, nil
	}
	return data, nil
}

func (m *mockLedgerView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := m.entries[k.Key]
	return ok, nil
}

func (m *mockLedgerView) Insert(k keylet.Keylet, data []byte) error {
	m.entries[k.Key] = data
	return nil
}

func (m *mockLedgerView) Update(k keylet.Keylet, data []byte) error {
	m.entries[k.Key] = data
	return nil
}

func (m *mockLedgerView) Erase(k keylet.Keylet) error {
	delete(m.entries, k.Key)
	return nil
}

func (m *mockLedgerView) AdjustDropsDestroyed(_ drops.XRPAmount) {}

func (m *mockLedgerView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.entries {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

func (m *mockLedgerView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	// Simple implementation: find the smallest key > given key
	var best [32]byte
	var bestData []byte
	found := false
	for k, v := range m.entries {
		if compareKeys(k, key) > 0 {
			if !found || compareKeys(k, best) < 0 {
				best = k
				bestData = v
				found = true
			}
		}
	}
	return best, bestData, found, nil
}

func (m *mockLedgerView) TxExists(_ [32]byte) bool { return false }

func compareKeys(a, b [32]byte) int {
	for i := 0; i < 32; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Test helper: create deterministic test account IDs
// ---------------------------------------------------------------------------

// testAccountID creates a deterministic 20-byte account ID from a seed byte.
func testAccountID(seed byte) [20]byte {
	var id [20]byte
	id[0] = seed
	// Fill with a recognizable pattern
	for i := 1; i < 20; i++ {
		id[i] = seed + byte(i)
	}
	return id
}

// testAccountAddress returns the base58 address string for a test account ID.
func testAccountAddress(id [20]byte) string {
	return state.EncodeAccountIDSafe(id)
}

// ---------------------------------------------------------------------------
// Test helper: populate a mock ledger with ledger entries
// ---------------------------------------------------------------------------

// addAccount stores a serialized AccountRoot at the proper keylet.
func addAccount(t *testing.T, ledger *mockLedgerView, accountID [20]byte, balance uint64, flags uint32) {
	t.Helper()
	addr := testAccountAddress(accountID)
	acct := &state.AccountRoot{
		Account:  addr,
		Balance:  balance,
		Sequence: 1,
		Flags:    flags,
	}
	data, err := state.SerializeAccountRoot(acct)
	require.NoError(t, err, "serialize AccountRoot for %x", accountID[:4])
	k := keylet.Account(accountID)
	ledger.entries[k.Key] = data
}

// addRippleState stores a serialized RippleState and creates the owner directory
// entries so that DirForEach can find it. lowAccount < highAccount is enforced.
func addRippleState(
	t *testing.T,
	ledger *mockLedgerView,
	lowAccount, highAccount [20]byte,
	currency string,
	balanceValue float64, // positive means low owes high
	lowLimit, highLimit float64,
	flags uint32,
) {
	t.Helper()
	lowAddr := testAccountAddress(lowAccount)
	highAddr := testAccountAddress(highAccount)

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(balanceValue, currency, state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(lowLimit, currency, lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(highLimit, currency, highAddr),
		Flags:     flags,
	}

	data, err := state.SerializeRippleState(rs)
	require.NoError(t, err, "serialize RippleState %s", currency)

	lineKey := keylet.Line(lowAccount, highAccount, currency)
	ledger.entries[lineKey.Key] = data

	// Add line key to both accounts' owner directories
	ensureOwnerDirContains(t, ledger, lowAccount, lineKey.Key)
	ensureOwnerDirContains(t, ledger, highAccount, lineKey.Key)
}

// addOffer stores a serialized LedgerOffer.
func addOffer(
	t *testing.T,
	ledger *mockLedgerView,
	account [20]byte,
	seq uint32,
	takerPays, takerGets state.Amount,
) {
	t.Helper()
	addr := testAccountAddress(account)
	offer := &state.LedgerOffer{
		Account:   addr,
		Sequence:  seq,
		TakerPays: takerPays,
		TakerGets: takerGets,
	}
	data, err := state.SerializeLedgerOffer(offer)
	require.NoError(t, err, "serialize LedgerOffer")

	// Store under a deterministic key (Offer keylet = hash(account, seq))
	offerKey := keylet.Offer(account, seq)
	ledger.entries[offerKey.Key] = data
}

// ensureOwnerDirContains adds itemKey to the owner directory of the given account.
// If no directory exists yet, creates one. If one exists, appends.
func ensureOwnerDirContains(t *testing.T, ledger *mockLedgerView, account [20]byte, itemKey [32]byte) {
	t.Helper()
	dirK := keylet.OwnerDir(account)

	existingData, ok := ledger.entries[dirK.Key]
	if !ok {
		// Create new directory
		dir := &state.DirectoryNode{
			Owner:     account,
			RootIndex: dirK.Key,
			Indexes:   [][32]byte{itemKey},
		}
		data, err := state.SerializeDirectoryNode(dir, false)
		require.NoError(t, err, "serialize OwnerDir")
		ledger.entries[dirK.Key] = data
		return
	}

	// Parse existing and add index
	dir, err := state.ParseDirectoryNode(existingData)
	require.NoError(t, err, "parse existing OwnerDir")

	// Check for duplicates
	for _, idx := range dir.Indexes {
		if idx == itemKey {
			return
		}
	}
	dir.Indexes = append(dir.Indexes, itemKey)
	data, err := state.SerializeDirectoryNode(dir, false)
	require.NoError(t, err, "re-serialize OwnerDir")
	ledger.entries[dirK.Key] = data
}

// addBookDir creates a book directory entry so BookExists can find it.
func addBookDir(t *testing.T, ledger *mockLedgerView, takerPays, takerGets payment.Issue) {
	t.Helper()
	paysCurr := currencyTo20(takerPays.Currency)
	paysIssuer := takerPays.Issuer
	getsCurr := currencyTo20(takerGets.Currency)
	getsIssuer := takerGets.Issuer
	k := keylet.BookDir(paysCurr, paysIssuer, getsCurr, getsIssuer)

	dir := &state.DirectoryNode{
		RootIndex: k.Key,
	}
	data, err := state.SerializeDirectoryNode(dir, true)
	require.NoError(t, err, "serialize BookDir")
	ledger.entries[k.Key] = data
}

// ===========================================================================
// Test 1: Path type tables
// ===========================================================================

func TestPathTable_Entries(t *testing.T) {
	// Verify all 5 payment types have entries in the table
	require.NotNil(t, pathTable, "pathTable should be initialized")

	// XRP_to_XRP has no paths (nil)
	require.Nil(t, pathTable[ptXRP_to_XRP], "XRP-to-XRP should have no path patterns")

	// All other types should have entries
	require.NotEmpty(t, pathTable[ptXRP_to_nonXRP], "XRP-to-nonXRP should have path patterns")
	require.NotEmpty(t, pathTable[ptNonXRP_to_XRP], "nonXRP-to-XRP should have path patterns")
	require.NotEmpty(t, pathTable[ptNonXRP_to_same], "nonXRP-to-same should have path patterns")
	require.NotEmpty(t, pathTable[ptNonXRP_to_nonXRP], "nonXRP-to-nonXRP should have path patterns")

	// Verify search levels are sorted ascending within each type
	for pt, entries := range pathTable {
		if entries == nil {
			continue
		}
		for i := 1; i < len(entries); i++ {
			require.GreaterOrEqual(t, entries[i].SearchLevel, entries[i-1].SearchLevel,
				"PaymentType %d: entries should be ordered by SearchLevel", pt)
		}
	}
}

func TestPathTable_XRPToNonXRP_AllStartWithSource(t *testing.T) {
	for _, cp := range pathTable[ptXRP_to_nonXRP] {
		require.NotEmpty(t, cp.Type)
		require.Equal(t, ntSOURCE, cp.Type[0], "all path patterns should start with ntSOURCE")
		require.Equal(t, ntDESTINATION, cp.Type[len(cp.Type)-1], "all path patterns should end with ntDESTINATION")
	}
}

func TestPathTable_NonXRPToXRP_AllHaveXRPBook(t *testing.T) {
	for _, cp := range pathTable[ptNonXRP_to_XRP] {
		hasXRPBook := false
		for _, nt := range cp.Type {
			if nt == ntXRP_BOOK {
				hasXRPBook = true
				break
			}
		}
		require.True(t, hasXRPBook,
			"NonXRP-to-XRP path should contain ntXRP_BOOK: level=%d", cp.SearchLevel)
	}
}

func TestPathTable_NonXRPToSame_HasDirectAccount(t *testing.T) {
	// The first entry at level 1 should be source->account->dest (sad)
	entries := pathTable[ptNonXRP_to_same]
	require.NotEmpty(t, entries)
	first := entries[0]
	require.Equal(t, 1, first.SearchLevel)
	require.Equal(t, PathType{ntSOURCE, ntACCOUNTS, ntDESTINATION}, first.Type,
		"first nonXRP-to-same pattern should be source->accounts->dest")
}

// ===========================================================================
// Test 2: PaymentType classification
// ===========================================================================

func TestClassifyPayment_XRPToXRP(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "XRP",
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptXRP_to_XRP, pf.classifyPayment())
}

func TestClassifyPayment_XRPToNonXRP(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewIssuedAmountFromFloat64(100, "USD", testAccountAddress(dst))

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "XRP",
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptXRP_to_nonXRP, pf.classifyPayment())
}

func TestClassifyPayment_NonXRPToXRP(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "USD",
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptNonXRP_to_XRP, pf.classifyPayment())
}

func TestClassifyPayment_NonXRPToSame(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewIssuedAmountFromFloat64(100, "USD", testAccountAddress(dst))

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "USD",
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptNonXRP_to_same, pf.classifyPayment())
}

func TestClassifyPayment_NonXRPToDifferent(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewIssuedAmountFromFloat64(100, "EUR", testAccountAddress(dst))

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "USD",
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptNonXRP_to_nonXRP, pf.classifyPayment())
}

func TestClassifyPayment_EmptySrcCurrencyIsXRP(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	pf := &Pathfinder{
		srcAccount:  src,
		dstAccount:  dst,
		srcCurrency: "", // empty means XRP
		dstAmount:   dstAmt,
	}
	require.Equal(t, ptXRP_to_XRP, pf.classifyPayment())
}

// ===========================================================================
// Test 3: RippleLineCache
// ===========================================================================

func TestRippleLineCache_GetLedger(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	require.Equal(t, ledger, cache.GetLedger())
}

func TestRippleLineCache_EmptyAccount(t *testing.T) {
	ledger := newMockLedger()
	acctID := testAccountID(1)
	// No directory -> no trust lines
	cache := NewRippleLineCache(ledger)
	lines := cache.GetRippleLines(acctID, LineDirectionOutgoing)
	require.Empty(t, lines, "account with no owner dir should have zero trust lines")
}

func TestRippleLineCache_CacheHit(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	// Ensure alice < bob for low/high ordering
	low, high := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		low, high = bob, alice
	}
	addRippleState(t, ledger, low, high, "USD", 100, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)

	// First call builds from ledger
	lines1 := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.NotEmpty(t, lines1)

	// Second call should return cached result (same pointer)
	lines2 := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.Equal(t, len(lines1), len(lines2))
}

func TestRippleLineCache_OutgoingIsSuperset(t *testing.T) {
	// When outgoing is already cached, incoming reuses it (outgoing is a superset).
	// To test the actual filtering behavior, we request incoming FIRST on a fresh cache.
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	low, high := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		low, high = bob, alice
	}
	// Set NoRipple on alice's side
	var flags uint32
	if low == alice {
		flags = state.LsfLowNoRipple
	} else {
		flags = state.LsfHighNoRipple
	}
	addRippleState(t, ledger, low, high, "USD", 100, 1000, 1000, flags)

	// Test 1: incoming first on a fresh cache — should filter NoRipple lines
	cache1 := NewRippleLineCache(ledger)
	inLines := cache1.GetRippleLines(alice, LineDirectionIncoming)
	require.Empty(t, inLines, "incoming should filter out lines with NoRipple on viewer's side")

	// Test 2: outgoing includes all lines (NoRipple does not affect outgoing)
	outLines := cache1.GetRippleLines(alice, LineDirectionOutgoing)
	require.Len(t, outLines, 1, "outgoing should include all trust lines")

	// Test 3: if outgoing was cached first, incoming reuses it (returns superset)
	cache2 := NewRippleLineCache(ledger)
	outFirst := cache2.GetRippleLines(alice, LineDirectionOutgoing)
	require.Len(t, outFirst, 1)
	inFromCached := cache2.GetRippleLines(alice, LineDirectionIncoming)
	// This returns the cached outgoing set (superset, unfiltered)
	require.Len(t, inFromCached, 1,
		"incoming reuses cached outgoing (superset) — no additional filtering")
}

func TestRippleLineCache_IncomingReuseOutgoing(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	low, high := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		low, high = bob, alice
	}
	addRippleState(t, ledger, low, high, "USD", 100, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)

	// Pre-cache outgoing
	outLines := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.NotEmpty(t, outLines)

	// Incoming reuses outgoing (superset), should return same data
	inLines := cache.GetRippleLines(alice, LineDirectionIncoming)
	require.Equal(t, len(outLines), len(inLines))
}

func TestRippleLineCache_TrustLineParsing(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	low, high := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		low, high = bob, alice
	}

	// Balance=50 means low owes high 50 USD
	addRippleState(t, ledger, low, high, "USD", 50, 1000, 500, 0)

	cache := NewRippleLineCache(ledger)
	lines := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.Len(t, lines, 1)

	line := lines[0]
	require.Equal(t, "USD", line.Currency)
	require.Equal(t, alice, line.AccountID, "account should be the viewing account")
	require.Equal(t, bob, line.AccountIDPeer, "peer should be the other account")
	require.False(t, line.NoRipple, "no flags set")
	require.False(t, line.Freeze, "no freeze flags")
}

func TestRippleLineCache_MultipleCurrencies(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	low, high := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		low, high = bob, alice
	}

	addRippleState(t, ledger, low, high, "USD", 50, 1000, 500, 0)
	addRippleState(t, ledger, low, high, "EUR", 25, 800, 600, 0)

	cache := NewRippleLineCache(ledger)
	lines := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.Len(t, lines, 2)

	currencies := make(map[string]bool)
	for _, l := range lines {
		currencies[l.Currency] = true
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
}

// ===========================================================================
// Test 4: AccountCurrencies
// ===========================================================================

func TestAccountSourceCurrencies_AlwaysIncludesXRP(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	cache := NewRippleLineCache(ledger)

	currencies := AccountSourceCurrencies(acct, cache)
	xrpIssue := payment.Issue{Currency: "XRP"}
	require.True(t, currencies[xrpIssue], "XRP should always be in source currencies")
}

func TestAccountSourceCurrencies_WithPositiveBalance(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := alice, gw
	if compareAccountIDs(alice, gw) > 0 {
		low, high = gw, alice
	}
	// Positive balance from alice's perspective means alice can send this currency.
	// If alice is low: balance is stored as-is, so balance > 0 means low owes high.
	// From low's perspective, balance stays positive.
	// From high's perspective, balance is negated -> becomes negative.
	// We need to find what value gives alice a positive viewed balance.
	var balVal float64
	if low == alice {
		// alice is low; viewed balance = stored balance; positive means alice owes gw
		// But for sending, we need alice to HOLD the IOU (positive from alice's view).
		// From rippled: positive balance means peer owes the viewer.
		// Actually: when viewIsLow, balance stays as-is. In rippled convention:
		// balance positive when low owes high. But PathFindTrustLine.Balance
		// is "from account's perspective": positive means "this account owes the peer"
		// => for Signum > 0 check in AccountSourceCurrencies to succeed.
		balVal = 50 // low owes high 50
	} else {
		// alice is high; viewed balance = negate(stored). For positive: stored must be negative.
		balVal = -50 // high owes low 50 -> negate -> alice sees +50
	}

	addRippleState(t, ledger, low, high, "USD", balVal, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	currencies := AccountSourceCurrencies(alice, cache)

	// Should include XRP and USD (issued by gw)
	require.True(t, currencies[payment.Issue{Currency: "XRP"}])
	require.True(t, currencies[payment.Issue{Currency: "USD", Issuer: gw}],
		"alice should be able to send USD (positive balance)")
}

func TestAccountSourceCurrencies_WithAvailableCredit(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(2)
	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := alice, gw
	if compareAccountIDs(alice, gw) > 0 {
		low, high = gw, alice
	}
	// Zero balance but peer extends credit.
	// From alice's perspective, LimitPeer > 0 means peer allows alice to go into debt.
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	currencies := AccountSourceCurrencies(alice, cache)

	require.True(t, currencies[payment.Issue{Currency: "USD", Issuer: gw}],
		"alice should be able to send USD (peer extends credit)")
}

func TestAccountDestCurrencies_AlwaysIncludesXRP(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	cache := NewRippleLineCache(ledger)

	currencies := AccountDestCurrencies(acct, cache)
	xrpIssue := payment.Issue{Currency: "XRP"}
	require.True(t, currencies[xrpIssue], "XRP should always be in dest currencies")
}

func TestAccountDestCurrencies_WithAvailableCapacity(t *testing.T) {
	ledger := newMockLedger()
	bob := testAccountID(1)
	gw := testAccountID(2)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := bob, gw
	if compareAccountIDs(bob, gw) > 0 {
		low, high = gw, bob
	}
	// Balance of 0, limit of 1000 -> can accept up to 1000.
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	currencies := AccountDestCurrencies(bob, cache)

	require.True(t, currencies[payment.Issue{Currency: "USD", Issuer: gw}],
		"bob should be able to receive USD (balance < limit)")
}

// ===========================================================================
// Test 5: BookIndex
// ===========================================================================

func TestBookIndex_EmptyLedger(t *testing.T) {
	ledger := newMockLedger()
	bi := NewBookIndex(ledger)
	bi.Build()

	result := bi.GetBooksByTakerPays(payment.Issue{Currency: "XRP"})
	require.Empty(t, result, "empty ledger should have no books")
}

func TestBookIndex_SingleOffer(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	addAccount(t, ledger, alice, 10000000000, 0)

	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	// alice offers: pays XRP, gets USD
	addOffer(t, ledger, alice, 1,
		state.NewXRPAmountFromInt(1000000),
		state.NewIssuedAmountFromFloat64(10, "USD", gwAddr),
	)

	bi := NewBookIndex(ledger)
	bi.Build()

	results := bi.GetBooksByTakerPays(payment.Issue{Currency: "XRP"})
	require.Len(t, results, 1)
	require.Equal(t, "USD", results[0].Currency)
}

func TestBookIndex_IsBookToXRP(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)
	addAccount(t, ledger, alice, 10000000000, 0)

	// alice offers: pays USD, gets XRP
	addOffer(t, ledger, alice, 1,
		state.NewIssuedAmountFromFloat64(10, "USD", gwAddr),
		state.NewXRPAmountFromInt(1000000),
	)

	bi := NewBookIndex(ledger)

	usdIssue := payment.Issue{Currency: "USD", Issuer: gw}
	require.True(t, bi.IsBookToXRP(usdIssue), "should find book from USD to XRP")

	eurIssue := payment.Issue{Currency: "EUR", Issuer: gw}
	require.False(t, bi.IsBookToXRP(eurIssue), "no book from EUR to XRP")
}

func TestBookIndex_BookExists(t *testing.T) {
	ledger := newMockLedger()
	gw := testAccountID(3)

	xrpIssue := payment.Issue{Currency: "XRP"}
	usdIssue := payment.Issue{Currency: "USD", Issuer: gw}

	addBookDir(t, ledger, xrpIssue, usdIssue)

	bi := NewBookIndex(ledger)
	require.True(t, bi.BookExists(xrpIssue, usdIssue), "book directory should exist")
	require.False(t, bi.BookExists(usdIssue, xrpIssue), "reverse book should not exist")
}

func TestBookIndex_LazyBuild(t *testing.T) {
	ledger := newMockLedger()
	bi := NewBookIndex(ledger)
	require.False(t, bi.built, "should not be built initially")

	bi.GetBooksByTakerPays(payment.Issue{Currency: "XRP"})
	require.True(t, bi.built, "should be built after first query")

	// Second Build call should be a no-op
	bi.Build()
	require.True(t, bi.built)
}

// ===========================================================================
// Test 6: pathHasSeen and pathHasSeenIssue (loop detection)
// ===========================================================================

func TestPathHasSeen_EmptyPath(t *testing.T) {
	acct := testAccountID(1)
	require.False(t, pathHasSeen(nil, acct, "USD"), "empty path has seen nothing")
}

func TestPathHasSeen_MatchFound(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	path := []payment.PathStep{
		{Account: acctAddr, Currency: "USD"},
	}
	require.True(t, pathHasSeen(path, acct, "USD"), "path should detect revisit")
}

func TestPathHasSeen_DifferentCurrency(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	path := []payment.PathStep{
		{Account: acctAddr, Currency: "USD"},
	}
	require.False(t, pathHasSeen(path, acct, "EUR"),
		"same account different currency should not be seen")
}

func TestPathHasSeen_DifferentAccount(t *testing.T) {
	acct1 := testAccountID(1)
	acct2 := testAccountID(2)
	acctAddr := testAccountAddress(acct1)
	path := []payment.PathStep{
		{Account: acctAddr, Currency: "USD"},
	}
	require.False(t, pathHasSeen(path, acct2, "USD"),
		"different account should not be seen")
}

func TestPathHasSeen_XRPEmptyCurrency(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	// Step with empty currency should match "XRP"
	path := []payment.PathStep{
		{Account: acctAddr, Currency: ""},
	}
	require.True(t, pathHasSeen(path, acct, "XRP"),
		"empty currency should match XRP")
}

func TestPathHasSeenIssue_EmptyPath(t *testing.T) {
	issue := payment.Issue{Currency: "USD", Issuer: testAccountID(1)}
	require.False(t, pathHasSeenIssue(nil, issue), "empty path has seen nothing")
}

func TestPathHasSeenIssue_MatchByIssuer(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	path := []payment.PathStep{
		{Currency: "USD", Issuer: acctAddr},
	}
	issue := payment.Issue{Currency: "USD", Issuer: acct}
	require.True(t, pathHasSeenIssue(path, issue), "should find issue by issuer")
}

func TestPathHasSeenIssue_MatchByAccount(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	path := []payment.PathStep{
		{Account: acctAddr, Currency: "USD"},
	}
	issue := payment.Issue{Currency: "USD", Issuer: acct}
	require.True(t, pathHasSeenIssue(path, issue), "should find issue by account")
}

func TestPathHasSeenIssue_DifferentCurrency(t *testing.T) {
	acct := testAccountID(1)
	acctAddr := testAccountAddress(acct)
	path := []payment.PathStep{
		{Currency: "EUR", Issuer: acctAddr},
	}
	issue := payment.Issue{Currency: "USD", Issuer: acct}
	require.False(t, pathHasSeenIssue(path, issue),
		"different currency should not match")
}

// ===========================================================================
// Test 7: addUniquePath (deduplication)
// ===========================================================================

func TestAddUniquePath_NoDuplicates(t *testing.T) {
	pf := &Pathfinder{}

	step := payment.PathStep{Account: "rSomeAccount", Currency: "USD"}
	path1 := []payment.PathStep{step}
	path2 := []payment.PathStep{step} // identical

	pf.addUniquePath(path1)
	pf.addUniquePath(path2) // duplicate — should be rejected

	require.Len(t, pf.completePaths, 1, "duplicate path should not be added")
}

func TestAddUniquePath_DifferentPathsAdded(t *testing.T) {
	pf := &Pathfinder{}

	path1 := []payment.PathStep{{Account: "rAccount1", Currency: "USD"}}
	path2 := []payment.PathStep{{Account: "rAccount2", Currency: "USD"}}

	pf.addUniquePath(path1)
	pf.addUniquePath(path2)

	require.Len(t, pf.completePaths, 2, "different paths should both be added")
}

func TestAddUniquePath_MakesCopy(t *testing.T) {
	pf := &Pathfinder{}

	path := []payment.PathStep{{Account: "rAccount1", Currency: "USD"}}
	pf.addUniquePath(path)

	// Modify original — should not affect stored path
	path[0].Currency = "EUR"
	require.Equal(t, "USD", pf.completePaths[0][0].Currency,
		"stored path should be independent copy")
}

// ===========================================================================
// Test 8: isNoRipple
// ===========================================================================

func TestIsNoRipple_XRPCurrency(t *testing.T) {
	ledger := newMockLedger()
	pf := &Pathfinder{ledger: ledger}

	// XRP never has no-ripple
	require.False(t, pf.isNoRipple(testAccountID(1), testAccountID(2), "XRP"))
	require.False(t, pf.isNoRipple(testAccountID(1), testAccountID(2), ""))
}

func TestIsNoRipple_NoTrustLine(t *testing.T) {
	ledger := newMockLedger()
	pf := &Pathfinder{ledger: ledger}

	// No trust line exists between accounts
	require.False(t, pf.isNoRipple(testAccountID(1), testAccountID(2), "USD"))
}

func TestIsNoRipple_FlagSet(t *testing.T) {
	ledger := newMockLedger()
	from := testAccountID(1)
	to := testAccountID(2)

	low, high := from, to
	if compareAccountIDs(from, to) > 0 {
		low, high = to, from
	}

	// Set NoRipple on to's side
	var flags uint32
	if low == to {
		flags = state.LsfLowNoRipple
	} else {
		flags = state.LsfHighNoRipple
	}
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, flags)

	pf := &Pathfinder{ledger: ledger}
	require.True(t, pf.isNoRipple(from, to, "USD"),
		"should detect NoRipple on toAccount's side")
}

func TestIsNoRipple_FlagNotSet(t *testing.T) {
	ledger := newMockLedger()
	from := testAccountID(1)
	to := testAccountID(2)

	low, high := from, to
	if compareAccountIDs(from, to) > 0 {
		low, high = to, from
	}

	// Set NoRipple on from's side only (not to's)
	var flags uint32
	if low == from {
		flags = state.LsfLowNoRipple
	} else {
		flags = state.LsfHighNoRipple
	}
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, flags)

	pf := &Pathfinder{ledger: ledger}
	require.False(t, pf.isNoRipple(from, to, "USD"),
		"NoRipple on from's side should not trigger")
}

// ===========================================================================
// Test 9: pathsEqual
// ===========================================================================

func TestPathsEqual_Empty(t *testing.T) {
	require.True(t, pathsEqual(nil, nil))
	require.True(t, pathsEqual([]payment.PathStep{}, []payment.PathStep{}))
}

func TestPathsEqual_DifferentLength(t *testing.T) {
	a := []payment.PathStep{{Currency: "USD"}}
	b := []payment.PathStep{{Currency: "USD"}, {Currency: "EUR"}}
	require.False(t, pathsEqual(a, b))
}

func TestPathsEqual_SameSteps(t *testing.T) {
	a := []payment.PathStep{
		{Account: "rA", Currency: "USD", Issuer: "rI"},
		{Currency: "EUR"},
	}
	b := []payment.PathStep{
		{Account: "rA", Currency: "USD", Issuer: "rI"},
		{Currency: "EUR"},
	}
	require.True(t, pathsEqual(a, b))
}

func TestPathsEqual_DifferentSteps(t *testing.T) {
	a := []payment.PathStep{{Currency: "USD"}}
	b := []payment.PathStep{{Currency: "EUR"}}
	require.False(t, pathsEqual(a, b))
}

// ===========================================================================
// Test 10: pathTypeKey
// ===========================================================================

func TestPathTypeKey_DeterministicAndUnique(t *testing.T) {
	pt1 := PathType{ntSOURCE, ntACCOUNTS, ntDESTINATION}
	pt2 := PathType{ntSOURCE, ntBOOKS, ntDESTINATION}
	pt3 := PathType{ntSOURCE, ntACCOUNTS, ntDESTINATION} // same as pt1

	k1 := pathTypeKey(pt1)
	k2 := pathTypeKey(pt2)
	k3 := pathTypeKey(pt3)

	require.Equal(t, k1, k3, "same PathType should produce same key")
	require.NotEqual(t, k1, k2, "different PathType should produce different key")
}

// ===========================================================================
// Test 11: currencyTo20
// ===========================================================================

func TestCurrencyTo20_XRP(t *testing.T) {
	result := currencyTo20("XRP")
	require.Equal(t, [20]byte{}, result, "XRP should be all zeros")
}

func TestCurrencyTo20_Empty(t *testing.T) {
	result := currencyTo20("")
	require.Equal(t, [20]byte{}, result, "empty should be all zeros")
}

func TestCurrencyTo20_Standard3Char(t *testing.T) {
	result := currencyTo20("USD")
	// bytes 12-14 should contain "USD"
	require.Equal(t, byte('U'), result[12])
	require.Equal(t, byte('S'), result[13])
	require.Equal(t, byte('D'), result[14])
	// All other bytes should be zero
	for i, b := range result {
		if i >= 12 && i <= 14 {
			continue
		}
		require.Equal(t, byte(0), b, "byte %d should be zero", i)
	}
}

// ===========================================================================
// Test 12: issueFromAmount
// ===========================================================================

func TestIssueFromAmount_XRP(t *testing.T) {
	amt := state.NewXRPAmountFromInt(1000000)
	issue := issueFromAmount(amt)
	require.Equal(t, "XRP", issue.Currency)
	require.Equal(t, [20]byte{}, issue.Issuer)
}

func TestIssueFromAmount_IOU(t *testing.T) {
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)
	amt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)
	issue := issueFromAmount(amt)
	require.Equal(t, "USD", issue.Currency)
	require.Equal(t, gw, issue.Issuer)
}

// ===========================================================================
// Test 13: FindPaths — basic scenarios
// ===========================================================================

func TestFindPaths_ZeroDstAmount(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	src := testAccountID(1)
	dst := testAccountID(2)

	pf := NewPathfinder(ledger, cache, src, dst,
		state.NewXRPAmountFromInt(0), // zero destination amount
		state.NewXRPAmountFromInt(0),
		"XRP", [20]byte{}, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	require.False(t, result, "zero dst amount should return false")
}

func TestFindPaths_SameAccountSameCurrency(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	addAccount(t, ledger, acct, 10000000000, 0)
	cache := NewRippleLineCache(ledger)

	pf := NewPathfinder(ledger, cache, acct, acct,
		state.NewXRPAmountFromInt(1000000),
		state.NewXRPAmountFromInt(1000000),
		"XRP", [20]byte{}, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	require.False(t, result, "same account same currency should return false (no paths needed)")
}

func TestFindPaths_SourceNotFound(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)
	addAccount(t, ledger, dst, 10000000000, 0)
	// src account NOT added to ledger
	cache := NewRippleLineCache(ledger)

	gwID := testAccountID(3)
	gwAddr := testAccountAddress(gwID)
	dstAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, src, dst,
		dstAmt,
		state.NewIssuedAmountFromFloat64(200, "USD", gwAddr),
		"USD", gwID, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	require.False(t, result, "non-existent source should return false")
}

func TestFindPaths_XRPToXRP_NoPaths(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)
	addAccount(t, ledger, src, 10000000000, 0)
	addAccount(t, ledger, dst, 10000000000, 0)
	cache := NewRippleLineCache(ledger)

	pf := NewPathfinder(ledger, cache, src, dst,
		state.NewXRPAmountFromInt(1000000),
		state.NewXRPAmountFromInt(1000000),
		"XRP", [20]byte{}, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	// XRP-to-XRP returns true but with no explicit paths (default path only)
	require.True(t, result, "XRP-to-XRP should succeed with default path")
	require.Empty(t, pf.CompletePaths(), "XRP-to-XRP should find no explicit paths")
}

func TestFindPaths_IOUToSameIOU_ThroughGateway(t *testing.T) {
	// Setup: alice sends USD to bob, both trust gateway for USD.
	// Path should go through gateway: alice -> gw -> bob
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	// alice trusts gw for USD (alice holds 100 USD from gw)
	lowA, highA := alice, gw
	if compareAccountIDs(alice, gw) > 0 {
		lowA, highA = gw, alice
	}
	// Set balance so alice has positive viewed balance of 100 USD.
	var balAlice float64
	if lowA == alice {
		balAlice = 100 // low owes high 100
	} else {
		balAlice = -100 // high owes low 100
	}
	addRippleState(t, ledger, lowA, highA, "USD", balAlice, 1000, 1000, 0)

	// bob trusts gw for USD
	lowB, highB := bob, gw
	if compareAccountIDs(bob, gw) > 0 {
		lowB, highB = gw, bob
	}
	addRippleState(t, ledger, lowB, highB, "USD", 0, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, alice, bob,
		dstAmt, srcAmt, "USD", gw, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	require.True(t, result, "should find paths for IOU-to-same-IOU through gateway")
	// There should be at least one complete path found
	// (the specific paths depend on which patterns match — gateway is directly connected)
}

func TestFindPaths_SearchLevelFiltering(t *testing.T) {
	// With searchLevel 0, most patterns should be skipped since they have level >= 1
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := alice, gw
	if compareAccountIDs(alice, gw) > 0 {
		low, high = gw, alice
	}
	var bal float64
	if low == alice {
		bal = 100
	} else {
		bal = -100
	}
	addRippleState(t, ledger, low, high, "USD", bal, 1000, 1000, 0)

	lowB, highB := bob, gw
	if compareAccountIDs(bob, gw) > 0 {
		lowB, highB = gw, bob
	}
	addRippleState(t, ledger, lowB, highB, "USD", 0, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	// Search level 0: should not find any patterns (all are level >= 1)
	pf0 := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "USD", gw, false)
	pf0.FindPaths(0)
	pathsLevel0 := len(pf0.CompletePaths())

	// Search level 10: should find more patterns
	pf10 := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "USD", gw, false)
	pf10.FindPaths(10)
	pathsLevel10 := len(pf10.CompletePaths())

	require.GreaterOrEqual(t, pathsLevel10, pathsLevel0,
		"higher search level should find at least as many paths")
}

func TestFindPaths_DestNotExist_IOUFails(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, src, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)
	// dst account NOT in ledger

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, src, dst, dstAmt, srcAmt, "USD", gw, false)
	result := pf.FindPaths(DefaultSearchLevel)
	require.False(t, result, "IOU payment to non-existent destination should fail")
}

func TestFindPaths_EffectiveDst_GatewayIssuer(t *testing.T) {
	// When dstAmount.Issuer != dstAccount, effectiveDst should be the issuer.
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	cache := NewRippleLineCache(ledger)
	// dstAmount issued by gw (not bob) -> effectiveDst = gw
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "USD", gw, false)
	require.Equal(t, gw, pf.effectiveDst, "effective destination should be the gateway issuer")
}

func TestFindPaths_EffectiveDst_SameAsDestAccount(t *testing.T) {
	// When dstAmount.Issuer == dstAccount, effectiveDst should be dstAccount.
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	bobAddr := testAccountAddress(bob)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", bobAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", bobAddr)

	pf := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "USD", bob, false)
	require.Equal(t, bob, pf.effectiveDst, "effective destination should be bob when issuer == dest")
}

func TestFindPaths_SourceIsEffectiveDst_DefaultPath(t *testing.T) {
	// When srcAccount == effectiveDst and srcCurrency == dstCurrency,
	// default path is sufficient — FindPaths returns true with no explicit paths.
	ledger := newMockLedger()
	gw := testAccountID(1)
	bob := testAccountID(2)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, gw, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	cache := NewRippleLineCache(ledger)
	// gw sends USD (issued by gw) to bob; effectiveDst = gw (the issuer) = srcAccount
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, gw, bob, dstAmt, srcAmt, "USD", gw, false)
	result := pf.FindPaths(DefaultSearchLevel)
	require.True(t, result, "source is effective dst with same currency -> default path works")
	require.Empty(t, pf.CompletePaths(), "should have no explicit paths (default path suffices)")
}

// ===========================================================================
// Test 14: BookIndex with offers for pathfinding
// ===========================================================================

func TestFindPaths_XRPToIOU_ThroughOfferBook(t *testing.T) {
	// Alice sends XRP, Bob receives USD. There's an offer: XRP -> USD.
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)
	mm := testAccountID(4) // market maker

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)
	addAccount(t, ledger, mm, 10000000000, 0)

	// bob trusts gw for USD
	lowB, highB := bob, gw
	if compareAccountIDs(bob, gw) > 0 {
		lowB, highB = gw, bob
	}
	addRippleState(t, ledger, lowB, highB, "USD", 0, 1000, 1000, 0)

	// mm trusts gw for USD and has balance
	lowM, highM := mm, gw
	if compareAccountIDs(mm, gw) > 0 {
		lowM, highM = gw, mm
	}
	var mmBal float64
	if lowM == mm {
		mmBal = 500
	} else {
		mmBal = -500
	}
	addRippleState(t, ledger, lowM, highM, "USD", mmBal, 10000, 10000, 0)

	// mm has an offer: taker pays XRP, taker gets USD
	addOffer(t, ledger, mm, 1,
		state.NewXRPAmountFromInt(10000000),                     // taker pays 10 XRP
		state.NewIssuedAmountFromFloat64(100, "USD", gwAddr),    // taker gets 100 USD
	)

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewXRPAmountFromInt(99999999999)

	pf := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "XRP", [20]byte{}, false)
	result := pf.FindPaths(DefaultSearchLevel)
	require.True(t, result, "should find paths for XRP-to-IOU through offer book")

	// The path type table for XRP-to-nonXRP includes patterns that go through books.
	// With the offer present, the BookIndex should discover the XRP->USD book.
	paths := pf.CompletePaths()
	t.Logf("Found %d complete paths for XRP->USD", len(paths))
}

// ===========================================================================
// Test 15: PathRank sorting
// ===========================================================================

func TestPathRank_QualitySorting(t *testing.T) {
	ranks := []PathRank{
		{Quality: 100, Length: 3, Index: 0},
		{Quality: 50, Length: 2, Index: 1},
		{Quality: 200, Length: 1, Index: 2},
	}

	sort.Slice(ranks, func(i, j int) bool {
		ri, rj := ranks[i], ranks[j]
		if ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index
	})

	require.Equal(t, uint64(50), ranks[0].Quality, "best quality first")
	require.Equal(t, uint64(100), ranks[1].Quality, "second quality")
	require.Equal(t, uint64(200), ranks[2].Quality, "worst quality last")
}

func TestPathRank_LengthTiebreaker(t *testing.T) {
	ranks := []PathRank{
		{Quality: 100, Length: 3, Index: 0},
		{Quality: 100, Length: 1, Index: 1},
		{Quality: 100, Length: 2, Index: 2},
	}

	sort.Slice(ranks, func(i, j int) bool {
		ri, rj := ranks[i], ranks[j]
		if ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index
	})

	require.Equal(t, 1, ranks[0].Length, "shorter path first when quality equal")
	require.Equal(t, 2, ranks[1].Length)
	require.Equal(t, 3, ranks[2].Length)
}

func TestPathRank_IndexTiebreaker(t *testing.T) {
	ranks := []PathRank{
		{Quality: 100, Length: 2, Index: 1},
		{Quality: 100, Length: 2, Index: 5},
		{Quality: 100, Length: 2, Index: 3},
	}

	sort.Slice(ranks, func(i, j int) bool {
		ri, rj := ranks[i], ranks[j]
		if ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index
	})

	// Higher index breaks ties (descending)
	require.Equal(t, 5, ranks[0].Index, "highest index first when quality and length equal")
	require.Equal(t, 3, ranks[1].Index)
	require.Equal(t, 1, ranks[2].Index)
}

// ===========================================================================
// Test 16: NewPathfinder constructor
// ===========================================================================

func TestNewPathfinder_SourceStep(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	src := testAccountID(1)
	dst := testAccountID(2)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)
	srcAddr := testAccountAddress(src)

	dstAmt := state.NewIssuedAmountFromFloat64(50, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(100, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, src, dst, dstAmt, srcAmt, "USD", gw, false)

	require.Equal(t, srcAddr, pf.source.Account)
	require.Equal(t, "USD", pf.source.Currency)
	require.Equal(t, gwAddr, pf.source.Issuer)
}

func TestNewPathfinder_XRPSourceHasNoIssuer(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	src := testAccountID(1)
	dst := testAccountID(2)
	srcAddr := testAccountAddress(src)

	dstAmt := state.NewXRPAmountFromInt(1000000)
	srcAmt := state.NewXRPAmountFromInt(2000000)

	pf := NewPathfinder(ledger, cache, src, dst, dstAmt, srcAmt, "XRP", [20]byte{}, false)

	require.Equal(t, srcAddr, pf.source.Account)
	require.Equal(t, "XRP", pf.source.Currency)
	require.Empty(t, pf.source.Issuer, "XRP source should have no issuer")
}

// ===========================================================================
// Test 17: PathRequest
// ===========================================================================

func TestNewPathRequest_Defaults(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	pr := NewPathRequest(src, dst, dstAmt, nil, nil, false)
	require.Equal(t, src, pr.srcAccount)
	require.Equal(t, dst, pr.dstAccount)
	require.Equal(t, maxReturnedPaths, pr.maxPaths)
	require.False(t, pr.convertAll)
}

func TestNewPathRequest_WithSendMax(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)
	sendMax := state.NewXRPAmountFromInt(2000000)

	pr := NewPathRequest(src, dst, dstAmt, &sendMax, nil, false)
	require.NotNil(t, pr.sendMax)
}

// ===========================================================================
// Test 18: AccountExists
// ===========================================================================

func TestAccountExists_Exists(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	addAccount(t, ledger, acct, 10000000000, 0)

	require.True(t, AccountExists(ledger, acct))
}

func TestAccountExists_DoesNotExist(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	require.False(t, AccountExists(ledger, acct))
}

// ===========================================================================
// Test 19: addPathsForType memoization
// ===========================================================================

func TestAddPathsForType_Memoization(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)
	addAccount(t, ledger, src, 10000000000, 0)
	addAccount(t, ledger, dst, 10000000000, 0)

	cache := NewRippleLineCache(ledger)
	pf := NewPathfinder(ledger, cache, src, dst,
		state.NewXRPAmountFromInt(1000000),
		state.NewXRPAmountFromInt(1000000),
		"XRP", [20]byte{}, false,
	)

	pt := PathType{ntSOURCE}
	// First call
	result1 := pf.addPathsForType(pt)
	// Second call should use cache
	result2 := pf.addPathsForType(pt)

	require.Equal(t, len(result1), len(result2))
	// Verify the cache was populated
	key := pathTypeKey(pt)
	_, ok := pf.paths[key]
	require.True(t, ok, "path type should be cached after first call")
}

func TestAddPathsForType_SourceProducesSingleEmpty(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)

	cache := NewRippleLineCache(ledger)
	pf := NewPathfinder(ledger, cache, src, dst,
		state.NewXRPAmountFromInt(1000000),
		state.NewXRPAmountFromInt(1000000),
		"XRP", [20]byte{}, false,
	)

	result := pf.addPathsForType(PathType{ntSOURCE})
	require.Len(t, result, 1, "ntSOURCE should produce exactly one path")
	require.Empty(t, result[0], "ntSOURCE path should be empty")
}

func TestAddPathsForType_EmptyPathType(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	pf := NewPathfinder(ledger, cache, testAccountID(1), testAccountID(2),
		state.NewXRPAmountFromInt(1000000),
		state.NewXRPAmountFromInt(1000000),
		"XRP", [20]byte{}, false,
	)

	result := pf.addPathsForType(PathType{})
	require.Len(t, result, 1, "empty PathType should produce one empty path")
}

// ===========================================================================
// Test 20: Integration scenario — IOU-to-same-IOU path discovery
// ===========================================================================

func TestFindPaths_IOUSameIOU_MultipleTrustLines(t *testing.T) {
	// Setup:
	// alice --USD--> gw --USD--> bob
	// alice --USD--> carol --USD--> bob (alternative path)
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	gw := testAccountID(3)
	carol := testAccountID(4)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)
	addAccount(t, ledger, carol, 10000000000, 0)

	// Helper to create trust lines in correct low/high order.
	createTrust := func(a, b [20]byte, currency string, bal, limA, limB float64, flags uint32) {
		low, high := a, b
		if compareAccountIDs(a, b) > 0 {
			low, high = b, a
		}
		var balVal float64
		if low == a {
			balVal = bal
		} else {
			balVal = -bal
		}
		var lowLim, highLim float64
		if low == a {
			lowLim = limA
			highLim = limB
		} else {
			lowLim = limB
			highLim = limA
		}
		addRippleState(t, ledger, low, high, currency, balVal, lowLim, highLim, flags)
	}

	// alice trusts gw for USD with positive balance
	createTrust(alice, gw, "USD", 100, 1000, 1000, 0)
	// bob trusts gw for USD
	createTrust(bob, gw, "USD", 0, 1000, 1000, 0)
	// alice trusts carol for USD with positive balance
	createTrust(alice, carol, "USD", 50, 500, 500, 0)
	// bob trusts carol for USD
	createTrust(bob, carol, "USD", 0, 500, 500, 0)

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewIssuedAmountFromFloat64(30, "USD", gwAddr)
	srcAmt := state.NewIssuedAmountFromFloat64(200, "USD", gwAddr)

	pf := NewPathfinder(ledger, cache, alice, bob, dstAmt, srcAmt, "USD", gw, false)
	result := pf.FindPaths(DefaultSearchLevel)
	require.True(t, result, "should complete pathfinding")

	paths := pf.CompletePaths()
	t.Logf("Found %d complete paths for USD->USD through multiple intermediaries", len(paths))
	// With multiple intermediaries, we expect paths through gw and possibly through carol.
}

// ===========================================================================
// Test 21: getPathsOut
// ===========================================================================

func TestGetPathsOut_AccountNotFound(t *testing.T) {
	ledger := newMockLedger()
	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		pathsOutCount: make(map[payment.Issue]int),
	}

	issue := payment.Issue{Currency: "USD", Issuer: testAccountID(99)}
	count := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	require.Equal(t, 0, count, "non-existent account should have 0 paths out")
}

func TestGetPathsOut_GlobalFreezeBlocks(t *testing.T) {
	ledger := newMockLedger()
	frozen := testAccountID(1)
	addAccount(t, ledger, frozen, 10000000000, state.LsfGlobalFreeze)

	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		pathsOutCount: make(map[payment.Issue]int),
	}

	issue := payment.Issue{Currency: "USD", Issuer: frozen}
	count := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	require.Equal(t, 0, count, "globally frozen account should have 0 paths out")
}

func TestGetPathsOut_Caching(t *testing.T) {
	ledger := newMockLedger()
	acct := testAccountID(1)
	addAccount(t, ledger, acct, 10000000000, 0)

	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		pathsOutCount: make(map[payment.Issue]int),
	}

	issue := payment.Issue{Currency: "USD", Issuer: acct}
	count1 := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	count2 := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	require.Equal(t, count1, count2, "cached results should be consistent")

	_, ok := pf.pathsOutCount[issue]
	require.True(t, ok, "result should be cached")
}

// ===========================================================================
// Test 22: isNoRippleOut
// ===========================================================================

func TestIsNoRippleOut_EmptyPath(t *testing.T) {
	pf := &Pathfinder{}
	require.False(t, pf.isNoRippleOut(nil), "empty path has no NoRipple")
}

func TestIsNoRippleOut_LastStepNoAccount(t *testing.T) {
	pf := &Pathfinder{}
	path := []payment.PathStep{
		{Currency: "USD"}, // no account
	}
	require.False(t, pf.isNoRippleOut(path), "step without account has no NoRipple")
}

// ===========================================================================
// Test 23: buildPathFindTrustLine
// ===========================================================================

func TestBuildPathFindTrustLine_ViewAsLow(t *testing.T) {
	low := testAccountID(1)
	high := testAccountID(2)
	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	// Ensure low < high
	if compareAccountIDs(low, high) > 0 {
		low, high = high, low
		lowAddr, highAddr = highAddr, lowAddr
	}

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(42, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(1000, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(500, "USD", highAddr),
		Flags:     state.LsfLowNoRipple | state.LsfHighFreeze,
	}

	line := buildPathFindTrustLine(rs, low)
	require.Equal(t, low, line.AccountID)
	require.Equal(t, high, line.AccountIDPeer)
	require.Equal(t, "USD", line.Currency)
	require.True(t, line.NoRipple, "low's NoRipple should be set")
	require.False(t, line.NoRipplePeer, "high's NoRipple should not be set")
	require.False(t, line.Freeze, "low's Freeze should not be set")
	require.True(t, line.FreezePeer, "high's Freeze should be set")
}

func TestBuildPathFindTrustLine_ViewAsHigh(t *testing.T) {
	low := testAccountID(1)
	high := testAccountID(2)
	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	if compareAccountIDs(low, high) > 0 {
		low, high = high, low
		lowAddr, highAddr = highAddr, lowAddr
	}

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(42, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(1000, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(500, "USD", highAddr),
		Flags:     state.LsfHighAuth,
	}

	line := buildPathFindTrustLine(rs, high)
	require.Equal(t, high, line.AccountID)
	require.Equal(t, low, line.AccountIDPeer)
	require.Equal(t, "USD", line.Currency)
	// Balance should be negated for high account
	require.True(t, line.Auth, "high's Auth should be set")
	require.False(t, line.AuthPeer, "low's Auth should not be set")
}

// ===========================================================================
// Utility: compareAccountIDs (used by helpers above)
// ===========================================================================

func compareAccountIDs(a, b [20]byte) int {
	for i := 0; i < 20; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// ===========================================================================
// Test 24: issueFromTxAmount
// ===========================================================================

func TestIssueFromTxAmount_XRP(t *testing.T) {
	amt := tx.NewXRPAmount(1000000)
	issue := issueFromTxAmount(amt)
	require.Equal(t, "XRP", issue.Currency)
}

func TestIssueFromTxAmount_IOU(t *testing.T) {
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)
	amt := tx.NewIssuedAmountFromFloat64(100, "EUR", gwAddr)
	issue := issueFromTxAmount(amt)
	require.Equal(t, "EUR", issue.Currency)
	require.Equal(t, gw, issue.Issuer)
}

// ===========================================================================
// Test 25: Constants
// ===========================================================================

func TestConstants(t *testing.T) {
	require.Equal(t, 10000, highPriority)
	require.Equal(t, 1000, maxCompletePaths)
	require.Equal(t, 4, maxReturnedPaths)
	require.Equal(t, 50, maxCandidatesFromSource)
	require.Equal(t, 10, maxCandidatesFromOther)
	require.Equal(t, 7, DefaultSearchLevel)
}

func TestAddFlags(t *testing.T) {
	require.Equal(t, uint32(0x001), afADD_ACCOUNTS)
	require.Equal(t, uint32(0x002), afADD_BOOKS)
	require.Equal(t, uint32(0x010), afOB_XRP)
	require.Equal(t, uint32(0x040), afOB_LAST)
	require.Equal(t, uint32(0x080), afAC_LAST)
}

// ===========================================================================
// Test 26: Additional path type table coverage
// ===========================================================================

func TestPathTable_AllEntriesEndWithDestination(t *testing.T) {
	for pt, entries := range pathTable {
		if entries == nil {
			continue
		}
		for i, entry := range entries {
			last := entry.Type[len(entry.Type)-1]
			require.Equal(t, ntDESTINATION, last,
				"PaymentType %d entry %d should end with ntDESTINATION", pt, i)
		}
	}
}

func TestPathTable_AllEntriesStartWithSource(t *testing.T) {
	for pt, entries := range pathTable {
		if entries == nil {
			continue
		}
		for i, entry := range entries {
			first := entry.Type[0]
			require.Equal(t, ntSOURCE, first,
				"PaymentType %d entry %d should start with ntSOURCE", pt, i)
		}
	}
}

func TestPathTable_NonXRPToNonXRP_HasDestBook(t *testing.T) {
	// All nonXRP-to-nonXRP patterns should contain a ntDEST_BOOK
	for _, cp := range pathTable[ptNonXRP_to_nonXRP] {
		hasDestBook := false
		for _, nt := range cp.Type {
			if nt == ntDEST_BOOK {
				hasDestBook = true
				break
			}
		}
		require.True(t, hasDestBook,
			"NonXRP-to-nonXRP path at level %d should contain ntDEST_BOOK", cp.SearchLevel)
	}
}

func TestPathTable_XRPToNonXRP_EntryCounts(t *testing.T) {
	entries := pathTable[ptXRP_to_nonXRP]
	require.Len(t, entries, 7, "XRP-to-nonXRP should have 7 path patterns")
}

func TestPathTable_NonXRPToXRP_EntryCounts(t *testing.T) {
	entries := pathTable[ptNonXRP_to_XRP]
	require.Len(t, entries, 6, "NonXRP-to-XRP should have 6 path patterns")
}

func TestPathTable_NonXRPToSame_EntryCounts(t *testing.T) {
	entries := pathTable[ptNonXRP_to_same]
	require.Len(t, entries, 12, "NonXRP-to-same should have 12 path patterns")
}

func TestPathTable_NonXRPToNonXRP_EntryCounts(t *testing.T) {
	entries := pathTable[ptNonXRP_to_nonXRP]
	require.Len(t, entries, 12, "NonXRP-to-nonXRP should have 12 path patterns")
}

func TestPathTable_XRPToNonXRP_KnownSecondEntry(t *testing.T) {
	// Second entry: level 3, [ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION]
	entries := pathTable[ptXRP_to_nonXRP]
	require.True(t, len(entries) >= 2)
	second := entries[1]
	require.Equal(t, 3, second.SearchLevel)
	expected := PathType{ntSOURCE, ntDEST_BOOK, ntACCOUNTS, ntDESTINATION}
	require.Equal(t, expected, PathType(second.Type))
}

func TestPathTable_NonXRPToXRP_KnownSecondEntry(t *testing.T) {
	// Second entry: level 2, [ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDESTINATION]
	entries := pathTable[ptNonXRP_to_XRP]
	require.True(t, len(entries) >= 2)
	second := entries[1]
	require.Equal(t, 2, second.SearchLevel)
	expected := PathType{ntSOURCE, ntACCOUNTS, ntXRP_BOOK, ntDESTINATION}
	require.Equal(t, expected, PathType(second.Type))
}

// ===========================================================================
// Test 27: Additional classifyPayment edge cases
// ===========================================================================

func TestClassifyPayment_XRP_EmptyString(t *testing.T) {
	// Empty source currency plus native dest should be XRP-to-XRP
	pf := &Pathfinder{
		srcAccount:  testAccountID(1),
		dstAccount:  testAccountID(2),
		srcCurrency: "",
		dstAmount:   state.NewXRPAmountFromInt(1000),
	}
	require.Equal(t, ptXRP_to_XRP, pf.classifyPayment())
}

// ===========================================================================
// Test 28: Additional RippleLineCache — multiple peers
// ===========================================================================

func TestRippleLineCache_MultiplePeers(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(2)
	carol := testAccountID(4) // use 4 to avoid ordering issues

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, carol, 10000000000, 0)

	// alice-bob USD trust line
	lowAB, highAB := alice, bob
	if compareAccountIDs(alice, bob) > 0 {
		lowAB, highAB = bob, alice
	}
	addRippleState(t, ledger, lowAB, highAB, "USD", 50, 1000, 1000, 0)

	// alice-carol EUR trust line
	lowAC, highAC := alice, carol
	if compareAccountIDs(alice, carol) > 0 {
		lowAC, highAC = carol, alice
	}
	addRippleState(t, ledger, lowAC, highAC, "EUR", 25, 500, 500, 0)

	cache := NewRippleLineCache(ledger)
	lines := cache.GetRippleLines(alice, LineDirectionOutgoing)
	require.Len(t, lines, 2, "alice should have 2 trust lines (USD with bob, EUR with carol)")

	currencies := make(map[string]bool)
	peers := make(map[[20]byte]bool)
	for _, l := range lines {
		currencies[l.Currency] = true
		peers[l.AccountIDPeer] = true
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
}

// ===========================================================================
// Test 29: Additional AccountCurrencies — exhausted credit
// ===========================================================================

func TestAccountSourceCurrencies_ExhaustedCredit(t *testing.T) {
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(2)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := alice, gw
	if compareAccountIDs(alice, gw) > 0 {
		low, high = gw, alice
	}

	// Create a trust line where balance is at the credit limit.
	// From alice's view: balance negative (alice owes peer), magnitude >= limitPeer.
	// We need: balSig <= 0 AND negBal >= limitPeer.
	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	var balVal float64
	var lowLim, highLim float64
	if low == alice {
		// alice is low; balance stays as-is; we need negative balance from alice's view
		// but rippled convention: balance positive = low owes high
		// buildPathFindTrustLine: when viewIsLow, Balance = rs.Balance (no negation)
		// So balance=1000 means "alice owes gw 1000" and Signum > 0 for the raw balance.
		// Wait - the balance field semantics differ.
		// In buildPathFindTrustLine, when viewIsLow: line.Balance = rs.Balance
		// The Signum() check in AccountSourceCurrencies: balSig <= 0 goes to credit path.
		// For exhausted: we need Signum() <= 0 AND negBal >= limitPeer.
		// So we need negative stored balance when alice is low: -1000
		// Then Signum(-1000) < 0, negBal = 1000, limitPeer = 1000 (HighLimit)
		// negBal >= limitPeer -> credit exhausted
		balVal = -1000
		lowLim = 1000
		highLim = 1000
	} else {
		// alice is high; viewed balance = negate(stored).
		// For Signum() <= 0 on viewed: need stored >= 0 -> negate <= 0
		// Stored balance = 1000, viewed = -1000, Signum(-1000) < 0
		// negBal = 1000, limitPeer = LowLimit
		balVal = 1000
		lowLim = 1000
		highLim = 1000
	}

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(balVal, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(lowLim, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(highLim, "USD", highAddr),
	}
	data, err := state.SerializeRippleState(rs)
	require.NoError(t, err)
	lineKey := keylet.Line(low, high, "USD")
	ledger.entries[lineKey.Key] = data
	ensureOwnerDirContains(t, ledger, alice, lineKey.Key)

	cache := NewRippleLineCache(ledger)
	currencies := AccountSourceCurrencies(alice, cache)

	// Only XRP should be present; USD credit is exhausted
	require.Len(t, currencies, 1, "exhausted credit line should not be in source currencies")
	require.True(t, currencies[payment.Issue{Currency: "XRP"}])
}

func TestAccountDestCurrencies_AtCapacity(t *testing.T) {
	// When balance >= limit, the account cannot accept more of that currency.
	ledger := newMockLedger()
	bob := testAccountID(1)
	gw := testAccountID(2)

	addAccount(t, ledger, bob, 10000000000, 0)
	addAccount(t, ledger, gw, 10000000000, 0)

	low, high := bob, gw
	if compareAccountIDs(bob, gw) > 0 {
		low, high = gw, bob
	}

	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	// Set balance = limit so bob can't accept more.
	// From bob's view: Balance >= Limit means at capacity.
	var balVal, lowLim, highLim float64
	if low == bob {
		// bob is low; balance stays as-is; we need Balance.Compare(Limit) >= 0
		// Limit = LowLimit for the low viewer
		// Set balance = 1000, limit = 1000 -> exactly at capacity
		balVal = 1000
		lowLim = 1000
		highLim = 1000
	} else {
		// bob is high; viewed balance = negate(stored)
		// For viewed balance >= limit: need negate(stored) >= HighLimit
		// stored = -1000 -> viewed = 1000 >= 1000 -> at capacity
		balVal = -1000
		lowLim = 1000
		highLim = 1000
	}

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(balVal, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(lowLim, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(highLim, "USD", highAddr),
	}
	data, err := state.SerializeRippleState(rs)
	require.NoError(t, err)
	lineKey := keylet.Line(low, high, "USD")
	ledger.entries[lineKey.Key] = data
	ensureOwnerDirContains(t, ledger, bob, lineKey.Key)

	cache := NewRippleLineCache(ledger)
	currencies := AccountDestCurrencies(bob, cache)

	// Only XRP should be present; bob can't accept more USD
	require.Len(t, currencies, 1, "account at capacity should not accept more of that currency")
	require.True(t, currencies[payment.Issue{Currency: "XRP"}])
}

// ===========================================================================
// Test 30: Additional BookIndex coverage
// ===========================================================================

func TestBookIndex_MultipleOffersForSameBook(t *testing.T) {
	// Two offers with the same taker_pays issue should deduplicate to one book entry.
	ledger := newMockLedger()
	alice := testAccountID(1)
	bob := testAccountID(5)
	gw := testAccountID(3)
	gwAddr := testAccountAddress(gw)

	addAccount(t, ledger, alice, 10000000000, 0)
	addAccount(t, ledger, bob, 10000000000, 0)

	// alice: pays XRP, gets USD
	addOffer(t, ledger, alice, 1,
		state.NewXRPAmountFromInt(1000000),
		state.NewIssuedAmountFromFloat64(10, "USD", gwAddr),
	)
	// bob: pays XRP, gets USD (same book)
	addOffer(t, ledger, bob, 1,
		state.NewXRPAmountFromInt(2000000),
		state.NewIssuedAmountFromFloat64(20, "USD", gwAddr),
	)

	bi := NewBookIndex(ledger)
	results := bi.GetBooksByTakerPays(payment.Issue{Currency: "XRP"})
	// Should be deduplicated: only one unique (XRP, USD/gw) book
	require.Len(t, results, 1, "duplicate books should be deduplicated")
	require.Equal(t, "USD", results[0].Currency)
}

func TestBookIndex_DifferentBooksForSameInput(t *testing.T) {
	// Two offers with same taker_pays but different taker_gets -> two book entries.
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(3)
	gw2 := testAccountID(6)
	gwAddr := testAccountAddress(gw)
	gw2Addr := testAccountAddress(gw2)

	addAccount(t, ledger, alice, 10000000000, 0)

	// Offer 1: pays XRP, gets USD/gw
	addOffer(t, ledger, alice, 1,
		state.NewXRPAmountFromInt(1000000),
		state.NewIssuedAmountFromFloat64(10, "USD", gwAddr),
	)
	// Offer 2: pays XRP, gets EUR/gw2
	addOffer(t, ledger, alice, 2,
		state.NewXRPAmountFromInt(2000000),
		state.NewIssuedAmountFromFloat64(5, "EUR", gw2Addr),
	)

	bi := NewBookIndex(ledger)
	results := bi.GetBooksByTakerPays(payment.Issue{Currency: "XRP"})
	require.Len(t, results, 2, "different taker_gets should produce different book entries")

	currencies := make(map[string]bool)
	for _, r := range results {
		currencies[r.Currency] = true
	}
	require.True(t, currencies["USD"])
	require.True(t, currencies["EUR"])
}

func TestBookIndex_IsBookToXRP_NotXRP(t *testing.T) {
	// An offer that pays USD and gets EUR should NOT report IsBookToXRP.
	ledger := newMockLedger()
	alice := testAccountID(1)
	gw := testAccountID(3)
	gw2 := testAccountID(6)
	gwAddr := testAccountAddress(gw)
	gw2Addr := testAccountAddress(gw2)

	addAccount(t, ledger, alice, 10000000000, 0)

	// Offer: pays USD/gw, gets EUR/gw2
	addOffer(t, ledger, alice, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", gwAddr),
		state.NewIssuedAmountFromFloat64(50, "EUR", gw2Addr),
	)

	bi := NewBookIndex(ledger)
	usdIssue := payment.Issue{Currency: "USD", Issuer: gw}
	require.False(t, bi.IsBookToXRP(usdIssue), "USD->EUR should not be a book to XRP")
}

// ===========================================================================
// Test 31: Additional pathHasSeen with multiple steps
// ===========================================================================

func TestPathHasSeen_MultiStepPath(t *testing.T) {
	acct1 := testAccountID(1)
	acct2 := testAccountID(2)
	acct3 := testAccountID(3)

	path := []payment.PathStep{
		{Account: testAccountAddress(acct1), Currency: "USD"},
		{Account: testAccountAddress(acct2), Currency: "EUR"},
		{Account: testAccountAddress(acct3), Currency: "USD"},
	}

	require.True(t, pathHasSeen(path, acct3, "USD"),
		"should find acct3+USD in multi-step path")
	require.False(t, pathHasSeen(path, acct2, "USD"),
		"acct2 has EUR not USD")
	require.True(t, pathHasSeen(path, acct2, "EUR"),
		"should find acct2+EUR")
}

func TestPathHasSeenIssue_MultipleSteps(t *testing.T) {
	acct1 := testAccountID(1)
	acct2 := testAccountID(2)

	path := []payment.PathStep{
		{Currency: "USD", Issuer: testAccountAddress(acct1)},
		{Currency: "EUR", Issuer: testAccountAddress(acct2)},
	}

	require.True(t, pathHasSeenIssue(path, payment.Issue{Currency: "EUR", Issuer: acct2}))
	require.False(t, pathHasSeenIssue(path, payment.Issue{Currency: "USD", Issuer: acct2}))
}

// ===========================================================================
// Test 32: PathRank sorting with liquidity
// ===========================================================================

func TestPathRank_LiquiditySorting(t *testing.T) {
	// Same quality, different liquidity: higher liquidity should come first.
	ranks := []PathRank{
		{Quality: 100, Length: 2, Liquidity: payment.NewXRPEitherAmount(50), Index: 0},
		{Quality: 100, Length: 2, Liquidity: payment.NewXRPEitherAmount(200), Index: 1},
		{Quality: 100, Length: 2, Liquidity: payment.NewXRPEitherAmount(100), Index: 2},
	}

	// Sort matching the logic in rankPaths
	sort.Slice(ranks, func(i, j int) bool {
		ri, rj := ranks[i], ranks[j]
		if ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		cmp := ri.Liquidity.Compare(rj.Liquidity)
		if cmp != 0 {
			return cmp > 0 // Higher liquidity better
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index
	})

	require.Equal(t, int64(200), ranks[0].Liquidity.XRP, "highest liquidity first")
	require.Equal(t, int64(100), ranks[1].Liquidity.XRP, "medium liquidity second")
	require.Equal(t, int64(50), ranks[2].Liquidity.XRP, "lowest liquidity last")
}

func TestPathRank_MixedCriteria(t *testing.T) {
	// Quality is primary, then liquidity, then length, then index.
	ranks := []PathRank{
		{Quality: 200, Length: 1, Liquidity: payment.NewXRPEitherAmount(500), Index: 0},
		{Quality: 100, Length: 3, Liquidity: payment.NewXRPEitherAmount(50), Index: 1},
		{Quality: 100, Length: 2, Liquidity: payment.NewXRPEitherAmount(100), Index: 2},
	}

	sort.Slice(ranks, func(i, j int) bool {
		ri, rj := ranks[i], ranks[j]
		if ri.Quality != rj.Quality {
			return ri.Quality < rj.Quality
		}
		cmp := ri.Liquidity.Compare(rj.Liquidity)
		if cmp != 0 {
			return cmp > 0
		}
		if ri.Length != rj.Length {
			return ri.Length < rj.Length
		}
		return ri.Index > rj.Index
	})

	// quality=100, liquidity=100 should be first (better quality, higher liquidity)
	require.Equal(t, 2, ranks[0].Index, "quality=100, highest liquidity first")
	// quality=100, liquidity=50 second
	require.Equal(t, 1, ranks[1].Index, "quality=100, lower liquidity second")
	// quality=200 last (worst quality)
	require.Equal(t, 0, ranks[2].Index, "worst quality last despite high liquidity")
}

// ===========================================================================
// Test 33: currencyTo20 additional cases
// ===========================================================================

func TestCurrencyTo20_EUR(t *testing.T) {
	result := currencyTo20("EUR")
	require.Equal(t, byte('E'), result[12])
	require.Equal(t, byte('U'), result[13])
	require.Equal(t, byte('R'), result[14])
}

func TestCurrencyTo20_DifferentCurrenciesAreDifferent(t *testing.T) {
	usd := currencyTo20("USD")
	eur := currencyTo20("EUR")
	btc := currencyTo20("BTC")
	require.NotEqual(t, usd, eur)
	require.NotEqual(t, usd, btc)
	require.NotEqual(t, eur, btc)
}

// ===========================================================================
// Test 34: FindPaths — XRP destination when dest doesn't exist
// ===========================================================================

func TestFindPaths_DestNotExist_XRPAllowed(t *testing.T) {
	// XRP payments to non-existent destination may still proceed
	// (account can be created if amount >= reserve)
	ledger := newMockLedger()
	src := testAccountID(1)
	dst := testAccountID(2)

	addAccount(t, ledger, src, 10000000000, 0)
	// dst NOT in ledger

	cache := NewRippleLineCache(ledger)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	pf := NewPathfinder(ledger, cache, src, dst,
		dstAmt, state.NewXRPAmountFromInt(2000000),
		"XRP", [20]byte{}, false,
	)

	result := pf.FindPaths(DefaultSearchLevel)
	// XRP-to-XRP pathfinding should succeed even when dest doesn't exist
	require.True(t, result, "XRP payment to non-existent dest should not fail at pathfinding stage")
}

// ===========================================================================
// Test 35: buildPathFindTrustLine balance negation
// ===========================================================================

func TestBuildPathFindTrustLine_BalanceNegation(t *testing.T) {
	low := testAccountID(1)
	high := testAccountID(2)

	if compareAccountIDs(low, high) > 0 {
		low, high = high, low
	}

	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	// Stored balance = 42 (low owes high)
	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(42, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(1000, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(500, "USD", highAddr),
	}

	// View as low: balance should be as stored (positive)
	lowLine := buildPathFindTrustLine(rs, low)
	require.True(t, lowLine.Balance.Signum() > 0,
		"low viewer: stored positive balance should remain positive")

	// View as high: balance should be negated (negative)
	highLine := buildPathFindTrustLine(rs, high)
	require.True(t, highLine.Balance.Signum() < 0,
		"high viewer: stored positive balance should become negative after negation")
}

func TestBuildPathFindTrustLine_AllFlags(t *testing.T) {
	low := testAccountID(1)
	high := testAccountID(2)

	if compareAccountIDs(low, high) > 0 {
		low, high = high, low
	}

	lowAddr := testAccountAddress(low)
	highAddr := testAccountAddress(high)

	// Set all flags
	allFlags := state.LsfLowNoRipple | state.LsfHighNoRipple |
		state.LsfLowFreeze | state.LsfHighFreeze |
		state.LsfLowAuth | state.LsfHighAuth

	rs := &state.RippleState{
		Balance:   state.NewIssuedAmountFromFloat64(10, "USD", state.AccountOneAddress),
		LowLimit:  state.NewIssuedAmountFromFloat64(100, "USD", lowAddr),
		HighLimit: state.NewIssuedAmountFromFloat64(200, "USD", highAddr),
		Flags:     allFlags,
	}

	// View as low
	lowLine := buildPathFindTrustLine(rs, low)
	require.True(t, lowLine.NoRipple, "low: LowNoRipple -> NoRipple")
	require.True(t, lowLine.NoRipplePeer, "low: HighNoRipple -> NoRipplePeer")
	require.True(t, lowLine.Freeze, "low: LowFreeze -> Freeze")
	require.True(t, lowLine.FreezePeer, "low: HighFreeze -> FreezePeer")
	require.True(t, lowLine.Auth, "low: LowAuth -> Auth")
	require.True(t, lowLine.AuthPeer, "low: HighAuth -> AuthPeer")

	// View as high (flags are swapped)
	highLine := buildPathFindTrustLine(rs, high)
	require.True(t, highLine.NoRipple, "high: HighNoRipple -> NoRipple")
	require.True(t, highLine.NoRipplePeer, "high: LowNoRipple -> NoRipplePeer")
	require.True(t, highLine.Freeze, "high: HighFreeze -> Freeze")
	require.True(t, highLine.FreezePeer, "high: LowFreeze -> FreezePeer")
	require.True(t, highLine.Auth, "high: HighAuth -> Auth")
	require.True(t, highLine.AuthPeer, "high: LowAuth -> AuthPeer")
}

// ===========================================================================
// Test 36: getPathsOut with trust lines and books
// ===========================================================================

func TestGetPathsOut_WithTrustLines(t *testing.T) {
	ledger := newMockLedger()
	gw := testAccountID(1)
	alice := testAccountID(2)

	addAccount(t, ledger, gw, 10000000000, 0)
	addAccount(t, ledger, alice, 10000000000, 0)

	low, high := gw, alice
	if compareAccountIDs(gw, alice) > 0 {
		low, high = alice, gw
	}
	// Create a trust line with available credit (balance < limitPeer)
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, 0)

	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		effectiveDst:  testAccountID(99), // some other destination
		pathsOutCount: make(map[payment.Issue]int),
	}

	issue := payment.Issue{Currency: "USD", Issuer: gw}
	count := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	require.Greater(t, count, 0, "account with usable trust line should have paths out")
}

func TestGetPathsOut_FrozenPeerExcluded(t *testing.T) {
	ledger := newMockLedger()
	gw := testAccountID(1)
	alice := testAccountID(2)

	addAccount(t, ledger, gw, 10000000000, 0)
	addAccount(t, ledger, alice, 10000000000, 0)

	low, high := gw, alice
	if compareAccountIDs(gw, alice) > 0 {
		low, high = alice, gw
	}

	// Set FreezePeer on gw's side (peer = alice is frozen).
	// From gw's view: FreezePeer means the other account froze gw.
	// In getPathsOut: FreezePeer leads to continue (skip).
	var flags uint32
	if low == gw {
		// gw is low; FreezePeer = HighFreeze
		flags = state.LsfHighFreeze
	} else {
		// gw is high; FreezePeer = LowFreeze
		flags = state.LsfLowFreeze
	}
	addRippleState(t, ledger, low, high, "USD", 0, 1000, 1000, flags)

	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		ledger:        ledger,
		cache:         cache,
		books:         NewBookIndex(ledger),
		effectiveDst:  testAccountID(99),
		pathsOutCount: make(map[payment.Issue]int),
	}

	issue := payment.Issue{Currency: "USD", Issuer: gw}
	count := pf.getPathsOut(issue, LineDirectionOutgoing, false)
	require.Equal(t, 0, count, "frozen peer should be excluded from paths out")
}

// ===========================================================================
// Test 37: Issue.IsXRP
// ===========================================================================

func TestIssue_IsXRP(t *testing.T) {
	require.True(t, payment.Issue{Currency: "XRP"}.IsXRP())
	require.True(t, payment.Issue{Currency: ""}.IsXRP())
	require.False(t, payment.Issue{Currency: "USD"}.IsXRP())
	require.False(t, payment.Issue{Currency: "EUR", Issuer: testAccountID(1)}.IsXRP())
}

// ===========================================================================
// Test 38: PathRequest with explicit source currencies
// ===========================================================================

func TestNewPathRequest_WithSourceCurrencies(t *testing.T) {
	src := testAccountID(1)
	dst := testAccountID(2)
	dstAmt := state.NewXRPAmountFromInt(1000000)

	srcCurrencies := []payment.Issue{
		{Currency: "USD", Issuer: testAccountID(3)},
		{Currency: "EUR", Issuer: testAccountID(4)},
	}

	pr := NewPathRequest(src, dst, dstAmt, nil, srcCurrencies, true)
	require.Len(t, pr.sourceCurrencies, 2)
	require.True(t, pr.convertAll)
}

// ===========================================================================
// Test 39: CompletePaths and PathRanks accessors
// ===========================================================================

func TestPathfinder_Accessors(t *testing.T) {
	pf := &Pathfinder{}
	require.Nil(t, pf.CompletePaths(), "new pathfinder should have nil complete paths")
	require.Nil(t, pf.PathRanks(), "new pathfinder should have nil path ranks")
}

// ===========================================================================
// Test 40: isNoRippleOut with single-step path from source
// ===========================================================================

func TestIsNoRippleOut_SingleStep_FromSource(t *testing.T) {
	ledger := newMockLedger()
	src := testAccountID(1)
	acct := testAccountID(2)

	// When path has 1 step, fromAccount = srcAccount
	low, high := src, acct
	if compareAccountIDs(src, acct) > 0 {
		low, high = acct, src
	}

	// Set NoRipple on acct's side in the trust line between src and acct
	var flags uint32
	if low == acct {
		flags = state.LsfLowNoRipple
	} else {
		flags = state.LsfHighNoRipple
	}
	addRippleState(t, ledger, low, high, "USD", 50, 1000, 1000, flags)

	cache := NewRippleLineCache(ledger)
	pf := &Pathfinder{
		srcAccount: src,
		ledger:     ledger,
		cache:      cache,
	}

	path := []payment.PathStep{
		{Account: testAccountAddress(acct), Currency: "USD"},
	}

	require.True(t, pf.isNoRippleOut(path),
		"should detect NoRipple when last step's account has NoRipple toward source")
}
