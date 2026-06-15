package service

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
)

// insertLineRaw inserts a RippleState (trust line) with full control over the
// raw sfBalance (from the low account's stored perspective), both limits, and
// flags. lowAddr must sort before highAddr per the trust-line ordering rule.
func insertLineRaw(t *testing.T, svc *Service, lowAddr, highAddr, currency, rawBalance, lowLimit, highLimit string, flags uint32) {
	t.Helper()
	_, lowBytes, err := addresscodec.DecodeClassicAddressToAccountID(lowAddr)
	if err != nil {
		t.Fatalf("decode low: %v", err)
	}
	_, highBytes, err := addresscodec.DecodeClassicAddressToAccountID(highAddr)
	if err != nil {
		t.Fatalf("decode high: %v", err)
	}
	var lowID, highID [20]byte
	copy(lowID[:], lowBytes)
	copy(highID[:], highBytes)
	if state.CompareAccountIDs(lowID, highID) >= 0 {
		t.Fatalf("lowAddr %s must sort before highAddr %s", lowAddr, highAddr)
	}

	balanceAmt, _ := state.NewIssuedAmountFromDecimalString(rawBalance, currency, state.AccountOneAddress)
	lowLimitAmt, _ := state.NewIssuedAmountFromDecimalString(lowLimit, currency, lowAddr)
	highLimitAmt, _ := state.NewIssuedAmountFromDecimalString(highLimit, currency, highAddr)
	rs := &state.RippleState{
		Balance:   balanceAmt,
		LowLimit:  lowLimitAmt,
		HighLimit: highLimitAmt,
		Flags:     flags,
	}
	data, err := state.SerializeRippleState(rs)
	if err != nil {
		t.Fatalf("serialize ripple state: %v", err)
	}
	lineKey := keylet.Line(lowID, highID, currency)
	if err := svc.openLedger.Insert(lineKey, data); err != nil {
		t.Fatalf("insert ripple state: %v", err)
	}
	// A trust line lives in both endpoints' owner directories; mirror that so the
	// owner-directory query path (gateway_balances, account_currencies, etc.) sees it.
	for _, id := range [][20]byte{lowID, highID} {
		if _, derr := state.DirInsert(svc.openLedger, keylet.OwnerDir(id), lineKey.Key, false, nil); derr != nil {
			t.Fatalf("owner-dir insert: %v", derr)
		}
	}
}

// insertPayChannelEntry inserts a PayChannel ledger entry between src and dst.
func insertPayChannelEntry(t *testing.T, svc *Service, srcAddr, dstAddr string, seq uint32, mutate func(*state.PayChannelData)) [32]byte {
	t.Helper()
	_, srcBytes, err := addresscodec.DecodeClassicAddressToAccountID(srcAddr)
	if err != nil {
		t.Fatalf("decode src: %v", err)
	}
	_, dstBytes, err := addresscodec.DecodeClassicAddressToAccountID(dstAddr)
	if err != nil {
		t.Fatalf("decode dst: %v", err)
	}
	var srcID, dstID [20]byte
	copy(srcID[:], srcBytes)
	copy(dstID[:], dstBytes)

	pc := &state.PayChannelData{
		Account:       srcID,
		DestinationID: dstID,
		Amount:        1_000_000,
		Balance:       0,
		SettleDelay:   60,
	}
	if mutate != nil {
		mutate(pc)
	}
	data, err := state.SerializePayChannelFromData(pc)
	if err != nil {
		t.Fatalf("serialize pay channel: %v", err)
	}
	k := keylet.PayChannel(srcID, dstID, seq)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert pay channel: %v", err)
	}
	return k.Key
}

// insertXRPEscrow inserts an XRP Escrow owned by ownerAddr holding the given
// drops, wiring it into both the ledger and the owner's directory so the
// owner-directory query path observes it.
func insertXRPEscrow(t *testing.T, svc *Service, ownerAddr, destAddr string, seq uint32, drops string) [32]byte {
	t.Helper()
	_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	var ownerID [20]byte
	copy(ownerID[:], ownerBytes)

	hexStr, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType": "Escrow",
		"Account":         ownerAddr,
		"Destination":     destAddr,
		"Amount":          drops,
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	})
	if err != nil {
		t.Fatalf("encode escrow: %v", err)
	}
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode escrow hex: %v", err)
	}

	k := keylet.Escrow(ownerID, seq)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert escrow: %v", err)
	}
	if _, derr := state.DirInsert(svc.openLedger, keylet.OwnerDir(ownerID), k.Key, false, nil); derr != nil {
		t.Fatalf("owner-dir insert escrow: %v", derr)
	}
	return k.Key
}

func TestGetAccountInfo_FieldsAndErrors(t *testing.T) {
	svc := newOfferTestService(t)
	addr, idBytes := addressFromBytes(t, 0x10)

	root := &state.AccountRoot{
		Account:    addr,
		Balance:    250_000_000,
		Sequence:   7,
		OwnerCount: 3,
		Flags:      state.LsfDefaultRipple,
	}
	data, err := state.SerializeAccountRoot(root)
	if err != nil {
		t.Fatalf("serialize account root: %v", err)
	}
	if err := svc.openLedger.Insert(keylet.Account(idBytes), data); err != nil {
		t.Fatalf("insert account root: %v", err)
	}

	info, err := svc.GetAccountInfo(context.Background(), addr, "current")
	if err != nil {
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if info.Balance != 250_000_000 {
		t.Errorf("balance = %d, want 250000000", info.Balance)
	}
	if info.Sequence != 7 {
		t.Errorf("sequence = %d, want 7", info.Sequence)
	}
	if info.OwnerCount != 3 {
		t.Errorf("owner_count = %d, want 3", info.OwnerCount)
	}
	if info.Flags&state.LsfDefaultRipple == 0 {
		t.Errorf("DefaultRipple flag not reflected, flags = %#x", info.Flags)
	}
	if info.Validated {
		t.Errorf("current ledger must report validated=false")
	}
	if len(info.RawData) == 0 {
		t.Errorf("RawData must be populated")
	}

	t.Run("not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAccountInfo(context.Background(), stranger, "current")
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})

	t.Run("malformed address", func(t *testing.T) {
		_, err := svc.GetAccountInfo(context.Background(), "not-an-address", "current")
		if !errors.Is(err, svcerr.ErrAccountMalformed) {
			t.Fatalf("want ErrAccountMalformed, got %v", err)
		}
	})

	t.Run("invalid ledger_index", func(t *testing.T) {
		_, err := svc.GetAccountInfo(context.Background(), addr, "bogus")
		if !errors.Is(err, svcerr.ErrInvalidLedgerIndex) {
			t.Fatalf("want ErrInvalidLedgerIndex, got %v", err)
		}
	})

	t.Run("numeric ledger not found", func(t *testing.T) {
		_, err := svc.GetAccountInfo(context.Background(), addr, "999999")
		if !errors.Is(err, ErrLedgerNotFound) {
			t.Fatalf("want ErrLedgerNotFound, got %v", err)
		}
	})

	t.Run("cancelled context", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := svc.GetAccountInfo(ctx, addr, "current")
		if err == nil {
			t.Fatalf("want context error, got nil")
		}
	})
}

// TestGetAccountLines_FlagMapping pins the per-side flag mapping fixed for #750:
// no_ripple / authorized must read lsfLowNoRipple (0x00100000) / lsfLowAuth
// (0x00040000) — not the reserve bits that were previously hardcoded. A single
// trust line is queried from both sides to exercise the low and high branches.
func TestGetAccountLines_FlagMapping(t *testing.T) {
	svc := newOfferTestService(t)

	// 0x10-account sorts before 0x40-account, so low = A, high = B.
	aAddr, _ := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000, 0)
	insertAccountRoot(t, svc, bAddr, 1_000_000_000, 0)

	// Low side sets NoRipple; high side sets Auth and Freeze.
	flags := state.LsfLowNoRipple | state.LsfHighAuth | state.LsfHighFreeze
	insertLineRaw(t, svc, aAddr, bAddr, "USD", "0", "100", "200", flags)

	t.Run("low account perspective", func(t *testing.T) {
		res, err := svc.GetAccountLines(context.Background(), aAddr, "current", "", 0, "")
		if err != nil {
			t.Fatalf("GetAccountLines: %v", err)
		}
		if len(res.Lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(res.Lines))
		}
		ln := res.Lines[0]
		if ln.Account != bAddr {
			t.Errorf("peer = %s, want %s", ln.Account, bAddr)
		}
		if ln.Currency != "USD" {
			t.Errorf("currency = %s, want USD", ln.Currency)
		}
		if ln.Limit != "100" || ln.LimitPeer != "200" {
			t.Errorf("limits = %s / %s, want 100 / 200", ln.Limit, ln.LimitPeer)
		}
		if !ln.NoRipple {
			t.Errorf("low side NoRipple must be true (lsfLowNoRipple)")
		}
		if ln.NoRipplePeer {
			t.Errorf("peer (high) did not set NoRipple")
		}
		if ln.Authorized {
			t.Errorf("low side did not set Auth")
		}
		if !ln.PeerAuthorized {
			t.Errorf("peer (high) set Auth → peer_authorized must be true (lsfHighAuth)")
		}
		if ln.Freeze {
			t.Errorf("low side did not freeze")
		}
		if !ln.FreezePeer {
			t.Errorf("peer (high) froze → freeze_peer must be true (lsfHighFreeze)")
		}
	})

	t.Run("high account perspective", func(t *testing.T) {
		res, err := svc.GetAccountLines(context.Background(), bAddr, "current", "", 0, "")
		if err != nil {
			t.Fatalf("GetAccountLines: %v", err)
		}
		if len(res.Lines) != 1 {
			t.Fatalf("expected 1 line, got %d", len(res.Lines))
		}
		ln := res.Lines[0]
		if ln.Account != aAddr {
			t.Errorf("peer = %s, want %s", ln.Account, aAddr)
		}
		if ln.NoRipple {
			t.Errorf("high side did not set NoRipple")
		}
		if !ln.NoRipplePeer {
			t.Errorf("peer (low) set NoRipple → no_ripple_peer must be true (lsfLowNoRipple)")
		}
		if !ln.Authorized {
			t.Errorf("high side set Auth → authorized must be true (lsfHighAuth)")
		}
		if !ln.Freeze {
			t.Errorf("high side froze → freeze must be true (lsfHighFreeze)")
		}
	})
}

func TestGetAccountLines_PeerFilterAndErrors(t *testing.T) {
	svc := newOfferTestService(t)
	aAddr, _ := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	cAddr, _ := addressFromBytes(t, 0x60)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000, 0)

	insertLineRaw(t, svc, aAddr, bAddr, "USD", "0", "100", "100", 0)
	insertLineRaw(t, svc, aAddr, cAddr, "EUR", "0", "100", "100", 0)

	t.Run("no filter returns both", func(t *testing.T) {
		res, err := svc.GetAccountLines(context.Background(), aAddr, "current", "", 0, "")
		if err != nil {
			t.Fatalf("GetAccountLines: %v", err)
		}
		if len(res.Lines) != 2 {
			t.Fatalf("expected 2 lines, got %d", len(res.Lines))
		}
	})

	t.Run("peer filter narrows to one", func(t *testing.T) {
		res, err := svc.GetAccountLines(context.Background(), aAddr, "current", bAddr, 0, "")
		if err != nil {
			t.Fatalf("GetAccountLines: %v", err)
		}
		if len(res.Lines) != 1 || res.Lines[0].Account != bAddr {
			t.Fatalf("peer filter failed: %+v", res.Lines)
		}
	})

	t.Run("invalid peer address", func(t *testing.T) {
		_, err := svc.GetAccountLines(context.Background(), aAddr, "current", "nope", 0, "")
		if err == nil {
			t.Fatalf("want error for invalid peer, got nil")
		}
	})

	t.Run("malformed account", func(t *testing.T) {
		_, err := svc.GetAccountLines(context.Background(), "bad", "current", "", 0, "")
		if !errors.Is(err, svcerr.ErrAccountMalformed) {
			t.Fatalf("want ErrAccountMalformed, got %v", err)
		}
	})
}

func TestGetAccountCurrencies_SendAndReceive(t *testing.T) {
	svc := newOfferTestService(t)
	aAddr, _ := addressFromBytes(t, 0x10)
	bAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, aAddr, 1_000_000_000, 0)

	// A is the low account. Raw balance -50 means (from A's perspective,
	// negated) A holds +50 → can send. LowLimit 100 > 50 → can also receive.
	insertLineRaw(t, svc, aAddr, bAddr, "USD", "-50", "100", "100", 0)

	res, err := svc.GetAccountCurrencies(context.Background(), aAddr, "current")
	if err != nil {
		t.Fatalf("GetAccountCurrencies: %v", err)
	}
	if !containsString(res.SendCurrencies, "USD") {
		t.Errorf("USD should be sendable, got %v", res.SendCurrencies)
	}
	if !containsString(res.ReceiveCurrencies, "USD") {
		t.Errorf("USD should be receivable, got %v", res.ReceiveCurrencies)
	}

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAccountCurrencies(context.Background(), stranger, "current")
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})
}

func TestGetAccountObjects_TypeFilterAndErrors(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	insertOffer(t, svc, ownerAddr, 2,
		state.NewIssuedAmountFromFloat64(200, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	t.Run("all objects", func(t *testing.T) {
		res, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "", 0, "")
		if err != nil {
			t.Fatalf("GetAccountObjects: %v", err)
		}
		offerCount := 0
		for _, o := range res.AccountObjects {
			if o.LedgerEntryType == "Offer" {
				offerCount++
			}
		}
		if offerCount != 2 {
			t.Fatalf("expected 2 Offer objects, got %d (of %d total)", offerCount, len(res.AccountObjects))
		}
	})

	t.Run("snake_case type filter", func(t *testing.T) {
		res, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "offer", 0, "")
		if err != nil {
			t.Fatalf("GetAccountObjects: %v", err)
		}
		if len(res.AccountObjects) != 2 {
			t.Fatalf("expected 2 offers, got %d", len(res.AccountObjects))
		}
	})

	t.Run("filter excludes other types", func(t *testing.T) {
		res, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "check", 0, "")
		if err != nil {
			t.Fatalf("GetAccountObjects: %v", err)
		}
		if len(res.AccountObjects) != 0 {
			t.Fatalf("expected 0 checks, got %d", len(res.AccountObjects))
		}
	})

	t.Run("limit honored", func(t *testing.T) {
		res, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "offer", 1, "")
		if err != nil {
			t.Fatalf("GetAccountObjects: %v", err)
		}
		if len(res.AccountObjects) != 1 {
			t.Fatalf("limit=1 must cap at 1 object, got %d", len(res.AccountObjects))
		}
		if res.Marker == "" {
			t.Fatal("a truncated page must carry a resume marker")
		}
	})

	t.Run("pagination via marker", func(t *testing.T) {
		// First page: limit 1 yields one Offer plus a resume marker.
		page1, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "offer", 1, "")
		if err != nil {
			t.Fatalf("GetAccountObjects page1: %v", err)
		}
		if len(page1.AccountObjects) != 1 || page1.Marker == "" {
			t.Fatalf("page1 = %d objects, marker %q; want 1 object + marker", len(page1.AccountObjects), page1.Marker)
		}

		// Second page: resume from the marker → the remaining Offer, no marker.
		page2, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "offer", 1, page1.Marker)
		if err != nil {
			t.Fatalf("GetAccountObjects page2: %v", err)
		}
		if len(page2.AccountObjects) != 1 {
			t.Fatalf("page2 should hold 1 object, got %d", len(page2.AccountObjects))
		}
		if page2.Marker != "" {
			t.Errorf("page2 is the last page; want no marker, got %q", page2.Marker)
		}
		if page1.AccountObjects[0].Index == page2.AccountObjects[0].Index {
			t.Errorf("pages overlap: both returned index %s", page1.AccountObjects[0].Index)
		}
	})

	t.Run("invalid marker rejected", func(t *testing.T) {
		_, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "", 0, "not-hex")
		if !errors.Is(err, svcerr.ErrInvalidMarker) {
			t.Fatalf("want ErrInvalidMarker, got %v", err)
		}
	})

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAccountObjects(context.Background(), stranger, "current", "", 0, "")
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})
}

// TestGetAccountObjects_MarkerPagination walks an account that owns more objects
// than fit in one directory page, with a small per-page limit, and asserts the
// marker round-trip returns every object exactly once across pages (including
// the IndexNext page transition) and stops with no marker on the last page.
func TestGetAccountObjects_MarkerPagination(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	// More than one directory page (32 entries) so IndexNext is exercised.
	const total = 40
	want := map[string]bool{}
	for seq := uint32(1); seq <= total; seq++ {
		key := insertOffer(t, svc, ownerAddr, seq,
			state.NewIssuedAmountFromFloat64(float64(seq), "USD", issuerAddr),
			tx.NewXRPAmount(10_000_000),
		)
		want[formatHashHex(key)] = true
	}

	seen := map[string]bool{}
	marker := ""
	pages := 0
	for {
		res, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "offer", 7, marker)
		if err != nil {
			t.Fatalf("page %d: %v", pages, err)
		}
		pages++
		for _, o := range res.AccountObjects {
			if o.LedgerEntryType != "Offer" {
				t.Fatalf("type filter leaked a %s object", o.LedgerEntryType)
			}
			if seen[o.Index] {
				t.Fatalf("object %s returned twice across pages", o.Index)
			}
			seen[o.Index] = true
		}
		if res.Marker == "" {
			break
		}
		marker = res.Marker
		if pages > total+2 {
			t.Fatal("pagination did not terminate")
		}
	}

	if len(seen) != total {
		t.Fatalf("paginated walk returned %d offers, want %d", len(seen), total)
	}
	for idx := range want {
		if !seen[idx] {
			t.Errorf("missing offer %s from paginated walk", idx)
		}
	}
	if pages < 2 {
		t.Fatalf("expected the walk to span multiple pages, got %d", pages)
	}

	t.Run("malformed marker (no comma)", func(t *testing.T) {
		_, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "", 10, "deadbeef")
		if !errors.Is(err, svcerr.ErrInvalidMarker) {
			t.Fatalf("want ErrInvalidMarker, got %v", err)
		}
	})

	t.Run("marker naming a nonexistent directory page", func(t *testing.T) {
		badDir := strings.Repeat("AB", 32)
		_, err := svc.GetAccountObjects(context.Background(), ownerAddr, "current", "", 10, badDir+","+badDir)
		if !errors.Is(err, svcerr.ErrInvalidMarker) {
			t.Fatalf("want ErrInvalidMarker, got %v", err)
		}
	})
}

func TestGetAccountChannels_FilterAndFields(t *testing.T) {
	svc := newOfferTestService(t)
	srcAddr, _ := addressFromBytes(t, 0x10)
	dst1Addr, _ := addressFromBytes(t, 0x40)
	dst2Addr, _ := addressFromBytes(t, 0x60)
	insertAccountRoot(t, svc, srcAddr, 1_000_000_000_000, 0)

	insertPayChannelEntry(t, svc, srcAddr, dst1Addr, 1, func(pc *state.PayChannelData) {
		pc.Amount = 5_000_000
		pc.Balance = 1_000_000
		pc.SettleDelay = 120
		pc.Expiration = 700000000
		pc.SourceTag = 42
		pc.HasSourceTag = true
	})
	insertPayChannelEntry(t, svc, srcAddr, dst2Addr, 2, func(pc *state.PayChannelData) {
		pc.Amount = 9_000_000
	})

	t.Run("all channels", func(t *testing.T) {
		res, err := svc.GetAccountChannels(context.Background(), srcAddr, "", "current", 0, "")
		if err != nil {
			t.Fatalf("GetAccountChannels: %v", err)
		}
		if len(res.Channels) != 2 {
			t.Fatalf("expected 2 channels, got %d", len(res.Channels))
		}
	})

	t.Run("destination filter + field decode", func(t *testing.T) {
		res, err := svc.GetAccountChannels(context.Background(), srcAddr, dst1Addr, "current", 0, "")
		if err != nil {
			t.Fatalf("GetAccountChannels: %v", err)
		}
		if len(res.Channels) != 1 {
			t.Fatalf("expected 1 channel, got %d", len(res.Channels))
		}
		ch := res.Channels[0]
		if ch.DestinationAccount != dst1Addr {
			t.Errorf("destination = %s, want %s", ch.DestinationAccount, dst1Addr)
		}
		if ch.Amount != "5000000" {
			t.Errorf("amount = %s, want 5000000", ch.Amount)
		}
		if ch.Balance != "1000000" {
			t.Errorf("balance = %s, want 1000000", ch.Balance)
		}
		if ch.SettleDelay != 120 {
			t.Errorf("settle_delay = %d, want 120", ch.SettleDelay)
		}
		if ch.Expiration != 700000000 {
			t.Errorf("expiration = %d, want 700000000", ch.Expiration)
		}
		if !ch.HasSourceTag || ch.SourceTag != 42 {
			t.Errorf("source_tag = %d (has=%v), want 42", ch.SourceTag, ch.HasSourceTag)
		}
	})

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetAccountChannels(context.Background(), stranger, "", "current", 0, "")
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})

	t.Run("invalid destination", func(t *testing.T) {
		_, err := svc.GetAccountChannels(context.Background(), srcAddr, "bad", "current", 0, "")
		if err == nil {
			t.Fatalf("want error for invalid destination, got nil")
		}
	})
}

func TestGetGatewayBalances_Categories(t *testing.T) {
	svc := newOfferTestService(t)
	// Gateway is the low account on every line (smaller seed).
	gwAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, gwAddr, 1_000_000_000_000, 0)

	obligPeer, _ := addressFromBytes(t, 0x40) // normal obligation
	hotPeer, _ := addressFromBytes(t, 0x50)   // hot wallet
	assetPeer, _ := addressFromBytes(t, 0x60) // gateway holds (asset)
	frozenPeer, _ := addressFromBytes(t, 0x70)

	// gateway low; raw balance negative ⇒ gateway owes the peer (obligation).
	insertLineRaw(t, svc, gwAddr, obligPeer, "USD", "-100", "0", "1000", 0)
	insertLineRaw(t, svc, gwAddr, hotPeer, "USD", "-25", "0", "1000", 0)
	// positive ⇒ gateway holds the peer's currency (asset).
	insertLineRaw(t, svc, gwAddr, assetPeer, "EUR", "30", "1000", "0", 0)
	// negative + low-side freeze ⇒ frozen obligation.
	insertLineRaw(t, svc, gwAddr, frozenPeer, "USD", "-40", "0", "1000", state.LsfLowFreeze)

	res, err := svc.GetGatewayBalances(context.Background(), gwAddr, []string{hotPeer}, "current")
	if err != nil {
		t.Fatalf("GetGatewayBalances: %v", err)
	}

	if res.Obligations["USD"] != "100" {
		t.Errorf("USD obligation = %q, want 100", res.Obligations["USD"])
	}
	if len(res.Balances[hotPeer]) != 1 || res.Balances[hotPeer][0].Value != "25" {
		t.Errorf("hot wallet balance not reported correctly: %+v", res.Balances[hotPeer])
	}
	if len(res.Assets[assetPeer]) != 1 || res.Assets[assetPeer][0].Currency != "EUR" {
		t.Errorf("asset not reported correctly: %+v", res.Assets[assetPeer])
	}
	if len(res.FrozenBalances[frozenPeer]) != 1 || res.FrozenBalances[frozenPeer][0].Value != "40" {
		t.Errorf("frozen balance not reported correctly: %+v", res.FrozenBalances[frozenPeer])
	}

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetGatewayBalances(context.Background(), stranger, nil, "current")
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})

	t.Run("invalid hot wallet", func(t *testing.T) {
		_, err := svc.GetGatewayBalances(context.Background(), gwAddr, []string{"bad"}, "current")
		if err == nil {
			t.Fatalf("want error for invalid hot wallet, got nil")
		}
	})
}

func TestGetGatewayBalances_LockedEscrows(t *testing.T) {
	svc := newOfferTestService(t)
	gwAddr, _ := addressFromBytes(t, 0x10)
	destAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, gwAddr, 1_000_000_000_000, 0)

	// Two XRP escrows owned by the gateway lock 1.5 XRP total.
	insertXRPEscrow(t, svc, gwAddr, destAddr, 1, "1000000")
	insertXRPEscrow(t, svc, gwAddr, destAddr, 2, "500000")
	// An ordinary obligation alongside the escrows must still be reported.
	insertLineRaw(t, svc, gwAddr, destAddr, "USD", "-100", "0", "1000", 0)

	res, err := svc.GetGatewayBalances(context.Background(), gwAddr, nil, "current")
	if err != nil {
		t.Fatalf("GetGatewayBalances: %v", err)
	}
	if got := res.Locked["XRP"]; got != "1500000" {
		t.Errorf("locked XRP = %q, want 1500000", got)
	}
	if got := res.Obligations["USD"]; got != "100" {
		t.Errorf("USD obligation = %q, want 100", got)
	}
}

// TestGetAccountCurrencies_OwnerDirExcludesForeignLines proves the owner-directory
// walk yields the same trust-line set the old whole-ledger scan would: it includes
// the account's own lines and excludes a foreign line between third parties that a
// scan would otherwise visit.
func TestGetAccountCurrencies_OwnerDirExcludesForeignLines(t *testing.T) {
	svc := newOfferTestService(t)
	acct, _ := addressFromBytes(t, 0x10)
	peer, _ := addressFromBytes(t, 0x40)
	other1, _ := addressFromBytes(t, 0x80)
	other2, _ := addressFromBytes(t, 0x90)
	insertAccountRoot(t, svc, acct, 1_000_000_000, 0)

	// Our line: acct is low; raw balance -10 means acct holds +10 → can send USD.
	insertLineRaw(t, svc, acct, peer, "USD", "-10", "1000", "1000", 0)
	// Foreign line present in the ledger but not in acct's owner directory.
	insertLineRaw(t, svc, other1, other2, "EUR", "-10", "1000", "1000", 0)

	res, err := svc.GetAccountCurrencies(context.Background(), acct, "current")
	if err != nil {
		t.Fatalf("GetAccountCurrencies: %v", err)
	}
	if !containsString(res.SendCurrencies, "USD") {
		t.Errorf("send currencies %v should contain USD", res.SendCurrencies)
	}
	if containsString(res.SendCurrencies, "EUR") || containsString(res.ReceiveCurrencies, "EUR") {
		t.Errorf("foreign EUR line must not surface: send=%v receive=%v", res.SendCurrencies, res.ReceiveCurrencies)
	}
}

func TestGetNoRippleCheck_RolesAndProblems(t *testing.T) {
	svc := newOfferTestService(t)
	gwAddr, _ := addressFromBytes(t, 0x10)
	peerAddr, _ := addressFromBytes(t, 0x40)
	// Gateway without DefaultRipple, with a low-side NoRipple line.
	insertAccountRoot(t, svc, gwAddr, 1_000_000_000, 0)
	insertLineRaw(t, svc, gwAddr, peerAddr, "USD", "0", "100", "100", state.LsfLowNoRipple)

	t.Run("gateway role flags default-ripple and no-ripple line", func(t *testing.T) {
		res, err := svc.GetNoRippleCheck(context.Background(), gwAddr, "gateway", "current", 0, true)
		if err != nil {
			t.Fatalf("GetNoRippleCheck: %v", err)
		}
		if len(res.Problems) < 2 {
			t.Fatalf("expected >=2 problems, got %v", res.Problems)
		}
		if len(res.Transactions) == 0 {
			t.Fatalf("transactions=true must yield suggested transactions")
		}
		var sawAccountSet, sawTrustSet bool
		for _, tx := range res.Transactions {
			switch tx.TransactionType {
			case "AccountSet":
				sawAccountSet = true
			case "TrustSet":
				sawTrustSet = true
			}
		}
		if !sawAccountSet || !sawTrustSet {
			t.Errorf("expected both AccountSet and TrustSet suggestions, got %+v", res.Transactions)
		}
	})

	t.Run("invalid role", func(t *testing.T) {
		_, err := svc.GetNoRippleCheck(context.Background(), gwAddr, "banker", "current", 0, false)
		if err == nil {
			t.Fatalf("want error for invalid role, got nil")
		}
	})

	t.Run("account not found", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		_, err := svc.GetNoRippleCheck(context.Background(), stranger, "user", "current", 0, false)
		if !errors.Is(err, svcerr.ErrAccountNotFound) {
			t.Fatalf("want ErrAccountNotFound, got %v", err)
		}
	})
}

func TestGetLedgerForQuery_Branches(t *testing.T) {
	svc := newOfferTestService(t)

	t.Run("current resolves open ledger", func(t *testing.T) {
		l, validated, err := svc.getLedgerForQuery("current")
		if err != nil || l == nil {
			t.Fatalf("current: l=%v err=%v", l, err)
		}
		if validated {
			t.Errorf("current must not be validated")
		}
	})
	t.Run("empty string resolves open ledger", func(t *testing.T) {
		l, _, err := svc.getLedgerForQuery("")
		if err != nil || l == nil {
			t.Fatalf("empty: l=%v err=%v", l, err)
		}
	})
	t.Run("validated", func(t *testing.T) {
		l, validated, err := svc.getLedgerForQuery("validated")
		if err != nil || l == nil {
			t.Fatalf("validated: l=%v err=%v", l, err)
		}
		if !validated {
			t.Errorf("validated branch must report validated=true")
		}
	})
	t.Run("closed", func(t *testing.T) {
		l, _, err := svc.getLedgerForQuery("closed")
		if err != nil || l == nil {
			t.Fatalf("closed: l=%v err=%v", l, err)
		}
	})
	t.Run("invalid", func(t *testing.T) {
		_, _, err := svc.getLedgerForQuery("xyz")
		if !errors.Is(err, svcerr.ErrInvalidLedgerIndex) {
			t.Fatalf("want ErrInvalidLedgerIndex, got %v", err)
		}
	})
	t.Run("numeric not found", func(t *testing.T) {
		_, _, err := svc.getLedgerForQuery("123456789")
		if !errors.Is(err, ErrLedgerNotFound) {
			t.Fatalf("want ErrLedgerNotFound, got %v", err)
		}
	})
}

func TestGetAccountOffers_FormatsAmounts(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	// Offer: TakerGets XRP (native), TakerPays IOU.
	takerPays := state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr)
	takerGets := tx.NewXRPAmount(10_000_000)
	insertOffer(t, svc, ownerAddr, 1, takerPays, takerGets)
	// Offer from a different owner must be excluded.
	otherAddr, _ := addressFromBytes(t, 0x30)
	insertAccountRoot(t, svc, otherAddr, 1_000_000_000_000, 0)
	insertOffer(t, svc, otherAddr, 1,
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	res, err := svc.GetAccountOffers(context.Background(), ownerAddr, "current", 0, "")
	if err != nil {
		t.Fatalf("GetAccountOffers: %v", err)
	}
	if len(res.Offers) != 1 {
		t.Fatalf("expected 1 offer for owner, got %d", len(res.Offers))
	}
	o := res.Offers[0]
	if o.Seq != 1 {
		t.Errorf("seq = %d, want 1", o.Seq)
	}
	// TakerGets is native → emitted as a drops string.
	if _, ok := o.TakerGets.(string); !ok {
		t.Errorf("native taker_gets should be a string, got %T", o.TakerGets)
	}
	// TakerPays is an IOU → emitted as a map.
	pays, ok := o.TakerPays.(map[string]string)
	if !ok {
		t.Fatalf("IOU taker_pays should be a map, got %T", o.TakerPays)
	}
	if pays["currency"] != "USD" || pays["issuer"] != issuerAddr {
		t.Errorf("taker_pays = %+v, want USD/%s", pays, issuerAddr)
	}
	// quality must equal the offer's book-directory rate (saDirRate), derived
	// from the BookDirectory key — not a recomputed TakerPays/TakerGets float
	// division.
	wantQuality := qualityFromDirKey(state.CalculateQuality(takerPays, takerGets))
	if o.Quality != wantQuality {
		t.Errorf("quality = %q, want %q (from book directory rate)", o.Quality, wantQuality)
	}

	t.Run("unknown account yields empty offers", func(t *testing.T) {
		stranger, _ := addressFromBytes(t, 0x99)
		res, err := svc.GetAccountOffers(context.Background(), stranger, "current", 0, "")
		if err != nil {
			t.Fatalf("GetAccountOffers(stranger): %v", err)
		}
		if len(res.Offers) != 0 {
			t.Fatalf("stranger must have 0 offers, got %d", len(res.Offers))
		}
	})
}

func containsString(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
