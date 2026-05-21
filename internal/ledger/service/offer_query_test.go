package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/goXRPLd/codec/addresscodec"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/ledger/entry"
)

// addressFromBytes produces a deterministic test classic-address from a seed byte.
func addressFromBytes(t *testing.T, seed byte) (string, [20]byte) {
	t.Helper()
	var id [20]byte
	for i := range id {
		id[i] = seed + byte(i)
	}
	addr, err := addresscodec.EncodeAccountIDToClassicAddress(id[:])
	if err != nil {
		t.Fatalf("encode address: %v", err)
	}
	return addr, id
}

func insertAccountRoot(t *testing.T, svc *Service, addr string, balanceDrops uint64, transferRate uint32) [20]byte {
	t.Helper()
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(addr)
	if err != nil {
		t.Fatalf("decode address: %v", err)
	}
	var id [20]byte
	copy(id[:], idBytes)
	root := &state.AccountRoot{
		Account:      addr,
		Balance:      balanceDrops,
		Sequence:     1,
		Flags:        0,
		TransferRate: transferRate,
	}
	data, err := state.SerializeAccountRoot(root)
	if err != nil {
		t.Fatalf("serialize account root: %v", err)
	}
	if err := svc.openLedger.Insert(keylet.Account(id), data); err != nil {
		t.Fatalf("insert account root: %v", err)
	}
	return id
}

func insertOffer(t *testing.T, svc *Service, ownerAddr string, sequence uint32, takerPays, takerGets tx.Amount) [32]byte {
	t.Helper()
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	var id [20]byte
	copy(id[:], idBytes)

	// Build the real book directory key so GetBookOffers can walk it.
	payCurr := state.GetCurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := state.GetCurrencyBytes(takerGets.Currency)
	getsIssuer := state.GetIssuerBytes(takerGets.Issuer)
	bookBase := keylet.BookDir(payCurr, payIssuer, getsCurr, getsIssuer).Key
	quality := state.CalculateQuality(takerPays, takerGets)
	var bookDir [32]byte
	copy(bookDir[:], bookBase[:])
	binary.BigEndian.PutUint64(bookDir[24:], quality)

	offer := &state.LedgerOffer{
		Account:       ownerAddr,
		Sequence:      sequence,
		TakerPays:     takerPays,
		TakerGets:     takerGets,
		BookDirectory: bookDir,
		Flags:         0,
	}
	data, err := state.SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatalf("serialize offer: %v", err)
	}
	k := keylet.Offer(id, sequence)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert offer: %v", err)
	}

	// Insert the offer key into the book directory so the directory walk
	// reaches it. Mirrors rippled's dirAdd from CreateOffer apply path.
	dirKeylet := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: bookDir}
	if _, derr := state.DirInsert(svc.openLedger, dirKeylet, k.Key, func(d *state.DirectoryNode) {
		d.TakerPaysCurrency = payCurr
		d.TakerPaysIssuer = payIssuer
		d.TakerGetsCurrency = getsCurr
		d.TakerGetsIssuer = getsIssuer
		d.ExchangeRate = quality
	}); derr != nil {
		t.Fatalf("dir insert: %v", derr)
	}

	return k.Key
}

func newOfferTestService(t *testing.T) *Service {
	t.Helper()
	svc, err := New(DefaultConfig())
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := svc.Start(); err != nil {
		t.Fatalf("start service: %v", err)
	}
	return svc
}

// TestGetBookOffers_XRP_OwnerFundsAndUnfunded covers the XRP-side ledger path:
// owner_funds tracks the spendable XRP (balance minus reserve) and underfunded
// offers get taker_gets_funded / taker_pays_funded fields.
func TestGetBookOffers_XRP_OwnerFundsAndUnfunded(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	wellFundedAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, wellFundedAddr, 1_000_000_000_000, 0)

	underFundedAddr, _ := addressFromBytes(t, 0x30)
	// Just over the base reserve so liquid XRP is small.
	insertAccountRoot(t, svc, underFundedAddr, 11_000_000, 0)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	// Well-funded: offer selling 10 XRP for 100 USD.
	insertOffer(t, svc, wellFundedAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	// Underfunded: offer selling 10 XRP for 50 USD, but liquid XRP is only ~1 XRP.
	insertOffer(t, svc, underFundedAddr, 1,
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}

	byOwner := map[string]BookOffer{}
	for _, o := range result.Offers {
		byOwner[o.Account] = o
	}

	wf := byOwner[wellFundedAddr]
	if wf.OwnerFunds == "" {
		t.Fatalf("well-funded owner_funds should be set, got empty")
	}
	if wf.TakerGetsFunded != nil || wf.TakerPaysFunded != nil {
		t.Errorf("well-funded offer should not have *_funded fields, got gets=%v pays=%v",
			wf.TakerGetsFunded, wf.TakerPaysFunded)
	}

	uf := byOwner[underFundedAddr]
	if uf.OwnerFunds == "" {
		t.Fatalf("underfunded owner_funds should be set")
	}
	// owner_funds should reflect liquid XRP — at most balance - reserveBase.
	of, perr := strconv.ParseUint(uf.OwnerFunds, 10, 64)
	if perr != nil {
		t.Fatalf("parse owner_funds: %v", perr)
	}
	if of >= 10_000_000 {
		t.Errorf("underfunded owner_funds (%d drops) should be below offer size (10000000)", of)
	}
	if uf.TakerGetsFunded == nil || uf.TakerPaysFunded == nil {
		t.Fatalf("underfunded offer must emit both *_funded fields, got gets=%v pays=%v",
			uf.TakerGetsFunded, uf.TakerPaysFunded)
	}
}

// TestGetBookOffers_OwnerFundsOncePerOwner ensures owner_funds appears only on
// the first offer surfaced for a given owner, matching rippled's
// firstOwnerOffer behavior.
func TestGetBookOffers_OwnerFundsOncePerOwner(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x40)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x50)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	// Two offers from the same owner at different qualities — owner_funds is
	// emitted on the better-priced offer only.
	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	insertOffer(t, svc, ownerAddr, 2,
		state.NewIssuedAmountFromFloat64(200, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}
	first, second := result.Offers[0], result.Offers[1]
	if first.OwnerFunds == "" {
		t.Errorf("first offer (best quality) should carry owner_funds")
	}
	if second.OwnerFunds != "" {
		t.Errorf("second offer from same owner should omit owner_funds, got %q", second.OwnerFunds)
	}
}

// TestGetBookOffers_IssuerOwnIOUFullyFunded covers the special case where the
// offer owner is the issuer of taker_gets — rippled treats this as fully
// funded with no transfer-fee reduction.
func TestGetBookOffers_IssuerOwnIOUFullyFunded(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x60)
	// Non-trivial transfer rate to verify the issuer-self path skips it.
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 1_200_000_000)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	// Issuer's own offer: takerGets = their own USD.
	insertOffer(t, svc, issuerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
	)

	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if o.TakerGetsFunded != nil || o.TakerPaysFunded != nil {
		t.Errorf("issuer-owned offer should be fully funded, got gets=%v pays=%v",
			o.TakerGetsFunded, o.TakerPaysFunded)
	}
	if o.OwnerFunds != "50" {
		t.Errorf("issuer's own owner_funds should equal taker_gets value (50), got %q", o.OwnerFunds)
	}
}

// TestGetBookOffers_IssuerOwnIOU_AllOffersEmitOwnerFunds pins rippled's
// per-iteration firstOwnerOffer semantics: when the offer owner is the issuer
// of taker_gets, every offer surfaced for that owner reports owner_funds equal
// to the offer's own taker_gets value (rippled NetworkOPs.cpp:4514,4607-4608).
func TestGetBookOffers_IssuerOwnIOU_AllOffersEmitOwnerFunds(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x70)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	// Two own-IOU offers from the same issuer at different qualities.
	insertOffer(t, svc, issuerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
	)
	insertOffer(t, svc, issuerAddr, 2,
		tx.NewXRPAmount(40_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}
	for i, o := range result.Offers {
		if o.OwnerFunds == "" {
			t.Errorf("offer %d: own-IOU branch must emit owner_funds on every iteration", i)
		}
	}
}

// insertPermissionedOffer inserts a pure permissioned offer (DomainID set,
// no hybrid) directly into a domain-scoped book directory. It mirrors the
// rippled CreateOffer apply path for `tfHybrid == 0`: the offer lives in
// the domain book only.
func insertPermissionedOffer(t *testing.T, svc *Service, ownerAddr string, sequence uint32, takerPays, takerGets tx.Amount, domainID [32]byte) [32]byte {
	t.Helper()
	_, idBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	var id [20]byte
	copy(id[:], idBytes)

	payCurr := state.GetCurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := state.GetCurrencyBytes(takerGets.Currency)
	getsIssuer := state.GetIssuerBytes(takerGets.Issuer)
	bookBase := keylet.BookDirWithDomain(payCurr, payIssuer, getsCurr, getsIssuer, domainID).Key
	quality := state.CalculateQuality(takerPays, takerGets)
	var bookDir [32]byte
	copy(bookDir[:], bookBase[:])
	binary.BigEndian.PutUint64(bookDir[24:], quality)

	offer := &state.LedgerOffer{
		Account:       ownerAddr,
		Sequence:      sequence,
		TakerPays:     takerPays,
		TakerGets:     takerGets,
		BookDirectory: bookDir,
		DomainID:      domainID,
		Flags:         0,
	}
	data, err := state.SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatalf("serialize offer: %v", err)
	}
	k := keylet.Offer(id, sequence)
	if err := svc.openLedger.Insert(k, data); err != nil {
		t.Fatalf("insert offer: %v", err)
	}

	dirKeylet := keylet.Keylet{Type: entry.TypeDirectoryNode, Key: bookDir}
	if _, derr := state.DirInsert(svc.openLedger, dirKeylet, k.Key, func(d *state.DirectoryNode) {
		d.TakerPaysCurrency = payCurr
		d.TakerPaysIssuer = payIssuer
		d.TakerGetsCurrency = getsCurr
		d.TakerGetsIssuer = getsIssuer
		d.ExchangeRate = quality
		d.DomainID = domainID
	}); derr != nil {
		t.Fatalf("dir insert: %v", derr)
	}
	return k.Key
}

// TestGetBookOffers_PermissionedOfferHiddenFromOpenBook verifies the
// domain-isolation behaviour: an offer placed only in a permissioned book
// (DomainID set, no hybrid AdditionalBookDirectory) must not surface in
// open-book queries, and conversely an open offer must not surface in
// a domain-scoped query. Mirrors rippled NetworkOPs.cpp:4443-4444 where
// getBookBase() embeds the domain in the directory hash.
func TestGetBookOffers_PermissionedOfferHiddenFromOpenBook(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x80)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	openOwnerAddr, _ := addressFromBytes(t, 0x82)
	insertAccountRoot(t, svc, openOwnerAddr, 1_000_000_000_000, 0)

	domOwnerAddr, _ := addressFromBytes(t, 0x84)
	insertAccountRoot(t, svc, domOwnerAddr, 1_000_000_000_000, 0)

	// Open offer.
	insertOffer(t, svc, openOwnerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	// Pure permissioned offer in the same currency pair.
	var domainID [32]byte
	for i := range domainID {
		domainID[i] = byte(0x90 + i)
	}
	insertPermissionedOffer(t, svc, domOwnerAddr, 2,
		state.NewIssuedAmountFromFloat64(200, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
		domainID,
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	// Open book: only the open offer.
	openResult, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10)
	if err != nil {
		t.Fatalf("open GetBookOffers: %v", err)
	}
	if len(openResult.Offers) != 1 {
		t.Fatalf("open book: expected 1 offer, got %d", len(openResult.Offers))
	}
	if openResult.Offers[0].Account != openOwnerAddr {
		t.Errorf("open book leaked a permissioned offer: account=%s", openResult.Offers[0].Account)
	}

	// Domain book: only the permissioned offer.
	domainHex := strings.ToUpper(hex.EncodeToString(domainID[:]))
	domResult, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", domainHex, "current", 10)
	if err != nil {
		t.Fatalf("domain GetBookOffers: %v", err)
	}
	if len(domResult.Offers) != 1 {
		t.Fatalf("domain book: expected 1 offer, got %d", len(domResult.Offers))
	}
	if domResult.Offers[0].Account != domOwnerAddr {
		t.Errorf("domain book returned wrong offer: account=%s", domResult.Offers[0].Account)
	}
}
