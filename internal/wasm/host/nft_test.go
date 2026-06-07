package host

import (
	"bytes"
	"testing"

	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// TestNFTFields builds a real NFTokenID with goXRPL's generator and checks each
// accessor recovers the original component.
func TestNFTFields(t *testing.T) {
	e := New(nil)
	var issuer [20]byte
	for i := range issuer {
		issuer[i] = byte(i + 1)
	}
	const (
		taxon  uint32 = 0xDEADBEEF
		serial uint32 = 42
		flags  uint16 = nftoken.NFTokenFlagTransferable
		fee    uint16 = 314
	)
	id := nftoken.GenerateNFTokenID(issuer, taxon, serial, flags, fee)

	if got, herr := e.GetNFTFlags(id[:]); herr != wasm.HfSuccess || got != int32(flags) {
		t.Errorf("GetNFTFlags = %d, %d, want %d", got, herr, flags)
	}
	if got, herr := e.GetNFTTransferFee(id[:]); herr != wasm.HfSuccess || got != int32(fee) {
		t.Errorf("GetNFTTransferFee = %d, %d, want %d", got, herr, fee)
	}
	if got, herr := e.GetNFTSerial(id[:]); herr != wasm.HfSuccess || got != serial {
		t.Errorf("GetNFTSerial = %d, %d, want %d", got, herr, serial)
	}
	if got, herr := e.GetNFTTaxon(id[:]); herr != wasm.HfSuccess || got != taxon {
		t.Errorf("GetNFTTaxon = %d, %d, want %d", got, herr, taxon)
	}
	if got, herr := e.GetNFTIssuer(id[:]); herr != wasm.HfSuccess || !bytes.Equal(got, issuer[:]) {
		t.Errorf("GetNFTIssuer = %x, %d, want %x", got, herr, issuer)
	}
}

func TestNFTRejectsBadLength(t *testing.T) {
	e := New(nil)
	if _, herr := e.GetNFTFlags([]byte{1, 2, 3}); herr != wasm.HfInvalidParams {
		t.Errorf("short id flags herr = %d, want HfInvalidParams", herr)
	}
	if _, herr := e.GetNFTTaxon(make([]byte, 31)); herr != wasm.HfInvalidParams {
		t.Errorf("short id taxon herr = %d, want HfInvalidParams", herr)
	}
}
