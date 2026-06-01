// Package wasm executes WebAssembly smart-contract bytecode for the XRPL
// SmartEscrow feature, matching rippled's wasmi-based engine.
//
// Consensus parity requires the exact wasmi engine rippled uses: the
// per-instruction fuel model is consensus-critical. The engine links libwasmi
// (upstream wasmi v0.42.1 + the XRPLF fuel-API patch) via cgo; see
// internal/wasm/wasmi/build.sh. With CGO disabled the engine is a stub that
// reports ErrCGODisabled.
package wasm

import "errors"

// ErrCGODisabled is returned by the stub engine, which is built unless both cgo
// is enabled and the `wasmi` build tag is set. WASM execution requires the
// native wasmi library.
var ErrCGODisabled = errors.New("wasm: execution unavailable (build with cgo and -tags wasmi)")

// ErrExecution is returned when WASM execution fails (compile, instantiate,
// trap, or out of gas). It mirrors rippled mapping every such failure to
// tecFAILED_PROCESSING.
var ErrExecution = errors.New("wasm: execution failed")

// Result is the outcome of a successful WASM run: the i32 the entry function
// returned, and the gas (fuel) it consumed.
type Result struct {
	Result int32
	Cost   int64
}

// GasUnlimited, passed as the gas limit, runs with the maximum fuel budget.
// It mirrors rippled's gasLimit == -1 sentinel.
const GasUnlimited int64 = -1

// HostFunctionError mirrors rippled's HostFunctions::HostFunctionError. Host
// functions return one of these (as a negative i32) to the contract on failure.
type HostFunctionError int32

const (
	HfSuccess             HostFunctionError = 0
	HfInternal            HostFunctionError = -1
	HfFieldNotFound       HostFunctionError = -2
	HfBufferTooSmall      HostFunctionError = -3
	HfNoArray             HostFunctionError = -4
	HfNotLeafField        HostFunctionError = -5
	HfLocatorMalformed    HostFunctionError = -6
	HfSlotOutRange        HostFunctionError = -7
	HfSlotsFull           HostFunctionError = -8
	HfEmptySlot           HostFunctionError = -9
	HfLedgerObjNotFound   HostFunctionError = -10
	HfDecoding            HostFunctionError = -11
	HfDataFieldTooLarge   HostFunctionError = -12
	HfPointerOutOfBounds  HostFunctionError = -13
	HfNoMemExported       HostFunctionError = -14
	HfInvalidParams       HostFunctionError = -15
	HfInvalidAccount      HostFunctionError = -16
	HfInvalidField        HostFunctionError = -17
	HfIndexOutOfBounds    HostFunctionError = -18
	HfFloatInputMalformed HostFunctionError = -19
	HfFloatComputeError   HostFunctionError = -20
	HfNoRuntime           HostFunctionError = -21
	HfOutOfGas            HostFunctionError = -22
	HfSubmitTxnFailure    HostFunctionError = -23
	HfInvalidState        HostFunctionError = -24
)

// HostFunctions is the interface a contract's execution context exposes to WASM
// imports. It mirrors rippled's HostFunctions virtual interface and grows as
// more host functions are ported; the Foundation implements the ledger-sequence
// query exercised by the escrow `finish` fixtures.
type HostFunctions interface {
	// GetLedgerSqn returns the sequence of the ledger being built.
	GetLedgerSqn() (int32, HostFunctionError)
}

// hostFnID identifies a host function for import registration and dispatch.
type hostFnID int

const (
	fnGetLedgerSqn hostFnID = iota
)

// Import binds a WASM import name to a host function and its gas cost. The
// caller assembles the ImportVec a contract is allowed to use, mirroring
// rippled's WASM_IMPORT_FUNC registrations.
type Import struct {
	Name string
	Gas  int64
	fn   hostFnID
}

// ImportGetLedgerSqn registers the get_ledger_sqn import (rippled production
// gas: 60).
func ImportGetLedgerSqn(gas int64) Import {
	return Import{Name: "get_ledger_sqn", Gas: gas, fn: fnGetLedgerSqn}
}

// paramKind enumerates the WASM value types a parameter can carry.
type paramKind int

const (
	kindI32 paramKind = iota
	kindI64
)

// Param is an input passed to the entry function. The Foundation supports the
// integer parameter types the escrow fixtures use; byte parameters (marshalled
// through linear memory) arrive with the broader host-function set.
type Param struct {
	kind paramKind
	i32  int32
	i64  int64
}

// I32 builds an i32 entry-function parameter.
func I32(v int32) Param { return Param{kind: kindI32, i32: v} }

// I64 builds an i64 entry-function parameter.
func I64(v int64) Param { return Param{kind: kindI64, i64: v} }
