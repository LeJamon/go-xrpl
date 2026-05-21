package service

import (
	"encoding/binary"
	"testing"
	"time"

	"github.com/LeJamon/goXRPLd/drops"
	"github.com/LeJamon/goXRPLd/internal/ledger"
	"github.com/LeJamon/goXRPLd/internal/ledger/genesis"
	"github.com/LeJamon/goXRPLd/internal/ledger/state"
	"github.com/LeJamon/goXRPLd/internal/tx"
	"github.com/LeJamon/goXRPLd/keylet"
	"github.com/LeJamon/goXRPLd/protocol"
)

// TestApplyBookOfferFundingInfo_IssuerOwnedFullyFunded exercises rippled's
// "offer is issuing its own IOUs → always fully funded" branch
// (NetworkOPs.cpp:4516-4521): when the offer owner equals the takerGets
// issuer, owner_funds reports the offer's takerGets amount and no
// taker_*_funded fields are emitted because the offer is fully fundable.
//
// Also verifies the "first owner offer only" rule: owner_funds appears on
// the first offer per owner in directory order and is omitted on subsequent
// offers from the same owner.
func TestApplyBookOfferFundingInfo_IssuerOwnedFullyFunded(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)

	rawOffers := []*state.LedgerOffer{
		makeOffer(t, l, issuerAddr, 1,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr), // takerGets: 100 USD
			tx.NewXRPAmount(1_000_000_000), // takerPays: 1000 XRP
		),
		makeOffer(t, l, issuerAddr, 2,
			state.NewIssuedAmountFromValue(5_000_000_000_000_000, -14, "USD", issuerAddr), // takerGets: 50 USD
			tx.NewXRPAmount(600_000_000), // takerPays: 600 XRP
		),
	}
	offers := []BookOffer{
		{Account: issuerAddr},
		{Account: issuerAddr},
	}

	takerGets := state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr)
	takerPays := tx.NewXRPAmount(0)
	applyBookOfferFundingInfo(l, offers, rawOffers, takerGets, takerPays, "")

	if got := offers[0].OwnerFunds; got != "100" {
		t.Errorf("offers[0].OwnerFunds = %q, want %q", got, "100")
	}
	if offers[0].TakerGetsFunded != nil || offers[0].TakerPaysFunded != nil {
		t.Errorf("offers[0] should be fully funded: TakerGetsFunded=%v TakerPaysFunded=%v",
			offers[0].TakerGetsFunded, offers[0].TakerPaysFunded)
	}
	if got := offers[1].OwnerFunds; got != "" {
		t.Errorf("offers[1].OwnerFunds = %q, want empty (same owner as offers[0])", got)
	}
}

// TestApplyBookOfferFundingInfo_GlobalFreeze checks the
// global-freeze short-circuit (NetworkOPs.cpp:4522-4527): if either side
// of the book is globally frozen, third-party offers report owner_funds=0
// and emit taker_*_funded reflecting the zero availability.
func TestApplyBookOfferFundingInfo_GlobalFreeze(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)
	freezeIssuer(t, l, issuerAddr)

	_, thirdParty, err := genesis.GenerateAccountIDFromPassphrase("third-party-test-account")
	if err != nil {
		t.Fatalf("GenerateAccountIDFromPassphrase: %v", err)
	}
	rawOffers := []*state.LedgerOffer{
		makeOffer(t, l, thirdParty, 11,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr),
			tx.NewXRPAmount(1_000_000_000),
		),
	}
	offers := []BookOffer{{Account: thirdParty}}

	applyBookOfferFundingInfo(l, offers,
		rawOffers,
		state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr),
		tx.NewXRPAmount(0),
		"",
	)

	if got := offers[0].OwnerFunds; got != "0" {
		t.Errorf("global-freeze: OwnerFunds = %q, want %q", got, "0")
	}
	if offers[0].TakerGetsFunded == nil {
		t.Errorf("global-freeze: TakerGetsFunded should be emitted (zero availability)")
	}
}

// TestMultiplyByQuality verifies that the quality decoded from a
// BookDirectory key is correctly applied to derive taker_pays_funded.
// At quality 1:1 (saTakerPays / saTakerGets), 50 takerGets → 50 takerPays.
func TestMultiplyByQuality(t *testing.T) {
	var book [32]byte
	// Encode quality 1.0 = mantissa 10^15 with exponent -15 → STAmount stored as
	// (exponent+100)<<56 | mantissa.
	storedExp := uint64(-15 + 100)
	storedMant := uint64(1_000_000_000_000_000)
	binary.BigEndian.PutUint64(book[24:], (storedExp<<56)|storedMant)

	gets := tx.NewXRPAmount(50_000_000) // 50 XRP in drops
	template := tx.NewXRPAmount(0)

	out := multiplyByQuality(gets, book, template)
	if !out.IsNative() {
		t.Fatalf("expected XRP-typed result for XRP template, got IOU")
	}
	if got := out.Drops(); got != 50_000_000 {
		t.Errorf("multiplyByQuality(50 XRP, q=1) = %d drops, want 50_000_000", got)
	}
}

// --- test helpers ---

func newTestLedger(t *testing.T) *ledger.Ledger {
	t.Helper()
	g, err := genesis.Create(genesis.DefaultConfig())
	if err != nil {
		t.Fatalf("genesis.Create: %v", err)
	}
	parent := ledger.FromGenesis(g.Header, g.StateMap, g.TxMap, drops.Fees{})
	open, err := ledger.NewOpen(parent, time.Unix(1700000000, 0))
	if err != nil {
		t.Fatalf("ledger.NewOpen: %v", err)
	}
	return open
}

func genesisAddress(t *testing.T) string {
	t.Helper()
	_, addr, err := genesis.GenerateGenesisAccountID()
	if err != nil {
		t.Fatalf("GenerateGenesisAccountID: %v", err)
	}
	return addr
}

// makeOffer builds a LedgerOffer, serializes it, and inserts it into l. The
// returned *LedgerOffer is the in-memory shape used by
// applyBookOfferFundingInfo.
func makeOffer(t *testing.T, l *ledger.Ledger, account string, seq uint32, gets, pays tx.Amount) *state.LedgerOffer {
	t.Helper()
	offer := &state.LedgerOffer{
		Account:   account,
		Sequence:  seq,
		TakerGets: gets,
		TakerPays: pays,
	}
	// Encode quality (pays / gets) into the low 8 bytes of BookDirectory so
	// multiplyByQuality round-trips. For these tests we only need a stable,
	// non-zero quality — the actual sort order does not matter.
	binary.BigEndian.PutUint64(offer.BookDirectory[24:], uint64(protocol.QualityOne))

	accID, err := state.DecodeAccountID(account)
	if err != nil {
		t.Fatalf("DecodeAccountID(%q): %v", account, err)
	}
	k := keylet.Offer(accID, seq)
	data, err := state.SerializeLedgerOffer(offer)
	if err != nil {
		t.Fatalf("SerializeLedgerOffer: %v", err)
	}
	if err := l.Insert(k, data); err != nil {
		t.Fatalf("ledger.Insert: %v", err)
	}
	return offer
}

// freezeIssuer flips LsfGlobalFreeze on the given account so the
// applyBookOfferFundingInfo global-freeze branch fires.
func freezeIssuer(t *testing.T, l *ledger.Ledger, address string) {
	t.Helper()
	id, err := state.DecodeAccountID(address)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	k := keylet.Account(id)
	data, err := l.Read(k)
	if err != nil || data == nil {
		t.Fatalf("read AccountRoot: data=%v err=%v", data, err)
	}
	acc, err := state.ParseAccountRoot(data)
	if err != nil {
		t.Fatalf("ParseAccountRoot: %v", err)
	}
	acc.Flags |= state.LsfGlobalFreeze
	updated, err := state.SerializeAccountRoot(acc)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	if err := l.Update(k, updated); err != nil {
		t.Fatalf("ledger.Update: %v", err)
	}
}
