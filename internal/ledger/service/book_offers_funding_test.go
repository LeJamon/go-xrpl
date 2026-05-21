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

// TestApplyBookOfferFundingInfo_IssuerOwnedFullyFunded covers the
// "offer is selling issuer's own IOUs → always fully funded" branch
// (NetworkOPs.cpp:4516-4521). rippled never clears firstOwnerOffer in
// this branch, so owner_funds is emitted on every issuer-owned offer
// (even multiple offers from the same issuer in the same book).
func TestApplyBookOfferFundingInfo_IssuerOwnedFullyFunded(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)

	rawOffers := []*state.LedgerOffer{
		makeOffer(t, l, issuerAddr, 1,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr), // 100 USD
			tx.NewXRPAmount(1_000_000_000),
		),
		makeOffer(t, l, issuerAddr, 2,
			state.NewIssuedAmountFromValue(5_000_000_000_000_000, -14, "USD", issuerAddr), // 50 USD
			tx.NewXRPAmount(600_000_000),
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
	// rippled emits owner_funds on every issuer-owned offer — firstOwnerOffer
	// is only cleared in the default branch (NetworkOPs.cpp:4536), never here.
	if got := offers[1].OwnerFunds; got != "50" {
		t.Errorf("offers[1].OwnerFunds = %q, want %q (rippled emits on every issuer offer)", got, "50")
	}
}

// TestApplyBookOfferFundingInfo_GlobalFreeze checks the global-freeze
// short-circuit (NetworkOPs.cpp:4522-4527): third-party offers report
// owner_funds=0 and emit taker_*_funded reflecting the zero availability.
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

// TestApplyBookOfferFundingInfo_PartialFunding exercises the
// saOwnerFundsLimit < saTakerGets branch (NetworkOPs.cpp:4582-4594):
// owner has fewer funds than the offer's takerGets, so taker_gets_funded
// and taker_pays_funded are emitted.
func TestApplyBookOfferFundingInfo_PartialFunding(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)

	_, holder, err := genesis.GenerateAccountIDFromPassphrase("partial-funding-holder")
	if err != nil {
		t.Fatalf("GenerateAccountIDFromPassphrase: %v", err)
	}
	setTrustlineBalance(t, l, holder, issuerAddr, "USD", 40)

	rawOffers := []*state.LedgerOffer{
		makeOfferWithQuality(t, l, holder, 21,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr), // 100 USD
			tx.NewXRPAmount(1_000_000_000),
			uint64(protocol.QualityOne),
		),
	}
	offers := []BookOffer{{Account: holder}}

	applyBookOfferFundingInfo(l, offers, rawOffers,
		state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr),
		tx.NewXRPAmount(0),
		"",
	)

	if got := offers[0].OwnerFunds; got != "40" {
		t.Errorf("OwnerFunds = %q, want %q", got, "40")
	}
	if offers[0].TakerGetsFunded == nil {
		t.Errorf("partial: TakerGetsFunded should be emitted, was nil")
	}
	if offers[0].TakerPaysFunded == nil {
		t.Errorf("partial: TakerPaysFunded should be emitted, was nil")
	}
}

// TestApplyBookOfferFundingInfo_TransferRate covers the transfer-fee
// divide path (NetworkOPs.cpp:4565-4575). With rate=1.1 and saOwnerFunds=100,
// saOwnerFundsLimit = 100/1.1 ≈ 90.9, which is < saTakerGets=100 →
// partial funding is reported.
func TestApplyBookOfferFundingInfo_TransferRate(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)
	setTransferRate(t, l, issuerAddr, uint32(1.1*float64(protocol.QualityOne)))

	_, holder, err := genesis.GenerateAccountIDFromPassphrase("transfer-rate-holder")
	if err != nil {
		t.Fatalf("GenerateAccountIDFromPassphrase: %v", err)
	}
	setTrustlineBalance(t, l, holder, issuerAddr, "USD", 100)

	rawOffers := []*state.LedgerOffer{
		makeOfferWithQuality(t, l, holder, 31,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr), // 100 USD
			tx.NewXRPAmount(1_000_000_000),
			uint64(protocol.QualityOne),
		),
	}
	offers := []BookOffer{{Account: holder}}

	applyBookOfferFundingInfo(l, offers, rawOffers,
		state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr),
		tx.NewXRPAmount(0),
		"",
	)

	if got := offers[0].OwnerFunds; got != "100" {
		t.Errorf("OwnerFunds = %q, want %q (rippled emits raw saOwnerFunds, not the post-fee limit)", got, "100")
	}
	if offers[0].TakerGetsFunded == nil {
		t.Errorf("TakerGetsFunded should be emitted when 100/1.1 < 100")
	}
}

// TestApplyBookOfferFundingInfo_TakerIsIssuerSuppressesFee covers the
// uTakerID == book.out.account suppression (NetworkOPs.cpp:4567): when
// the requesting taker IS the takerGets issuer, the transfer-fee
// deduction is skipped and the offer reports as fully funded.
func TestApplyBookOfferFundingInfo_TakerIsIssuerSuppressesFee(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)
	setTransferRate(t, l, issuerAddr, uint32(1.1*float64(protocol.QualityOne)))

	_, holder, err := genesis.GenerateAccountIDFromPassphrase("taker-is-issuer-holder")
	if err != nil {
		t.Fatalf("GenerateAccountIDFromPassphrase: %v", err)
	}
	setTrustlineBalance(t, l, holder, issuerAddr, "USD", 100)

	rawOffers := []*state.LedgerOffer{
		makeOfferWithQuality(t, l, holder, 41,
			state.NewIssuedAmountFromValue(1_000_000_000_000_000, -13, "USD", issuerAddr),
			tx.NewXRPAmount(1_000_000_000),
			uint64(protocol.QualityOne),
		),
	}
	offers := []BookOffer{{Account: holder}}

	applyBookOfferFundingInfo(l, offers, rawOffers,
		state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr),
		tx.NewXRPAmount(0),
		issuerAddr,
	)

	if offers[0].TakerGetsFunded != nil || offers[0].TakerPaysFunded != nil {
		t.Errorf("takerIsIssuer should suppress transfer fee and report fully funded: TakerGetsFunded=%v TakerPaysFunded=%v",
			offers[0].TakerGetsFunded, offers[0].TakerPaysFunded)
	}
}

// TestApplyBookOfferFundingInfo_RunningBalance exercises the umBalance
// table (NetworkOPs.cpp:4530-4536, 4601). Two consecutive offers from
// the same non-issuer owner: the first uses the full trust-line balance;
// the second sees the post-deduction remainder. firstOwnerOffer is
// cleared on the umBalance hit so only the first emits owner_funds.
func TestApplyBookOfferFundingInfo_RunningBalance(t *testing.T) {
	l := newTestLedger(t)
	issuerAddr := genesisAddress(t)

	_, owner, err := genesis.GenerateAccountIDFromPassphrase("running-balance-owner")
	if err != nil {
		t.Fatalf("GenerateAccountIDFromPassphrase: %v", err)
	}
	setTrustlineBalance(t, l, owner, issuerAddr, "USD", 60)

	rawOffers := []*state.LedgerOffer{
		makeOfferWithQuality(t, l, owner, 51,
			state.NewIssuedAmountFromValue(4_000_000_000_000_000, -14, "USD", issuerAddr), // 40 USD
			tx.NewXRPAmount(400_000_000),
			uint64(protocol.QualityOne),
		),
		makeOfferWithQuality(t, l, owner, 52,
			state.NewIssuedAmountFromValue(3_000_000_000_000_000, -14, "USD", issuerAddr), // 30 USD
			tx.NewXRPAmount(300_000_000),
			uint64(protocol.QualityOne)+1,
		),
	}
	offers := []BookOffer{{Account: owner}, {Account: owner}}

	applyBookOfferFundingInfo(l, offers, rawOffers,
		state.NewIssuedAmountFromValue(0, 0, "USD", issuerAddr),
		tx.NewXRPAmount(0),
		"",
	)

	if got := offers[0].OwnerFunds; got != "60" {
		t.Errorf("offers[0].OwnerFunds = %q, want %q", got, "60")
	}
	if offers[0].TakerGetsFunded != nil {
		t.Errorf("offers[0] should be fully funded (40 ≤ 60): TakerGetsFunded=%v", offers[0].TakerGetsFunded)
	}

	if offers[1].OwnerFunds != "" {
		t.Errorf("offers[1].OwnerFunds should be empty (firstOwnerOffer cleared on umBalance hit), got %q", offers[1].OwnerFunds)
	}
	if offers[1].TakerGetsFunded == nil {
		t.Errorf("offers[1] should be partially funded (30 needed, 20 remaining): TakerGetsFunded was nil")
	}
}

// TestMultiplyByQuality verifies the BookDirectory→quality decode used
// to derive taker_pays_funded. Quality 1:1 → 50 takerGets maps to 50
// takerPays at the template's issue (XRP here).
func TestMultiplyByQuality(t *testing.T) {
	var book [32]byte
	storedExp := uint64(-15 + 100)
	storedMant := uint64(1_000_000_000_000_000)
	binary.BigEndian.PutUint64(book[24:], (storedExp<<56)|storedMant)

	gets := tx.NewXRPAmount(50_000_000)
	template := tx.NewXRPAmount(0)

	out := multiplyByQuality(gets, book, template)
	if !out.IsNative() {
		t.Fatalf("expected XRP-typed result for XRP template, got IOU")
	}
	if got := out.Drops(); got != 50_000_000 {
		t.Errorf("multiplyByQuality(50 XRP, q=1) = %d drops, want 50_000_000", got)
	}
}

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

func makeOffer(t *testing.T, l *ledger.Ledger, account string, seq uint32, gets, pays tx.Amount) *state.LedgerOffer {
	t.Helper()
	return makeOfferWithQuality(t, l, account, seq, gets, pays, uint64(protocol.QualityOne))
}

func makeOfferWithQuality(t *testing.T, l *ledger.Ledger, account string, seq uint32, gets, pays tx.Amount, quality uint64) *state.LedgerOffer {
	t.Helper()
	offer := &state.LedgerOffer{
		Account:   account,
		Sequence:  seq,
		TakerGets: gets,
		TakerPays: pays,
	}
	binary.BigEndian.PutUint64(offer.BookDirectory[24:], quality)

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

func freezeIssuer(t *testing.T, l *ledger.Ledger, address string) {
	t.Helper()
	updateAccountRoot(t, l, address, func(a *state.AccountRoot) {
		a.Flags |= state.LsfGlobalFreeze
	})
}

func setTransferRate(t *testing.T, l *ledger.Ledger, address string, rate uint32) {
	t.Helper()
	updateAccountRoot(t, l, address, func(a *state.AccountRoot) {
		a.TransferRate = rate
	})
}

func updateAccountRoot(t *testing.T, l *ledger.Ledger, address string, mut func(*state.AccountRoot)) {
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
	mut(acc)
	updated, err := state.SerializeAccountRoot(acc)
	if err != nil {
		t.Fatalf("SerializeAccountRoot: %v", err)
	}
	if err := l.Update(k, updated); err != nil {
		t.Fatalf("ledger.Update: %v", err)
	}
}

// setTrustlineBalance creates a RippleState SLE so that
// tx.AccountFunds(holder, {currency, issuer}, ...) reads back exactly
// `value` units. RippleState balance is from the low account's
// perspective; tx.AccountFunds negates when the holder is high
// (see internal/tx/utils.go:217-228), so we mirror that sign convention.
func setTrustlineBalance(t *testing.T, l *ledger.Ledger, holder, issuer, currency string, value int64) {
	t.Helper()
	holderID, err := state.DecodeAccountID(holder)
	if err != nil {
		t.Fatalf("DecodeAccountID holder: %v", err)
	}
	issuerID, err := state.DecodeAccountID(issuer)
	if err != nil {
		t.Fatalf("DecodeAccountID issuer: %v", err)
	}

	holderIsLow := state.CompareAccountIDsForLine(holderID, issuerID) < 0
	lowAddr, highAddr := holder, issuer
	if !holderIsLow {
		lowAddr, highAddr = issuer, holder
	}

	mantissa := value * 1_000_000_000_000_000
	if !holderIsLow {
		mantissa = -mantissa
	}
	balance := state.NewIssuedAmountFromValue(mantissa, -15, currency, state.AccountOneAddress)

	rs := &state.RippleState{
		Balance:   balance,
		LowLimit:  state.NewIssuedAmountFromValue(0, 0, currency, lowAddr),
		HighLimit: state.NewIssuedAmountFromValue(1_000_000_000_000_000, -9, currency, highAddr),
	}
	data, err := state.SerializeRippleState(rs)
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}
	k := keylet.Line(holderID, issuerID, currency)
	if err := l.Insert(k, data); err != nil {
		t.Fatalf("Insert trustline: %v", err)
	}
}
