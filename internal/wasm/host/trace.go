package host

import "github.com/LeJamon/go-xrpl/internal/wasm"

// The trace family writes to rippled's journal for debugging and has no ledger
// effect; each returns 0. goXRPL accepts and discards the arguments, preserving
// the contract ABI and the (deterministic) return value and gas charge.

func (e *Env) Trace(msg, data []byte, asHex bool) (int32, wasm.HostFunctionError) {
	return 0, wasm.HfSuccess
}

func (e *Env) TraceNum(msg []byte, num int64) (int32, wasm.HostFunctionError) {
	return 0, wasm.HfSuccess
}

func (e *Env) TraceAccount(msg, account []byte) (int32, wasm.HostFunctionError) {
	return 0, wasm.HfSuccess
}

func (e *Env) TraceFloat(msg, value []byte) (int32, wasm.HostFunctionError) {
	return 0, wasm.HfSuccess
}

func (e *Env) TraceAmount(msg, amount []byte) (int32, wasm.HostFunctionError) {
	return 0, wasm.HfSuccess
}
