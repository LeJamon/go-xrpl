package invariants

// highrisk_test.go — dedicated trip/satisfy tests for the six high-risk
// invariants that were previously only registered in CheckInvariants but never
// exercised by a unit test: TransfersNotFrozen, ValidAMM (semantic finalize
// paths), ValidClawback, ValidMPTIssuance, ValidNFTokenPage and
// ValidPermissionedDEX. Reference: issue #623.

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// Distinct, round-trippable test addresses (re-used across the file).
const (
	addrIssuer  = "rPMh7Pi9ct699iZUTWaytJUoHcJ7cgyziK"
	addrHolderA = "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"
	addrHolderB = "rG1QQv2nh2gr7RCZ1P8YYcBUKCCN633jCn"
)

// mustEncode encodes a JSON-shaped SLE map to its binary form via the codec.
func mustEncode(t *testing.T, obj map[string]any) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(obj)
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return b
}

// acctEntry builds an AccountRoot InvariantEntry (used to seed possible
// issuers for the frozen-transfer checker).
func acctEntry(t *testing.T, addr string, flags uint32) InvariantEntry {
	t.Helper()
	b := mustSerializeAccount(t, &state.AccountRoot{
		Account: addr, Balance: 1_000_000, Sequence: 1, Flags: flags,
	})
	return InvariantEntry{EntryType: "AccountRoot", After: b}
}

// frozenLine builds a RippleState InvariantEntry between low and high accounts
// whose Balance moves from beforeVal to afterVal (in the low account's terms).
func frozenLine(t *testing.T, low, high, beforeVal, afterVal string, flags uint32) InvariantEntry {
	t.Helper()
	mk := func(val string) []byte {
		balanceAmt, _ := state.NewIssuedAmountFromDecimalString(val, "USD", state.AccountOneAddress)
		lowLimitAmt, _ := state.NewIssuedAmountFromDecimalString("1000", "USD", low)
		highLimitAmt, _ := state.NewIssuedAmountFromDecimalString("1000", "USD", high)
		rs := &state.RippleState{
			Balance:   balanceAmt,
			LowLimit:  lowLimitAmt,
			HighLimit: highLimitAmt,
			Flags:     flags,
		}
		b, err := state.SerializeRippleState(rs)
		if err != nil {
			t.Fatalf("SerializeRippleState: %v", err)
		}
		return b
	}
	return InvariantEntry{EntryType: "RippleState", Before: mk(beforeVal), After: mk(afterVal)}
}

// frozenTransferEntries models a USD transfer routed through addrIssuer: holder
// A sends (balance 10→5) and holder B receives (balance 10→15). The issuer
// carries issuerFlags so callers can toggle a global freeze.
func frozenTransferEntries(t *testing.T, issuerFlags uint32) []InvariantEntry {
	t.Helper()
	return []InvariantEntry{
		acctEntry(t, addrIssuer, issuerFlags),
		acctEntry(t, addrHolderA, 0),
		acctEntry(t, addrHolderB, 0),
		frozenLine(t, addrHolderA, addrIssuer, "10", "5", 0),
		frozenLine(t, addrHolderB, addrIssuer, "10", "15", 0),
	}
}

// TestTransfersNotFrozen_GlobalFreeze: funds flowing through a globally frozen
// issuer (both a sender and a receiver on its currency) must trip
// TransfersNotFrozen once featureDeepFreeze is enabled.
// Reference: rippled InvariantCheck.cpp TransfersNotFrozen (lines 652-926).
func TestTransfersNotFrozen_GlobalFreeze(t *testing.T) {
	rules := amendment.AllSupportedRules() // DeepFreeze enabled → enforced
	tx := stubTx{txType: TypePayment}

	frozen := frozenTransferEntries(t, state.LsfGlobalFreeze)
	if v := checkTransfersNotFrozen(tx, frozen, stubView{}, rules); v == nil {
		t.Fatal("expected TransfersNotFrozen violation for transfer through a globally frozen issuer")
	} else if v.Name != "TransfersNotFrozen" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	// Same flows, issuer not frozen → permitted.
	unfrozen := frozenTransferEntries(t, 0)
	if v := checkTransfersNotFrozen(tx, unfrozen, stubView{}, rules); v != nil {
		t.Fatalf("unfrozen issuer: unexpected violation %v", v)
	}
}

// TestTransfersNotFrozen_AmendmentGate: with featureDeepFreeze disabled the
// checker never enforces, even for a globally frozen issuer.
// Reference: rippled InvariantCheck.cpp lines 706-707.
func TestTransfersNotFrozen_AmendmentGate(t *testing.T) {
	rules := amendment.EmptyRules() // DeepFreeze disabled → not enforced
	tx := stubTx{txType: TypePayment}

	frozen := frozenTransferEntries(t, state.LsfGlobalFreeze)
	if v := checkTransfersNotFrozen(tx, frozen, stubView{}, rules); v != nil {
		t.Fatalf("deep freeze disabled: unexpected violation %v", v)
	}
}

// ammSLE builds an AMM ledger entry carrying the two soeREQUIRED fields the
// invariant reads: Account and LPTokenBalance.
func ammSLE(t *testing.T, account, lptValue string) []byte {
	t.Helper()
	return mustEncode(t, map[string]any{
		"LedgerEntryType": "AMM",
		"Account":         account,
		"LPTokenBalance": map[string]any{
			"currency": "USD", "issuer": account, "value": lptValue,
		},
	})
}

// TestValidAMM_VoteMustNotChangePool: AMMVote may not move LP tokens or the
// pool; a changed LPTokenBalance must trip ValidAMM under fixAMMv1_3.
// Reference: rippled InvariantCheck.cpp finalizeVote (lines 1774-1790).
func TestValidAMM_VoteMustNotChangePool(t *testing.T) {
	rules := amendment.AllSupportedRules() // fixAMMv1_3 enabled → enforced
	tx := stubTx{txType: TypeAMMVote}

	changed := []InvariantEntry{{
		EntryType: "AMM",
		Before:    ammSLE(t, addrIssuer, "1000"),
		After:     ammSLE(t, addrIssuer, "2000"),
	}}
	if v := checkValidAMM(tx, TesSUCCESS, changed, stubView{}, rules); v == nil {
		t.Fatal("expected ValidAMM violation: AMMVote changed LP tokens")
	} else if v.Name != "ValidAMM" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	unchanged := []InvariantEntry{{
		EntryType: "AMM",
		Before:    ammSLE(t, addrIssuer, "1000"),
		After:     ammSLE(t, addrIssuer, "1000"),
	}}
	if v := checkValidAMM(tx, TesSUCCESS, unchanged, stubView{}, rules); v != nil {
		t.Fatalf("AMMVote with unchanged LP tokens: unexpected violation %v", v)
	}

	// Amendment gate: with fixAMMv1_3 off the same change is not enforced.
	if v := checkValidAMM(tx, TesSUCCESS, changed, stubView{}, amendment.EmptyRules()); v != nil {
		t.Fatalf("fixAMMv1_3 disabled: unexpected violation %v", v)
	}
}

// TestValidAMM_VoteRejectsLPTokenIssueChange: an LP token whose numeric value is
// unchanged but whose issue (currency/issuer) changed must still trip ValidAMM —
// rippled's STAmount != compares the issue, not just the magnitude.
// Reference: rippled InvariantCheck.cpp finalizeVote (line 1776).
func TestValidAMM_VoteRejectsLPTokenIssueChange(t *testing.T) {
	rules := amendment.AllSupportedRules()
	tx := stubTx{txType: TypeAMMVote}

	before := ammSLE(t, addrIssuer, "1000")
	after := mustEncode(t, map[string]any{
		"LedgerEntryType": "AMM",
		"Account":         addrIssuer,
		"LPTokenBalance": map[string]any{
			"currency": "USD", "issuer": addrHolderA, "value": "1000",
		},
	})
	changed := []InvariantEntry{{EntryType: "AMM", Before: before, After: after}}
	if v := checkValidAMM(tx, TesSUCCESS, changed, stubView{}, rules); v == nil {
		t.Fatal("expected ValidAMM violation: LP token issue changed at constant value")
	} else if v.Name != "ValidAMM" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestValidAMM_DeleteMustRemoveObject: a successful AMMDelete that leaves the
// AMM object behind must trip ValidAMM; a delete that removes it satisfies.
// Reference: rippled InvariantCheck.cpp finalizeDelete (lines 1864-1880).
func TestValidAMM_DeleteMustRemoveObject(t *testing.T) {
	rules := amendment.AllSupportedRules()
	tx := stubTx{txType: TypeAMMDelete}

	stillThere := []InvariantEntry{{EntryType: "AMM", After: ammSLE(t, addrIssuer, "1000")}}
	if v := checkValidAMM(tx, TesSUCCESS, stillThere, stubView{}, rules); v == nil {
		t.Fatal("expected ValidAMM violation: AMM object not deleted on AMMDelete")
	} else if v.Name != "ValidAMM" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	// AMM properly removed: the delete entry is skipped in visitEntry, so no
	// AMM account is observed and the invariant is satisfied.
	removed := []InvariantEntry{{
		EntryType: "AMM",
		Before:    ammSLE(t, addrIssuer, "1000"),
		IsDelete:  true,
	}}
	if v := checkValidAMM(tx, TesSUCCESS, removed, stubView{}, rules); v != nil {
		t.Fatalf("AMM deleted cleanly: unexpected violation %v", v)
	}
}

// clawbackTx is a Clawback transaction stub exposing Account and Amount so the
// holder-balance branch of ValidClawback can run.
type clawbackTx struct {
	account string
	amount  Amount
}

func (t clawbackTx) TxType() TxType                   { return TypeClawback }
func (t clawbackTx) TxAccount() string                { return t.account }
func (t clawbackTx) TxHasField(string) bool           { return false }
func (t clawbackTx) Flatten() (map[string]any, error) { return map[string]any{}, nil }
func (t clawbackTx) ClawbackAmount() Amount           { return t.amount }

// lineView returns the same trust-line bytes for every Read, enough for the
// single accountHolds() lookup ValidClawback performs.
type lineView struct {
	stubView
	line []byte
}

func (v lineView) Read(k keylet.Keylet) ([]byte, error) { return v.line, nil }

// TestValidClawback_TooManyEntries: on success a Clawback may touch at most one
// trust line and one MPToken; more than one of either trips ValidClawback.
// Reference: rippled InvariantCheck.cpp ValidClawback (lines 1288-1362).
func TestValidClawback_TooManyEntries(t *testing.T) {
	nonNil := []byte{0x01}
	tx := stubTx{txType: TypeClawback}

	twoLines := []InvariantEntry{
		{EntryType: "RippleState", Before: nonNil},
		{EntryType: "RippleState", Before: nonNil},
	}
	if v := checkValidClawback(tx, TesSUCCESS, twoLines, stubView{}); v == nil {
		t.Fatal("expected ValidClawback violation: more than one trustline changed")
	} else if v.Name != "ValidClawback" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	twoMPTokens := []InvariantEntry{
		{EntryType: "MPToken", Before: nonNil},
		{EntryType: "MPToken", Before: nonNil},
	}
	if v := checkValidClawback(tx, TesSUCCESS, twoMPTokens, stubView{}); v == nil {
		t.Fatal("expected ValidClawback violation: more than one mptoken changed")
	}

	// Exactly one trust line and no Amount provider: the ==1 branch skips the
	// balance check and the invariant is satisfied.
	oneLine := []InvariantEntry{{EntryType: "RippleState", Before: nonNil}}
	if v := checkValidClawback(tx, TesSUCCESS, oneLine, stubView{}); v != nil {
		t.Fatalf("single trustline: unexpected violation %v", v)
	}
}

// TestValidClawback_ChangesOnFailure: a failed Clawback must not have modified
// any trust line or MPToken.
// Reference: rippled InvariantCheck.cpp lines 1344-1361.
func TestValidClawback_ChangesOnFailure(t *testing.T) {
	nonNil := []byte{0x01}
	tx := stubTx{txType: TypeClawback}
	const failure Result = 100 // any non-tesSUCCESS result

	changed := []InvariantEntry{{EntryType: "RippleState", Before: nonNil}}
	if v := checkValidClawback(tx, failure, changed, stubView{}); v == nil {
		t.Fatal("expected ValidClawback violation: trustline changed despite failure")
	}

	if v := checkValidClawback(tx, failure, nil, stubView{}); v != nil {
		t.Fatalf("failed clawback with no changes: unexpected violation %v", v)
	}
}

// TestValidClawback_HolderBalanceSign: when exactly one trust line changed, the
// holder's post-clawback balance must be non-negative.
// Reference: rippled InvariantCheck.cpp lines 1328-1342 (accountHolds()).
func TestValidClawback_HolderBalanceSign(t *testing.T) {
	holderID, err := state.DecodeAccountID(addrHolderA)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	issuerID, err := state.DecodeAccountID(addrIssuer)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}

	// line builds a trust line whose holder-terms balance has the requested
	// sign. accountHolds negates the stored balance when holder > issuer, so we
	// pre-negate to land on the intended sign regardless of address ordering.
	line := func(negative bool) []byte {
		bal, _ := state.NewIssuedAmountFromDecimalString("5", "USD", state.AccountOneAddress)
		holderTermsNegated := state.CompareAccountIDs(holderID, issuerID) > 0
		if negative != holderTermsNegated {
			bal = bal.Negate()
		}
		lowLimitAmt, _ := state.NewIssuedAmountFromDecimalString("0", "USD", addrHolderA)
		highLimitAmt, _ := state.NewIssuedAmountFromDecimalString("0", "USD", addrIssuer)
		rs := &state.RippleState{
			Balance:   bal,
			LowLimit:  lowLimitAmt,
			HighLimit: highLimitAmt,
		}
		b, err := state.SerializeRippleState(rs)
		if err != nil {
			t.Fatalf("SerializeRippleState: %v", err)
		}
		return b
	}

	amount, _ := state.NewIssuedAmountFromDecimalString("1", "USD", addrHolderA)
	tx := clawbackTx{
		account: addrIssuer,
		amount:  amount,
	}
	entries := func(b []byte) []InvariantEntry {
		return []InvariantEntry{{EntryType: "RippleState", Before: b, After: b}}
	}

	neg := line(true)
	if v := checkValidClawback(tx, TesSUCCESS, entries(neg), lineView{line: neg}); v == nil {
		t.Fatal("expected ValidClawback violation: negative holder balance")
	} else if v.Name != "ValidClawback" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	pos := line(false)
	if v := checkValidClawback(tx, TesSUCCESS, entries(pos), lineView{line: pos}); v != nil {
		t.Fatalf("non-negative holder balance: unexpected violation %v", v)
	}
}

// holderTx is an MPTokenAuthorize stub whose HasHolder reports whether the
// issuer (Holder field present) or the holder submitted the transaction.
type holderTx struct {
	stubTx
	hasHolder bool
}

func (t holderTx) HasHolder() bool { return t.hasHolder }

// TestValidMPTIssuance_CreateAndDestroy: a successful MPTokenIssuanceCreate must
// create exactly one issuance; destroy must delete exactly one.
// Reference: rippled InvariantCheck.cpp ValidMPTIssuance (lines 1366-1534).
func TestValidMPTIssuance_CreateAndDestroy(t *testing.T) {
	nonNil := []byte{0x01}
	created := InvariantEntry{EntryType: "MPTokenIssuance", After: nonNil}
	deleted := InvariantEntry{EntryType: "MPTokenIssuance", Before: nonNil, IsDelete: true}

	create := stubTx{txType: TypeMPTokenIssuanceCreate}
	if v := checkValidMPTIssuance(create, TesSUCCESS, []InvariantEntry{created}); v != nil {
		t.Fatalf("create exactly one issuance: unexpected violation %v", v)
	}
	if v := checkValidMPTIssuance(create, TesSUCCESS, []InvariantEntry{created, created}); v == nil {
		t.Fatal("expected ValidMPTIssuance violation: created two issuances")
	} else if v.Name != "ValidMPTIssuance" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	destroy := stubTx{txType: TypeMPTokenIssuanceDestroy}
	if v := checkValidMPTIssuance(destroy, TesSUCCESS, []InvariantEntry{deleted}); v != nil {
		t.Fatalf("destroy exactly one issuance: unexpected violation %v", v)
	}
	if v := checkValidMPTIssuance(destroy, TesSUCCESS, nil); v == nil {
		t.Fatal("expected ValidMPTIssuance violation: destroy deleted no issuance")
	}
}

// TestValidMPTIssuance_AuthorizeByActor: MPTokenAuthorize from the holder must
// create or delete exactly one MPToken; the same change submitted by the issuer
// (Holder field present) is forbidden.
// Reference: rippled InvariantCheck.cpp lines 1455-1499.
func TestValidMPTIssuance_AuthorizeByActor(t *testing.T) {
	nonNil := []byte{0x01}
	mptoken := []InvariantEntry{{EntryType: "MPToken", After: nonNil}}

	byHolder := holderTx{stubTx: stubTx{txType: TypeMPTokenAuthorize}, hasHolder: false}
	if v := checkValidMPTIssuance(byHolder, TesSUCCESS, mptoken); v != nil {
		t.Fatalf("holder authorize creating one mptoken: unexpected violation %v", v)
	}

	byIssuer := holderTx{stubTx: stubTx{txType: TypeMPTokenAuthorize}, hasHolder: true}
	if v := checkValidMPTIssuance(byIssuer, TesSUCCESS, mptoken); v == nil {
		t.Fatal("expected ValidMPTIssuance violation: issuer authorize created an mptoken")
	} else if v.Name != "ValidMPTIssuance" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestValidMPTIssuance_UnexpectedChanges: a transaction type that should never
// touch MPT objects (here Payment) must trip when one is created.
// Reference: rippled InvariantCheck.cpp lines 1524-1533.
func TestValidMPTIssuance_UnexpectedChanges(t *testing.T) {
	nonNil := []byte{0x01}
	tx := stubTx{txType: TypePayment}

	created := []InvariantEntry{{EntryType: "MPTokenIssuance", After: nonNil}}
	if v := checkValidMPTIssuance(tx, TesSUCCESS, created); v == nil {
		t.Fatal("expected ValidMPTIssuance violation: Payment created an MPTokenIssuance")
	}
	if v := checkValidMPTIssuance(tx, TesSUCCESS, nil); v != nil {
		t.Fatalf("Payment with no MPT changes: unexpected violation %v", v)
	}
}

// nftPageKey returns a page key whose low-96 bits are the maximum (final page),
// so every test token's page bits stay within the page bounds.
func nftPageKey() [32]byte {
	var key [32]byte
	for i := range 20 {
		key[i] = 0xAA
	}
	for i := 20; i < 32; i++ {
		key[i] = 0xFF
	}
	return key
}

// nftPage encodes an NFTokenPage holding the given token IDs (each with a
// non-empty URI so the empty-URI check stays quiet).
func nftPage(t *testing.T, ids ...[32]byte) []byte {
	t.Helper()
	arr := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		arr = append(arr, map[string]any{
			"NFToken": map[string]any{
				"NFTokenID": strings.ToUpper(hex.EncodeToString(id[:])),
				"URI":       "ABCD",
			},
		})
	}
	return mustEncode(t, map[string]any{
		"LedgerEntryType": "NFTokenPage",
		"NFTokens":        arr,
	})
}

// nftToken returns a 32-byte NFToken ID whose low-96 bits equal low.
func nftToken(low byte) [32]byte {
	var id [32]byte
	id[31] = low
	return id
}

// TestValidNFTokenPage_Sorting: NFTokens on a page must be sorted by ID; an
// out-of-order page trips ValidNFTokenPage while a sorted page satisfies it.
// Reference: rippled InvariantCheck.cpp ValidNFTokenPage (badSort branch).
func TestValidNFTokenPage_Sorting(t *testing.T) {
	rules := amendment.AllSupportedRules()
	key := nftPageKey()
	lo, hi := nftToken(0x02), nftToken(0x05)

	sorted := []InvariantEntry{{EntryType: "NFTokenPage", Key: key, After: nftPage(t, lo, hi)}}
	if v := checkValidNFTokenPage(sorted, stubView{}, rules); v != nil {
		t.Fatalf("sorted page: unexpected violation %v", v)
	}

	unsorted := []InvariantEntry{{EntryType: "NFTokenPage", Key: key, After: nftPage(t, hi, lo)}}
	if v := checkValidNFTokenPage(unsorted, stubView{}, rules); v == nil {
		t.Fatal("expected ValidNFTokenPage violation: NFTokens not sorted")
	} else if v.Name != "ValidNFTokenPage" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// domainTx is a DEX transaction stub that optionally carries a DomainID.
type domainTx struct {
	stubTx
	domain *[32]byte
}

func (t domainTx) GetDomainID() (*[32]byte, bool) {
	if t.domain == nil {
		return nil, false
	}
	return t.domain, true
}

// existsView reports a fixed result for Exists (the domain-existence probe).
type existsView struct {
	stubView
	exists bool
}

func (v existsView) Exists(k keylet.Keylet) (bool, error) { return v.exists, nil }

// dirNodeWithDomain encodes a minimal DirectoryNode carrying a DomainID, the
// shape ValidPermissionedDEX inspects when collecting touched domains.
func dirNodeWithDomain(t *testing.T, domain [32]byte) []byte {
	t.Helper()
	return mustEncode(t, map[string]any{
		"LedgerEntryType": "DirectoryNode",
		"DomainID":        strings.ToUpper(hex.EncodeToString(domain[:])),
	})
}

func domainHash(b byte) [32]byte {
	var d [32]byte
	d[0] = b
	return d
}

// TestValidPermissionedDEX_DomainExistence: a domain transaction whose DomainID
// is absent from the ledger trips ValidPermissionedDEX; one with no DomainID,
// or whose domain exists, satisfies it.
// Reference: rippled InvariantCheck.cpp ValidPermissionedDEX (lines 1690-1696).
func TestValidPermissionedDEX_DomainExistence(t *testing.T) {
	d := domainHash(0x01)
	withDomain := domainTx{stubTx: stubTx{txType: TypeOfferCreate}, domain: &d}
	noDomain := domainTx{stubTx: stubTx{txType: TypeOfferCreate}}

	if v := checkValidPermissionedDEX(withDomain, TesSUCCESS, nil, existsView{exists: false}); v == nil {
		t.Fatal("expected ValidPermissionedDEX violation: domain doesn't exist")
	} else if v.Name != "ValidPermissionedDEX" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}

	if v := checkValidPermissionedDEX(noDomain, TesSUCCESS, nil, existsView{exists: false}); v != nil {
		t.Fatalf("no DomainID on tx: unexpected violation %v", v)
	}

	if v := checkValidPermissionedDEX(withDomain, TesSUCCESS, nil, existsView{exists: true}); v != nil {
		t.Fatalf("existing domain: unexpected violation %v", v)
	}
}

// TestValidPermissionedDEX_WrongDomain: a directory touched by the transaction
// that belongs to a different domain than the tx's DomainID trips the checker.
// Reference: rippled InvariantCheck.cpp lines 1700-1708.
func TestValidPermissionedDEX_WrongDomain(t *testing.T) {
	d1, d2 := domainHash(0x01), domainHash(0x02)
	tx := domainTx{stubTx: stubTx{txType: TypeOfferCreate}, domain: &d1}
	view := existsView{exists: true}

	matching := []InvariantEntry{{EntryType: "DirectoryNode", After: dirNodeWithDomain(t, d1)}}
	if v := checkValidPermissionedDEX(tx, TesSUCCESS, matching, view); v != nil {
		t.Fatalf("matching domain directory: unexpected violation %v", v)
	}

	wrong := []InvariantEntry{{EntryType: "DirectoryNode", After: dirNodeWithDomain(t, d2)}}
	if v := checkValidPermissionedDEX(tx, TesSUCCESS, wrong, view); v == nil {
		t.Fatal("expected ValidPermissionedDEX violation: consumed wrong domains")
	} else if v.Name != "ValidPermissionedDEX" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestValidPermissionedDEX_PresentZeroDomain: a DirectoryNode whose DomainID is
// present but all-zero must be treated as present (not absent), so a tx whose
// DomainID differs from it trips "consumed wrong domains". rippled keys on
// isFieldPresent, not on the value being non-zero.
// Reference: rippled InvariantCheck.cpp:1645.
func TestValidPermissionedDEX_PresentZeroDomain(t *testing.T) {
	d1 := domainHash(0x01)
	tx := domainTx{stubTx: stubTx{txType: TypeOfferCreate}, domain: &d1}
	view := existsView{exists: true}

	var zero [32]byte
	dir := []InvariantEntry{{EntryType: "DirectoryNode", After: dirNodeWithDomain(t, zero)}}
	if v := checkValidPermissionedDEX(tx, TesSUCCESS, dir, view); v == nil {
		t.Fatal("expected ValidPermissionedDEX violation: present-but-zero domain differs from tx domain")
	} else if v.Name != "ValidPermissionedDEX" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}
