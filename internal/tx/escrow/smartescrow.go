package escrow

import (
	"encoding/hex"

	"github.com/LeJamon/go-xrpl/amendment"
	"github.com/LeJamon/go-xrpl/codec/binarycodec"
	"github.com/LeJamon/go-xrpl/internal/tx"
	"github.com/LeJamon/go-xrpl/internal/wasm"
	"github.com/LeJamon/go-xrpl/internal/wasm/host"
)

// maxWasmDataLength bounds the escrow's mutable Data field, matching rippled's
// Protocol.h (4KB).
const maxWasmDataLength = 4 * 1024

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
