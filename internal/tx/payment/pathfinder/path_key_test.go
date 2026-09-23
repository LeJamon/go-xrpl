package pathfinder

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx/mptutil"
	"github.com/LeJamon/go-xrpl/internal/tx/payment"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/stretchr/testify/require"
)

func TestPathKeyEquality(t *testing.T) {
	account := testAccountAddress(testAccountID(1))
	issuer := testAccountAddress(testAccountID(2))
	id := mptutil.EncodeID(keylet.MakeMPTID(0xabcdef, testAccountID(2)))
	tests := []struct {
		name string
		a, b payment.PathStep
	}{
		{"currency spelling", payment.PathStep{Type: 0x30, Currency: "USD", Issuer: issuer}, payment.PathStep{Type: 0x30, Currency: "0000000000000000000000005553440000000000", Issuer: issuer}},
		{"currency hex case", payment.PathStep{Type: 0x10, Currency: strings.Repeat("ab", 20)}, payment.PathStep{Type: 0x10, Currency: strings.Repeat("AB", 20)}},
		{"native currency", payment.PathStep{Type: 0x10, Currency: "XRP"}, payment.PathStep{}},
		{"zero issuer", payment.PathStep{Type: 0x30, Currency: "XRP", Issuer: testAccountAddress([20]byte{})}, payment.PathStep{Type: 0x10, Currency: "XRP"}},
		{"asset presence bits", payment.PathStep{Type: 0x01, Account: account, Currency: "USD", Issuer: issuer}, payment.PathStep{Type: 0x31, Account: account, Currency: "USD", Issuer: issuer}},
		{"MPT case", payment.PathStep{Type: 0x60, MPTIssuanceID: id, Issuer: issuer}, payment.PathStep{Type: 0x60, MPTIssuanceID: strings.ToLower(id), Issuer: issuer}},
		{"MPT issuer presence bit", payment.PathStep{Type: 0x40, MPTIssuanceID: id, Issuer: issuer}, payment.PathStep{Type: 0x60, MPTIssuanceID: id, Issuer: issuer}},
		{"display type", payment.PathStep{Type: 0x01, Account: account, TypeHex: "0000000000000001"}, payment.PathStep{Type: 0x01, Account: account}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, pathKey([]payment.PathStep{test.a}), pathKey([]payment.PathStep{test.b}))
		})
	}
	require.Equal(t, pathKey(nil), pathKey([]payment.PathStep{}))
}

func TestPathKeyDistinguishesPaths(t *testing.T) {
	account := testAccountAddress(testAccountID(1))
	issuer := testAccountAddress(testAccountID(2))
	id := mptutil.EncodeID(keylet.MakeMPTID(1, testAccountID(2)))
	otherID := mptutil.EncodeID(keylet.MakeMPTID(2, testAccountID(2)))
	paths := [][]payment.PathStep{
		nil,
		{{Type: 0x10, Currency: "XRP"}},
		{{Type: 0x01, Account: account}},
		{{Account: account}},
		{{Type: 0x01, Account: issuer}},
		{{Type: 0x30, Currency: "USD", Issuer: issuer}},
		{{Type: 0x30, Currency: "USD", Issuer: account}},
		{{Type: 0x30, Currency: "usd", Issuer: issuer}},
		{{Type: 0x60, MPTIssuanceID: id, Issuer: issuer}},
		{{Type: 0x60, MPTIssuanceID: otherID, Issuer: issuer}},
		{{Type: 0x40, MPTIssuanceID: strings.Repeat("0", 48)}},
		{{Type: 0x01, Account: account}, {Type: 0x30, Currency: "USD", Issuer: issuer}},
		{{Type: 0x30, Currency: "USD", Issuer: issuer}, {Type: 0x01, Account: account}},
		{{Type: 0x01, Account: account}, {Type: 0x01, Account: account}},
		{{Account: "a\x00b", Currency: "c"}},
		{{Account: "a", Currency: "b\x00c"}},
	}
	seen := make(map[string]int)
	for i, path := range paths {
		key := pathKey(path)
		if previous, exists := seen[key]; exists {
			t.Errorf("paths %d and %d have the same key", previous, i)
		}
		seen[key] = i
	}
}

func TestAddUniquePathCandidateIdentityAndOrder(t *testing.T) {
	account := testAccountAddress(testAccountID(1))
	issuer := testAccountAddress(testAccountID(2))
	id := mptutil.EncodeID(keylet.MakeMPTID(0xabcdef, testAccountID(2)))
	accountUSD := payment.PathStep{Type: 0x01, Account: account, Currency: "USD", Issuer: issuer}
	accountEUR := payment.PathStep{Type: 0x01, Account: account, Currency: "EUR", Issuer: issuer}
	book := payment.PathStep{Type: 0x30, Currency: "USD", Issuer: issuer}
	equivalentBook := payment.PathStep{Type: 0x30, Currency: "0000000000000000000000005553440000000000", Issuer: issuer}
	mpt := payment.PathStep{Type: 0x60, MPTIssuanceID: id, Issuer: issuer}
	equivalentMPT := payment.PathStep{Type: 0x40, MPTIssuanceID: strings.ToLower(id), Issuer: issuer}

	pf := &Pathfinder{}
	for _, path := range [][]payment.PathStep{
		{accountUSD, book},
		{accountUSD, equivalentBook},
		{book, accountUSD},
		{accountEUR, book},
		{mpt},
		{equivalentMPT},
		{accountUSD, book},
	} {
		pf.addUniquePath(path)
	}
	accountOnly := payment.PathStep{Type: 0x01, Account: account}
	require.Equal(t, [][]payment.PathStep{
		{accountOnly, book},
		{book, accountOnly},
		{accountOnly, book},
		{mpt},
	}, pf.CompletePaths())
	require.Len(t, pf.completePathKeys, len(pf.CompletePaths()))
}

func TestAddLinksDeduplicatesCompressedCandidates(t *testing.T) {
	source, destination := testAccountID(1), testAccountID(2)
	first, second := testAccountID(3), testAccountID(4)
	outputs := []payment.Issue{
		{Currency: "EUR", Issuer: testAccountID(5)},
		payment.NewMPTIssue(keylet.MakeMPTID(1, testAccountID(6))),
	}
	pf := &Pathfinder{
		srcAccount:   source,
		dstAccount:   destination,
		effectiveDst: destination,
		srcIssue:     payment.Issue{Currency: "XRP"},
		dstAmount:    state.NewIssuedAmountFromFloat64(10, "JPY", testAccountAddress(destination)),
		books: &BookIndex{
			built: true,
			byTakerPays: map[payment.Issue][]payment.Issue{
				{Currency: "USD", Issuer: first}:  outputs,
				{Currency: "USD", Issuer: second}: outputs,
			},
		},
	}
	book := pathStepForIssue(payment.Issue{Currency: "USD", Issuer: testAccountID(7)})
	parents := [][]payment.PathStep{
		{book, {Type: 0x01, Account: testAccountAddress(first), Currency: "USD", Issuer: testAccountAddress(first)}},
		{book, {Type: 0x01, Account: testAccountAddress(second), Currency: "USD", Issuer: testAccountAddress(second)}},
	}
	paths := pf.addLinks(parents, afADD_BOOKS)
	require.Len(t, paths, 2)
	for i, output := range outputs {
		expectedBook := pathStepForIssue(output)
		require.Equal(t, []payment.PathStep{
			book, expectedBook,
			{Type: 0x01, Account: expectedBook.Issuer, Currency: expectedBook.Currency, Issuer: expectedBook.Issuer, MPTIssuanceID: expectedBook.MPTIssuanceID},
		}, paths[i])
	}
	require.Empty(t, pf.CompletePaths())
}

func TestAddLinksOrdersAccountCandidatesBeforeLimiting(t *testing.T) {
	for _, test := range []struct {
		name       string
		count      int
		fromSource bool
		wantCount  int
	}{
		{"uncapped", 3, true, 3},
		{"source limit", 52, true, 50},
		{"intermediary limit", 12, false, 10},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, reverse := range []bool{false, true} {
				source, destination := testAccountID(1), testAccountID(3)
				endpoint := source
				var parent []payment.PathStep
				if !test.fromSource {
					endpoint = testAccountID(2)
					parent = []payment.PathStep{{Type: 1, Account: testAccountAddress(endpoint), Currency: "USD", Issuer: testAccountAddress(endpoint)}}
				}
				ledger := newMockLedger()
				addAccount(t, ledger, endpoint, 100_000_000, 0)
				cache := NewRippleLineCache(ledger)
				lines := make([]PathFindTrustLine, test.count)
				priorities := make(map[payment.Issue]int, test.count)
				for i := range test.count {
					peer := testAccountID(byte(i + 10))
					index := i
					if reverse {
						index = test.count - 1 - i
					}
					lines[index] = PathFindTrustLine{
						AccountID:     endpoint,
						AccountIDPeer: peer,
						Currency:      "USD",
						Balance:       state.NewIssuedAmountFromFloat64(1, "USD", testAccountAddress(peer)),
					}
					priority := 1
					if i == 0 {
						priority = 2
					}
					priorities[payment.Issue{Currency: "USD", Issuer: peer}] = priority
				}
				cache.lines[accountKey{Account: endpoint, Direction: LineDirectionOutgoing}] = lines
				pf := &Pathfinder{
					srcAccount:    source,
					dstAccount:    destination,
					effectiveDst:  destination,
					srcIssue:      payment.Issue{Currency: "USD", Issuer: source},
					dstAmount:     state.NewIssuedAmountFromFloat64(1, "JPY", testAccountAddress(destination)),
					ledger:        ledger,
					cache:         cache,
					pathsOutCount: priorities,
				}
				paths := pf.addLinks([][]payment.PathStep{parent}, afADD_ACCOUNTS)
				require.Len(t, paths, test.wantCount)
				for i, path := range paths {
					peer := byte(test.count + 10 - i)
					if i == 0 {
						peer = 10
					}
					require.Equal(t, testAccountAddress(testAccountID(peer)), path[len(path)-1].Account, "reverse=%v candidate=%d", reverse, i)
				}
			}
		})
	}
}
