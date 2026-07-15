package selector

import (
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	var hash [32]byte
	for i := range hash {
		hash[i] = byte(i)
	}

	tests := []struct {
		name  string
		value string
		want  Selector
	}{
		{name: "absent", value: "", want: Absent()},
		{name: "current", value: "current", want: Current()},
		{name: "closed", value: "closed", want: Closed()},
		{name: "validated", value: "validated", want: Validated()},
		{name: "zero sequence", value: "0", want: FromSequence(0)},
		{name: "plus zero sequence", value: "+0", want: FromSequence(0)},
		{name: "sequence", value: "123456", want: FromSequence(123456)},
		{name: "plus sequence", value: "+123456", want: FromSequence(123456)},
		{name: "maximum sequence", value: "4294967295", want: FromSequence(math.MaxUint32)},
		{name: "uppercase hash", value: strings.ToUpper(hex.EncodeToString(hash[:])), want: FromHash(hash)},
		{name: "lowercase hash", value: hex.EncodeToString(hash[:]), want: FromHash(hash)},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.value)
			if err != nil {
				t.Fatalf("Parse(%q) returned error: %v", test.value, err)
			}
			if got != test.want {
				t.Fatalf("Parse(%q) = %#v, want %#v", test.value, got, test.want)
			}
		})
	}
}

func TestParseRejectsMalformedSelectors(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  error
	}{
		{name: "unknown shortcut", value: "latest", want: ErrInvalidIndex},
		{name: "uppercase shortcut", value: "CURRENT", want: ErrInvalidIndex},
		{name: "leading whitespace", value: " 1", want: ErrInvalidIndex},
		{name: "trailing whitespace", value: "1 ", want: ErrInvalidIndex},
		{name: "plus sign without digits", value: "+", want: ErrInvalidIndex},
		{name: "minus sign", value: "-1", want: ErrInvalidIndex},
		{name: "fraction", value: "1.0", want: ErrInvalidIndex},
		{name: "uint32 overflow", value: "4294967296", want: ErrInvalidIndex},
		{name: "much larger overflow", value: "18446744073709551616", want: ErrInvalidIndex},
		{name: "short hash", value: strings.Repeat("a", 63), want: ErrInvalidIndex},
		{name: "long hash", value: strings.Repeat("a", 65), want: ErrInvalidIndex},
		{name: "non hex hash", value: strings.Repeat("g", 64), want: ErrInvalidHash},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.value)
			if !errors.Is(err, test.want) {
				t.Fatalf("Parse(%q) error = %v, want %v", test.value, err, test.want)
			}
			if !errors.Is(err, ErrInvalidSelector) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidSelector", test.value, err)
			}
			if got != (Selector{}) {
				t.Fatalf("Parse(%q) = %#v, want zero Selector", test.value, got)
			}
		})
	}
}

func TestFieldSpecificParsers(t *testing.T) {
	zeroPadded := strings.Repeat("0", 63) + "1"
	selection, err := ParseIndex(zeroPadded)
	if err != nil || selection != FromSequence(1) {
		t.Fatalf("ParseIndex(zero-padded 1) = %#v, %v", selection, err)
	}
	if _, err := ParseHash(zeroPadded); err != nil {
		t.Fatalf("ParseHash(zero-padded 1): %v", err)
	}
	zeroHash, err := ParseHash("0")
	if err != nil || zeroHash != FromHash([32]byte{}) {
		t.Fatalf("ParseHash(0) = %#v, %v", zeroHash, err)
	}
	selection, err = Parse("+1")
	if err != nil || selection != FromSequence(1) {
		t.Fatalf("Parse(+1) = %#v, %v", selection, err)
	}
}

func TestSelectorAccessorsAndCanonicalString(t *testing.T) {
	var hash [32]byte
	hash[0] = 0xab
	hash[31] = 0xcd

	tests := []struct {
		name        string
		selection   Selector
		kind        Kind
		value       string
		sequence    uint32
		hasSequence bool
		hash        [32]byte
		hasHash     bool
	}{
		{name: "absent", selection: Absent(), kind: KindAbsent, value: ""},
		{name: "current", selection: Current(), kind: KindCurrent, value: "current"},
		{name: "closed", selection: Closed(), kind: KindClosed, value: "closed"},
		{name: "validated", selection: Validated(), kind: KindValidated, value: "validated"},
		{name: "sequence", selection: FromSequence(42), kind: KindSequence, value: "42", sequence: 42, hasSequence: true},
		{name: "hash", selection: FromHash(hash), kind: KindHash, value: strings.ToUpper(hex.EncodeToString(hash[:])), hash: hash, hasHash: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.selection.Kind(); got != test.kind {
				t.Errorf("Kind() = %d, want %d", got, test.kind)
			}
			if got := test.selection.String(); got != test.value {
				t.Errorf("String() = %q, want %q", got, test.value)
			}
			sequence, ok := test.selection.Sequence()
			if sequence != test.sequence || ok != test.hasSequence {
				t.Errorf("Sequence() = (%d, %t), want (%d, %t)", sequence, ok, test.sequence, test.hasSequence)
			}
			hash, ok := test.selection.Hash()
			if hash != test.hash || ok != test.hasHash {
				t.Errorf("Hash() = (%x, %t), want (%x, %t)", hash, ok, test.hash, test.hasHash)
			}
		})
	}
}

type testLedger struct {
	name      string
	sequence  uint32
	hash      [32]byte
	validated bool
}

func (l testLedger) Sequence() uint32  { return l.sequence }
func (l testLedger) Hash() [32]byte    { return l.hash }
func (l testLedger) IsValidated() bool { return l.validated }

func TestResolve(t *testing.T) {
	var requestedSequence uint32
	var requestedHash [32]byte
	requestedHash[0] = 0x70

	tests := []struct {
		name      string
		selection Selector
		wantName  string
		wantValid bool
	}{
		{name: "absent remains distinct", selection: Absent(), wantName: "absent-default", wantValid: true},
		{name: "current", selection: Current(), wantName: "current", wantValid: false},
		{name: "closed", selection: Closed(), wantName: "closed", wantValid: true},
		{name: "validated uses target state", selection: Validated(), wantName: "validated", wantValid: false},
		{name: "sequence", selection: FromSequence(70), wantName: "sequence", wantValid: true},
		{name: "hash", selection: FromHash(requestedHash), wantName: "hash", wantValid: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := make(map[string]int)
			ledgerFor := func(name string, validated bool) (testLedger, bool, error) {
				calls[name]++
				var hash [32]byte
				hash[0] = byte(len(name))
				return testLedger{name: name, sequence: uint32(len(name)), hash: hash, validated: validated}, true, nil
			}
			callbacks := Callbacks[testLedger]{
				Absent: func() (testLedger, bool, error) {
					return ledgerFor("absent-default", true)
				},
				Current: func() (testLedger, bool, error) {
					return ledgerFor("current", false)
				},
				Closed: func() (testLedger, bool, error) {
					return ledgerFor("closed", true)
				},
				Validated: func() (testLedger, bool, error) {
					return ledgerFor("validated", false)
				},
				BySequence: func(sequence uint32) (testLedger, bool, error) {
					requestedSequence = sequence
					return ledgerFor("sequence", true)
				},
				ByHash: func(hash [32]byte) (testLedger, bool, error) {
					requestedHash = hash
					return ledgerFor("hash", false)
				},
			}

			result, err := Resolve(test.selection, callbacks)
			if err != nil {
				t.Fatalf("Resolve() returned error: %v", err)
			}
			if result.Value.name != test.wantName {
				t.Errorf("Value.name = %q, want %q", result.Value.name, test.wantName)
			}
			if result.Selector != test.selection {
				t.Errorf("Selector = %#v, want %#v", result.Selector, test.selection)
			}
			if result.Sequence != result.Value.Sequence() {
				t.Errorf("Sequence = %d, want %d", result.Sequence, result.Value.Sequence())
			}
			if result.Hash != result.Value.Hash() {
				t.Errorf("Hash = %x, want %x", result.Hash, result.Value.Hash())
			}
			if result.Validated != test.wantValid {
				t.Errorf("Validated = %t, want %t", result.Validated, test.wantValid)
			}
			if len(calls) != 1 || calls[test.wantName] != 1 {
				t.Errorf("callbacks invoked = %v, want only %q", calls, test.wantName)
			}
			if test.selection.Kind() == KindSequence && requestedSequence != 70 {
				t.Errorf("sequence callback argument = %d, want 70", requestedSequence)
			}
			if test.selection.Kind() == KindHash && requestedHash != test.selection.hash {
				t.Errorf("hash callback argument = %x, want %x", requestedHash, test.selection.hash)
			}
		})
	}
}

func TestResolveMissingTargets(t *testing.T) {
	missing := func() (testLedger, bool, error) { return testLedger{}, false, nil }
	missingSequence := func(uint32) (testLedger, bool, error) { return testLedger{}, false, nil }
	missingHash := func([32]byte) (testLedger, bool, error) { return testLedger{}, false, nil }
	callbacks := Callbacks[testLedger]{
		Absent: missing, Current: missing, Closed: missing, Validated: missing,
		BySequence: missingSequence, ByHash: missingHash,
	}

	tests := []Selector{Absent(), Current(), Closed(), Validated(), FromSequence(1), FromHash([32]byte{1})}
	for _, selection := range tests {
		t.Run(selectionName(selection), func(t *testing.T) {
			result, err := Resolve(selection, callbacks)
			if !errors.Is(err, ErrLedgerNotFound) {
				t.Fatalf("Resolve() error = %v, want ErrLedgerNotFound", err)
			}
			if result != (Result[testLedger]{}) {
				t.Fatalf("Resolve() result = %#v, want zero result", result)
			}
		})
	}
}

func TestResolvePropagatesCallbackErrors(t *testing.T) {
	callbackErr := errors.New("storage unavailable")
	failing := func() (testLedger, bool, error) { return testLedger{}, false, callbackErr }
	failingSequence := func(uint32) (testLedger, bool, error) { return testLedger{}, false, callbackErr }
	failingHash := func([32]byte) (testLedger, bool, error) { return testLedger{}, false, callbackErr }
	callbacks := Callbacks[testLedger]{
		Absent: failing, Current: failing, Closed: failing, Validated: failing,
		BySequence: failingSequence, ByHash: failingHash,
	}

	tests := []Selector{Absent(), Current(), Closed(), Validated(), FromSequence(1), FromHash([32]byte{1})}
	for _, selection := range tests {
		t.Run(selectionName(selection), func(t *testing.T) {
			result, err := Resolve(selection, callbacks)
			if !errors.Is(err, callbackErr) {
				t.Fatalf("Resolve() error = %v, want callback error", err)
			}
			if errors.Is(err, ErrLedgerNotFound) {
				t.Fatalf("Resolve() error = %v, must not replace callback error with ErrLedgerNotFound", err)
			}
			if result != (Result[testLedger]{}) {
				t.Fatalf("Resolve() result = %#v, want zero result", result)
			}
		})
	}
}

func TestResolveRejectsInvalidKind(t *testing.T) {
	selection := Selector{kind: Kind(255)}
	result, err := Resolve(selection, Callbacks[testLedger]{})
	if !errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("Resolve() error = %v, want ErrInvalidSelector", err)
	}
	if result != (Result[testLedger]{}) {
		t.Fatalf("Resolve() result = %#v, want zero result", result)
	}
}

func TestResolveReportsMissingCallback(t *testing.T) {
	result, err := Resolve(Current(), Callbacks[testLedger]{})
	if err == nil {
		t.Fatal("Resolve() returned nil error")
	}
	if errors.Is(err, ErrLedgerNotFound) || errors.Is(err, ErrInvalidSelector) {
		t.Fatalf("Resolve() error = %v, want resolver configuration error", err)
	}
	if result != (Result[testLedger]{}) {
		t.Fatalf("Resolve() result = %#v, want zero result", result)
	}
}

func selectionName(selection Selector) string {
	if selection.Kind() == KindAbsent {
		return "absent"
	}
	return selection.String()
}
