package invariants

import (
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/ledger/entry"
)

func nftInvariantPageKey(account, low byte) [32]byte {
	var key [32]byte
	for i := range 20 {
		key[i] = account
	}
	key[31] = low
	return key
}

func nftInvariantFinalPageKey(account byte) [32]byte {
	key := nftInvariantPageKey(account, 0xff)
	for i := 20; i < len(key); i++ {
		key[i] = 0xff
	}
	return key
}

func nftInvariantPageData(
	t testing.TB,
	ids [][32]byte,
	uriHex string,
	includeURI bool,
	previous, next *[32]byte,
) []byte {
	t.Helper()
	page := &entry.NFTokenPage{}
	page.SetFlagsValue(0)
	page.SetPreviousTxnIDValue([32]byte{})
	page.SetPreviousTxnLgrSeqValue(0)
	if previous != nil {
		page.SetPreviousPageMinValue(*previous)
	}
	if next != nil {
		page.SetNextPageMinValue(*next)
	}
	tokens := make([]entry.NFTokenValue, len(ids))
	for i, id := range ids {
		tokens[i].SetNFTokenID(id)
		if includeURI {
			if err := tokens[i].SetURIHex(uriHex); err != nil {
				t.Fatalf("SetURIHex: %v", err)
			}
		}
	}
	if err := page.SetNFTokensValue(tokens); err != nil {
		t.Fatalf("SetNFTokensValue: %v", err)
	}
	data, err := page.Encode()
	if err != nil {
		t.Fatalf("Encode NFTokenPage: %v", err)
	}
	return data
}

func nftInvariantEntry(key [32]byte, before, after []byte, deleted bool) InvariantEntry {
	return InvariantEntry{
		Key:       key,
		EntryType: entry.TypeNFTokenPage,
		Before:    before,
		After:     after,
		IsDelete:  deleted,
	}
}

func nftInvariantAssertMessage(t *testing.T, got *InvariantViolation, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("expected ValidNFTokenPage violation %q, got nil", want)
	}
	if got.Name != "ValidNFTokenPage" {
		t.Fatalf("violation name = %q, want ValidNFTokenPage", got.Name)
	}
	if got.Message != want {
		t.Fatalf("violation message = %q, want %q", got.Message, want)
	}
}

func nftInvariantAssertParseViolation(t *testing.T, got *InvariantViolation) {
	t.Helper()
	if got == nil {
		t.Fatal("expected malformed NFTokenPage violation, got nil")
	}
	if got.Name != "ValidNFTokenPage" {
		t.Fatalf("violation name = %q, want ValidNFTokenPage", got.Name)
	}
	if !strings.HasPrefix(got.Message, "could not parse NFTokenPage SLE: ") {
		t.Fatalf("violation message = %q, want parse-error prefix", got.Message)
	}
}

func TestValidNFTokenPage_FinalPageDeletionAmendmentGate(t *testing.T) {
	key := nftInvariantFinalPageKey(0xaa)
	previous := nftInvariantPageKey(0xaa, 0x10)
	before := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, &previous, nil)
	entry := nftInvariantEntry(key, before, nil, true)

	cases := []struct {
		name  string
		rules *amendment.Rules
		want  string
	}{
		{
			name:  "enabled",
			rules: amendment.NewRules([][32]byte{amendment.FeatureFixNFTokenPageLinks}),
			want:  "Last NFT page deleted with non-empty directory",
		},
		{name: "disabled", rules: amendment.EmptyRules()},
		{name: "nil", rules: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkValidNFTokenPage([]InvariantEntry{entry}, stubView{}, tc.rules)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected violation: %v", got)
				}
				return
			}
			nftInvariantAssertMessage(t, got, tc.want)
		})
	}
}

func TestValidNFTokenPage_LostNextPageMinAmendmentGate(t *testing.T) {
	key := nftInvariantPageKey(0xaa, 0x10)
	next := nftInvariantPageKey(0xaa, 0x20)
	before := nftInvariantPageData(t, [][32]byte{nftToken(0x05)}, "ABCD", true, nil, &next)
	after := nftInvariantPageData(t, [][32]byte{nftToken(0x05)}, "ABCD", true, nil, nil)
	entry := nftInvariantEntry(key, before, after, false)

	cases := []struct {
		name  string
		rules *amendment.Rules
		want  string
	}{
		{
			name:  "enabled",
			rules: amendment.NewRules([][32]byte{amendment.FeatureFixNFTokenPageLinks}),
			want:  "Lost NextMinPage link",
		},
		{name: "disabled", rules: amendment.EmptyRules()},
		{name: "nil", rules: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkValidNFTokenPage([]InvariantEntry{entry}, stubView{}, tc.rules)
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected violation: %v", got)
				}
				return
			}
			nftInvariantAssertMessage(t, got, tc.want)
		})
	}
}

func TestValidNFTokenPage_LinkOrderingAndAccountBounds(t *testing.T) {
	pageKey := nftInvariantPageKey(0xaa, 0x40)
	validPrevious := nftInvariantPageKey(0xaa, 0x10)
	validNext := nftInvariantPageKey(0xaa, 0x80)
	otherAccountPrevious := nftInvariantPageKey(0xbb, 0x10)
	otherAccountNext := nftInvariantPageKey(0xbb, 0x80)
	equalPrevious := nftInvariantPageKey(0xaa, 0x40)
	laterPrevious := nftInvariantPageKey(0xaa, 0x50)
	equalNext := nftInvariantPageKey(0xaa, 0x40)
	earlierNext := nftInvariantPageKey(0xaa, 0x30)

	cases := []struct {
		name     string
		previous *[32]byte
		next     *[32]byte
	}{
		{name: "previous account", previous: &otherAccountPrevious, next: &validNext},
		{name: "previous equal page", previous: &equalPrevious, next: &validNext},
		{name: "previous later page", previous: &laterPrevious, next: &validNext},
		{name: "next account", previous: &validPrevious, next: &otherAccountNext},
		{name: "next equal page", previous: &validPrevious, next: &equalNext},
		{name: "next earlier page", previous: &validPrevious, next: &earlierNext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, tc.previous, tc.next)
			got := checkValidNFTokenPage([]InvariantEntry{nftInvariantEntry(pageKey, nil, page, false)}, stubView{}, amendment.EmptyRules())
			nftInvariantAssertMessage(t, got, "NFT page is improperly linked")
		})
	}
}

func TestValidNFTokenPage_TokenSortingAndPageSize(t *testing.T) {
	pageKey := nftInvariantFinalPageKey(0xaa)
	ids := make([][32]byte, 33)
	for i := range ids {
		ids[i] = nftToken(byte(i + 1))
	}

	cases := []struct {
		name    string
		ids     [][32]byte
		deleted bool
		want    string
	}{
		{name: "sorted", ids: [][32]byte{nftToken(0x10), nftToken(0x20)}},
		{name: "unsorted", ids: [][32]byte{nftToken(0x20), nftToken(0x10)}, want: "NFTs on page are not sorted"},
		{name: "empty active page", ids: nil, want: "NFT page has invalid size"},
		{name: "full page", ids: ids[:32]},
		{name: "overfull page", ids: ids, want: "NFT page has invalid size"},
		{name: "empty deleted page", ids: nil, deleted: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := nftInvariantPageData(t, tc.ids, "ABCD", true, nil, nil)
			var before, after []byte
			if tc.deleted {
				before = page
			} else {
				after = page
			}
			got := checkValidNFTokenPage([]InvariantEntry{nftInvariantEntry(pageKey, before, after, tc.deleted)}, stubView{}, amendment.EmptyRules())
			if tc.want == "" {
				if got != nil {
					t.Fatalf("unexpected violation: %v", got)
				}
				return
			}
			nftInvariantAssertMessage(t, got, tc.want)
		})
	}
}

func TestValidNFTokenPage_LinkAndTokenBoundsPrecedence(t *testing.T) {
	pageKey := nftInvariantPageKey(0xaa, 0x40)
	badLink := nftInvariantPageKey(0xbb, 0x10)
	page := nftInvariantPageData(t, [][32]byte{nftToken(0x50), nftToken(0x10)}, "", true, &badLink, nil)
	got := checkValidNFTokenPage([]InvariantEntry{nftInvariantEntry(pageKey, nil, page, false)}, stubView{}, amendment.EmptyRules())
	nftInvariantAssertMessage(t, got, "NFT page is improperly linked")
}

func TestValidNFTokenPage_ExplicitEmptyURI(t *testing.T) {
	key := nftInvariantPageKey(0xaa, 0x40)
	valid := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "", false, nil, nil)
	cases := []struct {
		name       string
		uriHex     string
		includeURI bool
		want       string
	}{
		{name: "absent URI", want: ""},
		{name: "explicit empty URI", includeURI: true, want: "NFT contains empty URI"},
		{name: "zero byte URI", uriHex: "00", includeURI: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, tc.uriHex, tc.includeURI, nil, nil)
			for _, side := range []string{"before", "after"} {
				t.Run(side, func(t *testing.T) {
					before, after := valid, page
					if side == "before" {
						before, after = page, valid
					}
					got := checkValidNFTokenPage([]InvariantEntry{nftInvariantEntry(key, before, after, false)}, stubView{}, amendment.EmptyRules())
					if tc.want == "" {
						if got != nil {
							t.Fatalf("unexpected violation: %v", got)
						}
						return
					}
					nftInvariantAssertMessage(t, got, tc.want)
				})
			}
		})
	}
}

func TestValidNFTokenPage_MalformedBeforeAndAfter(t *testing.T) {
	key := nftInvariantFinalPageKey(0xaa)
	valid := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, nil, nil)
	malformed := []byte{0x00}
	cases := []struct {
		name  string
		entry InvariantEntry
	}{
		{
			name:  "malformed before",
			entry: nftInvariantEntry(key, malformed, valid, false),
		},
		{
			name:  "malformed after",
			entry: nftInvariantEntry(key, valid, malformed, false),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkValidNFTokenPage([]InvariantEntry{tc.entry}, stubView{}, amendment.AllSupportedRules())
			nftInvariantAssertParseViolation(t, got)
		})
	}
}

func TestValidNFTokenPage_ViolationPrecedence(t *testing.T) {
	key := nftInvariantPageKey(0xaa, 0x40)
	finalKey := nftInvariantFinalPageKey(0xaa)
	validPrevious := nftInvariantPageKey(0xaa, 0x10)
	validNext := nftInvariantPageKey(0xaa, 0x80)
	badAccountLink := nftInvariantPageKey(0xbb, 0x10)

	validPage := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, nil, nil)
	badLinkPage := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, &badAccountLink, &validNext)
	badEntryPage := nftInvariantPageData(t, [][32]byte{nftToken(0x50), nftToken(0x10)}, "ABCD", true, nil, nil)
	badSortPage := nftInvariantPageData(t, [][32]byte{nftToken(0x20), nftToken(0x10)}, "", true, nil, nil)
	invalidSizePage := nftInvariantPageData(t, nil, "", false, nil, nil)
	finalDeletePage := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, &validPrevious, nil)
	lostLinkBefore := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, nil, &validNext)
	lostLinkAfter := validPage
	overfullIDs := make([][32]byte, 33)
	for i := range overfullIDs {
		overfullIDs[i] = nftToken(byte(i + 1))
	}
	badURIAndSizePage := nftInvariantPageData(t, overfullIDs, "", true, nil, nil)

	cases := []struct {
		name    string
		entries []InvariantEntry
		want    string
	}{
		{
			name: "parse errors are immediate",
			entries: []InvariantEntry{
				nftInvariantEntry(key, nil, badLinkPage, false),
				nftInvariantEntry(key, []byte{0x00}, validPage, false),
			},
			want: "parse",
		},
		{
			name:    "badLink before badEntry",
			entries: []InvariantEntry{nftInvariantEntry(key, nil, nftInvariantPageData(t, [][32]byte{nftToken(0x50)}, "", true, &badAccountLink, nil), false)},
			want:    "NFT page is improperly linked",
		},
		{
			name:    "badEntry before badSort",
			entries: []InvariantEntry{nftInvariantEntry(key, nil, badEntryPage, false)},
			want:    "NFT found in incorrect page",
		},
		{
			name:    "badSort before badURI",
			entries: []InvariantEntry{nftInvariantEntry(finalKey, nil, badSortPage, false)},
			want:    "NFTs on page are not sorted",
		},
		{
			name:    "badURI before invalidSize",
			entries: []InvariantEntry{nftInvariantEntry(finalKey, nil, badURIAndSizePage, false)},
			want:    "NFT contains empty URI",
		},
		{
			name: "invalidSize before deleted page checks",
			entries: []InvariantEntry{
				nftInvariantEntry(key, nil, invalidSizePage, false),
				nftInvariantEntry(finalKey, finalDeletePage, nil, true),
			},
			want: "NFT page has invalid size",
		},
		{
			name: "deletedFinalPage before deletedLink",
			entries: []InvariantEntry{
				nftInvariantEntry(finalKey, finalDeletePage, nil, true),
				nftInvariantEntry(key, lostLinkBefore, lostLinkAfter, false),
			},
			want: "Last NFT page deleted with non-empty directory",
		},
		{
			name:    "deletedLink",
			entries: []InvariantEntry{nftInvariantEntry(key, lostLinkBefore, lostLinkAfter, false)},
			want:    "Lost NextMinPage link",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := checkValidNFTokenPage(tc.entries, stubView{}, amendment.NewRules([][32]byte{amendment.FeatureFixNFTokenPageLinks}))
			if tc.want == "parse" {
				nftInvariantAssertParseViolation(t, got)
				return
			}
			nftInvariantAssertMessage(t, got, tc.want)
		})
	}
}

func TestValidNFTokenPage_MultipleEntries(t *testing.T) {
	firstKey := nftInvariantPageKey(0xaa, 0x10)
	firstNext := nftInvariantPageKey(0xaa, 0x20)
	secondKey := nftInvariantPageKey(0xaa, 0x40)
	firstBefore := nftInvariantPageData(t, [][32]byte{nftToken(0x05)}, "ABCD", true, nil, &firstNext)
	firstAfter := nftInvariantPageData(t, [][32]byte{nftToken(0x05)}, "ABCD", true, nil, &firstNext)
	firstAfterLost := nftInvariantPageData(t, [][32]byte{nftToken(0x05)}, "ABCD", true, nil, nil)
	second := nftInvariantPageData(t, [][32]byte{nftToken(0x20)}, "ABCD", true, nil, nil)
	validEntries := []InvariantEntry{
		nftInvariantEntry(firstKey, firstBefore, firstAfter, false),
		nftInvariantEntry(secondKey, nil, second, false),
	}
	if got := checkValidNFTokenPage(validEntries, stubView{}, amendment.NewRules([][32]byte{amendment.FeatureFixNFTokenPageLinks})); got != nil {
		t.Fatalf("valid multiple-entry scope: unexpected violation: %v", got)
	}

	lostEntries := []InvariantEntry{
		nftInvariantEntry(firstKey, firstBefore, firstAfterLost, false),
		nftInvariantEntry(secondKey, nil, second, false),
	}
	got := checkValidNFTokenPage(lostEntries, stubView{}, amendment.NewRules([][32]byte{amendment.FeatureFixNFTokenPageLinks}))
	nftInvariantAssertMessage(t, got, "Lost NextMinPage link")
}
