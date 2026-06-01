//go:build cgo && wasmi

package wasm

import "encoding/binary"

// argKind classifies one logical host-function argument and how it maps onto
// the WASM import's i32/i64 parameters.
type argKind uint8

const (
	argSliceIn   argKind = iota // input bytes: two i32 params (ptr, size)
	argBufferOut                // output buffer: two i32 params (ptr, size)
	argScalarI32                // one i32 param
	argScalarI64                // one i64 param
)

// hostInputs holds a host call's decoded inputs, in logical order per kind.
type hostInputs struct {
	slices [][]byte
	i32s   []uint32
	i64s   []int64
}

func (h hostInputs) slice(i int) []byte { return h.slices[i] }
func (h hostInputs) u32(i int) uint32   { return h.i32s[i] }

// hostResult is what an adapter returns: bytes for the output buffer, or a
// direct i32 value, plus a status.
type hostResult struct {
	data []byte
	val  int32
	err  HostFunctionError
}

func okBytes(b []byte) hostResult          { return hostResult{data: b} }
func hfErr(e HostFunctionError) hostResult { return hostResult{err: e} }

// keyletResult adapts a ([]byte, HostFunctionError) keylet return.
func keyletResult(b []byte, e HostFunctionError) hostResult {
	if e != HfSuccess {
		return hfErr(e)
	}
	return okBytes(b)
}

// hostFn describes one WASM import: its gas cost, the shape of its arguments,
// and an adapter that performs the call against a HostFunctions implementation.
type hostFn struct {
	gas    int64
	args   []argKind
	invoke func(hf HostFunctions, in hostInputs) hostResult
}

func leU32(v uint32) []byte {
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, v)
	return b
}

// Argument shapes shared by many keylets.
var (
	shapeAcct       = []argKind{argSliceIn, argBufferOut}
	shapeAcctSeq    = []argKind{argSliceIn, argScalarI32, argBufferOut}
	shapeTwoAcct    = []argKind{argSliceIn, argSliceIn, argBufferOut}
	shapeThreeSlice = []argKind{argSliceIn, argSliceIn, argSliceIn, argBufferOut}
)

// registry maps each WASM import name to its descriptor. Gas costs match
// rippled's setCommonHostFunctions (smart-escrow WasmVM.cpp).
var registry = map[string]hostFn{
	"get_ledger_sqn": {gas: 60, args: []argKind{argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		v, e := hf.GetLedgerSqn()
		if e != HfSuccess {
			return hfErr(e)
		}
		return okBytes(leU32(v))
	}},

	"account_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.AccountKeylet(in.slice(0)))
	}},
	"amm_keylet": {gas: 450, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.AMMKeylet(in.slice(0), in.slice(1)))
	}},
	"check_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.CheckKeylet(in.slice(0), in.u32(0)))
	}},
	"credential_keylet": {gas: 350, args: shapeThreeSlice, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.CredentialKeylet(in.slice(0), in.slice(1), in.slice(2)))
	}},
	"delegate_keylet": {gas: 350, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.DelegateKeylet(in.slice(0), in.slice(1)))
	}},
	"deposit_preauth_keylet": {gas: 350, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.DepositPreauthKeylet(in.slice(0), in.slice(1)))
	}},
	"did_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.DIDKeylet(in.slice(0)))
	}},
	"escrow_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.EscrowKeylet(in.slice(0), in.u32(0)))
	}},
	"line_keylet": {gas: 400, args: shapeThreeSlice, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.LineKeylet(in.slice(0), in.slice(1), in.slice(2)))
	}},
	"mpt_issuance_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.MPTIssuanceKeylet(in.slice(0), in.u32(0)))
	}},
	"mptoken_keylet": {gas: 500, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.MPTokenKeylet(in.slice(0), in.slice(1)))
	}},
	"nft_offer_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.NFTOfferKeylet(in.slice(0), in.u32(0)))
	}},
	"offer_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.OfferKeylet(in.slice(0), in.u32(0)))
	}},
	"oracle_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.OracleKeylet(in.slice(0), in.u32(0)))
	}},
	"paychan_keylet": {gas: 350, args: []argKind{argSliceIn, argSliceIn, argScalarI32, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.PaychanKeylet(in.slice(0), in.slice(1), in.u32(0)))
	}},
	"permissioned_domain_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.PermissionedDomainKeylet(in.slice(0), in.u32(0)))
	}},
	"signers_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.SignersKeylet(in.slice(0)))
	}},
	"ticket_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.TicketKeylet(in.slice(0), in.u32(0)))
	}},
	"vault_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return keyletResult(hf.VaultKeylet(in.slice(0), in.u32(0)))
	}},
}
