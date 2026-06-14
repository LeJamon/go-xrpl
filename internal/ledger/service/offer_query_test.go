package service

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/codec/addresscodec"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/service/svcerr"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/keylet"
	"github.com/LeJamon/go-xrpl/ledger/entry"
	"github.com/LeJamon/go-xrpl/shamap"
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
	return insertAccountRootWithFlags(t, svc, addr, balanceDrops, transferRate, 0)
}

func insertAccountRootWithFlags(t *testing.T, svc *Service, addr string, balanceDrops uint64, transferRate, flags uint32) [20]byte {
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
		Flags:        flags,
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

// insertTrustLine creates a RippleState entry between owner and issuer for the
// given currency, with `ownerBalance` available to the owner. The sign convention
// follows rippled: Balance is positive when the LOW account holds the IOU.
func insertTrustLine(t *testing.T, svc *Service, ownerAddr, issuerAddr, currency, ownerBalance string) {
	t.Helper()
	_, ownerBytes, err := addresscodec.DecodeClassicAddressToAccountID(ownerAddr)
	if err != nil {
		t.Fatalf("decode owner: %v", err)
	}
	_, issuerBytes, err := addresscodec.DecodeClassicAddressToAccountID(issuerAddr)
	if err != nil {
		t.Fatalf("decode issuer: %v", err)
	}
	var ownerID, issuerID [20]byte
	copy(ownerID[:], ownerBytes)
	copy(issuerID[:], issuerBytes)

	ownerIsLow := state.CompareAccountIDs(ownerID, issuerID) < 0
	var lowAddr, highAddr string
	if ownerIsLow {
		lowAddr, highAddr = ownerAddr, issuerAddr
	} else {
		lowAddr, highAddr = issuerAddr, ownerAddr
	}

	// Balance issuer in RippleState is the magic ACCOUNT_ONE; the sign
	// encodes which side owns the IOU. When ownerIsLow=true, ownerBalance>0
	// means positive Balance; otherwise we negate.
	balanceValue := ownerBalance
	if !ownerIsLow {
		balanceValue = "-" + ownerBalance
	}
	balanceAmt, _ := state.NewIssuedAmountFromDecimalString(balanceValue, currency, state.AccountOneAddress)
	lowLimitAmt, _ := state.NewIssuedAmountFromDecimalString("0", currency, lowAddr)
	highLimitAmt, _ := state.NewIssuedAmountFromDecimalString("1000000000", currency, highAddr)
	rs := &state.RippleState{
		Balance:   balanceAmt,
		LowLimit:  lowLimitAmt,
		HighLimit: highLimitAmt,
	}
	data, err := state.SerializeRippleState(rs)
	if err != nil {
		t.Fatalf("serialize ripple state: %v", err)
	}
	if err := svc.openLedger.Insert(keylet.Line(ownerID, issuerID, currency), data); err != nil {
		t.Fatalf("insert trust line: %v", err)
	}
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
	payCurr := keylet.CurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := keylet.CurrencyBytes(takerGets.Currency)
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
	if _, derr := state.DirInsert(svc.openLedger, dirKeylet, k.Key, true, func(d *state.DirectoryNode) {
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

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
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

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
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

	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
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
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
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

	payCurr := keylet.CurrencyBytes(takerPays.Currency)
	payIssuer := state.GetIssuerBytes(takerPays.Issuer)
	getsCurr := keylet.CurrencyBytes(takerGets.Currency)
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
	if _, derr := state.DirInsert(svc.openLedger, dirKeylet, k.Key, true, func(d *state.DirectoryNode) {
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
	openResult, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
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
	domResult, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", domainHex, "current", 10, "", false)
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

// TestGetBookOffers_RunningBalanceConsumesAcrossOffers pins rippled's umBalance
// running-balance behaviour at NetworkOPs.cpp:4530-4537,4596-4601: a second
// offer from the same owner must see saOwnerFunds reduced by the first offer's
// saOwnerPays. The XRP-only setup lets us avoid trust lines while still
// exercising the default `umBalance` branch.
func TestGetBookOffers_RunningBalanceConsumesAcrossOffers(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0x90)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	ownerAddr, _ := addressFromBytes(t, 0x92)
	// Account balance just enough to cover one offer plus reserve: 10.6 XRP
	// after the 10 XRP base reserve leaves ~0.6 XRP liquid.
	insertAccountRoot(t, svc, ownerAddr, 10_600_000, 0)

	// Two offers from the same owner at different qualities. Each sells
	// 0.6 XRP; total is more than the liquid balance, so the second offer
	// must come back partially-funded.
	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(600_000),
	)
	insertOffer(t, svc, ownerAddr, 2,
		state.NewIssuedAmountFromFloat64(200, "USD", issuerAddr),
		tx.NewXRPAmount(600_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}

	first, second := result.Offers[0], result.Offers[1]
	if first.OwnerFunds == "" {
		t.Fatalf("first offer must carry owner_funds")
	}
	if second.OwnerFunds != "" {
		t.Errorf("second offer from same owner must omit owner_funds, got %q", second.OwnerFunds)
	}
	// First offer fully funded (0.6 XRP <= liquid 0.6 XRP).
	if first.TakerGetsFunded != nil || first.TakerPaysFunded != nil {
		t.Errorf("first offer should be fully funded, got gets=%v pays=%v",
			first.TakerGetsFunded, first.TakerPaysFunded)
	}
	// Second offer must emit both *_funded fields with reduced amounts.
	if second.TakerGetsFunded == nil || second.TakerPaysFunded == nil {
		t.Fatalf("second offer must emit both *_funded fields, got gets=%v pays=%v",
			second.TakerGetsFunded, second.TakerPaysFunded)
	}
	// The second offer's taker_gets_funded should be strictly less than 0.6 XRP
	// — running balance consumed by the first offer.
	gets, ok := second.TakerGetsFunded.(string)
	if !ok {
		t.Fatalf("taker_gets_funded should be a string drop count for XRP, got %T", second.TakerGetsFunded)
	}
	gotDrops, perr := strconv.ParseUint(gets, 10, 64)
	if perr != nil {
		t.Fatalf("parse taker_gets_funded: %v", perr)
	}
	if gotDrops >= 600_000 {
		t.Errorf("second offer taker_gets_funded (%d) should be < 600000 drops", gotDrops)
	}
}

// TestGetBookOffers_GlobalFreezeMarksAllUnfunded pins rippled
// NetworkOPs.cpp:4522-4527: when the issuer's account has the global-freeze
// flag set, every offer in the book is reported with owner_funds = "0" and
// both *_funded fields zeroed, AND firstOwnerOffer stays true so owner_funds
// is emitted on every offer (not just the first one per owner).
func TestGetBookOffers_GlobalFreezeMarksAllUnfunded(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xA0)
	insertAccountRootWithFlags(t, svc, issuerAddr, 1_000_000_000_000, 0, state.LsfGlobalFreeze)

	ownerAddr, _ := addressFromBytes(t, 0xA2)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	// Owner has plenty of USD on their trust line.
	insertTrustLine(t, svc, ownerAddr, issuerAddr, "USD", "1000")

	// Two offers from the same owner. Without freeze, the second would omit
	// owner_funds. With freeze, both must emit owner_funds = "0".
	insertOffer(t, svc, ownerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
	)
	insertOffer(t, svc, ownerAddr, 2,
		tx.NewXRPAmount(20_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}
	for i, o := range result.Offers {
		if o.OwnerFunds != "0" {
			t.Errorf("offer %d: global freeze must report owner_funds=\"0\", got %q", i, o.OwnerFunds)
		}
		if o.TakerGetsFunded == nil || o.TakerPaysFunded == nil {
			t.Errorf("offer %d: global freeze must emit *_funded fields (zeroed)", i)
		}
	}
}

// TestGetBookOffers_TransferRateAdjustsFunded pins the transfer-rate adjustment
// path in rippled NetworkOPs.cpp:4565-4575: when rate != parityRate AND the
// taker is not the issuer AND the offer owner is not the issuer, the limit on
// taker_gets_funded is saOwnerFunds / rate. A non-issuer owner with exactly
// enough balance for the face value of the offer must come back partially
// funded once the rate is applied.
func TestGetBookOffers_TransferRateAdjustsFunded(t *testing.T) {
	svc := newOfferTestService(t)

	// Issuer with 20% transfer fee (1.2e9 = 1.2 in rippled Rate semantics).
	issuerAddr, _ := addressFromBytes(t, 0xB0)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 1_200_000_000)

	ownerAddr, _ := addressFromBytes(t, 0xB2)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	// Owner has exactly the face value of the offer on its trust line.
	insertTrustLine(t, svc, ownerAddr, issuerAddr, "USD", "100")

	insertOffer(t, svc, ownerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if o.OwnerFunds == "" {
		t.Fatalf("owner_funds must be emitted on first offer")
	}
	if o.OwnerFunds != "100" {
		t.Errorf("owner_funds should equal trustline balance (100), got %q", o.OwnerFunds)
	}
	// With rate=1.2, ownerFundsLimit = 100/1.2 ≈ 83.33 < TakerGets(100),
	// so the offer must be reported partially funded.
	if o.TakerGetsFunded == nil || o.TakerPaysFunded == nil {
		t.Fatalf("transfer-rate adjustment must produce *_funded fields, got gets=%v pays=%v",
			o.TakerGetsFunded, o.TakerPaysFunded)
	}
}

// TestGetBookOffers_MarkerPagination walks a populated book in fixed-size
// pages and verifies the full set of offers is returned exactly once across
// the chain of (marker → next request) calls. This is a go-xrpl extension —
// rippled's NetworkOPsImp::getBookPage ignores its jvMarker parameter
// (NetworkOPs.cpp:4627) so there is no rippled fixture to mirror.
func TestGetBookOffers_MarkerPagination(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xA0)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0xB0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	// Distinct qualities so each offer lives in its own BookDirectory; the
	// page size below straddles directory boundaries.
	const totalOffers = 12
	for i := range totalOffers {
		insertOffer(t, svc, ownerAddr, uint32(i+1),
			state.NewIssuedAmountFromFloat64(float64(100+i), "USD", issuerAddr),
			tx.NewXRPAmount(10_000_000),
		)
	}

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	const pageSize uint32 = 5
	var collected []string
	marker := ""
	for page := range 10 {
		result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", pageSize, marker, false)
		if err != nil {
			t.Fatalf("page %d: GetBookOffers: %v", page, err)
		}
		if marker == "" && len(result.Offers) == 0 {
			t.Fatalf("first page must not be empty")
		}
		for _, o := range result.Offers {
			collected = append(collected, o.Index)
		}
		if result.Marker == "" {
			break
		}
		// The emitted marker must be the index of the last offer in the page.
		if last := result.Offers[len(result.Offers)-1].Index; result.Marker != last {
			t.Fatalf("page %d marker %q != last offer index %q", page, result.Marker, last)
		}
		marker = result.Marker
		if page == 9 {
			t.Fatalf("pagination did not terminate within 10 pages")
		}
	}

	if len(collected) != totalOffers {
		t.Fatalf("expected %d offers across all pages, got %d", totalOffers, len(collected))
	}
	seen := make(map[string]bool, len(collected))
	for _, idx := range collected {
		if seen[idx] {
			t.Fatalf("offer %s returned twice across pages", idx)
		}
		seen[idx] = true
	}
}

// TestGetBookOffers_MarkerInvalid covers markers that fail at the malformed /
// wrong-scope tier — they must map to ErrInvalidMarker (rippled's
// invalid_field_error("marker"), AccountOffers.cpp:107-121). The
// stale-marker tier is exercised separately in TestGetBookOffers_MarkerStale.
func TestGetBookOffers_MarkerInvalid(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0xC0)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0xD0)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	// Offer in a *different* book (USD/EUR) for the wrong-book case.
	eurOfferKey := insertOffer(t, svc, ownerAddr, 2,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		state.NewIssuedAmountFromFloat64(100, "EUR", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	cases := []struct {
		name   string
		marker string
	}{
		{"non-hex", strings.Repeat("Z", 64)},
		{"wrong-length", "DEADBEEF"},
		{"wrong-book", formatHashHex(eurOfferKey)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 5, tc.marker, false)
			if !errors.Is(err, svcerr.ErrInvalidMarker) {
				t.Fatalf("expected ErrInvalidMarker, got %v", err)
			}
			if errors.Is(err, svcerr.ErrStaleMarker) {
				t.Fatalf("malformed marker must not match ErrStaleMarker")
			}
		})
	}
}

// TestGetBookOffers_MarkerStale: a well-formed 64-hex marker that does not
// resolve to any offer in the ledger must surface as ErrStaleMarker, not
// ErrInvalidMarker — mirrors rippled's AccountOffers.cpp:128-132 "object
// pointed to by the marker does not exist" branch, which rippled returns
// as rpcINVALID_PARAMS rather than invalid_field_error.
func TestGetBookOffers_MarkerStale(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0xC4)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0xD4)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	_, err := svc.GetBookOffers(
		context.Background(), xrpModel, usd, "", "", "current", 5,
		strings.Repeat("0", 64), false,
	)
	if !errors.Is(err, svcerr.ErrStaleMarker) {
		t.Fatalf("unknown-key marker must surface as ErrStaleMarker, got %v", err)
	}
	if errors.Is(err, svcerr.ErrInvalidMarker) {
		t.Fatalf("stale marker must not match ErrInvalidMarker — handler distinguishes the two")
	}
}

// TestGetBookOffers_LimitZeroEmitsNoMarker: limit=0 is a legal request
// (rippled Tuning.h:49 declares bookOffers={0,60,100}) and must return
// zero offers with no marker. Without this guard, hitLimit flips true
// before any offer is recorded and the result carries a 64-zero marker
// that breaks the next paginated request.
func TestGetBookOffers_LimitZeroEmitsNoMarker(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0xC8)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0xD8)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 0, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers(limit=0): %v", err)
	}
	if len(result.Offers) != 0 {
		t.Fatalf("limit=0 must return zero offers, got %d", len(result.Offers))
	}
	if result.Marker != "" {
		t.Fatalf("limit=0 must not emit a marker, got %q", result.Marker)
	}
}

// TestGetBookOffers_HashFieldsAreUppercaseHex pins the wire shape of every
// [32]byte field emitted by book_offers: rippled's uint256::to_string returns
// a 64-char uppercase hex string, and clients parse it that way. This guards
// the formatHashHex consolidation done for issue #531 — a regression that
// leaks raw bytes through any of these fields would corrupt every JSON
// response that flows through GetBookOffers.
func TestGetBookOffers_HashFieldsAreUppercaseHex(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	sellerAddr, _ := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, sellerAddr, 1_000_000_000_000, 0)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)

	insertOffer(t, svc, sellerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}

	assertHash := func(name, v string) {
		t.Helper()
		if len(v) != 64 {
			t.Errorf("%s: want 64 hex chars, got %d (%q)", name, len(v), v)
			return
		}
		if _, err := hex.DecodeString(v); err != nil {
			t.Errorf("%s: not valid hex: %v (%q)", name, err, v)
		}
		if v != strings.ToUpper(v) {
			t.Errorf("%s: want uppercase hex, got %q", name, v)
		}
	}

	o := result.Offers[0]
	assertHash("index", o.Index)
	assertHash("BookDirectory", o.BookDirectory)
	assertHash("PreviousTxnID", o.PreviousTxnID)
}

// TestGetBookOffers_TransferRateSkippedWhenTakerIsIssuer pins the carve-out at
// rippled NetworkOPs.cpp:4569: `if (rate != parityRate && uTakerID !=
// book.out.account && book.out.account != uOfferOwnerID)`. When the taker IS
// the issuer of taker_gets, the transfer-fee adjustment must be skipped even
// for a non-issuer owner with a non-parity rate — `taker_gets_funded` stays
// equal to taker_gets.
func TestGetBookOffers_TransferRateSkippedWhenTakerIsIssuer(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xC0)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 1_200_000_000)

	ownerAddr, _ := addressFromBytes(t, 0xC2)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, ownerAddr, issuerAddr, "USD", "100")

	insertOffer(t, svc, ownerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, issuerAddr, "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if o.TakerGetsFunded != nil || o.TakerPaysFunded != nil {
		t.Errorf("taker==issuer must skip the rate adjustment, got gets=%v pays=%v",
			o.TakerGetsFunded, o.TakerPaysFunded)
	}
}

// TestGetBookOffers_TransferRateAcrossOwners covers the multi-issuer angle: two
// distinct non-issuer owners, each with a balance equal to the offer face
// value, both reduced by the same issuer transfer rate. The running-balance
// map only suppresses owner_funds on a second offer from the SAME owner, so
// both owners must surface owner_funds AND both must be reported partially
// funded.
func TestGetBookOffers_TransferRateAcrossOwners(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xD0)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 1_200_000_000)

	owner1Addr, _ := addressFromBytes(t, 0xD2)
	insertAccountRoot(t, svc, owner1Addr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, owner1Addr, issuerAddr, "USD", "100")

	owner2Addr, _ := addressFromBytes(t, 0xD4)
	insertAccountRoot(t, svc, owner2Addr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, owner2Addr, issuerAddr, "USD", "100")

	insertOffer(t, svc, owner1Addr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)
	insertOffer(t, svc, owner2Addr, 1,
		tx.NewXRPAmount(20_000_000),
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	// Pass a third-party taker so the issuer-skip carve-out doesn't kick in.
	thirdParty, _ := addressFromBytes(t, 0xDE)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, thirdParty, "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 2 {
		t.Fatalf("expected 2 offers, got %d", len(result.Offers))
	}
	for i, o := range result.Offers {
		if o.OwnerFunds == "" {
			t.Errorf("offer %d: distinct owners must each emit owner_funds, got empty", i)
		}
		if o.TakerGetsFunded == nil || o.TakerPaysFunded == nil {
			t.Errorf("offer %d: rate-adjusted offer must be reported partially funded", i)
		}
	}
}

// TestGetBookOffers_BothSidesFrozen pins the `||` in
// `bGlobalFreeze := IsGlobalFrozen(takerGets.Issuer) || IsGlobalFrozen(takerPays.Issuer)`
// (offer_query.go:85-86). Freezing the takerPays issuer must also trip the
// freeze branch even though the offer's owner has plenty of takerGets.
func TestGetBookOffers_BothSidesFrozen(t *testing.T) {
	svc := newOfferTestService(t)

	getsIssuerAddr, _ := addressFromBytes(t, 0xE0)
	insertAccountRoot(t, svc, getsIssuerAddr, 1_000_000_000_000, 0)
	paysIssuerAddr, _ := addressFromBytes(t, 0xE2)
	insertAccountRootWithFlags(t, svc, paysIssuerAddr, 1_000_000_000_000, 0, state.LsfGlobalFreeze)

	ownerAddr, _ := addressFromBytes(t, 0xE4)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, ownerAddr, getsIssuerAddr, "USD", "1000")

	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(50, "EUR", paysIssuerAddr),
		state.NewIssuedAmountFromFloat64(100, "USD", getsIssuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", getsIssuerAddr)
	eur := state.NewIssuedAmountFromFloat64(0, "EUR", paysIssuerAddr)
	result, err := svc.GetBookOffers(context.Background(), usd, eur, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if o.OwnerFunds != "0" {
		t.Errorf("pays-side global freeze must report owner_funds=\"0\", got %q", o.OwnerFunds)
	}
	if o.TakerGetsFunded == nil || o.TakerPaysFunded == nil {
		t.Errorf("pays-side global freeze must emit zeroed *_funded fields")
	}
}

// TestGetBookOffers_FrozenIssuerDistinctFromOwner verifies the freeze branch
// when the offer owner is NOT the frozen issuer — the third party with a
// healthy USD trustline must still be reported unfunded because the issuer of
// taker_gets is globally frozen. This pins NetworkOPs.cpp:4522-4527 against a
// regression that might key the freeze branch off the owner instead of the
// issuer.
func TestGetBookOffers_FrozenIssuerDistinctFromOwner(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xF0)
	insertAccountRootWithFlags(t, svc, issuerAddr, 1_000_000_000_000, 0, state.LsfGlobalFreeze)

	ownerAddr, _ := addressFromBytes(t, 0xF2)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, ownerAddr, issuerAddr, "USD", "1000")

	insertOffer(t, svc, ownerAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if o.Account != ownerAddr {
		t.Fatalf("unexpected offer owner %s", o.Account)
	}
	if o.OwnerFunds != "0" {
		t.Errorf("frozen issuer (distinct from owner) must report owner_funds=\"0\", got %q", o.OwnerFunds)
	}
}

// TestGetBookOffers_MultiOwnerDrainsTrustline mirrors rippled's nFundedFunded
// scenario: two distinct non-issuer owners both holding the same issuer's
// trust line, each placing offers in the same book. Owners are independent
// for funding purposes — running-balance suppression is keyed on owner, so
// each owner reports its own owner_funds independently, and each owner's
// second offer reuses the running balance from its first.
func TestGetBookOffers_MultiOwnerDrainsTrustline(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, _ := addressFromBytes(t, 0xA8)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	aliceAddr, _ := addressFromBytes(t, 0xAA)
	insertAccountRoot(t, svc, aliceAddr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, aliceAddr, issuerAddr, "USD", "60")

	bobAddr, _ := addressFromBytes(t, 0xAC)
	insertAccountRoot(t, svc, bobAddr, 1_000_000_000_000, 0)
	insertTrustLine(t, svc, bobAddr, issuerAddr, "USD", "30")

	// Alice: two offers selling 50 USD then 20 USD — total > balance so the
	// second offer must come back partially funded.
	insertOffer(t, svc, aliceAddr, 1,
		tx.NewXRPAmount(10_000_000),
		state.NewIssuedAmountFromFloat64(50, "USD", issuerAddr),
	)
	insertOffer(t, svc, aliceAddr, 2,
		tx.NewXRPAmount(20_000_000),
		state.NewIssuedAmountFromFloat64(20, "USD", issuerAddr),
	)
	// Bob: one fully-funded offer at a different price.
	insertOffer(t, svc, bobAddr, 1,
		tx.NewXRPAmount(15_000_000),
		state.NewIssuedAmountFromFloat64(25, "USD", issuerAddr),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), usd, xrpModel, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 3 {
		t.Fatalf("expected 3 offers, got %d", len(result.Offers))
	}

	var aliceOffers, bobOffers []BookOffer
	for _, o := range result.Offers {
		switch o.Account {
		case aliceAddr:
			aliceOffers = append(aliceOffers, o)
		case bobAddr:
			bobOffers = append(bobOffers, o)
		}
	}
	if len(aliceOffers) != 2 || len(bobOffers) != 1 {
		t.Fatalf("offer attribution mismatch: alice=%d bob=%d", len(aliceOffers), len(bobOffers))
	}
	// Alice's first offer (best price): owner_funds present, fully funded.
	if aliceOffers[0].OwnerFunds == "" {
		t.Errorf("alice[0] should emit owner_funds, got empty")
	}
	if aliceOffers[0].TakerGetsFunded != nil {
		t.Errorf("alice[0] should be fully funded, got gets_funded=%v", aliceOffers[0].TakerGetsFunded)
	}
	// Alice's second offer: owner_funds omitted (running balance), partially
	// funded (60 USD trust line - 50 USD consumed = 10 USD left for a 20 USD offer).
	if aliceOffers[1].OwnerFunds != "" {
		t.Errorf("alice[1] should omit owner_funds (running balance), got %q", aliceOffers[1].OwnerFunds)
	}
	if aliceOffers[1].TakerGetsFunded == nil || aliceOffers[1].TakerPaysFunded == nil {
		t.Errorf("alice[1] must report partial funding, got gets=%v pays=%v",
			aliceOffers[1].TakerGetsFunded, aliceOffers[1].TakerPaysFunded)
	}
	// Bob's offer: owner_funds present (different owner), fully funded against
	// his own 30 USD trustline.
	if bobOffers[0].OwnerFunds == "" {
		t.Errorf("bob's offer should emit owner_funds (distinct owner), got empty")
	}
	if bobOffers[0].TakerGetsFunded != nil {
		t.Errorf("bob's 25 USD offer is below his 30 USD trustline; expected fully funded, got gets_funded=%v",
			bobOffers[0].TakerGetsFunded)
	}
}

// TestGetBookOffers_WithProofs_VerifiesAgainstAccountHash pins that
// withProofs=true emits a SHAMap proof per offer that verifies against
// the queried ledger's state-map root (account_hash).
func TestGetBookOffers_WithProofs_VerifiesAgainstAccountHash(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x80)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x81)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	offerKey := insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", true)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	o := result.Offers[0]
	if len(o.Proof) == 0 {
		t.Fatalf("withProofs=true must populate Proof, got empty slice")
	}

	snap, err := svc.openLedger.StateMapSnapshot()
	if err != nil {
		t.Fatalf("snapshot state map: %v", err)
	}
	rootHash, err := snap.Hash()
	if err != nil {
		t.Fatalf("state map hash: %v", err)
	}

	rawNodes := make([][]byte, len(o.Proof))
	for i, h := range o.Proof {
		if strings.ToUpper(h) != h {
			t.Errorf("proof node %d not upper-case hex: %q", i, h)
		}
		b, derr := hex.DecodeString(h)
		if derr != nil {
			t.Fatalf("hex decode proof node %d: %v", i, derr)
		}
		rawNodes[i] = b
	}
	if !shamap.VerifyProofPath(rootHash, offerKey, rawNodes) {
		t.Fatalf("proof for offer %s does not verify against account_hash %x",
			hex.EncodeToString(offerKey[:]), rootHash)
	}
}

// TestGetBookOffers_WithProofsFalse_OmitsProof pins that the default
// (withProofs=false) request never sets the Proof field, so the JSON
// response keeps `proof` omitted via the BookOffer `omitempty` tag.
func TestGetBookOffers_WithProofsFalse_OmitsProof(t *testing.T) {
	svc := newOfferTestService(t)
	issuerAddr, _ := addressFromBytes(t, 0x90)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)
	ownerAddr, _ := addressFromBytes(t, 0x91)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)
	insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)

	usd := state.NewIssuedAmountFromFloat64(0, "USD", issuerAddr)
	xrpModel := tx.NewXRPAmount(0)
	result, err := svc.GetBookOffers(context.Background(), xrpModel, usd, "", "", "current", 10, "", false)
	if err != nil {
		t.Fatalf("GetBookOffers: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	if len(result.Offers[0].Proof) != 0 {
		t.Fatalf("withProofs=false must omit proof, got %v", result.Offers[0].Proof)
	}
}

// TestGetOwnerInfo_WalksOwnerDirectory drives owner_info through the real
// service: it walks the account's owner directory, groups offers and trust
// lines, and confirms each object's raw bytes round-trip through
// binarycodec.Decode (the same decode the handler performs). This covers the
// live decode path and "current" ledger resolution that the handler-level
// mocks cannot exercise. Mirrors rippled NetworkOPsImp::getOwnerInfo.
func TestGetOwnerInfo_WalksOwnerDirectory(t *testing.T) {
	svc := newOfferTestService(t)

	issuerAddr, issuerID := addressFromBytes(t, 0x10)
	insertAccountRoot(t, svc, issuerAddr, 1_000_000_000_000, 0)

	ownerAddr, ownerID := addressFromBytes(t, 0x20)
	insertAccountRoot(t, svc, ownerAddr, 1_000_000_000_000, 0)

	offerKey := insertOffer(t, svc, ownerAddr, 1,
		state.NewIssuedAmountFromFloat64(100, "USD", issuerAddr),
		tx.NewXRPAmount(10_000_000),
	)
	insertTrustLine(t, svc, ownerAddr, issuerAddr, "USD", "500")
	lineKey := keylet.Line(ownerID, issuerID, "USD").Key

	// Link both objects into the owner directory, mirroring dirAdd in the
	// rippled apply path — owner_info walks this directory, not the book.
	ownerDir := keylet.OwnerDir(ownerID)
	for _, k := range [][32]byte{offerKey, lineKey} {
		if _, err := state.DirInsert(svc.openLedger, ownerDir, k, false, nil); err != nil {
			t.Fatalf("owner dir insert: %v", err)
		}
	}

	result, err := svc.GetOwnerInfo(context.Background(), ownerAddr, "current")
	if err != nil {
		t.Fatalf("GetOwnerInfo: %v", err)
	}
	if len(result.Offers) != 1 {
		t.Fatalf("expected 1 offer, got %d", len(result.Offers))
	}
	if len(result.RippleLines) != 1 {
		t.Fatalf("expected 1 ripple_line, got %d", len(result.RippleLines))
	}

	off := result.Offers[0]
	if off.LedgerEntryType != "Offer" {
		t.Errorf("offer entry type = %q, want Offer", off.LedgerEntryType)
	}
	if want := strings.ToUpper(hex.EncodeToString(offerKey[:])); off.Index != want {
		t.Errorf("offer index = %q, want %q", off.Index, want)
	}
	decoded, derr := binarycodec.Decode(hex.EncodeToString(off.Data))
	if derr != nil {
		t.Fatalf("offer data must round-trip through binarycodec.Decode: %v", derr)
	}
	if decoded["LedgerEntryType"] != "Offer" {
		t.Errorf("decoded offer LedgerEntryType = %v, want Offer", decoded["LedgerEntryType"])
	}
	if decoded["Account"] != ownerAddr {
		t.Errorf("decoded offer Account = %v, want %s", decoded["Account"], ownerAddr)
	}

	line := result.RippleLines[0]
	if line.LedgerEntryType != "RippleState" {
		t.Errorf("line entry type = %q, want RippleState", line.LedgerEntryType)
	}
	if _, derr := binarycodec.Decode(hex.EncodeToString(line.Data)); derr != nil {
		t.Fatalf("ripple_state data must round-trip through binarycodec.Decode: %v", derr)
	}

	// An account with no owner directory yields empty groups (no error),
	// matching rippled's empty result for an account that owns nothing.
	strangerAddr, _ := addressFromBytes(t, 0x99)
	empty, err := svc.GetOwnerInfo(context.Background(), strangerAddr, "current")
	if err != nil {
		t.Fatalf("GetOwnerInfo(stranger): %v", err)
	}
	if len(empty.Offers) != 0 || len(empty.RippleLines) != 0 {
		t.Fatalf("account with no owner dir must yield empty groups, got offers=%d lines=%d",
			len(empty.Offers), len(empty.RippleLines))
	}
}
