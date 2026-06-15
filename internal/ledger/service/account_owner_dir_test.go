package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// assertDirMarker checks that a non-empty account_lines/offers/channels marker is
// "<64-hex entryKey>,<decimal ownerNode>" — the rippled forEachItemAfter shape.
func assertDirMarker(t *testing.T, marker string) {
	t.Helper()
	keyStr, pageStr, found := strings.Cut(marker, ",")
	if !found {
		t.Fatalf("marker %q is missing the ',' separator", marker)
	}
	if len(keyStr) != 64 {
		t.Fatalf("marker key half %q is not 64 hex chars", keyStr)
	}
	if _, err := hex.DecodeString(keyStr); err != nil {
		t.Fatalf("marker key half %q is not hex: %v", keyStr, err)
	}
	if _, err := strconv.ParseUint(pageStr, 10, 64); err != nil {
		t.Fatalf("marker page half %q is not a uint64: %v", pageStr, err)
	}
}

// ownerDirOrder returns the keys in an account's owner directory in walk order.
func ownerDirOrder(t *testing.T, svc *Service, id [20]byte) [][32]byte {
	t.Helper()
	var keys [][32]byte
	if err := state.DirForEach(svc.openLedger, keylet.OwnerDir(id), func(k [32]byte) error {
		keys = append(keys, k)
		return nil
	}); err != nil {
		t.Fatalf("walk owner dir: %v", err)
	}
	return keys
}

// TestAccountOffers_ExcludesIssuerOnlyOffer pins the headline correctness
// property of #938: an offer owned by B that merely *mentions* A (as the issuer
// of the IOU it sells) must not appear in A's results. Walking A's owner
// directory never reaches B's offer, where the old whole-ledger byte-scan did.
func TestAccountOffers_ExcludesIssuerOnlyOffer(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, issuerID := addressFromBytes(t, 0x10) // A, the IOU issuer
	ownerAddr, _ := addressFromBytes(t, 0x20)         // B, the offer owner
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	// B offers to buy A's USD with XRP — A's account ID is embedded as issuer.
	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	// A owns nothing.
	aOffers, err := svc.GetAccountOffers(context.Background(), issuerAddr, "current", 0, "")
	if err != nil {
		t.Fatalf("GetAccountOffers(A): %v", err)
	}
	if len(aOffers.Offers) != 0 {
		t.Fatalf("A must not see B's offer (issuer-only mention), got %d", len(aOffers.Offers))
	}

	// account_objects for A must also be empty (no owner-dir entries).
	aObjects, err := svc.GetAccountObjects(context.Background(), issuerAddr, "current", "", 0, "")
	if err != nil {
		t.Fatalf("GetAccountObjects(A): %v", err)
	}
	if len(aObjects.AccountObjects) != 0 {
		t.Fatalf("A's account_objects must be empty, got %d", len(aObjects.AccountObjects))
	}

	// B genuinely owns the offer.
	bOffers, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 0, "")
	if err != nil {
		t.Fatalf("GetAccountOffers(B): %v", err)
	}
	if len(bOffers.Offers) != 1 {
		t.Fatalf("B must own exactly 1 offer, got %d", len(bOffers.Offers))
	}
	_ = issuerID
}

// TestAccountChannels_ExcludesDestinationChannel: a payment channel lives in both
// the source and destination owner directories, but account_channels reports only
// channels the account is the *source* of. Querying the destination must return
// nothing even though the channel sits in its owner directory.
func TestAccountChannels_ExcludesDestinationChannel(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, _ := addressFromBytes(t, 0x10)
	dstAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, dstAddr, 1_000_000_000_000, 0)

	insertPayChannelEntry(t, svc, srcAddr, dstAddr, 1, nil)

	src, err := svc.GetAccountChannels(context.Background(), srcAddr, "", "current", 0, "")
	if err != nil {
		t.Fatalf("GetAccountChannels(src): %v", err)
	}
	if len(src.Channels) != 1 {
		t.Fatalf("source must report 1 channel, got %d", len(src.Channels))
	}

	dst, err := svc.GetAccountChannels(context.Background(), dstAddr, "", "current", 0, "")
	if err != nil {
		t.Fatalf("GetAccountChannels(dst): %v", err)
	}
	if len(dst.Channels) != 0 {
		t.Fatalf("destination must report 0 channels (not source-owned), got %d", len(dst.Channels))
	}
}

// TestAccountLines_OrderMatchesOwnerDir verifies the lines come back in
// owner-directory order, not SHAMap-key order.
func TestAccountLines_OrderMatchesOwnerDir(t *testing.T) {
	svc := newOfferTestService(t)
	aAddr, aID := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	cAddr, _ := addressFromBytes(t, 0x50)
	dAddr, _ := addressFromBytes(t, 0x60)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000_000, 0)
	insertLineRaw(t, svc, aAddr, bAddr, "USD", "0", "100", "100", 0)
	insertLineRaw(t, svc, aAddr, cAddr, "EUR", "0", "100", "100", 0)
	insertLineRaw(t, svc, aAddr, dAddr, "GBP", "0", "100", "100", 0)

	res, err := svc.GetAccountLines(context.Background(), aAddr, "current", "", 0, "")
	if err != nil {
		t.Fatalf("GetAccountLines: %v", err)
	}
	if len(res.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(res.Lines))
	}

	// Expected order: map each owner-dir key to its line currency.
	wantOrder := make([]string, 0, 3)
	for _, k := range ownerDirOrder(t, svc, aID) {
		data, rerr := svc.openLedger.Read(keylet.Keylet{Key: k})
		if rerr != nil || data == nil {
			t.Fatalf("read owner-dir entry: %v", rerr)
		}
		rs, perr := state.ParseRippleState(data)
		if perr != nil {
			t.Fatalf("parse ripple state: %v", perr)
		}
		wantOrder = append(wantOrder, rs.Balance.Currency)
	}
	for i, ln := range res.Lines {
		if ln.Currency != wantOrder[i] {
			t.Fatalf("line %d currency = %s, want %s (owner-dir order %v)", i, ln.Currency, wantOrder[i], wantOrder)
		}
	}
}

// TestAccountLines_MarkerPagination walks every line one page at a time and
// asserts the full set is returned exactly once, the marker is rippled-shaped,
// and the final page carries no marker.
func TestAccountLines_MarkerPagination(t *testing.T) {
	svc := newOfferTestService(t)
	aAddr, _ := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	cAddr, _ := addressFromBytes(t, 0x50)
	dAddr, _ := addressFromBytes(t, 0x60)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000_000, 0)
	insertLineRaw(t, svc, aAddr, bAddr, "USD", "0", "100", "100", 0)
	insertLineRaw(t, svc, aAddr, cAddr, "EUR", "0", "100", "100", 0)
	insertLineRaw(t, svc, aAddr, dAddr, "GBP", "0", "100", "100", 0)

	seen := map[string]bool{}
	marker := ""
	for page := range 10 {
		res, err := svc.GetAccountLines(context.Background(), aAddr, "current", "", 1, marker)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(res.Lines) != 1 {
			t.Fatalf("page %d: expected 1 line, got %d", page, len(res.Lines))
		}
		peer := res.Lines[0].Account
		if seen[peer] {
			t.Fatalf("page %d: duplicate line for %s", page, peer)
		}
		seen[peer] = true

		if res.Marker == "" {
			break
		}
		assertDirMarker(t, res.Marker)
		marker = res.Marker
	}
	if len(seen) != 3 {
		t.Fatalf("pagination returned %d distinct lines, want 3", len(seen))
	}
}

// TestAccountOffers_MarkerPagination mirrors the lines pagination walk for offers.
func TestAccountOffers_MarkerPagination(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	for seq := uint32(1); seq <= 3; seq++ {
		insertOffer(t, svc, ownerAddr, seq,
			state.NewIssuedAmountFromFloat64(float64(100*seq), "USD", issuerAddr),
			tx.NewXRPAmount(10_000_000),
		)
	}

	seen := map[uint32]bool{}
	marker := ""
	for page := range 10 {
		res, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 1, marker)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(res.Offers) != 1 {
			t.Fatalf("page %d: expected 1 offer, got %d", page, len(res.Offers))
		}
		if seen[res.Offers[0].Seq] {
			t.Fatalf("page %d: duplicate offer seq %d", page, res.Offers[0].Seq)
		}
		seen[res.Offers[0].Seq] = true
		if res.Marker == "" {
			break
		}
		assertDirMarker(t, res.Marker)
		marker = res.Marker
	}
	if len(seen) != 3 {
		t.Fatalf("pagination returned %d distinct offers, want 3", len(seen))
	}
}

// TestAccountChannels_MarkerPagination mirrors the pagination walk for channels.
func TestAccountChannels_MarkerPagination(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000_000, 0)
	dsts := []byte{0x40, 0x50, 0x60}
	for i, seed := range dsts {
		dstAddr, _ := addressFromBytes(t, seed)
		insertPayChannelEntry(t, svc, srcAddr, dstAddr, uint32(i+1), nil)
	}

	seen := map[string]bool{}
	marker := ""
	for page := range 10 {
		res, err := svc.GetAccountChannels(context.Background(), srcAddr, "", "current", 1, marker)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(res.Channels) != 1 {
			t.Fatalf("page %d: expected 1 channel, got %d", page, len(res.Channels))
		}
		id := res.Channels[0].ChannelID
		if seen[id] {
			t.Fatalf("page %d: duplicate channel %s", page, id)
		}
		seen[id] = true
		if res.Marker == "" {
			break
		}
		assertDirMarker(t, res.Marker)
		marker = res.Marker
	}
	if len(seen) != 3 {
		t.Fatalf("pagination returned %d distinct channels, want 3", len(seen))
	}
}

// TestAccountDirMarkers_Invalid covers the malformed and stale marker tiers for
// the owner-directory RPCs: both must surface as ErrInvalidMarker.
func TestAccountDirMarkers_Invalid(t *testing.T) {
	svc := newOfferTestService(t)
	aAddr, _ := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000_000, 0)
	insertLineRaw(t, svc, aAddr, bAddr, "USD", "0", "100", "100", 0)

	cases := []struct {
		name   string
		marker string
	}{
		{"no separator", "deadbeef"},
		{"bad hex key", strings.Repeat("z", 64) + ",0"},
		{"short key", "ABCD,0"},
		{"bad page", strings.Repeat("AB", 32) + ",notanumber"},
		{"key not in dir", strings.Repeat("AB", 32) + ",0"},
		{"zero-shorthand key", "0,0"},
	}
	for _, tc := range cases {
		t.Run("lines/"+tc.name, func(t *testing.T) {
			_, err := svc.GetAccountLines(context.Background(), aAddr, "current", "", 0, tc.marker)
			if !errors.Is(err, svcerr.ErrInvalidMarker) {
				t.Fatalf("marker %q: want ErrInvalidMarker, got %v", tc.marker, err)
			}
		})
	}
}

// ownerDirSpansMultiplePages reports whether id's owner directory chains to more
// than one page (a non-zero IndexNext on the root), so a test can assert it
// genuinely exercises the cross-page walk rather than the single-page fast path.
func ownerDirSpansMultiplePages(t *testing.T, svc *Service, id [20]byte) bool {
	t.Helper()
	root := keylet.OwnerDir(id).Key
	data, err := svc.openLedger.Read(keylet.Keylet{Key: root})
	if err != nil || data == nil {
		t.Fatalf("read owner dir root: %v", err)
	}
	node, err := state.ParseDirectoryNode(data)
	if err != nil {
		t.Fatalf("parse owner dir root: %v", err)
	}
	return node.IndexNext != 0
}

// TestAccountOffers_MarkerPaginationAcrossDirPages walks more offers than fit on
// a single owner-directory page (which holds 32 entries) and asserts the full
// set comes back exactly once with at least one marker landing on a non-zero
// owner-node page. That exercises both the IndexNext page traversal and the
// hintPage jump in ownerDirAfter, not just the single-page path the smaller
// pagination tests cover.
func TestAccountOffers_MarkerPaginationAcrossDirPages(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	ownerAddr, ownerID := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	const total = 50
	for seq := uint32(1); seq <= total; seq++ {
		insertOffer(t, svc, ownerAddr, seq,
			state.NewIssuedAmountFromFloat64(float64(seq), "USD", issuerAddr),
			tx.NewXRPAmount(10_000_000),
		)
	}
	if !ownerDirSpansMultiplePages(t, svc, ownerID) {
		t.Fatalf("owner directory must span >1 page for %d offers", total)
	}

	seen := map[uint32]bool{}
	crossedPage := false
	marker := ""
	for page := range total + 5 {
		res, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 20, marker)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		for _, o := range res.Offers {
			if seen[o.Seq] {
				t.Fatalf("page %d: duplicate offer seq %d", page, o.Seq)
			}
			seen[o.Seq] = true
		}
		if res.Marker == "" {
			break
		}
		assertDirMarker(t, res.Marker)
		if _, pageStr, _ := strings.Cut(res.Marker, ","); pageStr != "0" {
			crossedPage = true
		}
		marker = res.Marker
	}
	if len(seen) != total {
		t.Fatalf("pagination returned %d distinct offers, want %d", len(seen), total)
	}
	if !crossedPage {
		t.Fatal("expected a marker on a non-zero owner-dir page (cross-page resume)")
	}
}

// TestAccountChannels_ShortFilteredPageKeepsMarker pins the per-entry limit
// charging #938 introduced: ownerDirAfter spends the limit on every directory
// entry visited, not on every entry kept. With limit=1 and a destination filter
// that excludes the first owner-dir entry, the first page comes back empty yet
// still carries a marker (the budget was spent on the filtered entry), and the
// next page returns the matching channel. Mirrors rippled forEachItemAfter,
// whose lambda counts every visited entry regardless of the filter.
func TestAccountChannels_ShortFilteredPageKeepsMarker(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, srcID := addressFromBytes(t, 0x10)
	dAddr, _ := addressFromBytes(t, 0x40)
	eAddr, _ := addressFromBytes(t, 0x50)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000_000, 0)

	k1 := insertPayChannelEntry(t, svc, srcAddr, dAddr, 1, nil)
	k2 := insertPayChannelEntry(t, svc, srcAddr, eAddr, 2, nil)
	keyToDst := map[[32]byte]string{k1: dAddr, k2: eAddr}

	order := ownerDirOrder(t, svc, srcID)
	if len(order) != 2 {
		t.Fatalf("want 2 owner-dir entries, got %d", len(order))
	}
	// Filter on the destination of the second owner-dir entry, so the first entry
	// visited is filtered out and the page must come back short.
	target := keyToDst[order[1]]

	page1, err := svc.GetAccountChannels(context.Background(), srcAddr, target, "current", 1, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1.Channels) != 0 {
		t.Fatalf("page1 must be empty (first entry filtered out), got %d", len(page1.Channels))
	}
	if page1.Marker == "" {
		t.Fatal("page1 must carry a marker: the limit is charged per entry visited, not per kept")
	}
	assertDirMarker(t, page1.Marker)

	page2, err := svc.GetAccountChannels(context.Background(), srcAddr, target, "current", 1, page1.Marker)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2.Channels) != 1 {
		t.Fatalf("page2 must return the matching channel, got %d", len(page2.Channels))
	}
	if page2.Channels[0].DestinationAccount != target {
		t.Fatalf("page2 destination = %s, want %s", page2.Channels[0].DestinationAccount, target)
	}
	if page2.Marker != "" {
		t.Fatalf("page2 must be the final page (no marker), got %q", page2.Marker)
	}
}

// TestAccountOffers_MarkerIgnoresTrailingSegment pins that a marker with an
// extra trailing ",<junk>" segment resumes exactly as the clean marker would.
// rippled reads the hint as the second comma-delimited field and ignores the
// rest (std::getline), so the marker parser must do the same.
func TestAccountOffers_MarkerIgnoresTrailingSegment(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	for seq := uint32(1); seq <= 2; seq++ {
		insertOffer(t, svc, ownerAddr, seq,
			state.NewIssuedAmountFromFloat64(float64(seq), "USD", issuerAddr),
			tx.NewXRPAmount(10_000_000),
		)
	}

	page1, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 1, "")
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if page1.Marker == "" {
		t.Fatal("page1 must carry a marker")
	}

	clean, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 1, page1.Marker)
	if err != nil {
		t.Fatalf("clean resume: %v", err)
	}
	dirty, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 1, page1.Marker+",999")
	if err != nil {
		t.Fatalf("dirty resume (trailing segment must be ignored): %v", err)
	}
	if len(clean.Offers) != 1 || len(dirty.Offers) != 1 {
		t.Fatalf("expected 1 offer per resumed page, got clean=%d dirty=%d", len(clean.Offers), len(dirty.Offers))
	}
	if dirty.Offers[0].Seq != clean.Offers[0].Seq {
		t.Fatalf("dirty marker resumed to seq %d, clean to %d", dirty.Offers[0].Seq, clean.Offers[0].Seq)
	}
}

// TestAccountOffers_PhantomDirEntryDoesNotConsumeLimit pins that a directory
// entry whose backing object is missing is skipped without charging the limit
// (rippled's null-SLE traversal). With one phantom entry and one real offer at
// limit=1, the real offer comes back on a single page with no marker — the
// phantom neither consumes the slot nor becomes a marker.
func TestAccountOffers_PhantomDirEntryDoesNotConsumeLimit(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	ownerAddr, ownerID := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	// Link a key with no backing ledger object into the owner directory.
	var phantom [32]byte
	for i := range phantom {
		phantom[i] = 0xEE
	}
	if _, derr := state.DirInsert(svc.openLedger, keylet.OwnerDir(ownerID), phantom, false, nil); derr != nil {
		t.Fatalf("owner dir insert (phantom): %v", derr)
	}

	res, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 1, "")
	if err != nil {
		t.Fatalf("GetAccountOffers: %v", err)
	}
	if len(res.Offers) != 1 {
		t.Fatalf("expected 1 real offer (phantom skipped), got %d", len(res.Offers))
	}
	if res.Marker != "" {
		t.Fatalf("phantom must not produce a marker, got %q", res.Marker)
	}
}
