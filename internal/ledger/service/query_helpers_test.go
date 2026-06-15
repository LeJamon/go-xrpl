package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/ledger"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

func TestNormalizeObjectType(t *testing.T) {
	cases := map[string]string{
		"account":             "AccountRoot",
		"amendments":          "Amendments",
		"amm":                 "AMM",
		"bridge":              "Bridge",
		"check":               "Check",
		"credential":          "Credential",
		"delegate":            "Delegate",
		"deposit_preauth":     "DepositPreauth",
		"did":                 "DID",
		"directory":           "DirectoryNode",
		"escrow":              "Escrow",
		"fee":                 "FeeSettings",
		"hashes":              "LedgerHashes",
		"mptoken":             "MPToken",
		"mpt_issuance":        "MPTokenIssuance",
		"nft_offer":           "NFTokenOffer",
		"nft_page":            "NFTokenPage",
		"nunl":                "NegativeUNL",
		"offer":               "Offer",
		"oracle":              "Oracle",
		"payment_channel":     "PayChannel",
		"permissioned_domain": "PermissionedDomain",
		"state":               "RippleState",
		"signer_list":         "SignerList",
		"ticket":              "Ticket",
		"vault":               "Vault",
		"":                    "", // passthrough default
		"AlreadyPascal":       "AlreadyPascal",
	}
	for in, want := range cases {
		if got := normalizeObjectType(in); got != want {
			t.Errorf("normalizeObjectType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGetLedgerEntryType(t *testing.T) {
	codes := map[uint16]string{
		55: "NFTokenOffer", 67: "Check", 73: "DID", 78: "NegativeUNL",
		80: "NFTokenPage", 83: "SignerList", 84: "Ticket", 97: "AccountRoot",
		100: "DirectoryNode", 102: "Amendments", 104: "LedgerHashes", 105: "Bridge",
		111: "Offer", 112: "DepositPreauth", 113: "XChainOwnedClaimID", 114: "RippleState",
		115: "FeeSettings", 117: "Escrow", 120: "PayChannel", 121: "AMM",
		126: "MPTokenIssuance", 127: "MPToken", 128: "Oracle", 129: "Credential",
		130: "PermissionedDomain", 131: "Delegate", 132: "Vault",
	}
	for code, want := range codes {
		data := []byte{0x11, byte(code >> 8), byte(code & 0xff)}
		if got := state.EntryType(data); got != want {
			t.Errorf("state.EntryType(code=%d) = %q, want %q", code, got, want)
		}
	}

	// No 0x11 header → empty type.
	if got := state.EntryType([]byte{0x11}); got != "" {
		t.Errorf("short data must yield empty type, got %q", got)
	}
	if got := state.EntryType([]byte{0x22, 0x00, 0x61}); got != "" {
		t.Errorf("wrong field header must yield empty type, got %q", got)
	}
	// Present-but-unknown code is named "Unknown(...)" by the shared mapping.
	if got := state.EntryType([]byte{0x11, 0x00, 0x01}); got == "" || got[:7] != "Unknown" {
		t.Errorf("unknown type code must yield an Unknown(...) name, got %q", got)
	}
}

func TestFormatRangeAndHashHex(t *testing.T) {
	if r := formatRange(3, 9); r != "3-9" {
		t.Errorf("formatRange = %q, want 3-9", r)
	}
	var h [32]byte
	h[0] = 0xAB
	h[31] = 0xCD
	got := formatHashHex(h)
	if len(got) != 64 || got != strings.ToUpper(got) {
		t.Errorf("formatHashHex must be 64 upper-hex chars, got %q", got)
	}
	if got[:2] != "AB" || got[62:] != "CD" {
		t.Errorf("formatHashHex byte mapping wrong: %q", got)
	}
}

func TestDecodeAccountIDLocal(t *testing.T) {
	if _, err := decodeAccountIDLocal(""); err == nil {
		t.Errorf("empty address must error")
	}
	if _, err := decodeAccountIDLocal("not-valid"); err == nil {
		t.Errorf("invalid address must error")
	}
	addr, want := addressFromBytes(t, 0x33)
	got, err := decodeAccountIDLocal(addr)
	if err != nil || got != want {
		t.Fatalf("decodeAccountIDLocal(%s) = %x err=%v, want %x", addr, got, err, want)
	}
}

func TestGetLedgerEntry(t *testing.T) {
	svc := newOfferTestService(t)
	addr, idBytes := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, addr, 500_000_000, 0)

	accKey := keylet.Account(idBytes)
	res, err := svc.GetLedgerEntry(context.Background(), accKey.Key, "current")
	if err != nil {
		t.Fatalf("GetLedgerEntry: %v", err)
	}
	if len(res.Node) == 0 {
		t.Errorf("Node data must be populated")
	}
	if res.Index != formatHashHex(accKey.Key) {
		t.Errorf("index = %s, want %s", res.Index, formatHashHex(accKey.Key))
	}

	t.Run("not found", func(t *testing.T) {
		var missing [32]byte
		missing[0] = 0xFE
		_, err := svc.GetLedgerEntry(context.Background(), missing, "current")
		if !errors.Is(err, svcerr.ErrLedgerEntryNotFound) {
			t.Fatalf("want ErrLedgerEntryNotFound, got %v", err)
		}
	})
}

func TestGetLedgerData_HeaderAndPagination(t *testing.T) {
	svc := newOfferTestService(t)
	for i := byte(0x10); i <= 0x14; i++ {
		addr, _ := addressFromBytes(t, i)
		insertAccountRoot(t, svc, addr, 1_000_000_000, 0)
	}

	first, err := svc.GetLedgerData(context.Background(), "current", 1, "")
	if err != nil {
		t.Fatalf("GetLedgerData: %v", err)
	}
	if first.LedgerHeader == nil {
		t.Errorf("first page (no marker) must include the ledger header")
	}
	if len(first.State) != 1 {
		t.Fatalf("limit=1 must return 1 entry, got %d", len(first.State))
	}
	if first.Marker == "" {
		t.Fatalf("more entries remain → marker must be set")
	}

	second, err := svc.GetLedgerData(context.Background(), "current", 1, first.Marker)
	if err != nil {
		t.Fatalf("GetLedgerData page2: %v", err)
	}
	if second.LedgerHeader != nil {
		t.Errorf("marker page must omit the ledger header")
	}
	if len(second.State) == 0 {
		t.Errorf("second page must return entries")
	}
	if second.State[0].Index == first.State[0].Index {
		t.Errorf("pagination returned the marker entry again")
	}
}

// TestGetLedgerData_PageFullMarkerIsFirstUnemittedMinusOne pins the JSON
// ledger_data resume marker to rippled's value (`--k` in doLedgerData): the
// first un-emitted key minus one, NOT the last emitted key. Resume is
// strictly-greater than the marker, so both land on the same next page, but
// the wire bytes must match rippled for cross-client diffing.
func TestGetLedgerData_PageFullMarkerIsFirstUnemittedMinusOne(t *testing.T) {
	svc := newOfferTestService(t)
	for i := byte(0x10); i <= 0x16; i++ {
		addr, _ := addressFromBytes(t, i)
		insertAccountRoot(t, svc, addr, 1_000_000_000, 0)
	}

	// Establish the iteration order with a single full page.
	all, err := svc.GetLedgerData(context.Background(), "current", 256, "")
	if err != nil {
		t.Fatalf("GetLedgerData (all): %v", err)
	}
	if len(all.State) < 3 {
		t.Fatalf("need >=3 state entries, got %d", len(all.State))
	}

	page, err := svc.GetLedgerData(context.Background(), "current", 2, "")
	if err != nil {
		t.Fatalf("GetLedgerData (page): %v", err)
	}
	if len(page.State) != 2 {
		t.Fatalf("limit=2 must return 2 entries, got %d", len(page.State))
	}
	if page.Marker == "" {
		t.Fatalf("more entries remain → marker must be set")
	}

	firstUnemitted := parseHashHex(t, all.State[2].Index)
	want := formatHashHex(ledger.DecrementKey(firstUnemitted))
	if page.Marker != want {
		t.Errorf("marker = %s, want first-un-emitted-minus-one %s", page.Marker, want)
	}
	if page.Marker == page.State[len(page.State)-1].Index {
		t.Errorf("marker must not equal the last emitted key %s (rippled off-by-one)", page.State[len(page.State)-1].Index)
	}
}

func parseHashHex(t *testing.T, s string) [32]byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) != 32 {
		t.Fatalf("bad hash hex %q: %v", s, err)
	}
	var k [32]byte
	copy(k[:], raw)
	return k
}

// TestGetLedgerData_FollowMarkerCoversEveryEntryExactlyOnce mirrors rippled's
// testMarkerFollow (LedgerData_test.cpp): paging with a small limit and
// following the resume marker to exhaustion must visit every state entry
// exactly once, in ascending key order, with no gaps or repeats across page
// boundaries — the invariant the first-un-emitted-minus-one marker preserves.
func TestGetLedgerData_FollowMarkerCoversEveryEntryExactlyOnce(t *testing.T) {
	svc := newOfferTestService(t)
	for i := byte(0x10); i <= 0x1a; i++ {
		addr, _ := addressFromBytes(t, i)
		insertAccountRoot(t, svc, addr, 1_000_000_000, 0)
	}

	all, err := svc.GetLedgerData(context.Background(), "current", 256, "")
	if err != nil {
		t.Fatalf("GetLedgerData (all): %v", err)
	}
	want := make([]string, len(all.State))
	for i, it := range all.State {
		want[i] = it.Index
	}
	if len(want) < 4 {
		t.Fatalf("need >=4 state entries to exercise multi-page follow, got %d", len(want))
	}

	const pageSize = 2
	var got []string
	marker := ""
	for pages := 0; ; pages++ {
		page, err := svc.GetLedgerData(context.Background(), "current", pageSize, marker)
		if err != nil {
			t.Fatalf("GetLedgerData (page %d): %v", pages, err)
		}
		if len(page.State) > pageSize {
			t.Fatalf("page %d returned %d entries, exceeds limit %d", pages, len(page.State), pageSize)
		}
		for _, it := range page.State {
			got = append(got, it.Index)
		}
		if page.Marker == "" {
			break
		}
		marker = page.Marker
		if pages > len(want)+2 {
			t.Fatalf("marker follow did not terminate after %d pages", pages)
		}
	}

	if len(got) != len(want) {
		t.Fatalf("followed %d entries, want %d (gaps or repeats across pages)", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: followed %s, want %s (order/coverage diverged)", i, got[i], want[i])
		}
	}
}

// TestGetLedgerData_MalformedMarkerRejected pins rippled's doLedgerData
// behaviour: a present but unparseable marker is rejected (the handler maps
// svcerr.ErrInvalidMarker to "Invalid field 'marker', not valid."), not
// silently treated as a fresh first-page query.
func TestGetLedgerData_MalformedMarkerRejected(t *testing.T) {
	svc := newOfferTestService(t)
	addr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, addr, 1_000_000_000, 0)

	for _, bad := range []string{"xyz", "ABCD", strings.Repeat("0", 63), strings.Repeat("0", 65), strings.Repeat("G", 64)} {
		if _, err := svc.GetLedgerData(context.Background(), "current", 256, bad); !errors.Is(err, svcerr.ErrInvalidMarker) {
			t.Errorf("marker %q: got err %v, want ErrInvalidMarker", bad, err)
		}
	}
}

func TestGetLedgerRange(t *testing.T) {
	svc := newOfferTestService(t)
	res, err := svc.GetLedgerRange(context.Background(), 1, 3)
	if err != nil {
		t.Fatalf("GetLedgerRange: %v", err)
	}
	if res.LedgerFirst != 1 || res.LedgerLast != 3 {
		t.Errorf("range = %d..%d, want 1..3", res.LedgerFirst, res.LedgerLast)
	}
	if res.Hashes == nil {
		t.Errorf("Hashes map must be initialized")
	}
}

func TestGetAutofillSequence(t *testing.T) {
	svc := newOfferTestService(t)
	addr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, addr, 1_000_000_000, 0) // Sequence = 1

	t.Run("existing account", func(t *testing.T) {
		seq, err := svc.GetAutofillSequence(addr, false)
		if err != nil {
			t.Fatalf("GetAutofillSequence: %v", err)
		}
		if seq != 1 {
			t.Errorf("sequence = %d, want 1", seq)
		}
	})

	t.Run("malformed address", func(t *testing.T) {
		_, err := svc.GetAutofillSequence("bad", false)
		if !errors.Is(err, svcerr.ErrAccountMalformed) {
			t.Fatalf("want ErrAccountMalformed, got %v", err)
		}
	})

	t.Run("missing account without ticket", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAutofillSequence(stranger, false)
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})

	t.Run("missing account with ticket → zero", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		seq, err := svc.GetAutofillSequence(stranger, true)
		if err != nil {
			t.Fatalf("ticket path must not error: %v", err)
		}
		if seq != 0 {
			t.Errorf("ticket sequence must be 0, got %d", seq)
		}
	})
}

func TestGetTransaction_NotFound(t *testing.T) {
	svc := newOfferTestService(t)
	var hash [32]byte
	hash[0] = 0xAA
	_, err := svc.GetTransaction(hash)
	if err == nil {
		t.Fatalf("unknown transaction must error")
	}
}

func TestGetCurrentFees_Defaults(t *testing.T) {
	svc := newOfferTestService(t)
	baseFee, reserveBase, reserveInc := svc.GetCurrentFees()
	if baseFee == 0 || reserveBase == 0 || reserveInc == 0 {
		t.Errorf("fees must be non-zero, got base=%d reserveBase=%d reserveInc=%d",
			baseFee, reserveBase, reserveInc)
	}
}
