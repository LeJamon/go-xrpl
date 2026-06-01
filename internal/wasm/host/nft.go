package host

import (
	"encoding/binary"

	"github.com/LeJamon/go-xrpl/internal/tx/nftoken"
	"github.com/LeJamon/go-xrpl/internal/wasm"
)

// An NFTokenID is 32 bytes, big-endian:
//
//	[0:2]   flags
//	[2:4]   transfer fee
//	[4:24]  issuer
//	[24:28] taxon (ciphered)
//	[28:32] serial

func nftField16(nftID []byte, off int) (uint16, wasm.HostFunctionError) {
	if len(nftID) != 32 {
		return 0, wasm.HfInvalidParams
	}
	return binary.BigEndian.Uint16(nftID[off : off+2]), wasm.HfSuccess
}

func (e *Env) GetNFTFlags(nftID []byte) (int32, wasm.HostFunctionError) {
	v, herr := nftField16(nftID, 0)
	return int32(v), herr
}

func (e *Env) GetNFTTransferFee(nftID []byte) (int32, wasm.HostFunctionError) {
	v, herr := nftField16(nftID, 2)
	return int32(v), herr
}

func (e *Env) GetNFTSerial(nftID []byte) (uint32, wasm.HostFunctionError) {
	if len(nftID) != 32 {
		return 0, wasm.HfInvalidParams
	}
	return binary.BigEndian.Uint32(nftID[28:32]), wasm.HfSuccess
}

// GetNFTTaxon returns the deciphered taxon. The id stores taxon XOR-mixed with
// the serial; CipheredTaxon is its own inverse.
func (e *Env) GetNFTTaxon(nftID []byte) (uint32, wasm.HostFunctionError) {
	if len(nftID) != 32 {
		return 0, wasm.HfInvalidParams
	}
	serial := binary.BigEndian.Uint32(nftID[28:32])
	ciphered := binary.BigEndian.Uint32(nftID[24:28])
	return nftoken.CipheredTaxon(serial, ciphered), wasm.HfSuccess
}

func (e *Env) GetNFTIssuer(nftID []byte) ([]byte, wasm.HostFunctionError) {
	if len(nftID) != 32 {
		return nil, wasm.HfInvalidParams
	}
	b := make([]byte, 20)
	copy(b, nftID[4:24])
	return b, wasm.HfSuccess
}
