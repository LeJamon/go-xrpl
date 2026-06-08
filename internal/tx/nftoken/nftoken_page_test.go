package nftoken

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/drops"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// mockView is an in-memory tx.LedgerView backed by a flat key→bytes map. It
// implements just enough of the interface (Read/Insert/Update/Erase/Succ) to
// exercise NFTokenPage split/merge logic without a full ledger.
type mockView struct {
	store map[[32]byte][]byte
}

func newMockView() *mockView {
	return &mockView{store: make(map[[32]byte][]byte)}
}

func (m *mockView) Read(k keylet.Keylet) ([]byte, error) {
	if d, ok := m.store[k.Key]; ok {
		return d, nil
	}
	return nil, nil
}

func (m *mockView) Exists(k keylet.Keylet) (bool, error) {
	_, ok := m.store[k.Key]
	return ok, nil
}

func (m *mockView) Insert(k keylet.Keylet, data []byte) error {
	m.store[k.Key] = append([]byte(nil), data...)
	return nil
}

func (m *mockView) Update(k keylet.Keylet, data []byte) error {
	m.store[k.Key] = append([]byte(nil), data...)
	return nil
}

func (m *mockView) Erase(k keylet.Keylet) error {
	delete(m.store, k.Key)
	return nil
}

func (m *mockView) AdjustDropsDestroyed(drops.XRPAmount) {}

func (m *mockView) ForEach(fn func(key [32]byte, data []byte) bool) error {
	for k, v := range m.store {
		if !fn(k, v) {
			break
		}
	}
	return nil
}

// Succ returns the first stored key strictly greater than the given key.
func (m *mockView) Succ(key [32]byte) ([32]byte, []byte, bool, error) {
	var best [32]byte
	found := false
	for k := range m.store {
		if bytes.Compare(k[:], key[:]) <= 0 {
			continue
		}
		if !found || bytes.Compare(k[:], best[:]) < 0 {
			best = k
			found = true
		}
	}
	if !found {
		return [32]byte{}, nil, false, nil
	}
	return best, m.store[best], true, nil
}

func (m *mockView) TxExists([32]byte) bool  { return false }
func (m *mockView) Rules() *amendment.Rules { return nil }
func (m *mockView) LedgerSeq() uint32       { return 0 }

func pageStats(t *testing.T, view *mockView) (pages, tokens, maxPerPage int) {
	t.Helper()
	_ = view.ForEach(func(_ [32]byte, data []byte) bool {
		page, err := state.ParseNFTokenPage(data)
		if err != nil {
			t.Fatalf("ParseNFTokenPage: %v", err)
		}
		pages++
		tokens += len(page.NFTokens)
		if len(page.NFTokens) > maxPerPage {
			maxPerPage = len(page.NFTokens)
		}
		return true
	})
	return
}

// TestNFTokenPageKeylets verifies the unhashed page-key layout
// [owner_20 | low_96_bits]:
//   - min  → [owner | 0x00...]
//   - max  → [owner | 0xFF...]
//   - token → [owner | tokenID[20:32]]
//
// Reference: rippled Indexes.cpp nftpage_min / nftpage_max / nftpage.
func TestNFTokenPageKeylets(t *testing.T) {
	owner := mustHexIssuer(t, "0102030405060708090A0B0C0D0E0F1011121314")

	min := keylet.NFTokenPageMin(owner)
	if !bytes.Equal(min.Key[:20], owner[:]) {
		t.Errorf("min key prefix = %X, want owner %X", min.Key[:20], owner)
	}
	for i := 20; i < 32; i++ {
		if min.Key[i] != 0x00 {
			t.Errorf("min key byte %d = %#02x, want 0x00", i, min.Key[i])
		}
	}

	max := keylet.NFTokenPageMax(owner)
	if !bytes.Equal(max.Key[:20], owner[:]) {
		t.Errorf("max key prefix = %X, want owner %X", max.Key[:20], owner)
	}
	for i := 20; i < 32; i++ {
		if max.Key[i] != 0xFF {
			t.Errorf("max key byte %d = %#02x, want 0xFF", i, max.Key[i])
		}
	}

	tokenID := mustHexID(t, "000B013AB5F762798A53D543A014CAF8B297CFF8F2F937E816E5DA9C00000001")
	forToken := keylet.NFTokenPageForToken(min, tokenID)
	if !bytes.Equal(forToken.Key[:20], owner[:]) {
		t.Errorf("token page key prefix = %X, want owner %X", forToken.Key[:20], owner)
	}
	if !bytes.Equal(forToken.Key[20:], tokenID[20:]) {
		t.Errorf("token page key low 96 = %X, want %X", forToken.Key[20:], tokenID[20:])
	}

	// All three keylets share the NFTokenPage type.
	if forToken.Type != min.Type || max.Type != min.Type {
		t.Errorf("keylet types disagree: min=%v max=%v token=%v", min.Type, max.Type, forToken.Type)
	}
}

// TestPageSplitAndMergeThresholds exercises the dirMaxTokensPerPage threshold:
// a page holds up to 32 tokens; the 33rd forces a split into two pages, and a
// subsequent removal that brings the combined size back to <= 32 merges them.
// Reference: rippled NFTokenUtils.cpp getPageForToken / removeToken.
func TestPageSplitAndMergeThresholds(t *testing.T) {
	view := newMockView()
	owner := mustHexIssuer(t, "B5F762798A53D543A014CAF8B297CFF8F2F937E8")

	ids := make([][32]byte, 0, dirMaxTokensPerPage+1)

	// Fill exactly one page. Distinct sequences give distinct low-96 bits, so
	// the split can always find a page boundary.
	for i := range dirMaxTokensPerPage {
		id := generateNFTokenID(owner, 0, uint32(i), nftFlagTransferable, 0)
		ids = append(ids, id)
		res := insertNFToken(owner, state.NFTokenData{NFTokenID: id}, view, true)
		if res.Result != tx.TesSUCCESS {
			t.Fatalf("insert %d: result %v", i, res.Result)
		}
	}

	if pages, tokens, _ := pageStats(t, view); pages != 1 || tokens != dirMaxTokensPerPage {
		t.Fatalf("after filling one page: pages=%d tokens=%d, want 1 page / %d tokens",
			pages, tokens, dirMaxTokensPerPage)
	}

	// The 33rd token overflows the page and triggers a split.
	overflow := generateNFTokenID(owner, 0, uint32(dirMaxTokensPerPage), nftFlagTransferable, 0)
	ids = append(ids, overflow)
	res := insertNFToken(owner, state.NFTokenData{NFTokenID: overflow}, view, true)
	if res.Result != tx.TesSUCCESS {
		t.Fatalf("overflow insert: result %v", res.Result)
	}
	if res.PagesCreated != 1 {
		t.Errorf("overflow insert PagesCreated = %d, want 1 (split)", res.PagesCreated)
	}

	pages, tokens, maxPer := pageStats(t, view)
	if pages != 2 {
		t.Errorf("after split: pages = %d, want 2", pages)
	}
	if tokens != dirMaxTokensPerPage+1 {
		t.Errorf("after split: tokens = %d, want %d", tokens, dirMaxTokensPerPage+1)
	}
	if maxPer > dirMaxTokensPerPage {
		t.Errorf("after split: a page holds %d tokens, exceeds max %d", maxPer, dirMaxTokensPerPage)
	}

	// Removing one token returns the total to 32; the two pages (combined size
	// <= dirMaxTokensPerPage) must merge back into one.
	result, removed := removeToken(view, owner, ids[0], true)
	if result != tx.TesSUCCESS {
		t.Fatalf("removeToken: result %v", result)
	}
	if removed != 1 {
		t.Errorf("removeToken pagesRemoved = %d, want 1 (merge)", removed)
	}

	pages, tokens, _ = pageStats(t, view)
	if pages != 1 {
		t.Errorf("after merge: pages = %d, want 1", pages)
	}
	if tokens != dirMaxTokensPerPage {
		t.Errorf("after merge: tokens = %d, want %d", tokens, dirMaxTokensPerPage)
	}
}
