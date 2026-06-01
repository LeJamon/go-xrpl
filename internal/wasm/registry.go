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
func (h hostInputs) i64(i int) int64    { return h.i64s[i] }

// hostResult is what an adapter returns: bytes for the output buffer, or a
// direct i32 value, plus a status.
type hostResult struct {
	data []byte
	val  int32
	err  HostFunctionError
}

func okBytes(b []byte) hostResult          { return hostResult{data: b} }
func okVal(v int32) hostResult             { return hostResult{val: v} }
func hfErr(e HostFunctionError) hostResult { return hostResult{err: e} }

// bytesResult adapts a ([]byte, HostFunctionError) return whose bytes go to the
// output buffer.
func bytesResult(b []byte, e HostFunctionError) hostResult {
	if e != HfSuccess {
		return hfErr(e)
	}
	return okBytes(b)
}

// u32Result writes a uint32 little-endian to the output buffer.
func u32Result(v uint32, e HostFunctionError) hostResult {
	if e != HfSuccess {
		return hfErr(e)
	}
	return okBytes(leU32(v))
}

// valResult returns a direct i32 (functions with no output buffer).
func valResult(v int32, e HostFunctionError) hostResult {
	if e != HfSuccess {
		return hfErr(e)
	}
	return okVal(v)
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
		return u32Result(hf.GetLedgerSqn())
	}},
	"get_parent_ledger_time": {gas: 60, args: []argKind{argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return u32Result(hf.GetParentLedgerTime())
	}},
	"get_parent_ledger_hash": {gas: 60, args: []argKind{argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetParentLedgerHash())
	}},
	"get_base_fee": {gas: 60, args: []argKind{argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return u32Result(hf.GetBaseFee())
	}},
	"amendment_enabled": {gas: 100, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.IsAmendmentEnabled(in.slice(0)))
	}},

	"compute_sha512_half": {gas: 1500, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.ComputeSha512Half(in.slice(0)))
	}},
	"check_sig": {gas: 35000, args: []argKind{argSliceIn, argSliceIn, argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.CheckSignature(in.slice(0), in.slice(1), in.slice(2)))
	}},
	"update_data": {gas: 1000, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.UpdateData(in.slice(0)))
	}},

	"trace": {gas: 500, args: []argKind{argSliceIn, argSliceIn, argScalarI32}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.Trace(in.slice(0), in.slice(1), in.u32(0) != 0))
	}},
	"trace_num": {gas: 500, args: []argKind{argSliceIn, argScalarI64}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.TraceNum(in.slice(0), in.i64(0)))
	}},
	"trace_account": {gas: 500, args: []argKind{argSliceIn, argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.TraceAccount(in.slice(0), in.slice(1)))
	}},
	"trace_opaque_float": {gas: 500, args: []argKind{argSliceIn, argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.TraceFloat(in.slice(0), in.slice(1)))
	}},
	"trace_amount": {gas: 500, args: []argKind{argSliceIn, argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.TraceAmount(in.slice(0), in.slice(1)))
	}},

	"get_nft_flags": {gas: 60, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetNFTFlags(in.slice(0)))
	}},
	"get_nft_transfer_fee": {gas: 60, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetNFTTransferFee(in.slice(0)))
	}},
	"get_nft_taxon": {gas: 60, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return u32Result(hf.GetNFTTaxon(in.slice(0)))
	}},
	"get_nft_serial": {gas: 60, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return u32Result(hf.GetNFTSerial(in.slice(0)))
	}},
	"get_nft_issuer": {gas: 70, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetNFTIssuer(in.slice(0)))
	}},
	"get_nft": {gas: 5000, args: []argKind{argSliceIn, argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetNFT(in.slice(0), in.slice(1)))
	}},

	"cache_ledger_obj": {gas: 5000, args: []argKind{argSliceIn, argScalarI32}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.CacheLedgerObj(in.slice(0), int32(in.u32(0))))
	}},
	"get_tx_field": {gas: 70, args: []argKind{argScalarI32, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetTxField(int32(in.u32(0))))
	}},
	"get_current_ledger_obj_field": {gas: 70, args: []argKind{argScalarI32, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetCurrentLedgerObjField(int32(in.u32(0))))
	}},
	"get_ledger_obj_field": {gas: 70, args: []argKind{argScalarI32, argScalarI32, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetLedgerObjField(int32(in.u32(0)), int32(in.u32(1))))
	}},
	"get_tx_array_len": {gas: 40, args: []argKind{argScalarI32}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetTxArrayLen(int32(in.u32(0))))
	}},
	"get_current_ledger_obj_array_len": {gas: 40, args: []argKind{argScalarI32}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetCurrentLedgerObjArrayLen(int32(in.u32(0))))
	}},
	"get_ledger_obj_array_len": {gas: 40, args: []argKind{argScalarI32, argScalarI32}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetLedgerObjArrayLen(int32(in.u32(0)), int32(in.u32(1))))
	}},
	"get_tx_nested_field": {gas: 110, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetTxNestedField(in.slice(0)))
	}},
	"get_current_ledger_obj_nested_field": {gas: 110, args: []argKind{argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetCurrentLedgerObjNestedField(in.slice(0)))
	}},
	"get_ledger_obj_nested_field": {gas: 110, args: []argKind{argScalarI32, argSliceIn, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.GetLedgerObjNestedField(int32(in.u32(0)), in.slice(0)))
	}},
	"get_tx_nested_array_len": {gas: 70, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetTxNestedArrayLen(in.slice(0)))
	}},
	"get_current_ledger_obj_nested_array_len": {gas: 70, args: []argKind{argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetCurrentLedgerObjNestedArrayLen(in.slice(0)))
	}},
	"get_ledger_obj_nested_array_len": {gas: 70, args: []argKind{argScalarI32, argSliceIn}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return valResult(hf.GetLedgerObjNestedArrayLen(int32(in.u32(0)), in.slice(0)))
	}},

	"account_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.AccountKeylet(in.slice(0)))
	}},
	"amm_keylet": {gas: 450, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.AMMKeylet(in.slice(0), in.slice(1)))
	}},
	"check_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.CheckKeylet(in.slice(0), in.u32(0)))
	}},
	"credential_keylet": {gas: 350, args: shapeThreeSlice, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.CredentialKeylet(in.slice(0), in.slice(1), in.slice(2)))
	}},
	"delegate_keylet": {gas: 350, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.DelegateKeylet(in.slice(0), in.slice(1)))
	}},
	"deposit_preauth_keylet": {gas: 350, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.DepositPreauthKeylet(in.slice(0), in.slice(1)))
	}},
	"did_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.DIDKeylet(in.slice(0)))
	}},
	"escrow_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.EscrowKeylet(in.slice(0), in.u32(0)))
	}},
	"line_keylet": {gas: 400, args: shapeThreeSlice, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.LineKeylet(in.slice(0), in.slice(1), in.slice(2)))
	}},
	"mpt_issuance_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.MPTIssuanceKeylet(in.slice(0), in.u32(0)))
	}},
	"mptoken_keylet": {gas: 500, args: shapeTwoAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.MPTokenKeylet(in.slice(0), in.slice(1)))
	}},
	"nft_offer_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.NFTOfferKeylet(in.slice(0), in.u32(0)))
	}},
	"offer_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.OfferKeylet(in.slice(0), in.u32(0)))
	}},
	"oracle_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.OracleKeylet(in.slice(0), in.u32(0)))
	}},
	"paychan_keylet": {gas: 350, args: []argKind{argSliceIn, argSliceIn, argScalarI32, argBufferOut}, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.PaychanKeylet(in.slice(0), in.slice(1), in.u32(0)))
	}},
	"permissioned_domain_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.PermissionedDomainKeylet(in.slice(0), in.u32(0)))
	}},
	"signers_keylet": {gas: 350, args: shapeAcct, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.SignersKeylet(in.slice(0)))
	}},
	"ticket_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.TicketKeylet(in.slice(0), in.u32(0)))
	}},
	"vault_keylet": {gas: 350, args: shapeAcctSeq, invoke: func(hf HostFunctions, in hostInputs) hostResult {
		return bytesResult(hf.VaultKeylet(in.slice(0), in.u32(0)))
	}},
}
