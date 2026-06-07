package definitions

import "testing"

// TestSmartEscrowTransactionResults pins the SmartEscrow TER codes in
// TRANSACTION_RESULTS so the metadata codec can round-trip them. The escrow
// transactors emit these names via Result.String(); a missing entry breaks
// SerializeMetadata for an applied tecWASM_REJECTED finish.
// Reference: rippled-smart-escrow include/xrpl/protocol/TER.h.
func TestSmartEscrowTransactionResults(t *testing.T) {
	defs := Get()
	want := map[string]int32{
		"tecWASM_REJECTED":           198,
		"tefNO_WASM":                 -177,
		"tefWASM_FIELD_NOT_INCLUDED": -176,
		"temBAD_WASM":                -248,
		"temTEMP_DISABLED":           -247,
		"terNO_DELEGATE_PERMISSION":  -85,
	}
	for name, code := range want {
		got, err := defs.GetTransactionResultTypeCodeByTransactionResultName(name)
		if err != nil {
			t.Errorf("%s: unexpected error %v", name, err)
			continue
		}
		if got != code {
			t.Errorf("%s = %d, want %d", name, got, code)
		}
		rname, rerr := defs.GetTransactionResultNameByTransactionResultTypeCode(code)
		if rerr != nil || rname != name {
			t.Errorf("reverse %d = %q (err %v), want %q", code, rname, rerr, name)
		}
	}

	// 198 was tecNO_DELEGATE_PERMISSION before SmartEscrow; it is now
	// tecWASM_REJECTED, and the old name must no longer resolve (the Go side
	// carries NO_DELEGATE_PERMISSION as terNO_DELEGATE_PERMISSION at -85).
	if _, err := defs.GetTransactionResultTypeCodeByTransactionResultName("tecNO_DELEGATE_PERMISSION"); err == nil {
		t.Error("tecNO_DELEGATE_PERMISSION should no longer resolve (repurposed to tecWASM_REJECTED at 198)")
	}
}
