package tx

import (
	"testing"

	"github.com/LeJamon/go-xrpl/codec/binarycodec/definitions"
)

// decodeOnlyResultExtras lists TRANSACTION_RESULTS entries in definitions.json
// that intentionally have no Go Result constant. These are decode-only codes
// inherited from upstream ripple-binary-codec definitions: they may appear in
// historical metadata and must decode by name, but go-xrpl never produces them.
//
//   - terNO_DELEGATE_PERMISSION (-85): superseded by tecNO_DELEGATE_PERMISSION
//     (198); the negative-range form is retained only for backward decode.
var decodeOnlyResultExtras = map[string]int32{
	"terNO_DELEGATE_PERMISSION": -85,
}

// TestResultDefinitionsLockstep enforces the invariant that every Result
// constant in result.go has a matching definitions.json TRANSACTION_RESULTS
// entry (same name AND same value) and vice versa, modulo the documented
// decode-only extras.
//
// This is fork protection: transaction metadata serializes TransactionResult
// BY NAME via definitions.json. A Result code present in result.go but missing
// from definitions.json (or with a divergent value) breaks metadata
// serialization with ledger state already mutated, producing a divergent
// ledger (the PR #726 hazard class).
func TestResultDefinitionsLockstep(t *testing.T) {
	defs := definitions.Get().TransactionResults

	// Every result.go constant must appear in definitions.json with the same value.
	for code, name := range resultNames {
		defCode, ok := defs[name]
		if !ok {
			t.Errorf("result.go has %s (%d) but definitions.json TRANSACTION_RESULTS does not", name, code)
			continue
		}
		if int32(code) != defCode {
			t.Errorf("value mismatch for %s: result.go=%d definitions.json=%d", name, code, defCode)
		}
	}

	// Every definitions.json entry must appear in result.go, except the
	// documented decode-only extras.
	for name, defCode := range defs {
		if extra, ok := decodeOnlyResultExtras[name]; ok {
			if extra != defCode {
				t.Errorf("decodeOnlyResultExtras[%s]=%d but definitions.json=%d — update the exception list", name, extra, defCode)
			}
			continue
		}
		if _, ok := resultNames[Result(defCode)]; !ok {
			t.Errorf("definitions.json has %s (%d) but result.go has no constant for that value", name, defCode)
			continue
		}
		if resultNames[Result(defCode)] != name {
			t.Errorf("name mismatch at value %d: result.go=%q definitions.json=%q", defCode, resultNames[Result(defCode)], name)
		}
	}

	// The exception list must not reference names that definitions.json dropped.
	for name := range decodeOnlyResultExtras {
		if _, ok := defs[name]; !ok {
			t.Errorf("decodeOnlyResultExtras lists %s but definitions.json no longer has it — prune the exception", name)
		}
	}
}

// TestResultMessage asserts a representative sample of result messages matches
// rippled's transHuman (TER.cpp transResults) byte-for-byte.
func TestResultMessage(t *testing.T) {
	cases := []struct {
		code Result
		want string
	}{
		{TesSUCCESS, "The transaction was applied. Only final in a validated ledger."},
		{TecCLAIM, "Fee claimed. Sequence used. No action."},
		{TemBAD_AMOUNT, "Malformed: Bad amount."},
		{TecPATH_DRY, "Path could not send partial amount."},
		{TefALREADY, "The exact transaction was already in this ledger."},
		{TelCAN_NOT_QUEUE, "Can not queue at this time."},
		{TerRETRY, "Retry transaction."},
		{TelENV_RPC_FAILED, "Unit test RPC failure."},
		{TemCAN_NOT_PREAUTH_SELF, "Malformed: An account may not preauthorize itself."},
	}
	for _, c := range cases {
		if got := c.code.Message(); got != c.want {
			t.Errorf("Message(%s): got %q, want %q", c.code, got, c.want)
		}
	}

	// tecHOOK_REJECTED has no rippled description; Message returns "-".
	if got := TecHOOK_REJECTED.Message(); got != "-" {
		t.Errorf("Message(tecHOOK_REJECTED): got %q, want %q", got, "-")
	}

	// An unrecognized code returns "-" (matching rippled transHuman).
	if got := Result(12345).Message(); got != "-" {
		t.Errorf("Message(unknown): got %q, want %q", got, "-")
	}
}

// TestResultStringUnknown asserts the String() fallback for an unrecognized
// code is "-" (matching rippled transToken).
func TestResultStringUnknown(t *testing.T) {
	if got := Result(12345).String(); got != "-" {
		t.Errorf("String(unknown): got %q, want %q", got, "-")
	}
	if got := TesSUCCESS.String(); got != "tesSUCCESS" {
		t.Errorf("String(tesSUCCESS): got %q, want %q", got, "tesSUCCESS")
	}
}
