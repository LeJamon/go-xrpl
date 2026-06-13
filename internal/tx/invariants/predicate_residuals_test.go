package invariants

// predicate_residuals_test.go — tests for the checker-predicate fixes in
// issue #857: NoBadOffers sign-aware parsing (zero acceptable, negative bad),
// TransfersNotFrozen without the same-issuer skip, ValidAMM create tolerance,
// and hard-fail-on-parse-error hygiene across the affected checkers.

import (
	"encoding/binary"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/amendment"
	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/keylet"
)

// offerSLE encodes an Offer ledger entry. takerPays and takerGets are either a
// drops string (XRP) or a {currency,issuer,value} map (IOU).
func offerSLE(t *testing.T, takerPays, takerGets any) []byte {
	t.Helper()
	hexStr, err := binarycodec.Encode(map[string]any{
		"LedgerEntryType": "Offer",
		"Account":         addrHolderA,
		"Sequence":        uint32(1),
		"TakerPays":       takerPays,
		"TakerGets":       takerGets,
		"BookDirectory":   "0000000000000000000000000000000000000000000000000000000000000000",
		"BookNode":        "0",
		"OwnerNode":       "0",
		"Flags":           uint32(0),
	})
	if err != nil {
		t.Fatalf("binarycodec.Encode: %v", err)
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("hex.DecodeString: %v", err)
	}
	return b
}

func usdAmount(value string) map[string]any {
	return map[string]any{"currency": "USD", "issuer": addrIssuer, "value": value}
}

// TestNoBadOffers_ZeroAmountsAccepted: rippled's isBad accepts zero amounts; an
// offer with zero XRP on one side and an IOU on the other must NOT trip the
// invariant (the old code raised a false tecINVARIANT_FAILED here).
// Reference: rippled InvariantCheck.cpp:228-245.
func TestNoBadOffers_ZeroAmountsAccepted(t *testing.T) {
	entries := []InvariantEntry{{
		EntryType: "Offer",
		After:     offerSLE(t, "0", usdAmount("5")),
	}}
	if v := checkNoBadOffers(entries); v != nil {
		t.Fatalf("zero-XRP offer must be accepted, got violation %v", v)
	}

	// Zero on the IOU side is equally acceptable.
	entries = []InvariantEntry{{
		EntryType: "Offer",
		After:     offerSLE(t, "1000000", usdAmount("0")),
	}}
	if v := checkNoBadOffers(entries); v != nil {
		t.Fatalf("zero-IOU offer must be accepted, got violation %v", v)
	}
}

// TestNoBadOffers_NegativeXRP: a negative native amount is produced by clearing
// the sign bit (bit 62) of the serialized TakerGets. rippled flags pays<0 ||
// gets<0; the old sign-masking parser was blind to it.
// Reference: rippled InvariantCheck.cpp:230-234.
func TestNoBadOffers_NegativeXRP(t *testing.T) {
	blob := offerSLE(t, usdAmount("5"), "1000000")
	negateNativeAmount(t, blob)

	entries := []InvariantEntry{{EntryType: "Offer", After: blob}}
	if v := checkNoBadOffers(entries); v == nil {
		t.Fatal("expected NoBadOffers violation for negative XRP TakerGets")
	} else if v.Name != "NoBadOffers" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestNoBadOffers_NegativeIOU: a negative IOU amount (encodable directly) on a
// leg must trip the invariant. The old parser skipped IOU amounts entirely.
// Reference: rippled InvariantCheck.cpp:230-234.
func TestNoBadOffers_NegativeIOU(t *testing.T) {
	entries := []InvariantEntry{{
		EntryType: "Offer",
		After:     offerSLE(t, usdAmount("-5"), "1000000"),
	}}
	if v := checkNoBadOffers(entries); v == nil {
		t.Fatal("expected NoBadOffers violation for negative IOU TakerPays")
	} else if v.Name != "NoBadOffers" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestNoBadOffers_XRPForXRP: an offer native on both sides is still forbidden,
// even when both magnitudes are positive.
// Reference: rippled InvariantCheck.cpp:237.
func TestNoBadOffers_XRPForXRP(t *testing.T) {
	entries := []InvariantEntry{{
		EntryType: "Offer",
		After:     offerSLE(t, "1000000", "2000000"),
	}}
	if v := checkNoBadOffers(entries); v == nil {
		t.Fatal("expected NoBadOffers violation for XRP-for-XRP offer")
	}
}

// TestNoBadOffers_ParseFailure: a corrupt Offer SLE must fail the invariant
// rather than silently continuing.
func TestNoBadOffers_ParseFailure(t *testing.T) {
	entries := []InvariantEntry{{EntryType: "Offer", After: []byte{0x01}}}
	if v := checkNoBadOffers(entries); v == nil {
		t.Fatal("expected NoBadOffers violation for unparseable Offer SLE")
	} else if v.Name != "NoBadOffers" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// negateNativeAmount clears the sign bit on the first native (XRP) Amount field
// in an SLE blob, turning a positive drops value into a negative one.
func negateNativeAmount(t *testing.T, data []byte) {
	t.Helper()
	offset := 0
	for offset < len(data) {
		header := data[offset]
		offset++
		typeCode := int((header >> 4) & 0x0F)
		fieldCode := int(header & 0x0F)
		if typeCode == 0 {
			typeCode = int(data[offset])
			offset++
		}
		if fieldCode == 0 {
			fieldCode = int(data[offset])
			offset++
		}
		if typeCode == 6 { // Amount
			if data[offset]&0x80 == 0 { // native
				raw := binary.BigEndian.Uint64(data[offset : offset+8])
				raw &^= 0x4000000000000000 // clear sign bit → negative
				binary.BigEndian.PutUint64(data[offset:offset+8], raw)
				return
			}
			offset += 48
			continue
		}
		skip, ok := skipFieldBytes(typeCode, fieldCode, data, offset)
		if !ok {
			t.Fatal("negateNativeAmount: could not walk SLE")
		}
		offset += skip
	}
	t.Fatal("negateNativeAmount: no native Amount field found")
}

// TestTransfersNotFrozen_SameIssuerLineEnforced: a frozen transfer routed
// through an issuer whose trust lines were once skipped by the (now removed)
// LowLimit.Issuer==HighLimit.Issuer guard is now caught. The lines here use
// distinct low/high issuers — the shape every RippleState actually serializes
// to — and a global freeze must trip the checker.
func TestTransfersNotFrozen_SameIssuerLineEnforced(t *testing.T) {
	rules := amendment.AllSupportedRules()
	tx := stubTx{txType: TypePayment}

	frozen := frozenTransferEntries(t, state.LsfGlobalFreeze)
	if v := checkTransfersNotFrozen(tx, frozen, stubView{}, rules); v == nil {
		t.Fatal("expected TransfersNotFrozen violation now that the same-issuer skip is gone")
	} else if v.Name != "TransfersNotFrozen" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// TestTransfersNotFrozen_ParseFailure: a corrupt RippleState in the entry set
// fails the invariant when enforcing, and is ignored when not enforcing
// (matching the checker's amendment-gated enforcement model).
func TestTransfersNotFrozen_ParseFailure(t *testing.T) {
	tx := stubTx{txType: TypePayment}
	corrupt := mustEncode(t, map[string]any{"LedgerEntryType": "RippleState"})
	corrupt = append(corrupt, 0xFF) // trailing junk → ParseRippleState fails
	entries := []InvariantEntry{{EntryType: "RippleState", After: corrupt}}

	if v := checkTransfersNotFrozen(tx, entries, stubView{}, amendment.AllSupportedRules()); v == nil {
		t.Fatal("expected TransfersNotFrozen violation for unparseable RippleState (enforcing)")
	}
	if v := checkTransfersNotFrozen(tx, entries, stubView{}, amendment.EmptyRules()); v != nil {
		t.Fatalf("deep freeze disabled: unexpected violation %v", v)
	}
}

// ammPoolView returns AMM-account holdings keyed by keylet: the AMM
// AccountRoot (XRP side) and the AMM↔issuer trust line (USD side).
type ammPoolView struct {
	stubView
	entries map[[32]byte][]byte
}

func (v ammPoolView) Read(k keylet.Keylet) ([]byte, error) { return v.entries[k.Key], nil }

// ammCreateTx supplies the two pool assets for finalizeAMMCreate (XRP + USD).
type ammCreateTx struct {
	stubTx
}

func (t ammCreateTx) GetAmountAsset() Asset { return Asset{Currency: "XRP"} }
func (t ammCreateTx) GetAmount2Asset() Asset {
	return Asset{Currency: "USD", Issuer: addrIssuer}
}

// TestValidAMM_CreateToleranceAbsorbsULP: the create-time LP-token reconstruction
// (sqrt(amount*amount2)) can land one unit off the recorded LPTokenBalance in the
// 16th significant digit. That ULP-scale drift must NOT trip ValidAMM, while a
// gross mismatch still does. See issue #857 (c).
func TestValidAMM_CreateToleranceAbsorbsULP(t *testing.T) {
	rules := amendment.AllSupportedRules()
	tx := ammCreateTx{stubTx: stubTx{txType: TypeAMMCreate}}

	ammID, err := state.DecodeAccountID(addrHolderA)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}
	issuerID, err := state.DecodeAccountID(addrIssuer)
	if err != nil {
		t.Fatalf("DecodeAccountID: %v", err)
	}

	// Pool: 25_000_000 (XRP side, in drops) and 250000000 USD → the invariant
	// reconstructs sqrt(product) = 79056941.50420945. A recorded LPTokenBalance
	// one unit off in the 16th significant digit (…46) is ULP-scale drift the
	// tolerance must absorb.
	acctBlob := mustSerializeAccount(t, &state.AccountRoot{
		Account: addrHolderA, Balance: 25_000_000, Sequence: 1,
	})

	ammIsLow := state.CompareAccountIDsForLine(ammID, issuerID) < 0
	bal, _ := state.NewIssuedAmountFromDecimalString("250000000", "USD", state.AccountOneAddress)
	if !ammIsLow {
		bal = bal.Negate()
	}
	low, high := addrHolderA, addrIssuer
	if !ammIsLow {
		low, high = addrIssuer, addrHolderA
	}
	lowLimitAmt, _ := state.NewIssuedAmountFromDecimalString("0", "USD", low)
	highLimitAmt, _ := state.NewIssuedAmountFromDecimalString("0", "USD", high)
	rsBlob, err := state.SerializeRippleState(&state.RippleState{
		Balance:   bal,
		LowLimit:  lowLimitAmt,
		HighLimit: highLimitAmt,
		Flags:     state.LsfAMMNode,
	})
	if err != nil {
		t.Fatalf("SerializeRippleState: %v", err)
	}

	view := ammPoolView{entries: map[[32]byte][]byte{
		keylet.Account(ammID).Key:               acctBlob,
		keylet.Line(ammID, issuerID, "USD").Key: rsBlob,
	}}

	within := []InvariantEntry{{EntryType: "AMM", After: ammSLE(t, addrHolderA, "79056941.50420946")}}
	if v := checkValidAMM(tx, TesSUCCESS, within, view, rules); v != nil {
		t.Fatalf("ULP-scale LP token drift must be tolerated, got violation %v", v)
	}

	// A 1% mismatch is well outside the 1e-11 tolerance and must trip.
	gross := []InvariantEntry{{EntryType: "AMM", After: ammSLE(t, addrHolderA, "80000000")}}
	if v := checkValidAMM(tx, TesSUCCESS, gross, view, rules); v == nil {
		t.Fatal("expected ValidAMM violation for a gross LP-token mismatch")
	} else if v.Name != "ValidAMM" {
		t.Fatalf("unexpected violation name %q", v.Name)
	}
}

// corruptRippleState returns a blob whose LedgerEntryType header decodes as
// RippleState but whose body is too short for ParseRippleState to succeed.
func corruptRippleState(t *testing.T) []byte {
	t.Helper()
	return append(mustEncode(t, map[string]any{"LedgerEntryType": "RippleState"}), 0xFF)
}

// TestParseError_HardFails verifies the parse-error hygiene fixes: an
// unparseable SLE in an affected node set fails the invariant instead of being
// silently skipped. The basic.go checkers already follow this rule; these
// checkers now match.
func TestParseError_HardFails(t *testing.T) {
	rsBad := corruptRippleState(t)
	acctBad := []byte{0x01} // too short for ParseAccountRoot

	t.Run("NoXRPTrustLines", func(t *testing.T) {
		entries := []InvariantEntry{{EntryType: "RippleState", After: rsBad}}
		if v := checkNoXRPTrustLines(entries); v == nil {
			t.Fatal("expected NoXRPTrustLines violation for unparseable RippleState")
		}
	})

	t.Run("NoDeepFreeze", func(t *testing.T) {
		entries := []InvariantEntry{{EntryType: "RippleState", After: rsBad}}
		if v := checkNoDeepFreezeTrustLinesWithoutFreeze(entries); v == nil {
			t.Fatal("expected NoDeepFreezeTrustLinesWithoutFreeze violation for unparseable RippleState")
		}
	})

	t.Run("NFTokenCountTracking", func(t *testing.T) {
		entries := []InvariantEntry{{EntryType: "AccountRoot", After: acctBad}}
		if v := checkNFTokenCountTracking("Payment", TesSUCCESS, entries); v == nil {
			t.Fatal("expected NFTokenCountTracking violation for unparseable AccountRoot")
		}
	})

	t.Run("ValidClawback", func(t *testing.T) {
		amount, _ := state.NewIssuedAmountFromDecimalString("1", "USD", addrHolderA)
		tx := clawbackTx{
			account: addrIssuer,
			amount:  amount,
		}
		entries := []InvariantEntry{{EntryType: "RippleState", Before: rsBad, After: rsBad}}
		if v := checkValidClawback(tx, TesSUCCESS, entries, lineView{line: rsBad}); v == nil {
			t.Fatal("expected ValidClawback violation for unparseable trust line")
		}
	})

	t.Run("ValidPermissionedDomain", func(t *testing.T) {
		pdBad := append(mustEncode(t, map[string]any{"LedgerEntryType": "PermissionedDomain"}), 0xFF)
		entries := []InvariantEntry{{EntryType: "PermissionedDomain", After: pdBad}}
		if v := checkValidPermissionedDomain(stubTx{txType: TypePermissionedDomainSet}, TesSUCCESS, entries); v == nil {
			t.Fatal("expected ValidPermissionedDomain violation for unparseable PermissionedDomain")
		}
	})

	t.Run("ValidPermissionedDEX", func(t *testing.T) {
		offerBad := append(mustEncode(t, map[string]any{"LedgerEntryType": "Offer"}), 0xFF)
		entries := []InvariantEntry{{EntryType: "Offer", After: offerBad}}
		tx := domainTx{stubTx: stubTx{txType: TypeOfferCreate}}
		if v := checkValidPermissionedDEX(tx, TesSUCCESS, entries, existsView{exists: true}); v == nil {
			t.Fatal("expected ValidPermissionedDEX violation for unparseable Offer")
		}
	})
}
