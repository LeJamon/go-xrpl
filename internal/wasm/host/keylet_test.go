package host

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/wasm"
	"github.com/LeJamon/go-xrpl/keylet"
)

func acct20(seed byte) [20]byte {
	var a [20]byte
	for i := range a {
		a[i] = seed + byte(i)
	}
	return a
}

// TestKeyletsMatchKeyletPackage verifies every keylet host function produces the
// same 32-byte index as a direct call into the keylet package — proving the
// argument parsing and wiring, with the keylet package as the correctness
// oracle.
func TestKeyletsMatchKeyletPackage(t *testing.T) {
	e := New(nil)
	a := acct20(1)
	b := acct20(40)
	cur := make([]byte, 20)
	cur[12], cur[13], cur[14] = 'U', 'S', 'D'
	var mptid [24]byte
	for i := range mptid {
		mptid[i] = byte(i + 3)
	}
	cred := []byte("my_credential")

	// A bare 20-byte asset must be native XRP (rippled's getDataAsset); an IOU
	// is a 40-byte currency+issuer pair.
	asset1 := make([]byte, 20)                          // XRP
	asset2 := append(append([]byte{}, cur...), b[:]...) // currency+issuer

	tests := []struct {
		name string
		got  func() ([]byte, wasm.HostFunctionError)
		want keylet.Keylet
	}{
		{"account", func() ([]byte, wasm.HostFunctionError) { return e.AccountKeylet(a[:]) }, keylet.Account(a)},
		{"check", func() ([]byte, wasm.HostFunctionError) { return e.CheckKeylet(a[:], 7) }, keylet.Check(a, 7)},
		{"did", func() ([]byte, wasm.HostFunctionError) { return e.DIDKeylet(a[:]) }, keylet.DID(a)},
		{"escrow", func() ([]byte, wasm.HostFunctionError) { return e.EscrowKeylet(a[:], 9) }, keylet.Escrow(a, 9)},
		{"nft_offer", func() ([]byte, wasm.HostFunctionError) { return e.NFTOfferKeylet(a[:], 11) }, keylet.NFTokenOffer(a, 11)},
		{"offer", func() ([]byte, wasm.HostFunctionError) { return e.OfferKeylet(a[:], 13) }, keylet.Offer(a, 13)},
		{"oracle", func() ([]byte, wasm.HostFunctionError) { return e.OracleKeylet(a[:], 17) }, keylet.Oracle(a, 17)},
		{"permissioned_domain", func() ([]byte, wasm.HostFunctionError) { return e.PermissionedDomainKeylet(a[:], 19) }, keylet.PermissionedDomain(a, 19)},
		{"signers", func() ([]byte, wasm.HostFunctionError) { return e.SignersKeylet(a[:]) }, keylet.SignerList(a)},
		{"ticket", func() ([]byte, wasm.HostFunctionError) { return e.TicketKeylet(a[:], 23) }, keylet.Ticket(a, 23)},
		{"vault", func() ([]byte, wasm.HostFunctionError) { return e.VaultKeylet(a[:], 29) }, keylet.Vault(a, 29)},
		{"delegate", func() ([]byte, wasm.HostFunctionError) { return e.DelegateKeylet(a[:], b[:]) }, keylet.DelegateKeylet(a, b)},
		{"deposit_preauth", func() ([]byte, wasm.HostFunctionError) { return e.DepositPreauthKeylet(a[:], b[:]) }, keylet.DepositPreauth(a, b)},
		{"paychan", func() ([]byte, wasm.HostFunctionError) { return e.PaychanKeylet(a[:], b[:], 31) }, keylet.PayChannel(a, b, 31)},
		{"credential", func() ([]byte, wasm.HostFunctionError) { return e.CredentialKeylet(a[:], b[:], cred) }, keylet.Credential(a, b, cred)},
		{"mpt_issuance", func() ([]byte, wasm.HostFunctionError) { return e.MPTIssuanceKeylet(a[:], 37) }, keylet.MPTIssuanceBySeq(37, a)},
		{"mptoken", func() ([]byte, wasm.HostFunctionError) { return e.MPTokenKeylet(mptid[:], b[:]) }, keylet.MPTokenByID(mptid, b)},
		{"line", func() ([]byte, wasm.HostFunctionError) { return e.LineKeylet(a[:], b[:], cur) }, keylet.Line(a, b, hex.EncodeToString(cur))},
		{"amm", func() ([]byte, wasm.HostFunctionError) { return e.AMMKeylet(asset1, asset2) }, keylet.AMM([20]byte{}, [20]byte{}, b, asArr(cur))},
	}

	for _, tt := range tests {
		got, herr := tt.got()
		if herr != wasm.HfSuccess {
			t.Errorf("%s: unexpected error %d", tt.name, herr)
			continue
		}
		if !bytes.Equal(got, tt.want.Key[:]) {
			t.Errorf("%s: got %x, want %x", tt.name, got, tt.want.Key[:])
		}
	}
}

func asArr(b []byte) [20]byte {
	var a [20]byte
	copy(a[:], b)
	return a
}

func TestKeyletRejectsBadLength(t *testing.T) {
	e := New(nil)
	// A wrong-length account is rejected at the marshalling boundary with
	// HfInvalidParams, matching rippled's getDataAccountID.
	if _, herr := e.AccountKeylet([]byte{1, 2, 3}); herr != wasm.HfInvalidParams {
		t.Errorf("short account: got %d, want HfInvalidParams", herr)
	}
	holder := acct20(1)
	if _, herr := e.MPTokenKeylet([]byte{1, 2, 3}, holder[:]); herr != wasm.HfInvalidParams {
		t.Errorf("short mptid: got %d, want HfInvalidParams", herr)
	}
	if _, herr := e.AMMKeylet([]byte{1, 2, 3}, []byte{4}); herr != wasm.HfInvalidParams {
		t.Errorf("bad asset: got %d, want HfInvalidParams", herr)
	}
}

// TestKeyletRejectsInvalidValues covers the value-level guards rippled enforces
// in HostFuncImplKeylet: the all-zero account, equal account pairs, zero
// currency, credential-type bounds, and non-native AMM assets.
func TestKeyletRejectsInvalidValues(t *testing.T) {
	e := New(nil)
	zero := make([]byte, 20)
	a := acct20(1)
	b := acct20(2)

	if _, herr := e.AccountKeylet(zero); herr != wasm.HfInvalidAccount {
		t.Errorf("zero account: got %d, want HfInvalidAccount", herr)
	}
	if _, herr := e.DelegateKeylet(a[:], a[:]); herr != wasm.HfInvalidParams {
		t.Errorf("equal delegate pair: got %d, want HfInvalidParams", herr)
	}
	if _, herr := e.PaychanKeylet(a[:], a[:], 1); herr != wasm.HfInvalidParams {
		t.Errorf("equal paychan pair: got %d, want HfInvalidParams", herr)
	}
	if _, herr := e.LineKeylet(a[:], a[:], make([]byte, 20)); herr != wasm.HfInvalidParams {
		t.Errorf("equal line accounts: got %d, want HfInvalidParams", herr)
	}
	cur := make([]byte, 20)
	cur[19] = 1
	if _, herr := e.LineKeylet(a[:], b[:], make([]byte, 20)); herr != wasm.HfInvalidParams {
		t.Errorf("zero currency: got %d, want HfInvalidParams", herr)
	}
	if _, herr := e.CredentialKeylet(a[:], b[:], nil); herr != wasm.HfInvalidParams {
		t.Errorf("empty credential type: got %d, want HfInvalidParams", herr)
	}
	if _, herr := e.CredentialKeylet(a[:], b[:], make([]byte, 65)); herr != wasm.HfInvalidParams {
		t.Errorf("oversized credential type: got %d, want HfInvalidParams", herr)
	}
	// A bare 20-byte currency must be native (XRP); a non-zero one is rejected.
	if _, herr := e.AMMKeylet(cur, make([]byte, 40)); herr != wasm.HfInvalidParams {
		t.Errorf("bare non-native currency: got %d, want HfInvalidParams", herr)
	}
	// Two identical assets are rejected.
	if _, herr := e.AMMKeylet(make([]byte, 20), make([]byte, 20)); herr != wasm.HfInvalidParams {
		t.Errorf("equal assets: got %d, want HfInvalidParams", herr)
	}
}
