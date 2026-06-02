package escrow

import (
	"encoding/hex"
	"errors"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/ledger/state"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/wasm"
	"github.com/LeJamon/go-xrpl/internal/wasm/host"
	"github.com/LeJamon/go-xrpl/keylet"
)

// escrowFunctionName is the WASM export a SmartEscrow finish function must
// provide. Reference: rippled WasmVM.h escrowFunctionName.
const escrowFunctionName = "finish"

// validateFinishFunctionWasm checks that the FinishFunction WASM compiles and
// exports a finish() -> i32 entry point, mirroring rippled's preflightEscrowWasm
// (run after the size checks at create time). In a build without the wasmi
// engine the module cannot be validated here; the check is skipped and the
// finish-time execution rejects a malformed module instead.
// Reference: rippled EscrowCreate.cpp preflightSigValidated lines 237-254
func validateFinishFunctionWasm(code []byte) tx.Result {
	engine := wasm.New()
	defer engine.Close()
	switch err := engine.Check(code, escrowFunctionName); {
	case err == nil, errors.Is(err, wasm.ErrCGODisabled):
		return tx.TesSUCCESS
	default:
		return tx.TemBAD_WASM
	}
}

// maxWasmDataLength bounds the escrow's mutable Data field, matching rippled's
// Protocol.h (4KB).
const maxWasmDataLength = 4 * 1024

// microDropsPerDrop converts WASM gas cost (priced in micro-drops) to drops.
// Reference: rippled Fees.h microDropsPerDrop.
const microDropsPerDrop = 1_000_000

// escrowFeeSettings reads the FeeSettings ledger entry used by the SmartEscrow
// fee formulas. It returns nil when the entry is unavailable, in which case
// callers fall back to EngineConfig / FeeSetup defaults.
func escrowFeeSettings(view tx.LedgerView) *state.FeeSettings {
	if view == nil {
		return nil
	}
	data, err := view.Read(keylet.Fees())
	if err != nil || data == nil {
		return nil
	}
	fs, err := state.ParseFeeSettings(data)
	if err != nil {
		return nil
	}
	return fs
}

// vlByteLen returns the decoded byte length of a hex-encoded VL field, matching
// rippled's tx[sfField].size() (the blob's byte count, not the hex length).
func vlByteLen(hexStr string) uint64 {
	if decoded, err := hex.DecodeString(hexStr); err == nil {
		return uint64(len(decoded))
	}
	return uint64(len(hexStr) / 2)
}

// calculateAdditionalReserve returns the number of owner-reserve slots an escrow
// consumes: a base of 1, plus 1 for each additional 500 bytes of FinishFunction
// code beyond the first 500. finishFunctionBytes is the decoded FinishFunction
// length (0 for a plain escrow with no finish function).
// Reference: rippled-smart-escrow EscrowHelpers.h:232-239
func calculateAdditionalReserve(finishFunctionBytes int) uint32 {
	return 1 + uint32(finishFunctionBytes/500)
}

// escrowDataReserve returns the owner-reserve slots held by a serialized escrow
// ledger object, derived from the byte length of its FinishFunction.
func escrowDataReserve(escrowData []byte) uint32 {
	bytes := 0
	if ffHex, ok := escrowFinishFunctionHex(escrowData); ok {
		bytes = len(ffHex) / 2
	}
	return calculateAdditionalReserve(bytes)
}

// smartEscrowFinishPreclaim validates the FinishFunction/ComputationAllowance
// pairing for an EscrowFinish. It runs in the preclaim portion of Apply, before
// the doApply-stage condition and WASM checks, so a field mismatch is reported
// even when the fulfillment is also wrong. escrowData is the serialized escrow
// ledger object being finished.
// Reference: rippled EscrowFinish.cpp preclaim lines 260-278
func smartEscrowFinishPreclaim(ctx *tx.ApplyContext, e *EscrowFinish, escrowData []byte) tx.Result {
	if !ctx.Rules().Enabled(amendment.FeatureSmartEscrow) {
		return tx.TesSUCCESS
	}
	_, hasFF := escrowFinishFunctionHex(escrowData)
	hasCA := e.ComputationAllowance != nil
	switch {
	case hasFF && !hasCA:
		return tx.TefWASM_FIELD_NOT_INCLUDED
	case !hasFF && hasCA:
		return tx.TefNO_WASM
	}
	return tx.TesSUCCESS
}

// runSmartEscrow executes the escrow's FinishFunction, mirroring the WASM block
// of rippled's EscrowFinish::doApply. It assumes smartEscrowFinishPreclaim has
// already validated field presence. It returns TesSUCCESS when SmartEscrow is
// disabled or the escrow has no finish function, tecWASM_REJECTED when the
// function returns a non-positive result, or a tec/tef on execution failure.
//
// The engine is reached through internal/wasm, a stub unless built with
// -tags wasmi; in a stub build engine.Run reports ErrCGODisabled, mapped here
// to tecFAILED_PROCESSING (only reachable once SmartEscrow is enabled).
// Reference: rippled EscrowFinish.cpp doApply lines 406-457
func runSmartEscrow(ctx *tx.ApplyContext, e *EscrowFinish, escrowData []byte) tx.Result {
	if !ctx.Rules().Enabled(amendment.FeatureSmartEscrow) {
		return tx.TesSUCCESS
	}
	ffHex, hasFF := escrowFinishFunctionHex(escrowData)
	if !hasFF {
		return tx.TesSUCCESS
	}
	if e.ComputationAllowance == nil {
		// Already validated in smartEscrowFinishPreclaim; defensive, matching
		// rippled's "just in case" tecINTERNAL.
		return tx.TecINTERNAL
	}

	wasmBytes, err := hex.DecodeString(ffHex)
	if err != nil {
		return tx.TefINTERNAL
	}

	view := &escrowView{ctx: ctx, txBytes: e.GetRawBytes(), escrowBytes: escrowData}
	engine := wasm.New()
	defer engine.Close()
	res, runErr := engine.Run(wasmBytes, "finish", nil, host.New(view), int64(*e.ComputationAllowance))
	if runErr != nil {
		ctx.Log.Debug("escrow finish: wasm execution failed", "error", runErr)
		return tx.TecFAILED_PROCESSING
	}
	ctx.Log.Debug("escrow finish: wasm ran", "result", res.Result, "cost", res.Cost)
	if res.Result <= 0 {
		return tx.TecWASM_REJECTED
	}
	return tx.TesSUCCESS
}

// escrowFinishFunctionHex extracts the FinishFunction (hex string) from a
// serialized escrow ledger object.
func escrowFinishFunctionHex(escrowData []byte) (string, bool) {
	m, err := binarycodec.DecodeBytes(escrowData)
	if err != nil {
		return "", false
	}
	ff, ok := m["FinishFunction"].(string)
	if !ok || ff == "" {
		return "", false
	}
	return ff, true
}
