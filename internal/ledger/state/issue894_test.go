package state

import (
	"encoding/hex"
	"errors"
	"slices"
	"testing"

	binarycodec "github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/keylet"
)

// TestGetOwnerNode_TicketSequenceWith0x34 guards the H1 fix: GetOwnerNode must
// walk fields by their typed widths rather than scanning for the header byte
// 0x34. A Ticket with TicketSequence == 52 serializes that field's value as
// 0x00000034, and because UInt32 fields sort before the UInt64 sfOwnerNode, the
// old byte-scan would return 8 bytes straddling TicketSequence and yield a
// garbage directory page.
func TestGetOwnerNode_TicketSequenceWith0x34(t *testing.T) {
	const wantOwnerNode uint64 = 0xAABBCCDDEEFF0011 // contains no 0x34 byte

	ticket := map[string]any{
		"LedgerEntryType":   "Ticket",
		"Account":           "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh",
		"Flags":             uint32(0),
		"OwnerNode":         "AABBCCDDEEFF0011",
		"PreviousTxnID":     "0000000000000000000000000000000000000000000000000000000000000000",
		"PreviousTxnLgrSeq": uint32(1),
		"TicketSequence":    uint32(52), // 0x00000034
	}

	hexStr, err := binarycodec.Encode(ticket)
	if err != nil {
		t.Fatalf("encode ticket: %v", err)
	}
	data, err := hex.DecodeString(hexStr)
	if err != nil {
		t.Fatalf("decode hex: %v", err)
	}

	// The hazardous 0x34 byte must actually be present in the serialized form,
	// otherwise the test would not exercise the bug.
	if !slices.Contains(data, 0x34) {
		t.Fatal("expected a 0x34 byte in the serialized ticket to exercise the byte-scan hazard")
	}

	if got := GetOwnerNode(data); got != wantOwnerNode {
		t.Fatalf("GetOwnerNode = %#016x, want %#016x", got, wantOwnerNode)
	}
}

// TestGetRate_RoundToNearest guards the H2 fix: GetRate must round the quotient
// the way rippled's divide()->canonicalize() does under fixUniversalNumber
// (round-to-nearest-ties-to-even), not truncate. The values below are derived
// from rippled's divide(offerIn, offerOut) = muldiv(num, 10^17, den) + 5,
// canonicalized into the [10^15, 10^16) mantissa band.
func TestGetRate_RoundToNearest(t *testing.T) {
	prev := GetNumberSwitchover()
	SetNumberSwitchover(true)
	defer SetNumberSwitchover(prev)

	rate := func(exp int, mantissa uint64) uint64 {
		return uint64(exp+100)<<56 | mantissa
	}

	tests := []struct {
		name             string
		offerOut, offerIn Amount
		want             uint64
	}{
		{
			name:     "identity 1/1",
			offerOut: NewXRPAmountFromInt(1),
			offerIn:  NewXRPAmountFromInt(1),
			want:     rate(-15, 1000000000000000),
		},
		{
			name:     "half 1/2",
			offerOut: NewXRPAmountFromInt(2),
			offerIn:  NewXRPAmountFromInt(1),
			want:     rate(-16, 5000000000000000),
		},
		{
			name:     "double 2/1",
			offerOut: NewXRPAmountFromInt(1),
			offerIn:  NewXRPAmountFromInt(2),
			want:     rate(-15, 2000000000000000),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := GetRate(tc.offerOut, tc.offerIn); got != tc.want {
				t.Fatalf("GetRate = %#x, want %#x", got, tc.want)
			}
		})
	}

	// A zero operand yields a zero rate ("offer too good" / undefined).
	if got := GetRate(NewXRPAmountFromInt(0), NewXRPAmountFromInt(1)); got != 0 {
		t.Fatalf("GetRate(0, 1) = %#x, want 0", got)
	}
}

// TestParseIOUValueRoundTrip guards the H3 fix: a value rendered by
// IOUAmountValue.String() — including the scientific notation it emits for
// normalized values at or above ~10^12 — must parse back to the same
// mantissa/exponent, not silently collapse to zero.
func TestParseIOUValueRoundTrip(t *testing.T) {
	const mantissa int64 = 1234567890123456 // 16 significant digits

	for exp := MinExponent; exp <= MaxExponent; exp++ {
		want := NewIOUAmountValue(mantissa, exp)
		s := want.String()

		got, err := parseIOUValueFromString(s)
		if err != nil {
			t.Fatalf("parse %q (exp %d): %v", s, exp, err)
		}
		if got.Mantissa() != want.Mantissa() || got.Exponent() != want.Exponent() {
			t.Fatalf("round-trip %q: got (m=%d,e=%d), want (m=%d,e=%d)",
				s, got.Mantissa(), got.Exponent(), want.Mantissa(), want.Exponent())
		}
	}
}

// TestParseIOUValue_ScientificNotation pins the specific regression from H3: an
// LPTokenBalance of 10^12 units renders as scientific notation and must not
// parse back to zero.
func TestParseIOUValue_ScientificNotation(t *testing.T) {
	v := NewIOUAmountValue(1000000000000000, -3) // 10^15 * 10^-3 = 10^12
	s := v.String()

	got, err := parseIOUValueFromString(s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	if got.IsZero() {
		t.Fatalf("parse %q collapsed to zero (string was %q)", s, s)
	}
	if got.Mantissa() != v.Mantissa() || got.Exponent() != v.Exponent() {
		t.Fatalf("parse %q: got (m=%d,e=%d), want (m=%d,e=%d)",
			s, got.Mantissa(), got.Exponent(), v.Mantissa(), v.Exponent())
	}
}

// TestDivideByZeroPanics guards the M5 fix: a zero denominator is an engine
// bug, so the division helpers panic (recovered at the tx-apply boundary)
// instead of silently returning zero and risking a wrong ledger.
func TestDivideByZeroPanics(t *testing.T) {
	usd := NewIssuedAmountFromValue(MinMantissa, -15, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")
	zero := NewIssuedAmountFromValue(0, zeroExponent, "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh")

	mustPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if recover() == nil {
				t.Fatalf("%s: expected panic on zero denominator", name)
			}
		}()
		fn()
	}

	mustPanic("Amount.Div", func() { usd.Div(zero, false) })
	mustPanic("DivRound", func() { DivRound(usd, zero, "USD", usd.Issuer, false) })
	mustPanic("DivRoundStrict", func() { DivRoundStrict(usd, zero, "USD", usd.Issuer, false) })
	mustPanic("DivRoundNative", func() { DivRoundNative(usd, zero, false) })
}

// TestDirInsert_PreserveOrderRejectsDuplicate guards the M3 fix: the
// preserveOrder (book-directory) branch of DirInsert now rejects a double
// insertion, matching rippled's dirAdd which checks both branches.
func TestDirInsert_PreserveOrderRejectsDuplicate(t *testing.T) {
	v := newStubView()
	dir := testDir()
	item := itemKeyN(1)

	if _, err := DirInsert(v, dir, item, true, nil); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	if _, err := DirInsert(v, dir, item, true, nil); err == nil {
		t.Fatal("expected a double-insertion error on the preserveOrder branch")
	}
}

// TestDirRemove_MissingPageNotFound guards the M4 fix: a genuinely absent page
// (Read returns nil, nil) is reported as a clean not-found (Success=false, no
// error), not a misleading codec error.
func TestDirRemove_MissingPageNotFound(t *testing.T) {
	v := newStubView()
	res, err := DirRemove(v, testDir(), 0, itemKeyN(1), false)
	if err != nil {
		t.Fatalf("missing page must not be an error, got %v", err)
	}
	if res == nil || res.Success {
		t.Fatalf("missing page: want non-nil result with Success=false, got %+v", res)
	}
}

// errReadView wraps stubView but fails every Read, to exercise the M4 storage-
// error propagation path.
type errReadView struct{ *stubView }

func (errReadView) Read(keylet.Keylet) ([]byte, error) {
	return nil, errors.New("storage failure")
}

// TestDirRemove_PropagatesStorageError guards the M4 fix: a real storage error
// propagates instead of being swallowed as a benign not-found.
func TestDirRemove_PropagatesStorageError(t *testing.T) {
	v := errReadView{newStubView()}
	res, err := DirRemove(v, testDir(), 0, itemKeyN(1), false)
	if err == nil {
		t.Fatal("expected the storage error to propagate")
	}
	if res != nil {
		t.Fatalf("expected a nil result on storage error, got %+v", res)
	}
}

// TestNewIssuedAmountFromDecimalString_Error confirms unparseable input now
// surfaces an error instead of a masked zero amount.
func TestNewIssuedAmountFromDecimalString_Error(t *testing.T) {
	if _, err := NewIssuedAmountFromDecimalString("not-a-number", "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"); err == nil {
		t.Fatal("expected error for unparseable decimal string, got nil")
	}
	if _, err := NewIssuedAmountFromDecimalString("1.2.3", "USD", "rHb9CJAWyB4rj91VRWn96DkukG4bwdtyTh"); err == nil {
		t.Fatal("expected error for malformed decimal string, got nil")
	}
}
